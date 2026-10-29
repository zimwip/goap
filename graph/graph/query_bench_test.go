package graph

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/platform"
)

// stmtCounter counts the statements a pool sends, by the first words of their text.
type stmtCounter struct {
	n  atomic.Int64
	mu sync.Mutex
	by map[string]int
	on atomic.Bool
}

func (c *stmtCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	if !c.on.Load() {
		return ctx
	}
	c.n.Add(1)
	k := strings.Join(strings.Fields(d.SQL), " ")
	if len(k) > 90 {
		k = k[:90]
	}
	c.mu.Lock()
	c.by[k]++
	c.mu.Unlock()
	return ctx
}

func (c *stmtCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// queryFixture commits a tree of 1111 items under R (fanout 10, depth 3), each inner item linking 20 pool nodes by
// `ref`, and 50 pool nodes watching each item of the first two levels (`watch`): the links a navigation along `child`
// should not read, leaving and entering.
func queryFixture(t testing.TB, g *Graph) {
	t.Helper()
	ctx := context.Background()
	const pool = 50
	var edits []NodeEdit
	var watched []string
	var walk func(key string, level int)
	walk = func(key string, level int) {
		e := NodeEdit{Key: key, Type: "Item"}
		if level < 3 {
			if level < 2 {
				watched = append(watched, key)
			}
			for i := range 10 {
				child := fmt.Sprintf("%s.%d", key, i)
				e.Links = append(e.Links, LinkEdit{Type: "child", ToKey: child})
				walk(child, level+1)
			}
			for i := range 20 {
				e.Links = append(e.Links, LinkEdit{Type: "ref", ToKey: fmt.Sprintf("P%d", i%pool)})
			}
		}
		edits = append(edits, e)
	}
	walk("R", 0)
	for i := range pool {
		e := NodeEdit{Key: fmt.Sprintf("P%d", i), Type: "Pool"}
		for _, w := range watched {
			e.Links = append(e.Links, LinkEdit{Type: "watch", ToKey: w})
		}
		edits = append(edits, e)
	}
	if _, err := g.CommitEdits(ctx, Edits{Title: "fixture", By: "tester", ProjectID: "PROJ-ROOT", Edits: edits}); err != nil {
		t.Fatal(err)
	}
}

var benchQueries = []struct {
	name, q string
	rows    int
	first   any
}{
	{"down3", `MATCH (r:Item {key: 'R'})-[:child]->(a)-[:child]->(b)-[:child]->(c) RETURN count(c)`, 1, int64(1000)},
	{"down-var2", `MATCH (r:Item {key: 'R'})-[:child*1..2]->(x) RETURN count(x)`, 1, int64(110)},
	{"up-var3", `MATCH (c:Item {key: 'R.1.2.3'})<-[:child*1..3]-(a) RETURN count(a)`, 1, int64(3)},
	{"hub-in", `MATCH (p:Pool {key: 'P1'})<-[:ref]-(a) RETURN count(a)`, 1, int64(111)},
	{"label-where", `MATCH (a:Item)-[:child]->(b) WHERE a.key STARTS WITH 'R.1' RETURN count(b)`, 1, int64(110)},
	{"exists", `MATCH (a:Item {key: 'R.1'})-[:child]->(b) WHERE EXISTS { (b)-[:child]->(:Item) } RETURN count(b)`, 1, int64(10)},
}

func benchRepos(b *testing.B) map[string]func(b *testing.B) (Repo, *stmtCounter) {
	out := map[string]func(b *testing.B) (Repo, *stmtCounter){
		"sqlite": func(b *testing.B) (Repo, *stmtCounter) {
			ctx := context.Background()
			db, err := platform.OpenSQLite(ctx, filepath.Join(b.TempDir(), "goap.db"))
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { db.Close() })
			if err := platform.MigrateSQLite(ctx, db, "graph", SQLiteMigrations, "migrations_sqlite"); err != nil {
				b.Fatal(err)
			}
			return NewSQLite(db), nil
		},
	}
	dsn := os.Getenv("GOAP_TEST_PG_DSN")
	if dsn == "" {
		return out
	}
	out["postgres"] = func(b *testing.B) (Repo, *stmtCounter) {
		ctx := context.Background()
		schema := fmt.Sprintf("graph_bench_%d", time.Now().UnixNano())
		admin, err := pgxpool.New(ctx, dsn)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
			b.Fatal(err)
		}
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			b.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema
		c := &stmtCounter{by: map[string]int{}}
		cfg.ConnConfig.Tracer = c
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() {
			pool.Close()
			_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
			admin.Close()
		})
		if err := platform.Migrate(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), pool, Migrations, "migrations"); err != nil {
			b.Fatal(err)
		}
		return NewPostgres(pool), c
	}
	return out
}

