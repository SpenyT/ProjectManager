// Package postgres implements the domain repository interfaces against PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgerrcode"

	"ProjectManager/internal/domain"
)

// NewPool creates and pings a pgx connection pool from the given DSN.
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres.NewPool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres.NewPool ping: %w", err)
	}
	return pool, nil
}

// mapErr converts pgx-level errors to domain errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgerrcode.UniqueViolation:
			return domain.ErrConflict
		case pgerrcode.ForeignKeyViolation:
			return domain.ErrNotFound
		case pgerrcode.CheckViolation:
			return fmt.Errorf("db check constraint %q: %w", pgErr.ConstraintName, domain.ErrConflict)
		}
	}
	return err
}

// toStrPtr converts a *domain.Numeric to *string for pgx scanning/binding.
func toStrPtr(n *domain.Numeric) *string {
	if n == nil {
		return nil
	}
	s := string(*n)
	return &s
}

// toNumericPtr converts a *string (from a NUMERIC::text cast) to *domain.Numeric.
func toNumericPtr(s *string) *domain.Numeric {
	if s == nil {
		return nil
	}
	n := domain.Numeric(*s)
	return &n
}
