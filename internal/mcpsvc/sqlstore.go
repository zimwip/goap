package mcpsvc

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	connectorv1 "github.com/zimwip/goap/gen/goap/connector/v1"
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

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound(what)
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
