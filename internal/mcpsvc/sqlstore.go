package mcpsvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
	"github.com/zimwip/goap/pkg/mcp"
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

func (s SQLStore) exec(ctx context.Context, query string, args ...any) (int64, error) {
	r, err := s.DB.ExecContext(ctx, s.q(query), args...)
	if err != nil {
		return 0, err
	}
	return r.RowsAffected()
}

func (s SQLStore) count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, s.q(query), args...).Scan(&n)
	return n, err
}

func js(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound(what)
	}
	return err
}

func (s SQLStore) SaveMcp(ctx context.Context, d mcp.Def) error {
	_, err := s.exec(ctx, `INSERT INTO mcp (name, description, tools) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET description = excluded.description, tools = excluded.tools`, d.Name, d.Description, js(d.Tools))
	return err
}

func scanMcp(r interface{ Scan(...any) error }) (mcp.Def, error) {
	var d mcp.Def
	var tools string
	if err := r.Scan(&d.Name, &d.Description, &tools); err != nil {
		return d, err
	}
	return d, json.Unmarshal([]byte(tools), &d.Tools)
}

func (s SQLStore) Mcp(ctx context.Context, name string) (mcp.Def, error) {
	d, err := scanMcp(s.DB.QueryRowContext(ctx, s.q(`SELECT name, description, tools FROM mcp WHERE name = ?`), name))
	return d, notFound(err, "mcp "+name)
}

func (s SQLStore) Mcps(ctx context.Context) ([]mcp.Def, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT name, description, tools FROM mcp ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mcp.Def
	for rows.Next() {
		d, err := scanMcp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s SQLStore) DeleteMcp(ctx context.Context, name string) error {
	if n, err := s.count(ctx, `SELECT COUNT(*) FROM mcp_adapter WHERE mcp = ?`, name); err != nil {
		return err
	} else if n > 0 {
		return errConflict("mcp " + name + " is implemented by adapters")
	}
	n, err := s.exec(ctx, `DELETE FROM mcp WHERE name = ?`, name)
	if err == nil && n == 0 {
		return errNotFound("mcp " + name)
	}
	return err
}

func (s SQLStore) SaveAdapter(ctx context.Context, a mcp.Adapter) error {
	if _, err := s.Mcp(ctx, a.MCP); err != nil {
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO mcp_adapter (mcp, connector, tools) VALUES (?, ?, ?)
		ON CONFLICT (mcp, connector) DO UPDATE SET tools = excluded.tools`, a.MCP, a.Connector, js(a.Tools))
	return err
}

func scanAdapter(r interface{ Scan(...any) error }) (mcp.Adapter, error) {
	var a mcp.Adapter
	var tools string
	if err := r.Scan(&a.MCP, &a.Connector, &tools); err != nil {
		return a, err
	}
	return a, json.Unmarshal([]byte(tools), &a.Tools)
}

func (s SQLStore) Adapter(ctx context.Context, m, c string) (mcp.Adapter, error) {
	a, err := scanAdapter(s.DB.QueryRowContext(ctx, s.q(`SELECT mcp, connector, tools FROM mcp_adapter WHERE mcp = ? AND connector = ?`), m, c))
	return a, notFound(err, "adapter "+m+"/"+c)
}

func (s SQLStore) Adapters(ctx context.Context, m string) ([]mcp.Adapter, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT mcp, connector, tools FROM mcp_adapter WHERE (? = '' OR mcp = ?) ORDER BY mcp, connector`), m, m)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mcp.Adapter
	for rows.Next() {
		a, err := scanAdapter(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s SQLStore) DeleteAdapter(ctx context.Context, m, c string) error {
	if n, err := s.count(ctx, `SELECT COUNT(*) FROM mcp_binding WHERE mcp = ? AND connector = ?`, m, c); err != nil {
		return err
	} else if n > 0 {
		return errConflict("adapter " + m + "/" + c + " is bound by an organization")
	}
	n, err := s.exec(ctx, `DELETE FROM mcp_adapter WHERE mcp = ? AND connector = ?`, m, c)
	if err == nil && n == 0 {
		return errNotFound("adapter " + m + "/" + c)
	}
	return err
}

func (s SQLStore) SaveBinding(ctx context.Context, b mcp.Binding) error {
	if _, err := s.Adapter(ctx, b.MCP, b.Connector); err != nil {
		return err
	}
	_, err := s.exec(ctx, `INSERT INTO mcp_binding (org_id, mcp, connector, config, secrets) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (org_id, mcp) DO UPDATE SET connector = excluded.connector, config = excluded.config, secrets = excluded.secrets`,
		b.OrgID, b.MCP, b.Connector, js(orEmpty(b.Config)), js(orEmptyS(b.Secrets)))
	return err
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyS(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func scanBinding(r interface{ Scan(...any) error }) (mcp.Binding, error) {
	var b mcp.Binding
	var cfg, sec string
	if err := r.Scan(&b.OrgID, &b.MCP, &b.Connector, &cfg, &sec); err != nil {
		return b, err
	}
	if err := json.Unmarshal([]byte(cfg), &b.Config); err != nil {
		return b, err
	}
	return b, json.Unmarshal([]byte(sec), &b.Secrets)
}

func (s SQLStore) Binding(ctx context.Context, org, m string) (mcp.Binding, error) {
	b, err := scanBinding(s.DB.QueryRowContext(ctx, s.q(`SELECT org_id, mcp, connector, config, secrets FROM mcp_binding WHERE org_id = ? AND mcp = ?`), org, m))
	return b, notFound(err, "binding "+org+"/"+m)
}

func (s SQLStore) Bindings(ctx context.Context, org string) ([]mcp.Binding, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT org_id, mcp, connector, config, secrets FROM mcp_binding WHERE (? = '' OR org_id = ?) ORDER BY org_id, mcp`), org, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mcp.Binding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s SQLStore) DeleteBinding(ctx context.Context, org, m string) error {
	n, err := s.exec(ctx, `DELETE FROM mcp_binding WHERE org_id = ? AND mcp = ?`, org, m)
	if err == nil && n == 0 {
		return errNotFound("binding " + org + "/" + m)
	}
	return err
}

func (s SQLStore) SaveConnector(ctx context.Context, r ConnectorReg) error {
	info, err := protojson.Marshal(r.Info)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `INSERT INTO connector (id, endpoint, info, last_seen_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET endpoint = excluded.endpoint, info = excluded.info, last_seen_ms = excluded.last_seen_ms`,
		r.Info.Id, r.Endpoint, string(info), r.LastSeen.UnixMilli())
	return err
}

func scanConnector(r interface{ Scan(...any) error }) (ConnectorReg, error) {
	var c ConnectorReg
	var info string
	var ms int64
	if err := r.Scan(&c.Endpoint, &info, &ms); err != nil {
		return c, err
	}
	c.Info = &connectorv1.ConnectorInfo{}
	c.LastSeen = time.UnixMilli(ms).UTC()
	return c, protojson.Unmarshal([]byte(info), c.Info)
}

func (s SQLStore) Connector(ctx context.Context, id string) (ConnectorReg, error) {
	c, err := scanConnector(s.DB.QueryRowContext(ctx, s.q(`SELECT endpoint, info, last_seen_ms FROM connector WHERE id = ?`), id))
	return c, notFound(err, "connector "+id)
}

func (s SQLStore) Connectors(ctx context.Context) ([]ConnectorReg, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT endpoint, info, last_seen_ms FROM connector ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConnectorReg
	for rows.Next() {
		c, err := scanConnector(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
