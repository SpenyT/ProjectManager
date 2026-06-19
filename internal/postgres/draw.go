package postgres

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// DrawRepo implements domain.DrawRepository against Postgres.
type DrawRepo struct {
	pool *pgxpool.Pool
}

// NewDrawRepo returns a DrawRepo backed by pool.
func NewDrawRepo(pool *pgxpool.Pool) *DrawRepo {
	return &DrawRepo{pool: pool}
}

var _ domain.DrawRepository = (*DrawRepo)(nil)

// ClaimLots creates or accumulates draw claims for a requirement against specific lots.
//
// For each lot in claims:
//  1. Locks the lot row (SELECT ... FOR UPDATE) to serialize concurrent claims.
//  2. Computes available = remaining_qty − active_claims (active = project.status='active').
//  3. Returns ErrOversell if claim.Qty > available.
//  4. Upserts the draw row (accumulates qty on conflict).
//
// All claims in one call are applied in a single transaction — all succeed or none.
func (r *DrawRepo) ClaimLots(
	ctx context.Context,
	sc domain.ScopeCtx,
	requirementID int64,
	claims []domain.LotClaim,
	via *domain.SatisfiedViaKind,
	substitutionID *int64,
) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Verify the requirement belongs to the caller's scope.
		var exists bool
		err := tx.QueryRow(ctx, `
			SELECT true
			FROM project_requirement pr
			JOIN project p ON p.id = pr.project_id
			JOIN inventory_scope s ON s.id = p.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE pr.id = $1 AND p.scope_id = $2 AND tm.user_id = $3`,
			requirementID, sc.ScopeID, sc.UserID).Scan(&exists)
		if err != nil {
			return mapErr(err)
		}

		var viaStr *string
		if via != nil {
			s := string(*via)
			viaStr = &s
		}

		// Sort by lot_id before locking to guarantee a consistent lock-acquisition
		// order across concurrent callers. Without this, two callers claiming
		// overlapping lots in opposite orders can deadlock.
		slices.SortFunc(claims, func(a, b domain.LotClaim) int {
			return cmp.Compare(a.LotID, b.LotID)
		})

		for _, claim := range claims {
			// Step 1: Lock the lot row. This establishes the critical-section boundary —
			// all concurrent ClaimLots calls for this lot serialize here.
			var remaining int64
			err := tx.QueryRow(ctx, `
				SELECT l.remaining_qty
				FROM lot l
				JOIN order_line ol ON ol.id = l.source_order_line_id
				JOIN purchase_order po ON po.id = ol.order_id
				WHERE l.id = $1 AND po.scope_id = $2
				FOR UPDATE OF l`,
				claim.LotID, sc.ScopeID).Scan(&remaining)
			if err != nil {
				return fmt.Errorf("ClaimLots: lock lot %d: %w", claim.LotID, mapErr(err))
			}

			// Step 2: Re-read active claims in a separate statement AFTER acquiring
			// the lock. A combined FOR UPDATE + LATERAL evaluates the subquery with
			// the pre-lock snapshot, which is stale. A separate statement sees the
			// latest committed state (READ COMMITTED per-statement snapshot).
			var activeClaim int64
			if err := tx.QueryRow(ctx, `
				SELECT COALESCE(SUM(d.qty - d.consumed_qty), 0)
				FROM draw d
				JOIN project_requirement pr ON pr.id = d.project_requirement_id
				JOIN project p ON p.id = pr.project_id
				WHERE d.lot_id = $1 AND p.status = 'active'`,
				claim.LotID).Scan(&activeClaim); err != nil {
				return fmt.Errorf("ClaimLots: active claims for lot %d: %w", claim.LotID, err)
			}

			if claim.Qty > remaining-activeClaim {
				return domain.ErrOversell
			}

			// Upsert the draw: accumulate qty if a draw already exists for this (req, lot) pair.
			_, err = tx.Exec(ctx, `
				INSERT INTO draw
					(project_requirement_id, lot_id, qty, consumed_qty,
					 satisfied_via_kind, satisfied_via_substitution)
				VALUES ($1, $2, $3, 0, $4, $5)
				ON CONFLICT (project_requirement_id, lot_id) DO UPDATE SET
					qty = draw.qty + EXCLUDED.qty,
					satisfied_via_kind =
						COALESCE(EXCLUDED.satisfied_via_kind, draw.satisfied_via_kind),
					satisfied_via_substitution =
						COALESCE(EXCLUDED.satisfied_via_substitution, draw.satisfied_via_substitution)`,
				requirementID, claim.LotID, claim.Qty, viaStr, substitutionID)
			if err != nil {
				return fmt.Errorf("ClaimLots: upsert draw: %w", mapErr(err))
			}
		}
		return nil
	})
}

// Release reduces a draw's qty by qty. Deletes the draw if qty would drop to consumed_qty.
func (r *DrawRepo) Release(ctx context.Context, sc domain.ScopeCtx, drawID int64, qty int64) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Fetch current draw state; verify scope.
		var (
			currentQty  int64
			consumedQty int64
		)
		err := tx.QueryRow(ctx, `
			SELECT d.qty, d.consumed_qty
			FROM draw d
			JOIN project_requirement pr ON pr.id = d.project_requirement_id
			JOIN project p ON p.id = pr.project_id
			JOIN inventory_scope s ON s.id = p.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE d.id = $1 AND p.scope_id = $2 AND tm.user_id = $3
			FOR UPDATE OF d`,
			drawID, sc.ScopeID, sc.UserID).Scan(&currentQty, &consumedQty)
		if err != nil {
			return mapErr(err)
		}

		newQty := currentQty - qty
		if newQty < consumedQty {
			return fmt.Errorf("release qty %d would drop below consumed_qty %d: %w",
				qty, consumedQty, domain.ErrConflict)
		}

		if newQty == consumedQty {
			_, err = tx.Exec(ctx, `DELETE FROM draw WHERE id = $1`, drawID)
		} else {
			_, err = tx.Exec(ctx, `UPDATE draw SET qty = $2 WHERE id = $1`, drawID, newQty)
		}
		return err
	})
}

// GetCoverage returns per-requirement coverage for a project (exact-match only).
func (r *DrawRepo) GetCoverage(ctx context.Context, sc domain.ScopeCtx, projectID int64) ([]*domain.RequirementCoverage, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT pc.project_id, pc.canonical_part_id, pc.qty_required, pc.available, pc.satisfied
		FROM project_coverage pc
		JOIN project p ON p.id = pc.project_id
		JOIN inventory_scope s ON s.id = p.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE pc.project_id = $1 AND p.scope_id = $2 AND tm.user_id = $3`,
		projectID, sc.ScopeID, sc.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var coverage []*domain.RequirementCoverage
	for rows.Next() {
		var rc domain.RequirementCoverage
		if err := rows.Scan(
			&rc.ProjectID, &rc.CanonicalPartID, &rc.QtyRequired, &rc.Available, &rc.Satisfied,
		); err != nil {
			return nil, mapErr(err)
		}
		coverage = append(coverage, &rc)
	}
	return coverage, rows.Err()
}
