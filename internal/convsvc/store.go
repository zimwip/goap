// Package convsvc keeps the conversations of the users with the assistant outside the graph (ADR 0085): per subject,
// conversations holding ordered messages, read and written by their owner alone (the executor of the assistant, a
// platform service, appends and updates the assistant's messages).
package convsvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Migrations holds the PostgreSQL schema; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// Roles and statuses of a message.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"

	StatusPending = "pending"
	StatusDone    = "done"
	StatusError   = "error"
)

// Conversation is a thread of messages owned by one subject.
type Conversation struct {
	ID        string
	Subject   string
	Title     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Action is a structured UI action the assistant asked the web to run: {type, args, result?}.
type Action = map[string]any

// Message is one entry of a conversation.
type Message struct {
	ID             string
	ConversationID string
	// Seq is the position in the conversation, from 1.
	Seq     int
	Role    string
	Text    string
	Actions []Action
	// ProcessID is the engine run that produced an assistant message (optional).
	ProcessID string
	Status    string
	Error     string
	// Context is a short description of what the user was looking at when they wrote a user message (never the page
	// itself, never form contents).
	Context   string
	CreatedAt time.Time
}

// Cursor is the position after which a page of conversations starts (zero: the first page).
type Cursor struct {
	UpdatedAt time.Time
	ID        string
}

func (c Cursor) zero() bool { return c.ID == "" && c.UpdatedAt.IsZero() }

// Errors of a Store.
var (
	ErrNotFound = errors.New("not found")
	// ErrFull is returned when a limit given to the Store would be exceeded.
	ErrFull = errors.New("limit reached")
)

// Store keeps the conversations. It enforces no ownership: the Service does.
type Store interface {
	// Create adds a conversation, refusing it (ErrFull) when its subject already holds maxPerSubject of them.
	Create(ctx context.Context, c Conversation, maxPerSubject int) error
	Get(ctx context.Context, id string) (Conversation, error)
	// List returns the conversations of a subject, most recently updated first, after the cursor.
	List(ctx context.Context, subject string, after Cursor, limit int) ([]Conversation, error)
	Rename(ctx context.Context, id, title string, now time.Time) (Conversation, error)
	// Delete removes a conversation and its messages.
	Delete(ctx context.Context, id string) error
	// Messages returns the messages of a conversation in order.
	Messages(ctx context.Context, conversationID string) ([]Message, error)
	GetMessage(ctx context.Context, id string) (Message, error)
	// Append adds a message at the next position, refusing it (ErrFull) beyond maxMessages, and touches the
	// conversation.
	Append(ctx context.Context, m Message, maxMessages int) (Message, error)
	// UpdateMessage reads a message, lets fn change it, stores the result and touches the conversation, atomically.
	UpdateMessage(ctx context.Context, id string, now time.Time, fn func(Message) (Message, error)) (Message, error)
}

// micros is the stored form of a time.
func micros(t time.Time) int64 { return t.UnixMicro() }

func fromMicros(n int64) time.Time { return time.UnixMicro(n).UTC() }

// admits reports whether c comes after the cursor in the listing order (newest first).
func (cur Cursor) admits(c Conversation) bool {
	if cur.zero() {
		return true
	}
	u, v := micros(c.UpdatedAt), micros(cur.UpdatedAt)
	return u < v || (u == v && c.ID < cur.ID)
}

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu    sync.Mutex
	convs map[string]Conversation
	msgs  map[string][]Message // conversation id -> messages in order
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{convs: map[string]Conversation{}, msgs: map[string][]Message{}}
}

func cloneMessage(m Message) Message {
	if m.Actions != nil {
		b, _ := json.Marshal(m.Actions)
		var out []Action
		_ = json.Unmarshal(b, &out)
		m.Actions = out
	}
	return m
}

func (s *MemoryStore) Create(_ context.Context, c Conversation, maxPerSubject int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, o := range s.convs {
		if o.Subject == c.Subject {
			n++
		}
	}
	if n >= maxPerSubject {
		return ErrFull
	}
	s.convs[c.ID] = c
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[id]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	return c, nil
}

