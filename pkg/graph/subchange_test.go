package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

type orgWorld struct {
	g                        *Graph
	base                     domain.Baseline
	cmp1, cmp2, cmp3         domain.Node
	acme, digital, team1, t2 domain.Node
}

// acme <- digital <- {team1, team2}; CMP-1 owned by team1, CMP-2 by team2, CMP-3 unowned.
func newOrgWorld(t *testing.T, repo Repo) orgWorld {
	t.Helper()
	ctx := context.Background()
	g := New(repo)
	w := orgWorld{g: g}
	mk := func(ns, key, typ string, props map[string]any) domain.Node {
		n, err := g.CreateNode(ctx, NewNode{Namespace: ns, Key: key, Type: typ, Properties: props})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	link := func(typ string, from, to domain.Node) {
		if _, err := g.Link(ctx, typ, from.Ref(), to.Ref(), nil); err != nil {
			t.Fatal(err)
		}
	}
	w.acme = mk("organisation", "ORG-ACME", "OrgUnit", map[string]any{"name": "Acme"})
	w.digital = mk("organisation", "ORG-DIGITAL", "OrgUnit", map[string]any{"name": "Digital"})
	w.team1 = mk("organisation", "ORG-T1", "OrgUnit", map[string]any{"name": "Team 1"})
	w.t2 = mk("organisation", "ORG-T2", "OrgUnit", map[string]any{"name": "Team 2"})
	link(LinkPartOf, w.digital, w.acme)
	link(LinkPartOf, w.team1, w.digital)
	link(LinkPartOf, w.t2, w.digital)
	w.cmp1 = mk("", "CMP-1", "Component", map[string]any{"title": "one"})
	w.cmp2 = mk("", "CMP-2", "Component", map[string]any{"title": "two"})
	w.cmp3 = mk("", "CMP-3", "Component", map[string]any{"title": "three"})
	link(LinkOwner, w.cmp1, w.team1)
	link(LinkOwner, w.cmp2, w.t2)
	var err error
	all := []domain.NodeRef{}
	for _, n := range []domain.Node{w.acme, w.digital, w.team1, w.t2, w.cmp1, w.cmp2, w.cmp3} {
		all = append(all, n.Ref())
	}
	if w.base, err = g.CreateBaseline(ctx, "B", all); err != nil {
		t.Fatal(err)
	}
	return w
}

func TestSplitByOwnerAndMerge(t *testing.T) { forEachRepo(t, testSplitByOwnerAndMerge) }

func testSplitByOwnerAndMerge(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	parent, err := g.CreateChange(ctx, NewChange{Title: "Upgrade", BaselineID: w.base.ID, OwnBranch: true, OwnerOrg: "ORG-DIGITAL"})
	if err != nil {
		t.Fatal(err)
	}
	r1, r2, r3 := w.cmp1.Ref(), w.cmp2.Ref(), w.cmp3.Ref()
	if _, err := g.AddItems(ctx, parent.ID, []domain.ChangeItem{
		{Kind: domain.KindImpact, Type: "direct", Target: &r1},
		{Kind: domain.KindImpact, Type: "direct", Target: &r2},
		{Kind: domain.KindImpact, Type: "direct", Target: &r3},
	}); err != nil {
		t.Fatal(err)
	}
	subs, err := g.SplitByOwner(ctx, parent.ID)
	if err != nil || len(subs) != 2 {
		t.Fatalf("split = %d, %v", len(subs), err)
	}
	if again, err := g.SplitByOwner(ctx, parent.ID); err != nil || len(again) != 0 {
		t.Fatalf("split must be idempotent: %d, %v", len(again), err)
	}
	orgs := map[string]domain.ChangeSet{}
	for _, s := range subs {
		if s.ParentID != parent.ID || s.Namespace != parent.Namespace || s.Status != domain.ChangeActive || len(s.Items) != 0 {
			t.Fatalf("sub-change = %+v", s)
		}
		full, _ := g.Change(ctx, s.ID)
		if len(full.Items) != 1 || full.Items[0].Kind != domain.KindImpact {
			t.Fatalf("impacts not copied: %+v", full.Items)
		}
		orgs[s.OwnerOrg] = s
	}
	if _, ok := orgs["ORG-T1"]; !ok || len(orgs) != 2 {
		t.Fatalf("orgs = %v", orgs)
	}

	// the parent cannot apply while its sub-changes are open
	if _, err := g.Apply(ctx, parent.ID, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("apply with open sub-changes: %v", err)
	}
	edit := func(c domain.ChangeSet, n domain.Node, title string) {
		ref := n.Ref()
		if _, err := g.AddItems(ctx, c.ID, []domain.ChangeItem{{Kind: domain.KindProposal,
			Proposal: &domain.Proposal{Op: domain.OpUpdateNode, Node: &domain.NodeDraft{Base: &ref, Properties: map[string]any{"title": title}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	edit(orgs["ORG-T1"], w.cmp1, "one v2")
	edit(orgs["ORG-T2"], w.cmp2, "two v2")
	edit(parent, w.cmp3, "three v2")
	for _, s := range orgs {
		if _, err := g.Apply(ctx, s.ID, ""); err != nil {
			t.Fatal(err)
		}
		if got, _ := g.Change(ctx, s.ID); got.Status != domain.ChangeApplied {
			t.Fatalf("%s = %s", s.OwnerOrg, got.Status)
		}
	}
	// merged into the parent branch only, main is untouched
	if n, _ := g.Node(ctx, domain.NodeRef{ID: w.cmp1.ID}); n.Properties["title"] != "one" {
		t.Fatalf("sub-change leaked on main: %v", n.Properties)
	}
	if n, err := g.NodeByKeyOn(ctx, "", parent.Branch, "CMP-1"); err != nil || n.Properties["title"] != "one v2" {
		t.Fatalf("parent branch = %v, %v", n.Properties, err)
	}
	// the parent applies on top of what its sub-changes merged, then merges into main
	if _, err := g.Apply(ctx, parent.ID, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.Change(ctx, parent.ID); got.Status != domain.ChangeApplied {
		t.Fatalf("parent = %s", got.Status)
	}
	for key, want := range map[string]string{"CMP-1": "one v2", "CMP-2": "two v2", "CMP-3": "three v2"} {
		if n, err := g.NodeByKey(ctx, "", key); err != nil || n.Properties["title"] != want {
			t.Fatalf("%s on main = %v, %v", key, n.Properties, err)
		}
	}
	if subs, _ := g.SubChanges(ctx, parent.ID); len(subs) != 2 {
		t.Fatalf("SubChanges = %d", len(subs))
	}
}

func TestSubChangeRules(t *testing.T) { forEachRepo(t, testSubChangeRules) }

func testSubChangeRules(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	// a parent without a branch of its own cannot have sub-changes
	flat, _ := g.CreateChange(ctx, NewChange{Title: "flat", BaselineID: w.base.ID})
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", ParentID: flat.ID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("sub-change of a flat change: %v", err)
	}
	// unknown owner org
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: w.base.ID, OwnerOrg: "ORG-NOPE"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown org: %v", err)
	}
	parent, err := g.CreateChange(ctx, NewChange{Title: "p", BaselineID: w.base.ID, OwnBranch: true, OwnerOrg: "ORG-T1"})
	if err != nil {
		t.Fatal(err)
	}
	// the sub-change's unit must be inside the parent's unit
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", ParentID: parent.ID, OwnerOrg: "ORG-T2"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unit outside the parent unit: %v", err)
	}
	// namespace is the parent's
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", ParentID: parent.ID, Namespace: "other"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("other namespace: %v", err)
	}
	p2, _ := g.CreateChange(ctx, NewChange{Title: "p2", BaselineID: w.base.ID, OwnBranch: true, OwnerOrg: "ORG-DIGITAL"})
	sub, err := g.CreateChange(ctx, NewChange{Title: "s", ParentID: p2.ID, OwnerOrg: "ORG-T2"})
	if err != nil || sub.Branch == p2.Branch || sub.ParentID != p2.ID {
		t.Fatalf("sub = %+v, %v", sub, err)
	}
	// abandoning the parent abandons its open sub-changes and their branches
	st := domain.ChangeAbandoned
	if _, err := g.UpdateChange(ctx, p2.ID, ChangePatch{Status: &st}); err != nil {
		t.Fatal(err)
	}
	if got, _ := g.Change(ctx, sub.ID); got.Status != domain.ChangeAbandoned {
		t.Fatalf("sub = %s", got.Status)
	}
	if b, _ := g.Branch(ctx, sub.Branch); b.Status != domain.BranchAbandoned {
		t.Fatalf("sub branch = %s", b.Status)
	}
}
