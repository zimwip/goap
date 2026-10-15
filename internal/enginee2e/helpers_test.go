package enginee2e

import (
	"os"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/engine"
)

// readRoles reads the methodology with roles the engine's own tests use too.
func readRoles() string {
	b, err := os.ReadFile("../../pkg/engine/testdata/roles.yaml")
	if err != nil {
		panic(err)
	}
	return string(b)
}

func scopeEngine(a authz.Authorizer) *engine.Engine {
	return &engine.Engine{Scope: engine.AuthzScope{Authz: a}, Store: engine.NewMemoryStore()}
}
