package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	c0 := testChange(t, g, "")
	link := func(typ string, from, to domain.Node) {
		if _, err := g.Link(ctx, c0, typ, from.Ref(), to.Ref(), nil); err != nil {
			t.Fatal(err)
		}
	}
	w.acme = mk("organisation", "ORG-ACME", NodeTypeOrgUnit, map[string]any{"name": "Acme"})
	w.digital = mk("organisation", "ORG-DIGITAL", NodeTypeOrgUnit, map[string]any{"name": "Digital"})
	w.team1 = mk("organisation", "ORG-T1", NodeTypeOrgUnit, map[string]any{"name": "Team 1"})
	w.t2 = mk("organisation", "ORG-T2", NodeTypeOrgUnit, map[string]any{"name": "Team 2"})
	link(LinkPartOf, w.digital, w.acme)
	link(LinkPartOf, w.team1, w.digital)
	link(LinkPartOf, w.t2, w.digital)
	owned := func(key, owner, title string) domain.Node {
		n, err := g.CreateNode(ctx, NewNode{Key: key, Type: "Component", Properties: map[string]any{"title": title}, Owner: owner})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	w.cmp1 = owned("CMP-1", "ORG-T1", "one")
	w.cmp2 = owned("CMP-2", "ORG-T2", "two")
	w.cmp3 = owned("CMP-3", "", "three") // owned by the root unit, outside ORG-DIGITAL
	// the change acts on the default namespace: only its own nodes belong in its baseline.
	// Organisation units are resolved independently of it (Graph.structureNode / within read the
	// organisation namespace's own head, ADR 0016).
	all := []domain.NodeRef{}
	for _, n := range []domain.Node{w.cmp1, w.cmp2, w.cmp3} {
		all = append(all, n.Ref())
	}
	var err error
	if w.base, err = g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch); err != nil {
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
	pnodes, err := g.AddNodes(ctx, parent.ID, []domain.ChangeImpact{
		{Intent: domain.IntentModified, Pre: &r1, Rationale: "upgrade one"},
		{Intent: domain.IntentModified, Pre: &r2, Rationale: "upgrade two"},
		{Intent: domain.IntentModified, Pre: &r3, Rationale: "upgrade three"},
	})
	if err != nil {
		t.Fatal(err)
	}
	subs, err := g.SplitByOwner(ctx, parent.ID)
	if err != nil || len(subs) != 2 {
		t.Fatalf("split = %d, %v", len(subs), err)
	}
	if again, err := g.SplitByOwner(ctx, parent.ID); err != nil || len(again) != 0 {
		t.Fatalf("split must be idempotent: %d, %v", len(again), err)
	}
	orgs := map[string]domain.Change{}
	for _, s := range subs {
		if s.ParentID != parent.ID || s.Namespace != parent.Namespace || s.Status != domain.ChangeActive || len(s.Items) != 0 {
			t.Fatalf("sub-change = %+v", s)
		}
		full, _ := g.Change(ctx, s.ID)
		if len(full.Nodes) != 1 || full.Nodes[0].Intent != domain.IntentModified || full.Nodes[0].Pre == nil || !full.Nodes[0].Planned() ||
			!strings.HasPrefix(full.Nodes[0].Rationale, "upgrade ") || len(full.Nodes[0].DerivedFrom) != 1 {
			t.Fatalf("change impacts not copied: %+v", full.Nodes)
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
	// each change writes its node through its change impact and accepts it
	edit := func(c domain.Change, n domain.Node, title string) {
		nodes, err := g.ListChangeImpacts(ctx, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		for _, cn := range nodes {
			if cn.Key != n.Key {
				continue
			}
			if _, err := g.WriteNode(ctx, c.ID, cn.ID, NodeWrite{Properties: map[string]any{"title": title}}); err != nil {
				t.Fatal(err)
			}
			if _, err := g.ReviewNode(ctx, c.ID, cn.ID, domain.ReviewAccepted, "u", "ok"); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatalf("no change impact for %s in %s", n.Key, c.ID)
	}
	_ = pnodes
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
	if b, _ := g.Branch(ctx, sub.Namespace, sub.Branch); b.Status != domain.BranchAbandoned {
		t.Fatalf("sub branch = %s", b.Status)
	}
}

// A root project links project_part_of to itself (ADR 0039, 0054: the bootstrap writes the self-link). The checks and
// the walk up the projects must handle it without looping forever.
func TestProjectSelfLinkTerminates(t *testing.T) {
	ctx := context.Background()
	g := New(NewMemory())
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	root, err := g.NodeByKey(ctx, "organisation", domain.DefaultProject)
	if err != nil {
		t.Fatal(err)
	}
	if links, err := g.OutLinksOf(ctx, root.Ref()); err != nil || len(links) != 1 || links[0].To != root.Ref() || links[0].Type != LinkProjectPartOf {
		t.Fatalf("the root project links to itself: %+v %v", links, err)
	}
	sub, err := g.CreateNode(ctx, NewNode{Namespace: "organisation", Key: "PROJ-SUB", Type: NodeTypeProjectUnit, Properties: map[string]any{"name": "Sub project"}})
	if err != nil {
		t.Fatal(err)
	}
	c0 := testChange(t, g, "organisation")
	if _, err := g.Link(ctx, c0, LinkProjectPartOf, sub.Ref(), root.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	within := func(tx Tx, key, ancestor string) (bool, error) {
		return g.within(ctx, tx, domain.StructureProject, key, ancestor)
	}
	done := make(chan error, 1)
	go func() {
		done <- g.repo.InTx(ctx, func(tx Tx) error {
			if _, err := g.structureNode(ctx, tx, domain.StructureProject, "PROJ-ROOT"); err != nil {
				return fmt.Errorf("root project must check out: %w", err)
			}
			if ok, err := within(tx, "PROJ-ROOT", "PROJ-ROOT"); err != nil || !ok {
				return fmt.Errorf("the root project is within itself: %v %v", ok, err)
			}
			if ok, err := within(tx, "PROJ-SUB", "PROJ-ROOT"); err != nil || !ok {
				return fmt.Errorf("the sub project is within the root project: %v %v", ok, err)
			}
			if ok, err := within(tx, "PROJ-ROOT", "PROJ-SUB"); err != nil || ok {
				return fmt.Errorf("the root project is not within the sub project: %v %v", ok, err)
			}
			return nil
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a self-link made the walk loop forever")
	}
}

// Project rules mirror the owner-org ones (ADR 0039): an unknown project is refused, a sub-change's
// project must be within the parent's, and it is inherited when unset. Administrative changes are exempt.
func TestProjectSubChangeRules(t *testing.T) { forEachRepo(t, testProjectSubChangeRules) }

func testProjectSubChangeRules(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	mk := func(key string, props map[string]any) domain.Node {
		n, err := g.CreateNode(ctx, NewNode{Namespace: "organisation", Key: key, Type: NodeTypeProjectUnit, Properties: props})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	c0 := testChange(t, g, "organisation")
	root, err := g.NodeByKey(ctx, "organisation", domain.DefaultProject)
	if err != nil {
		t.Fatal(err)
	}
	a := mk("PROJ-A", map[string]any{"name": "A"})
	if _, err := g.Link(ctx, c0, LinkProjectPartOf, a.Ref(), root.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	a1 := mk("PROJ-A1", map[string]any{"name": "A1"})
	if _, err := g.Link(ctx, c0, LinkProjectPartOf, a1.Ref(), a.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	b := mk("PROJ-B", map[string]any{"name": "B"})
	if _, err := g.Link(ctx, c0, LinkProjectPartOf, b.Ref(), root.Ref(), nil); err != nil {
		t.Fatal(err)
	}
	base, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	// unknown project
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID, ProjectID: "PROJ-NOPE"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown project: %v", err)
	}
	// none named: the default project (ADR 0054)
	if c, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID, Administrative: true}); err != nil || c.ProjectID != domain.DefaultProject || c.OwnerOrg != domain.DefaultOrg {
		t.Fatalf("a change naming no project acts in the default one, held by the root unit: %+v %v", c, err)
	}
	// a node of the organisation that is not a project is refused
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID, ProjectID: domain.DefaultOrg}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a unit as the project: %v", err)
	}
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", BaselineID: base.ID, OwnerOrg: "PROJ-A"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a project as the owner unit: %v", err)
	}
	parent, err := g.CreateChange(ctx, NewChange{Title: "p", BaselineID: base.ID, OwnBranch: true, ProjectID: "PROJ-A"})
	if err != nil {
		t.Fatal(err)
	}
	// the sub-change's project must be within the parent's project
	if _, err := g.CreateChange(ctx, NewChange{Title: "x", ParentID: parent.ID, ProjectID: "PROJ-B"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("project outside the parent project: %v", err)
	}
	// within (a descendant) is accepted
	if sub, err := g.CreateChange(ctx, NewChange{Title: "x", ParentID: parent.ID, ProjectID: "PROJ-A1"}); err != nil {
		t.Fatalf("project within the parent project: %v", err)
	} else if sub.ProjectID != "PROJ-A1" {
		t.Fatalf("sub.ProjectID = %q", sub.ProjectID)
	}
	// unset: inherited from the parent
	if sub, err := g.CreateChange(ctx, NewChange{Title: "x", ParentID: parent.ID}); err != nil || sub.ProjectID != "PROJ-A" {
		t.Fatalf("project inherited from the parent: %+v, %v", sub, err)
	}
}

// A sub-change's Activity, when it names one, must be the parent's own or a descendant of it reached by
// sub_activity links (architecture plan "Activity concept" cascade); unset is fine (no inheritance, since a
// sub-change is usually scoped to a more specific sub-activity, not the parent's own). The branch it gets
// defaults to Intent derive.
func TestSubChangeActivityCascade(t *testing.T) { forEachRepo(t, testSubChangeActivityCascade) }

func testSubChangeActivityCascade(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	mk := func(key string) domain.Node {
		n, err := g.CreateNode(ctx, NewNode{Namespace: NamespaceMethodology, Key: key, Type: "methodology@Process"})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	act := mk("ACT-PARENT")
	sub := mk("ACT-CHILD")
	other := mk("ACT-OTHER")
	c0 := testChange(t, g, NamespaceMethodology)
	if _, err := g.Link(ctx, c0, LinkSubActivity, act.Ref(), sub.Ref(), nil); err != nil {
		t.Fatal(err)
	}

	parent, err := g.CreateChange(ctx, NewChange{Title: "p", BaselineID: w.base.ID, OwnBranch: true, ActivityRef: "ACT-PARENT"})
	if err != nil {
		t.Fatal(err)
	}
	// a descendant activity is accepted, and the sub-change's branch defaults to Intent derive
	within, err := g.CreateChange(ctx, NewChange{Title: "within", ParentID: parent.ID, ActivityRef: "ACT-CHILD"})
	if err != nil {
		t.Fatalf("activity within the parent's: %v", err)
	}
	if within.ActivityRef != "ACT-CHILD" {
		t.Fatalf("activityRef = %q", within.ActivityRef)
	}
	if b, err := g.Branch(ctx, within.Namespace, within.Branch); err != nil || b.Intent != domain.IntentDerive {
		t.Fatalf("branch intent defaults to derive: %+v, %v", b, err)
	}
	// an unrelated activity is refused
	if _, err := g.CreateChange(ctx, NewChange{Title: "outside", ParentID: parent.ID, ActivityRef: other.Key}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("activity outside the parent's: %v", err)
	}
	// unset: not inherited, no error
	if noAct, err := g.CreateChange(ctx, NewChange{Title: "unscoped", ParentID: parent.ID}); err != nil || noAct.ActivityRef != "" {
		t.Fatalf("activity ref not inherited: %+v, %v", noAct, err)
	}
}

// Two sibling sub-changes, each on its own branch forked from the same parent branch, touching the same node:
// whichever applies (merges into the parent branch) first wins; the other waits committed and needs a
// resolution, exactly the precedence the parent/parallel-sub-activity design relies on.
func TestSubChangeMergePrecedence(t *testing.T) { forEachRepo(t, testSubChangeMergePrecedence) }

func testSubChangeMergePrecedence(t *testing.T, repo Repo) {
	ctx := context.Background()
	w := newOrgWorld(t, repo)
	g := w.g
	parent, err := g.CreateChange(ctx, NewChange{Title: "parent", BaselineID: w.base.ID, OwnBranch: true})
	if err != nil {
		t.Fatal(err)
	}
	a, err := g.CreateChange(ctx, NewChange{Title: "sub A", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	b, err := g.CreateChange(ctx, NewChange{Title: "sub B", ParentID: parent.ID})
	if err != nil {
		t.Fatal(err)
	}
	if a.Branch == b.Branch || a.Branch == parent.Branch {
		t.Fatalf("sibling sub-changes must each have their own branch: a=%s b=%s parent=%s", a.Branch, b.Branch, parent.Branch)
	}
	pre := w.cmp3.Ref()
	writeTitle := func(c domain.Change, title string) domain.ChangeImpactID {
		t.Helper()
		added, err := g.AddNodes(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &pre, Rationale: title}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.WriteNode(ctx, c.ID, added[0].ID, NodeWrite{Properties: map[string]any{"title": title}}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.ReviewNode(ctx, c.ID, added[0].ID, domain.ReviewAccepted, "u", "ok"); err != nil {
			t.Fatal(err)
		}
		return added[0].ID
	}
	writeTitle(a, "from A")
	writeTitle(b, "from B")

	// A applies first: fast-forwards cleanly into the parent branch
	if _, err := g.Apply(ctx, a.ID, ""); err != nil {
		t.Fatalf("sub A applies first: %v", err)
	}
	if head, err := g.NodeByKeyOn(ctx, domain.DefaultNamespace, parent.Branch, "CMP-3"); err != nil || head.Properties["title"] != "from A" {
		t.Fatalf("parent branch after A: %+v, %v", head, err)
	}

	// B, forked from the same base as A, now conflicts: committed (integration waits), not a silent clobber
	if _, err := g.Apply(ctx, b.ID, ""); err != nil {
		t.Fatalf("sub B apply: %v", err)
	}
	if got, _ := g.Change(ctx, b.ID); got.Status != domain.ChangeCommitted {
		t.Fatalf("sub B must be committed after A landed first, got %s", got.Status)
	}

	// B's author adapts: resolve and complete the merge
	if _, err := g.IntegrateChange(ctx, b.ID, map[domain.NodeID]Resolution{w.cmp3.ID: {Props: map[string]any{"title": "from A and B"}}}); err != nil {
		t.Fatal(err)
	}
	if head, err := g.NodeByKeyOn(ctx, domain.DefaultNamespace, parent.Branch, "CMP-3"); err != nil || head.Properties["title"] != "from A and B" {
		t.Fatalf("parent branch after B's resolved merge: %+v, %v", head, err)
	}
}
