package graph

import (
	"context"
	"errors"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
)

func TestCommit(t *testing.T) { forEachRepo(t, testCommit) }

func testCommit(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g

	// a header created with the element it defines, in the order that suits the links
	res, err := g.Commit(ctx, Commit{Title: "Define DOC-1", Baseline: f.base.ID, By: "registry", BaselineName: "def",
		Edits: []NodeEdit{
			{Key: "DOC-1", Type: "Doc", Props: map[string]any{"status": "draft"}, Rationale: "new document",
				Links: []LinkEdit{{Type: "defines", ToKey: "DOC-1/el"}, {Type: "about", To: refPtr(f.need.Ref())}}},
			{Key: "DOC-1/el", Type: "Element", Props: map[string]any{"n": 1}},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Baseline.ID == "" {
		t.Fatal("no baseline")
	}
	doc, err := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1")
	if err != nil {
		t.Fatal(err)
	}
	el, err := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1/el")
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.View(ctx, doc.Ref())
	if err != nil || len(v.Out) != 2 {
		t.Fatalf("header links: %+v %v", v, err)
	}
	var defines domain.NodeRef
	for _, l := range v.Out {
		if l.Type == "defines" {
			defines = l.To
		}
	}
	if defines.ID != el.ID {
		t.Fatalf("defines must point at the element: %+v vs %+v", defines, el.Ref())
	}
	if doc.ChangeID != res.Change || doc.Comment != "new document" || doc.ChangeImpact == "" {
		t.Fatalf("the version must record its origin: %+v", doc)
	}

	// both modified, the header linking to the element: the header follows the element's new version
	head, _ := g.BranchHead(ctx, "", domain.MainBranch)
	docRef, elRef := doc.Ref(), el.Ref()
	if _, err := g.Commit(ctx, Commit{Title: "Edit DOC-1", Baseline: head.ID, By: "registry",
		Edits: []NodeEdit{
			{Pre: &docRef, Props: map[string]any{"status": "published"}},
			{Pre: &elRef, Props: map[string]any{"n": 2}},
		}}); err != nil {
		t.Fatal(err)
	}
	doc2, _ := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1")
	el2, _ := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1/el")
	v2, _ := g.View(ctx, doc2.Ref())
	for _, l := range v2.Out {
		if l.Type == "defines" && l.To != el2.Ref() {
			t.Fatalf("the carried link must follow the new element version %s, got %s", el2.Ref(), l.To)
		}
	}
	if doc2.Properties["status"] != "published" || el2.Properties["n"] != float64(2) && el2.Properties["n"] != 2 {
		t.Fatalf("edits lost: %v %v", doc2.Properties, el2.Properties)
	}

	// a link by key may target a node the graph already holds; one the version already carries is not added again
	head, _ = g.BranchHead(ctx, "", domain.MainBranch)
	doc2Ref := doc2.Ref()
	if _, err := g.Commit(ctx, Commit{Title: "Link stored", Baseline: head.ID, By: "registry",
		Edits: []NodeEdit{
			{Key: "DOC-2", Type: "Doc", Links: []LinkEdit{{Type: "defines", ToKey: "DOC-1/el"}}},
			{Pre: &doc2Ref, Links: []LinkEdit{{Type: "defines", ToKey: "DOC-1/el"}}},
		}}); err != nil {
		t.Fatalf("a link to a stored node: %v", err)
	}
	other, _ := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-2")
	if v, _ := g.View(ctx, other.Ref()); len(v.Out) != 1 || v.Out[0].To.ID != el2.ID {
		t.Fatalf("DOC-2 must define the stored element: %+v", v.Out)
	}
	doc3, _ := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1")
	if v, _ := g.View(ctx, doc3.Ref()); len(v.Out) != len(v2.Out) {
		t.Fatalf("the link DOC-1 carries must not be added twice: %d links, had %d", len(v.Out), len(v2.Out))
	}
	head, _ = g.BranchHead(ctx, "", domain.MainBranch)
	if _, err := g.Commit(ctx, Commit{Title: "Link nowhere", Baseline: head.ID, By: "registry",
		Edits: []NodeEdit{{Key: "DOC-3", Type: "Doc", Links: []LinkEdit{{Type: "defines", ToKey: "NOPE"}}}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a link to a node nowhere must be invalid, got %v", err)
	}

	// retire the element; a stale edit conflicts and leaves no open change
	head, _ = g.BranchHead(ctx, "", domain.MainBranch)
	el2Ref := el2.Ref()
	if _, err := g.Commit(ctx, Commit{Title: "Retire", Baseline: head.ID, By: "registry", Edits: []NodeEdit{{Pre: &el2Ref, Retire: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1/el"); err == nil {
		if n, _ := g.NodeByKey(ctx, domain.DefaultNamespace, "DOC-1/el"); !n.Deleted {
			t.Fatalf("the element must be retired: %+v", n)
		}
	}
	if _, err := g.Commit(ctx, Commit{Title: "Stale", Baseline: head.ID, By: "registry",
		Edits: []NodeEdit{{Pre: &docRef, Props: map[string]any{"status": "x"}}}}); !errors.Is(err, ErrConflict) && !errors.Is(err, ErrInvalid) {
		t.Fatalf("an edit based on a stale version must fail, got %v", err)
	}
	cs, _ := g.Changes(ctx)
	for _, c := range cs {
		if c.Title == "Stale" && c.Status != domain.ChangeAbandoned {
			t.Fatalf("a failed commit must be abandoned, is %s", c.Status)
		}
	}
}

func refPtr(r domain.NodeRef) *domain.NodeRef { return &r }
