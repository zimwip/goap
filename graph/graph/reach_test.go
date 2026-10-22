package graph

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
)

// reachFixture commits nodes of the projects of a test, each in a change of its project.
type reachFixture struct {
	t   *testing.T
	ctx context.Context
	g   *Graph
}

// commit creates a node in a change of a project and merges it to main.
func (f reachFixture) commit(project, ns, key, typ string, links ...LinkWrite) domain.Node {
	f.t.Helper()
	head, err := f.g.BranchHead(f.ctx, ns, domain.MainBranch)
	if err != nil {
		f.t.Fatal(err)
	}
	c, err := f.g.CreateChange(f.ctx, NewChange{ProjectID: project, Namespace: ns, Title: "create " + key, BaselineID: head.ID, OwnBranch: true})
	if err != nil {
		f.t.Fatal(err)
	}
	cn, err := f.g.ImpactNodeCreate(f.ctx, c.ID, NodeCreate{Key: key, Type: typ, Properties: map[string]any{"name": key}, Links: links, Rationale: "create " + key})
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.g.ImpactNodeReviewOn(f.ctx, c.ID, domain.MainFlow, "", cn.ID, domain.ReviewAccepted, "tester", "ok"); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.g.CommitChange(f.ctx, c.ID, ""); err != nil {
		f.t.Fatal(err)
	}
	if _, err := f.g.IntegrateChange(f.ctx, c.ID, nil); err != nil {
		f.t.Fatal(err)
	}
	n, err := f.g.NodeByKey(f.ctx, ns, key)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

// project commits a project under another.
func (f reachFixture) project(key, parent string) {
	f.t.Helper()
	st := f.g.Structure(domain.StructureProject)
	p, err := f.g.NodeByKey(f.ctx, st.Namespace, parent)
	if err != nil {
		f.t.Fatal(err)
	}
	f.commit(st.Root, st.Namespace, key, st.Type, LinkWrite{Type: st.Parent, To: p.Ref()})
}

// TestReach: a caller reads the nodes of the projects it works on, and the links between them (ADR 0161 §5): the
// nodes, neighbourhoods, pages, counts, links, queries and exports of a view leave out the others, as if they did not
// exist; an open namespace is read whole.
func TestReach(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		if err := g.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		f := reachFixture{t: t, ctx: ctx, g: g}
		root := g.Structure(domain.StructureProject).Root
		f.project("P-A", root)
		f.project("P-B", root)
		b1 := f.commit("P-B", "alm", "B-1", "alm@Requirement")
		a2 := f.commit("P-A", "alm", "A-2", "alm@Requirement")
		a1 := f.commit("P-A", "alm", "A-1", "alm@Requirement",
			LinkWrite{Type: "alm@refines", To: a2.Ref()}, LinkWrite{Type: "alm@refines", To: b1.Ref()})
		f.commit("P-B", "alm", "B-2", "alm@Requirement", LinkWrite{Type: "alm@refines", To: a1.Ref()})

		reach := &Reach{Projects: map[string]bool{"P-A": true}}
		g.ReachOf = func(context.Context) (*Reach, error) { return reach, nil }
		main := View{Namespace: "alm", Branch: domain.MainBranch}

		if _, err := g.ViewNode(ctx, main, "", "B-1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a node of another project: %v, want not found", err)
		}
		nv, err := g.ViewNode(ctx, main, "", "A-1")
		if err != nil {
			t.Fatal(err)
		}
		if len(nv.Out) != 1 || nv.Out[0].To.ID != a2.ID || len(nv.In) != 0 {
			t.Fatalf("links of A-1: out %v in %v, want only the one to A-2", nv.Out, nv.In)
		}
		if _, err := g.ViewNeighbourhood(ctx, main, b1.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("the neighbourhood of a node of another project: %v, want not found", err)
		}
		nb, err := g.ViewNeighbourhood(ctx, main, a1.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(nb.Nodes) != 1 || nb.Nodes[0].Key != "A-2" || len(nb.Links) != 1 {
			t.Fatalf("neighbourhood of A-1: %d nodes %v, %d links, want A-2 and one link", len(nb.Nodes), nb.Nodes, len(nb.Links))
		}

		page, err := g.ViewNodes(ctx, main, NodeQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if page.Total != 2 || len(page.Types) != 1 || page.Types[0].Count != 2 {
			t.Fatalf("page: total %d, types %v, want the two nodes of P-A", page.Total, page.Types)
		}
		all, err := g.ViewNodesOfType(ctx, main, "alm@Requirement")
		if err != nil || len(all) != 2 {
			t.Fatalf("nodes of the type: %d, %v, want 2", len(all), err)
		}
		links, err := g.BaselineLinks(ctx, page.Baseline, LinkQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if links.Total != 1 || len(links.Links) != 1 || links.Links[0].To.ID != a2.ID {
			t.Fatalf("links: total %d %v, want the one between A-1 and A-2", links.Total, links.Links)
		}
		bpage, err := g.BaselineNodes(ctx, page.Baseline, NodeQuery{})
		if err != nil || bpage.Total != 2 {
			t.Fatalf("baseline nodes: %d, %v, want 2", bpage.Total, err)
		}
		res, err := g.QueryView(ctx, main, "MATCH (n)--(m) RETURN m.key ORDER BY m.key", QueryOptions{Bind: map[string]domain.NodeID{"n": a1.ID}})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Rows) != 1 || res.Rows[0][0] != "A-2" {
			t.Fatalf("query: %v, want A-2 only", res.Rows)
		}
		snap, err := g.ExportSnapshot(ctx, "alm", "", "")
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, n := range snap.Nodes {
			keys = append(keys, n.Key)
		}
		slices.Sort(keys)
		if !slices.Equal(keys, []string{"A-1", "A-2"}) {
			t.Fatalf("export: %v, want A-1 and A-2", keys)
		}

		// a change of P-A sees what main shows P-A, and its own drafts
		head, _ := g.BranchHead(ctx, "alm", domain.MainBranch)
		c, err := g.CreateChange(ctx, NewChange{ProjectID: "P-A", Namespace: "alm", Title: "work", BaselineID: head.ID, OwnBranch: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "A-3", Type: "alm@Requirement", Rationale: "new"}); err != nil {
			t.Fatal(err)
		}
		cpage, err := g.ViewNodes(ctx, View{Change: c.ID}, NodeQuery{})
		if err != nil || cpage.Total != 3 {
			t.Fatalf("change view: %d, %v, want A-1, A-2 and the draft A-3", cpage.Total, err)
		}

		// an open namespace is read whole; no reach reads everything
		reach.Namespaces = map[string]bool{"alm": true}
		if _, err := g.ViewNode(ctx, main, "", "B-1"); err != nil {
			t.Fatalf("an open namespace: %v", err)
		}
		reach = nil
		if page, err := g.ViewNodes(ctx, main, NodeQuery{}); err != nil || page.Total != 4 {
			t.Fatalf("no reach: %d, %v, want the four nodes", page.Total, err)
		}
	})
}

