package postgres_test

import (
	"context"
	"errors"
	"testing"

	"ProjectManager/internal/domain"
	"ProjectManager/internal/postgres"
)

func TestProjectRequirementRepo_Add_and_List(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partA := seedCanonicalPart(t)
	partB := seedCanonicalPart(t)
	ctx := context.Background()

	projRepo := postgres.NewProjectRepo(testPool)
	proj, err := projRepo.Create(ctx, sc, &domain.Project{Name: "p"})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	repo := postgres.NewProjectRequirementRepo(testPool)

	reqA, err := repo.Add(ctx, sc, &domain.ProjectRequirement{
		ProjectID: proj.ID, CanonicalPartID: partA, QtyRequired: 5,
	})
	if err != nil {
		t.Fatalf("Add partA: %v", err)
	}
	if reqA.ID == 0 {
		t.Fatal("reqA.ID is zero")
	}

	if _, err := repo.Add(ctx, sc, &domain.ProjectRequirement{
		ProjectID: proj.ID, CanonicalPartID: partB, QtyRequired: 10,
	}); err != nil {
		t.Fatalf("Add partB: %v", err)
	}

	reqs, err := repo.List(ctx, sc, proj.ID)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(reqs) != 2 {
		t.Fatalf("want 2 requirements, got %d", len(reqs))
	}
}

func TestProjectRequirementRepo_Add_DuplicatePart(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partID := seedCanonicalPart(t)
	ctx := context.Background()

	proj, err := postgres.NewProjectRepo(testPool).Create(ctx, sc, &domain.Project{Name: "p"})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	repo := postgres.NewProjectRequirementRepo(testPool)

	req := &domain.ProjectRequirement{ProjectID: proj.ID, CanonicalPartID: partID, QtyRequired: 5}
	if _, err := repo.Add(ctx, sc, req); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	_, err = repo.Add(ctx, sc, req)
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("expected ErrConflict on duplicate, got %v", err)
	}
}

func TestProjectRequirementRepo_Add_ScopeViolation(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	// Owner creates the project.
	ownerUserID, scopeID := seedTenancy(t)
	ownerSC := domain.ScopeCtx{UserID: ownerUserID, ScopeID: scopeID}
	ctx := context.Background()

	proj, err := postgres.NewProjectRepo(testPool).Create(ctx, ownerSC, &domain.Project{Name: "p"})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}

	// Outsider attempts to add a requirement using a different scope.
	_, otherScopeID := seedTenancy(t)
	outsiderSC := domain.ScopeCtx{UserID: ownerUserID, ScopeID: otherScopeID}

	partID := seedCanonicalPart(t)
	repo := postgres.NewProjectRequirementRepo(testPool)
	_, err = repo.Add(ctx, outsiderSC, &domain.ProjectRequirement{
		ProjectID: proj.ID, CanonicalPartID: partID, QtyRequired: 1,
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound for scope violation, got %v", err)
	}
}

func TestProjectRequirementRepo_Remove_Planning(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partID := seedCanonicalPart(t)
	ctx := context.Background()

	proj, err := postgres.NewProjectRepo(testPool).Create(ctx, sc, &domain.Project{Name: "p"})
	if err != nil {
		t.Fatalf("Create project: %v", err)
	}
	repo := postgres.NewProjectRequirementRepo(testPool)
	req, err := repo.Add(ctx, sc, &domain.ProjectRequirement{
		ProjectID: proj.ID, CanonicalPartID: partID, QtyRequired: 5,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := repo.Remove(ctx, sc, req.ID); err != nil {
		t.Fatalf("Remove on planning project: %v", err)
	}

	reqs, err := repo.List(ctx, sc, proj.ID)
	if err != nil {
		t.Fatalf("List after remove: %v", err)
	}
	if len(reqs) != 0 {
		t.Errorf("want 0 requirements after remove, got %d", len(reqs))
	}
}

func TestProjectRequirementRepo_Remove_ActiveProject_Blocked(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	// seedActiveProjectWithDraw creates an active project with a draw.
	sc, _, _, reqID, _ := seedActiveProjectWithDraw(t, 10, 5)
	repo := postgres.NewProjectRequirementRepo(testPool)

	err := repo.Remove(context.Background(), sc, reqID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("expected ErrConflict removing from active project, got %v", err)
	}
}

// TestProjectRequirementRepo_Remove_ConsumedHistory_Blocked verifies that a requirement
// whose draws have consumed_qty > 0 cannot be removed, even on a non-active project.
// This protects the inventory audit trail: if parts were spent against a requirement,
// that requirement is historical record.
func TestProjectRequirementRepo_Remove_ConsumedHistory_Blocked(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	sc, projID, lotID, reqID, drawID := seedActiveProjectWithDraw(t, 10, 7)
	ctx := context.Background()

	// Simulate a partial consumption: set consumed_qty > 0 on the draw and decrement
	// the lot, as the active→paused reconciliation path would do.
	if _, err := testPool.Exec(ctx, `UPDATE draw SET consumed_qty = 3 WHERE id = $1`, drawID); err != nil {
		t.Fatalf("update draw consumed_qty: %v", err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE lot SET remaining_qty = remaining_qty - 3 WHERE id = $1`, lotID); err != nil {
		t.Fatalf("update lot remaining_qty: %v", err)
	}
	// Transition project to paused (non-active, so the active guard alone would allow removal).
	if _, err := testPool.Exec(ctx, `UPDATE project SET status = 'paused' WHERE id = $1`, projID); err != nil {
		t.Fatalf("update project status: %v", err)
	}

	repo := postgres.NewProjectRequirementRepo(testPool)
	err := repo.Remove(ctx, sc, reqID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("expected ErrConflict for consumed history, got %v", err)
	}
}

func TestProjectRequirementRepo_Remove_NotFound(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	repo := postgres.NewProjectRequirementRepo(testPool)

	err := repo.Remove(context.Background(), sc, 99999)
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}
