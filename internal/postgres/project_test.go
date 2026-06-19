package postgres_test

import (
	"context"
	"errors"
	"testing"

	"ProjectManager/internal/domain"
	"ProjectManager/internal/postgres"
)

func TestProjectRepo_Create_and_GetByID(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	repo := postgres.NewProjectRepo(testPool)
	ctx := context.Background()

	created, err := repo.Create(ctx, sc, &domain.Project{Name: "Blinky"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == 0 {
		t.Fatal("created.ID is zero")
	}
	if created.Status != domain.ProjectStatusPlanning {
		t.Errorf("default status: got %q, want planning", created.Status)
	}

	got, err := repo.GetByID(ctx, sc, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got.Name != "Blinky" {
		t.Errorf("name: got %q, want Blinky", got.Name)
	}
}

func TestProjectRepo_List(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	repo := postgres.NewProjectRepo(testPool)
	ctx := context.Background()

	for _, name := range []string{"Alpha", "Beta"} {
		if _, err := repo.Create(ctx, sc, &domain.Project{Name: name}); err != nil {
			t.Fatalf("Create %s: %v", name, err)
		}
	}

	projects, err := repo.List(ctx, sc)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("want 2 projects, got %d", len(projects))
	}
}

func TestProjectRepo_GetByID_NotFound(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	repo := postgres.NewProjectRepo(testPool)

	_, err := repo.GetByID(context.Background(), sc, 99999)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestProjectRepo_TransitionStatus_Invalid(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	repo := postgres.NewProjectRepo(testPool)
	ctx := context.Background()

	p, err := repo.Create(ctx, sc, &domain.Project{Name: "p"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// planning → built is not allowed.
	err = repo.TransitionStatus(ctx, sc, p.ID, domain.ProjectStatusBuilt, nil)
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("expected ErrInvalidTransition, got %v", err)
	}
}

// TestProjectRepo_TransitionStatus_ActiveToBuilt verifies the built path:
// all draw qty is permanently consumed, lot.remaining_qty is decremented,
// and breakage_event rows with kind='build_consumed' are created.
func TestProjectRepo_TransitionStatus_ActiveToBuilt(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	sc, projID, lotID, _, drawID := seedActiveProjectWithDraw(t, 10, 7)
	repo := postgres.NewProjectRepo(testPool)
	ctx := context.Background()

	if err := repo.TransitionStatus(ctx, sc, projID, domain.ProjectStatusBuilt, nil); err != nil {
		t.Fatalf("TransitionStatus built: %v", err)
	}

	// lot.remaining_qty must be decremented by the draw qty (7).
	var remaining int64
	if err := testPool.QueryRow(ctx, `SELECT remaining_qty FROM lot WHERE id = $1`, lotID).Scan(&remaining); err != nil {
		t.Fatalf("query remaining_qty: %v", err)
	}
	if remaining != 3 { // 10 - 7
		t.Errorf("remaining_qty: got %d, want 3", remaining)
	}

	// draw.consumed_qty must equal draw.qty (fully consumed).
	var consumedQty int64
	if err := testPool.QueryRow(ctx, `SELECT consumed_qty FROM draw WHERE id = $1`, drawID).Scan(&consumedQty); err != nil {
		t.Fatalf("query consumed_qty: %v", err)
	}
	if consumedQty != 7 {
		t.Errorf("consumed_qty: got %d, want 7", consumedQty)
	}

	// Exactly one breakage_event with kind='build_consumed' must exist.
	var kind string
	var evtQty int64
	if err := testPool.QueryRow(ctx,
		`SELECT kind, qty FROM breakage_event WHERE draw_id = $1`, drawID).Scan(&kind, &evtQty); err != nil {
		t.Fatalf("query breakage_event: %v", err)
	}
	if kind != "build_consumed" {
		t.Errorf("breakage_event kind: got %q, want build_consumed", kind)
	}
	if evtQty != 7 {
		t.Errorf("breakage_event qty: got %d, want 7", evtQty)
	}
}

// TestProjectRepo_TransitionStatus_ActiveToCancelled verifies the cancelled path:
// explicit breakage qty reduces lot.remaining_qty, a breakage_event with kind='broken'
// is recorded, and the remaining claim is implicitly released (project no longer active).
func TestProjectRepo_TransitionStatus_ActiveToCancelled(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	sc, projID, lotID, _, drawID := seedActiveProjectWithDraw(t, 10, 7)
	repo := postgres.NewProjectRepo(testPool)
	ctx := context.Background()

	reason := "damaged during assembly"
	breakage := []domain.BreakageRecord{{
		DrawID: drawID,
		Qty:    3,
		Kind:   domain.BreakageBroken,
		Reason: &reason,
	}}

	if err := repo.TransitionStatus(ctx, sc, projID, domain.ProjectStatusCancelled, breakage); err != nil {
		t.Fatalf("TransitionStatus cancelled: %v", err)
	}

	// Only the breakage qty (3) is permanently consumed; remaining_qty decrements by 3.
	var remaining int64
	if err := testPool.QueryRow(ctx, `SELECT remaining_qty FROM lot WHERE id = $1`, lotID).Scan(&remaining); err != nil {
		t.Fatalf("query remaining_qty: %v", err)
	}
	if remaining != 7 { // 10 - 3
		t.Errorf("remaining_qty: got %d, want 7", remaining)
	}

	// breakage_event must record kind='broken' (not build_consumed).
	var kind string
	var evtQty int64
	if err := testPool.QueryRow(ctx,
		`SELECT kind, qty FROM breakage_event WHERE draw_id = $1`, drawID).Scan(&kind, &evtQty); err != nil {
		t.Fatalf("query breakage_event: %v", err)
	}
	if kind != "broken" {
		t.Errorf("breakage_event kind: got %q, want broken", kind)
	}
	if evtQty != 3 {
		t.Errorf("breakage_event qty: got %d, want 3", evtQty)
	}

	// Project is now cancelled → draws are no longer active claims.
	var status domain.ProjectStatus
	if err := testPool.QueryRow(ctx, `SELECT status FROM project WHERE id = $1`, projID).Scan(&status); err != nil {
		t.Fatalf("query project status: %v", err)
	}
	if status != domain.ProjectStatusCancelled {
		t.Errorf("project status: got %q, want cancelled", status)
	}
}
