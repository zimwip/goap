package graphsnap

import (
	"context"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// Heads keeps the heads of the branches of a graph another process holds, followed by its events (ADR 0154 §9: a
// content graph reading the organisation of the platform graph, at every write it places): a head is asked once, then
// again only once Moved says a main branch moved (the graph's commit events), or past MaxAge (an event lost on the
// way; 0: never). A reader of a graph whose BranchHeadHeader goes through Get reads that graph only when it changed.
type Heads struct {
	MaxAge time.Duration

	mu  sync.Mutex
	at  map[[2]string]head
	gen uint64 // bumped by Moved: an answer asked before it is not kept
}

type head struct {
	b    domain.Commit
	when time.Time
}

// Get is the head of a branch of a namespace: the one kept, else asked.
func (h *Heads) Get(ctx context.Context, namespace, branch string, ask func(ctx context.Context, namespace, branch string) (domain.Commit, error)) (domain.Commit, error) {
	k := [2]string{namespace, branch}
	h.mu.Lock()
	cur, ok := h.at[k]
	gen := h.gen
	h.mu.Unlock()
	if ok && (h.MaxAge == 0 || time.Since(cur.when) < h.MaxAge) {
		return cur.b, nil
	}
	b, err := ask(ctx, namespace, branch)
	if err != nil {
		return b, err
	}
	h.mu.Lock()
	if h.gen == gen {
		if h.at == nil {
			h.at = map[[2]string]head{}
		}
		h.at[k] = head{b: b, when: time.Now()}
	}
	h.mu.Unlock()
	return b, nil
}

// Moved forgets every head kept: a branch moved.
func (h *Heads) Moved() {
	h.mu.Lock()
	h.at = nil
	h.gen++
	h.mu.Unlock()
}
