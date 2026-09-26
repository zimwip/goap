package mcpsvc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/graphsvc"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/mcp"

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

func newHub(t *testing.T, g *graph.Graph, inv mcpsvc.Invoker) *mcpsvc.Service {
	t.Helper()
	svc := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: g}, Invoker: inv,
		Secrets: func(_ context.Context, ref string) (string, error) { return "secret:" + ref, nil }}
	if _, err := svc.RegisterConnector(context.Background(), localfsInfo(), "http://localfs:8080"); err != nil {
		t.Fatal(err)
	}
	return svc
}

// The same MCP and the same connector, configured differently by each unit; the nearest unit's
// adapter wins, and every unit falls back on the default organisation.
func TestNearestAdapterWins(t *testing.T) {
	ctx := context.Background()
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "hi"})}}
	svc := newHub(t, world(t), inv)
	args := map[string]any{"path": "n.txt"}
	for unit, wantRoot := range map[string]string{
		"ORG-A":           "/a",       // its own adapter
		"ORG-A1":          "/a",       // inherited from ORG-A, not from the default organisation
		"ORG-B":           "/default", // no ancestor: the default organisation
		"":                "/default", // a change naming no unit
		"ORG-UNKNOWN":     "/default", // a unit the graph does not know
		domain.DefaultOrg: "/default",
	} {
		if _, err := svc.Call(ctx, unit, "document-repository/read", args); err != nil {
			t.Fatalf("%q: %v", unit, err)
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
	if len(eff) != 1 || eff[0].Adapter.Unit != "ORG-A" || !eff[0].Inherited {
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

func TestCallErrors(t *testing.T) {
	ctx := context.Background()
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "hi"})}}
	svc := newHub(t, world(t), inv)

	// an MCP nobody implements
	if _, err := svc.Call(ctx, "ORG-A", "ticketing/create", nil); !errors.Is(err, mcpsvc.ErrNotBound) {
		t.Fatalf("unimplemented MCP = %v", err)
	}
	if _, err := svc.Call(ctx, "ORG-A", "read", nil); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("unqualified tool = %v", err)
	}
	// only the secrets the connector declares leave the hub
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/read", nil); err != nil {
		t.Fatal(err)
	}
	if len(inv.last.Secrets) != 0 {
		t.Fatalf("secrets = %v", inv.last.Secrets)
	}
	// failures reported by the connector, and transport failures
	inv.resp = &connectorv1.InvokeResponse{IsError: true, Error: "no such file"}
	var te *mcpsvc.ToolError
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/read", nil); !errors.As(err, &te) || te.Msg != "no such file" {
		t.Fatalf("connector error = %v", err)
	}
	inv.resp, inv.err = nil, errors.New("connection refused")
	if _, err := svc.Call(ctx, "ORG-A", "document-repository/read", nil); !errors.Is(err, mcpsvc.ErrUnavailable) {
		t.Fatalf("transport error = %v", err)
	}
	// a connector that is not registered
	inv.err = nil
	other := &mcpsvc.Service{Store: mcpsvc.NewMemoryStore(), Directory: &mcpsvc.Directory{Graph: world(t)}, Invoker: inv}
	if _, err := other.Call(ctx, "ORG-A", "document-repository/read", nil); !errors.Is(err, mcpsvc.ErrUnavailable) {
		t.Fatalf("unregistered connector = %v", err)
	}
}

func TestSecretsAreResolvedAndFiltered(t *testing.T) {
	ctx := context.Background()
	g := world(t)
	a := graphsvc.LocalFSAdapter("ORG-B", "/b")
	a.Secrets = map[string]string{"token": "env:TOK", "undeclared": "env:X"}
	if err := graphsvc.SeedAdapter(ctx, g, a); err != nil {
		t.Fatal(err)
	}
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{}}
	svc := newHub(t, g, inv)
	if _, err := svc.Call(ctx, "ORG-B", "document-repository/read", nil); err != nil {
		t.Fatal(err)
	}
	if len(inv.last.Secrets) != 1 || inv.last.Secrets["token"] != "secret:env:TOK" {
		t.Fatalf("secrets = %v", inv.last.Secrets)
	}
}

func TestCheckAdapter(t *testing.T) {
	ctx := context.Background()
	svc := newHub(t, world(t), &fakeInvoker{})
	ok := graphsvc.LocalFSAdapter("ORG-B", "/b")
	ok.Secrets = map[string]string{"token": "env:T"}
	if w, err := svc.CheckAdapter(ctx, ok); err != nil || len(w) != 0 {
		t.Fatalf("valid adapter: %v, %v", w, err)
	}
	// unknown MCP, unknown tool: blocking
	bad := ok
	bad.MCP = "nope"
	if _, err := svc.CheckAdapter(ctx, bad); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("unknown MCP = %v", err)
	}
	bad = ok
	bad.Tools = []mcp.ToolMapping{{Tool: "delete", Operation: "rm"}}
	if _, err := svc.CheckAdapter(ctx, bad); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("unknown tool = %v", err)
	}
	// mismatches with the registered connector: warnings
	warn := ok
	warn.Tools = []mcp.ToolMapping{{Tool: "read", Operation: "no_such_op"}}
	warn.Config = nil
	warn.Secrets = map[string]string{"other": "env:X"}
	w, err := svc.CheckAdapter(ctx, warn)
	if err != nil || len(w) < 3 {
		t.Fatalf("warnings = %v, %v", w, err)
	}
	// an unregistered connector cannot be checked
	unk := ok
	unk.Connector = "gdrive"
	if w, err := svc.CheckAdapter(ctx, unk); err != nil || len(w) != 1 {
		t.Fatalf("unregistered connector = %v, %v", w, err)
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
	if a, inherited, ok := s3.Resolve("ORG-B", "document-repository"); !ok || inherited || a.Config["root"] != "/b" {
		t.Fatalf("new adapter not seen: %+v %v %v", a, inherited, ok)
	}
	// an empty graph has no adapter and no MCP
	empty := &mcpsvc.Directory{Graph: graph.New(graph.NewMemory())}
	if s, err := empty.Snapshot(ctx); err != nil || len(s.Defs()) != 0 {
		t.Fatalf("empty graph = %v, %v", s, err)
	}
}
