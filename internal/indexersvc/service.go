// Package indexersvc is the node index service (ADR 0026): it follows the node and baseline events of
// the graph, embeds the documents through the model gateway and answers searches.
package indexersvc

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zimwip/goap/pkg/access"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/index"
	"github.com/zimwip/goap/pkg/llm"
)

// Subjects the indexer consumes.
var Subjects = []string{"goap.node.>", "goap.baseline.>", "goap.changeindex.>"}

// Access is what the service asks of the organisation to authorize and scope a search (ADR 0095): implemented by
// access.Directory. The index itself knows no organisation.
type Access interface {
	// MayAccessProject reports whether the principal may work on a project (a platform role, or a role on the project
	// or above it; administrators always).
	MayAccessProject(ctx context.Context, p authz.Principal, project string) (bool, error)
	// SubProjects returns a project and the projects below it.
	SubProjects(ctx context.Context, project string) ([]string, error)
}

// Service ties the store, the indexer and the searcher.
type Service struct {
	Store   index.Store
	Log     *slog.Logger
	Indexer *index.Indexer
	// Searcher is built by New; Authz filters its hits (nil: none).
	Searcher *index.Searcher
	// Republish asks the graph to publish its nodes again (Reindex); nil: not available.
	Republish func(ctx context.Context) (int, error)
	// StoreName is shown by Status.
	StoreName string
	// Access decides which projects a caller may see and expands "a project and its sub-projects"; set it before
	// serving. Nil: no project check (tests, and a deployment with no organisation to ask).
	Access Access

	embed    *Embedder
	nodes    atomic.Int64
	baseline atomic.Int64
	changes  atomic.Int64
	errs     atomic.Int64
}

// New builds the service. embedder may be nil (text-only index); authz may be nil (no filtering).
func New(store index.Store, embedder llm.Embedder, authorizer authz.Authorizer, log *slog.Logger) *Service {
	s := &Service{Store: store, Log: log, StoreName: fmt.Sprintf("%T", store)}
	var e index.Embedder
	if embedder != nil {
		s.embed = &Embedder{Client: embedder, Store: store, Log: log}
		e = s.embed
	}
	s.Indexer = &index.Indexer{Store: store, Embedder: e, EmbedFailed: func(key string, err error) {
		s.errs.Add(1)
		log.Debug("embedding skipped", "key", key, "err", err)
	}}
	s.Searcher = &index.Searcher{Store: store, Embedder: e}
	if authorizer != nil {
		s.Searcher.Authorize = readFilter(authorizer, func() Access { return s.Access })
	}
	return s
}

// readFilter keeps the documents the caller may see (ABAC, ADR 0095), before anything is counted or quoted:
//   - a node: action "read" on its type, in its namespace, and access to the project it was created in (a platform
//     role, or a role on the project or above it; administrators see all);
//   - a change: a personal one (held by a personal unit, ADR 0037) only for its subject, administrators included; any
//     other one needs access to its project.
//
// Callers without identity are trusted internal services and see everything (the gateway always identifies the
// callers of the public API; the index port is not exposed).
func readFilter(a authz.Authorizer, access func() Access) index.Authorizer {
	return func(ctx context.Context, hits []index.Hit) ([]index.Hit, error) {
		who := authz.From(ctx)
		if who.Anonymous() {
			return hits, nil
		}
		type key struct{ typ, ns string }
		decided := map[key]bool{}
		projects := map[string]bool{}
		mayProject := func(project string) (bool, error) {
			acc := access()
			if project == "" || acc == nil {
				return true, nil
			}
			ok, seen := projects[project]
			if !seen {
				var err error
				if ok, err = acc.MayAccessProject(ctx, who, project); err != nil {
					return false, err
				}
				projects[project] = ok
			}
			return ok, nil
		}
		out := hits[:0:0]
		for _, h := range hits {
			if h.Kind == index.KindChange {
				if h.PersonalTo != "" {
					if h.PersonalTo == who.Subject {
						out = append(out, h)
					}
					continue
				}
				if ok, err := mayProject(h.Project); err != nil {
					return nil, err
				} else if ok {
					out = append(out, h)
				}
				continue
			}
			k := key{h.Type, h.Namespace}
			ok, seen := decided[k]
			if !seen {
				var err error
				if ok, err = a.Authorize(ctx, authz.Request{Subject: who, Action: "read", Resource: authz.Resource{Type: h.Type, Namespace: h.Namespace}}); err != nil {
					return nil, err
				}
				decided[k] = ok
			}
			if !ok {
				continue
			}
			if ok, err := mayProject(h.Project); err != nil {
				return nil, err
			} else if ok {
				out = append(out, h)
			}
		}
		return out, nil
	}
}

