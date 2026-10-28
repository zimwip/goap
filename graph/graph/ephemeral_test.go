package graph

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

func TestEphemeralBranchHoldsNoCommit(t *testing.T) { forEachRepo(t, testEphemeralBranch) }

// testEphemeralBranch: the branch of a change is ephemeral, it holds no commit however many commits it has, and its
// state (its fork and its commits) still reads right (ADR 0140).
func testEphemeralBranch(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "seed", By: "t", Edits: []NodeEdit{{Key: "N-0", Type: "Need"}}}); err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Title: "c", OwnBranch: true, ProjectID: "PROJ-ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: fmt.Sprintf("M-%d", i), Type: "Need", Rationale: "r"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, domain.ReviewBatch{By: "t", Verdicts: []domain.ImpactVerdict{{Impact: cn.ID, Status: domain.ReviewAccepted, Comment: "ok"}}}); err != nil {
			t.Fatal(err)
		}
		if _, err := g.commitChange(ctx, c.ID, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.InTx(ctx, func(tx Tx) error {
		own, err := tx.Branch(ctx, c.Namespace, changeBranchName(c.ID))
		if err != nil {
			return err
		}
		if !own.Ephemeral {
			t.Fatalf("the branch of change %s is not ephemeral", c.ID)
		}
		hs, err := tx.BranchCommitHeaders(ctx, c.Namespace, own.Name)
		if err != nil {
			return err
		}
		if len(hs) != 0 {
			t.Fatalf("the branch of the change holds %d commits, want none", len(hs))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	b, err := g.BranchHead(ctx, c.Namespace, changeBranchName(c.ID))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Nodes) != 6 {
		t.Fatalf("the change's branch holds %d nodes, want 6", len(b.Nodes))
	}
}

func TestCommitNodesOfAChange(t *testing.T) { forEachRepo(t, testCommitNodesOfAChange) }

// testCommitNodesOfAChange: the nodes of a commit scoped by a change are the nodes its impacts act on, whatever
// else the commit holds (ADR 0140).
func testCommitNodesOfAChange(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	var edits []NodeEdit
	for i := range 5 {
		edits = append(edits, NodeEdit{Key: fmt.Sprintf("N-%d", i), Type: "Need"})
	}
	if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "seed", By: "t", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Title: "c", OwnBranch: true, ProjectID: "PROJ-ROOT"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: "M-1", Type: "Need", Rationale: "r"})
	if err != nil {
		t.Fatal(err)
	}
	n2, err := g.NodeByKey(ctx, domain.DefaultNamespace, "N-2")
	if err != nil {
		t.Fatal(err)
	}
	ref := n2.Ref()
	added, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: added[0].ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ImpactNodeUpdate(ctx, c.ID, added[0].ID, NodeUpdate{Properties: map[string]any{"title": "x"}}); err != nil {
		t.Fatal(err)
	}
	batch := domain.ReviewBatch{By: "t"}
	for _, id := range []domain.ChangeImpactID{created.ID, added[0].ID} {
		batch.Verdicts = append(batch.Verdicts, domain.ImpactVerdict{Impact: id, Status: domain.ReviewAccepted, Comment: "ok"})
	}
	if _, err := g.ImpactNodeReviewBatch(ctx, c.ID, batch); err != nil {
		t.Fatal(err)
	}
	b, err := g.apply(ctx, c.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	page, err := g.CommitNodes(ctx, b.ID, NodeQuery{Change: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, n := range page.Nodes {
		keys = append(keys, fmt.Sprintf("%s@%d", n.Key, n.Version))
	}
	if fmt.Sprint(keys) != "[M-1@1 N-2@2]" || page.Total != 2 {
		t.Fatalf("nodes of the change %v (total %d), want [M-1@1 N-2@2]", keys, page.Total)
	}
}

func TestNodeHistoryPages(t *testing.T) { forEachRepo(t, testNodeHistoryPages) }

// testNodeHistoryPages: the versions and the changes of a node are read a page at a time, newest first, whatever the
// length of its history (ADR 0140).
func testNodeHistoryPages(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	_, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "c0", By: "t", Edits: []NodeEdit{{Key: "N-0", Type: "Need", Props: map[string]any{"n": 0}}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := g.NodeByKey(ctx, "", "N-0")
	if err != nil {
		t.Fatal(err)
	}
	id := first.ID
	for i := 1; i < 7; i++ {
		n, err := g.Node(ctx, domain.NodeRef{ID: id})
		if err != nil {
			t.Fatal(err)
		}
		ref := n.Ref()
		if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: fmt.Sprintf("c%d", i), By: "t", Edits: []NodeEdit{{Pre: &ref, Props: map[string]any{"n": i}}}}); err != nil {
			t.Fatal(err)
		}
	}
	var versions []domain.Version
	pages := 0
	for before := domain.Version(0); ; pages++ {
		vs, next, err := g.VersionPage(ctx, id, before, 3)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(vs) - 1; i >= 0; i-- {
			versions = append(versions, vs[i].Version)
		}
		if next == 0 {
			break
		}
		before = next
	}
	if fmt.Sprint(versions) != "[7 6 5 4 3 2 1]" || pages != 2 {
		t.Fatalf("versions by pages of 3: %v in %d pages", versions, pages+1)
	}
	var titles []string
	for before := int64(0); ; {
		cs, next, err := g.NodeChanges(ctx, id, before, 3)
		if err != nil {
			t.Fatal(err)
		}
		if len(cs) > 3 {
			t.Fatalf("a page of 3 holds %d changes", len(cs))
		}
		for i := len(cs) - 1; i >= 0; i-- {
			titles = append(titles, cs[i].Title)
		}
		if next == 0 {
			break
		}
		before = next
	}
	if fmt.Sprint(titles) != "[c6 c5 c4 c3 c2 c1 c0]" {
		t.Fatalf("changes by pages of 3: %v", titles)
	}
}

func TestDiffCommitsByCommits(t *testing.T) { forEachRepo(t, testDiffCommitsByCommits) }

// testDiffCommitsByCommits: the difference of two commits of a branch read from what committed between them is the
// one of their whole states, whatever pair is asked (ADR 0140).
func testDiffCommitsByCommits(t *testing.T, repo Repo) {
	ctx := context.Background()
	g := New(repo)
	var heads []domain.CommitID
	commit := func(edits ...NodeEdit) {
		if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: fmt.Sprintf("c%d", len(heads)), By: "t", Edits: edits}); err != nil {
			t.Fatal(err)
		}
		head, err := g.BranchHeadHeader(ctx, "", domain.MainBranch)
		if err != nil {
			t.Fatal(err)
		}
		heads = append(heads, head.ID)
	}
	edit := func(key string, n int) NodeEdit {
		cur, err := g.NodeByKey(ctx, "", key)
		if err != nil {
			t.Fatal(err)
		}
		ref := cur.Ref()
		return NodeEdit{Pre: &ref, Props: map[string]any{"n": n}}
	}
	commit(NodeEdit{Key: "A", Type: "Need"}, NodeEdit{Key: "B", Type: "Need"})
	commit(NodeEdit{Key: "C", Type: "Need"})
	commit(edit("A", 1))
	commit(edit("B", 1), NodeEdit{Key: "D", Type: "Need"})
	commit(edit("A", 2), edit("C", 1))
	whole := func(from, to domain.CommitID) string {
		var a, b domain.Commit
		if err := g.repo.InTx(ctx, func(tx Tx) (err error) {
			if a, err = tx.Commit(ctx, from); err != nil {
				return err
			}
			b, err = tx.Commit(ctx, to)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		var out []string
		for id, v := range b.Nodes {
			if a.Nodes[id] != v {
				out = append(out, fmt.Sprintf("%s:%d>%d", id, a.Nodes[id], v))
			}
		}
		for id, v := range a.Nodes {
			if _, ok := b.Nodes[id]; !ok {
				out = append(out, fmt.Sprintf("%s:%d>0", id, v))
			}
		}
		slices.Sort(out)
		return fmt.Sprint(out)
	}
	for i := range heads {
		for j := range heads {
			ds, err := g.DiffCommits(ctx, heads[i], heads[j])
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range ds {
				var fv, tv domain.Version
				if d.From != nil {
					fv = d.From.Version
				}
				if d.To != nil {
					tv = d.To.Version
				}
				got = append(got, fmt.Sprintf("%s:%d>%d", d.Node, fv, tv))
			}
			slices.Sort(got)
			if want := whole(heads[i], heads[j]); fmt.Sprint(got) != want {
				t.Fatalf("diff c%d → c%d: %v, the whole states give %s", i, j, got, want)
			}
		}
	}
}
