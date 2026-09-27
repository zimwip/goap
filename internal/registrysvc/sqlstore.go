package registrysvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Migrations holds the PostgreSQL schema of the registry's database; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLDomainStore keeps the domain versions in the registry's database (ADR 0023), on database/sql: SQLite (local mode)
// and PostgreSQL (through the pgx stdlib adapter, Dollar set). A domain is the model each graph holds in memory to check
// its data; it is not graph data itself.
type SQLDomainStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var _ DomainStore = SQLDomainStore{}

func (s SQLDomainStore) q(query string) string {
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

func scanDomain(row rowScanner) (DomainRecord, error) {
	var r DomainRecord
	var name, version, status, def, created, updated, published string
	if err := row.Scan(&name, &version, &status, &def, &created, &updated, &published, &r.UpdatedBy); err != nil {
		return DomainRecord{}, err
	}
	if err := json.Unmarshal([]byte(def), &r.Domain); err != nil {
		return DomainRecord{}, fmt.Errorf("domain %s: %w", key(name, version), err)
	}
	r.Domain.Name, r.Domain.Version, r.Status = name, version, Status(status)
	var err error
	if r.CreatedAt, err = parseSQLTime(created); err != nil {
		return DomainRecord{}, err
	}
	if r.UpdatedAt, err = parseSQLTime(updated); err != nil {
		return DomainRecord{}, err
	}
	if r.PublishedAt, err = parseSQLTime(published); err != nil {
		return DomainRecord{}, err
	}
	return r, nil
}

func (s SQLDomainStore) SaveDomain(ctx context.Context, r DomainRecord) error {
	def, err := json.Marshal(r.Domain)
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
			r.Domain.Name, r.Domain.Version, string(r.Status), string(def), created, sqlTime(r.UpdatedAt), sqlTime(r.PublishedAt), r.UpdatedBy); err != nil {
			return err
		}
	case err != nil:
		return err
	case Status(status) != StatusDraft:
		return fmt.Errorf("domain %s: %w", k, ErrImmutable)
	default:
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE domain_version SET status = ?, definition = ?, updated_at = ?, published_at = ?, updated_by = ?
			WHERE name = ? AND version = ?`), string(r.Status), string(def), sqlTime(r.UpdatedAt), sqlTime(r.PublishedAt), r.UpdatedBy,
			r.Domain.Name, r.Domain.Version); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s SQLDomainStore) GetDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	if version != "" {
		r, err := scanDomain(s.DB.QueryRowContext(ctx, s.q(`SELECT `+domainColumns+` FROM domain_version WHERE name = ? AND version = ?`), name, version))
		if errors.Is(err, sql.ErrNoRows) {
			return DomainRecord{}, fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
		}
		return r, err
	}
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT `+domainColumns+` FROM domain_version WHERE name = ? AND status = ?`), name, string(StatusPublished))
	if err != nil {
		return DomainRecord{}, err
	}
	defer rows.Close()
	var best DomainRecord
	found := false
	for rows.Next() {
		r, err := scanDomain(rows)
		if err != nil {
			return DomainRecord{}, err
		}
		if !found || r.PublishedAt.After(best.PublishedAt) {
			best, found = r, true
		}
	}
	if err := rows.Err(); err != nil {
		return DomainRecord{}, err
	}
	if !found {
		return DomainRecord{}, fmt.Errorf("%s (published): %w", name, errDomainNotFound)
	}
	return best, nil
}

func (s SQLDomainStore) ListDomains(ctx context.Context) ([]DomainRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+domainColumns+` FROM domain_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainRecord
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

func (s SQLDomainStore) SetDomainStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	query := `UPDATE domain_version SET status = ?, updated_at = ? WHERE name = ? AND version = ?`
	args := []any{string(st), sqlTime(at), name, version}
	if st == StatusPublished {
		query = `UPDATE domain_version SET status = ?, updated_at = ?, published_at = ? WHERE name = ? AND version = ?`
		args = []any{string(st), sqlTime(at), sqlTime(at), name, version}
	}
	res, err := s.DB.ExecContext(ctx, s.q(query), args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
	}
	return nil
}

func (s SQLDomainStore) DeleteDomain(ctx context.Context, name, version string) error {
	r, err := s.GetDomain(ctx, name, version)
	if err != nil {
		return err
	}
	if r.Status != StatusDraft {
		return fmt.Errorf("domain %s: %w", key(name, version), ErrImmutable)
	}
	_, err = s.DB.ExecContext(ctx, s.q(`DELETE FROM domain_version WHERE name = ? AND version = ? AND status = ?`), name, version, string(StatusDraft))
	return err
}