// TestOwnedWrites: a change checks out only the nodes its project, or one below it, owns (ADR 0161 §3).
func TestOwnedWrites(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		if err := g.Bootstrap(ctx); err != nil {
			t.Fatal(err)
		}
		f := reachFixture{t: t, ctx: ctx, g: g}
		root := g.Structure(domain.StructureProject).Root
		f.project("P-A", root)
		f.project("P-AA", "P-A")
		f.project("P-B", root)
		a := f.commit("P-A", "alm", "A-1", "alm@Requirement")
		aa := f.commit("P-AA", "alm", "AA-1", "alm@Requirement")
		b := f.commit("P-B", "alm", "B-1", "alm@Requirement")
		g.OwnedWrites = func(ns string) bool { return ns == "alm" }

		checkout := func(project string, n domain.Node) error {
			head, _ := g.BranchHead(ctx, "alm", domain.MainBranch)
			c, err := g.CreateChange(ctx, NewChange{ProjectID: project, Namespace: "alm", Title: "edit", BaselineID: head.ID, OwnBranch: true})
			if err != nil {
				t.Fatal(err)
			}
			_, err = g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Node: n.ID, Rationale: "edit"})
			return err
		}
		if err := checkout("P-A", a); err != nil {
			t.Fatalf("its own project's node: %v", err)
		}
		if err := checkout("P-A", aa); err != nil {
			t.Fatalf("a node of a sub-project: %v", err)
		}
		if err := checkout("P-A", b); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("a node of another project: %v, want forbidden", err)
		}
		if err := checkout("P-AA", a); !errors.Is(err, authz.ErrForbidden) {
			t.Fatalf("a node of the parent project: %v, want forbidden", err)
		}
		g.OwnedWrites = nil
		if err := checkout("P-A", b); err != nil {
			t.Fatalf("no rule: %v", err)
		}
	})
}
