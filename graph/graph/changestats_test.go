package graph

import (
	"context"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The counters of a change: each commit records what its versions do to their nodes, the header keeps the counters of
// what the change committed, net per node (a node committed twice counts once), and the merge of a sub-change counts its
// work in its parent; the impacts not committed yet are counted from the impacts, never stored.
func TestChangeStats(t *testing.T) { forEachRepo(t, testChangeStats) }

func testChangeStats(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "stats", BaselineID: f.base.ID, OwnBranch: true}))
	req := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, req.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))
	design := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "D-1", Type: "Design", Rationale: "new", Properties: map[string]any{"title": "PSP"}}))
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, req.ID, LinkWrite{Type: "refines", To: *design.Post}, "", ""))
	for _, id := range []domain.ChangeImpactID{req.ID, design.ID} {
		must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, id, domain.ReviewAccepted, "bob", "ok"))
	}
	must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.test.ID, Rationale: "later"}))
	if s := domain.StatsOf(must[domain.Change](t)(g.Change(ctx, c.ID)).Impacts()); s.Nodes() != 0 || s.Accepted != 2 || s.Proposed != 1 {
		t.Fatalf("nothing committed, three impacts pending: %+v", s)
	}
	must[domain.Baseline](t)(g.CommitChange(ctx, c.ID, ""))

	got := must[domain.Change](t)(g.Change(ctx, c.ID))
	for _, cn := range got.Impacts().All() {
		switch cn.ID {
		case req.ID:
			if cn.Effect != (domain.ImpactEffect{Kind: domain.EffectModified, Properties: 1, LinksAdded: 1}) {
				t.Fatalf("the effect of the modification: %+v", cn.Effect)
			}
		case design.ID:
			if cn.Effect != (domain.ImpactEffect{Kind: domain.EffectCreated, Properties: 1}) {
				t.Fatalf("the effect of the creation: %+v", cn.Effect)
			}
		}
	}
	want := domain.ChangeStats{Created: 1, Modified: 1, Properties: 2, LinksAdded: 1}
	if got.Stats != want {
		t.Fatalf("the header keeps what the change committed: %+v", got.Stats)
	}
	if s := domain.StatsOf(got.Impacts()); s.Committed() != want || s.Proposed != 1 {
		t.Fatalf("the fold of the impacts: %+v", s)
	}

	// the node committed again is still one modification, its effect over the version the change started from
	req2 := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v3"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, req2.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v3", "note": "x"}}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, req2.ID, domain.ReviewAccepted, "bob", "ok"))
	must[domain.Baseline](t)(g.CommitChangeNodes(ctx, c.ID, "", []domain.NodeID{f.req.ID}))
	got = must[domain.Change](t)(g.Change(ctx, c.ID))
	if want := (domain.ChangeStats{Created: 1, Modified: 1, Properties: 3, LinksAdded: 1}); got.Stats != want {
		t.Fatalf("net per node: %+v", got.Stats)
	}

	// the work of a sub-change merged is its parent's
	sub := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "sub", ParentID: c.ID}))
	d2 := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, sub.ID, NodeCreate{Key: "D-2", Type: "Design", Rationale: "new"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, sub.ID, d2.ID, domain.ReviewAccepted, "bob", "ok"))
	must[domain.Baseline](t)(g.CommitChange(ctx, sub.ID, ""))
	if s := must[domain.Change](t)(g.Change(ctx, sub.ID)).Stats; s != (domain.ChangeStats{Created: 1}) {
		t.Fatalf("the sub-change's own counters: %+v", s)
	}
	must[domain.Change](t)(g.IntegrateChange(ctx, sub.ID, nil))
	if s := must[domain.Change](t)(g.Change(ctx, c.ID)).Stats; s.Created != 2 || s.Modified != 1 {
		t.Fatalf("the parent counts the work merged: %+v", s)
	}
	// the statistics of the graph sum the root changes' counters (the sub-change is counted in its parent): the
	// fixture's import counts its own creations
	st := must[GraphStats](t)(g.GraphStats(ctx, "", domain.MainBranch))
	if got := st.Store[StatChangeCounters]; got["created"] < 2 || got["modified"] < 1 || got["linksAdded"] < 1 {
		t.Fatalf("the counters of the changes in the statistics: %v", got)
	}
}

// A deletion, a restoration and a creation requalified as the modification of an existing node (ADR 0172) are counted
// as such, and the counters stay on the header of the change once it is applied.
func TestChangeStatsDeleteAndRestore(t *testing.T) { forEachRepo(t, testChangeStatsDeleteAndRestore) }

func testChangeStatsDeleteAndRestore(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "drop REQ-1", BaselineID: f.base.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeDelete(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "obsolete"}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c.ID, cn.ID, "bob", "gone"))
	gone := must[domain.Baseline](t)(g.apply(ctx, c.ID, ""))
	if s := must[domain.Change](t)(g.Change(ctx, c.ID)).Stats; s != (domain.ChangeStats{Deleted: 1, LinksRemoved: 1}) {
		t.Fatalf("a deletion drops the node and its links: %+v", s)
	}

	c3 := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "undo", BaselineID: gone.ID}))
	undo := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c3.ID, NodeCheckout{Node: f.req.ID, Rationale: "undo"}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c3.ID, undo.ID, "bob", "undo"))
	must[domain.Baseline](t)(g.CommitChange(ctx, c3.ID, ""))
	if s := must[domain.Change](t)(g.Change(ctx, c3.ID)).Stats; s != (domain.ChangeStats{Restored: 1, Properties: 1}) {
		t.Fatalf("a checkout of a deleted node restores it: %+v", s)
	}

	c2 := must[domain.Change](t)(g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "REQ-1 again", BaselineID: gone.ID}))
	back := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c2.ID, NodeCreate{Key: "REQ-1", Type: "Requirement", Rationale: "needed again",
		Properties: map[string]any{"title": "Use PSP v3"}, Links: []LinkWrite{{Type: "satisfies", To: f.need.Ref()}}}))
	must[domain.ChangeImpact](t)(g.accept(ctx, c2.ID, back.ID, "bob", "back"))
	must[domain.Baseline](t)(g.apply(ctx, c2.ID, ""))
	if s := must[domain.Change](t)(g.Change(ctx, c2.ID)).Stats; s != (domain.ChangeStats{Restored: 1, Requalified: 1, Properties: 1, LinksAdded: 1}) {
		t.Fatalf("the creation of a deleted key is a restoration, requalified: %+v", s)
	}
}
