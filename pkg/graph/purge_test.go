package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestPurgeChange(t *testing.T) { forEachRepo(t, testPurgeChange) }

func testPurgeChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g

	// an unapplied change: its versions, log, impacts and branch go, the node it modified is back to v1
	c := setProp(t, g, f, f.base.ID, "A", map[string]any{"title": "A title"})
	if _, err := g.PurgeChange(ctx, c.ID); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := g.Change(ctx, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("change after purge: %v", err)
	}
	if _, err := g.Branch(ctx, "", c.Branch); !errors.Is(err, ErrNotFound) {
		t.Fatalf("branch after purge: %v", err)
	}
	vs, err := g.Versions(ctx, f.req.ID)
	if err != nil || len(vs) != 1 {
		t.Fatalf("versions after purge = %d, %v", len(vs), err)
	}
	// the node is still writable: the next version follows v1
	d := setProp(t, g, f, f.base.ID, "B", map[string]any{"title": "B title"})
	if _, err := g.Apply(ctx, d.ID, ""); err != nil {
		t.Fatalf("apply after purge: %v", err)
	}

	// an applied change landed in the graph: it stays
	if _, err := g.PurgeChange(ctx, d.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("purge of an applied change: %v", err)
	}

	// a business rule may keep a discarded change
	e := setProp(t, g, f, f.base.ID, "E", map[string]any{"title": "E"})
	keep := errors.New("kept by policy")
	g.PurgePolicy = func(context.Context, domain.Change) error { return keep }
	if _, err := g.PurgeChange(ctx, e.ID); !errors.Is(err, keep) {
		t.Fatalf("purge against the policy: %v", err)
	}
	g.PurgePolicy = nil
	if _, err := g.PurgeChange(ctx, e.ID); err != nil {
		t.Fatalf("purge: %v", err)
	}
}
