package registrysvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// SQLAlgorithmStore keeps the platform-wide algorithm registry (ADR 0041) in the registry's database, on
// database/sql: SQLite (local mode) and PostgreSQL (through the pgx stdlib adapter, Dollar set).
type SQLAlgorithmStore struct {
	DB *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var _ AlgorithmStore = SQLAlgorithmStore{}

func (s SQLAlgorithmStore) q(query string) string { return rewritePlaceholders(s.Dollar, query) }

const algorithmColumns = `name, definition, source_domain, source_version, created_at, updated_at`

func scanAlgorithm(row rowScanner) (AlgorithmRecord, error) {
	var r AlgorithmRecord
	var name, def, created, updated string
	if err := row.Scan(&name, &def, &r.SourceDomain, &r.SourceVersion, &created, &updated); err != nil {
		return AlgorithmRecord{}, err
	}
	if err := json.Unmarshal([]byte(def), &r.Algorithm); err != nil {
		return AlgorithmRecord{}, fmt.Errorf("algorithm %s: %w", name, err)
	}
	var err error
	if r.CreatedAt, err = parseSQLTime(created); err != nil {
		return AlgorithmRecord{}, err
	}
	if r.UpdatedAt, err = parseSQLTime(updated); err != nil {
		return AlgorithmRecord{}, err
	}
	return r, nil
}

func (s SQLAlgorithmStore) SaveAlgorithm(ctx context.Context, r AlgorithmRecord) error {
	def, err := json.Marshal(r.Algorithm)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	var created string
	switch err := tx.QueryRowContext(ctx, s.q(`SELECT created_at FROM algorithm_registry WHERE name = ?`), r.Algorithm.Name).Scan(&created); {
	case errors.Is(err, sql.ErrNoRows):
		created = sqlTime(r.CreatedAt)
		if created == "" {
			created = sqlTime(r.UpdatedAt)
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO algorithm_registry (`+algorithmColumns+`) VALUES (?, ?, ?, ?, ?, ?)`),
			r.Algorithm.Name, string(def), r.SourceDomain, r.SourceVersion, created, sqlTime(r.UpdatedAt)); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if _, err := tx.ExecContext(ctx, s.q(`UPDATE algorithm_registry SET definition = ?, source_domain = ?, source_version = ?, updated_at = ? WHERE name = ?`),
			string(def), r.SourceDomain, r.SourceVersion, sqlTime(r.UpdatedAt), r.Algorithm.Name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s SQLAlgorithmStore) GetAlgorithm(ctx context.Context, name string) (AlgorithmRecord, error) {
	r, err := scanAlgorithm(s.DB.QueryRowContext(ctx, s.q(`SELECT `+algorithmColumns+` FROM algorithm_registry WHERE name = ?`), name))
	if errors.Is(err, sql.ErrNoRows) {
		return AlgorithmRecord{}, fmt.Errorf("%s: %w", name, errAlgorithmNotFound)
	}
	return r, err
}

func (s SQLAlgorithmStore) ListAlgorithms(ctx context.Context) ([]AlgorithmRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+algorithmColumns+` FROM algorithm_registry ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlgorithmRecord
	for rows.Next() {
		r, err := scanAlgorithm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
