package enginee2e

import (
	"os"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/engine/engine"
)

// readRoles reads the methodology with roles the engine's own tests use too.
func readRoles() string {
	b, err := os.ReadFile("../../../engine/engine/testdata/roles.yaml")
	if err != nil {
		panic(err)
	}
	return string(b)
}

func scopeEngine(a authz.Authorizer) *engine.Engine {
	return &engine.Engine{Scope: engine.AuthzScope{Authz: a}, Store: engine.NewMemoryStore()}
}
