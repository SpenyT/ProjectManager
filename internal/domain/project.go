package domain

import (
	"time"

	"github.com/google/uuid"
)

// ProjectStatus is the lifecycle state of a project.
// It is the single source of truth for whether a project's draws are live claims.
type ProjectStatus string

// ProjectStatus constants. Use CanTransitionTo to validate moves between them.
const (
	ProjectStatusPlanning  ProjectStatus = "planning"
	ProjectStatusActive    ProjectStatus = "active"
	ProjectStatusPaused    ProjectStatus = "paused"
	ProjectStatusCancelled ProjectStatus = "cancelled"
	ProjectStatusBuilt     ProjectStatus = "built"
	ProjectStatusArchived  ProjectStatus = "archived"
)

// validTransitions maps each status to the statuses it may move to.
var validTransitions = map[ProjectStatus][]ProjectStatus{
	ProjectStatusPlanning:  {ProjectStatusActive, ProjectStatusCancelled},
	ProjectStatusActive:    {ProjectStatusPaused, ProjectStatusCancelled, ProjectStatusBuilt},
	ProjectStatusPaused:    {ProjectStatusActive, ProjectStatusCancelled},
	ProjectStatusCancelled: {ProjectStatusArchived},
	ProjectStatusBuilt:     {ProjectStatusArchived},
	ProjectStatusArchived:  {},
}

// Project is the aggregate root for demand. Its Status drives whether its
// draws are live hard claims against inventory.
type Project struct {
	ID        int64
	ScopeID   uuid.UUID
	Name      string
	Status    ProjectStatus
	CreatedAt time.Time
	UpdatedAt time.Time
}

// IsActive reports whether the project holds live hard claims against inventory.
func (p *Project) IsActive() bool {
	return p.Status == ProjectStatusActive
}

// CanTransitionTo reports whether moving to next is a permitted status transition.
func (p *Project) CanTransitionTo(next ProjectStatus) bool {
	for _, allowed := range validTransitions[p.Status] {
		if allowed == next {
			return true
		}
	}
	return false
}

// ProjectRequirement declares that a project needs QtyRequired units of a
// canonical part. Scope is inherited through the parent project.
type ProjectRequirement struct {
	ID              int64
	ProjectID       int64
	CanonicalPartID uuid.UUID
	QtyRequired     int64
}
