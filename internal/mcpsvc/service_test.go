package mcpsvc_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/algo"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"
	"github.com/zimwip/goap/pkg/methodology"

	"github.com/jackc/pgx/v5/stdlib"
)

type fakeInvoker struct {
	last *connectorv1.InvokeRequest
	resp *connectorv1.InvokeResponse
	err  error
}

func (f *fakeInvoker) Invoke(_ context.Context, _ string, r *connectorv1.InvokeRequest) (*connectorv1.InvokeResponse, error) {
	f.last = r
	return f.resp, f.err
}

func localfsInfo() *connectorv1.ConnectorInfo {
	return &connectorv1.ConnectorInfo{Id: "localfs", Version: "1", SecretNames: []string{"token"}, Operations: []*connectorv1.Operation{
		{Name: "list_dir"}, {Name: "read_file"}, {Name: "write_file"}}}
}

// library serves the algorithms of the platform domain of the repository, plus the ones a test adds.
type library struct{ algos map[string]algo.Algorithm }

func (l library) Algorithm(_ context.Context, domain, _, name string) (algo.Algorithm, string, error) {
	if a, ok := l.algos[domain+"/"+name]; ok {
		return a, "1.0.0", nil
	}
	return algo.Algorithm{}, "", errors.New("not in the library")
}

func platformLibrary(t *testing.T) library {
	t.Helper()
	raw, err := os.ReadFile("../../domains/platform.yaml")
	if err != nil {
		t.Fatal(err)
	}
	d, err := methodology.ParseDomain(raw)
	if err != nil {
		t.Fatal(err)
	}
	if issues := d.Validate(); len(issues) > 0 {
		t.Fatalf("platform domain: %v", issues)
	}
	l := library{algos: map[string]algo.Algorithm{}}
	for _, a := range d.Algorithms {
		l.algos["platform/"+a.Name] = a
	}
	return l
}

// world is a graph with the default organisation and the document-repository MCP, and two trees:
//
//	ORG-DEFAULT (implicit root of every chain)   adapter: localfs, root /default
//	ORG-A      part_of ORG-DEFAULT               adapter: localfs, root /a   (overrides)
//	ORG-A1     part_of ORG-A                     (nothing: inherits ORG-A's)
//	ORG-B      no parent                         (nothing: inherits the default organisation's)
func world(t *testing.T) *graph.Graph {
	t.Helper()
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	if seeded, err := graphsvc.SeedDefaults(ctx, g); err != nil || !seeded {
		t.Fatalf("seed defaults = %v, %v", seeded, err)
	}
	if seeded, err := graphsvc.SeedDefaults(ctx, g); err != nil || seeded {
		t.Fatalf("seeding twice = %v, %v", seeded, err)
	}
	for _, u := range [][2]string{{"ORG-A", domain.DefaultOrg}, {"ORG-A1", "ORG-A"}, {"ORG-B", ""}} {
		if err := graphsvc.SeedUnit(ctx, g, u[0], u[0], "team", u[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := graphsvc.SeedAdapter(ctx, g, graphsvc.LocalFSAdapter(domain.DefaultOrg, "/default")); err != nil {
		t.Fatal(err)
	}
	if err := graphsvc.SeedAdapter(ctx, g, graphsvc.LocalFSAdapter("ORG-A", "/a")); err != nil {
		t.Fatal(err)
	}
	return g
}

func TestConnectorRegistryMemory(t *testing.T) { testRegistry(t, mcpsvc.NewMemoryStore()) }

func TestConnectorRegistrySQLite(t *testing.T) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "mcp", mcpsvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	testRegistry(t, mcpsvc.SQLStore{DB: db})
}

func TestConnectorRegistryPG(t *testing.T) {
	testRegistry(t, mcpsvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, mcpsvc.Migrations)), Dollar: true})
}

func testRegistry(t *testing.T, st mcpsvc.Store) {
	ctx := context.Background()
	now := time.Now().UTC()
	svc := &mcpsvc.Service{Store: st, Now: func() time.Time { return now }, Lease: time.Minute}
	if _, err := st.Connector(ctx, "localfs"); !errors.Is(err, mcpsvc.ErrNotFound) {
		t.Fatalf("unknown connector = %v", err)
	}
	if _, err := svc.RegisterConnector(ctx, &connectorv1.ConnectorInfo{Id: "Bad Id"}, "http://x"); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("bad id = %v", err)
	}
	if _, err := svc.RegisterConnector(ctx, localfsInfo(), "http://localfs:8080"); err != nil {
		t.Fatal(err)
	}
	cs, err := svc.Connectors(ctx)
	if err != nil || len(cs) != 1 || !cs[0].Live || cs[0].Endpoint != "http://localfs:8080" || len(cs[0].Info.Operations) != 3 {
		t.Fatalf("connectors = %+v, %v", cs, err)
	}
	now = now.Add(2 * time.Minute)
	if cs, _ = svc.Connectors(ctx); cs[0].Live {
		t.Fatal("expired registration is live")
	}
	if _, err := svc.RegisterConnector(ctx, localfsInfo(), "http://other:8080"); err != nil {
		t.Fatal(err)
	}
	if cs, _ = svc.Connectors(ctx); !cs[0].Live || cs[0].Endpoint != "http://other:8080" {
		t.Fatalf("renewed registration = %+v", cs[0])
	}
}

