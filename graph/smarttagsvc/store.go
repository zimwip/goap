// Package smarttagsvc keeps the smart tags (ADR 0143 §7): shared selections of node versions, of any nodes and
// namespaces, that give people a context (the versions a review, a release or an investigation is about). A smart tag
// is data of its own, like the preferences (ADR 0038): never a state of the graph, no change, no journal; adding or
// removing a version is an edit of the smart tag, not a change of the nodes. Its owner shares it with users by
// subject, to read or to edit. Smart tags form trees (ADR 0178): a tag may have a parent, the root carrying the access
// of its tree; an entry may name a node without its version (followed in the reader's view); a tree may belong to a
// project, whose members read and edit it: the directory of the project.
package smarttagsvc

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/contracts/domain"
)

// Migrations holds the PostgreSQL schema of the smart tags; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// Accesses of a share, and of the owner.
const (
	AccessRead  = "read"
	AccessEdit  = "edit"
	AccessOwner = "owner"
)

// SmartTag is a named selection of node versions: an exact version, or version 0 for the node itself (ADR 0178).
type SmartTag struct {
	ID          string
	Name        string
	Description string
	// Owner is the subject who created it: the owner of a root shares and deletes it.
	Owner string
	// Parent is the tag it is a sub-tag of ("": a root, whose owner and shares are the access of its tree).
	Parent string
	// Project is the project the tree belongs to ("": none): its members read and edit it.
	Project  string
	Versions []domain.NodeRef
	Shares   []Share
	// Access is the access of the caller it was read for (set by the Service, never stored).
	Access string
	// By is who last changed it.
	By        string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Share grants a user access to a smart tag.
type Share struct {
	Subject string
	// Access is AccessRead or AccessEdit.
	Access string
}

// Selects reports whether the smart tag selects node: a version of it, or the node itself.
func (t SmartTag) Selects(node domain.NodeID) bool {
	return slices.ContainsFunc(t.Versions, func(r domain.NodeRef) bool { return r.ID == node })
}

// AccessOf is the access subject has to the smart tag by its owner and its shares, "" for none (for a sub-tag, the
// access of its tree is its root's: Service).
func (t SmartTag) AccessOf(subject string) string {
	if subject == t.Owner {
		return AccessOwner
	}
	for _, s := range t.Shares {
		if s.Subject == subject {
			return s.Access
		}
	}
	return ""
}

// ErrNotFound is returned for a smart tag that does not exist, or that the caller may not read.
var ErrNotFound = errors.New("not found")

// Store keeps the smart tags. The lists are by name.
type Store interface {
	// Get returns a smart tag (ErrNotFound when absent).
	Get(ctx context.Context, id string) (SmartTag, error)
	// List returns the smart tags subject owns or that are shared with them.
	List(ctx context.Context, subject string) ([]SmartTag, error)
	// Selecting returns the smart tags selecting node (a version of it, or the node itself), whoever reads them.
	Selecting(ctx context.Context, node domain.NodeID) ([]SmartTag, error)
	// Children returns the sub-tags of the tags listed.
	Children(ctx context.Context, parents []string) ([]SmartTag, error)
	// InProject returns the smart tags of a project, every tree.
	InProject(ctx context.Context, project string) ([]SmartTag, error)
	// Create stores a new smart tag.
	Create(ctx context.Context, t SmartTag) error
	// Update reads a smart tag, lets fn change it and stores the result, atomically (an error of fn writes nothing).
	Update(ctx context.Context, id string, fn func(*SmartTag) error) (SmartTag, error)
	// Delete removes a smart tag (ErrNotFound when absent).
	Delete(ctx context.Context, id string) error
}

func clone(t SmartTag) SmartTag {
	t.Versions = slices.Clone(t.Versions)
	t.Shares = slices.Clone(t.Shares)
	return t
}

func sortTags(ts []SmartTag) {
	slices.SortFunc(ts, func(a, b SmartTag) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// MemoryStore is an in-memory Store (tests, GOAP_STORE=memory).
type MemoryStore struct {
	mu   sync.Mutex
	tags map[string]SmartTag
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore { return &MemoryStore{tags: map[string]SmartTag{}} }

func (s *MemoryStore) Get(_ context.Context, id string) (SmartTag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tags[id]
	if !ok {
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	return clone(t), nil
}

func (s *MemoryStore) where(keep func(SmartTag) bool) []SmartTag {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SmartTag
	for _, t := range s.tags {
		if keep(t) {
			out = append(out, clone(t))
		}
	}
	sortTags(out)
	return out
}

func (s *MemoryStore) List(_ context.Context, subject string) ([]SmartTag, error) {
	return s.where(func(t SmartTag) bool { return t.AccessOf(subject) != "" }), nil
}

func (s *MemoryStore) Selecting(_ context.Context, node domain.NodeID) ([]SmartTag, error) {
	return s.where(func(t SmartTag) bool { return t.Selects(node) }), nil
}

func (s *MemoryStore) Children(_ context.Context, parents []string) ([]SmartTag, error) {
	return s.where(func(t SmartTag) bool { return t.Parent != "" && slices.Contains(parents, t.Parent) }), nil
}

func (s *MemoryStore) InProject(_ context.Context, project string) ([]SmartTag, error) {
	return s.where(func(t SmartTag) bool { return project != "" && t.Project == project }), nil
}

func (s *MemoryStore) Create(_ context.Context, t SmartTag) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tags[t.ID]; ok {
		return fmt.Errorf("smart tag %s exists", t.ID)
	}
	s.tags[t.ID] = clone(t)
	return nil
}

func (s *MemoryStore) Update(_ context.Context, id string, fn func(*SmartTag) error) (SmartTag, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tags[id]
	if !ok {
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	t = clone(t)
	if err := fn(&t); err != nil {
		return SmartTag{}, err
	}
	s.tags[id] = clone(t)
	return t, nil
}

func (s *MemoryStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tags[id]; !ok {
		return fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	delete(s.tags, id)
	return nil
}

// SQLStore is the Store on database/sql, for SQLite (local mode) and PostgreSQL (through the pgx stdlib adapter,
// Dollar set), in the database of the graph.
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

func sqlTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// read reads the smart tags whose ids are listed, with their versions and shares.
func (s SQLStore) read(ctx context.Context, db querier, where string, args ...any) ([]SmartTag, error) {
	rows, err := db.QueryContext(ctx, s.q(`SELECT id, name, description, owner, parent, project, by, created_at, updated_at FROM smart_tag WHERE `+where), args...)
	if err != nil {
		return nil, err
	}
	var out []SmartTag
	byID := map[string]int{}
	for rows.Next() {
		var t SmartTag
		var created, updated string
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.Owner, &t.Parent, &t.Project, &t.By, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		t.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		t.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
		byID[t.ID] = len(out)
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}
	ids := make([]any, 0, len(out))
	ph := make([]string, 0, len(out))
	for _, t := range out {
		ids = append(ids, t.ID)
		ph = append(ph, "?")
	}
	in := `(` + strings.Join(ph, ", ") + `)`
	vrows, err := db.QueryContext(ctx, s.q(`SELECT tag_id, node_id, version FROM smart_tag_version WHERE tag_id IN `+in+` ORDER BY tag_id, node_id, version`), ids...)
	if err != nil {
		return nil, err
	}
	for vrows.Next() {
		var id string
		var r domain.NodeRef
		if err := vrows.Scan(&id, (*string)(&r.ID), (*int)(&r.Version)); err != nil {
			vrows.Close()
			return nil, err
		}
		out[byID[id]].Versions = append(out[byID[id]].Versions, r)
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, err
	}
	srows, err := db.QueryContext(ctx, s.q(`SELECT tag_id, subject, access FROM smart_tag_share WHERE tag_id IN `+in+` ORDER BY tag_id, subject`), ids...)
	if err != nil {
		return nil, err
	}
	defer srows.Close()
	for srows.Next() {
		var id string
		var sh Share
		if err := srows.Scan(&id, &sh.Subject, &sh.Access); err != nil {
			return nil, err
		}
		out[byID[id]].Shares = append(out[byID[id]].Shares, sh)
	}
	return out, srows.Err()
}

func (s SQLStore) Get(ctx context.Context, id string) (SmartTag, error) {
	ts, err := s.read(ctx, s.DB, `id = ?`, id)
	if err != nil {
		return SmartTag{}, err
	}
	if len(ts) == 0 {
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	return ts[0], nil
}

func (s SQLStore) list(ctx context.Context, where string, args ...any) ([]SmartTag, error) {
	ts, err := s.read(ctx, s.DB, where+` ORDER BY name, id`, args...)
	if err != nil {
		return nil, err
	}
	sortTags(ts)
	return ts, nil
}

func (s SQLStore) List(ctx context.Context, subject string) ([]SmartTag, error) {
	return s.list(ctx, `(owner = ? OR id IN (SELECT tag_id FROM smart_tag_share WHERE subject = ?))`, subject, subject)
}

func (s SQLStore) Selecting(ctx context.Context, node domain.NodeID) ([]SmartTag, error) {
	return s.list(ctx, `id IN (SELECT tag_id FROM smart_tag_version WHERE node_id = ?)`, string(node))
}

func (s SQLStore) Children(ctx context.Context, parents []string) ([]SmartTag, error) {
	if len(parents) == 0 {
		return nil, nil
	}
	args := make([]any, len(parents))
	for i, p := range parents {
		args[i] = p
	}
	return s.list(ctx, `parent <> '' AND parent IN (`+strings.TrimSuffix(strings.Repeat("?, ", len(parents)), ", ")+`)`, args...)
}

func (s SQLStore) InProject(ctx context.Context, project string) ([]SmartTag, error) {
	if project == "" {
		return nil, nil
	}
	return s.list(ctx, `project = ?`, project)
}

// write writes the selection and the shares of t, replacing those it had.
func (s SQLStore) write(ctx context.Context, tx *sql.Tx, t SmartTag) error {
	for _, q := range []string{`DELETE FROM smart_tag_version WHERE tag_id = ?`, `DELETE FROM smart_tag_share WHERE tag_id = ?`} {
		if _, err := tx.ExecContext(ctx, s.q(q), t.ID); err != nil {
			return err
		}
	}
	for _, r := range t.Versions {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO smart_tag_version (tag_id, node_id, version) VALUES (?, ?, ?)`), t.ID, string(r.ID), int(r.Version)); err != nil {
			return err
		}
	}
	for _, sh := range t.Shares {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO smart_tag_share (tag_id, subject, access) VALUES (?, ?, ?)`), t.ID, sh.Subject, sh.Access); err != nil {
			return err
		}
	}
	return nil
}

func (s SQLStore) Create(ctx context.Context, t SmartTag) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO smart_tag (id, name, description, owner, parent, project, by, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`),
		t.ID, t.Name, t.Description, t.Owner, t.Parent, t.Project, t.By, sqlTime(t.CreatedAt), sqlTime(t.UpdatedAt)); err != nil {
		return err
	}
	if err := s.write(ctx, tx, t); err != nil {
		return err
	}
	return tx.Commit()
}

func (s SQLStore) Update(ctx context.Context, id string, fn func(*SmartTag) error) (SmartTag, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return SmartTag{}, err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if s.Dollar {
		lock = " FOR UPDATE" // PostgreSQL: two editors of one smart tag do not lose each other's versions
	}
	ts, err := s.read(ctx, tx, `id = ?`+lock, id)
	if err != nil {
		return SmartTag{}, err
	}
	if len(ts) == 0 {
		return SmartTag{}, fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	t := ts[0]
	if err := fn(&t); err != nil {
		return SmartTag{}, err
	}
	if _, err := tx.ExecContext(ctx, s.q(`UPDATE smart_tag SET name = ?, description = ?, parent = ?, by = ?, updated_at = ? WHERE id = ?`),
		t.Name, t.Description, t.Parent, t.By, sqlTime(t.UpdatedAt), id); err != nil {
		return SmartTag{}, err
	}
	if err := s.write(ctx, tx, t); err != nil {
		return SmartTag{}, err
	}
	return t, tx.Commit()
}

func (s SQLStore) Delete(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// the selection and the shares go with it (ON DELETE CASCADE needs foreign keys on in SQLite: said explicitly)
	for _, q := range []string{`DELETE FROM smart_tag_version WHERE tag_id = ?`, `DELETE FROM smart_tag_share WHERE tag_id = ?`} {
		if _, err := tx.ExecContext(ctx, s.q(q), id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, s.q(`DELETE FROM smart_tag WHERE id = ?`), id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("smart tag %s: %w", id, ErrNotFound)
	}
	return tx.Commit()
}
