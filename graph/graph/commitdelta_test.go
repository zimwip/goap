package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// The state of a commit is the selection of the versions committed (ADR 0143): reading it back gives what was written,
// with nodes added and changed.
func TestCommitStates(t *testing.T) { forEachRepo(t, testCommitStates) }

func testCommitStates(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	written := []domain.Commit{f.base}
	head := f.base
	for i := range 7 {
		edits := []NodeEdit{{Key: fmt.Sprintf("DES-%d", i), Type: "Design"}}
		switch i {
		case 2:
			edits = append(edits, NodeEdit{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": i}})
		case 4:
			edits = append(edits, NodeEdit{Pre: &domain.NodeRef{ID: f.test.ID, Version: head.Nodes[f.test.ID]}, Props: map[string]any{"obsolete": true}})
		}
		head = commitOn(t, g, "", head.ID, edits...)
		written = append(written, head)
	}
	for i, want := range written {
		got := must[domain.Commit](t)(g.Commit(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("commit %d: read %v, wrote %v", i, got.Nodes, want.Nodes)
		}
		nodes, _ := must2(t)(g.CommitGraph(ctx, want.ID))
		if len(nodes) != len(want.Nodes) {
			t.Fatalf("commit %d: %d nodes, want %d", i, len(nodes), len(want.Nodes))
		}
		for _, n := range nodes {
			if !want.Contains(n.Ref()) {
				t.Fatalf("commit %d: %s is not in it", i, n.Ref())
			}
		}
	}
}

// checkCommits verifies that every committed event agrees with the commit it names (ADR 0032): the version is in it
// (a tombstone: the node is not), and the commit changed it from its parent.
func checkCommits(t *testing.T, repo Repo) {
	t.Helper()
	if t.Failed() {
		return
	}
	ctx := context.Background()
	err := New(repo).repo.InTx(ctx, func(tx Tx) error { // through the guard: a commit kept as a header only is computed
		cs, err := tx.Changes(ctx)
		if err != nil {
			return err
		}
		for _, c := range cs {
			events, err := impactEvents(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			for _, e := range events {
				if e.Op != domain.ImpactCommitted || e.Commit == "" {
					continue
				}
				b, err := tx.Commit(ctx, e.Commit)
				if err != nil {
					return err
				}
				var parent map[domain.NodeID]domain.Version
				if b.ParentID != "" {
					p, err := tx.Commit(ctx, b.ParentID)
					if err != nil {
						return err
					}
					parent = p.Nodes
				}
				n, err := tx.Node(ctx, *e.Committed)
				if err != nil {
					return err
				}
				v, in := b.Nodes[n.ID]
				switch {
				case n.Reason == domain.ReasonRevert && !n.Deleted:
					// a revert reads as the version of the fork it copies (ADR 0140)
					if !in {
						t.Errorf("change %s: %s reverted as v%d in %s, which does not hold it", c.ID, n.Key, n.Version, b.ID)
					}
				case n.Deleted && in:
					t.Errorf("change %s: %s committed as a tombstone in %s, which holds v%d", c.ID, n.Key, b.ID, v)
				case !n.Deleted && v != n.Version:
					t.Errorf("change %s: %s committed as v%d in %s, which holds v%d", c.ID, n.Key, n.Version, b.ID, v)
				case !n.Deleted && parent[n.ID] == n.Version:
					t.Errorf("change %s: %s committed as v%d in %s, already in its parent", c.ID, n.Key, n.Version, b.ID)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A commit writes a header (ADR 0143): its state, whole or for some nodes, is the selection of the versions committed.
func TestCommitWritesNoEntry(t *testing.T) { forEachRepo(t, testCommitWritesNoEntry) }

func testCommitWritesNoEntry(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	var written []domain.Commit
	head := f.base
	for i := range 6 {
		edits := []NodeEdit{{Key: fmt.Sprintf("DES-%d", i), Type: "Design"}}
		if i == 3 {
			edits = append(edits, NodeEdit{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": i}})
		}
		head = commitOn(t, g, "", head.ID, edits...)
		written = append(written, head)
	}
	for i, want := range written {
		got := must[domain.Commit](t)(g.Commit(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("commit %d: read %v, wrote %v", i, got.Nodes, want.Nodes)
		}
		var some map[domain.NodeID]domain.Version
		if err := g.repo.InTx(ctx, func(tx Tx) (err error) {
			some, err = versionsAt(ctx, tx, want.ID, []domain.NodeID{f.req.ID, "nope"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(some) != 1 || some[f.req.ID] != want.Nodes[f.req.ID] {
			t.Fatalf("commit %d: versions %v, want %s v%d", i, some, f.req.ID, want.Nodes[f.req.ID])
		}
	}
}

// checkStates verifies that what the changes did tells the state of every commit (ADR 0056, 0140): replaying, from
// the state of its parent, the `committed` events naming a commit whose state is derived gives the state the graph reads
// for it, whatever the commits in between are stored as. A commit that is not derived is a source and is skipped.
func checkStates(t *testing.T, repo Repo) {
	t.Helper()
	if t.Failed() {
		return
	}
	ctx := context.Background()
	err := New(repo).repo.InTx(ctx, func(tx Tx) error { // through the guard: the state of a header is computed
		nss, err := tx.Namespaces(ctx)
		if err != nil {
			return err
		}
		committed := map[domain.CommitID][]domain.NodeRef{}
		cs, err := tx.Changes(ctx)
		if err != nil {
			return err
		}
		for _, c := range cs {
			events, err := impactEvents(ctx, tx, c.ID)
			if err != nil {
				return err
			}
			for _, e := range events {
				if e.Op == domain.ImpactCommitted && e.Commit != "" && e.Committed != nil {
					committed[e.Commit] = append(committed[e.Commit], *e.Committed)
				}
			}
		}
		for _, ns := range nss {
			bs, err := tx.Commits(ctx, ns)
			if err != nil {
				return err
			}
			byID := map[domain.CommitID]domain.Commit{}
			for _, b := range bs {
				byID[b.ID] = b
			}
			for _, b := range bs {
				if !replayable(b.Kind) || b.ParentID == "" {
					continue
				}
				p, ok := byID[b.ParentID]
				if !ok {
					t.Errorf("commit %s: parent %s is unknown", b.ID, b.ParentID)
					continue
				}
				want := maps.Clone(p.Nodes)
				if br, err := tx.Branch(ctx, b.Namespace, b.Branch); err == nil && br.Ephemeral && b.Kind != domain.CommitKindFastForward {
					// a change's branch takes the commits of its target by a rebase (ADR 0177): the rows it took
					// between its parent and this commit are part of the state the commit stands on
					after := int64(0)
					if p.Branch == b.Branch {
						after = p.Seq
					}
					taken, err := tx.CommittedVersions(ctx, b.Namespace, b.Branch, after, b.Seq-1, nil)
					if err != nil {
						return err
					}
					for id, v := range taken {
						if v == 0 {
							delete(want, id)
						} else {
							want[id] = v
						}
					}
				}
				refs := committed[b.ID]
				if b.Kind == domain.CommitKindFastForward {
					// a fast-forward commits what its change joined to the branch, the last version of each node
					refs = nil
					ids := maps.Clone(p.Nodes)
					maps.Copy(ids, b.Nodes)
					for id := range ids {
						js, err := tx.VersionJoins(ctx, id)
						if err != nil {
							return err
						}
						last := domain.Version(0)
						for _, j := range js {
							if j.Branch != domain.BranchOf(b.Branch) || j.Change != b.ChangeID || j.Version <= last {
								continue
							}
							last = j.Version
						}
						if last > 0 {
							// a change open after its merge (ADR 0166) joins its later commits at its later merges: the
							// version this one took is the last its branch committed before it
							ch, err := tx.ChangeHead(ctx, b.ChangeID)
							if err != nil {
								return err
							}
							cv, err := tx.BranchCommits(ctx, b.Namespace, ch.Branch, b.Seq, []domain.NodeID{id})
							if err != nil {
								return err
							}
							if v, ok := cv[id]; ok {
								last = v.Version
							}
						}
						if last > 0 {
							refs = append(refs, domain.NodeRef{ID: id, Version: last})
						}
					}
				}
				for _, ref := range refs {
					n, err := tx.Node(ctx, ref)
					if err != nil {
						return err
					}
					if n.Deleted {
						delete(want, n.ID)
					} else {
						want[n.ID] = n.Version
					}
				}
				if !maps.Equal(want, b.Nodes) {
					diff := ""
					for id, v := range b.Nodes {
						if want[id] != v {
							diff += fmt.Sprintf(" %s: read v%d, replayed v%d;", id, v, want[id])
						}
					}
					for id, v := range want {
						if _, ok := b.Nodes[id]; !ok {
							diff += fmt.Sprintf(" %s: not read, replayed v%d;", id, v)
						}
					}
					t.Errorf("commit %s (%s, %s): replayed %d nodes, %d read:%s", b.ID, b.Name, b.Kind, len(want), len(b.Nodes), diff)
				}
			}
			if err := checkDiffs(ctx, t, tx, bs); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFailedCommitLeavesNothing(t *testing.T) { forEachRepo(t, testFailedCommitLeavesNothing) }

// testFailedCommitLeavesNothing: a commit that fails leaves nothing on main; its change, once it committed on its own
// branch, stays abandoned.
func testFailedCommitLeavesNothing(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	head := f.base
	if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "c", Commit: head.ID, By: "test", Edits: []NodeEdit{
		{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	changes := len(must[[]domain.Change](t)(g.Changes(ctx)))
	if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "stale", Commit: head.ID, By: "test", Edits: []NodeEdit{
		{Key: "DES-3", Type: "Design"},
		{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": 2}},
	}}); err == nil {
		t.Fatal("a commit from a stale version commits")
	}
	for _, c := range must[[]domain.Change](t)(g.Changes(ctx))[changes:] {
		if c.Status != domain.ChangeAbandoned {
			t.Fatalf("a failed commit left change %s %s", c.ID, c.Status)
		}
	}
	if _, err := g.NodeByKey(ctx, head.Namespace, "DES-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a failed commit left its node: %v", err)
	}
}

// checkDiffs checks the differences read from the distance between two commits (ADR 0140) against the ones of their
// whole states and whole lineages: each commit against its parent, the first, the middle and the last one, both ways.
func checkDiffs(ctx context.Context, t *testing.T, tx Tx, bs []domain.Commit) error {
	if len(bs) == 0 {
		return nil
	}
	heads, err := tx.CommitHeaders(ctx, bs[0].Namespace)
	if err != nil {
		return err
	}
	byID := map[domain.CommitID]domain.Commit{}
	for _, b := range bs {
		byID[b.ID] = b
	}
	merged := mergedInto(ctx, tx)
	stack := func(id domain.CommitID) []domain.ChangeID {
		l, _ := domain.CommitLineage(heads, id, merged, len(heads))
		return domain.StackChangeIDs(nil, l)
	}
	for _, b := range bs {
		for _, other := range []domain.CommitID{b.ParentID, bs[0].ID, bs[len(bs)/2].ID, bs[len(bs)-1].ID} {
			o, ok := byID[other]
			if !ok {
				continue
			}
			for _, pair := range [][2]domain.Commit{{o, b}, {b, o}} {
				from, to := pair[0], pair[1]
				wantAdded, wantRemoved := domain.DiffStacks(stack(from.ID), stack(to.ID))
				gap, err := lineageGap(ctx, tx, from.ID, to.ID)
				if err != nil {
					return err
				}
				if fmt.Sprint(gap.added) != fmt.Sprint(wantAdded) || fmt.Sprint(gap.removed) != fmt.Sprint(wantRemoved) {
					t.Errorf("changes from %s (%s) to %s (%s): +%v -%v, the whole lineages give +%v -%v", from.ID, from.Kind, to.ID, to.Kind, gap.added, gap.removed, wantAdded, wantRemoved)
				}
				ds, err := diffCommits(ctx, tx, from.ID, to.ID)
				if err != nil {
					return err
				}
				got := map[domain.NodeID]domain.Version{}
				for _, d := range ds {
					got[d.Node] = 0
					if d.To != nil {
						got[d.Node] = d.To.Version
					}
				}
				want := map[domain.NodeID]domain.Version{}
				for id, v := range to.Nodes {
					if from.Nodes[id] != v {
						want[id] = v
					}
				}
				for id := range from.Nodes {
					if _, ok := to.Nodes[id]; !ok {
						want[id] = 0
					}
				}
				if !maps.Equal(got, want) {
					t.Errorf("diff from %s (%s) to %s (%s): %v, the whole states give %v", from.ID, from.Kind, to.ID, to.Kind, got, want)
				}
			}
		}
	}
	return nil
}

// replayable tells whether the state of a commit of this kind is replayed from what its change did: a commit and a
// merge from the `committed` events naming the commit, a fast-forward from the versions its change joined to the branch.
func replayable(kind string) bool {
	return kind == domain.CommitKindCommit || kind == domain.CommitKindMerge || kind == domain.CommitKindFastForward
}
