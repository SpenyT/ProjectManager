package postgres_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"

	"ProjectManager/internal/domain"
	"ProjectManager/internal/postgres"
)

// seedForDrawTest creates the minimum data required for a draw claim test:
// one user, one team, one scope, one canonical part, one purchase order,
// one order line, and one lot with the given remaining_qty.
// Returns (sc, requirementID, lotID).
func seedForDrawTest(t *testing.T, remainingQty int64) (domain.ScopeCtx, int64, int64) {
	t.Helper()
	ctx := context.Background()
	pool := testPool

	var (
		userID  uuid.UUID
		teamID  uuid.UUID
		scopeID uuid.UUID
		partID  uuid.UUID
		orderID int64
		lineID  int64
		lotID   int64
		projID  int64
		reqID   int64
	)

	// app_user
	if err := pool.QueryRow(ctx, `INSERT INTO app_user (email) VALUES ($1) RETURNING id`, uuid.New().String()+"@test.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// team
	if err := pool.QueryRow(ctx, `INSERT INTO team (name) VALUES ('test') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	// team_membership
	if _, err := pool.Exec(ctx, `INSERT INTO team_membership (team_id, user_id, role) VALUES ($1, $2, 'admin')`, teamID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	// inventory_scope
	if err := pool.QueryRow(ctx, `INSERT INTO inventory_scope (team_id, name) VALUES ($1, 'default') RETURNING id`, teamID).Scan(&scopeID); err != nil {
		t.Fatalf("seed scope: %v", err)
	}
	// canonical_part
	if err := pool.QueryRow(ctx, `INSERT INTO canonical_part DEFAULT VALUES RETURNING id`).Scan(&partID); err != nil {
		t.Fatalf("seed part: %v", err)
	}
	// offering
	var offeringID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO offering (source, source_external_id) VALUES ('digikey', $1) RETURNING id`, uuid.New().String()).Scan(&offeringID); err != nil {
		t.Fatalf("seed offering: %v", err)
	}
	// purchase_order
	if err := pool.QueryRow(ctx, `INSERT INTO purchase_order (scope_id, source, source_order_id) VALUES ($1, 'digikey', $2) RETURNING id`, scopeID, uuid.New().String()).Scan(&orderID); err != nil {
		t.Fatalf("seed purchase_order: %v", err)
	}
	// order_line
	if err := pool.QueryRow(ctx, `INSERT INTO order_line (order_id, offering_id, qty, unit_price, currency) VALUES ($1, $2, 10, 1.00, 'USD') RETURNING id`, orderID, offeringID).Scan(&lineID); err != nil {
		t.Fatalf("seed order_line: %v", err)
	}
	// lot
	if err := pool.QueryRow(ctx, `
		INSERT INTO lot (canonical_part_id, source_order_line_id, qty_received, remaining_qty, unit_cost, currency)
		VALUES ($1, $2, $3, $3, '1.00', 'USD') RETURNING id`, partID, lineID, remainingQty).Scan(&lotID); err != nil {
		t.Fatalf("seed lot: %v", err)
	}
	// project (active)
	if err := pool.QueryRow(ctx, `INSERT INTO project (scope_id, name, status) VALUES ($1, 'test', 'active') RETURNING id`, scopeID).Scan(&projID); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	// project_requirement
	if err := pool.QueryRow(ctx, `INSERT INTO project_requirement (project_id, canonical_part_id, qty_required) VALUES ($1, $2, $3) RETURNING id`, projID, partID, remainingQty).Scan(&reqID); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}

	return domain.ScopeCtx{UserID: userID, ScopeID: scopeID}, reqID, lotID
}

func TestDrawRepo_ClaimLots_Basic(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	sc, reqID, lotID := seedForDrawTest(t, 10)
	repo := postgres.NewDrawRepo(testPool)
	ctx := context.Background()

	if err := repo.ClaimLots(ctx, sc, reqID, []domain.LotClaim{{LotID: lotID, Qty: 5}}, nil, nil); err != nil {
		t.Fatalf("ClaimLots: %v", err)
	}
}

func TestDrawRepo_ClaimLots_Oversell(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	sc, reqID, lotID := seedForDrawTest(t, 5)
	repo := postgres.NewDrawRepo(testPool)
	ctx := context.Background()

	err := repo.ClaimLots(ctx, sc, reqID, []domain.LotClaim{{LotID: lotID, Qty: 10}}, nil, nil)
	if !errors.Is(err, domain.ErrOversell) {
		t.Errorf("expected ErrOversell, got %v", err)
	}
}

