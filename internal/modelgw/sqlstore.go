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

func (s SQLStore) exec(ctx context.Context, query string, args ...any) error {
	_, err := s.DB.ExecContext(ctx, s.q(query), args...)
	return err
}

const providerCols = `name, kind, protocol, base_url, enabled, api_key_enc, key_hint`

func scanProvider(r interface{ Scan(...any) error }) (ProviderRecord, error) {
	var p ProviderRecord
	err := r.Scan(&p.Name, &p.Kind, &p.Protocol, &p.BaseURL, &p.Enabled, &p.KeyEnc, &p.KeyHint)
	return p, err
}

func (s SQLStore) ListProviders(ctx context.Context) ([]ProviderRecord, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+providerCols+` FROM llm_provider ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProviderRecord
	for rows.Next() {
		p, err := scanProvider(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s SQLStore) GetProvider(ctx context.Context, name string) (ProviderRecord, bool, error) {
	p, err := scanProvider(s.DB.QueryRowContext(ctx, s.q(`SELECT `+providerCols+` FROM llm_provider WHERE name = ?`), name))
	if err == sql.ErrNoRows {
		return ProviderRecord{}, false, nil
	}
	return p, err == nil, err
}

func (s SQLStore) SaveProvider(ctx context.Context, p ProviderRecord) error {
	return s.exec(ctx, `INSERT INTO llm_provider (`+providerCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET kind = excluded.kind, protocol = excluded.protocol, base_url = excluded.base_url,
		enabled = excluded.enabled, api_key_enc = excluded.api_key_enc, key_hint = excluded.key_hint`,
		p.Name, p.Kind, p.Protocol, p.BaseURL, p.Enabled, p.KeyEnc, p.KeyHint)
}

func (s SQLStore) DeleteProvider(ctx context.Context, name string) error {
	// explicit, so that it does not depend on the foreign_keys pragma
	if err := s.exec(ctx, `DELETE FROM llm_model WHERE provider = ?`, name); err != nil {
		return err
	}
	return s.exec(ctx, `DELETE FROM llm_provider WHERE name = ?`, name)
}

const modelCols = `provider, model, display_name, enabled, quota_tokens, quota_period, roles`

func scanModel(r interface{ Scan(...any) error }) (ModelEntry, error) {
	var m ModelEntry
	var roles string
	err := r.Scan(&m.Provider, &m.Model, &m.DisplayName, &m.Enabled, &m.QuotaTokens, &m.QuotaPeriod, &roles)
	m.Roles = splitRoles(roles)
	return m, err
}

func (s SQLStore) ListModels(ctx context.Context) ([]ModelEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT `+modelCols+` FROM llm_model ORDER BY provider, model`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ModelEntry
	for rows.Next() {
		m, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s SQLStore) GetModel(ctx context.Context, provider, model string) (ModelEntry, bool, error) {
	m, err := scanModel(s.DB.QueryRowContext(ctx, s.q(`SELECT `+modelCols+` FROM llm_model WHERE provider = ? AND model = ?`), provider, model))
	if err == sql.ErrNoRows {
		return ModelEntry{}, false, nil
	}
	return m, err == nil, err
}

func (s SQLStore) SaveModel(ctx context.Context, m ModelEntry) error {
	return s.exec(ctx, `INSERT INTO llm_model (`+modelCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (provider, model) DO UPDATE SET display_name = excluded.display_name, enabled = excluded.enabled,
		quota_tokens = excluded.quota_tokens, quota_period = excluded.quota_period, roles = excluded.roles`,
		m.Provider, m.Model, m.DisplayName, m.Enabled, m.QuotaTokens, m.QuotaPeriod, joinRoles(m.Roles))
}

func (s SQLStore) DeleteModel(ctx context.Context, provider, model string) error {
	return s.exec(ctx, `DELETE FROM llm_model WHERE provider = ? AND model = ?`, provider, model)
}

func (s SQLStore) ListAliases(ctx context.Context) ([]AliasEntry, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT alias, target FROM llm_alias ORDER BY alias`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AliasEntry
	for rows.Next() {
		var a AliasEntry
		if err := rows.Scan(&a.Alias, &a.Target); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s SQLStore) SaveAlias(ctx context.Context, a AliasEntry) error {
	return s.exec(ctx, `INSERT INTO llm_alias (alias, target) VALUES (?, ?) ON CONFLICT (alias) DO UPDATE SET target = excluded.target`, a.Alias, a.Target)
}

func (s SQLStore) DeleteAlias(ctx context.Context, alias string) error {
	return s.exec(ctx, `DELETE FROM llm_alias WHERE alias = ?`, alias)
}

func (s SQLStore) Usage(ctx context.Context, provider, model, period string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT tokens FROM llm_usage WHERE provider = ? AND model = ? AND period = ?`), provider, model, period).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (s SQLStore) AddUsage(ctx context.Context, provider, model, period string, tokens int64) error {
	return s.exec(ctx, `INSERT INTO llm_usage (provider, model, period, tokens) VALUES (?, ?, ?, ?)
		ON CONFLICT (provider, model, period) DO UPDATE SET tokens = llm_usage.tokens + excluded.tokens`, provider, model, period, tokens)
}