func newHub(t *testing.T, g *graph.Graph, inv mcpsvc.Invoker, extra ...algo.Algorithm) *mcpsvc.Service {
	t.Helper()
	lib := platformLibrary(t)
	for _, a := range extra {
		lib.algos["platform/"+a.Name] = a
	}
	svc := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: g}, Library: lib, Invoker: inv,
		Secrets: func(_ context.Context, ref string) (string, error) { return "secret:" + ref, nil }}
	if _, err := svc.RegisterConnector(context.Background(), localfsInfo(), "http://localfs:8080"); err != nil {
		t.Fatal(err)
	}
	return svc
}

// The same adapter of the library and the same connector, instantiated by each unit with its own
// parameter values; the nearest unit's instance wins, and every unit falls back on the default organisation.
func TestNearestAdapterWins(t *testing.T) {
	ctx := context.Background()
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "hi"})}}
	svc := newHub(t, world(t), inv)
	args := map[string]any{"path": "n.txt"}
	for unit, wantRoot := range map[string]string{
		"ORG-A":           "/a",       // its own instance
		"ORG-A1":          "/a",       // inherited from ORG-A, not from the default organisation
		"ORG-B":           "/default", // no ancestor: the default organisation
		"":                "/default", // a change naming no unit
		"ORG-UNKNOWN":     "/default", // a unit the graph does not know
		domain.DefaultOrg: "/default",
	} {
		out, err := svc.Call(ctx, unit, "document-repository/read", args)
		if err != nil || out["text"] != "hi" {
			t.Fatalf("%q: %v %v", unit, out, err)
		}
		if got := pbconv.Map(inv.last.Config)["root"]; got != wantRoot {
			t.Errorf("%q: root %v, want %s", unit, got, wantRoot)
		}
		if inv.last.Operation != "read_file" || pbconv.Map(inv.last.Arguments)["path"] != "n.txt" {
			t.Errorf("%q: call = %v", unit, inv.last)
		}
	}

	chain, eff, err := svc.Effective(ctx, "ORG-A1")
	if err != nil || len(chain) != 3 || chain[0] != "ORG-A1" || chain[1] != "ORG-A" || chain[2] != domain.DefaultOrg {
		t.Fatalf("chain = %v, %v", chain, err)
	}
	if len(eff) != 1 || eff[0].Adapter.Unit != "ORG-A" || !eff[0].Inherited || svc.ConnectorOf(ctx, eff[0].Adapter) != "localfs" {
		t.Fatalf("effective = %+v", eff)
	}
	if _, eff, _ = svc.Effective(ctx, "ORG-A"); eff[0].Inherited {
		t.Fatal("an own adapter is reported as inherited")
	}

	tools, mcps, err := svc.Tools(ctx, "ORG-B")
	if err != nil || len(tools) != 3 || len(mcps) != 1 || tools[0].Name != "document-repository/list" {
		t.Fatalf("tools = %v %v %v", tools, mcps, err)
	}
}

