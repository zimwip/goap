package iamsvc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
)

func TestNewOrganization(t *testing.T) {
	o, err := NewOrganization("", "  Acme Corp. ")
	if err != nil || o.ID != "acme-corp" || o.Name != "Acme Corp." {
		t.Fatalf("derived = %+v, %v", o, err)
	}
	for _, bad := range [][2]string{{"", ""}, {"Bad Id", "x"}, {"-x", "x"}} {
		if _, err := NewOrganization(bad[0], bad[1]); !errors.Is(err, ErrOrgInvalid) {
			t.Errorf("%v accepted: %v", bad, err)
		}
	}
}

func TestMemoryOrgStore(t *testing.T) { testOrgStore(t, NewMemoryOrgStore()) }

func TestSQLiteOrgStore(t *testing.T) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "iam", SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	testOrgStore(t, SQLiteOrgStore{DB: db})
}

func TestPGOrgStore(t *testing.T) { testOrgStore(t, PGOrgStore{Pool: pgtest.Pool(t, Migrations)}) }

func testOrgStore(t *testing.T, s OrgStore) {
	ctx := context.Background()
	if _, err := s.Get(ctx, "default"); err != nil {
		t.Fatalf("default organization missing: %v", err)
	}
	o, err := s.Create(ctx, Organization{ID: "acme", Name: "Acme"})
	if err != nil || o.CreatedAt.IsZero() {
		t.Fatalf("create = %+v, %v", o, err)
	}
	if _, err := s.Create(ctx, Organization{ID: "acme", Name: "Again"}); !errors.Is(err, ErrOrgExists) {
		t.Fatalf("duplicate = %v", err)
	}
	if _, err := s.Get(ctx, "nope"); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("unknown = %v", err)
	}
	all, err := s.List(ctx)
	if err != nil || len(all) != 2 || all[0].ID != "acme" || all[1].ID != "default" {
		t.Fatalf("list = %+v, %v", all, err)
	}
}
