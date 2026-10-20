package graph

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/graphsnap"
)

// A view of the head followed by deltas holds what a whole read of the head holds (ADR 0143 §4).
func TestBaselineDelta(t *testing.T) { forEachRepo(t, testBaselineDelta) }

func testBaselineDelta(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	var moved, movedPlain graphsnap.Head
	if err := moved.Move(ctx, g, f.base.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := movedPlain.Move(ctx, g, f.base.ID, false); err != nil {
		t.Fatal(err)
	}
	check := func(at domain.Baseline) {
		t.Helper()
		for _, suspects := range []bool{true, false} {
			h := &movedPlain
			if suspects {
				h = &moved
			}
			if err := h.Move(ctx, g, at.ID, suspects); err != nil {
				t.Fatal(err)
			}
			var whole graphsnap.Head
			if err := whole.Move(ctx, New(repo), at.ID, suspects); err != nil {
				t.Fatal(err)
			}
			gotN, gotL := h.State(suspects)
			wantN, wantL := whole.State(suspects)
			if !reflect.DeepEqual(gotN, wantN) || !reflect.DeepEqual(gotL, wantL) {
				t.Fatalf("suspects %v: moved by deltas %d nodes %d links, read whole %d nodes %d links\n%+v\n%+v",
					suspects, len(gotN), len(gotL), len(wantN), len(wantL), gotL, wantL)
			}
		}
	}
	req := f.req.Ref()
	b1 := commitOn(t, g, "", f.base.ID, NodeEdit{Key: "DES-1", Type: "Design", Links: []LinkEdit{{Type: "refines", To: &req}}})
	check(b1)
	// REQ-1 moves: the links into it from TST-1 and DES-1 become suspect
	b2 := commitOn(t, g, "", b1.ID, setEdit(req, map[string]any{"title": "Use PSP v2"}))
	check(b2)
	des := domain.NodeRef{ID: must[domain.Node](t)(g.NodeByKey(ctx, "", "DES-1")).ID, Version: b2.Nodes[must[domain.Node](t)(g.NodeByKey(ctx, "", "DES-1")).ID]}
	b3 := commitOn(t, g, "", b2.ID, NodeEdit{Key: "DES-2", Type: "Design", Links: []LinkEdit{{Type: "refines", To: &des}}},
		setEdit(f.test.Ref(), map[string]any{"title": "Pay by card"}))
	check(b3)

	// one delta over several landings names each node once, at its last version
	d := must[BaselineDelta](t)(g.BaselineDelta(ctx, f.base.ID, b3.ID))
	if len(d.Nodes) != 4 || len(d.Removed) != 0 {
		t.Fatalf("delta from the fixture: %d nodes %v, removed %v", len(d.Nodes), d.Nodes, d.Removed)
	}
	for _, n := range d.Nodes {
		if b3.Nodes[n.ID] != n.Version {
			t.Fatalf("%s at v%d in the delta, v%d in the baseline", n.Key, n.Version, b3.Nodes[n.ID])
		}
	}
	// backwards, or from nothing: read the baseline whole
	for _, ft := range [][2]domain.BaselineID{{b3.ID, b1.ID}, {"", b1.ID}} {
		if _, err := g.BaselineDelta(ctx, ft[0], ft[1]); !errors.Is(err, ErrInvalid) {
			t.Fatalf("delta %s -> %s: %v", ft[0], ft[1], err)
		}
	}
}

// countingGraph counts the whole reads and the deltas a reader of the heads asks.
type countingGraph struct {
	*Graph
	whole, deltas int
}

func (c *countingGraph) BaselineGraph(ctx context.Context, id domain.BaselineID) ([]domain.Node, []domain.Link, error) {
	c.whole++
	return c.Graph.BaselineGraph(ctx, id)
}

func (c *countingGraph) BaselineDelta(ctx context.Context, from, to domain.BaselineID) (BaselineDelta, error) {
	c.deltas++
	return c.Graph.BaselineDelta(ctx, from, to)
}

// Views of the head share one reader (ADR 0145): the namespace is read whole once and moved once per landing whatever
// the number of views, and each view holds what a whole read holds, with or without the suspect links.
func TestSharedHeads(t *testing.T) { forEachRepo(t, testSharedHeads) }

func testSharedHeads(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := &countingGraph{Graph: f.g}
	heads := &graphsnap.Heads{Graph: g, TTL: -1}
	first := func(s []graphsnap.State) graphsnap.State { return s[0] }
	views := map[bool]*graphsnap.View[graphsnap.State]{
		false: {Heads: heads, Namespaces: []string{""}, Build: first},
		true:  {Heads: heads, Namespaces: []string{""}, Suspects: true, Build: first},
	}
	check := func(at domain.Baseline, whole, deltas int) {
		t.Helper()
		for suspects, v := range views {
			got, ids, err := v.Get(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var want graphsnap.Head
			if err := want.Move(ctx, New(repo), at.ID, suspects); err != nil {
				t.Fatal(err)
			}
			wantN, wantL := want.State(suspects)
			if ids[0] != at.ID || got.ID != at.ID || !reflect.DeepEqual(got.Nodes, wantN) || !reflect.DeepEqual(got.Links, wantL) {
				t.Fatalf("suspects %v at %s: view of %s, %d nodes %d links; whole read %d nodes %d links", suspects, at.ID, got.ID,
					len(got.Nodes), len(got.Links), len(wantN), len(wantL))
			}
		}
		if g.whole != whole || g.deltas != deltas {
			t.Fatalf("at %s: %d whole reads and %d deltas, want %d and %d", at.ID, g.whole, g.deltas, whole, deltas)
		}
	}
	check(f.base, 1, 0)
	req := f.req.Ref()
	b1 := commitOn(t, g.Graph, "", f.base.ID, NodeEdit{Key: "DES-1", Type: "Design", Links: []LinkEdit{{Type: "refines", To: &req}}})
	check(b1, 1, 1)
	// REQ-1 moves: the links into it become suspect, the plain view drops them, the other keeps them
	b2 := commitOn(t, g.Graph, "", b1.ID, setEdit(req, map[string]any{"title": "Use PSP v2"}))
	check(b2, 1, 2)
	check(b2, 1, 2) // nothing moved: nothing read

	// a reader looking once per TTL does not look again before it, unless told the head moved
	lazy := &graphsnap.Heads{Graph: f.g, TTL: time.Hour}
	at := must[domain.BaselineID](t)(lazy.Head(ctx, "", false))
	b3 := commitOn(t, f.g, "", b2.ID, setEdit(f.test.Ref(), map[string]any{"title": "Pay by card"}))
	if id := must[domain.BaselineID](t)(lazy.Head(ctx, "", false)); id != at {
		t.Fatalf("looked again within the TTL: %s", id)
	}
	lazy.Moved("")
	if id := must[domain.BaselineID](t)(lazy.Head(ctx, "", false)); id != b3.ID {
		t.Fatalf("told the head moved: at %s, want %s", id, b3.ID)
	}
}
