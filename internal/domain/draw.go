package domain

import (
	"time"

	"github.com/google/uuid"
)

// SatisfiedViaKind records the mechanism by which a draw fulfills a requirement
// with a substitute part. Nil on the Draw means exact-match — no substitution.
type SatisfiedViaKind string

const (
	// SatisfiedFunctional indicates fulfillment via a declared equivalence edge
	// in part_substitution. The specific edge is recorded in Draw.SatisfiedViaSubstitution.
	SatisfiedFunctional SatisfiedViaKind = "functional"

	// SatisfiedParametric indicates fulfillment via computed attribute comparison
	// (e.g. a 20V-rated part satisfying a 12V requirement). No edge stored.
	SatisfiedParametric SatisfiedViaKind = "parametric"
)

// BreakageKind distinguishes why parts were permanently removed from inventory.
type BreakageKind string

// BreakageKind constants distinguish why parts were permanently removed from inventory.
const (
	BreakageBroken        BreakageKind = "broken"
	BreakageUsed          BreakageKind = "used"
	BreakageBuildConsumed BreakageKind = "build_consumed"
)

// Draw links a project requirement to a specific lot. It is a current-state
// fact ("this requirement claims Qty from this lot"), not an event log.
//
// Whether the claim is live is determined by the parent project's status —
// there is no stored draw status. ConsumedQty is the only fact stored beyond
// Qty because permanent consumption cannot be derived from project status.
type Draw struct {
	ID                       int64
	ProjectRequirementID     int64
	LotID                    int64
	Qty                      int64
	ConsumedQty              int64
	SatisfiedViaKind         *SatisfiedViaKind // nil = exact match
	SatisfiedViaSubstitution *int64            // part_substitution.id, set iff functional
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

// BreakageEvent is the audit record for permanently lost or consumed parts.
// Both breakage and build consumption flow through this table (distinguished by Kind).
// LotID is reachable via Draw and intentionally omitted here.
type BreakageEvent struct {
	ID         int64
	DrawID     int64
	Qty        int64
	Kind       BreakageKind
	Reason     *string
	ReportedBy uuid.UUID
	ReportedAt time.Time
}
