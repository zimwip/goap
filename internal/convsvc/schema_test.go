package convsvc

import (
	"testing"

	"github.com/zimwip/goap/internal/sqlschematest"
)

func TestSchemasAligned(t *testing.T) {
	sqlschematest.AssertAligned(t, "migrations", "migrations_sqlite")
}
