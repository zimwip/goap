package methodology

import (
	"embed"
	"fmt"
	"sync"
)

//go:embed meta/*.yaml
var metaFS embed.FS

var (
	metaOnce sync.Once
	metaDs   []*Domain
)

// MetaDomains returns the built-in meta-domains (ADR 0012 §4): "methodology" and "domain", which type the definition
// nodes of the methodologies and domains (ADR 0023). They are known before any domain is loaded.
func MetaDomains() []*Domain {
	metaOnce.Do(func() {
		for _, f := range []string{"meta/methodology.yaml", "meta/domain.yaml"} {
			src, err := metaFS.ReadFile(f)
			if err != nil {
				panic(err)
			}
			d, err := ParseDomain(src)
			if err != nil {
				panic(fmt.Errorf("%s: %w", f, err)) // embedded, covered by the tests
			}
			metaDs = append(metaDs, d)
		}
	})
	return metaDs
}
