package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/typecat"
)

// A rejected impact keeps its working version, to be reworked once reopened; taking it out of the change is an
// explicit operation (ADR 0076 §5b), refused once the change checked a version of it in.
func TestWithdrawImpact(t *testing.T) { forEachRepo(t, testWithdrawImpact) }

func testWithdrawImpact(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Title: "rework", BaselineID: f.base.ID}))

	// a rejected checkout keeps its working version: reopened, it is edited again
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.req.ID, Rationale: "v2"}))
	work := *cn.Post
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewRejected, "bob", "not like this"))
	if n := must[domain.Node](t)(g.Node(ctx, work)); !n.CheckedOut {
		t.Fatalf("a rejected impact keeps its working version: %+v", n)
	}
	must[[]domain.ChangeImpactID](t)(g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{cn.ID}, "try again"))
	must[domain.ChangeImpact](t)(g.ImpactNodeUpdate(ctx, c.ID, cn.ID, NodeUpdate{Properties: map[string]any{"title": "Use PSP v2"}}))

	// removed: the working version goes, the node is back to its version, the impact leaves the change
	if err := g.WithdrawImpact(ctx, c.ID, cn.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if latest := must[domain.Node](t)(g.Node(ctx, domain.NodeRef{ID: f.req.ID})); latest.Version != f.req.Version {
		t.Fatalf("the working version is dropped: v%d", latest.Version)
	}
	if got := must[domain.Change](t)(g.Change(ctx, c.ID)); len(got.Nodes) != 0 {
		t.Fatalf("the impact leaves the change: %+v", got.Nodes)
	}

	// a creation removed before its first check-in leaves no node
	created := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "REQ-NEW", Type: "Requirement", Rationale: "new"}))
	if err := g.WithdrawImpact(ctx, c.ID, created.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, "", "REQ-NEW"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the created node is gone: %v", err)
	}

	// a checked-in version is frozen: the impact is rejected, not removed
	cn = must[domain.ChangeImpact](t)(g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: f.test.ID, Rationale: "v2"}))
	if err := g.acceptAndCheckin(ctx, c.ID, cn.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := g.WithdrawImpact(ctx, c.ID, cn.ID, "", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("an impact with a checked-in version is not removed: %v", err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
}

// The links a type requires (the parent of a unit, ADR 0054) are checked when the version is frozen and when it lands,
// whatever operations built it; a working version may be on its way there.
func TestRequiredLinksAtCheckin(t *testing.T) { forEachRepo(t, testRequiredLinksAtCheckin) }

func testRequiredLinksAtCheckin(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	root := must[domain.Node](t)(g.NodeByKey(ctx, "organisation", rootOrg(g)))
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "organisation", domain.MainBranch))
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Namespace: "organisation", Title: "unit", BaselineID: head.ID}))
	cn := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "ORG-X", Type: NodeTypeOrgUnit, Properties: map[string]any{"name": "X"}, Rationale: "new unit"}))
	must[domain.ChangeImpact](t)(g.ImpactNodeReview(ctx, c.ID, cn.ID, domain.ReviewAccepted, "bob", "ok"))
	if _, err := g.ImpactNodeCheckin(ctx, c.ID, cn.ID, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a unit without a parent is not checked in: %v", err)
	}
	// an accepted version is not edited: reopened, the parent is added and the version reviewed again
	must[[]domain.ChangeImpactID](t)(g.ReopenImpacts(ctx, c.ID, []domain.ChangeImpactID{cn.ID}, "add the parent"))
	must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, cn.ID, LinkWrite{Type: LinkPartOf, To: root.Ref()}, "", ""))
	if err := g.acceptAndCheckin(ctx, c.ID, cn.ID, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Apply(ctx, c.ID, ""); err != nil {
		t.Fatal(err)
	}
}

// The attributes of a link type are checked like the ones of a node type: on every link edit.
func TestLinkAttributes(t *testing.T) {
	ctx := context.Background()
	d, err := def.ParseDomain([]byte(`
name: docs
version: 1.0.0
enums:
  - {name: strength, values: [{value: weak}, {value: strong}]}
nodeTypes:
  - {name: Req, attributes: [title]}
  - {name: Test, attributes: [title]}
linkTypes:
  - {name: verifies, from: Test, to: Req, attributes: [{name: strength, type: enum, enum: strength}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(d)
	if err != nil {
		t.Fatal(err)
	}
	g := New(NewMemory())
	g.Types = func() TypeCatalog { return cat }
	head := must[domain.Baseline](t)(g.BranchHead(ctx, "docs", domain.MainBranch))
	c := must[domain.Change](t)(g.CreateChange(ctx, NewChange{Namespace: "docs", Title: "t", BaselineID: head.ID}))
	req := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "R1", Type: "docs@Req", Rationale: "r"}))
	tst := must[domain.ChangeImpact](t)(g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "T1", Type: "docs@Test", Rationale: "t"}))
	if _, err := g.ImpactLinkCreate(ctx, c.ID, tst.ID, LinkWrite{Type: "docs@verifies", To: *req.Post, Properties: map[string]any{"strength": "huge"}}, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a link property outside its enum: %v", err)
	}
	l := must[domain.Link](t)(g.ImpactLinkCreate(ctx, c.ID, tst.ID, LinkWrite{Type: "docs@verifies", To: *req.Post, Properties: map[string]any{"strength": "weak"}}, "", ""))
	if _, err := g.ImpactLinkUpdate(ctx, c.ID, l.ID, map[string]any{"strength": "huge"}, "", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a link property updated outside its enum: %v", err)
	}
	must[domain.Link](t)(g.ImpactLinkUpdate(ctx, c.ID, l.ID, map[string]any{"strength": "strong"}, "", ""))
}

// A node no parent holds is retired by its lifecycle, never deleted (ADR 0076 §4c); restoring it moves it back to an
// editable state before the edits of the same commit.
func TestRetireAndRestoreByCommit(t *testing.T) { forEachRepo(t, testRetireAndRestoreByCommit) }

func testRetireAndRestoreByCommit(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	g.Types = builtinTypes
	commit := func(edits ...NodeEdit) {
		t.Helper()
		head := must[domain.Baseline](t)(g.BranchHead(ctx, "organisation", domain.MainBranch))
		if _, err := g.Commit(ctx, Commit{Namespace: "organisation", Title: "policy", Baseline: head.ID, By: "t", Edits: edits}); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	commit(NodeEdit{Key: "POL:x", Type: "organisation@Policy", Props: map[string]any{"rule": "true", "resource": "x", "action": "read"}})
	pol := must[domain.Node](t)(g.NodeByKey(ctx, "organisation", "POL:x"))
	if pol.State != "active" {
		t.Fatalf("created in force: %+v", pol)
	}
	ref := pol.Ref()
	commit(NodeEdit{Pre: &ref, State: "retired"})
	pol = must[domain.Node](t)(g.NodeByKey(ctx, "organisation", "POL:x"))
	if pol.State != "retired" {
		t.Fatalf("retired: %+v", pol)
	}
	ref = pol.Ref()
	commit(NodeEdit{Pre: &ref, State: "active", Props: map[string]any{"action": "write"}})
	pol = must[domain.Node](t)(g.NodeByKey(ctx, "organisation", "POL:x"))
	if pol.State != "active" || pol.Properties["action"] != "write" {
		t.Fatalf("restored and edited: %+v", pol)
	}
}
