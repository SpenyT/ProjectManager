package domain

import (
	"time"

	"github.com/google/uuid"
)

// Source identifies which external marketplace an offering or order came from.
// A new source must not leak beyond this column — no source-specific logic elsewhere.
type Source string

// Source constants. Adding a new source requires a migration to extend the DB CHECK constraint.
const (
	SourceDigiKey    Source = "digikey"
	SourceMouser     Source = "mouser"
	SourceLCSC       Source = "lcsc"
	SourceAliExpress Source = "aliexpress"
	SourceTemu       Source = "temu"
)

// AttributeDirection is the comparison semantics for a part_attribute axis.
// Betterness is not global — a capacity axis is higher_ok, a supply window is
// range, a package is exact. The ESP32 3.3V case proves "higher wins" is wrong.
type AttributeDirection string

// AttributeDirection constants define comparison semantics per axis.
const (
	DirectionHigherOK AttributeDirection = "higher_ok" // capacity/rating: candidate ≥ required satisfies
	DirectionLowerOK  AttributeDirection = "lower_ok"  // candidate ≤ required satisfies
	DirectionRange    AttributeDirection = "range"     // value must fall within [ValueMin, ValueMax]
	DirectionExact    AttributeDirection = "exact"     // must match exactly (package, pinout)
)

// AttributeSource records the provenance of a part attribute value.
type AttributeSource string

// AttributeSource constants record where a part attribute value originated.
const (
	AttrSourceUser     AttributeSource = "user"
	AttrSourceOctopart AttributeSource = "octopart"
	AttrSourceDigiKey  AttributeSource = "digikey"
	AttrSourceLCSC     AttributeSource = "lcsc"
	AttrSourceComputed AttributeSource = "computed"
)

// SubstitutionSource records where a functional substitution edge came from.
type SubstitutionSource string

// SubstitutionSource constants record where a functional substitution edge was declared.
const (
	SubstSourceUser     SubstitutionSource = "user"
	SubstSourceOctopart SubstitutionSource = "octopart"
	SubstSourceDigiKey  SubstitutionSource = "digikey"
	SubstSourceLCSC     SubstitutionSource = "lcsc"
)

// CanonicalPart is the physical component identity, source-independent.
// Inventory and project requirements are denominated in canonical parts.
// MPN and Manufacturer may be nil for no-name / generic parts.
type CanonicalPart struct {
	ID                  uuid.UUID
	MPN                 *string
	Manufacturer        *string
	Description         *string
	Package             *string
	DatasheetURL        *string
	DatasheetResolvedAt *time.Time
	CreatedAt           time.Time
}

// Offering is a buyable listing from a single source. A purchase references
// an offering. Kits (IsKit=true) yield multiple canonical parts on receipt
// and have no single CanonicalPartID.
type Offering struct {
	ID               uuid.UUID
	Source           Source
	SourceExternalID string
	CanonicalPartID  *uuid.UUID // nil until resolved; always nil for kits
	IsKit            bool
	URL              *string
	Seller           *string
	Title            *string
	LastSeenPrice    *Numeric
	Currency         *string
	LastSeenAt       *time.Time
	CreatedAt        time.Time
}

// KitContent is one component line of a kit's decomposition: this kit offering
// yields QtyPerUnit of CanonicalPartID per kit unit purchased.
type KitContent struct {
	OfferingID      uuid.UUID
	CanonicalPartID uuid.UUID
	QtyPerUnit      int64
}

// PartAttribute is a typed, directional parametric spec on a canonical part.
// Used by the deferred parametric substitution resolver — never stored as graph edges.
type PartAttribute struct {
	ID              int64
	CanonicalPartID uuid.UUID
	Axis            string // e.g. "vmax_rating", "supply_voltage", "package"
	Direction       AttributeDirection
	ValueNum        *Numeric // point value for higher_ok / lower_ok / exact numeric axes
	ValueMin        *Numeric // lower bound for range axes
	ValueMax        *Numeric // upper bound for range axes
	ValueText       *string  // exact non-numeric value (package code, etc.)
	Unit            *string  // "V", "A", "Ω", "°C", …
	Source          AttributeSource
	CreatedAt       time.Time
}

// PartSubstitution is a directed functional equivalence edge:
// SatisfyingPartID can fill a requirement for SatisfiedPartID.
// Asymmetric and user-declared — cannot be derived from specs.
// Surrogate PK so a Draw can FK a specific edge for full traceability.
type PartSubstitution struct {
	ID               int64
	SatisfyingPartID uuid.UUID // the part you HAVE
	SatisfiedPartID  uuid.UUID // the requirement it can fill
	Source           SubstitutionSource
	Note             *string
	CreatedAt        time.Time
}