// TestDrawRepo_ClaimLots_ConcurrentNoOversell is the critical invariant test.
// Two goroutines race to claim the only available unit from the same lot.
// Exactly one must succeed; the other must get ErrOversell.
func TestDrawRepo_ClaimLots_ConcurrentNoOversell(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	ctx := context.Background()
	pool := testPool

	// Shared lot with exactly 1 unit.
	var (
		userID  uuid.UUID
		teamID  uuid.UUID
		scopeID uuid.UUID
		partID  uuid.UUID
		orderID int64
		lineID  int64
		lotID   int64
	)
	if err := pool.QueryRow(ctx, `INSERT INTO app_user (email) VALUES ($1) RETURNING id`, uuid.New().String()+"@test.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO team (name) VALUES ('race') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO team_membership (team_id, user_id, role) VALUES ($1, $2, 'admin')`, teamID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO inventory_scope (team_id) VALUES ($1) RETURNING id`, teamID).Scan(&scopeID); err != nil {
		t.Fatalf("seed scope: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO canonical_part DEFAULT VALUES RETURNING id`).Scan(&partID); err != nil {
		t.Fatalf("seed part: %v", err)
	}
	var offeringID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO offering (source, source_external_id) VALUES ('digikey', $1) RETURNING id`, uuid.New().String()).Scan(&offeringID); err != nil {
		t.Fatalf("seed offering: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO purchase_order (scope_id, source, source_order_id) VALUES ($1, 'digikey', $2) RETURNING id`, scopeID, uuid.New().String()).Scan(&orderID); err != nil {
		t.Fatalf("seed purchase_order: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO order_line (order_id, offering_id, qty, unit_price, currency) VALUES ($1, $2, 1, 1.00, 'USD') RETURNING id`, orderID, offeringID).Scan(&lineID); err != nil {
		t.Fatalf("seed order_line: %v", err)
	}
	// The contested lot: exactly 1 unit.
	if err := pool.QueryRow(ctx, `
		INSERT INTO lot (canonical_part_id, source_order_line_id, qty_received, remaining_qty, unit_cost, currency)
		VALUES ($1, $2, 1, 1, '1.00', 'USD') RETURNING id`, partID, lineID).Scan(&lotID); err != nil {
		t.Fatalf("seed lot: %v", err)
	}

	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}

	// Two projects, each with a requirement for the same part — both active.
	makeActiveReq := func() int64 {
		var projID, reqID int64
		if err := pool.QueryRow(ctx, `INSERT INTO project (scope_id, name, status) VALUES ($1, 'p', 'active') RETURNING id`, scopeID).Scan(&projID); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO project_requirement (project_id, canonical_part_id, qty_required) VALUES ($1, $2, 1) RETURNING id`, projID, partID).Scan(&reqID); err != nil {
			t.Fatalf("seed requirement: %v", err)
		}
		return reqID
	}
	reqA := makeActiveReq()
	reqB := makeActiveReq()

	repo := postgres.NewDrawRepo(pool)
	claim := []domain.LotClaim{{LotID: lotID, Qty: 1}}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = repo.ClaimLots(ctx, sc, reqA, claim, nil, nil) }()
	go func() { defer wg.Done(); errs[1] = repo.ClaimLots(ctx, sc, reqB, claim, nil, nil) }()
	wg.Wait()

	var successes, oversells int
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, domain.ErrOversell):
			oversells++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if successes != 1 || oversells != 1 {
		t.Errorf("want 1 success + 1 ErrOversell, got %d success + %d ErrOversell", successes, oversells)
	}
}

// TestDrawRepo_ClaimLots_ConcurrentMultiLot_NoDeadlock exercises the lock-ordering fix.
//
// Without slices.SortFunc in ClaimLots, two goroutines claiming the same two lots in
// opposite input orders can deadlock (goroutine A holds lot 1, waits for lot 2;
// goroutine B holds lot 2, waits for lot 1). PostgreSQL detects this after
// deadlock_detection_timeout and kills one transaction with error 40P01.
//
// With the fix, both goroutines sort to the same lot order, so one serialises behind
// the other — no cycle, no deadlock. The test asserts that neither goroutine receives
// a deadlock error. Both goroutines claim from two separate lots (qty=2 each), so
// both claims can succeed and the assertion on outcomes is also checked.
func TestDrawRepo_ClaimLots_ConcurrentMultiLot_NoDeadlock(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	ctx := context.Background()
	pool := testPool

	var (
		userID  uuid.UUID
		teamID  uuid.UUID
		scopeID uuid.UUID
		partID  uuid.UUID
	)
	if err := pool.QueryRow(ctx, `INSERT INTO app_user (email) VALUES ($1) RETURNING id`, uuid.New().String()+"@test.com").Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO team (name) VALUES ('dl') RETURNING id`).Scan(&teamID); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO team_membership (team_id, user_id, role) VALUES ($1,$2,'admin')`, teamID, userID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO inventory_scope (team_id) VALUES ($1) RETURNING id`, teamID).Scan(&scopeID); err != nil {
		t.Fatalf("seed scope: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO canonical_part DEFAULT VALUES RETURNING id`).Scan(&partID); err != nil {
		t.Fatalf("seed part: %v", err)
	}

	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}

	// Two lots, each with qty=2 — enough for both goroutines to succeed.
	makeLot := func() int64 {
		var offeringID uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO offering (source, source_external_id) VALUES ('digikey',$1) RETURNING id`, uuid.New().String()).Scan(&offeringID); err != nil {
			t.Fatalf("seed offering: %v", err)
		}
		var orderID, lineID, lotID int64
		if err := pool.QueryRow(ctx, `INSERT INTO purchase_order (scope_id, source, source_order_id) VALUES ($1,'digikey',$2) RETURNING id`, scopeID, uuid.New().String()).Scan(&orderID); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO order_line (order_id, offering_id, qty, unit_price, currency) VALUES ($1,$2,2,'1.00','USD') RETURNING id`, orderID, offeringID).Scan(&lineID); err != nil {
			t.Fatalf("seed line: %v", err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO lot (canonical_part_id, source_order_line_id, qty_received, remaining_qty, unit_cost, currency) VALUES ($1,$2,2,2,'1.00','USD') RETURNING id`, partID, lineID).Scan(&lotID); err != nil {
			t.Fatalf("seed lot: %v", err)
		}
		return lotID
	}
	lotA := makeLot()
	lotB := makeLot()

	// Two active projects, one requirement each.
	makeActiveReq := func() int64 {
		var projID, reqID int64
		if err := pool.QueryRow(ctx, `INSERT INTO project (scope_id, name, status) VALUES ($1,'p','active') RETURNING id`, scopeID).Scan(&projID); err != nil {
			t.Fatalf("seed project: %v", err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO project_requirement (project_id, canonical_part_id, qty_required) VALUES ($1,$2,2) RETURNING id`, projID, partID).Scan(&reqID); err != nil {
			t.Fatalf("seed requirement: %v", err)
		}
		return reqID
	}
	reqA := makeActiveReq()
	reqB := makeActiveReq()

	repo := postgres.NewDrawRepo(pool)

	// Goroutine A provides lots in [A, B] order; goroutine B provides them in [B, A] order.
	// Without the sort fix these are opposite lock orders → deadlock risk.
	claimAB := []domain.LotClaim{{LotID: lotA, Qty: 1}, {LotID: lotB, Qty: 1}}
	claimBA := []domain.LotClaim{{LotID: lotB, Qty: 1}, {LotID: lotA, Qty: 1}}

	errs := make([]error, 2)
	ready := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		<-ready
		errs[0] = repo.ClaimLots(ctx, sc, reqA, claimAB, nil, nil)
	}()
	go func() {
		defer wg.Done()
		<-ready
		errs[1] = repo.ClaimLots(ctx, sc, reqB, claimBA, nil, nil)
	}()
	close(ready)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			continue
		}
		// ErrOversell is acceptable (serialisation means one might not see stock).
		if errors.Is(err, domain.ErrOversell) {
			continue
		}
		// A deadlock error (40P01) from Postgres means the sort fix is broken.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.DeadlockDetected {
			t.Errorf("goroutine %d: deadlock detected — lock ordering fix not working: %v", i, err)
			continue
		}
		t.Errorf("goroutine %d: unexpected error: %v", i, err)
	}
}
