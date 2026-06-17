package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// CanonicalPartRepo implements domain.CanonicalPartRepository against Postgres.
type CanonicalPartRepo struct {
	pool *pgxpool.Pool
}

// NewCanonicalPartRepo returns a CanonicalPartRepo backed by pool.
func NewCanonicalPartRepo(pool *pgxpool.Pool) *CanonicalPartRepo {
	return &CanonicalPartRepo{pool: pool}
}

var _ domain.CanonicalPartRepository = (*CanonicalPartRepo)(nil)

const cpCols = `id, mpn, manufacturer, description, package,
	datasheet_url, datasheet_resolved_at, created_at`

func (r *CanonicalPartRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.CanonicalPart, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+cpCols+` FROM canonical_part WHERE id = $1`, id)
	return scanCP(row)
}

func (r *CanonicalPartRepo) GetByMPNAndManufacturer(ctx context.Context, mpn, manufacturer string) (*domain.CanonicalPart, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+cpCols+`
		FROM canonical_part WHERE mpn = $1 AND manufacturer = $2`, mpn, manufacturer)
	return scanCP(row)
}

func (r *CanonicalPartRepo) Create(ctx context.Context, part *domain.CanonicalPart) (*domain.CanonicalPart, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO canonical_part (mpn, manufacturer, description, package)
		VALUES ($1, $2, $3, $4)
		RETURNING `+cpCols,
		part.MPN, part.Manufacturer, part.Description, part.Package)
	p, err := scanCP(row)
	return p, mapErr(err)
}

func (r *CanonicalPartRepo) ListUnresolvedDatasheets(ctx context.Context) ([]*domain.CanonicalPart, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+cpCols+`
		FROM canonical_part
		WHERE datasheet_resolved_at IS NULL
		ORDER BY created_at`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return collectCPs(rows)
}

func (r *CanonicalPartRepo) UpdateDatasheetURL(ctx context.Context, id uuid.UUID, url string, resolvedAt time.Time) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE canonical_part
		SET datasheet_url = $2, datasheet_resolved_at = $3
		WHERE id = $1`, id, url, resolvedAt)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func scanCP(row pgx.Row) (*domain.CanonicalPart, error) {
	var p domain.CanonicalPart
	if err := row.Scan(
		&p.ID, &p.MPN, &p.Manufacturer, &p.Description, &p.Package,
		&p.DatasheetURL, &p.DatasheetResolvedAt, &p.CreatedAt,
	); err != nil {
		return nil, mapErr(err)
	}
	return &p, nil
}

func collectCPs(rows pgx.Rows) ([]*domain.CanonicalPart, error) {
	var parts []*domain.CanonicalPart
	for rows.Next() {
		var p domain.CanonicalPart
		if err := rows.Scan(
			&p.ID, &p.MPN, &p.Manufacturer, &p.Description, &p.Package,
			&p.DatasheetURL, &p.DatasheetResolvedAt, &p.CreatedAt,
		); err != nil {
			return nil, mapErr(err)
		}
		parts = append(parts, &p)
	}
	return parts, rows.Err()
}
