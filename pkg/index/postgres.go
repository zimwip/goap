package index

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/pkg/domain"
)

// Migrations holds the PostgreSQL schema of the index service.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// Postgres is the production Store: tsvector for text, pgvector (HNSW, cosine) for embeddings.
type Postgres struct{ pool *pgxpool.Pool }

// NewPostgres returns a store on a migrated pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// EnsureVectorIndex creates the HNSW index for embeddings of the given dimension. The dimension
// belongs to the embedding model: changing the model means Reset, re-embedding and a new index.
func (p *Postgres) EnsureVectorIndex(ctx context.Context, dim int) error {
	if dim <= 0 {
		return fmt.Errorf("invalid embedding dimension %d", dim)
	}
	_, err := p.pool.Exec(ctx, fmt.Sprintf(`DROP INDEX IF EXISTS node_index_hnsw;
		CREATE INDEX node_index_hnsw ON node_index USING hnsw ((embedding::public.vector(%d)) public.vector_cosine_ops)`, dim))
	return err
}

func vecText(v []float32) *string {
	if v == nil {
		return nil
	}
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = strconv.FormatFloat(float64(x), 'g', -1, 32)
	}
	s := "[" + strings.Join(parts, ",") + "]"
	return &s
}

func (p *Postgres) Upsert(ctx context.Context, d Doc) error {
	facets, _ := json.Marshal(d.Facets)
	if d.Facets == nil {
		facets = []byte("{}")
	}
	_, err := p.pool.Exec(ctx, `INSERT INTO node_index (node_id, version, namespace, type, key, state, branch, main, deleted, facets, doc, hash, embedding)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::public.vector)
		ON CONFLICT (node_id, version) DO UPDATE SET namespace=EXCLUDED.namespace, type=EXCLUDED.type, key=EXCLUDED.key, state=EXCLUDED.state,
			branch=EXCLUDED.branch, main = node_index.main OR EXCLUDED.main, deleted=EXCLUDED.deleted, facets=EXCLUDED.facets, doc=EXCLUDED.doc,
			embedding = COALESCE(EXCLUDED.embedding, CASE WHEN node_index.hash = EXCLUDED.hash THEN node_index.embedding END),
			hash=EXCLUDED.hash, updated=now()`,
		string(d.ID), int(d.Version), d.Namespace, d.Type, d.Key, d.State, d.Branch, d.Main, d.Deleted, facets, d.Text, d.Hash, vecText(d.Embedding))
	return err
}

func (p *Postgres) Hash(ctx context.Context, id domain.NodeID, v domain.Version) (hash string, embedded, found bool, err error) {
	err = p.pool.QueryRow(ctx, `SELECT hash, embedding IS NOT NULL FROM node_index WHERE node_id = $1 AND version = $2`, string(id), int(v)).Scan(&hash, &embedded)
	if err == pgx.ErrNoRows {
		return "", false, false, nil
	}
	return hash, embedded, err == nil, err
}

func (p *Postgres) SetMain(ctx context.Context, set map[domain.NodeID]domain.Version, removed []domain.NodeID) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		for id, v := range set {
			if _, err := tx.Exec(ctx, `UPDATE node_index SET main = (version = $2) WHERE node_id = $1 AND main <> (version = $2)`, string(id), int(v)); err != nil {
				return err
			}
		}
		for _, id := range removed {
			if _, err := tx.Exec(ctx, `UPDATE node_index SET main = false WHERE node_id = $1 AND main`, string(id)); err != nil {
				return err
			}
		}
		return nil
	})
}

func (p *Postgres) Reset(ctx context.Context) error {
	_, err := p.pool.Exec(ctx, `TRUNCATE node_index`)
	return err
}

const pgCols = `node_id, version, namespace, type, key, state, branch, main, facets`

// pgWhere renders the filter with numbered placeholders appended to args.
func pgWhere(f Filter, args []any) (string, []any) {
	conds := []string{"true"}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$$", "$"+strconv.Itoa(len(args))))
	}
	if len(f.Namespace) > 0 {
		add("namespace = ANY($$)", f.Namespace)
	}
	if len(f.Type) > 0 {
		add("type = ANY($$)", f.Type)
	}
	if len(f.State) > 0 {
		add("state = ANY($$)", f.State)
	}
	if len(f.Branch) > 0 {
		add("branch = ANY($$)", f.Branch)
	}
	if f.Main != nil {
		add("main = $$", *f.Main)
	}
	for name, vals := range f.Facets {
		if len(vals) == 0 {
			continue
		}
		args = append(args, name)
		k := len(args)
		args = append(args, vals)
		conds = append(conds, fmt.Sprintf("facets->>$%d = ANY($%d)", k, k+1))
	}
	return strings.Join(conds, " AND "), args
}

func (p *Postgres) query(ctx context.Context, q string, args []any, withScore bool) ([]Hit, error) {
	rows, err := p.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var id string
		var v int32
		var facets map[string]string
		dest := []any{&id, &v, &h.Namespace, &h.Type, &h.Key, &h.State, &h.Branch, &h.Main, &facets}
		if withScore {
			dest = append(dest, &h.Score)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		h.ID, h.Version = domain.NodeID(id), domain.Version(v)
		if len(facets) > 0 {
			h.Facets = facets
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func (p *Postgres) List(ctx context.Context, f Filter, limit int) ([]Hit, error) {
	w, args := pgWhere(f, nil)
	args = append(args, limit)
	return p.query(ctx, `SELECT `+pgCols+` FROM node_index WHERE `+w+` ORDER BY key, version LIMIT $`+strconv.Itoa(len(args)), args, false)
}

func (p *Postgres) FullText(ctx context.Context, text string, f Filter, limit int) ([]Hit, error) {
	toks := Tokens(text)
	if len(toks) == 0 {
		return nil, nil
	}
	for i, t := range toks {
		toks[i] = t + ":*"
	}
	w, args := pgWhere(f, []any{strings.Join(toks, " & ")})
	args = append(args, limit)
	return p.query(ctx, `SELECT `+pgCols+` FROM node_index, to_tsquery('simple', $1) q WHERE tsv @@ q AND `+w+
		` ORDER BY ts_rank_cd(tsv, q) DESC, key LIMIT $`+strconv.Itoa(len(args)), args, false)
}

func (p *Postgres) Nearest(ctx context.Context, vec []float32, f Filter, limit int) ([]Hit, error) {
	w, args := pgWhere(f, []any{*vecText(vec)})
	args = append(args, limit)
	// the expression matches the HNSW index of EnsureVectorIndex
	dist := fmt.Sprintf(`embedding::public.vector(%d) OPERATOR(public.<=>) $1::public.vector(%d)`, len(vec), len(vec))
	return p.query(ctx, `SELECT `+pgCols+`, 1 - (`+dist+`) AS score FROM node_index WHERE embedding IS NOT NULL AND `+w+` ORDER BY `+dist+` LIMIT $`+strconv.Itoa(len(args)), args, true)
}
