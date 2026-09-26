// Package graphsnap reads a typed view of the graph at the head of the main branch, rebuilding it only
// when the head moved. Services that keep configuration as graph nodes (access, models, ...) build their
// snapshot with it.
package graphsnap

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
)

// Graph is the part of the graph a snapshot reads.
type Graph interface {
	BranchHead(ctx context.Context, name string) (domain.Baseline, error)
	BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error)
}

// Cache holds the snapshot of the head of main. The head is looked at most once per TTL (one second by
// default). When the graph cannot be read the last snapshot keeps serving and the error is returned with it.
type Cache[T any] struct {
	Graph Graph
	TTL   time.Duration
	// Build makes the view of a baseline; the graph without any baseline yields Build("", nil, nil).
	Build func(id domain.BaselineID, nodes []domain.Node, links []domain.Link) T

	mu      sync.Mutex
	have    bool
	cur     T
	id      domain.BaselineID
	checked time.Time
}

// Get returns the current snapshot and the baseline it was built from.
func (c *Cache[T]) Get(ctx context.Context) (T, domain.BaselineID, error) { return c.get(ctx, false) }

// Fresh is Get without the TTL: the head is looked at now (before a write that builds on what it reads).
func (c *Cache[T]) Fresh(ctx context.Context) (T, domain.BaselineID, error) { return c.get(ctx, true) }

func (c *Cache[T]) get(ctx context.Context, fresh bool) (T, domain.BaselineID, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ttl := c.TTL
	if ttl == 0 {
		ttl = time.Second
	}
	if !fresh && c.have && time.Since(c.checked) < ttl {
		return c.cur, c.id, nil
	}
	head, err := c.Graph.BranchHead(ctx, domain.MainBranch)
	if errors.Is(err, graph.ErrNotFound) {
		c.cur, c.id, c.have, c.checked = c.Build("", nil, nil), "", true, time.Now()
		return c.cur, c.id, nil
	}
	if err != nil {
		return c.cur, c.id, err
	}
	c.checked = time.Now()
	if c.have && c.id == head.ID {
		return c.cur, c.id, nil
	}
	nodes, links, err := c.Graph.BaselineGraph(ctx, head.ID)
	if err != nil {
		return c.cur, c.id, err
	}
	c.cur, c.id, c.have = c.Build(head.ID, nodes, links), head.ID, true
	return c.cur, c.id, nil
}