func (s *MemoryStore) List(_ context.Context, subject string, after Cursor, limit int) ([]Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Conversation
	for _, c := range s.convs {
		if c.Subject == subject && after.admits(c) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID > out[j].ID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemoryStore) Rename(_ context.Context, id, title string, now time.Time) (Conversation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[id]
	if !ok {
		return Conversation{}, ErrNotFound
	}
	c.Title, c.UpdatedAt = title, now
	s.convs[id] = c
	return c, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.convs, id)
	delete(s.msgs, id)
	return nil
}

func (s *MemoryStore) Messages(_ context.Context, id string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Message, 0, len(s.msgs[id]))
	for _, m := range s.msgs[id] {
		out = append(out, cloneMessage(m))
	}
	return out, nil
}

func (s *MemoryStore) find(id string) (string, int, bool) {
	for cid, ms := range s.msgs {
		for i, m := range ms {
			if m.ID == id {
				return cid, i, true
			}
		}
	}
	return "", 0, false
}

func (s *MemoryStore) GetMessage(_ context.Context, id string) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cid, i, ok := s.find(id)
	if !ok {
		return Message{}, ErrNotFound
	}
	return cloneMessage(s.msgs[cid][i]), nil
}

func (s *MemoryStore) Append(_ context.Context, m Message, maxMessages int) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.convs[m.ConversationID]
	if !ok {
		return Message{}, ErrNotFound
	}
	if len(s.msgs[c.ID]) >= maxMessages {
		return Message{}, ErrFull
	}
	m.Seq = len(s.msgs[c.ID]) + 1
	m = cloneMessage(m)
	s.msgs[c.ID] = append(s.msgs[c.ID], m)
	c.UpdatedAt = m.CreatedAt
	s.convs[c.ID] = c
	return cloneMessage(m), nil
}

func (s *MemoryStore) UpdateMessage(_ context.Context, id string, now time.Time, fn func(Message) (Message, error)) (Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cid, i, ok := s.find(id)
	if !ok {
		return Message{}, ErrNotFound
	}
	out, err := fn(cloneMessage(s.msgs[cid][i]))
	if err != nil {
		return Message{}, err
	}
	s.msgs[cid][i] = cloneMessage(out)
	c := s.convs[cid]
	c.UpdatedAt = now
	s.convs[cid] = c
	return cloneMessage(out), nil
}

// SQLStore is the Store on database/sql, for SQLite (local mode) and PostgreSQL (through the pgx stdlib adapter,
// Dollar set).
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

// lock is the row lock of a PostgreSQL read-modify-write (SQLite serializes its writers).
func (s SQLStore) lock() string {
	if s.Dollar {
		return " FOR UPDATE"
	}
	return ""
}

const convCols = `id, subject, title, created_at, updated_at`

type scanner interface{ Scan(...any) error }

func scanConv(r scanner) (Conversation, error) {
	var c Conversation
	var created, updated int64
	if err := r.Scan(&c.ID, &c.Subject, &c.Title, &created, &updated); err != nil {
		return Conversation{}, err
	}
	c.CreatedAt, c.UpdatedAt = fromMicros(created), fromMicros(updated)
	return c, nil
}

const msgCols = `id, conversation_id, seq, role, body, actions, process_id, status, error, context, created_at`

func scanMsg(r scanner) (Message, error) {
	var m Message
	var actions string
	var created int64
	if err := r.Scan(&m.ID, &m.ConversationID, &m.Seq, &m.Role, &m.Text, &actions, &m.ProcessID, &m.Status, &m.Error, &m.Context, &created); err != nil {
		return Message{}, err
	}
	if actions != "" {
		if err := json.Unmarshal([]byte(actions), &m.Actions); err != nil {
			return Message{}, fmt.Errorf("stored actions: %w", err)
		}
	}
	m.CreatedAt = fromMicros(created)
	return m, nil
}

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func (s SQLStore) Create(ctx context.Context, c Conversation, maxPerSubject int) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var n int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT count(*) FROM conversation WHERE subject = ?`), c.Subject).Scan(&n); err != nil {
		return err
	}
	if n >= maxPerSubject {
		return ErrFull
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO conversation (`+convCols+`) VALUES (?, ?, ?, ?, ?)`),
		c.ID, c.Subject, c.Title, micros(c.CreatedAt), micros(c.UpdatedAt)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s SQLStore) Get(ctx context.Context, id string) (Conversation, error) {
	c, err := scanConv(s.DB.QueryRowContext(ctx, s.q(`SELECT `+convCols+` FROM conversation WHERE id = ?`), id))
	return c, notFound(err)
}

