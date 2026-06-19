package domain

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// Numeric holds an exact decimal value from a Postgres NUMERIC column,
// represented as its string form (e.g. "12.3400"). Repositories convert
// to/from pgtype.Numeric at the DB boundary. Never use float64 for money.
type Numeric string

// Availability is the derived inventory position for one (scope, part) pair.
// Available may be negative if the no-oversell invariant was breached —
// never floor it to zero; negative means corruption upstream.
type Availability struct {
	ScopeID         uuid.UUID
	CanonicalPartID uuid.UUID
	OnHand          int64 // physical stock: sum of remaining_qty across lots
	Available       int64 // on_hand minus active (uncommitted) claims
}

// RequirementCoverage summarises whether a single project requirement is met
// by current available inventory (exact-match only; substitute-aware view deferred).
type RequirementCoverage struct {
	ProjectID       int64
	CanonicalPartID uuid.UUID
	QtyRequired     int64
	Available       int64
	Satisfied       bool
}

// LotClaim is one lot's contribution to satisfying a draw claim.
type LotClaim struct {
	LotID int64
	Qty   int64
}

// BreakageRecord is the input for recording consumed or broken parts during
// a project status transition. Qty must be ≤ draw.qty − draw.consumed_qty.
type BreakageRecord struct {
	DrawID int64
	Qty    int64
	Kind   BreakageKind
	Reason *string
}

// CanonicalPartRepository operates on the global part catalog.
// No ScopeCtx — canonical parts are shared across all teams and scopes.
type CanonicalPartRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*CanonicalPart, error)
	GetByMPNAndManufacturer(ctx context.Context, mpn, manufacturer string) (*CanonicalPart, error)
	Create(ctx context.Context, part *CanonicalPart) (*CanonicalPart, error)
	ListUnresolvedDatasheets(ctx context.Context) ([]*CanonicalPart, error)
	UpdateDatasheetURL(ctx context.Context, id uuid.UUID, url string, resolvedAt time.Time) error
}

// OfferingRepository operates on the global offering catalog. No ScopeCtx.
type OfferingRepository interface {
	GetByID(ctx context.Context, id uuid.UUID) (*Offering, error)
	// Upsert inserts or updates on (source, source_external_id).
	Upsert(ctx context.Context, offering *Offering) (*Offering, error)
	ListUnmapped(ctx context.Context) ([]*Offering, error)
	SetCanonicalPart(ctx context.Context, offeringID, partID uuid.UUID) error
	GetKitContents(ctx context.Context, offeringID uuid.UUID) ([]*KitContent, error)
}

// PurchaseOrderRepository operates on scoped purchase orders.
type PurchaseOrderRepository interface {
	GetByID(ctx context.Context, sc ScopeCtx, id int64) (*PurchaseOrder, error)
	// Upsert inserts or updates on (scope_id, source, source_order_id).
	Upsert(ctx context.Context, sc ScopeCtx, po *PurchaseOrder) (*PurchaseOrder, error)
	List(ctx context.Context, sc ScopeCtx) ([]*PurchaseOrder, error)
	AddLine(ctx context.Context, sc ScopeCtx, line *OrderLine) (*OrderLine, error)
}

// LotRepository manages inventory lots. Lots are scoped through their parent
// order line; the repository resolves scope via join.
type LotRepository interface {
	GetByID(ctx context.Context, sc ScopeCtx, id int64) (*Lot, error)
	// ExplodeOrderLine creates lots from a delivered order line.
	// Non-kit lines produce one lot; kit lines produce N lots (one per KitContent).
	// Kit-derived lots are created with UnitCost = nil.
	ExplodeOrderLine(ctx context.Context, sc ScopeCtx, line *OrderLine, offering *Offering, kitContents []*KitContent) ([]*Lot, error)
	ListByPart(ctx context.Context, sc ScopeCtx, partID uuid.UUID) ([]*Lot, error)
	GetAvailability(ctx context.Context, sc ScopeCtx, partID uuid.UUID) (*Availability, error)
}

// ProjectRepository manages scoped projects and their status lifecycle.
type ProjectRepository interface {
	GetByID(ctx context.Context, sc ScopeCtx, id int64) (*Project, error)
	Create(ctx context.Context, sc ScopeCtx, project *Project) (*Project, error)
	List(ctx context.Context, sc ScopeCtx) ([]*Project, error)
	// TransitionStatus atomically changes a project's status and reconciles its
	// draws in one transaction. breakage records parts consumed during the transition.
	// Returns ErrInvalidTransition if the move is not in the allowed set.
	TransitionStatus(ctx context.Context, sc ScopeCtx, id int64, to ProjectStatus, breakage []BreakageRecord) error
}

// ProjectRequirementRepository manages the BOM requirements within a scoped project.
//
// Remove is blocked when the project is active (live hard claims exist), or
// when any draw for the requirement has consumed_qty > 0 (inventory has been
// permanently spent against it — the requirement is now historical record).
// The DB also blocks via FK RESTRICT on breakage_event → draw, but the explicit
// guard above returns a clear ErrConflict before reaching the constraint.
type ProjectRequirementRepository interface {
	Add(ctx context.Context, sc ScopeCtx, req *ProjectRequirement) (*ProjectRequirement, error)
	List(ctx context.Context, sc ScopeCtx, projectID int64) ([]*ProjectRequirement, error)
	Remove(ctx context.Context, sc ScopeCtx, id int64) error
}

// DrawRepository manages draw claims against lots.
type DrawRepository interface {
	// ClaimLots creates or accumulates draw claims for a requirement against
	// specific lots. MUST run SELECT...FOR UPDATE on target lots to serialize
	// concurrent claims. Returns ErrOversell if any lot lacks sufficient stock.
	ClaimLots(ctx context.Context, sc ScopeCtx, requirementID int64, claims []LotClaim, via *SatisfiedViaKind, substitutionID *int64) error
	// Release reduces a draw's qty by qty. Deletes the draw row if the result
	// would equal consumed_qty (no unclaimed qty remains).
	// Returns ErrForbidden if the draw is outside the caller's scope.
	Release(ctx context.Context, sc ScopeCtx, drawID int64, qty int64) error
	GetCoverage(ctx context.Context, sc ScopeCtx, projectID int64) ([]*RequirementCoverage, error)
}
