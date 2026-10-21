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
	"time"

	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/lib/cache"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// CompiledBounds are the bounds of the caches of compiled methodologies (the registry's and its clients', lib/cache):
// a compile is only a cost, never a result to keep.
var CompiledBounds = cache.Bounds{Bytes: 64 << 20, TTL: 10 * time.Minute}

// compiledBytes estimates the memory a compiled methodology holds from the size of its definition: the definition, its
// CEL programs and indexes, about eight times it (an estimate, not a measure).
func compiledBytes(definition int) int64 { return 8 * int64(definition) }

// compileKey identifies one compilation: the definition (name, version and a hash of its content: a draft is
// replaced in place) and the types it was compiled against (the published domains in force, name@version each).
type compileKey struct {
	name, version, content, types string
}

type compileEntry struct {
	c     *methodology.Compiled
	err   error
	bytes int64 // estimated
}

// compileCache holds the compiled methodologies of the registry (ADR 0072): Methodology and List are called on the
// hot paths of the engine (a companion scan per event, a specialization lookup per action) and a compile is the
// expensive part. The key makes a stale hit impossible (a new content or a domain republish is another key); the
// service also empties it on every write it publishes, to let go of what can no longer be asked for. Safe for
// concurrent use.
type compileCache struct {
	once    sync.Once
	entries *cache.Cache[compileKey, compileEntry]
	// compiles counts the compilations done on a miss (a test seam: a second read must not add one).
	compiles atomic.Int64
}

func (c *compileCache) cache() *cache.Cache[compileKey, compileEntry] {
	c.once.Do(func() {
		c.entries = cache.New(cache.Fixed(CompiledBounds), func(_ compileKey, e compileEntry) int64 { return e.bytes })
	})
	return c.entries
}

func (c *compileCache) get(k compileKey) (compileEntry, bool) { return c.cache().Get(k) }

func (c *compileCache) put(k compileKey, e compileEntry) { c.cache().Put(k, e) }

func (c *compileCache) clear() { c.cache().Clear() }

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

// contentHash is the hash of a definition and its size.
func contentHash(m methodology.Methodology) (string, int) {
	b, err := json.Marshal(m)
	if err != nil {
		return "", 0 // not cacheable under a shared key: the caller compiles anyway
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), len(b)
}

// Activate builds the activable form of a methodology version from its definition and the domains it compiles against
// (ADR 0126): the one compilation of a stored version, run by the registry and by its clients (Client.Methodology)
// with the same inputs. It is the refusal of the compiler, or of the type catalogue when the domains do not make one
// together.
func Activate(m methodology.Methodology, ds []*def.Domain) (*methodology.Compiled, error) {
	cat, err := typecat.New(ds...)
	if err != nil {
		return nil, err
	}
	return resolve(m, cat).Compile()
}

// CompileDomains are the domain versions a stored methodology version compiles against: the ones it pins, else the
// ones in force at its publication, else the ones in force (ADR 0113).
func (s *Service) CompileDomains(ctx context.Context, r Record) ([]*def.Domain, error) {
	inForce, err := s.domainsInForce(ctx)
	if err != nil {
		return nil, err
	}
	return s.domainsAmong(ctx, &r.Methodology, r.PublishedAt, inForce)
}

// Methodology implements engine.MethodologyPort for in-process use: the version a reference names (domain.MethodologyRef,
// ADR 0113), else the latest published one, compiled against the domain versions it pins once for as long as neither
// it nor those types change. A published version the domains in force break is suspended (ErrSuspended, ADR 0125 §3).
func (s *Service) Methodology(ctx context.Context, ref string) (*methodology.Compiled, error) {
	r, err := s.record(ctx, ref)
	if err != nil {
		return nil, engine.ErrUnknownMethodology{Name: ref}
	}
	inForce, err := s.domainsInForce(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := s.domainsAmong(ctx, &r.Methodology, r.PublishedAt, inForce)
	if err != nil {
		return nil, err
	}
	c, broken := s.compileAgainst(r, ds)
	if broken == nil && r.Status == StatusPublished {
		_, broken = s.compileAgainst(r, inForce) // suspended (ADR 0125 §3): the same compile when it pins what is in force
	}
	if broken != nil && r.Status == StatusPublished {
		return nil, suspendedError{ref: key(r.Methodology.Name, r.Methodology.Version), cause: broken}
	}
	return c, broken
}

// compileAgainst compiles a stored version against domains, through the cache: the refusal of the compiler, or of the
// type catalogue when the domains do not make one together.
func (s *Service) compileAgainst(r Record, ds []*def.Domain) (*methodology.Compiled, error) {
	content, size := contentHash(r.Methodology)
	k := compileKey{name: r.Methodology.Name, version: r.Methodology.Version, content: content, types: typesStamp(ds)}
	if k.content != "" {
		if e, ok := s.compiled.get(k); ok {
			return e.c, e.err
		}
	}
	s.compiled.compiles.Add(1)
	c, err := Activate(r.Methodology, ds)
	if k.content != "" {
		s.compiled.put(k, compileEntry{c: c, err: err, bytes: compiledBytes(size)})
	}
	return c, err
}
