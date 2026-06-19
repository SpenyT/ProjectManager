package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"ProjectManager/internal/domain"
	"ProjectManager/internal/postgres"
)

func TestLotRepo_ExplodeOrderLine_Single(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partID := seedCanonicalPart(t)
	offeringID := seedOffering(t, partID)
	orderID := seedPurchaseOrder(t, scopeID)
	lineID := seedOrderLine(t, orderID, offeringID, 20)

	repo := postgres.NewLotRepo(testPool)
	ctx := context.Background()

	line := &domain.OrderLine{
		ID: lineID, OrderID: orderID, OfferingID: offeringID,
		Qty: 20, UnitPrice: "1.5000", Currency: "USD",
	}
	offering := &domain.Offering{ID: offeringID, CanonicalPartID: &partID, IsKit: false}

	lots, err := repo.ExplodeOrderLine(ctx, sc, line, offering, nil)
	if err != nil {
		t.Fatalf("ExplodeOrderLine: %v", err)
	}
	if len(lots) != 1 {
		t.Fatalf("want 1 lot, got %d", len(lots))
	}
	l := lots[0]
	if l.QtyReceived != 20 || l.RemainingQty != 20 {
		t.Errorf("qty: received=%d remaining=%d, want 20/20", l.QtyReceived, l.RemainingQty)
	}
	if l.UnitCost == nil || string(*l.UnitCost) != "1.5000" {
		t.Errorf("UnitCost: got %v, want 1.5000", l.UnitCost)
	}
	if l.CanonicalPartID != partID {
		t.Errorf("canonical_part_id mismatch")
	}
}

func TestLotRepo_ExplodeOrderLine_Kit(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partA := seedCanonicalPart(t)
	partB := seedCanonicalPart(t)
	kitID := seedKitOffering(t)
	ctx := context.Background()

	if _, err := testPool.Exec(ctx, `
		INSERT INTO kit_content (offering_id, canonical_part_id, qty_per_unit)
		VALUES ($1,$2,2), ($1,$3,5)`, kitID, partA, partB); err != nil {
		t.Fatalf("seed kit_content: %v", err)
	}

	orderID := seedPurchaseOrder(t, scopeID)
	lineID := seedOrderLine(t, orderID, kitID, 3) // 3 kits purchased

	repo := postgres.NewLotRepo(testPool)
	line := &domain.OrderLine{
		ID: lineID, OrderID: orderID, OfferingID: kitID,
		Qty: 3, UnitPrice: "10.0000", Currency: "USD",
	}
	offering := &domain.Offering{ID: kitID, IsKit: true}
	contents := []*domain.KitContent{
		{OfferingID: kitID, CanonicalPartID: partA, QtyPerUnit: 2},
		{OfferingID: kitID, CanonicalPartID: partB, QtyPerUnit: 5},
	}

	lots, err := repo.ExplodeOrderLine(ctx, sc, line, offering, contents)
	if err != nil {
		t.Fatalf("ExplodeOrderLine kit: %v", err)
	}
	if len(lots) != 2 {
		t.Fatalf("want 2 lots, got %d", len(lots))
	}

	byPart := map[uuid.UUID]*domain.Lot{}
	for _, l := range lots {
		byPart[l.CanonicalPartID] = l
	}
	if byPart[partA] == nil || byPart[partA].QtyReceived != 6 { // 3×2
		t.Errorf("partA qty: got %v, want 6", byPart[partA])
	}
	if byPart[partB] == nil || byPart[partB].QtyReceived != 15 { // 3×5
		t.Errorf("partB qty: got %v, want 15", byPart[partB])
	}
	for _, l := range lots {
		if l.UnitCost != nil {
			t.Errorf("kit lot unit_cost must be nil, got %v", *l.UnitCost)
		}
	}
}

