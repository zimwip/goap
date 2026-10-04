package graph

import (
	"context"
	"fmt"
	"maps"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
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
			edits = append(edits, NodeEdit{Pre: &domain.NodeRef{ID: f.test.ID, Version: head.Nodes[f.test.ID]}, Retire: true})
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
	if _, ok := head.Nodes[f.test.ID]; ok {
		t.Fatal("TST-1 was retired")
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

// Baselines stored whole (before ADR 0032, or under another checkpoint interval) are compacted to deltas without
// changing their content, and compacting again changes nothing.
func TestCompactBaselines(t *testing.T) { forEachRepo(t, testCompactBaselines) }

func testCompactBaselines(t *testing.T, repo Repo) {
	ctx := context.Background()
	old, oldEvery := checkpointEvery, DefaultMaterializeEvery
	defer func() { checkpointEvery, DefaultMaterializeEvery = old, oldEvery }()
	checkpointEvery, DefaultMaterializeEvery = 1, 1 // every baseline written whole, as before deltas
	f := newFixture(t, repo)
	g := f.g
	written := []domain.Baseline{f.base}
	head := f.base
	for i := range 6 {
		head = commitOn(t, g, "", head.ID, NodeEdit{Key: fmt.Sprintf("DES-%d", i), Type: "Design"})
		written = append(written, head)
	}
	before := storedEntryCounts(t, repo, written)
	checkpointEvery = 4
	n, err := g.CompactBaselines(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range written {
		got := must[domain.Baseline](t)(g.Baseline(ctx, want.ID))
		if !maps.Equal(got.Nodes, want.Nodes) {
			t.Fatalf("baseline %d after compaction: %v, want %v", i, got.Nodes, want.Nodes)
		}
	}
	if before == nil {
		if n != 0 {
			t.Fatalf("the memory store keeps whole maps, %d compacted", n)
		}
		return
	}
	after := storedEntryCounts(t, repo, written)
	total := func(cs []int) (s int) {
		for _, c := range cs {
			s += c
		}
		return
	}
	if n == 0 || total(after) >= total(before) {
		t.Fatalf("compacted %d: entries %v -> %v", n, before, after)
	}
	if again, err := g.CompactBaselines(ctx); err != nil || again != 0 {
		t.Fatalf("second compaction: %d %v", again, err)
	}
	// a baseline written after the compaction continues the chains
	next := commitOn(t, g, "", head.ID, NodeEdit{Key: "DES-X", Type: "Design"})
	if got := must[domain.Baseline](t)(g.Baseline(ctx, next.ID)); !maps.Equal(got.Nodes, next.Nodes) {
		t.Fatalf("next baseline: %v", got.Nodes)
	}
}

// checkStates verifies that what the changes did tells the state of every baseline (ADR 0056): replaying from the
// state of its parent the `landed` events of a commit baseline, or the versions the change joined to the branch for a
// fast-forward, gives exactly the entries stored for it, whatever the baselines in between
// were stored as (an entry-less baseline is a header only). A snapshot is not derived and is skipped.
func checkStates(t *testing.T, repo Repo) {
	t.Helper()
	if t.Failed() {
		return
	}
	ctx := context.Background()
	err := repo.InTx(ctx, func(tx Tx) error {
		nss, err := tx.Namespaces(ctx)
		if err != nil {
			return err
		}
		byID := map[domain.BaselineID]domain.Baseline{}
		memo := map[domain.BaselineID]map[domain.NodeID]domain.Version{}
		var state func(b domain.Baseline) (map[domain.NodeID]domain.Version, error)
		state = func(b domain.Baseline) (map[domain.NodeID]domain.Version, error) {
			if s, ok := memo[b.ID]; ok {
				return s, nil
			}
			if !domain.DerivedKind(b.Kind) {
				return b.Nodes, nil // stored whole
			}
			from := map[domain.NodeID]domain.Version{}
			if b.ParentID != "" {
				p, ok := byID[b.ParentID]
				if !ok {
					return nil, fmt.Errorf("baseline %s: parent %s is unknown", b.ID, b.ParentID)
				}
				var err error
				if from, err = state(p); err != nil {
					return nil, err
				}
			}
			out := maps.Clone(from)
			put := func(ref domain.NodeRef) error {
				n, err := tx.Node(ctx, ref)
				if err != nil {
					return err
				}
				if n.Deleted {
					delete(out, n.ID)
				} else {
					out[n.ID] = n.Version
				}
				return nil
			}
			if b.Kind != domain.BaselineFastForward {
				events, err := impactEvents(ctx, tx, b.ChangeID)
				if err != nil {
					return nil, err
				}
				for _, e := range events {
					if e.Op == domain.ImpactLanded && e.Baseline == b.ID {
						if err := put(*e.Landed); err != nil {
							return nil, err
						}
					}
				}
			} else {
				refs, err := tx.BranchJoins(ctx, b.Namespace, b.Branch, b.ChangeID)
				if err != nil {
					return nil, err
				}
				latest := map[domain.NodeID]domain.NodeRef{}
				for _, r := range refs {
					if r.Version > latest[r.ID].Version {
						latest[r.ID] = r
					}
				}
				for _, r := range latest {
					if err := put(r); err != nil {
						return nil, err
					}
				}
			}
			memo[b.ID] = out
			return out, nil
		}
		for _, ns := range nss {
			bs, err := tx.Baselines(ctx, ns)
			if err != nil {
				return err
			}
			for _, b := range bs {
				byID[b.ID] = b
			}
			for _, b := range bs {
				got, err := state(b)
				if err != nil {
					t.Errorf("%v", err)
				} else if b.Gap == 0 && !maps.Equal(got, b.Nodes) {
					diff := ""
					for id, v := range b.Nodes {
						if got[id] != v {
							diff += fmt.Sprintf(" %s: stored v%d, replayed v%d;", id, v, got[id])
						}
					}
					t.Errorf("baseline %s (%s, %s): replayed %d nodes, %d are stored:%s", b.ID, b.Name, b.Kind, len(got), len(b.Nodes), diff)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
