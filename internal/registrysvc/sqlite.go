package registrysvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// SQLiteMigrations holds the registry schema of the local development mode.
//
//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLiteStore stores methodologies in SQLite (local development mode), the
// definition as a JSON document.
type SQLiteStore struct{ DB *sql.DB }

const sqliteTime = "2006-01-02T15:04:05.000000000Z"

func tsText(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(sqliteTime)
}

func tsParse(s sql.NullString) time.Time {
	t, _ := time.Parse(sqliteTime, s.String)
	return t
}

func (s SQLiteStore) Save(ctx context.Context, r Record) error {
	m := r.Methodology
	def, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM methodology WHERE name = ? AND version = ?`, m.Name, m.Version).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO methodology (name, version, status, definition, created_at, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, m.Name, m.Version, string(r.Status), string(def), tsText(r.UpdatedAt), tsText(r.UpdatedAt), r.UpdatedBy)
	case err != nil:
		return err
	case Status(status) != StatusDraft:
		return fmt.Errorf("%s: %w", key(m.Name, m.Version), ErrImmutable)
	default:
		_, err = tx.ExecContext(ctx, `UPDATE methodology SET definition = ?, updated_at = ?, updated_by = ? WHERE name = ? AND version = ?`,
			string(def), tsText(r.UpdatedAt), r.UpdatedBy, m.Name, m.Version)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

const sqliteCols = `definition, status, created_at, updated_at, published_at, updated_by`

func scanSQLite(row interface{ Scan(...any) error }) (Record, error) {
	var r Record
	var def, status string
	var created, updated, published sql.NullString
	if err := row.Scan(&def, &status, &created, &updated, &published, &r.UpdatedBy); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(def), &r.Methodology); err != nil {
		return r, err
	}
	r.Status, r.CreatedAt, r.UpdatedAt, r.PublishedAt = Status(status), tsParse(created), tsParse(updated), tsParse(published)
	return r, nil
}

func (s SQLiteStore) Get(ctx context.Context, name, version string) (Record, error) {
	q := `SELECT ` + sqliteCols + ` FROM methodology WHERE name = ? AND version = ?`
	args := []any{name, version}
	if version == "" {
		q = `SELECT ` + sqliteCols + ` FROM methodology WHERE name = ? AND status = 'published' ORDER BY published_at DESC LIMIT 1`
		args = args[:1]
	}
	r, err := scanSQLite(s.DB.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	return r, err
}

func (s SQLiteStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+sqliteCols+` FROM methodology ORDER BY name, created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanSQLite(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s SQLiteStore) SetStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	q := `UPDATE methodology SET status = ?, updated_at = ? WHERE name = ? AND version = ?`
	args := []any{string(st), tsText(at), name, version}
	if st == StatusPublished {
		q = `UPDATE methodology SET status = ?, updated_at = ?, published_at = ? WHERE name = ? AND version = ?`
		args = []any{string(st), tsText(at), tsText(at), name, version}
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	return nil
}

func (s SQLiteStore) Delete(ctx context.Context, name, version string) error {
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT status FROM methodology WHERE name = ? AND version = ?`, name, version).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	if err != nil {
		return err
	}
	if Status(status) != StatusDraft {
		return fmt.Errorf("%s: %w", key(name, version), ErrImmutable)
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM methodology WHERE name = ? AND version = ? AND status = 'draft'`, name, version)
	return err
}

func (s SQLiteStore) SaveDomain(ctx context.Context, r DomainRecord) error {
	d := r.Domain
	def, err := json.Marshal(d)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM domain WHERE name = ? AND version = ?`, d.Name, d.Version).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO domain (name, version, status, definition, created_at, updated_at, updated_by)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, d.Name, d.Version, string(r.Status), string(def), tsText(r.UpdatedAt), tsText(r.UpdatedAt), r.UpdatedBy)
	case err != nil:
		return err
	case Status(status) != StatusDraft:
		return fmt.Errorf("domain %s: %w", key(d.Name, d.Version), ErrImmutable)
	default:
		_, err = tx.ExecContext(ctx, `UPDATE domain SET definition = ?, updated_at = ?, updated_by = ? WHERE name = ? AND version = ?`,
			string(def), tsText(r.UpdatedAt), r.UpdatedBy, d.Name, d.Version)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}

func scanSQLiteDomain(row interface{ Scan(...any) error }) (DomainRecord, error) {
	var r DomainRecord
	var def, status string
	var created, updated, published sql.NullString
	if err := row.Scan(&def, &status, &created, &updated, &published, &r.UpdatedBy); err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(def), &r.Domain); err != nil {
		return r, err
	}
	r.Status, r.CreatedAt, r.UpdatedAt, r.PublishedAt = Status(status), tsParse(created), tsParse(updated), tsParse(published)
	return r, nil
}

func (s SQLiteStore) GetDomain(ctx context.Context, name, version string) (DomainRecord, error) {
	q := `SELECT ` + sqliteCols + ` FROM domain WHERE name = ? AND version = ?`
	args := []any{name, version}
	if version == "" {
		q = `SELECT ` + sqliteCols + ` FROM domain WHERE name = ? AND status = 'published' ORDER BY published_at DESC LIMIT 1`
		args = args[:1]
	}
	r, err := scanSQLiteDomain(s.DB.QueryRowContext(ctx, q, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return DomainRecord{}, fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
	}
	return r, err
}

func (s SQLiteStore) ListDomains(ctx context.Context) ([]DomainRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+sqliteCols+` FROM domain ORDER BY name, created_at, rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DomainRecord
	for rows.Next() {
		r, err := scanSQLiteDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s SQLiteStore) SetDomainStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	q := `UPDATE domain SET status = ?, updated_at = ? WHERE name = ? AND version = ?`
	args := []any{string(st), tsText(at), name, version}
	if st == StatusPublished {
		q = `UPDATE domain SET status = ?, updated_at = ?, published_at = ? WHERE name = ? AND version = ?`
		args = []any{string(st), tsText(at), tsText(at), name, version}
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
	}
	return nil
}

func (s SQLiteStore) DeleteDomain(ctx context.Context, name, version string) error {
	var status string
	err := s.DB.QueryRowContext(ctx, `SELECT status FROM domain WHERE name = ? AND version = ?`, name, version).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s: %w", key(name, version), errDomainNotFound)
	}
	if err != nil {
		return err
	}
	if Status(status) != StatusDraft {
		return fmt.Errorf("domain %s: %w", key(name, version), ErrImmutable)
	}
	_, err = s.DB.ExecContext(ctx, `DELETE FROM domain WHERE name = ? AND version = ? AND status = 'draft'`, name, version)
	return err
}
