package mcpsvc

import (
	"testing"

	"github.com/zimwip/goap/lib/sqlschematest"
)

func TestSchemasAligned(t *testing.T) {
	sqlschematest.AssertAligned(t, "migrations", "migrations_sqlite")
}
