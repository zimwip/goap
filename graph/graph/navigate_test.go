package graph

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/platform"
)

// tree commits a tree of depth levels under the node R: each inner node holds fanout links `child` to its children and
// decoys links `ref` to the nodes of a pool, the links a navigation along `child` should not read.
func tree(t testing.TB, g *Graph, levels, fanout, decoys int) domain.Node {
	t.Helper()
	ctx := context.Background()
	const pool = 50
	var edits []NodeEdit
	for i := range pool {
		edits = append(edits, NodeEdit{Key: fmt.Sprintf("P%d", i), Type: "Pool"})
	}
	var walk func(key string, level int)
	walk = func(key string, level int) {
		e := NodeEdit{Key: key, Type: "Item"}
		if level < levels {
			for i := range fanout {
				child := fmt.Sprintf("%s.%d", key, i)
				e.Links = append(e.Links, LinkEdit{Type: "child", ToKey: child})
				walk(child, level+1)
			}
			for i := range decoys {
				e.Links = append(e.Links, LinkEdit{Type: "ref", ToKey: fmt.Sprintf("P%d", i%pool)})
			}
		}
		edits = append(edits, e)
	}
	walk("R", 0)
	if _, err := g.CommitEdits(ctx, Edits{Title: "tree", By: "tester", ProjectID: "PROJ-ROOT", Edits: edits}); err != nil {
		t.Fatal(err)
	}
	root, err := g.NodeByKey(ctx, "", "R")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// hops walks from the version ref along the links of some types, hop by hop, each hop one read of the frontier:
// typed, by LinksFromOfType; else by LinksFrom, the other types left out after the read. It returns the versions
// reached at each hop.
func hops(ctx context.Context, tx Tx, from domain.NodeRef, n int, typ string, typed bool) ([]int, error) {
	frontier, reached := []domain.NodeRef{from}, []int{}
	for range n {
		var ls []domain.Link
		var err error
		if typed {
			ls, err = tx.LinksFromOfType(ctx, frontier, []string{typ})
		} else {
			ls, err = tx.LinksFrom(ctx, frontier)
			ls = ofTypes(ls, []string{typ})
		}
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, l := range ls {
			frontier = append(frontier, l.To)
		}
		reached = append(reached, len(frontier))
	}
	return reached, nil
}

// TestLinksOfType: a hop of a navigation reads the links of its types only, the links of a set read through its
// chain of deltas (a dropped link stays out), entering links by type and version.
func TestLinksOfType(t *testing.T) {
	forEachRepo(t, func(t *testing.T, repo Repo) {
		ctx := context.Background()
		g := New(repo)
		root := tree(t, g, 3, 3, 4)
		err := g.repo.InTx(ctx, func(tx Tx) error {
			for _, typed := range []bool{false, true} {
				got, err := hops(ctx, tx, root.Ref(), 3, "child", typed)
				if err != nil {
					return err
				}
				if !slices.Equal(got, []int{3, 9, 27}) {
					t.Fatalf("typed %v: reached %v", typed, got)
				}
			}
			none, err := tx.LinksFromOfType(ctx, []domain.NodeRef{root.Ref()}, nil)
			if err != nil || len(none) != 0 {
				t.Fatalf("no type: %v, %v", none, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}

		// a new version of R drops a child: its set is a delta over the first one
		out, err := g.OutLinksOf(ctx, root.Ref())
		if err != nil {
			t.Fatal(err)
		}
		var drop domain.Link
		for _, l := range out {
			if l.Type == "child" {
				drop = l
				break
			}
		}
		pre := root.Ref()
		if _, err := g.CommitEdits(ctx, Edits{Title: "drop", By: "tester", ProjectID: "PROJ-ROOT", Edits: []NodeEdit{{Pre: &pre, RemoveLinks: []domain.LinkID{drop.ID}}}}); err != nil {
			t.Fatal(err)
		}
		v2, err := g.NodeByKey(ctx, "", "R")
		if err != nil {
			t.Fatal(err)
		}
		err = g.repo.InTx(ctx, func(tx Tx) error {
			ls, err := tx.LinksFromOfType(ctx, []domain.NodeRef{v2.Ref()}, []string{"child"})
			if err != nil {
				return err
			}
			if len(ls) != 2 || slices.ContainsFunc(ls, func(l domain.Link) bool { return l.To == drop.To }) {
				t.Fatalf("the children of the second version: %v", ls)
			}
			refs, err := tx.LinksFromOfType(ctx, []domain.NodeRef{v2.Ref()}, []string{"ref"})
			if err != nil || len(refs) != 4 {
				t.Fatalf("the refs of the second version: %v, %v", refs, err)
			}
			// entering the dropped child: held by the first version of R only
			in, err := tx.LinksToOfType(ctx, drop.To, []string{"child"})
			if err != nil || len(in) != 1 || in[0].From != root.Ref() {
				t.Fatalf("entering %v: %v, %v", drop.To, in, err)
			}
			if in, err := tx.LinksToOfType(ctx, drop.To, []string{"ref"}); err != nil || len(in) != 0 {
				t.Fatalf("entering %v by ref: %v, %v", drop.To, in, err)
			}
			// entering a pool node by ref, any version: one per version holding the link
			i := slices.IndexFunc(out, func(l domain.Link) bool { return l.Type == "ref" })
			pool, err := tx.LinksToOfType(ctx, domain.NodeRef{ID: out[i].To.ID}, []string{"ref"})
			if err != nil || !slices.ContainsFunc(pool, func(l domain.Link) bool { return l.From == v2.Ref() }) {
				t.Fatalf("entering a pool node: %v, %v", pool, err)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

// BenchmarkNavigation measures a three-hop navigation along `child` on SQLite, each inner node also holding decoy
// links of another type: the hops read every link of the frontier then keep the type (untyped) or read the type only
// (typed), with the index of the links by source with the type (link_from as it is) or without it (as it was).
func BenchmarkNavigation(b *testing.B) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(b.TempDir(), "goap.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "graph", SQLiteMigrations, "migrations_sqlite"); err != nil {
		b.Fatal(err)
	}
	g := New(NewSQLite(db))
	root := tree(b, g, 3, 10, 200)
	index := func(cols string) {
		for _, q := range []string{`DROP INDEX link_from`, `CREATE INDEX link_from ON link (` + cols + `)`, `ANALYZE`} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				b.Fatal(err)
			}
		}
	}
	run := func(b *testing.B, typed bool) {
		for b.Loop() {
			err := g.repo.InTx(ctx, func(tx Tx) error {
				got, err := hops(ctx, tx, root.Ref(), 3, "child", typed)
				if err == nil && got[2] != 1000 {
					err = fmt.Errorf("reached %v", got)
				}
				return err
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	}
	for _, ix := range []struct{ name, cols string }{{"index-without-type", "from_id, from_version"}, {"index-with-type", "from_id, from_version, type"}} {
		index(ix.cols)
		b.Run(ix.name+"/untyped", func(b *testing.B) { run(b, false) })
		b.Run(ix.name+"/typed", func(b *testing.B) { run(b, true) })
	}
}
