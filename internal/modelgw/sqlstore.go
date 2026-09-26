package modelgw

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
)

// Migrations holds the PostgreSQL schema; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLStore is the Store on database/sql, for SQLite (local mode) and
// PostgreSQL (through the pgx stdlib adapter, Dollar set).
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

func (s SQLStore) Usage(ctx context.Context, model, period string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT tokens FROM llm_usage WHERE model_key = ? AND period = ?`), model, period).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (s SQLStore) AddUsage(ctx context.Context, model, period string, tokens int64) error {
	_, err := s.DB.ExecContext(ctx, s.q(`INSERT INTO llm_usage (model_key, period, tokens) VALUES (?, ?, ?)
		ON CONFLICT (model_key, period) DO UPDATE SET tokens = llm_usage.tokens + excluded.tokens`), model, period, tokens)
	return err
}
