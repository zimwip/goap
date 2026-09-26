package mcpsvc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/pbconv"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
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

func TestMemoryStore(t *testing.T) { testHub(t, NewMemoryStore()) }

func TestSQLiteStore(t *testing.T) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "mcp", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	testHub(t, SQLStore{DB: db})
}

func TestPGStore(t *testing.T) {
	testHub(t, SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, Migrations)), Dollar: true})
}

func testHub(t *testing.T, st Store) {
	ctx := context.Background()
	now := time.Now().UTC()
	inv := &fakeInvoker{resp: &connectorv1.InvokeResponse{Result: pbconv.Struct(map[string]any{"text": "hello", "meta": map[string]any{"n": 1}})}}
	svc := &Service{Store: st, Invoker: inv, Now: func() time.Time { return now }, Lease: time.Minute,
		Secrets: func(_ context.Context, ref string) (string, error) { return "secret:" + ref, nil }}

	if err := Seed(ctx, st); err != nil {
		t.Fatal(err)
	}
	if err := Seed(ctx, st); err != nil { // idempotent
		t.Fatal(err)
	}

	// nothing bound yet
	if _, err := svc.Call(ctx, "acme", "document-repository/read", nil); !errors.Is(err, ErrNotBound) {
		t.Fatalf("unbound call = %v", err)
	}
	tools, mcps, err := svc.Tools(ctx, "acme")
	if err != nil || len(tools) != 0 || len(mcps) != 0 {
		t.Fatalf("tools before binding = %v %v %v", tools, mcps, err)
	}

	// an adapter for a tool the MCP does not have is refused; the valid one warns while the connector is unknown
	bad := mcp.Adapter{MCP: "document-repository", Connector: "gdrive", Tools: []mcp.ToolMapping{{Tool: "delete", Operation: "rm"}}}
	if _, err := svc.SaveAdapter(ctx, bad); !errors.Is(err, mcp.ErrInvalid) {
		t.Fatalf("bad adapter = %v", err)
	}
	warn, err := svc.SaveAdapter(ctx, LocalFSAdapter())
	if err != nil || len(warn) != 1 {
		t.Fatalf("adapter before registration: warnings %v, %v", warn, err)
	}

	// binding needs an adapter
	if err := svc.Bind(ctx, mcp.Binding{OrgID: "acme", MCP: "document-repository", Connector: "gdrive"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bind without adapter = %v", err)
	}
	b := mcp.Binding{OrgID: "acme", MCP: "document-repository", Connector: "localfs",
		Config: map[string]any{"root": "/data"}, Secrets: map[string]string{"token": "env:TOK", "unused": "env:X"}}
	if err := svc.Bind(ctx, b); err != nil {
		t.Fatal(err)
	}
	tools, mcps, err = svc.Tools(ctx, "acme")
	if err != nil || len(tools) != 3 || len(mcps) != 1 || tools[0].Name != "document-repository/list" {
		t.Fatalf("tools = %v %v %v", tools, mcps, err)
	}
	if other, _, _ := svc.Tools(ctx, "globex"); len(other) != 0 {
		t.Fatal("another organization sees the tools")
	}

	// the connector is not registered yet
	if _, err := svc.Call(ctx, "acme", "document-repository/read", map[string]any{"path": "a.txt"}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("call before registration = %v", err)
	}
	if _, err := svc.RegisterConnector(ctx, localfsInfo(), "http://localfs:8080"); err != nil {
		t.Fatal(err)
	}
	out, err := svc.Call(ctx, "acme", "document-repository/read", map[string]any{"path": "a.txt", "ignored": 1})
	if err != nil || out["text"] != "hello" {
		t.Fatalf("call = %v, %v", out, err)
	}
	got := inv.last
	if got.Operation != "read_file" || pbconv.Map(got.Arguments)["path"] != "a.txt" || len(pbconv.Map(got.Arguments)) != 1 {
		t.Fatalf("operation call = %v", got)
	}
	if pbconv.Map(got.Config)["root"] != "/data" || got.OrgId != "acme" {
		t.Fatalf("config/org = %v %v", got.Config, got.OrgId)
	}
	if len(got.Secrets) != 1 || got.Secrets["token"] != "secret:env:TOK" {
		t.Fatalf("secrets must be limited to the ones the connector declares: %v", got.Secrets)
	}

	// a failure reported by the connector
	inv.resp = &connectorv1.InvokeResponse{IsError: true, Error: "no such file"}
	var te *ToolError
	if _, err := svc.Call(ctx, "acme", "document-repository/read", nil); !errors.As(err, &te) || te.Msg != "no such file" {
		t.Fatalf("connector error = %v", err)
	}
	// transport failure
	inv.resp, inv.err = nil, errors.New("connection refused")
	if _, err := svc.Call(ctx, "acme", "document-repository/read", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("transport error = %v", err)
	}
	inv.err = nil

	// lease expiry
	now = now.Add(2 * time.Minute)
	if _, err := svc.Call(ctx, "acme", "document-repository/read", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("expired lease = %v", err)
	}
	cs, err := svc.Connectors(ctx)
	if err != nil || len(cs) != 1 || cs[0].Live {
		t.Fatalf("connectors = %+v, %v", cs, err)
	}
	if _, err := svc.RegisterConnector(ctx, localfsInfo(), "http://localfs:8080"); err != nil {
		t.Fatal(err)
	}
	if cs, _ = svc.Connectors(ctx); !cs[0].Live {
		t.Fatal("renewed registration is not live")
	}

	// referential integrity
	if err := st.DeleteAdapter(ctx, "document-repository", "localfs"); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete bound adapter = %v", err)
	}
	if err := st.DeleteMcp(ctx, "document-repository"); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete implemented mcp = %v", err)
	}
	for _, f := range []func() error{
		func() error { return st.DeleteBinding(ctx, "acme", "document-repository") },
		func() error { return st.DeleteAdapter(ctx, "document-repository", "localfs") },
		func() error { return st.DeleteMcp(ctx, "document-repository") },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Mcp(ctx, "document-repository"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted mcp = %v", err)
	}
}
