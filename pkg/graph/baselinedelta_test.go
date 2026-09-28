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
	old := checkpointEvery
	checkpointEvery = 3
	defer func() { checkpointEvery = old }()
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
	err := repo.InTx(ctx, func(tx Tx) error {
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
