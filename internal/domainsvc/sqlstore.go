package domainsvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain/def"
)

// Migrations holds the PostgreSQL schema of the domain registry; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLStore keeps the domain versions in the database of the domain registry (ADR 0023, 0125), on database/sql: SQLite (local mode)
// and PostgreSQL (through the pgx stdlib adapter, Dollar set). A domain is the model each graph holds in memory to check
// its data; it is not graph data itself.
type SQLStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var _ Store = SQLStore{}

func (s SQLStore) q(query string) string {
	if !s.Dollar {
		return query
	}
	var b strings.Builder
	n := 0
	for _, r := range query {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func sqlTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseSQLTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

const domainColumns = `name, version, status, definition, created_at, updated_at, published_at, updated_by`

type rowScanner interface{ Scan(dest ...any) error }

func scanDomain(row rowScanner) (def.Record, error) {
	var r def.Record
	var name, version, status, definition, created, updated, published string
	if err := row.Scan(&name, &version, &status, &definition, &created, &updated, &published, &r.UpdatedBy); err != nil {
		return def.Record{}, err
	}
	if err := json.Unmarshal([]byte(definition), &r.Domain); err != nil {
		return def.Record{}, fmt.Errorf("domain %s: %w", key(name, version), err)
	}
	r.Domain.Name, r.Domain.Version, r.Status = name, version, def.Status(status)
	var err error
	if r.CreatedAt, err = parseSQLTime(created); err != nil {
		return def.Record{}, err
	}
	if r.UpdatedAt, err = parseSQLTime(updated); err != nil {
		return def.Record{}, err
	}
	if r.PublishedAt, err = parseSQLTime(published); err != nil {
		return def.Record{}, err
	}
	return r, nil
}

func (s SQLStore) SaveDomain(ctx context.Context, r def.Record) error {
	definition, err := json.Marshal(r.Domain)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	k := key(r.Domain.Name, r.Domain.Version)
	var status, created string
	switch err := tx.QueryRowContext(ctx, s.q(`SELECT status, created_at FROM domain_version WHERE name = ? AND version = ?`),
		r.Domain.Name, r.Domain.Version).Scan(&status, &created); {
	case errors.Is(err, sql.ErrNoRows):
		created = sqlTime(r.CreatedAt)
		if created == "" {
			created = sqlTime(r.UpdatedAt)
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO domain_version (`+domainColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`),
			r.Domain.Name, r.Domain.Version, string(r.Status), string(definition), created, sqlTime(r.UpdatedAt), sqlTime(r.PublishedAt), r.UpdatedBy); err != nil {
			return err
		}
	case err != nil:
		return err
	case def.Status(status) != def.StatusDraft:
		return fmt.Errorf("domain %s: %w", k, ErrImmutable)
	default:
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE domain_version SET status = ?, definition = ?, updated_at = ?, published_at = ?, updated_by = ?
			WHERE name = ? AND version = ?`), string(r.Status), string(definition), sqlTime(r.UpdatedAt), sqlTime(r.PublishedAt), r.UpdatedBy,
			r.Domain.Name, r.Domain.Version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s SQLStore) GetDomain(ctx context.Context, name, version string) (def.Record, error) {
	if version != "" {
		r, err := scanDomain(s.DB.QueryRowContext(ctx, s.q(`SELECT `+domainColumns+` FROM domain_version WHERE name = ? AND version = ?`), name, version))
		if errors.Is(err, sql.ErrNoRows) {
			return def.Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
		}
		return r, err
	}
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT `+domainColumns+` FROM domain_version WHERE name = ? AND status = ?`), name, string(def.StatusPublished))
	if err != nil {
		return def.Record{}, err
	}
	defer rows.Close()
	var best def.Record
	found := false
	for rows.Next() {
		r, err := scanDomain(rows)
		if err != nil {
			return def.Record{}, err
		}
		if !found || r.PublishedAt.After(best.PublishedAt) {
			best, found = r, true
		}
	}
	if err := rows.Err(); err != nil {
		return def.Record{}, err
	}
	if !found {
		return def.Record{}, fmt.Errorf("%s (published): %w", name, ErrNotFound)
	}
	return best, nil
}

func (s SQLStore) ListDomains(ctx context.Context) ([]def.Record, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+domainColumns+` FROM domain_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []def.Record
	for rows.Next() {
		r, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sortDomainRecords(out)
	return out, nil
}

func (s SQLStore) SetDomainStatus(ctx context.Context, name, version string, st def.Status, at time.Time) error {
	query := `UPDATE domain_version SET status = ?, updated_at = ? WHERE name = ? AND version = ?`
	args := []any{string(st), sqlTime(at), name, version}
	if st == def.StatusPublished {
		query = `UPDATE domain_version SET status = ?, updated_at = ?, published_at = ? WHERE name = ? AND version = ?`
		args = []any{string(st), sqlTime(at), sqlTime(at), name, version}
	}
	res, err := s.DB.ExecContext(ctx, s.q(query), args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	return nil
}

func (s SQLStore) DeleteDomain(ctx context.Context, name, version string) error {
	r, err := s.GetDomain(ctx, name, version)
	if err != nil {
		return err
	}
	if r.Status != def.StatusDraft {
		return fmt.Errorf("domain %s: %w", key(name, version), ErrImmutable)
	}
	_, err = s.DB.ExecContext(ctx, s.q(`DELETE FROM domain_version WHERE name = ? AND version = ? AND status = ?`), name, version, string(def.StatusDraft))
	return err
}