// BenchmarkQueryView measures the read queries of ADR 0160 over queryFixture, in a branch view and in the view of a
// change holding drafts of the first level, with the statements each sends on PostgreSQL (stmts/op).
func BenchmarkQueryView(b *testing.B) {
	ctx := context.Background()
	for name, open := range benchRepos(b) {
		b.Run(name, func(b *testing.B) {
			repo, counter := open(b)
			g := New(repo)
			queryFixture(b, g)
			head, err := g.BranchHead(ctx, "", domain.MainBranch)
			if err != nil {
				b.Fatal(err)
			}
			c, err := g.CreateChange(ctx, NewChange{ProjectID: "PROJ-ROOT", Title: "bench", ForkCommit: head.ID, OwnBranch: true})
			if err != nil {
				b.Fatal(err)
			}
			for i := range 10 {
				key := fmt.Sprintf("R.%d", i)
				imp, err := g.ImpactNodeCheckout(ctx, c.ID, NodeCheckout{Key: key, Rationale: "bench"})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := g.edit(ctx, c.ID, imp.ID, edit{Properties: map[string]any{"title": "edited " + key}}); err != nil {
					b.Fatal(err)
				}
			}
			views := []struct {
				name string
				v    View
			}{{"branch", View{Namespace: head.Namespace, Branch: domain.MainBranch}}, {"change", View{Change: c.ID}}}
			for _, vw := range views {
				for _, q := range benchQueries {
					b.Run(vw.name+"/"+q.name, func(b *testing.B) {
						check := func(res QueryResult) {
							if len(res.Rows) != q.rows || res.Rows[0][0] != q.first {
								b.Fatalf("%s: %v (truncated %v, %v)", q.name, res.Rows, res.Truncated, res.Notes)
							}
						}
						res, err := g.QueryView(ctx, vw.v, q.q, QueryOptions{})
						if err != nil {
							b.Fatal(err)
						}
						check(res)
						if counter != nil {
							counter.by = map[string]int{}
							counter.n.Store(0)
							counter.on.Store(true)
						}
						var reads int
						for b.Loop() {
							res, err := g.QueryView(ctx, vw.v, q.q, QueryOptions{})
							if err != nil {
								b.Fatal(err)
							}
							reads = res.Reads
						}
						b.ReportMetric(float64(reads), "reads/op")
						if counter != nil {
							counter.on.Store(false)
							b.ReportMetric(float64(counter.n.Load())/float64(b.N), "stmts/op")
							if os.Getenv("GOAP_BENCH_STMTS") != "" {
								type kv struct {
									k string
									n int
								}
								var list []kv
								for k, n := range counter.by {
									list = append(list, kv{k, n})
								}
								sort.Slice(list, func(i, j int) bool { return list[i].n > list[j].n })
								for i, e := range list {
									if i == 12 {
										break
									}
									b.Logf("%8.1f  %s", float64(e.n)/float64(b.N), e.k)
								}
							}
						}
					})
				}
			}
		})
	}
}