func (s SQLStore) List(ctx context.Context, subject string, after Cursor, limit int) ([]Conversation, error) {
	query, args := `SELECT `+convCols+` FROM conversation WHERE subject = ?`, []any{subject}
	if !after.zero() {
		u := micros(after.UpdatedAt)
		query += ` AND (updated_at < ? OR (updated_at = ? AND id < ?))`
		args = append(args, u, u, after.ID)
	}
	query += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, s.q(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Conversation
	for rows.Next() {
		c, err := scanConv(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s SQLStore) Rename(ctx context.Context, id, title string, now time.Time) (Conversation, error) {
	res, err := s.DB.ExecContext(ctx, s.q(`UPDATE conversation SET title = ?, updated_at = ? WHERE id = ?`), title, micros(now), id)
	if err != nil {
		return Conversation{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Conversation{}, ErrNotFound
	}
	return s.Get(ctx, id)
}

func (s SQLStore) Delete(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// explicit: SQLite enforces foreign keys only when asked to
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM conversation_message WHERE conversation_id = ?`), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM conversation WHERE id = ?`), id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s SQLStore) Messages(ctx context.Context, conversationID string) ([]Message, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT `+msgCols+` FROM conversation_message WHERE conversation_id = ? ORDER BY seq`), conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m, err := scanMsg(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s SQLStore) GetMessage(ctx context.Context, id string) (Message, error) {
	m, err := scanMsg(s.DB.QueryRowContext(ctx, s.q(`SELECT `+msgCols+` FROM conversation_message WHERE id = ?`), id))
	return m, notFound(err)
}

func encodeActions(a []Action) (string, error) {
	if a == nil {
		return "[]", nil
	}
	b, err := json.Marshal(a)
	return string(b), err
}

func (s SQLStore) Append(ctx context.Context, m Message, maxMessages int) (Message, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// the conversation row is the lock of its messages: two appenders do not take the same position
	var id string
	if err := tx.QueryRowContext(ctx, s.q(`SELECT id FROM conversation WHERE id = ?`+s.lock()), m.ConversationID).Scan(&id); err != nil {
		return Message{}, notFound(err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, s.q(`SELECT count(*) FROM conversation_message WHERE conversation_id = ?`), id).Scan(&n); err != nil {
		return Message{}, err
	}
	if n >= maxMessages {
		return Message{}, ErrFull
	}
	m.Seq = n + 1
	actions, err := encodeActions(m.Actions)
	if err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO conversation_message (`+msgCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		m.ID, m.ConversationID, m.Seq, m.Role, m.Text, actions, m.ProcessID, m.Status, m.Error, m.Context, micros(m.CreatedAt)); err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE conversation SET updated_at = ? WHERE id = ?`), micros(m.CreatedAt), id); err != nil {
		return Message{}, err
	}
	return m, tx.Commit()
}

func (s SQLStore) UpdateMessage(ctx context.Context, id string, now time.Time, fn func(Message) (Message, error)) (Message, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := scanMsg(tx.QueryRowContext(ctx, s.q(`SELECT `+msgCols+` FROM conversation_message WHERE id = ?`+s.lock()), id))
	if err != nil {
		return Message{}, notFound(err)
	}
	out, err := fn(cur)
	if err != nil {
		return Message{}, err
	}
	actions, err := encodeActions(out.Actions)
	if err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE conversation_message SET body = ?, actions = ?, process_id = ?, status = ?, error = ? WHERE id = ?`),
		out.Text, actions, out.ProcessID, out.Status, out.Error, id); err != nil {
		return Message{}, err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE conversation SET updated_at = ? WHERE id = ?`), micros(now), cur.ConversationID); err != nil {
		return Message{}, err
	}
	return out, tx.Commit()
}
