package engine

import (
	"os"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/methodology"
)

// rolesYAML declares who may run its agents and actions with its own roles (ADR 0043).
var rolesYAML = func() string {
	b, err := os.ReadFile("testdata/roles.yaml")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

func loadMethodology(t *testing.T, file string) *methodology.Compiled {
	t.Helper()
	path := "../../methodologies/" + file
	if _, err := os.Stat(path); err != nil { // the examples are not seeded at start: they live in methodologies/examples
		path = "../../methodologies/examples/" + file
	}
	m, err := methodology.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func mustCasbin(t *testing.T) authz.Authorizer {
	t.Helper()
	c, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
