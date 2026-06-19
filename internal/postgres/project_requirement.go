package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// ProjectRequirementRepo implements domain.ProjectRequirementRepository against Postgres.
type ProjectRequirementRepo struct {
	pool *pgxpool.Pool
}

// NewProjectRequirementRepo returns a ProjectRequirementRepo backed by pool.
func NewProjectRequirementRepo(pool *pgxpool.Pool) *ProjectRequirementRepo {
	return &ProjectRequirementRepo{pool: pool}
}

var _ domain.ProjectRequirementRepository = (*ProjectRequirementRepo)(nil)

func (r *ProjectRequirementRepo) Add(ctx context.Context, sc domain.ScopeCtx, req *domain.ProjectRequirement) (*domain.ProjectRequirement, error) {
	row := r.pool.QueryRow(ctx, `
		WITH auth AS (
			SELECT 1 FROM project p
			JOIN inventory_scope s ON s.id = p.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE p.id = $1 AND p.scope_id = $2 AND tm.user_id = $3
		)
		INSERT INTO project_requirement (project_id, canonical_part_id, qty_required)
		SELECT $1, $4, $5 FROM auth
		RETURNING id, project_id, canonical_part_id, qty_required`,
		req.ProjectID, sc.ScopeID, sc.UserID, req.CanonicalPartID, req.QtyRequired)

	var out domain.ProjectRequirement
	if err := row.Scan(&out.ID, &out.ProjectID, &out.CanonicalPartID, &out.QtyRequired); err != nil {
		return nil, mapErr(err)
	}
	return &out, nil
}

func (r *ProjectRequirementRepo) List(ctx context.Context, sc domain.ScopeCtx, projectID int64) ([]*domain.ProjectRequirement, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT pr.id, pr.project_id, pr.canonical_part_id, pr.qty_required
		FROM project_requirement pr
		JOIN project p ON p.id = pr.project_id
		JOIN inventory_scope s ON s.id = p.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE pr.project_id = $1 AND p.scope_id = $2 AND tm.user_id = $3
		ORDER BY pr.id`,
		projectID, sc.ScopeID, sc.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var reqs []*domain.ProjectRequirement
	for rows.Next() {
		var req domain.ProjectRequirement
		if err := rows.Scan(&req.ID, &req.ProjectID, &req.CanonicalPartID, &req.QtyRequired); err != nil {
			return nil, mapErr(err)
		}
		reqs = append(reqs, &req)
	}
	return reqs, rows.Err()
}

// Remove deletes a project requirement and cascades to its draw rows.
//
// Blocked (ErrConflict) when:
//   - the project is active: live hard claims exist; transition the project first.
//   - any draw has consumed_qty > 0: inventory was permanently spent against this
//     requirement; it is now historical record and must not be erased.
//
// The DB also protects via FK RESTRICT on breakage_event → draw, but the guards
// above return a clear ErrConflict before that constraint is reached.
func (r *ProjectRequirementRepo) Remove(ctx context.Context, sc domain.ScopeCtx, id int64) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Lock project + requirement rows; verify scope membership in the same read.
		var status domain.ProjectStatus
		err := tx.QueryRow(ctx, `
			SELECT p.status
			FROM project_requirement pr
			JOIN project p ON p.id = pr.project_id
			JOIN inventory_scope s ON s.id = p.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE pr.id = $1 AND p.scope_id = $2 AND tm.user_id = $3
			FOR UPDATE OF p, pr`,
			id, sc.ScopeID, sc.UserID).Scan(&status)
		if err != nil {
			return mapErr(err)
		}

		if status == domain.ProjectStatusActive {
			return fmt.Errorf("cannot remove requirement from active project: %w", domain.ErrConflict)
		}

		// Guard consumed history regardless of project status.
		var hasConsumed bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM draw WHERE project_requirement_id = $1 AND consumed_qty > 0
			)`, id).Scan(&hasConsumed); err != nil {
			return err
		}
		if hasConsumed {
			return fmt.Errorf("requirement has consumed draw history: %w", domain.ErrConflict)
		}

		tag, err := tx.Exec(ctx, `DELETE FROM project_requirement WHERE id = $1`, id)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return nil
	})
}
