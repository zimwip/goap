// Package iamsvc is the IAM service: ABAC policies (Casbin) stored in
// PostgreSQL and permission checks for the other services.
package iamsvc

import (
	"context"
	"embed"
	"errors"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrations holds the iam schema.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Adapter is a Casbin persist.Adapter on PostgreSQL (table casbin_rule).
type Adapter struct{ Pool *pgxpool.Pool }

var _ persist.Adapter = (*Adapter)(nil)

func line(ptype string, rule []string) []any {
	out := []any{ptype, "", "", "", "", "", ""}
	for i, v := range rule {
		if i < 6 {
			out[i+1] = v
		}
	}
	return out
}

// LoadPolicy implements persist.Adapter.
func (a *Adapter) LoadPolicy(m model.Model) error {
	rows, err := a.Pool.Query(context.Background(), `SELECT ptype, v0, v1, v2, v3, v4, v5 FROM casbin_rule ORDER BY id`)
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
func (a *Adapter) SavePolicy(m model.Model) error {
	ctx := context.Background()
	return pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM casbin_rule`); err != nil {
			return err
		}
		for _, sec := range []string{"p", "g"} {
			for ptype, ast := range m[sec] {
				for _, rule := range ast.Policy {
					if _, err := tx.Exec(ctx, `INSERT INTO casbin_rule (ptype, v0, v1, v2, v3, v4, v5) VALUES ($1, $2, $3, $4, $5, $6, $7)`, line(ptype, rule)...); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
}

// AddPolicy implements persist.Adapter.
func (a *Adapter) AddPolicy(_ string, ptype string, rule []string) error {
	_, err := a.Pool.Exec(context.Background(), `INSERT INTO casbin_rule (ptype, v0, v1, v2, v3, v4, v5) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT DO NOTHING`, line(ptype, rule)...)
	return err
}

// RemovePolicy implements persist.Adapter.
func (a *Adapter) RemovePolicy(_ string, ptype string, rule []string) error {
	_, err := a.Pool.Exec(context.Background(), `DELETE FROM casbin_rule WHERE ptype = $1 AND v0 = $2 AND v1 = $3 AND v2 = $4 AND v3 = $5 AND v4 = $6 AND v5 = $7`,
		line(ptype, rule)...)
	return err
}

// RemoveFilteredPolicy implements persist.Adapter.
func (a *Adapter) RemoveFilteredPolicy(string, string, int, ...string) error {
	return errors.New("filtered removal not supported")
}
