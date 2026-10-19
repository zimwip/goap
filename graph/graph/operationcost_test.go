package graph

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/platform"
)

// TestOperationCost measures the fixed cost of each operation on a change over a graph of a given size (ADR 0140). It runs only
// when GOAP_SCALE_NODES is set: against PostgreSQL in a fresh schema when GOAP_TEST_PG_DSN is set (with the WAL each
// operation writes), else on a temporary SQLite database.
//
//	GOAP_SCALE_NODES     nodes seeded on main before measuring (required)
//	GOAP_SCALE_BATCH     nodes per seeding commit (default 1000)
//	GOAP_SCALE_VERSIONS  more versions per node after the seeding (default 0)
//	GOAP_SCALE_EDITS     nodes each measured change touches (default 10)
//	GOAP_SCALE_ROUNDS    measured changes of each kind, update and create (default 10)
//	GOAP_SCALE_SQL       on PostgreSQL, list the statements of each operation (any value)
//
// Example: GOAP_SCALE_NODES=10000 go test ./graph/graph/ -run TestOperationCost -v -count=1 -timeout 0
func TestOperationCost(t *testing.T) {
	n := envInt("GOAP_SCALE_NODES", 0)
	if n == 0 {
		t.Skip("GOAP_SCALE_NODES not set")
	}
	seedBatch := envInt("GOAP_SCALE_BATCH", 1000)
	extra := envInt("GOAP_SCALE_VERSIONS", 0)
	edits := envInt("GOAP_SCALE_EDITS", 10)
	rounds := envInt("GOAP_SCALE_ROUNDS", 10)
	ctx := context.Background()

	repo, wal, size, queries := costRepo(t)
	g := New(repo)
	g.CheckpointEvery = DefaultCheckpointEvery

	keys := make([]string, 0, n)
	start := time.Now()
	for len(keys) < n {
		var e []NodeEdit
		for i := 0; i < seedBatch && len(keys) < n; i++ {
			k := fmt.Sprintf("N-%d", len(keys))
			keys = append(keys, k)
			e = append(e, NodeEdit{Key: k, Type: "Need", Props: map[string]any{"title": k}})
		}
		if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "seed", By: "bench", Edits: e}); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("seed: %d nodes in batches of %d, %v (%.0f nodes/s)", n, seedBatch, time.Since(start).Round(time.Second), float64(n)/time.Since(start).Seconds())

	start = time.Now()
	for v := 0; v < extra; v++ {
		for b := 0; b < len(keys); b += seedBatch {
			var e []NodeEdit
			for _, k := range keys[b:min(b+seedBatch, len(keys))] {
				node, err := g.NodeByKey(ctx, domain.DefaultNamespace, k)
				if err != nil {
					t.Fatal(err)
				}
				ref := node.Ref()
				e = append(e, NodeEdit{Pre: &ref, Props: map[string]any{"title": fmt.Sprintf("%s v%d", k, v+2)}})
			}
			if _, err := g.Commit(ctx, Commit{ProjectID: "PROJ-ROOT", Title: "versions", By: "bench", Edits: e}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if extra > 0 {
		t.Logf("versions: %d more per node, %v (%.0f versions/s)", extra, time.Since(start).Round(time.Second), float64(extra*n)/time.Since(start).Seconds())
	}
	g.WaitCheckpoints()
	if head, err := g.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch); err == nil {
		w, t0 := wal(), time.Now()
		if err := g.Materialize(ctx, head.ID); err != nil {
			t.Fatal(err)
		}
		t.Logf("one checkpoint (head stored whole): %v, %d KB WAL", time.Since(t0).Round(time.Millisecond), (wal()-w)>>10)
	}
	t.Logf("database: %d MB", size()>>20)

	cold := New(repo) // no cache
	t0 := time.Now()
	head, err := cold.BranchHead(ctx, domain.DefaultNamespace, domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("head state read cold: %d nodes in %v", len(head.Nodes), time.Since(t0).Round(time.Millisecond))
	t0 = time.Now()
	for i := 0; i < 100; i++ {
		if _, err := cold.NodeByKey(ctx, domain.DefaultNamespace, keys[(i*7919)%len(keys)]); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("NodeByKey: %.2f ms", float64(time.Since(t0).Microseconds())/1000/100)

	type stat struct {
		n, q int
		d    time.Duration
		wal  int64
		sql  map[string]*sqlStat
	}
	var stats map[string]*stat
	var order []string
	op := func(name string, f func() error) {
		w, q0 := wal(), queries.start()
		t0 := time.Now()
		if err := f(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		d := time.Since(t0)
		n, by := queries.stop(q0)
		s := stats[name]
		if s == nil {
			s = &stat{sql: map[string]*sqlStat{}}
			stats[name] = s
			order = append(order, name)
		}
		s.n++
		s.q += n
		s.d += d
		s.wal += wal() - w
		for k, v := range by {
			if s.sql[k] == nil {
				s.sql[k] = &sqlStat{}
			}
			s.sql[k].n += v.n
			s.sql[k].d += v.d
		}
	}
	run := func(create bool, r int) {
		var c domain.Change
		op("CreateChange", func() (err error) {
			c, err = g.CreateChange(ctx, NewChange{Title: "c", OwnBranch: true, ProjectID: "PROJ-ROOT"})
			return err
		})
		var ids []domain.ChangeImpactID
		for i := 0; i < edits; i++ {
			if create {
				op("ImpactNodeCreate", func() error {
					cn, err := g.ImpactNodeCreate(ctx, c.ID, NodeCreate{Key: fmt.Sprintf("M-%d-%d", r, i), Type: "Need", Properties: map[string]any{"title": "x"}, Rationale: "c"})
					ids = append(ids, cn.ID)
					return err
				})
				continue
			}
			node, err := g.NodeByKey(ctx, domain.DefaultNamespace, keys[(r*edits+i)%len(keys)])
			if err != nil {
				t.Fatal(err)
			}
			ref := node.Ref()
			var id domain.ChangeImpactID
			op("ProposeImpact", func() error {
				added, err := g.ProposeImpact(ctx, c.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "c"}})
				if err == nil {
					id = added[0].ID
				}
				return err
			})
			op("ImpactNodeCheckout", func() error {
				_, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Impact: id})
				return err
			})
			op("ImpactNodeUpdate", func() error {
				_, err := g.ImpactNodeUpdate(ctx, c.ID, id, NodeUpdate{Properties: map[string]any{"title": fmt.Sprintf("r%d", r)}})
				return err
			})
			ids = append(ids, id)
		}
		batch := domain.ReviewBatch{By: "bench"}
		for _, id := range ids {
			op("ImpactModifies", func() error { _, err := g.ImpactModifies(ctx, c.ID, id); return err })
			batch.Verdicts = append(batch.Verdicts, domain.ImpactVerdict{Impact: id, Status: domain.ReviewAccepted, Comment: "c"})
		}
		op("ImpactNodeReviewBatch", func() error { _, err := g.ImpactNodeReviewBatch(ctx, c.ID, batch); return err })
		op("CommitChange", func() error { _, err := g.commitChange(ctx, c.ID, ""); return err })
		op("IntegrateChange", func() error { _, err := g.IntegrateChange(ctx, c.ID, nil); return err })
	}
	for _, create := range []bool{false, true} {
		stats, order = map[string]*stat{}, nil
		for r := 0; r < rounds; r++ {
			run(create, r)
		}
		var total time.Duration
		var w int64
		t.Logf("== %s: %d nodes on main, %d nodes per change, %d changes", map[bool]string{false: "update", true: "create"}[create], n, edits, rounds)
		for _, k := range order {
			s := stats[k]
			total += s.d
			w += s.wal
			t.Logf("  %-22s calls/change %4.1f  %7.2f ms/call  %7.1f ms/change  %6.1f KB WAL/call  %5.1f statements/call", k, float64(s.n)/float64(rounds),
				float64(s.d.Microseconds())/1000/float64(s.n), float64(s.d.Microseconds())/1000/float64(rounds), float64(s.wal)/1024/float64(s.n),
				float64(s.q)/float64(s.n))
			if os.Getenv("GOAP_SCALE_SQL") != "" {
				keys := make([]string, 0, len(s.sql))
				for q := range s.sql {
					keys = append(keys, q)
				}
				sort.Slice(keys, func(i, j int) bool { return s.sql[keys[i]].d > s.sql[keys[j]].d })
				for _, q := range keys[:min(len(keys), 12)] {
					t.Logf("      %6.1f/call %7.2f ms/call  %s", float64(s.sql[q].n)/float64(s.n), float64(s.sql[q].d.Microseconds())/1000/float64(s.n), q)
				}
			}
		}
		t.Logf("  %-22s %7.1f ms/change, %6.1f KB WAL/change", "total", float64(total.Microseconds())/1000/float64(rounds), float64(w)/1024/float64(rounds))
	}
}

