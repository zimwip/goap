package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// The outbox table of a service (both dialects, in its own migrations; services sharing a database, as in the local
// mode, name theirs apart: SQL.Table):
//
//	CREATE TABLE event_outbox (
//	    seq          bigserial   PRIMARY KEY,   -- INTEGER PRIMARY KEY AUTOINCREMENT
//	    subject      text        NOT NULL,
//	    payload      jsonb       NOT NULL,      -- text
//	    headers      jsonb       NOT NULL DEFAULT '{}',
//	    created_at   timestamptz NOT NULL,      -- text, SQLiteTime
//	    published_at timestamptz                -- text
//	);
//	CREATE INDEX event_outbox_pending ON event_outbox (seq) WHERE published_at IS NULL;

// SQLiteTime is the layout of the timestamps of the SQLite outbox: fixed width, so text order is time order.
const SQLiteTime = "2006-01-02T15:04:05.000000000Z"

// Execer is a transaction (or a database) of database/sql.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// SQL is the outbox of a database/sql database; Postgres selects the dialect (SQLite otherwise), Table names the
// outbox table (event_outbox by default).
type SQL struct {
	DB       *sql.DB
	Postgres bool
	Table    string
}

func (s SQL) q(query string) string {
	t := s.Table
	if t == "" {
		t = "event_outbox"
	}
	return Rebind(strings.ReplaceAll(query, "event_outbox", t), s.Postgres)
}

// Rebind turns the ? placeholders of a query into $n for PostgreSQL.
func Rebind(query string, postgres bool) string {
	if !postgres {
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

func (s SQL) ts(t time.Time) any {
	if s.Postgres {
		return t.UTC()
	}
	return t.UTC().Format(SQLiteTime)
}

// Append writes the messages in the outbox, in the caller's transaction.
func (s SQL) Append(ctx context.Context, tx Execer, msgs ...Message) error {
	for _, m := range msgs {
		h, err := json.Marshal(m.Headers)
		if err != nil {
			return err
		}
		if m.Headers == nil {
			h = []byte("{}")
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO event_outbox (subject, payload, headers, created_at) VALUES (?, ?, ?, ?)`),
			m.Subject, string(m.Data), string(h), s.ts(m.CreatedAt)); err != nil {
			return fmt.Errorf("outbox: %w", err)
		}
	}
	return nil
}

// Pending implements Store.
func (s SQL) Pending(ctx context.Context, limit int) ([]Message, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT seq, subject, payload, headers, created_at FROM event_outbox
		WHERE published_at IS NULL ORDER BY seq LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var data, headers string
		var created any
		if err := rows.Scan(&m.Seq, &m.Subject, &data, &headers, &created); err != nil {
			return nil, err
		}
		m.Data = []byte(data)
		_ = json.Unmarshal([]byte(headers), &m.Headers)
		switch c := created.(type) {
		case time.Time:
			m.CreatedAt = c
		case string:
			m.CreatedAt, _ = time.Parse(SQLiteTime, c)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// MarkPublished implements Store.
func (s SQL) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	if len(seqs) == 0 {
		return nil
	}
	args := []any{s.ts(at)}
	marks := make([]string, len(seqs))
	for i, q := range seqs {
		marks[i], args = "?", append(args, q)
	}
	_, err := s.DB.ExecContext(ctx, s.q(`UPDATE event_outbox SET published_at = ? WHERE seq IN (`+strings.Join(marks, ", ")+`)`), args...)
	return err
}

// PurgePublished implements Store.
func (s SQL) PurgePublished(ctx context.Context, before time.Time) error {
	_, err := s.DB.ExecContext(ctx, s.q(`DELETE FROM event_outbox WHERE published_at IS NOT NULL AND published_at < ?`), s.ts(before))
	return err
}

// Memory is an outbox in memory, for the stores that keep their state there (tests): the store appends the messages
// of a transaction once it commits.
type Memory struct {
	mu   sync.Mutex
	seq  int64
	msgs []memMsg
}

type memMsg struct {
	m         Message
	published time.Time
}

func (o *Memory) lock()   { o.mu.Lock() }
func (o *Memory) unlock() { o.mu.Unlock() }

// Append stores the messages.
func (o *Memory) Append(msgs ...Message) {
	o.lock()
	defer o.unlock()
	for _, m := range msgs {
		o.seq++
		m.Seq = o.seq
		o.msgs = append(o.msgs, memMsg{m: m})
	}
}

// Pending implements Store.
func (o *Memory) Pending(_ context.Context, limit int) ([]Message, error) {
	o.lock()
	defer o.unlock()
	var out []Message
	for _, x := range o.msgs {
		if x.published.IsZero() {
			out = append(out, x.m)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

// MarkPublished implements Store.
func (o *Memory) MarkPublished(_ context.Context, seqs []int64, at time.Time) error {
	o.lock()
	defer o.unlock()
	set := map[int64]bool{}
	for _, q := range seqs {
		set[q] = true
	}
	for i := range o.msgs {
		if set[o.msgs[i].m.Seq] {
			o.msgs[i].published = at
		}
	}
	return nil
}

// PurgePublished implements Store.
func (o *Memory) PurgePublished(_ context.Context, before time.Time) error {
	o.lock()
	defer o.unlock()
	kept := o.msgs[:0]
	for _, x := range o.msgs {
		if x.published.IsZero() || !x.published.Before(before) {
			kept = append(kept, x)
		}
	}
	o.msgs = kept
	return nil
}
