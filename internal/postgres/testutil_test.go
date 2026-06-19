package postgres_test

import (
	"context"
	"os"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"ProjectManager/internal/domain"
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

// seedTenancy creates one user + team + membership + scope and returns (userID, scopeID).
func seedTenancy(t *testing.T) (uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	var userID, teamID, scopeID uuid.UUID
	if err := testPool.QueryRow(ctx, `INSERT INTO app_user (email) VALUES ($1) RETURNING id`,
		uuid.New().String()+"@t.test").Scan(&userID); err != nil {
		t.Fatalf("seedTenancy user: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO team (name) VALUES ('t') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatalf("seedTenancy team: %v", err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO team_membership (team_id, user_id, role) VALUES ($1,$2,'admin')`,
		teamID, userID); err != nil {
		t.Fatalf("seedTenancy membership: %v", err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO inventory_scope (team_id) VALUES ($1) RETURNING id`,
		teamID).Scan(&scopeID); err != nil {
		t.Fatalf("seedTenancy scope: %v", err)
	}
	return userID, scopeID
}

// seedCanonicalPart inserts a minimal canonical_part row and returns its ID.
func seedCanonicalPart(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := testPool.QueryRow(context.Background(),
		`INSERT INTO canonical_part DEFAULT VALUES RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seedCanonicalPart: %v", err)
	}
	return id
}

// seedOffering inserts a non-kit offering linked to partID and returns its ID.
func seedOffering(t *testing.T, partID uuid.UUID) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO offering (source, source_external_id, canonical_part_id)
		VALUES ('digikey', $1, $2) RETURNING id`,
		uuid.New().String(), partID).Scan(&id); err != nil {
		t.Fatalf("seedOffering: %v", err)
	}
	return id
}

// seedKitOffering inserts a kit offering (is_kit=true, no canonical_part_id) and returns its ID.
func seedKitOffering(t *testing.T) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO offering (source, source_external_id, is_kit)
		VALUES ('digikey', $1, true) RETURNING id`,
		uuid.New().String()).Scan(&id); err != nil {
		t.Fatalf("seedKitOffering: %v", err)
	}
	return id
}

// seedPurchaseOrder inserts a purchase_order in scopeID and returns its ID.
func seedPurchaseOrder(t *testing.T, scopeID uuid.UUID) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO purchase_order (scope_id, source, source_order_id)
		VALUES ($1, 'digikey', $2) RETURNING id`,
		scopeID, uuid.New().String()).Scan(&id); err != nil {
		t.Fatalf("seedPurchaseOrder: %v", err)
	}
	return id
}

// seedOrderLine inserts an order_line under orderID and returns its ID.
// unit_price is fixed at 1.5000 USD; adjust directly via SQL if other values are needed.
func seedOrderLine(t *testing.T, orderID int64, offeringID uuid.UUID, qty int64) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO order_line (order_id, offering_id, qty, unit_price, currency)
		VALUES ($1, $2, $3, '1.5000', 'USD') RETURNING id`,
		orderID, offeringID, qty).Scan(&id); err != nil {
		t.Fatalf("seedOrderLine: %v", err)
	}
	return id
}

// seedLot inserts a lot directly (bypassing ExplodeOrderLine) and returns its ID.
// unit_cost is fixed at 1.5000 USD/unit; use this only for tests that don't care about cost.
func seedLot(t *testing.T, partID uuid.UUID, lineID int64, qty int64) int64 {
	t.Helper()
	var id int64
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO lot (canonical_part_id, source_order_line_id, qty_received, remaining_qty, unit_cost, currency)
		VALUES ($1, $2, $3, $3, '1.5000', 'USD') RETURNING id`,
		partID, lineID, qty).Scan(&id); err != nil {
		t.Fatalf("seedLot: %v", err)
	}
	return id
}

// seedActiveProjectWithDraw creates the minimum data to test project status transitions:
// tenancy, canonical_part, lot (lotQty), active project, requirement, and a draw (drawQty).
// Returns (sc, projID, lotID, reqID, drawID).
func seedActiveProjectWithDraw(t *testing.T, lotQty, drawQty int64) (domain.ScopeCtx, int64, int64, int64, int64) {
	t.Helper()
	ctx := context.Background()
	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}

	partID := seedCanonicalPart(t)
	offeringID := seedOffering(t, partID)
	orderID := seedPurchaseOrder(t, scopeID)
	lineID := seedOrderLine(t, orderID, offeringID, lotQty)
	lotID := seedLot(t, partID, lineID, lotQty)

	var projID, reqID, drawID int64
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project (scope_id, name, status) VALUES ($1, 'p', 'active') RETURNING id`,
		scopeID).Scan(&projID); err != nil {
		t.Fatalf("seedActiveProjectWithDraw project: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO project_requirement (project_id, canonical_part_id, qty_required) VALUES ($1, $2, $3) RETURNING id`,
		projID, partID, drawQty).Scan(&reqID); err != nil {
		t.Fatalf("seedActiveProjectWithDraw requirement: %v", err)
	}
	if err := testPool.QueryRow(ctx, `
		INSERT INTO draw (project_requirement_id, lot_id, qty) VALUES ($1, $2, $3) RETURNING id`,
		reqID, lotID, drawQty).Scan(&drawID); err != nil {
		t.Fatalf("seedActiveProjectWithDraw draw: %v", err)
	}
	return sc, projID, lotID, reqID, drawID
}
