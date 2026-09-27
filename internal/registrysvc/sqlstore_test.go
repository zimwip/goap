package registrysvc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/domains/builtin"
	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/methodology"
)

// domainStores are the stores of the domain versions: in memory, the registry's database in local mode (SQLite) and,
// with GOAP_TEST_PG_DSN, on PostgreSQL.
func domainStores(t *testing.T) map[string]func(t *testing.T) DomainStore {
	return map[string]func(t *testing.T) DomainStore{
		"memory": func(*testing.T) DomainStore { return NewMemoryStore() },
		"sqlite": func(t *testing.T) DomainStore {
			ctx := context.Background()
			db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if err := platform.MigrateSQLite(ctx, db, "registry", SQLiteMigrations, "migrations_sqlite"); err != nil {
				t.Fatal(err)
			}
			return SQLDomainStore{DB: db}
		},
		"postgres": func(t *testing.T) DomainStore {
			return SQLDomainStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, Migrations)), Dollar: true}
		},
	}
}

// Every domain of the repository, the built-in ones included, round-trips through the stores.
func TestDomainStoresRoundTripEveryDomain(t *testing.T) {
	var ds []*methodology.Domain
	files, _ := filepath.Glob("../../domains/*.yaml")
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		d, err := methodology.ParseDomain(src)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		ds = append(ds, d)
	}
	entries, _ := builtin.FS.ReadDir(".")
	for _, e := range entries {
		src, _ := builtin.FS.ReadFile(e.Name())
		d, err := methodology.ParseDomain(src)
		if err != nil {
			t.Fatalf("%s: %v", e.Name(), err)
		}
		ds = append(ds, d)
	}
	if len(ds) < 4 {
		t.Fatalf("domains: %d", len(ds))
	}
	for name, mk := range domainStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := mk(t)
			now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
			for _, d := range ds {
				if err := st.SaveDomain(ctx, DomainRecord{Domain: *d, Status: StatusDraft, CreatedAt: now, UpdatedAt: now, UpdatedBy: "mia"}); err != nil {
					t.Fatal(err)
				}
				got, err := st.GetDomain(ctx, d.Name, d.Version)
				if err != nil {
					t.Fatal(err)
				}
				if canon(t, got.Domain) != canon(t, *d) || got.Status != StatusDraft || !got.CreatedAt.Equal(now) || got.UpdatedBy != "mia" {
					t.Errorf("domain %s does not round-trip:\n have %s\n want %s", d.Name, canon(t, got.Domain), canon(t, *d))
				}
			}
		})
	}
}

// The stores keep the lifecycle of a version: a draft is replaced, a published version is immutable and in force,
// only a draft is deleted.
func TestDomainStoresLifecycle(t *testing.T) {
	for name, mk := range domainStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := mk(t)
			t0 := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
			d := almDomain("1")
			if err := st.SaveDomain(ctx, DomainRecord{Domain: d, Status: StatusDraft, CreatedAt: t0, UpdatedAt: t0}); err != nil {
				t.Fatal(err)
			}
			d.Description = "second draft"
			if err := st.SaveDomain(ctx, DomainRecord{Domain: d, Status: StatusDraft, UpdatedAt: t0.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if got, _ := st.GetDomain(ctx, "alm", "1"); got.Domain.Description != "second draft" || !got.CreatedAt.Equal(t0) {
				t.Fatalf("a draft is replaced and keeps its creation: %+v", got)
			}
			if _, err := st.GetDomain(ctx, "alm", ""); !errors.Is(err, ErrNotFound) {
				t.Fatalf("no published version: %v", err)
			}
			for i, v := range []string{"1", "2"} {
				if v != "1" {
					if err := st.SaveDomain(ctx, DomainRecord{Domain: almDomain(v), Status: StatusDraft, CreatedAt: t0, UpdatedAt: t0}); err != nil {
						t.Fatal(err)
					}
				}
				if err := st.SetDomainStatus(ctx, "alm", v, StatusPublished, t0.Add(time.Duration(i+1)*time.Hour)); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := st.GetDomain(ctx, "alm", ""); err != nil || got.Domain.Version != "2" || !got.PublishedAt.Equal(t0.Add(2*time.Hour)) {
				t.Fatalf("the latest published version is in force: %+v %v", got, err)
			}
			if err := st.SaveDomain(ctx, DomainRecord{Domain: almDomain("2"), Status: StatusDraft, UpdatedAt: t0}); !errors.Is(err, ErrImmutable) {
				t.Fatalf("a published version is immutable: %v", err)
			}
			if err := st.DeleteDomain(ctx, "alm", "2"); !errors.Is(err, ErrImmutable) {
				t.Fatalf("only a draft is deleted: %v", err)
			}
			if err := st.SaveDomain(ctx, DomainRecord{Domain: almDomain("3"), Status: StatusDraft, CreatedAt: t0, UpdatedAt: t0}); err != nil {
				t.Fatal(err)
			}
			if err := st.DeleteDomain(ctx, "alm", "3"); err != nil {
				t.Fatal(err)
			}
			if list, err := st.ListDomains(ctx); err != nil || len(list) != 2 {
				t.Fatalf("list: %+v %v", list, err)
			}
			if err := st.SetDomainStatus(ctx, "alm", "9", StatusPublished, t0); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unknown version: %v", err)
			}
		})
	}
}
