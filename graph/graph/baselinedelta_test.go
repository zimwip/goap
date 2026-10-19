package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// A baseline is stored as a delta from its parent, whole every checkpointEvery baselines (ADR 0032): reading it back
// gives what was written, across checkpoints, with nodes added, changed and removed.
func TestBaselineDeltas(t *testing.T) {
	old, oldEvery := checkpointEvery, DefaultMaterializeEvery
	checkpointEvery, DefaultMaterializeEvery = 3, 1 // every baseline materialised: what is stored are its entries
	defer func() { checkpointEvery, DefaultMaterializeEvery = old, oldEvery }()
	forEachRepo(t, testBaselineDeltas)
}

func testBaselineDeltas(t *testing.T, repo Repo) {
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	written := []domain.Baseline{f.base}
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
		got := must[domain.Baseline](t)(g.Baseline(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("baseline %d: read %v, wrote %v", i, got.Nodes, want.Nodes)
		}
		nodes, _ := must2(t)(g.BaselineGraph(ctx, want.ID))
		if len(nodes) != len(want.Nodes) {
			t.Fatalf("baseline %d: %d nodes, want %d", i, len(nodes), len(want.Nodes))
		}
		for _, n := range nodes {
			if !want.Contains(n.Ref()) {
				t.Fatalf("baseline %d: %s is not in it", i, n.Ref())
			}
		}
	}
	// what is stored: whole at a checkpoint, the difference with the parent otherwise
	counts := storedEntryCounts(t, repo, written)
	if counts == nil {
		return // the memory store keeps whole maps
	}
	deltas := 0
	for i, b := range written {
		if counts[i] < len(b.Nodes) {
			deltas++
		}
	}
	if deltas == 0 {
		t.Fatalf("no baseline stored as a delta: %v", counts)
	}
}

