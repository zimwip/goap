package registrysvc

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/typecat"
)

// maxCompiled bounds the compile cache: the latest published version of every methodology, with room for the
// versions in use. When it is full the cache starts over (a compile is only a cost, never a result to keep).
const maxCompiled = 256

// compileKey identifies one compilation: the definition (name, version and a hash of its content: a draft is
// replaced in place) and the types it was compiled against (the published domains in force, name@version each).
type compileKey struct {
	name, version, content, types string
}

type compileEntry struct {
	c   *methodology.Compiled
	err error
}

// compileCache holds the compiled methodologies of the registry (ADR 0072): Methodology and List are called on the
// hot paths of the engine (a companion scan per event, a specialization lookup per action) and a compile is the
// expensive part. The key makes a stale hit impossible (a new content or a domain republish is another key); the
// service also empties it on every write it publishes, to let go of what can no longer be asked for. Safe for
// concurrent use.
type compileCache struct {
	mu      sync.Mutex
	entries map[compileKey]compileEntry
	// compiles counts the compilations done on a miss (a test seam: a second read must not add one).
	compiles atomic.Int64
}

func (c *compileCache) get(k compileKey) (compileEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[k]
	return e, ok
}

func (c *compileCache) put(k compileKey, e compileEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil || len(c.entries) >= maxCompiled {
		c.entries = map[compileKey]compileEntry{}
	}
	c.entries[k] = e
}

func (c *compileCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = nil
}

// typesStamp names the types in force: the published domains, name@version each, in name order (a published
// version is immutable, a republish is a new version).
func typesStamp(ds []*def.Domain) string {
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		parts = append(parts, d.Name+"@"+d.Version)
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func contentHash(m methodology.Methodology) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "" // not cacheable under a shared key: the caller compiles anyway
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Methodology implements engine.MethodologyPort for in-process use: the latest published version, compiled once for
// as long as neither it nor the types in force change.
func (s *Service) Methodology(ctx context.Context, name string) (*methodology.Compiled, error) {
	r, err := s.Store.Get(ctx, name, "")
	if err != nil {
		return nil, engine.ErrUnknownMethodology{Name: name}
	}
	ds, err := s.Domains(ctx)
	if err != nil {
		return nil, err
	}
	k := compileKey{name: r.Methodology.Name, version: r.Methodology.Version, content: contentHash(r.Methodology), types: typesStamp(ds)}
	if k.content != "" {
		if e, ok := s.compiled.get(k); ok {
			return e.c, e.err
		}
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		return nil, err
	}
	s.compiled.compiles.Add(1)
	c, err := resolve(r.Methodology, cat).Compile()
	if k.content != "" {
		s.compiled.put(k, compileEntry{c: c, err: err})
	}
	return c, err
}
