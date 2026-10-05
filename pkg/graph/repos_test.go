package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/domain"
)

// testChange opens a change on the current head of a namespace's main branch (the empty state if none
// exists yet), for a test that needs a domain.ChangeID to attribute a raw g.Link call to (ADR 0049) without
// disturbing whatever real content is already on main.
func testChange(t *testing.T, g *Graph, namespace string) domain.ChangeID {
	t.Helper()
	ctx := context.Background()
	head, err := g.BranchHead(ctx, namespace, domain.MainBranch)
	if errors.Is(err, ErrNotFound) {
		head, err = g.BranchHead(ctx, namespace, domain.MainBranch)
	}
	if err != nil {
		t.Fatal(err)
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: namespace, BaselineID: head.ID})
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// seedNode writes a node version directly, the way CreateNode did before ADR 0049 made it go through Commit:
// test fixtures use it to arrange a world already sitting in a given lifecycle state (including an editable
// one, or one not reachable from the lifecycle's initial state by a single transition) without walking every
// transition to get there — something no real caller needs, since every production write is change-shaped.
func seedNode(ctx context.Context, g *Graph, in NewNode) (domain.Node, error) {
	n := domain.Node{ID: domain.NodeID(g.newID()), Version: 1, Branch: domain.MainBranch, Reason: domain.ReasonCreate,
		Namespace: domain.NamespaceOf(in.Namespace), Key: in.Key, Type: in.Type, Properties: in.Properties, CreatedAt: g.now(), State: in.State}
	if n.Key == "" {
		n.Key = string(n.ID)
	}
	// even a fixture writes inside a change (ADR 0054): one opened on the current head, left open
	head, err := g.BranchHead(ctx, n.Namespace, domain.MainBranch)
	if errors.Is(err, ErrNotFound) {
		head, err = g.BranchHead(ctx, n.Namespace, domain.MainBranch)
	}
	if err != nil {
		return n, err
	}
	c, err := g.CreateChange(ctx, NewChange{Namespace: n.Namespace, Title: "seed " + n.Key, BaselineID: head.ID})
	if err != nil {
		return n, err
	}
	n.ChangeID = c.ID
	err = g.repo.InTx(ctx, func(tx Tx) error { return tx.PutNode(ctx, n) })
	if err == nil {
		n, err = g.Node(ctx, n.Ref())
	}
	return n, err
}

// forEachRepo runs f against the in-memory repository and, when
// GOAP_TEST_PG_DSN is set, against PostgreSQL in a fresh schema.
func forEachRepo(t *testing.T, f func(t *testing.T, repo Repo)) {
	t.Run("memory", func(t *testing.T) {
		repo := NewMemory()
		f(t, repo)
		checkImpactLogs(t, repo)
		checkLandings(t, repo)
		checkStates(t, repo)
	})
	t.Run("sqlite", func(t *testing.T) {
		ctx := context.Background()
		db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := platform.MigrateSQLite(ctx, db, "graph", SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		repo := NewSQLite(db)
		f(t, repo)
		checkImpactLogs(t, repo)
		checkLandings(t, repo)
		checkStates(t, repo)
	})
	dsn := os.Getenv("GOAP_TEST_PG_DSN")
	if dsn == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) {
		ctx := context.Background()
		schema := fmt.Sprintf("graph_test_%d", time.Now().UnixNano())
		admin, err := pgxpool.New(ctx, dsn)
		if err != nil {
			t.Fatal(err)
		}
		defer admin.Close()
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			t.Fatal(err)
		}
		defer admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") //nolint:errcheck
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		if err := platform.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), pool, Migrations, "migrations"); err != nil {
			t.Fatal(err)
		}
		repo := NewPostgres(pool)
		f(t, repo)
		checkImpactLogs(t, repo)
		checkLandings(t, repo)
		checkStates(t, repo)
	})
}

