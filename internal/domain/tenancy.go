// Package domain defines the core types, interfaces, and business rules
// for the parts tracker. It has no dependency on storage or transport.
package domain

import (
	"time"

	"github.com/google/uuid"
)

// TeamRole is the role a user holds within a team.
type TeamRole string

// TeamRole constants. Admin may perform irreversible shared-inventory actions; member may not.
const (
	RoleAdmin  TeamRole = "admin"
	RoleMember TeamRole = "member"
)

// ScopeCtx carries the authorization context required by every repository
// operation on scoped data. Repositories reject rows whose scope does not
// match ScopeID and whose team does not include UserID.
type ScopeCtx struct {
	UserID  uuid.UUID
	ScopeID uuid.UUID
}

// AppUser is a registered user. Auth credentials are out of scope.
type AppUser struct {
	ID        uuid.UUID
	Email     string
	CreatedAt time.Time
}

// Team is the top-level tenant. Inventory ownership belongs to a team,
// not an individual user. Personal use = a team of one.
type Team struct {
	ID        uuid.UUID
	Name      string
	CreatedAt time.Time
}

// TeamMembership links a user to a team with a role.
type TeamMembership struct {
	TeamID    uuid.UUID
	UserID    uuid.UUID
	Role      TeamRole
	CreatedAt time.Time
}

// InventoryScope is the inventory boundary within a team. Exactly one scope
// per team today ("default"); multi-scope is a future additive change.
type InventoryScope struct {
	ID        uuid.UUID
	TeamID    uuid.UUID
	Name      string
	CreatedAt time.Time
}
