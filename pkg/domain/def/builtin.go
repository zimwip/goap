package def

import (
	"fmt"
	"sync"

	"github.com/zimwip/goap/domains/builtin"
)

var (
	builtinOnce sync.Once
	builtinDs   []*Domain
)

// BuiltinDomains returns the domains that ship with the platform (ADR 0012 §4), known before any domain is loaded:
// the meta-domain "methodology", which types the definition nodes of the methodologies (ADR 0023), and "organisation"
// and "platform", whose types the platform itself reads (units, adapters, users, policies; MCPs, adapter definitions,
// model configuration), and "execution", the change object types of the engine's view of a change (ADR 0098). They are frozen: they change with the code (their YAML), the registry never versions them.
func BuiltinDomains() []*Domain {
	builtinOnce.Do(func() {
		for _, f := range []string{"methodology.yaml", "organisation.yaml", "platform.yaml", "execution.yaml"} {
			src, err := builtin.FS.ReadFile(f)
			if err != nil {
				panic(err)
			}
			d, err := ParseDomain(src)
			if err != nil {
				panic(fmt.Errorf("%s: %w", f, err)) // embedded, covered by the tests
			}
			builtinDs = append(builtinDs, d)
		}
	})
	return builtinDs
}

// IsBuiltinDomain reports the name (namespace) of a built-in domain.
func IsBuiltinDomain(name string) bool {
	for _, d := range BuiltinDomains() {
		if d.Name == name {
			return true
		}
	}
	return false
}
