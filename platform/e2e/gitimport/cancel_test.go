package gitimporte2e

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/platform/gitimport"
)

// cancelling is the graph as a run sees it, cancelled at its n-th call: before the call is made, or once it returned
// (a cancel landing right after a write).
type cancelling struct {
	gitimport.Port
	cancel context.CancelFunc
	n      int
	after  bool
	calls  int
}

func (c *cancelling) at(ctx context.Context) context.Context {
	c.calls++
	if c.calls == c.n && !c.after {
		c.cancel()
	}
	return ctx
}

func (c *cancelling) done() {
	if c.calls == c.n && c.after {
		c.cancel()
	}
}

func (c *cancelling) CreateChange(ctx context.Context, in graphapi.NewChange) (domain.Change, error) {
	defer c.done()
	return c.Port.CreateChange(c.at(ctx), in)
}

func (c *cancelling) ImpactNodeCreate(ctx context.Context, id domain.ChangeID, in graphapi.NodeCreate) (domain.ChangeImpact, error) {
	defer c.done()
	return c.Port.ImpactNodeCreate(c.at(ctx), id, in)
}

func (c *cancelling) ImpactNodeCheckout(ctx context.Context, id domain.ChangeID, in graphapi.NodeCheckout) (domain.ChangeImpact, error) {
	defer c.done()
	return c.Port.ImpactNodeCheckout(c.at(ctx), id, in)
}

func (c *cancelling) ImpactNodeUpdate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, in graphapi.NodeUpdate) (domain.ChangeImpact, error) {
	defer c.done()
	return c.Port.ImpactNodeUpdate(c.at(ctx), id, impact, in)
}

func (c *cancelling) ImpactLinkCreate(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, l graphapi.LinkWrite, flow, execution string) (domain.Link, error) {
	defer c.done()
	return c.Port.ImpactLinkCreate(c.at(ctx), id, impact, l, flow, execution)
}

func (c *cancelling) ImpactLinkDelete(ctx context.Context, id domain.ChangeID, link domain.LinkID, flow, execution string) error {
	defer c.done()
	return c.Port.ImpactLinkDelete(c.at(ctx), id, link, flow, execution)
}

func (c *cancelling) ImpactNodeTransition(ctx context.Context, id domain.ChangeID, in graphapi.NodeTransition) (domain.ChangeImpact, error) {
	defer c.done()
	return c.Port.ImpactNodeTransition(c.at(ctx), id, in)
}

func (c *cancelling) ImpactNodeReviewBatch(ctx context.Context, id domain.ChangeID, b domain.ReviewBatch) ([]domain.ChangeImpact, error) {
	defer c.done()
	return c.Port.ImpactNodeReviewBatch(c.at(ctx), id, b)
}

func (c *cancelling) CommitChangeHeader(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	defer c.done()
	return c.Port.CommitChangeHeader(c.at(ctx), id, baselineName)
}

func (c *cancelling) IntegrateChange(ctx context.Context, id domain.ChangeID, r map[domain.NodeID]graphapi.Resolution) (domain.Change, error) {
	defer c.done()
	return c.Port.IntegrateChange(c.at(ctx), id, r)
}

// TestImportCancelled cancels a run at every write it makes, before and after the write: the run stops with the
// cancel, leaves no change open, and the next run brings the graph to the repository.
func TestImportCancelled(t *testing.T) {
	r := newRepo(t)
	r.write("README", "hello\n")
	r.write("src/main.c", "int main(){}\n")
	r.commit("Initial import")
	r.write("src/main.c", "int main(){return 0;}\n")
	r.write("docs/guide.md", "# Guide\n")
	r.commit("Fix main")
	r.git("rm", "-q", "README")
	r.commit("Drop README")

	// the writes of a whole run
	count := &cancelling{Port: newGraph(t), cancel: func() {}}
	if _, err := (&gitimport.Importer{Graph: count, Dir: r.dir, Name: "demo", ProjectID: project}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	sqlite := func(t *testing.T) *graph.Graph {
		ctx := context.Background()
		db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := platform.MigrateSQLite(ctx, db, "graph", graph.SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		return newGraphOn(t, graph.NewSQLite(db))
	}
	// the memory store does not watch the context, SQLite does: a transaction cancelled midway
	for _, store := range []func(*testing.T) *graph.Graph{newGraph, sqlite} {
		for _, after := range []bool{false, true} {
			for n := 1; n <= count.calls; n++ {
				g := store(t)
				ctx, cancel := context.WithCancel(context.Background())
				port := &cancelling{Port: g, cancel: cancel, n: n, after: after}
				_, err := (&gitimport.Importer{Graph: port, Dir: r.dir, Name: "demo", ProjectID: project}).Run(ctx)
				cancel()
				// a cancel at the last write comes once the work is done, or nearly: the memory store does not watch
				// the context, and the history may be read to its end before the cancel reaches git
				if !errors.Is(err, context.Canceled) && (err != nil || n < count.calls) {
					t.Fatalf("cancelled at write %d (after %v): %v", n, after, err)
				}
				changes, err := g.ListChanges(context.Background(), graph.ChangesFilter{Namespace: gitimport.Namespace})
				if err != nil {
					t.Fatal(err)
				}
				for _, c := range changes {
					if c.Status != domain.ChangeApplied && c.Status != domain.ChangeAbandoned {
						t.Fatalf("cancelled at write %d (after %v): change %q left %s", n, after, c.Title, c.Status)
					}
				}
				if _, err := (&gitimport.Importer{Graph: g, Dir: r.dir, Name: "demo", ProjectID: project}).Run(context.Background()); err != nil {
					t.Fatalf("resumed after a cancel at write %d (after %v): %v", n, after, err)
				}
				got, _ := graphTree(t, g, "demo")
				same(t, r.tree(), got)
			}
		}
	}
}
