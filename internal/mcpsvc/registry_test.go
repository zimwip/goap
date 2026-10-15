package mcpsvc_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/internal/mcpsvc"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/mcp"

	"github.com/jackc/pgx/v5/stdlib"
)

func localfsInfo() *connectorv1.ConnectorInfo {
	return &connectorv1.ConnectorInfo{Id: "localfs", Version: "1", SecretNames: []string{"token"}, Operations: []*connectorv1.Operation{
		{Name: "list_dir"}, {Name: "read_file"}, {Name: "write_file"}}}
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
