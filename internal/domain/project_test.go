package domain

import "testing"

func TestProject_IsActive(t *testing.T) {
	tests := []struct {
		status ProjectStatus
		want   bool
	}{
		{ProjectStatusActive, true},
		{ProjectStatusPlanning, false},
		{ProjectStatusPaused, false},
		{ProjectStatusCancelled, false},
		{ProjectStatusBuilt, false},
		{ProjectStatusArchived, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			p := &Project{Status: tt.status}
			if got := p.IsActive(); got != tt.want {
				t.Errorf("Project{Status:%q}.IsActive() = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}

func TestProject_CanTransitionTo(t *testing.T) {
	tests := []struct {
		name    string
		current ProjectStatus
		next    ProjectStatus
		want    bool
	}{
		// valid transitions
		{"planning→active", ProjectStatusPlanning, ProjectStatusActive, true},
		{"planning→cancelled", ProjectStatusPlanning, ProjectStatusCancelled, true},
		{"active→paused", ProjectStatusActive, ProjectStatusPaused, true},
		{"active→cancelled", ProjectStatusActive, ProjectStatusCancelled, true},
		{"active→built", ProjectStatusActive, ProjectStatusBuilt, true},
		{"paused→active", ProjectStatusPaused, ProjectStatusActive, true},
		{"paused→cancelled", ProjectStatusPaused, ProjectStatusCancelled, true},
		{"cancelled→archived", ProjectStatusCancelled, ProjectStatusArchived, true},
		{"built→archived", ProjectStatusBuilt, ProjectStatusArchived, true},
		// invalid transitions
		{"planning→built", ProjectStatusPlanning, ProjectStatusBuilt, false},
		{"planning→paused", ProjectStatusPlanning, ProjectStatusPaused, false},
		{"planning→archived", ProjectStatusPlanning, ProjectStatusArchived, false},
		{"active→planning", ProjectStatusActive, ProjectStatusPlanning, false},
		{"active→archived", ProjectStatusActive, ProjectStatusArchived, false},
		{"paused→built", ProjectStatusPaused, ProjectStatusBuilt, false},
		{"paused→planning", ProjectStatusPaused, ProjectStatusPlanning, false},
		{"built→active", ProjectStatusBuilt, ProjectStatusActive, false},
		{"archived→active", ProjectStatusArchived, ProjectStatusActive, false},
		{"archived→archived", ProjectStatusArchived, ProjectStatusArchived, false},
		{"self active→active", ProjectStatusActive, ProjectStatusActive, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Project{Status: tt.current}
			got := p.CanTransitionTo(tt.next)
			if got != tt.want {
				t.Errorf("Project{Status:%q}.CanTransitionTo(%q) = %v, want %v",
					tt.current, tt.next, got, tt.want)
			}
		})
	}
}