// The adapter is code: it maps the tools the MCP expects onto the operations the connector exposes.
func TestTheAdapterCodeMapsTheMCPOntoTheConnector(t *testing.T) {
	ctx := context.Background()
	g := world(t)
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "raw"})}}
	custom := algo.Algorithm{Name: "shouting-docs", Type: algo.UsageAdapter, Language: algo.JavaScript, MCP: "document-repository", Connector: "localfs",
		Params: []algo.Param{{Name: "root", Type: algo.ParamString, Required: true}, {Name: "prefix", Type: algo.ParamString, Default: ">"}},
		Code: `
			if (ctx.tool() === "read") {
				const r = ctx.call("read_file", { path: ctx.param("prefix") + ctx.args().path });
				return { text: r.text.toUpperCase() };
			}
			if (ctx.tool() === "list") {
				// several calls, reshaped result
				const a = ctx.call("list_dir", { path: "" });
				const b = ctx.call("list_dir", { path: "docs" });
				return { calls: ctx.operations().length };
			}
			ctx.fail("write is not allowed here: " + ctx.tool());`}
	svc := newHub(t, g, inv, custom)
	b := mcp.Adapter{Unit: "ORG-B", MCP: "document-repository", Domain: "platform", Algorithm: "shouting-docs", Params: map[string]any{"root": "/b"}}
	if err := graphsvc.SeedAdapter(ctx, g, b); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Call(ctx, "ORG-B", "document-repository/read", map[string]any{"path": "f.txt"})
	if err != nil || out["text"] != "RAW" {
		t.Fatalf("read = %v, %v", out, err)
	}
	if got := pbconv.Map(inv.last.Arguments)["path"]; got != ">f.txt" { // the default of the parameter
		t.Fatalf("connector path = %v", got)
	}
	if out, err = svc.Call(ctx, "ORG-B", "document-repository/list", nil); err != nil || out["calls"] == nil {
		t.Fatalf("list = %v, %v", out, err)
	}
	var te *mcpsvc.ToolError
	if _, err = svc.Call(ctx, "ORG-B", "document-repository/write", map[string]any{"path": "x", "content": "y"}); !errors.As(err, &te) || !strings.Contains(te.Msg, "not allowed") {
		t.Fatalf("rejected by the code = %v", err)
	}
	// units without their own instance keep using the library adapter of the default organisation
	if _, err = svc.Call(ctx, "ORG-A", "document-repository/read", map[string]any{"path": "f.txt"}); err != nil {
		t.Fatal(err)
	}
}

func TestCallErrors(t *testing.T) {
	ctx := context.Background()
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "hi"})}}
	svc := newHub(t, world(t), inv)
	args := map[string]any{"path": "n.txt"}

	if _, err := svc.Call(ctx, "ORG-A", "ticketing/create", nil); !errors.Is(err, mcpsvc.ErrNotBound) {
		t.Fatalf("unknown MCP = %v", err)
	}
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/delete", nil); !errors.Is(err, mcpsvc.ErrNotFound) {
		t.Fatalf("unknown tool = %v", err)
	}
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/read", nil); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("missing argument = %v", err)
	}
	if _, err := svc.Call(ctx, "ORG-A", "read", nil); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("unqualified tool = %v", err)
	}
	// failures reported by the connector reach the caller as tool errors
	inv.resp = &connectorv1.InvokeResponse{IsError: true, Error: "no such file"}
	var te *mcpsvc.ToolError
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/read", args); !errors.As(err, &te) || !strings.Contains(te.Msg, "no such file") {
		t.Fatalf("connector error = %v", err)
	}
	// transport failures: the connector is unavailable
	inv.resp, inv.err = nil, errors.New("connection refused")
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/read", args); !errors.Is(err, mcpsvc.ErrUnavailable) {
		t.Fatalf("transport error = %v", err)
	}
	inv.err = nil
	// a connector that is not registered
	other := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: world(t)}, Library: platformLibrary(t), Invoker: inv}
	if _, err := other.Call(ctx, "ORG-A", "document-repository/read", args); !errors.Is(err, mcpsvc.ErrUnavailable) {
		t.Fatalf("unregistered connector = %v", err)
	}
	// an algorithm the library does not have
	broken := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: world(t)}, Library: library{algos: map[string]algo.Algorithm{}}, Invoker: inv}
	if _, err := broken.Call(ctx, "ORG-A", "document-repository/read", args); !errors.Is(err, mcpsvc.ErrUnavailable) {
		t.Fatalf("missing algorithm = %v", err)
	}
	// parameter values that do not fit the algorithm
	g := world(t)
	if err := graphsvc.SeedAdapter(ctx, g, mcp.Adapter{Unit: "ORG-B", MCP: "document-repository", Domain: "platform", Algorithm: "localfs-document-repository"}); err != nil {
		t.Fatal(err)
	}
	if _, err := newHub(t, g, inv).Call(ctx, "ORG-B", "document-repository/read", args); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("missing parameter = %v", err)
	}
}

