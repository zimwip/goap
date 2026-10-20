package bench_test

import (
	"bytes"
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/gen/goap/graph/v1/graphv1connect"
	"github.com/zimwip/goap/contracts/gen/goap/registry/v1/registryv1connect"
	"github.com/zimwip/goap/graph/domainsvc"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graphsvc"
	"github.com/zimwip/goap/lib/access"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
	"github.com/zimwip/goap/platform/bench"
)

// server is a graph service with its domain registry in memory, wired as platform/cmd/graph wires them.
func server(t *testing.T) (*graph.Graph, string) {
	t.Helper()
	ctx := t.Context()
	enf, err := authz.NewCasbin(nil)
	if err != nil {
		t.Fatal(err)
	}
	domains := &domainsvc.Service{Store: domainsvc.NewMemoryStore(), Authz: enf}
	types := typecat.NewLive(domains.Domains)
	domains.Changed = func(ctx context.Context, _ def.Record) {
		if err := types.Reload(ctx); err != nil {
			t.Error(err)
		}
	}
	if err := types.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	g.Types = func() graph.TypeCatalog { return types.Get() }
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	h := &graphsvc.Handler{Projects: graphsvc.EveryProject, Authz: enf, Graph: g}
	mux.Handle(graphv1connect.NewGraphServiceHandler(h, connect.WithInterceptors(h.Identify())))
	mux.Handle(registryv1connect.NewDomainServiceHandler(&domainsvc.Handler{Service: domains}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func runner(t *testing.T, url string, cfg bench.Config) (*bench.Runner, *bytes.Buffer) {
	t.Helper()
	m, err := bench.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	var report bytes.Buffer
	w, err := bench.NewCSVWriter(&report)
	if err != nil {
		t.Fatal(err)
	}
	return &bench.Runner{Config: cfg, Metrics: m, CSV: w, Log: t.Output(),
		Clients:  bench.NewClients(url, url, authz.System("bench", access.RoleAdmin), "", m),
		Reviewer: bench.NewClients(url, url, authz.System("bench-reviewer", access.RoleAdmin), "", m)}, &report
}

// Both modes commit their changes through the API: the domain is published, nodes are created then modified (their
// versions follow), the reads are probed, and the report has one row per operation.
func TestRun(t *testing.T) {
	for _, mode := range []string{bench.ModeCommit, bench.ModeLifecycle} {
		t.Run(mode, func(t *testing.T) {
			g, url := server(t)
			r, report := runner(t, url, bench.Config{Run: "t", Domain: bench.DomainShape{Types: 3, Attributes: 2}, Workers: 2, Edits: 5,
				CreateRatio: 0.5, Links: 2, AddLinks: 0.5, HotNodes: 2, HotRatio: 0.5, TextBytes: 40, Mode: mode, MaxChanges: 12, ProbeEvery: 10 * time.Millisecond,
				ReportEvery: time.Hour, Seed: 1})
			if err := r.WaitReady(t.Context(), time.Second); err != nil {
				t.Fatal(err)
			}
			if err := r.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			tot := &r.Metrics.Totals
			if tot.Changes.Load() < 12 || tot.Errors.Load() != 0 || tot.DomainVersions.Load() != 1 || tot.DomainTypes.Load() != 3 {
				t.Fatalf("changes %d, errors %d, domain versions %d, types %d", tot.Changes.Load(), tot.Errors.Load(), tot.DomainVersions.Load(), tot.DomainTypes.Load())
			}
			head, err := g.BranchHead(t.Context(), bench.Namespace, domain.MainBranch)
			if err != nil {
				t.Fatal(err)
			}
			versions, links := 0, 0
			for id, v := range head.Nodes {
				vs, err := g.Versions(t.Context(), id)
				if err != nil {
					t.Fatal(err)
				}
				versions += len(vs)
				ls, err := g.OutLinksOf(t.Context(), domain.NodeRef{ID: id, Version: v})
				if err != nil {
					t.Fatal(err)
				}
				links += len(ls)
			}
			// the modifications add links too: a node gathers more than the ones it was created with
			if int64(links) != tot.Links.Load() || links <= int(tot.Nodes.Load()) {
				t.Fatalf("the graph holds %d links, the bench counted %d for %d nodes", links, tot.Links.Load(), tot.Nodes.Load())
			}
			if int64(len(head.Nodes)) != tot.Nodes.Load() || int64(versions) != tot.Versions.Load() {
				t.Fatalf("the graph holds %d nodes and %d versions, the bench counted %d and %d", len(head.Nodes), versions, tot.Nodes.Load(), tot.Versions.Load())
			}
			if versions <= len(head.Nodes) {
				t.Fatalf("no node was modified: %d versions of %d nodes", versions, len(head.Nodes))
			}
			rows, err := csv.NewReader(report).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			ops := map[string]bool{}
			for _, row := range rows[1:] {
				ops[row[6]] = true
			}
			want := []string{"ImportDomain", "GetBranch", "ListNodeVersions"}
			if mode == bench.ModeCommit {
				want = append(want, "CommitEdits")
			} else {
				want = append(want, "CreateChange", "ImpactNodeCreate", "ImpactNodeCheckout", "ImpactLinkCreate", "ImpactNodeReviewBatch", "CommitChange", "MergeChange")
			}
			for _, op := range want {
				if !ops[op] {
					t.Errorf("no row of %s in the report: %v", op, ops)
				}
			}
		})
	}
}

// A second run publishes the next version of the domain, one type more, and the domain axis grows during a run.
func TestDomainGrows(t *testing.T) {
	_, url := server(t)
	cfg := bench.Config{Run: "a", Domain: bench.DomainShape{Types: 2}, Workers: 1, Edits: 4, CreateRatio: 1, MaxChanges: 1, ReportEvery: time.Hour}
	r, _ := runner(t, url, cfg)
	if err := r.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	cfg.Run, cfg.MaxChanges, cfg.DomainEvery = "b", 0, 4
	cfg.TargetVersions = 12
	r2, _ := runner(t, url, cfg)
	if err := r2.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	// 1.0.0 by the first run, 1.0.1 at the start of the second, then one more per 4 versions as long as it lasts
	if n := r2.Metrics.Totals.DomainVersions.Load(); n < 2 {
		t.Fatalf("domain versions %d", n)
	}
	if !strings.Contains(bench.DomainYAML(cfg.Domain, 3), "T004") {
		t.Fatal("step 3 holds 2+3 types")
	}
}
