package domain

import (
	"time"

	"github.com/google/uuid"
)

// OrderStatus is the lifecycle state of a purchase order.
type OrderStatus string

// OrderStatus constants track delivery progress of a purchase order.
const (
	OrderStatusOrdered   OrderStatus = "ordered"
	OrderStatusShipped   OrderStatus = "shipped"
	OrderStatusDelivered OrderStatus = "delivered"
	OrderStatusCancelled OrderStatus = "cancelled"
)

// PurchaseOrder is the aggregate root for a purchase. It carries ScopeID,
// establishing ownership for all child order lines and lots.
type PurchaseOrder struct {
	ID            int64
	ScopeID       uuid.UUID
	Source        Source
	SourceOrderID string
	Status        OrderStatus
	PlacedAt      *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// OrderLine is one line item within a purchase order. Currency is the
// authoritative record of what this purchase cost.
type OrderLine struct {
	ID         int64
	OrderID    int64
	OfferingID uuid.UUID
	Qty        int64
	UnitPrice  Numeric
	Currency   string
	CreatedAt  time.Time
}

// Lot is the inventory primitive. All stock exists as lots; "how many do I
// have" is always derived by summing lots via inventory_available.
//
// Kit order lines explode into N lots (one per KitContent row). Kit-derived
// lots have UnitCost = nil — per-part cost is genuinely unknown, not allocated.
//
// RemainingQty = physical stock = QtyReceived − permanently consumed.
// Active reversible claims do NOT reduce RemainingQty; they reduce availability.
type Lot struct {
	ID                int64
	CanonicalPartID   uuid.UUID
	SourceOrderLineID int64
	QtyReceived       int64
	RemainingQty      int64
	UnitCost          *Numeric // nil for kit-derived lots
	Currency          string   // audit snapshot of order_line.currency at creation
	ReceivedAt        *time.Time
	CreatedAt         time.Time
	UpdatedAt         time.Time
}
