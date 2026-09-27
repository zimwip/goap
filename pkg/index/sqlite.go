package index

import (
	"context"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/zimwip/goap/pkg/domain"
)

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// SQLite is the Store of the local mode: FTS5 for text, exact cosine scan for vectors.
type SQLite struct{ db *sql.DB }

// NewSQLite returns a store on an opened, migrated database (component "index").
func NewSQLite(db *sql.DB) *SQLite { return &SQLite{db: db} }

func (s *SQLite) Upsert(ctx context.Context, d Doc) error {
	facets, _ := json.Marshal(d.Facets)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var rowid int64
	err = tx.QueryRowContext(ctx, `SELECT rowid FROM node_index WHERE node_id = ? AND version = ?`, string(d.ID), int(d.Version)).Scan(&rowid)
	switch err {
	case nil:
		_, err = tx.ExecContext(ctx, `UPDATE node_index SET namespace=?, type=?, key=?, state=?, branch=?, main = main OR ?, deleted=?, facets=?, doc=?, embedding = COALESCE(?, CASE WHEN hash = ? THEN embedding END), hash=?, updated=? WHERE rowid=?`,
			d.Namespace, d.Type, d.Key, d.State, d.Branch, d.Main, d.Deleted, string(facets), d.Text, packVec(d.Embedding), d.Hash, d.Hash, ts(d.Time), rowid)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM node_fts WHERE rowid = ?`, rowid)
		}
	case sql.ErrNoRows:
		var res sql.Result
		res, err = tx.ExecContext(ctx, `INSERT INTO node_index (node_id, version, namespace, type, key, state, branch, main, deleted, facets, doc, hash, embedding, updated)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, string(d.ID), int(d.Version), d.Namespace, d.Type, d.Key, d.State, d.Branch, d.Main, d.Deleted, string(facets), d.Text, d.Hash, packVec(d.Embedding), ts(d.Time))
		if err == nil {
			rowid, err = res.LastInsertId()
		}
	}
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO node_fts (rowid, doc) VALUES (?, ?)`, rowid, d.Text); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Hash(ctx context.Context, id domain.NodeID, v domain.Version) (hash string, embedded, found bool, err error) {
	var emb []byte
	err = s.db.QueryRowContext(ctx, `SELECT hash, embedding FROM node_index WHERE node_id = ? AND version = ?`, string(id), int(v)).Scan(&hash, &emb)
	if err == sql.ErrNoRows {
		return "", false, false, nil
	}
	return hash, len(emb) > 0, err == nil, err
}

func (s *SQLite) SetMain(ctx context.Context, set map[domain.NodeID]domain.Version, removed []domain.NodeID) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, v := range set {
		if _, err := tx.ExecContext(ctx, `UPDATE node_index SET main = (version = ?) WHERE node_id = ?`, int(v), string(id)); err != nil {
			return err
		}
	}
	for _, id := range removed {
		if _, err := tx.ExecContext(ctx, `UPDATE node_index SET main = 0 WHERE node_id = ?`, string(id)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) Reset(ctx context.Context) error {
	for _, q := range []string{`DELETE FROM node_fts`, `DELETE FROM node_index`} {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

const hitCols = `i.node_id, i.version, i.namespace, i.type, i.key, i.state, i.branch, i.main, i.facets`

// where renders the filter as SQL conditions on alias i.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		conds = append(conds, col+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("i.namespace", f.Namespace)
	in("i.type", f.Type)
	in("i.state", f.State)
	in("i.branch", f.Branch)
	if f.Main != nil {
		conds = append(conds, "i.main = ?")
		args = append(args, *f.Main)
	}
	names := make([]string, 0, len(f.Facets))
	for n := range f.Facets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		vals := f.Facets[n]
		if len(vals) == 0 {
			continue
		}
		conds = append(conds, "json_extract(i.facets, ?) IN ("+strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",")+")")
		args = append(args, `$."`+strings.ReplaceAll(n, `"`, ``)+`"`)
		for _, v := range vals {
			args = append(args, v)
		}
	}
	if len(conds) == 0 {
		return "1=1", nil
	}
	return strings.Join(conds, " AND "), args
}

func scanHit(r interface{ Scan(...any) error }) (Hit, error) {
	var h Hit
	var id, facets string
	var v int
	if err := r.Scan(&id, &v, &h.Namespace, &h.Type, &h.Key, &h.State, &h.Branch, &h.Main, &facets); err != nil {
		return h, err
	}
	h.ID, h.Version = domain.NodeID(id), domain.Version(v)
	_ = json.Unmarshal([]byte(facets), &h.Facets)
	if len(h.Facets) == 0 {
		h.Facets = nil
	}
	return h, nil
}

func (s *SQLite) query(ctx context.Context, q string, args []any) ([]Hit, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		h, err := scanHit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (s *SQLite) List(ctx context.Context, f Filter, limit int) ([]Hit, error) {
	w, args := f.where()
	return s.query(ctx, `SELECT `+hitCols+` FROM node_index i WHERE `+w+` ORDER BY i.key, i.version LIMIT ?`, append(args, limit))
}

// ftsQuery turns free text into an AND of prefix terms, quoted so that FTS5 syntax in the input is inert.
func ftsQuery(text string) string {
	toks := Tokens(text)
	for i, t := range toks {
		toks[i] = `"` + t + `"*`
	}
	return strings.Join(toks, " ")
}

func (s *SQLite) FullText(ctx context.Context, text string, f Filter, limit int) ([]Hit, error) {
	m := ftsQuery(text)
	if m == "" {
		return nil, nil
	}
	w, args := f.where()
	return s.query(ctx, `SELECT `+hitCols+` FROM node_fts JOIN node_index i ON i.rowid = node_fts.rowid
		WHERE node_fts MATCH ? AND `+w+` ORDER BY bm25(node_fts) LIMIT ?`, append(append([]any{m}, args...), limit))
}

func (s *SQLite) Nearest(ctx context.Context, vec []float32, f Filter, limit int) ([]Hit, error) {
	w, args := f.where()
	rows, err := s.db.QueryContext(ctx, `SELECT `+hitCols+`, i.embedding FROM node_index i WHERE i.embedding IS NOT NULL AND `+w, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var id, facets string
		var v int
		var emb []byte
		if err := rows.Scan(&id, &v, &h.Namespace, &h.Type, &h.Key, &h.State, &h.Branch, &h.Main, &facets, &emb); err != nil {
			return nil, err
		}
		h.ID, h.Version = domain.NodeID(id), domain.Version(v)
		_ = json.Unmarshal([]byte(facets), &h.Facets)
		h.Score = cosine(vec, unpackVec(emb))
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func ts(t time.Time) string {
	if t.IsZero() {
		t = time.Now().UTC()
	}
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

func packVec(v []float32) []byte {
	if v == nil {
		return nil
	}
	b := make([]byte, 4*len(v))
	for i, x := range v {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return b
}

func unpackVec(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return v
}
