package methodology

import (
	"embed"
	"fmt"
	"sync"
)

//go:embed builtin/*.yaml
var builtinFS embed.FS

var (
	builtinOnce sync.Once
	builtinDs   []*Domain
)

// BuiltinDomains returns the domains built into the platform (ADR 0012 §4): the meta-domains "methodology" and
// "domain", which type the definition nodes of the methodologies and domains (ADR 0023), and "organisation", whose
// types the platform itself reads (units, adapters, users, policies). The code depends on them: they ship with it,
// are known before any domain is loaded, and are frozen (no version is saved in the registry).
func BuiltinDomains() []*Domain {
	builtinOnce.Do(func() {
		for _, f := range []string{"builtin/methodology.yaml", "builtin/domain.yaml", "builtin/organisation.yaml"} {
			src, err := builtinFS.ReadFile(f)
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