// TestLotRepo_ExplodeOrderLine_Kit_Atomic verifies that a kit explosion is all-or-nothing.
//
// Sequence: lot #1 INSERT (valid partA) succeeds; lot #2 INSERT (non-existent UUID) fails
// with a FK violation. Without the transaction, lot #1 is already auto-committed by the
// pool at that point. With the transaction wrapping both inserts, the rollback erases
// lot #1 as well. COUNT = 0 after the error proves genuine partial-commit rollback,
// not whole-batch rejection (lot #1 did succeed before the failure).
func TestLotRepo_ExplodeOrderLine_Kit_Atomic(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partA := seedCanonicalPart(t)
	partB := seedCanonicalPart(t) // valid; used for lot #1 and lot #3
	kitID := seedKitOffering(t)
	ctx := context.Background()
	orderID := seedPurchaseOrder(t, scopeID)
	lineID := seedOrderLine(t, orderID, kitID, 3)

	repo := postgres.NewLotRepo(testPool)
	line := &domain.OrderLine{
		ID: lineID, OrderID: orderID, OfferingID: kitID,
		Qty: 3, UnitPrice: "10.0000", Currency: "USD",
	}
	offering := &domain.Offering{ID: kitID, IsKit: true}

	// Three contents: #1 valid, #2 non-existent canonical_part_id (fails mid-loop),
	// #3 valid but never reached. Proves abort is genuinely mid-stream, not last-fails.
	badPartID := uuid.New()
	contents := []*domain.KitContent{
		{OfferingID: kitID, CanonicalPartID: partA, QtyPerUnit: 2},  // succeeds
		{OfferingID: kitID, CanonicalPartID: badPartID, QtyPerUnit: 1}, // FK violation
		{OfferingID: kitID, CanonicalPartID: partB, QtyPerUnit: 3},  // never reached
	}

	_, err := repo.ExplodeOrderLine(ctx, sc, line, offering, contents)
	if err == nil {
		t.Fatal("expected error for non-existent canonical_part_id")
	}

	// Atomic rollback: lot #1 must not persist despite its INSERT having succeeded.
	var count int
	if err := testPool.QueryRow(ctx,
		`SELECT COUNT(*) FROM lot WHERE source_order_line_id = $1`, lineID).Scan(&count); err != nil {
		t.Fatalf("count lots: %v", err)
	}
	if count != 0 {
		t.Errorf("atomic rollback failed: %d lot(s) persisted, want 0", count)
	}
}

func TestLotRepo_ListByPart(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partID := seedCanonicalPart(t)
	offeringID := seedOffering(t, partID)
	orderID := seedPurchaseOrder(t, scopeID)
	lineID := seedOrderLine(t, orderID, offeringID, 10)
	seedLot(t, partID, lineID, 10)

	repo := postgres.NewLotRepo(testPool)
	lots, err := repo.ListByPart(context.Background(), sc, partID)
	if err != nil {
		t.Fatalf("ListByPart: %v", err)
	}
	if len(lots) != 1 {
		t.Fatalf("want 1 lot, got %d", len(lots))
	}
	if lots[0].QtyReceived != 10 {
		t.Errorf("qty_received: got %d, want 10", lots[0].QtyReceived)
	}
}

func TestLotRepo_GetAvailability(t *testing.T) {
	requireTestDB(t)
	truncateAll(t)

	userID, scopeID := seedTenancy(t)
	sc := domain.ScopeCtx{UserID: userID, ScopeID: scopeID}
	partID := seedCanonicalPart(t)
	offeringID := seedOffering(t, partID)
	orderID := seedPurchaseOrder(t, scopeID)
	lineID := seedOrderLine(t, orderID, offeringID, 10)
	seedLot(t, partID, lineID, 10)

	repo := postgres.NewLotRepo(testPool)
	ctx := context.Background()

	av, err := repo.GetAvailability(ctx, sc, partID)
	if err != nil {
		t.Fatalf("GetAvailability: %v", err)
	}
	if av.OnHand != 10 || av.Available != 10 {
		t.Errorf("on_hand=%d available=%d, want 10/10", av.OnHand, av.Available)
	}
	if av.ScopeID != scopeID {
		t.Errorf("scope_id mismatch")
	}
}
