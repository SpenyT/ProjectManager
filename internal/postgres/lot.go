package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// LotRepo implements domain.LotRepository against Postgres.
type LotRepo struct {
	pool *pgxpool.Pool
}

// NewLotRepo returns a LotRepo backed by pool.
func NewLotRepo(pool *pgxpool.Pool) *LotRepo {
	return &LotRepo{pool: pool}
}

var _ domain.LotRepository = (*LotRepo)(nil)

const (
	// lotCols is used in RETURNING clauses where no table alias is needed.
	lotCols = `id, canonical_part_id, source_order_line_id,
		qty_received, remaining_qty, unit_cost::text, currency, received_at, created_at, updated_at`
	// lotColsQ is used in SELECT … FROM lot l JOIN … queries to avoid ambiguous column references.
	lotColsQ = `l.id, l.canonical_part_id, l.source_order_line_id,
		l.qty_received, l.remaining_qty, l.unit_cost::text, l.currency, l.received_at, l.created_at, l.updated_at`
)

func (r *LotRepo) GetByID(ctx context.Context, sc domain.ScopeCtx, id int64) (*domain.Lot, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+lotColsQ+`
		FROM lot l
		JOIN order_line ol ON ol.id = l.source_order_line_id
		JOIN purchase_order po ON po.id = ol.order_id
		JOIN inventory_scope s ON s.id = po.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE l.id = $1 AND po.scope_id = $2 AND tm.user_id = $3`,
		id, sc.ScopeID, sc.UserID)
	return scanLot(row)
}

// ExplodeOrderLine creates lots from a delivered order line inside a single transaction.
// Non-kit: one lot for the full qty with unit_cost from the line.
// Kit: N lots (one per KitContent), unit_cost = NULL, qty = line.Qty × content.QtyPerUnit.
// All inserts are atomic: if any lot fails, the entire explosion is rolled back.
func (r *LotRepo) ExplodeOrderLine(
	ctx context.Context,
	sc domain.ScopeCtx,
	line *domain.OrderLine,
	offering *domain.Offering,
	kitContents []*domain.KitContent,
) ([]*domain.Lot, error) {
	var lots []*domain.Lot
	err := pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		var err error
		if offering.IsKit {
			lots, err = explodeKit(ctx, tx, sc, line, kitContents)
		} else {
			lots, err = explodeSingle(ctx, tx, sc, line)
		}
		return err
	})
	return lots, mapErr(err)
}

