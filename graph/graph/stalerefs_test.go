package graph_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/lib/platform"
)

// TestCheckReferences: a database whose references are logical URLs passes, one holding a bare key is refused
// (ADR 0154 §9: nothing migrates them).
func TestCheckReferences(t *testing.T) {
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := platform.MigrateSQLite(ctx, db, "graph", graph.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewSQLite(db))
	if err := g.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if err := graph.CheckReferences(ctx, db); err != nil {
		t.Fatalf("a database of logical URLs: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE node_version SET owner = 'ORG-DEFAULT'`); err != nil {
		t.Fatal(err)
	}
	if err := graph.CheckReferences(ctx, db); !errors.Is(err, graph.ErrStaleReferences) {
		t.Fatalf("a bare key: %v", err)
	}
}
