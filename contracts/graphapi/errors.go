package graphapi

import (
	"errors"
	"fmt"
)

var (
	// ErrNotFound is returned when an entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict is returned on optimistic concurrency violations.
	ErrConflict = errors.New("conflict")
	// ErrInvalid is returned on invalid input.
	ErrInvalid = errors.New("invalid")
	// ErrNothingToCommit refuses a commit that would write nothing: no accepted modification, nothing to revert (ADR
	// 0102).
	ErrNothingToCommit = fmt.Errorf("nothing to commit: %w", ErrInvalid)
)