// explodeSingle creates one lot for a non-kit order line. Must run inside a transaction.
func explodeSingle(ctx context.Context, tx pgx.Tx, sc domain.ScopeCtx, line *domain.OrderLine) ([]*domain.Lot, error) {
	if line.OfferingID == uuid.Nil {
		return nil, fmt.Errorf("explodeSingle: line.OfferingID is zero")
	}
	// Resolve canonical_part_id inside the transaction to avoid TOCTOU on the offering.
	var canonicalPartID uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT o.canonical_part_id
		FROM offering o WHERE o.id = $1 AND o.canonical_part_id IS NOT NULL`,
		line.OfferingID).Scan(&canonicalPartID)
	if err != nil {
		return nil, fmt.Errorf("explodeSingle: resolve canonical part: %w", mapErr(err))
	}

	row := tx.QueryRow(ctx, `
		WITH auth AS (
			SELECT 1 FROM order_line ol
			JOIN purchase_order po ON po.id = ol.order_id
			JOIN inventory_scope s ON s.id = po.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE ol.id = $1 AND po.scope_id = $2 AND tm.user_id = $3
		)
		INSERT INTO lot (canonical_part_id, source_order_line_id, qty_received, remaining_qty, unit_cost, currency)
		SELECT $4, $1, $5, $5, $6::numeric, $7 FROM auth
		RETURNING `+lotCols,
		line.ID, sc.ScopeID, sc.UserID,
		canonicalPartID, line.Qty, string(line.UnitPrice), line.Currency)

	lot, err := scanLot(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return []*domain.Lot{lot}, nil
}

// explodeKit creates N lots, one per KitContent. Must run inside a transaction.
// All lots share the same source_order_line_id; unit_cost is NULL for each.
func explodeKit(ctx context.Context, tx pgx.Tx, sc domain.ScopeCtx, line *domain.OrderLine, contents []*domain.KitContent) ([]*domain.Lot, error) {
	if len(contents) == 0 {
		return nil, fmt.Errorf("explodeKit: no kit contents provided")
	}
	// Verify scope access once before inserting any lots.
	var scopeOK bool
	err := tx.QueryRow(ctx, `
		SELECT true FROM order_line ol
		JOIN purchase_order po ON po.id = ol.order_id
		JOIN inventory_scope s ON s.id = po.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE ol.id = $1 AND po.scope_id = $2 AND tm.user_id = $3`,
		line.ID, sc.ScopeID, sc.UserID).Scan(&scopeOK)
	if err != nil {
		return nil, mapErr(err)
	}

	lots := make([]*domain.Lot, 0, len(contents))
	for _, kc := range contents {
		qty := line.Qty * kc.QtyPerUnit
		row := tx.QueryRow(ctx, `
			INSERT INTO lot (canonical_part_id, source_order_line_id, qty_received, remaining_qty, unit_cost, currency)
			VALUES ($1, $2, $3, $3, NULL, $4)
			RETURNING `+lotCols,
			kc.CanonicalPartID, line.ID, qty, line.Currency)
		lot, err := scanLot(row)
		if err != nil {
			return nil, fmt.Errorf("explodeKit: insert lot for part %s: %w", kc.CanonicalPartID, mapErr(err))
		}
		lots = append(lots, lot)
	}
	return lots, nil
}

func (r *LotRepo) ListByPart(ctx context.Context, sc domain.ScopeCtx, partID uuid.UUID) ([]*domain.Lot, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+lotColsQ+`
		FROM lot l
		JOIN order_line ol ON ol.id = l.source_order_line_id
		JOIN purchase_order po ON po.id = ol.order_id
		JOIN inventory_scope s ON s.id = po.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE l.canonical_part_id = $1 AND po.scope_id = $2 AND tm.user_id = $3
		ORDER BY l.created_at`,
		partID, sc.ScopeID, sc.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var lots []*domain.Lot
	for rows.Next() {
		lot, err := scanLot(rows)
		if err != nil {
			return nil, err
		}
		lots = append(lots, lot)
	}
	return lots, rows.Err()
}

func (r *LotRepo) GetAvailability(ctx context.Context, sc domain.ScopeCtx, partID uuid.UUID) (*domain.Availability, error) {
	var av domain.Availability
	err := r.pool.QueryRow(ctx, `
		SELECT ia.scope_id, ia.canonical_part_id, ia.on_hand, ia.available
		FROM inventory_available ia
		JOIN inventory_scope s ON s.id = ia.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE ia.scope_id = $1 AND ia.canonical_part_id = $2 AND tm.user_id = $3`,
		sc.ScopeID, partID, sc.UserID).Scan(&av.ScopeID, &av.CanonicalPartID, &av.OnHand, &av.Available)
	if err != nil {
		return nil, mapErr(err)
	}
	return &av, nil
}

func scanLot(row pgx.Row) (*domain.Lot, error) {
	var (
		l       domain.Lot
		costStr *string
	)
	if err := row.Scan(
		&l.ID, &l.CanonicalPartID, &l.SourceOrderLineID,
		&l.QtyReceived, &l.RemainingQty, &costStr, &l.Currency,
		&l.ReceivedAt, &l.CreatedAt, &l.UpdatedAt,
	); err != nil {
		return nil, mapErr(err)
	}
	l.UnitCost = toNumericPtr(costStr)
	return &l, nil
}
