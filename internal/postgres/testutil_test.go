package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/postgres"
)

// testPool is the shared connection pool for all integration tests in this package.
// It is nil when DATABASE_TEST_URL is not set; tests call requireTestDB to get it or skip.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	dsn := os.Getenv("DATABASE_TEST_URL")
	if dsn != "" {
		pool, err := postgres.NewPool(context.Background(), dsn)
		if err != nil {
			panic("test DB connect: " + err.Error())
		}
		testPool = pool
		defer pool.Close()

		mig, err := migrate.New("file://../../db/migrations", dsn)
		if err != nil {
			panic("migrate init: " + err.Error())
		}
		if err := mig.Up(); err != nil && err != migrate.ErrNoChange {
			panic("migrate up: " + err.Error())
		}
	}

	os.Exit(m.Run())
}

// requireTestDB returns the test pool or skips the test if DATABASE_TEST_URL is not set.
func requireTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_TEST_URL not set — skipping integration test")
	}
	return testPool
}

// truncateAll removes all data from every table, resetting identity sequences.
// Call at the start of each test that writes data.
func truncateAll(t *testing.T) {
	t.Helper()
	_, err := testPool.Exec(context.Background(), `
		TRUNCATE
			breakage_event, draw, project_requirement, project,
			lot, order_line, purchase_order,
			part_substitution, part_attribute, kit_content, offering, canonical_part,
			team_membership, inventory_scope, team, app_user
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncateAll: %v", err)
	}
}
