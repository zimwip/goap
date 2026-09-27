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

// BuiltinDomains returns the domains that ship with the platform (ADR 0012 §4), known before any domain is loaded:
// the meta-domains "methodology" and "domain", which type the definition nodes of the methodologies and domains
// (ADR 0023), and "organisation", whose types the platform itself reads (units, adapters, users, policies).
// "methodology" and "organisation" are frozen (see IsFrozenDomain); the embedded "domain" is its initial version,
// which published versions of the registry extend.
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

// frozenDomains are the built-in domains the registry never versions: they change with the code (their YAML).
var frozenDomains = []string{"methodology", "organisation"}

// IsFrozenDomain reports a built-in domain that only changes with the code: no version is saved in the registry.
func IsFrozenDomain(name string) bool {
	for _, n := range frozenDomains {
		if n == name {
			return true
		}
	}
	return false
}

// BuiltinDomain returns the embedded version of a built-in domain (nil for another name).
func BuiltinDomain(name string) *Domain {
	for _, d := range BuiltinDomains() {
		if d.Name == name {
			return d
		}
	}
	return nil
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
