package postgres

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// ProjectRepo implements domain.ProjectRepository against Postgres.
type ProjectRepo struct {
	pool *pgxpool.Pool
}

// NewProjectRepo returns a ProjectRepo backed by pool.
func NewProjectRepo(pool *pgxpool.Pool) *ProjectRepo {
	return &ProjectRepo{pool: pool}
}

var _ domain.ProjectRepository = (*ProjectRepo)(nil)

const projCols = `id, scope_id, name, status, created_at, updated_at`

func (r *ProjectRepo) GetByID(ctx context.Context, sc domain.ScopeCtx, id int64) (*domain.Project, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT p.`+projCols+`
		FROM project p
		JOIN inventory_scope s ON s.id = p.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE p.id = $1 AND p.scope_id = $2 AND tm.user_id = $3`,
		id, sc.ScopeID, sc.UserID)
	return scanProject(row)
}

func (r *ProjectRepo) Create(ctx context.Context, sc domain.ScopeCtx, project *domain.Project) (*domain.Project, error) {
	row := r.pool.QueryRow(ctx, `
		WITH auth AS (
			SELECT 1 FROM inventory_scope s
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE s.id = $1 AND tm.user_id = $2
		)
		INSERT INTO project (scope_id, name)
		SELECT $1, $3 FROM auth
		RETURNING `+projCols,
		sc.ScopeID, sc.UserID, project.Name)
	p, err := scanProject(row)
	return p, mapErr(err)
}

func (r *ProjectRepo) List(ctx context.Context, sc domain.ScopeCtx) ([]*domain.Project, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT p.`+projCols+`
		FROM project p
		JOIN inventory_scope s ON s.id = p.scope_id
		JOIN team_membership tm ON tm.team_id = s.team_id
		WHERE p.scope_id = $1 AND tm.user_id = $2
		ORDER BY p.created_at DESC`,
		sc.ScopeID, sc.UserID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var projects []*domain.Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// TransitionStatus atomically changes a project's status and reconciles its draws.
// active→built: permanently consumes all remaining draw qty and records build_consumed events.
// active→paused/cancelled: applies explicit breakage records; remaining claims are implicitly released.
func (r *ProjectRepo) TransitionStatus(
	ctx context.Context,
	sc domain.ScopeCtx,
	id int64,
	to domain.ProjectStatus,
	breakage []domain.BreakageRecord,
) error {
	return pgx.BeginTxFunc(ctx, r.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		// Lock the project row to serialize concurrent transitions.
		var current domain.ProjectStatus
		err := tx.QueryRow(ctx, `
			SELECT p.status
			FROM project p
			JOIN inventory_scope s ON s.id = p.scope_id
			JOIN team_membership tm ON tm.team_id = s.team_id
			WHERE p.id = $1 AND p.scope_id = $2 AND tm.user_id = $3
			FOR UPDATE OF p`,
			id, sc.ScopeID, sc.UserID).Scan(&current)
		if err != nil {
			return mapErr(err)
		}

		proj := &domain.Project{Status: current}
		if !proj.CanTransitionTo(to) {
			return domain.ErrInvalidTransition
		}

		switch {
		case current == domain.ProjectStatusActive && to == domain.ProjectStatusBuilt:
			if err := consumeAllDraws(ctx, tx, id, sc.UserID); err != nil {
				return err
			}
		case current == domain.ProjectStatusActive &&
			(to == domain.ProjectStatusPaused || to == domain.ProjectStatusCancelled):
			if err := applyBreakageRecords(ctx, tx, id, breakage, sc.UserID); err != nil {
				return err
			}
		}

		_, err = tx.Exec(ctx, `UPDATE project SET status = $2 WHERE id = $1`, id, to)
		return err
	})
}