// benchWatchers is the number of `watch` links into the hub of BenchmarkQueryEntering each of its 30 rounds adds.
var benchWatchers = 100

// BenchmarkQueryEntering measures a hop against the links into a node many links of another type enter (3000 `watch`
// links, 20 `child` ones, over 30 versions of the node), with the index of the links by target and type
// (link_to_type: to_id, type, from_id) and without it (link_to only: to_id, to_version, type).
func BenchmarkQueryEntering(b *testing.B) {
	ctx := context.Background()
	if n, err := strconv.Atoi(os.Getenv("GOAP_BENCH_WATCHERS")); err == nil {
		benchWatchers = n
	}
	for name, open := range benchRepos(b) {
		b.Run(name, func(b *testing.B) {
			repo, counter := open(b)
			g := New(repo)
			if _, err := g.CommitEdits(ctx, Edits{Title: "hub", By: "tester", ProjectID: "PROJ-ROOT", Edits: []NodeEdit{{Key: "H", Type: "Item"}}}); err != nil {
				b.Fatal(err)
			}
			for round := range 30 {
				h, err := g.NodeByKey(ctx, "", "H")
				if err != nil {
					b.Fatal(err)
				}
				pre := h.Ref()
				edits := []NodeEdit{{Pre: &pre, Props: map[string]any{"title": fmt.Sprint("round ", round)}}}
				for i := range benchWatchers {
					edits = append(edits, NodeEdit{Key: fmt.Sprintf("W%d.%d", round, i), Type: "Item", Links: []LinkEdit{{Type: "watch", ToKey: "H"}}})
				}
				if round%3 == 0 {
					for i := range 2 {
						edits = append(edits, NodeEdit{Key: fmt.Sprintf("C%d.%d", round, i), Type: "Item", Links: []LinkEdit{{Type: "child", ToKey: "H"}}})
					}
				}
				if _, err := g.CommitEdits(ctx, Edits{Title: "round", By: "tester", ProjectID: "PROJ-ROOT", Edits: edits}); err != nil {
					b.Fatal(err)
				}
			}
			exec := func(q string) {
				var err error
				if pg, ok := repo.(*Postgres); ok {
					_, err = pg.pool.Exec(ctx, q)
				} else {
					_, err = repo.(*SQLite).db.ExecContext(ctx, q)
				}
				if err != nil {
					b.Fatal(err)
				}
			}
			main := View{Namespace: domain.DefaultNamespace, Branch: domain.MainBranch}
			for _, ix := range []struct{ name, ddl string }{
				{"index-to-type", ""},
				{"index-to", "DROP INDEX link_to_type"},
			} {
				if ix.ddl != "" {
					exec(ix.ddl)
				}
				exec("ANALYZE")
				for _, q := range []struct{ name, q, want string }{
					{"child", `MATCH (h {key: 'H'})<-[:child]-(x) RETURN count(x)`, "20"},
					{"watch", `MATCH (h {key: 'H'})<-[:watch]-(x) RETURN count(x)`, fmt.Sprint(min(QueryMaxRows, 30*benchWatchers))},
					{"watch-limit", `MATCH (h {key: 'H'})<-[:watch]-(x) RETURN x.key LIMIT 10`, ""},
				} {
					b.Run(ix.name+"/"+q.name, func(b *testing.B) {
						if counter != nil {
							counter.n.Store(0)
							counter.on.Store(true)
						}
						for b.Loop() {
							res, err := g.QueryView(ctx, main, q.q, QueryOptions{})
							if err != nil || (q.want != "" && fmt.Sprint(res.Rows[0][0]) != q.want) {
								b.Fatal(res.Rows, err)
							}
						}
						if counter != nil {
							counter.on.Store(false)
							b.ReportMetric(float64(counter.n.Load())/float64(b.N), "stmts/op")
						}
					})
				}
			}
		})
	}
}
