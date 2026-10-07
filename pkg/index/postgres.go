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
	_, err := p.pool.Exec(ctx, `INSERT INTO node_index (kind, node_id, version, namespace, type, key, state, branch, main, deleted, project, owner, status, methodology, parent, personal_to, title, facets, doc, hash, embedding)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21::public.vector)
		ON CONFLICT (kind, node_id, version) DO UPDATE SET namespace=EXCLUDED.namespace, type=EXCLUDED.type, key=EXCLUDED.key, state=EXCLUDED.state,
			branch=EXCLUDED.branch, main = node_index.main OR EXCLUDED.main, deleted=EXCLUDED.deleted, project=EXCLUDED.project, owner=EXCLUDED.owner,
			status=EXCLUDED.status, methodology=EXCLUDED.methodology, parent=EXCLUDED.parent, personal_to=EXCLUDED.personal_to, title=EXCLUDED.title,
			facets=EXCLUDED.facets, doc=EXCLUDED.doc,
			embedding = COALESCE(EXCLUDED.embedding, CASE WHEN node_index.hash = EXCLUDED.hash THEN node_index.embedding END),
			hash=EXCLUDED.hash, updated=now()`,
		kindOf(d.Kind), string(d.ID), int(d.Version), d.Namespace, d.Type, d.Key, d.State, d.Branch, d.Main, d.Deleted, d.Project, d.Owner, d.Status,
		d.Methodology, d.Parent, d.PersonalTo, d.Title, facets, d.Text, d.Hash, vecText(d.Embedding))
	return err
}

func (p *Postgres) Hash(ctx context.Context, kind string, id domain.NodeID, v domain.Version) (hash string, embedded, found bool, err error) {
	err = p.pool.QueryRow(ctx, `SELECT hash, embedding IS NOT NULL FROM node_index WHERE kind = $1 AND node_id = $2 AND version = $3`, kindOf(kind), string(id), int(v)).Scan(&hash, &embedded)
	if err == pgx.ErrNoRows {
		return "", false, false, nil
	}
	return hash, embedded, err == nil, err
}

func (p *Postgres) Delete(ctx context.Context, kind string, id domain.NodeID) error {
	_, err := p.pool.Exec(ctx, `DELETE FROM node_index WHERE kind = $1 AND node_id = $2`, kindOf(kind), string(id))
	return err
}

func (p *Postgres) Texts(ctx context.Context, refs []Ref) (map[Ref]string, error) {
	out := make(map[Ref]string, len(refs))
	for _, r := range refs {
		var doc string
		err := p.pool.QueryRow(ctx, `SELECT doc FROM node_index WHERE kind = $1 AND node_id = $2 AND version = $3`, kindOf(r.Kind), string(r.ID), int(r.Version)).Scan(&doc)
		if err == pgx.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[r] = doc
	}
	return out, nil
}

func (p *Postgres) Vector(ctx context.Context, kind string, id domain.NodeID) (Hit, []float32, bool, error) {
	hits, err := p.query(ctx, `SELECT `+pgCols+`, 0::float8 AS score FROM node_index WHERE kind = $1 AND node_id = $2 ORDER BY main DESC, version DESC LIMIT 1`, []any{kindOf(kind), string(id)}, true)
	if err != nil || len(hits) == 0 {
		return Hit{}, nil, false, err
	}
	var text *string
	if err := p.pool.QueryRow(ctx, `SELECT embedding::text FROM node_index WHERE kind = $1 AND node_id = $2 AND version = $3`, kindOf(kind), string(id), int(hits[0].Version)).Scan(&text); err != nil {
		return Hit{}, nil, false, err
	}
	if text == nil {
		return hits[0], nil, true, nil
	}
	vec, err := parseVec(*text)
	return hits[0], vec, err == nil, err
}

// parseVec reads the text form of a pgvector value, "[1,2,3]".
func parseVec(s string) ([]float32, error) {
	s = strings.TrimSuffix(strings.TrimPrefix(s, "["), "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, x := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

func (p *Postgres) SetMain(ctx context.Context, set map[domain.NodeID]domain.Version, removed []domain.NodeID) error {
	return pgx.BeginFunc(ctx, p.pool, func(tx pgx.Tx) error {
		for id, v := range set {
			if _, err := tx.Exec(ctx, `UPDATE node_index SET main = (version = $2) WHERE kind = 'node' AND node_id = $1 AND main <> (version = $2)`, string(id), int(v)); err != nil {
				return err
			}
		}
		for _, id := range removed {
			if _, err := tx.Exec(ctx, `UPDATE node_index SET main = false WHERE kind = 'node' AND node_id = $1 AND main`, string(id)); err != nil {
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

const pgCols = `kind, node_id, version, namespace, type, key, state, branch, main, deleted, project, owner, status, methodology, parent, personal_to, title, facets`

// pgWhere renders the filter with numbered placeholders appended to args.
func pgWhere(f Filter, args []any) (string, []any) {
	conds := []string{"true"}
	add := func(cond string, v any) {
		args = append(args, v)
		conds = append(conds, strings.ReplaceAll(cond, "$$", "$"+strconv.Itoa(len(args))))
	}
	if len(f.Kind) > 0 {
		add("kind = ANY($$)", f.Kind)
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
	if len(f.Project) > 0 {
		add("project = ANY($$)", f.Project)
	}
	if len(f.Owner) > 0 {
		add("owner = ANY($$)", f.Owner)
	}
	if len(f.Status) > 0 {
		add("status = ANY($$)", f.Status)
	}
	if len(f.Methodology) > 0 {
		add("methodology = ANY($$)", f.Methodology)
	}
	if f.RootsOnly {
		conds = append(conds, "kind = 'change' AND parent = ''")
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
		dest := []any{&h.Kind, &id, &v, &h.Namespace, &h.Type, &h.Key, &h.State, &h.Branch, &h.Main, &h.Deleted, &h.Project, &h.Owner, &h.Status,
			&h.Methodology, &h.Parent, &h.PersonalTo, &h.Title, &facets}
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
