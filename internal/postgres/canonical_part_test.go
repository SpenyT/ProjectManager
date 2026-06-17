package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"ProjectManager/internal/domain"
	"ProjectManager/internal/postgres"
)

func TestCanonicalPartRepo_Create_and_GetByID(t *testing.T) {
	pool := requireTestDB(t)
	truncateAll(t)

	repo := postgres.NewCanonicalPartRepo(pool)
	ctx := context.Background()

	mpn := "NE555P"
	mfr := "Texas Instruments"
	desc := "Timer IC"
	pkg := "DIP-8"

	created, err := repo.Create(ctx, &domain.CanonicalPart{
		MPN:          &mpn,
		Manufacturer: &mfr,
		Description:  &desc,
		Package:      &pkg,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID.String() == "" {
		t.Fatal("created.ID is zero")
	}
	if created.DatasheetURL != nil || created.DatasheetResolvedAt != nil {
		t.Error("datasheet fields should be nil on creation")
	}

	got, err := repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if *got.MPN != mpn {
		t.Errorf("MPN: got %q, want %q", *got.MPN, mpn)
	}
	if *got.Manufacturer != mfr {
		t.Errorf("Manufacturer: got %q, want %q", *got.Manufacturer, mfr)
	}
}

func TestCanonicalPartRepo_GetByID_NotFound(t *testing.T) {
	pool := requireTestDB(t)
	truncateAll(t)

	repo := postgres.NewCanonicalPartRepo(pool)
	ctx := context.Background()

	_, err := repo.GetByID(ctx, [16]byte{0xFF})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCanonicalPartRepo_GetByMPNAndManufacturer(t *testing.T) {
	pool := requireTestDB(t)
	truncateAll(t)

	repo := postgres.NewCanonicalPartRepo(pool)
	ctx := context.Background()

	mpn := "LM741"
	mfr := "Fairchild"
	if _, err := repo.Create(ctx, &domain.CanonicalPart{MPN: &mpn, Manufacturer: &mfr}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByMPNAndManufacturer(ctx, mpn, mfr)
	if err != nil {
		t.Fatalf("GetByMPNAndManufacturer: %v", err)
	}
	if *got.MPN != mpn {
		t.Errorf("MPN: got %q, want %q", *got.MPN, mpn)
	}
}

func TestCanonicalPartRepo_ListUnresolvedDatasheets(t *testing.T) {
	pool := requireTestDB(t)
	truncateAll(t)

	repo := postgres.NewCanonicalPartRepo(pool)
	ctx := context.Background()

	mpn1, mpn2 := "A001", "B002"
	mfr := "ACME"
	p1, _ := repo.Create(ctx, &domain.CanonicalPart{MPN: &mpn1, Manufacturer: &mfr})
	p2, _ := repo.Create(ctx, &domain.CanonicalPart{MPN: &mpn2, Manufacturer: &mfr})

	// Resolve p2's datasheet.
	if err := repo.UpdateDatasheetURL(ctx, p2.ID, "https://example.com/ds.pdf", time.Now()); err != nil {
		t.Fatalf("UpdateDatasheetURL: %v", err)
	}

	unresolved, err := repo.ListUnresolvedDatasheets(ctx)
	if err != nil {
		t.Fatalf("ListUnresolvedDatasheets: %v", err)
	}
	if len(unresolved) != 1 {
		t.Fatalf("expected 1 unresolved, got %d", len(unresolved))
	}
	if unresolved[0].ID != p1.ID {
		t.Errorf("expected unresolved part %s, got %s", p1.ID, unresolved[0].ID)
	}
}

func TestCanonicalPartRepo_Create_DuplicateMPNManufacturer(t *testing.T) {
	pool := requireTestDB(t)
	truncateAll(t)

	repo := postgres.NewCanonicalPartRepo(pool)
	ctx := context.Background()

	mpn := "NE555P"
	mfr := "TI"
	if _, err := repo.Create(ctx, &domain.CanonicalPart{MPN: &mpn, Manufacturer: &mfr}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	_, err := repo.Create(ctx, &domain.CanonicalPart{MPN: &mpn, Manufacturer: &mfr})
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("expected ErrConflict on duplicate, got %v", err)
	}
}