// storedEntryCounts counts the stored entries of each baseline (nil for the memory store).
func storedEntryCounts(t *testing.T, repo Repo, bs []domain.Baseline) []int {
	t.Helper()
	ctx := context.Background()
	out := make([]int, len(bs))
	for i, b := range bs {
		var err error
		switch r := repo.(type) {
		case *SQLite:
			err = r.db.QueryRowContext(ctx, `SELECT count(*) FROM baseline_entry WHERE baseline_id = ?`, string(b.ID)).Scan(&out[i])
		case *Postgres:
			err = r.pool.QueryRow(ctx, `SELECT count(*) FROM baseline_entry WHERE baseline_id = $1`, string(b.ID)).Scan(&out[i])
		default:
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// checkLandings verifies that every landed event agrees with the baseline it names (ADR 0032): the version is in it
// (a tombstone: the node is not), and the baseline changed it from its parent.
func checkLandings(t *testing.T, repo Repo) {
	t.Helper()
	if t.Failed() {
		return
	}
	ctx := context.Background()
	err := New(repo).repo.InTx(ctx, func(tx Tx) error { // through the guard: a baseline kept as a header only is computed
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
				if e.Op != domain.ImpactLanded || e.Baseline == "" {
					continue
				}
				b, err := tx.Baseline(ctx, e.Baseline)
				if err != nil {
					return err
				}
				var parent map[domain.NodeID]domain.Version
				if b.ParentID != "" {
					p, err := tx.Baseline(ctx, b.ParentID)
					if err != nil {
						return err
					}
					parent = p.Nodes
				}
				n, err := tx.Node(ctx, *e.Landed)
				if err != nil {
					return err
				}
				v, in := b.Nodes[n.ID]
				switch {
				case n.Deleted && in:
					t.Errorf("change %s: %s landed as a tombstone in %s, which holds v%d", c.ID, n.Key, b.ID, v)
				case !n.Deleted && v != n.Version:
					t.Errorf("change %s: %s landed as v%d in %s, which holds v%d", c.ID, n.Key, n.Version, b.ID, v)
				case !n.Deleted && parent[n.ID] == n.Version:
					t.Errorf("change %s: %s landed as v%d in %s, already in its parent", c.ID, n.Key, n.Version, b.ID)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A commit writes no entry (ADR 0140): its baseline is a header, its state the versions it landed put in the state of
// the baseline before, read back the same through every chain of headers.
func TestCommitWritesNoEntry(t *testing.T) { forEachRepo(t, testCommitWritesNoEntry) }

func testCommitWritesNoEntry(t *testing.T, repo Repo) {
	if DefaultMaterializeEvery != 0 {
		t.Skip("GOAP_MATERIALIZE_EVERY stores entries")
	}
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	var written []domain.Baseline
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
		got := must[domain.Baseline](t)(g.Baseline(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("baseline %d: read %v, wrote %v", i, got.Nodes, want.Nodes)
		}
		var some map[domain.NodeID]domain.Version
		if err := g.repo.InTx(ctx, func(tx Tx) (err error) {
			some, err = versionsAt(ctx, tx, want.ID, []domain.NodeID{f.req.ID, "nope"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(some) != 1 || some[f.req.ID] != want.Nodes[f.req.ID] {
			t.Fatalf("baseline %d: versions %v, want %s v%d", i, some, f.req.ID, want.Nodes[f.req.ID])
		}
	}
	counts := storedEntryCounts(t, repo, written)
	for i, c := range counts {
		if c != 0 {
			t.Fatalf("baseline %d stores %d entries: %v", i, c, counts)
		}
	}
}

func TestCheckpoints(t *testing.T) { forEachRepo(t, testCheckpoints) }

// testCheckpoints: every CheckpointEvery-th header of a branch stores its state after its commit, and the headers
// committed after it are kept over it, their states unchanged (ADR 0140).
func testCheckpoints(t *testing.T, repo Repo) {
	if DefaultMaterializeEvery != 0 {
		t.Skip("GOAP_MATERIALIZE_EVERY stores entries")
	}
	ctx := context.Background()
	f := newFixture(t, repo)
	g := f.g
	g.CheckpointEvery = 3
	var written []domain.Baseline
	head := f.base
	for i := range 8 {
		edits := []NodeEdit{{Key: fmt.Sprintf("DES-%d", i), Type: "Design"}}
		if i%2 == 1 {
			edits = append(edits, NodeEdit{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": i}})
		}
		head = commitOn(t, g, "", head.ID, edits...)
		g.WaitCheckpoints()
		written = append(written, head)
	}
	var stored []domain.BaselineID
	headers := map[domain.BaselineID]domain.Baseline{}
	if err := repo.InTx(ctx, func(tx Tx) error {
		for _, b := range written {
			h, err := tx.BaselineHeader(ctx, b.ID)
			if err != nil {
				return err
			}
			headers[b.ID] = h
			if h.Gap == 0 {
				stored = append(stored, b.ID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 { // the fixture committed twice before: the 1st, 4th and 7th commits are 3rd headers
		t.Fatalf("stored %v, want 3 checkpoints out of 8 commits", stored)
	}
	for _, b := range written {
		h := headers[b.ID]
		if h.Gap == 0 {
			continue
		}
		var want domain.BaselineID
		for _, cp := range stored {
			if headers[cp].Seq < h.Seq {
				want = cp
			}
		}
		if h.BaseID != want {
			t.Fatalf("header %s is kept over %s, want the checkpoint %s", b.ID, h.BaseID, want)
		}
	}
	// a header committed after a checkpoint stored late (here by hand) is kept over it
	g.CheckpointEvery = 0
	head = commitOn(t, g, "", head.ID, NodeEdit{Key: "DES-8", Type: "Design"})
	written = append(written, head)
	if err := g.Materialize(ctx, written[7].ID); err != nil {
		t.Fatal(err)
	}
	head = commitOn(t, g, "", head.ID, NodeEdit{Key: "DES-9", Type: "Design"})
	written = append(written, head)
	if err := repo.InTx(ctx, func(tx Tx) error {
		h, err := tx.BaselineHeader(ctx, head.ID)
		if err == nil && h.BaseID != written[7].ID {
			t.Errorf("header kept over %s, want the late checkpoint %s", h.BaseID, written[7].ID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	reread := New(repo) // no cache
	for i, want := range written {
		got := must[domain.Baseline](t)(reread.Baseline(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("baseline %d: read %v, wrote %v", i, got.Nodes, want.Nodes)
		}
	}
}

// checkStates verifies that what the changes did tells the state of every baseline (ADR 0056, 0140): replaying, from
// the state of its parent, the `landed` events naming a baseline whose state is derived gives the state the graph reads
// for it, whatever the baselines in between are stored as. A baseline that is not derived is a source and is skipped.
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
		landed := map[domain.BaselineID][]domain.NodeRef{}
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
				if e.Op == domain.ImpactLanded && e.Baseline != "" && e.Landed != nil {
					landed[e.Baseline] = append(landed[e.Baseline], *e.Landed)
				}
			}
		}
		for _, ns := range nss {
			bs, err := tx.Baselines(ctx, ns)
			if err != nil {
				return err
			}
			byID := map[domain.BaselineID]domain.Baseline{}
			for _, b := range bs {
				byID[b.ID] = b
			}
			for _, b := range bs {
				if !domain.DerivedKind(b.Kind) || b.ParentID == "" {
					continue
				}
				p, ok := byID[b.ParentID]
				if !ok {
					t.Errorf("baseline %s: parent %s is unknown", b.ID, b.ParentID)
					continue
				}
				want := maps.Clone(p.Nodes)
				refs := landed[b.ID]
				if b.Kind == domain.BaselineFastForward {
					// a fast-forward lands what its change joined to the branch, the last version of each node
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
							if j.Branch == domain.BranchOf(b.Branch) && j.Change == b.ChangeID && j.Version > last {
								last = j.Version
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
					t.Errorf("baseline %s (%s, %s): replayed %d nodes, %d read:%s", b.ID, b.Name, b.Kind, len(want), len(b.Nodes), diff)
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
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "c", Baseline: head.ID, By: "test", Edits: []NodeEdit{
		{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": 1}},
	}}); err != nil {
		t.Fatal(err)
	}
	changes := len(must[[]domain.Change](t)(g.Changes(ctx)))
	if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "stale", Baseline: head.ID, By: "test", Edits: []NodeEdit{
		{Key: "DES-3", Type: "Design"},
		{Pre: &domain.NodeRef{ID: f.req.ID, Version: head.Nodes[f.req.ID]}, Props: map[string]any{"step": 2}},
	}}); err == nil {
		t.Fatal("a commit from a stale version lands")
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

// checkDiffs checks the differences read from the distance between two baselines (ADR 0140) against the ones of their
// whole states and whole lineages: each baseline against its parent, the first, the middle and the last one, both ways.
func checkDiffs(ctx context.Context, t *testing.T, tx Tx, bs []domain.Baseline) error {
	if len(bs) == 0 {
		return nil
	}
	heads, err := tx.BaselineHeaders(ctx, bs[0].Namespace)
	if err != nil {
		return err
	}
	byID := map[domain.BaselineID]domain.Baseline{}
	for _, b := range bs {
		byID[b.ID] = b
	}
	branchOf := branchOfChange(ctx, tx)
	stack := func(id domain.BaselineID) []domain.ChangeID {
		l, _ := domain.BaselineLineage(heads, id, branchOf, len(heads))
		return domain.StackChangeIDs(nil, l)
	}
	for _, b := range bs {
		for _, other := range []domain.BaselineID{b.ParentID, bs[0].ID, bs[len(bs)/2].ID, bs[len(bs)-1].ID} {
			o, ok := byID[other]
			if !ok {
				continue
			}
			for _, pair := range [][2]domain.Baseline{{o, b}, {b, o}} {
				from, to := pair[0], pair[1]
				wantAdded, wantRemoved := domain.DiffStacks(stack(from.ID), stack(to.ID))
				gap, err := lineageGap(ctx, tx, from.ID, to.ID)
				if err != nil {
					return err
				}
				if fmt.Sprint(gap.added) != fmt.Sprint(wantAdded) || fmt.Sprint(gap.removed) != fmt.Sprint(wantRemoved) {
					t.Errorf("changes from %s (%s) to %s (%s): +%v -%v, the whole lineages give +%v -%v", from.ID, from.Kind, to.ID, to.Kind, gap.added, gap.removed, wantAdded, wantRemoved)
				}
				ds, err := diffBaselines(ctx, tx, from.ID, to.ID)
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