// checkImpactLogs replays the impact log of every change and compares it with the change_impact projection
// (ADR 0029): nothing may write the projection without an event.
func checkImpactLogs(t *testing.T, repo Repo) {
	t.Helper()
	if t.Failed() {
		return
	}
	norm := func(list []domain.ChangeImpact) string {
		out := make([]domain.ChangeImpact, len(list))
		for i, cn := range list {
			cn.CreatedAt = cn.CreatedAt.UTC().Truncate(time.Millisecond)
			cn.Reviews = slices.Clone(cn.Reviews)
			for j := range cn.Reviews {
				cn.Reviews[j].At = cn.Reviews[j].At.UTC().Truncate(time.Millisecond)
			}
			if len(cn.Reviews) == 0 {
				cn.Reviews = nil
			}
			if len(cn.DerivedFrom) == 0 {
				cn.DerivedFrom = nil
			}
			if len(cn.Items) == 0 {
				cn.Items = nil
			}
			out[i] = cn
		}
		b, _ := json.MarshalIndent(out, "", " ")
		return string(b)
	}
	err := repo.InTx(context.Background(), func(tx Tx) error {
		cs, err := tx.Changes(context.Background())
		if err != nil {
			return err
		}
		for _, c := range cs {
			stored, err := tx.ChangeImpacts(context.Background(), c.ID)
			if err != nil {
				return err
			}
			events, err := impactEvents(context.Background(), tx, c.ID)
			if err != nil {
				return err
			}
			if got, want := norm(domain.FoldImpacts(events)), norm(stored); got != want {
				t.Errorf("change %s: the replay of its %d events differs from the projection\nreplay: %s\nstored: %s", c.ID, len(events), got, want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// pinBaseline makes the head of main hold exactly the given versions on top of what it holds: the fixtures that
// arrange a world with seedNode (versions written outside the merge of a change) need them part of the state a
// change starts from. It is written through an applied change like any other state (ADR 0056); production code has
// no such operation, a state is only ever what a change left.
func pinBaseline(t *testing.T, g *Graph, namespace string, refs ...domain.NodeRef) domain.Baseline {
	t.Helper()
	ctx := context.Background()
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	namespace = domain.NamespaceOf(namespace)
	var out domain.Baseline
	err := g.repo.InTx(ctx, func(tx Tx) error {
		head, err := branchHead(ctx, tx, namespace, domain.MainBranch)
		if err != nil {
			return err
		}
		nodes := map[domain.NodeID]domain.Version{}
		for id, v := range head.Nodes {
			nodes[id] = v
		}
		for _, r := range refs {
			n, err := tx.Node(ctx, r)
			if err != nil {
				return err
			}
			if n.Namespace != namespace {
				return fmt.Errorf("node %s is of namespace %s, not %s: %w", n.Key, n.Namespace, namespace, ErrInvalid)
			}
			if !n.Deleted {
				nodes[n.ID] = n.Version
			}
		}
		org, proj := g.Structure(domain.StructureOrganisation), g.Structure(domain.StructureProject)
		c := domain.Change{ID: domain.ChangeID(g.newID()), Title: "Pin", Namespace: namespace, Status: domain.ChangeApplied, Intent: "Pin node versions for a fixture",
			BaselineID: head.ID, Branch: domain.MainBranch, OwnerOrg: org.Root, ProjectID: proj.Root, CreatedAt: g.now()}
		if err := tx.PutChange(ctx, c); err != nil {
			return err
		}
		out = domain.Baseline{ID: domain.BaselineID(g.newID()), Name: "Pin", Namespace: namespace, Branch: domain.MainBranch, ParentID: head.ID, ChangeID: c.ID, Nodes: nodes, CreatedAt: g.now()}
		if err := tx.PutBaseline(ctx, out); err != nil {
			return err
		}
		if err := g.advanceBranch(ctx, tx, namespace, domain.MainBranch, out.ID); err != nil {
			return err
		}
		c.ResultBaselineID = out.ID
		return tx.PutChange(ctx, c)
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
