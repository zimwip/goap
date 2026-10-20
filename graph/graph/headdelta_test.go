package graph

import (
	"context"
	"errors"
	"reflect"
	"testing"

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
			gotN, gotL := h.State()
			wantN, wantL := whole.State()
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
