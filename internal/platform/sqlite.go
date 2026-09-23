package platform

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure Go SQLite driver (no cgo)
)

// OpenSQLite opens (and creates) a SQLite database file for the local
// development mode. Writes are serialized on a single connection: SQLite has
// one writer, and this avoids SQLITE_BUSY on concurrent transactions.
func OpenSQLite(ctx context.Context, path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	q := url.Values{}
	for _, p := range []string{"foreign_keys(1)", "journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite %s: %w", path, err)
	}
	return db, nil
}

// MigrateSQLite applies the *.sql files of dir in name order, once. Several
// components share one file: applied migrations are recorded per component.
func MigrateSQLite(ctx context.Context, db *sql.DB, component string, migrations fs.FS, dir string) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		component TEXT NOT NULL, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		PRIMARY KEY (component, name))`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrations, dir)
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migrations WHERE component = ? AND name = ?`, component, e.Name()).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		script, err := fs.ReadFile(migrations, dir+"/"+e.Name())
		if err != nil {
			return err
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(script)); err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("%s/%s: %w", component, e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (component, name) VALUES (?, ?)`, component, e.Name()); err != nil {
			tx.Rollback() //nolint:errcheck
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
