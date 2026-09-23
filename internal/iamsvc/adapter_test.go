package iamsvc

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/casbin/casbin/v2/persist"

	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/authz"
)

func TestAdapterPersistsPolicies(t *testing.T) {
	pool := pgtest.Pool(t, Migrations)
	testAdapter(t, &Adapter{Pool: pool})
}

func TestSQLiteAdapterPersistsPolicies(t *testing.T) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "iam", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	testAdapter(t, &SQLiteAdapter{DB: db})
}

func testAdapter(t *testing.T, adapter persist.Adapter) {
	ctx := context.Background()
	e1, err := authz.NewCasbin(adapter)
	if err != nil {
		t.Fatal(err)
	}
	extra := authz.Policy{Rule: `r.sub.Org == "frozen"`, Resource: "change", Action: "apply", Effect: "deny"}
	if err := e1.AddPolicy(extra); err != nil {
		t.Fatal(err)
	}
	// a second enforcer (another replica) sees seeded + added policies
	e2, err := authz.NewCasbin(adapter)
	if err != nil {
		t.Fatal(err)
	}
	pols, _ := e2.Policies()
	if len(pols) != len(authz.DefaultPolicies)+1 {
		t.Fatalf("expected %d policies, got %d", len(authz.DefaultPolicies)+1, len(pols))
	}
	req := authz.Request{Subject: authz.Principal{Subject: "root", Org: "frozen", Roles: []string{"admin"}}, Action: "apply", Resource: authz.Resource{Type: "change"}}
	if ok, _ := e2.Authorize(ctx, req); ok {
		t.Fatal("deny policy not loaded")
	}
	if err := e1.RemovePolicy(extra); err != nil {
		t.Fatal(err)
	}
	if err := e2.Reload(); err != nil {
		t.Fatal(err)
	}
	if ok, _ := e2.Authorize(ctx, req); !ok {
		t.Fatal("removed policy still applied after reload")
	}
}
