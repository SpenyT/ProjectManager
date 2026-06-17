package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
)

// OfferingRepo implements domain.OfferingRepository against Postgres.
type OfferingRepo struct {
	pool *pgxpool.Pool
}

// NewOfferingRepo returns an OfferingRepo backed by pool.
func NewOfferingRepo(pool *pgxpool.Pool) *OfferingRepo {
	return &OfferingRepo{pool: pool}
}

var _ domain.OfferingRepository = (*OfferingRepo)(nil)

const offeringCols = `id, source, source_external_id, canonical_part_id, is_kit,
	url, seller, title, last_seen_price::text, currency, last_seen_at, created_at`

func (r *OfferingRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.Offering, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+offeringCols+` FROM offering WHERE id = $1`, id)
	return scanOffering(row)
}

func (r *OfferingRepo) Upsert(ctx context.Context, o *domain.Offering) (*domain.Offering, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO offering (source, source_external_id, canonical_part_id, is_kit, url, seller, title,
		                      last_seen_price, currency, last_seen_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9, $10)
		ON CONFLICT (source, source_external_id) DO UPDATE SET
			canonical_part_id = COALESCE(EXCLUDED.canonical_part_id, offering.canonical_part_id),
			last_seen_price   = COALESCE(EXCLUDED.last_seen_price,   offering.last_seen_price),
			currency          = COALESCE(EXCLUDED.currency,          offering.currency),
			last_seen_at      = COALESCE(EXCLUDED.last_seen_at,      offering.last_seen_at)
		RETURNING `+offeringCols,
		o.Source, o.SourceExternalID, o.CanonicalPartID, o.IsKit,
		o.URL, o.Seller, o.Title, toStrPtr(o.LastSeenPrice), o.Currency, o.LastSeenAt)
	of, err := scanOffering(row)
	return of, mapErr(err)
}

func (r *OfferingRepo) ListUnmapped(ctx context.Context) ([]*domain.Offering, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+offeringCols+`
		FROM offering
		WHERE canonical_part_id IS NULL AND is_kit = false
		ORDER BY created_at`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	return collectOfferings(rows)
}

func (r *OfferingRepo) SetCanonicalPart(ctx context.Context, offeringID, partID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE offering SET canonical_part_id = $2 WHERE id = $1`, offeringID, partID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (r *OfferingRepo) GetKitContents(ctx context.Context, offeringID uuid.UUID) ([]*domain.KitContent, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT offering_id, canonical_part_id, qty_per_unit
		FROM kit_content
		WHERE offering_id = $1
		ORDER BY canonical_part_id`, offeringID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var contents []*domain.KitContent
	for rows.Next() {
		var kc domain.KitContent
		if err := rows.Scan(&kc.OfferingID, &kc.CanonicalPartID, &kc.QtyPerUnit); err != nil {
			return nil, mapErr(err)
		}
		contents = append(contents, &kc)
	}
	return contents, rows.Err()
}

func scanOffering(row pgx.Row) (*domain.Offering, error) {
	var (
		o        domain.Offering
		priceStr *string
	)
	if err := row.Scan(
		&o.ID, &o.Source, &o.SourceExternalID, &o.CanonicalPartID, &o.IsKit,
		&o.URL, &o.Seller, &o.Title, &priceStr, &o.Currency, &o.LastSeenAt, &o.CreatedAt,
	); err != nil {
		return nil, mapErr(err)
	}
	o.LastSeenPrice = toNumericPtr(priceStr)
	return &o, nil
}

func collectOfferings(rows pgx.Rows) ([]*domain.Offering, error) {
	var offerings []*domain.Offering
	for rows.Next() {
		var (
			o        domain.Offering
			priceStr *string
		)
		if err := rows.Scan(
			&o.ID, &o.Source, &o.SourceExternalID, &o.CanonicalPartID, &o.IsKit,
			&o.URL, &o.Seller, &o.Title, &priceStr, &o.Currency, &o.LastSeenAt, &o.CreatedAt,
		); err != nil {
			return nil, mapErr(err)
		}
		o.LastSeenPrice = toNumericPtr(priceStr)
		offerings = append(offerings, &o)
	}
	return offerings, rows.Err()
}
