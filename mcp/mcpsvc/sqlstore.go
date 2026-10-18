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

	connectorv1 "github.com/zimwip/goap/contracts/gen/goap/connector/v1"
	"github.com/zimwip/goap/lib/outbox"
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

var (
	_ Store        = SQLStore{}
	_ outbox.Store = SQLStore{}
)

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

func notFound(err error, what string) error {
	if errors.Is(err, sql.ErrNoRows) {
		return errNotFound(what)
	}
	return err
}

func (s SQLStore) SaveConnector(ctx context.Context, r ConnectorReg, events SaveEvents) error {
	info, err := protojson.Marshal(r.Info)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var msgs []outbox.Message
	if events != nil {
		lock := ""
		if s.Dollar {
			lock = " FOR UPDATE" // two renewals of one connector decide one after the other
		}
		prev, err := scanConnector(tx.QueryRowContext(ctx, s.q(`SELECT endpoint, info, last_seen_ms, lost_ms FROM connector WHERE id = ?`+lock), r.Info.Id))
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if msgs, err = events(prev, found); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO connector (id, endpoint, info, last_seen_ms, lost_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET endpoint = excluded.endpoint, info = excluded.info, last_seen_ms = excluded.last_seen_ms, lost_ms = excluded.lost_ms`),
		r.Info.Id, r.Endpoint, string(info), r.LastSeen.UnixMilli(), millis(r.LostAt)); err != nil {
		return err
	}
	if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s SQLStore) MarkLost(ctx context.Context, id string, lastSeen, at time.Time, msgs ...outbox.Message) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, s.q(`UPDATE connector SET lost_ms = ? WHERE id = ? AND last_seen_ms = ? AND lost_ms = 0`),
		max(at.UnixMilli(), 1), id, lastSeen.UnixMilli())
	if err != nil {
		return false, err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return false, err
	}
	if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (s SQLStore) outbox() outbox.SQL {
	return outbox.SQL{DB: s.DB, Postgres: s.Dollar, Table: "mcp_outbox"}
}

// Pending implements outbox.Store.
func (s SQLStore) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return s.outbox().Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s SQLStore) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return s.outbox().MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s SQLStore) PurgePublished(ctx context.Context, before time.Time) error {
	return s.outbox().PurgePublished(ctx, before)
}

// millis is t in milliseconds, 0 for the zero time.
func millis(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return max(t.UnixMilli(), 1)
}

func scanConnector(r interface{ Scan(...any) error }) (ConnectorReg, error) {
	var c ConnectorReg
	var info string
	var ms, lost int64
	if err := r.Scan(&c.Endpoint, &info, &ms, &lost); err != nil {
		return c, err
	}
	c.Info = &connectorv1.ConnectorInfo{}
	c.LastSeen = time.UnixMilli(ms).UTC()
	if lost != 0 {
		c.LostAt = time.UnixMilli(lost).UTC()
	}
	return c, protojson.Unmarshal([]byte(info), c.Info)
}

func (s SQLStore) Connector(ctx context.Context, id string) (ConnectorReg, error) {
	c, err := scanConnector(s.DB.QueryRowContext(ctx, s.q(`SELECT endpoint, info, last_seen_ms, lost_ms FROM connector WHERE id = ?`), id))
	return c, notFound(err, "connector "+id)
}

func (s SQLStore) Connectors(ctx context.Context) ([]ConnectorReg, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT endpoint, info, last_seen_ms, lost_ms FROM connector ORDER BY id`)
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