func TestSecretParametersReachTheConnectorButNotTheScript(t *testing.T) {
	ctx := context.Background()
	g := world(t)
	secure := algo.Algorithm{Name: "secure-docs", Type: algo.UsageAdapter, Language: algo.JavaScript, MCP: "document-repository", Connector: "localfs",
		Params: []algo.Param{{Name: "root", Type: algo.ParamString, Required: true}, {Name: "token", Type: algo.ParamSecret, Required: true}, {Name: "other", Type: algo.ParamSecret}},
		Code:   `return { leaked: String(ctx.param("token")), out: ctx.call("read_file", { path: ctx.args().path }).text };`}
	if err := graphsvc.SeedAdapter(ctx, g, mcp.Adapter{Unit: "ORG-B", MCP: "document-repository", Domain: "platform", Algorithm: "secure-docs",
		Params: map[string]any{"root": "/b", "token": "env:TOK", "other": "env:X"}}); err != nil {
		t.Fatal(err)
	}
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "ok"})}}
	out, err := newHub(t, g, inv, secure).Call(ctx, "ORG-B", "document-repository/read", map[string]any{"path": "f"})
	if err != nil || out["leaked"] != "null" {
		t.Fatalf("the script sees the secret: %v, %v", out, err)
	}
	// only the secrets the connector declares leave the hub, resolved; the secret is not in its configuration
	if len(inv.last.Secrets) != 1 || inv.last.Secrets["token"] != "secret:env:TOK" {
		t.Fatalf("secrets = %v", inv.last.Secrets)
	}
	if _, has := pbconv.Map(inv.last.Config)["token"]; has {
		t.Fatalf("a secret is in the configuration: %v", inv.last.Config)
	}
}

func TestCheckAdapterAndTemplate(t *testing.T) {
	ctx := context.Background()
	svc := newHub(t, world(t), &fakeInvoker{})
	ok := graphsvc.LocalFSAdapter("ORG-B", "/b")
	if w, err := svc.CheckAdapter(ctx, ok); err != nil || len(w) != 1 || !strings.Contains(w[0], `secret "token"`) {
		t.Fatalf("valid instance: %v, %v", w, err)
	}
	for name, bad := range map[string]mcp.Adapter{
		"unknown MCP":       {MCP: "nope", Domain: "platform", Algorithm: "localfs-document-repository"},
		"unknown algorithm": {MCP: "document-repository", Domain: "platform", Algorithm: "nope"},
		"missing parameter": {MCP: "document-repository", Domain: "platform", Algorithm: "localfs-document-repository"},
		"unknown parameter": {MCP: "document-repository", Domain: "platform", Algorithm: "localfs-document-repository", Params: map[string]any{"root": "/", "x": 1}},
	} {
		if _, err := svc.CheckAdapter(ctx, bad); !errors.Is(err, mcp.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// an algorithm of another MCP is refused
	other := algo.Algorithm{Name: "tickets", Type: algo.UsageAdapter, Language: algo.JavaScript, MCP: "ticketing", Connector: "jira", Code: "return 1"}
	svc = newHub(t, world(t), &fakeInvoker{}, other)
	if _, err := svc.CheckAdapter(ctx, mcp.Adapter{MCP: "document-repository", Domain: "platform", Algorithm: "tickets"}); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("algorithm of another MCP = %v", err)
	}

	// the template follows the MCP and what the connector exposes
	code, params, err := svc.Template(ctx, "document-repository", "localfs")
	if err != nil || !strings.Contains(code, `ctx.call("read_file", { path: ctx.args().path })`) || !strings.Contains(code, `case "write"`) {
		t.Fatalf("template = %q, %v", code, err)
	}
	if len(params) != 1 || params[0].Name != "token" || params[0].Type != algo.ParamSecret {
		t.Fatalf("params = %+v", params)
	}
	if _, _, err := svc.Template(ctx, "document-repository", "gdrive"); !errors.Is(err, mcpsvc.ErrNotFound) {
		t.Fatalf("template for an unregistered connector = %v", err)
	}
}

func TestSnapshotFollowsTheGraph(t *testing.T) {
	ctx := context.Background()
	g := world(t)
	d := &mcpsvc.Directory{Graph: g}
	s1, err := d.Snapshot(ctx)
	if err != nil || len(s1.Problems) != 0 {
		t.Fatalf("snapshot = %v, %v", s1, err)
	}
	if s2, _ := d.Snapshot(ctx); s2 != s1 {
		t.Fatal("the snapshot of an unchanged head must be reused")
	}
	if err := graphsvc.SeedAdapter(ctx, g, graphsvc.LocalFSAdapter("ORG-B", "/b")); err != nil {
		t.Fatal(err)
	}
	s3, _ := d.Snapshot(ctx)
	if a, inherited, ok := s3.Resolve("ORG-B", "document-repository"); !ok || inherited || a.Params["root"] != "/b" || a.Algorithm != "localfs-document-repository" {
		t.Fatalf("new adapter not seen: %+v %v %v", a, inherited, ok)
	}
	// an empty graph has no adapter and no MCP
	empty := &mcpsvc.Directory{Graph: graph.New(graph.NewMemory())}
	if s, err := empty.Snapshot(ctx); err != nil || len(s.Defs()) != 0 {
		t.Fatalf("empty graph = %v, %v", s, err)
	}
}