// Handle processes one event of the bus: the subject tells its kind. Unknown subjects are ignored.
func (s *Service) Handle(ctx context.Context, subject string, data []byte) error {
	switch {
	case strings.HasPrefix(subject, "goap.node."):
		var ev domain.NodeEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil // a malformed event is not worth a redelivery
		}
		return s.count(&s.nodes, s.Indexer.OnNode(ctx, ev))
	case strings.HasPrefix(subject, "goap.baseline."):
		var ev domain.BaselineEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		return s.count(&s.baseline, s.Indexer.OnBaseline(ctx, ev))
	case strings.HasPrefix(subject, "goap.changeindex."):
		var ev domain.ChangeDocEvent
		if err := json.Unmarshal(data, &ev); err != nil {
			return nil
		}
		return s.count(&s.changes, s.Indexer.OnChange(ctx, ev, access.PersonalSubject(ev.OwnerOrg)))
	}
	return nil
}

func (s *Service) count(c *atomic.Int64, err error) error {
	if err != nil {
		s.errs.Add(1)
		return err
	}
	c.Add(1)
	return nil
}

// Reindex empties the index and has the graph publish its nodes and changes again.
func (s *Service) Reindex(ctx context.Context) (int, error) {
	if s.Republish == nil {
		return 0, fmt.Errorf("reindex is not available")
	}
	if err := s.Store.Reset(ctx); err != nil {
		return 0, err
	}
	if s.embed != nil {
		s.embed.reset()
	}
	return s.Republish(ctx)
}

// Status reports what the service does.
func (s *Service) Status() (semantic bool, store string, nodes, baselines, changes, errs int64) {
	return s.embed != nil && s.embed.Available(), s.StoreName, s.nodes.Load(), s.baseline.Load(), s.changes.Load(), s.errs.Load()
}

// Sink is a graph.EventSink for the all-in-one mode: events are handled in order by one goroutine,
// off the writer's path (embedding calls are slow).
type Sink struct {
	svc *Service
	ch  chan func()
}

// NewSink starts the worker; it stops with ctx.
func NewSink(ctx context.Context, svc *Service) *Sink {
	k := &Sink{svc: svc, ch: make(chan func(), 4096)}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-k.ch:
				f()
			}
		}
	}()
	return k
}

// Publish implements graph.EventSink.
func (k *Sink) Publish(ctx context.Context, subject string, v any) error {
	if !strings.HasPrefix(subject, "goap.node.") && !strings.HasPrefix(subject, "goap.baseline.") && !strings.HasPrefix(subject, "goap.changeindex.") {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	ctx = context.WithoutCancel(ctx)
	k.ch <- func() {
		if err := k.svc.Handle(ctx, subject, data); err != nil {
			k.svc.Log.Warn("index event", "subject", subject, "err", err)
		}
	}
	return nil
}

// Embedder adapts the model gateway to index.Embedder. After a failure (no embedding model configured,
// quota…) it stays quiet for a while instead of calling the gateway for every node; the first success
// makes the store prepare its vector index for the model's dimension.
type Embedder struct {
	Client llm.Embedder
	Model  string // alias or provider/model; empty: the "embed" alias
	Store  index.Store
	Log    *slog.Logger

	mu    sync.Mutex
	until time.Time
	dim   int
	ok    bool
}

// retryAfter is how long a failed embedder is left alone.
const retryAfter = 30 * time.Second

// Embed implements index.Embedder.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	if time.Now().Before(e.until) {
		e.mu.Unlock()
		return nil, fmt.Errorf("embedding model unavailable, retrying later")
	}
	e.mu.Unlock()
	r, err := e.Client.Embed(llm.WithMeta(ctx, llm.CallMeta{Source: llm.SourceIndexer}), llm.EmbedRequest{Model: e.Model, Texts: texts})
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.until, e.ok = time.Now().Add(retryAfter), false
		if e.Log != nil {
			e.Log.Warn("embedding unavailable: indexing text only", "err", err)
		}
		return nil, err
	}
	e.ok = true
	if len(r.Vectors) > 0 && len(r.Vectors[0]) != e.dim {
		e.dim = len(r.Vectors[0])
		if p, ok := e.Store.(interface {
			EnsureVectorIndex(context.Context, int) error
		}); ok {
			if err := p.EnsureVectorIndex(ctx, e.dim); err != nil && e.Log != nil {
				e.Log.Error("vector index", "dim", e.dim, "err", err)
			}
		}
	}
	return r.Vectors, nil
}

// Available tells whether the last embedding call worked.
func (e *Embedder) Available() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ok
}

func (e *Embedder) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.until, e.dim = time.Time{}, 0
}