func envInt(name string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v > 0 {
		return v
	}
	return def
}

// costRepo is the repository of TestOperationCost, with the WAL position (0 on SQLite) and the size of the database.
// sqlStat is how often a statement ran and how long it took.
type sqlStat struct {
	n int
	d time.Duration
}

// queryCounter counts the statements a pool runs (a pgx tracer): the cost of an operation in round trips.
type queryCounter struct {
	mu  sync.Mutex
	n   int
	by  map[string]*sqlStat
	key struct{}
}

func (q *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, &q.key, [2]any{data.SQL, time.Now()})
}

func (q *queryCounter) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryEndData) {
	v, ok := ctx.Value(&q.key).([2]any)
	if !ok {
		return
	}
	sql := strings.Join(strings.Fields(v[0].(string)), " ")
	if len(sql) > 110 {
		sql = sql[:110]
	}
	if os.Getenv("GOAP_SCALE_SQL") == "callers" {
		sql = callerOf() + " " + sql[:min(len(sql), 50)]
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.n++
	if q.by == nil {
		q.by = map[string]*sqlStat{}
	}
	s := q.by[sql]
	if s == nil {
		s = &sqlStat{}
		q.by[sql] = s
	}
	s.n++
	s.d += time.Since(v[1].(time.Time))
}

// callerOf names the functions of the graph that ran a statement: the first three frames outside the repository.
func callerOf() string {
	pcs := make([]uintptr, 40)
	frames := runtime.CallersFrames(pcs[:runtime.Callers(3, pcs)])
	var out []string
	for f, more := frames.Next(); more && len(out) < 3; f, more = frames.Next() {
		if !strings.Contains(f.Function, "goap/graph/graph.") || strings.Contains(f.File, "postgres.go") ||
			strings.Contains(f.File, "guard.go") || strings.Contains(f.File, "observe") || strings.Contains(f.File, "_test.go") {
			continue
		}
		out = append(out, fmt.Sprintf("%s:%d", f.Function[strings.LastIndex(f.Function, ".")+1:], f.Line))
	}
	return strings.Join(out, "<")
}

// start begins counting the statements of an operation; stop returns how many ran since and which.
func (q *queryCounter) start() int {
	if q == nil {
		return 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.by = nil
	return q.n
}

func (q *queryCounter) stop(from int) (int, map[string]*sqlStat) {
	if q == nil {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.n - from, q.by
}

func costRepo(t *testing.T) (Repo, func() int64, func() int64, *queryCounter) {
	ctx := context.Background()
	dsn := os.Getenv("GOAP_TEST_PG_DSN")
	if dsn == "" {
		path := filepath.Join(t.TempDir(), "goap.db")
		db, err := platform.OpenSQLite(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := platform.MigrateSQLite(ctx, db, "graph", SQLiteMigrations, "migrations_sqlite"); err != nil {
			t.Fatal(err)
		}
		size := func() int64 {
			fi, err := os.Stat(path)
			if err != nil {
				return 0
			}
			return fi.Size()
		}
		return NewSQLite(db), func() int64 { return 0 }, size, nil
	}
	schema := fmt.Sprintf("graph_cost_%d", time.Now().UnixNano())
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }) //nolint:errcheck
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	queries := &queryCounter{}
	cfg.ConnConfig.Tracer = queries
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := platform.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), pool, Migrations, "migrations"); err != nil {
		t.Fatal(err)
	}
	wal := func() int64 {
		var l int64
		_ = admin.QueryRow(ctx, "SELECT pg_wal_lsn_diff(pg_current_wal_lsn(), '0/0')::bigint").Scan(&l)
		return l
	}
	size := func() int64 {
		var s int64
		_ = admin.QueryRow(ctx, "SELECT coalesce(sum(pg_total_relation_size(c.oid)), 0) FROM pg_class c JOIN pg_namespace s ON s.oid = c.relnamespace WHERE s.nspname = $1 AND c.relkind = 'r'", schema).Scan(&s)
		return s
	}
	return NewPostgres(pool), wal, size, queries
}
