package registrysvc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/internal/pgtest"
	"github.com/zimwip/goap/internal/platform"
	"github.com/zimwip/goap/pkg/algo"
)

// algorithmStores are the stores of the platform-wide algorithm registry (ADR 0041): in memory, the registry's
// database in local mode (SQLite) and, with GOAP_TEST_PG_DSN, on PostgreSQL.
func algorithmStores(t *testing.T) map[string]func(t *testing.T) AlgorithmStore {
	return map[string]func(t *testing.T) AlgorithmStore{
		"memory": func(*testing.T) AlgorithmStore { return NewMemoryStore() },
		"sqlite": func(t *testing.T) AlgorithmStore {
			ctx := context.Background()
			db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			if err := platform.MigrateSQLite(ctx, db, "registry", SQLiteMigrations, "migrations_sqlite"); err != nil {
				t.Fatal(err)
			}
			return SQLAlgorithmStore{DB: db}
		},
		"postgres": func(t *testing.T) AlgorithmStore {
			return SQLAlgorithmStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, Migrations)), Dollar: true}
		},
	}
}

func TestAlgorithmStoresRoundTrip(t *testing.T) {
	a := algo.Algorithm{Name: "regex-match", Description: "checks a pattern", Type: algo.UsagePropertyValidator, Language: algo.JavaScript,
		Params: []algo.Param{{Name: "pattern", Type: algo.ParamRegex, Required: true}}, Code: "return true"}
	for name, mk := range algorithmStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := mk(t)
			if _, err := st.GetAlgorithm(ctx, "regex-match"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("unknown algorithm: %v", err)
			}
			t0 := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
			if err := st.SaveAlgorithm(ctx, AlgorithmRecord{Algorithm: a, SourceDomain: "alm", SourceVersion: "1", CreatedAt: t0, UpdatedAt: t0}); err != nil {
				t.Fatal(err)
			}
			got, err := st.GetAlgorithm(ctx, "regex-match")
			if err != nil || got.Algorithm.Description != a.Description || got.Algorithm.Params[0].Type != "regex" || got.SourceDomain != "alm" || !got.CreatedAt.Equal(t0) {
				t.Fatalf("get: %+v %v", got, err)
			}
			// saving again (e.g. an idempotent republish) replaces the definition, keeps the creation time
			a2 := a
			a2.Description = "updated"
			if err := st.SaveAlgorithm(ctx, AlgorithmRecord{Algorithm: a2, SourceDomain: "alm", SourceVersion: "2", UpdatedAt: t0.Add(time.Hour)}); err != nil {
				t.Fatal(err)
			}
			if got, err := st.GetAlgorithm(ctx, "regex-match"); err != nil || got.Algorithm.Description != "updated" || got.SourceVersion != "2" || !got.CreatedAt.Equal(t0) {
				t.Fatalf("update keeps creation: %+v %v", got, err)
			}
			if err := st.SaveAlgorithm(ctx, AlgorithmRecord{Algorithm: algo.Algorithm{Name: "other"}, CreatedAt: t0, UpdatedAt: t0}); err != nil {
				t.Fatal(err)
			}
			list, err := st.ListAlgorithms(ctx)
			if err != nil || len(list) != 2 || list[0].Algorithm.Name != "other" || list[1].Algorithm.Name != "regex-match" {
				t.Fatalf("list (sorted by name): %+v %v", list, err)
			}
		})
	}
}
