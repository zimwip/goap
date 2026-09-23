package iamsvc

import (
	"context"
	"database/sql"
	"embed"
	"errors"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
)

// SQLiteMigrations holds the iam schema of the local development mode.
//
//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLiteAdapter is a Casbin persist.Adapter on SQLite (local development mode).
type SQLiteAdapter struct{ DB *sql.DB }

var _ persist.Adapter = (*SQLiteAdapter)(nil)

// LoadPolicy implements persist.Adapter.
func (a *SQLiteAdapter) LoadPolicy(m model.Model) error {
	rows, err := a.DB.Query(`SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v [7]string
		if err := rows.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6]); err != nil {
			return err
		}
		rule := v[:]
		for len(rule) > 1 && rule[len(rule)-1] == "" {
			rule = rule[:len(rule)-1]
		}
		if err := persist.LoadPolicyArray(rule, m); err != nil {
			return err
		}
	}
	return rows.Err()
}

// SavePolicy implements persist.Adapter.
func (a *SQLiteAdapter) SavePolicy(m model.Model) error {
	tx, err := a.DB.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.Exec(`DELETE FROM casbin_rule`); err != nil {
		return err
	}
	for _, sec := range []string{"p", "g"} {
		for ptype, ast := range m[sec] {
			for _, rule := range ast.Policy {
				if _, err := tx.Exec(`INSERT INTO casbin_rule (ptype, v0, v1, v2, v3, v4, v5) VALUES (?, ?, ?, ?, ?, ?, ?)`, line(ptype, rule)...); err != nil {
					return err
				}
			}
		}
	}
	return tx.Commit()
}

// AddPolicy implements persist.Adapter.
func (a *SQLiteAdapter) AddPolicy(_ string, ptype string, rule []string) error {
	_, err := a.DB.Exec(`INSERT INTO casbin_rule (ptype, v0, v1, v2, v3, v4, v5) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`, line(ptype, rule)...)
	return err
}

// RemovePolicy implements persist.Adapter.
func (a *SQLiteAdapter) RemovePolicy(_ string, ptype string, rule []string) error {
	_, err := a.DB.Exec(`DELETE FROM casbin_rule WHERE ptype = ? AND v0 = ? AND v1 = ? AND v2 = ? AND v3 = ? AND v4 = ? AND v5 = ?`, line(ptype, rule)...)
	return err
}

// RemoveFilteredPolicy implements persist.Adapter.
func (a *SQLiteAdapter) RemoveFilteredPolicy(string, string, int, ...string) error {
	return errors.New("filtered removal not supported")
}
