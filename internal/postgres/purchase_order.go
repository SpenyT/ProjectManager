package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// PurchaseOrderRepo implements domain.PurchaseOrderRepository against Postgres.
type PurchaseOrderRepo struct {
	pool *pgxpool.Pool
}

// NewPurchaseOrderRepo returns a PurchaseOrderRepo backed by pool.
func NewPurchaseOrderRepo(pool *pgxpool.Pool) *PurchaseOrderRepo {
	return &PurchaseOrderRepo{pool: pool}
}

var _ domain.PurchaseOrderRepository = (*PurchaseOrderRepo)(nil)

const poCols = `id, scope_id, source, source_order_id, status, placed_at, created_at, updated_at`

func (r *PurchaseOrderRepo) GetByID(ctx context.Context, sc domain.ScopeCtx, id int64) (*domain.PurchaseOrder, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT po.`+poCols+`
		FROM purchase_order po
		JOIN inventory_scope s ON s.id = po.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE po.id = $1 AND po.scope_id = $2 AND tm.user_id = $3`,
		id, sc.ScopeID, sc.UserID)
	return scanPO(row)
}

func (r *PurchaseOrderRepo) Upsert(ctx context.Context, sc domain.ScopeCtx, po *domain.PurchaseOrder) (*domain.PurchaseOrder, error) {
	// Auth via CTE: if user is not in the scope's team, INSERT produces 0 rows → ErrNotFound.
	row := r.pool.QueryRow(ctx, `
		WITH auth AS (
			SELECT 1 FROM inventory_scope s
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE s.id = $1 AND tm.user_id = $2
		)
		INSERT INTO purchase_order (scope_id, source, source_order_id, status, placed_at)
		SELECT $1, $3, $4, $5, $6 FROM auth
		ON CONFLICT (scope_id, source, source_order_id) DO UPDATE SET
			status    = EXCLUDED.status,
			placed_at = COALESCE(EXCLUDED.placed_at, purchase_order.placed_at),
			updated_at = now()
		RETURNING `+poCols,
		sc.ScopeID, sc.UserID, po.Source, po.SourceOrderID, po.Status, po.PlacedAt)
	out, err := scanPO(row)
	return out, mapErr(err)
}

func (r *PurchaseOrderRepo) List(ctx context.Context, sc domain.ScopeCtx) ([]*domain.PurchaseOrder, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT po.`+poCols+`
		FROM purchase_order po
		JOIN inventory_scope s ON s.id = po.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE po.scope_id = $1 AND tm.user_id = $2
		ORDER BY po.created_at DESC`,
		sc.ScopeID, sc.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var orders []*domain.PurchaseOrder
	for rows.Next() {
		po, err := scanPO(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, po)
	}
	return orders, rows.Err()
}

func (r *PurchaseOrderRepo) AddLine(ctx context.Context, sc domain.ScopeCtx, line *domain.OrderLine) (*domain.OrderLine, error) {
	// Verify the order belongs to the caller's scope via CTE.
	row := r.pool.QueryRow(ctx, `
		WITH auth AS (
			SELECT 1 FROM purchase_order po
			JOIN inventory_scope s ON s.id = po.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE po.id = $1 AND po.scope_id = $2 AND tm.user_id = $3
		)
		INSERT INTO order_line (order_id, offering_id, qty, unit_price, currency)
		SELECT $1, $4, $5, $6::numeric, $7 FROM auth
		RETURNING id, order_id, offering_id, qty, unit_price::text, currency, created_at`,
		line.OrderID, sc.ScopeID, sc.UserID,
		line.OfferingID, line.Qty, string(line.UnitPrice), line.Currency)

	var (
		out      domain.OrderLine
		priceStr string
	)
	if err := row.Scan(
		&out.ID, &out.OrderID, &out.OfferingID, &out.Qty, &priceStr, &out.Currency, &out.CreatedAt,
	); err != nil {
		return nil, mapErr(err)
	}
	out.UnitPrice = domain.Numeric(priceStr)
	return &out, nil
}

func scanPO(row pgx.Row) (*domain.PurchaseOrder, error) {
	var po domain.PurchaseOrder
	if err := row.Scan(
		&po.ID, &po.ScopeID, &po.Source, &po.SourceOrderID, &po.Status,
		&po.PlacedAt, &po.CreatedAt, &po.UpdatedAt,
	); err != nil {
		return nil, mapErr(err)
	}
	return &po, nil
}
