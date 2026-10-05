package graph

import (
	"testing"

	"github.com/zimwip/goap/internal/sqlschematest"
)

// The PostgreSQL and the SQLite schemas of the graph are kept by hand (ADR 0074): they must describe the same tables,
// columns, defaults, keys, constraints and indexes, whatever the spelling of the types.
func TestSchemasAligned(t *testing.T) {
	sqlschematest.AssertAligned(t, "migrations", "migrations_sqlite")
}
