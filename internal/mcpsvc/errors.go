package mcpsvc

import (
	"errors"
	"fmt"
)

func errNotFound(what string) error { return fmt.Errorf("%s: %w", what, ErrNotFound) }

var (
	// ErrNotBound is returned when the organization does not bind the MCP of a tool.
	ErrNotBound = errors.New("mcp not bound")
	// ErrLibrary is returned when the algorithm of an adapter cannot be loaded from the library.
	ErrLibrary = errors.New("adapter library")
	// ErrUnavailable is returned when the connector of a binding is not registered or its lease expired.
	ErrUnavailable = errors.New("connector unavailable")
)

// ToolError is a failure reported by the connector for one operation.
type ToolError struct{ Msg string }

func (e *ToolError) Error() string { return e.Msg }
