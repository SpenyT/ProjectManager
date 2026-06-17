package domain

import "errors"

var (
	// ErrNotFound is returned when a requested entity does not exist or is outside the caller's scope.
	ErrNotFound = errors.New("not found")

	// ErrForbidden is returned when the ScopeCtx does not permit the operation.
	ErrForbidden = errors.New("forbidden")

	// ErrOversell is returned when a draw claim would exceed available lot stock.
	// Never suppress or floor this — a negative available value is a corruption signal.
	ErrOversell = errors.New("insufficient lot stock: would oversell")

	// ErrInvalidTransition is returned when a project status move is not in the allowed set.
	ErrInvalidTransition = errors.New("invalid project status transition")

	// ErrConflict is returned on unique-constraint violations (e.g. duplicate upsert race).
	ErrConflict = errors.New("conflict")
)