// consumeAllDraws permanently consumes every unclaimed draw in the project (built transition).
func consumeAllDraws(ctx context.Context, tx pgx.Tx, projectID int64, reportedBy uuid.UUID) error {
	// Decrement lot.remaining_qty for each draw's unclaimed qty (aggregated per lot).
	_, err := tx.Exec(ctx, `
		UPDATE lot
		SET remaining_qty = lot.remaining_qty - src.to_consume
		FROM (
			SELECT d.lot_id, SUM(d.qty - d.consumed_qty) AS to_consume
			FROM draw d
			JOIN project_requirement pr ON pr.id = d.project_requirement_id
			WHERE pr.project_id = $1 AND d.qty > d.consumed_qty
			GROUP BY d.lot_id
		) src
		WHERE lot.id = src.lot_id`,
		projectID)
	if err != nil {
		return fmt.Errorf("consumeAllDraws: decrement lots: %w", err)
	}

	// Record build_consumed events for all draws with remaining qty.
	_, err = tx.Exec(ctx, `
		INSERT INTO breakage_event (draw_id, qty, kind, reported_by)
		SELECT d.id, d.qty - d.consumed_qty, 'build_consumed', $2
		FROM draw d
		JOIN project_requirement pr ON pr.id = d.project_requirement_id
		WHERE pr.project_id = $1 AND d.qty > d.consumed_qty`,
		projectID, reportedBy)
	if err != nil {
		return fmt.Errorf("consumeAllDraws: insert events: %w", err)
	}

	// Mark all draws fully consumed.
	_, err = tx.Exec(ctx, `
		UPDATE draw SET consumed_qty = qty
		WHERE id IN (
			SELECT d.id FROM draw d
			JOIN project_requirement pr ON pr.id = d.project_requirement_id
			WHERE pr.project_id = $1 AND d.qty > d.consumed_qty
		)`,
		projectID)
	if err != nil {
		return fmt.Errorf("consumeAllDraws: update draws: %w", err)
	}

	return nil
}

// applyBreakageRecords processes explicit per-draw breakage during a status transition.
// For each record: verify ownership, validate qty, decrement lot, insert event, increment consumed.
func applyBreakageRecords(
	ctx context.Context,
	tx pgx.Tx,
	projectID int64,
	records []domain.BreakageRecord,
	reportedBy uuid.UUID,
) error {
	for _, rec := range records {
		// Lock the draw row and verify it belongs to this project.
		var (
			lotID       int64
			qty         int64
			consumedQty int64
		)
		err := tx.QueryRow(ctx, `
			SELECT d.lot_id, d.qty, d.consumed_qty
			FROM draw d
			JOIN project_requirement pr ON pr.id = d.project_requirement_id
			WHERE d.id = $1 AND pr.project_id = $2
			FOR UPDATE OF d`,
			rec.DrawID, projectID).Scan(&lotID, &qty, &consumedQty)
		if err != nil {
			return fmt.Errorf("applyBreakageRecords: draw %d: %w", rec.DrawID, mapErr(err))
		}

		if rec.Qty > qty-consumedQty {
			return fmt.Errorf("breakage qty %d exceeds unclaimed %d on draw %d: %w",
				rec.Qty, qty-consumedQty, rec.DrawID, domain.ErrConflict)
		}

		if _, err = tx.Exec(ctx, `
			UPDATE lot SET remaining_qty = remaining_qty - $2 WHERE id = $1`,
			lotID, rec.Qty); err != nil {
			return fmt.Errorf("applyBreakageRecords: decrement lot %d: %w", lotID, err)
		}

		if _, err = tx.Exec(ctx, `
			INSERT INTO breakage_event (draw_id, qty, kind, reason, reported_by)
			VALUES ($1, $2, $3, $4, $5)`,
			rec.DrawID, rec.Qty, string(rec.Kind), rec.Reason, reportedBy); err != nil {
			return fmt.Errorf("applyBreakageRecords: insert event: %w", mapErr(err))
		}

		if _, err = tx.Exec(ctx, `
			UPDATE draw SET consumed_qty = consumed_qty + $2 WHERE id = $1`,
			rec.DrawID, rec.Qty); err != nil {
			return fmt.Errorf("applyBreakageRecords: update draw %d: %w", rec.DrawID, err)
		}
	}
	return nil
}

func scanProject(row pgx.Row) (*domain.Project, error) {
	var p domain.Project
	if err := row.Scan(
		&p.ID, &p.ScopeID, &p.Name, &p.Status, &p.CreatedAt, &p.UpdatedAt,
	); err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}
