package graph

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
)

// countingRepo counts the single-row reads the graph asks its store for: a read path that asks one per element it
// meets (one per neighbour, one per change of a history) shows here, whatever the store.
type countingRepo struct {
	Repo
	nodes, heads atomic.Int64
}

func (r *countingRepo) InTx(ctx context.Context, fn func(tx Tx) error) error {
	return r.Repo.InTx(ctx, func(tx Tx) error {
		return fn(&countingTx{Tx: tx, r: r})
	})
}

type countingTx struct {
	Tx
	r *countingRepo
}

func (t *countingTx) Node(ctx context.Context, ref domain.NodeRef) (domain.Node, error) {
	t.r.nodes.Add(1)
	return t.Tx.Node(ctx, ref)
}

func (t *countingTx) ChangeHead(ctx context.Context, id domain.ChangeID) (domain.Change, error) {
	t.r.heads.Add(1)
	return t.Tx.ChangeHead(ctx, id)
}

// The reads of the current graph read what they meet in batches: the neighbours of a hub, a query's hop through it,
// the blackboard of a change touching it, the changes of a node's history, cost a fixed number of reads, not one per
// link or per change.
func TestReadsAreBatched(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		cr := &countingRepo{Repo: repo}
		g := New(cr)
		const fan = 40
		var e []NodeEdit
		hub := NodeEdit{Key: "HUB", Type: "Need", Props: map[string]any{"title": "hub"}}
		for i := 0; i < fan; i++ {
			k, s := fmt.Sprintf("T-%d", i), fmt.Sprintf("S-%d", i)
			e = append(e, NodeEdit{Key: k, Type: "Need", Props: map[string]any{"title": k}},
				NodeEdit{Key: s, Type: "Need", Props: map[string]any{"title": s}, Links: []LinkEdit{{Type: "refines", ToKey: "HUB"}}})
			hub.Links = append(hub.Links, LinkEdit{Type: "depends", ToKey: k})
		}
		if _, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "seed", By: "t", Edits: append(e, hub)}); err != nil {
			t.Fatal(err)
		}
		var last domain.ChangeID
		for v := 0; v < 5; v++ {
			n, err := g.NodeByKey(ctx, domain.DefaultNamespace, "HUB")
			if err != nil {
				t.Fatal(err)
			}
			r := n.Ref()
			res, err := g.CommitEdits(ctx, Edits{ProjectID: "PROJ-ROOT", Title: "v", By: "t", Edits: []NodeEdit{{Pre: &r, Props: map[string]any{"title": fmt.Sprint(v)}}}})
			if err != nil {
				t.Fatal(err)
			}
			last = res.Change
		}
		hubNode, err := g.NodeByKey(ctx, domain.DefaultNamespace, "HUB")
		if err != nil {
			t.Fatal(err)
		}
		main := View{Namespace: domain.DefaultNamespace, Branch: domain.MainBranch}
		reads := func(name string, max int64, f func() error) {
			t.Helper()
			n0, h0 := cr.nodes.Load(), cr.heads.Load()
			if err := f(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if n, h := cr.nodes.Load()-n0, cr.heads.Load()-h0; n+h > max {
				t.Errorf("%s: %d node reads and %d change header reads one by one, want at most %d", name, n, h, max)
			}
		}
		reads("neighbourhood", 4, func() error {
			nb, err := g.ViewNeighbourhood(ctx, main, hubNode.ID)
			if err == nil && len(nb.Nodes) != 2*fan {
				err = fmt.Errorf("%d neighbours, want %d", len(nb.Nodes), 2*fan)
			}
			return err
		})
		reads("query hop", 6, func() error {
			res, err := g.QueryView(ctx, main, "MATCH (h {key: 'HUB'})-[:depends]->(t) RETURN t.key", QueryOptions{})
			if err == nil && len(res.Rows) != fan {
				err = fmt.Errorf("%d rows, want %d", len(res.Rows), fan)
			}
			return err
		})
		reads("blackboard", 8, func() error {
			bb, err := g.Blackboard(ctx, last)
			if err == nil && len(bb.Neighbors) < fan {
				err = fmt.Errorf("%d neighbours, want %d", len(bb.Neighbors), 2*fan)
			}
			return err
		})
		reads("node changes", 1, func() error {
			cs, _, err := g.NodeChanges(ctx, hubNode.ID, 0, 20)
			if err == nil && len(cs) != 6 {
				err = fmt.Errorf("%d changes, want 6", len(cs))
			}
			return err
		})
		reads("change stack", 2, func() error {
			_, err := g.ChangeStack(ctx, ChangeStackQuery{Node: hubNode.Ref()})
			return err
		})
	})
}
