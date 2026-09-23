package registrysvc

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/methodology"
)

// Migrations holds the registry schema.
//
//go:embed migrations/*.sql
var Migrations embed.FS

// PostgresStore stores methodologies in normalized tables (one table per
// section), so that they can be administered in the database.
type PostgresStore struct{ Pool *pgxpool.Pool }

func jsonOf(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (s PostgresStore) Save(ctx context.Context, r Record) error {
	m := r.Methodology
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var id string
		var status string
		err := tx.QueryRow(ctx, `SELECT id::text, status FROM methodology WHERE name = $1 AND version = $2 FOR UPDATE`, m.Name, m.Version).Scan(&id, &status)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			id = uuid.NewString()
			_, err = tx.Exec(ctx, `INSERT INTO methodology (id, name, version, description, status, created_at, updated_at, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $6, $7)`, id, m.Name, m.Version, m.Description, string(r.Status), r.UpdatedAt, r.UpdatedBy)
			if err != nil {
				return err
			}
		case err != nil:
			return err
		case Status(status) != StatusDraft:
			return fmt.Errorf("%s: %w", key(m.Name, m.Version), ErrImmutable)
		default:
			if _, err := tx.Exec(ctx, `UPDATE methodology SET description = $2, updated_at = $3, updated_by = $4 WHERE id = $1`,
				id, m.Description, r.UpdatedAt, r.UpdatedBy); err != nil {
				return err
			}
			for _, t := range []string{"methodology_node_type", "methodology_link_type", "methodology_condition", "methodology_action", "methodology_goal", "methodology_agent"} {
				if _, err := tx.Exec(ctx, `DELETE FROM `+t+` WHERE methodology_id = $1`, id); err != nil {
					return err
				}
			}
		}
		batch := &pgx.Batch{}
		for i, n := range m.Domain.NodeTypes {
			props := n.Properties
			if props == nil {
				props = []string{}
			}
			batch.Queue(`INSERT INTO methodology_node_type VALUES ($1, $2, $3, $4, $5)`, id, i, n.Name, n.Description, props)
		}
		for i, l := range m.Domain.LinkTypes {
			batch.Queue(`INSERT INTO methodology_link_type VALUES ($1, $2, $3, $4, $5)`, id, i, l.Name, l.From, l.To)
		}
		for i, c := range m.Conditions {
			batch.Queue(`INSERT INTO methodology_condition VALUES ($1, $2, $3, $4, $5)`, id, i, c.Name, c.Description, c.Expr)
		}
		for i, a := range m.Actions {
			var expects []byte
			if a.Expects != nil {
				expects = jsonOf(a.Expects)
			}
			batch.Queue(`INSERT INTO methodology_action (methodology_id, position, name, description, kind, pre, effects, cost, expects,
				permission, model, prompt, tool, builtin, instructions, params, language, code, utility)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)`,
				id, i, a.Name, a.Description, a.Kind, jsonOf(orEmptyBool(a.Pre)), jsonOf(orEmptyBool(a.Effects)), a.Cost, expects,
				a.Permission, a.Model, a.Prompt, a.Tool, a.Builtin, a.Instructions, jsonOf(orEmptyAny(a.Params)), a.Language, a.Code, a.Utility)
		}
		for i, g := range m.Goals {
			ex := g.Examples
			if ex == nil {
				ex = []string{}
			}
			batch.Queue(`INSERT INTO methodology_goal VALUES ($1, $2, $3, $4, $5, $6, $7)`, id, i, g.Name, g.Description, ex, jsonOf(orEmptyBool(g.Pre)), g.Value)
		}
		for i, ag := range m.Agents {
			batch.Queue(`INSERT INTO methodology_agent VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, id, i, ag.Name, ag.Description,
				orEmptyStrings(ag.Examples), ag.Planner, orEmptyStrings(ag.Actions), orEmptyStrings(ag.Goals))
		}
		return tx.SendBatch(ctx, batch).Close()
	})
}

func orEmptyStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orEmptyBool(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	return m
}

func orEmptyAny(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

const headerCols = `id::text, name, version, description, status, created_at, updated_at, published_at, updated_by`

type header struct {
	id string
	r  Record
}

func scanHeader(row pgx.Row) (header, error) {
	var h header
	var published *time.Time
	var status string
	m := &h.r.Methodology
	err := row.Scan(&h.id, &m.Name, &m.Version, &m.Description, &status, &h.r.CreatedAt, &h.r.UpdatedAt, &published, &h.r.UpdatedBy)
	h.r.Status = Status(status)
	if published != nil {
		h.r.PublishedAt = *published
	}
	return h, err
}

func (s PostgresStore) Get(ctx context.Context, name, version string) (Record, error) {
	q := `SELECT ` + headerCols + ` FROM methodology WHERE name = $1 AND version = $2`
	args := []any{name, version}
	if version == "" {
		q = `SELECT ` + headerCols + ` FROM methodology WHERE name = $1 AND status = 'published' ORDER BY published_at DESC LIMIT 1`
		args = args[:1]
	}
	h, err := scanHeader(s.Pool.QueryRow(ctx, q, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	if err != nil {
		return Record{}, err
	}
	if err := s.loadSections(ctx, h.id, &h.r.Methodology); err != nil {
		return Record{}, err
	}
	return h.r, nil
}

func (s PostgresStore) loadSections(ctx context.Context, id string, m *methodology.Methodology) error {
	rows, err := s.Pool.Query(ctx, `SELECT name, description, properties FROM methodology_node_type WHERE methodology_id = $1 ORDER BY position`, id)
	if err != nil {
		return err
	}
	m.Domain.NodeTypes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (methodology.NodeType, error) {
		var n methodology.NodeType
		err := r.Scan(&n.Name, &n.Description, &n.Properties)
		if len(n.Properties) == 0 {
			n.Properties = nil
		}
		return n, err
	})
	if err != nil {
		return err
	}
	rows, err = s.Pool.Query(ctx, `SELECT name, from_type, to_type FROM methodology_link_type WHERE methodology_id = $1 ORDER BY position`, id)
	if err != nil {
		return err
	}
	m.Domain.LinkTypes, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (methodology.LinkType, error) {
		var l methodology.LinkType
		return l, r.Scan(&l.Name, &l.From, &l.To)
	})
	if err != nil {
		return err
	}
	rows, err = s.Pool.Query(ctx, `SELECT name, description, expr FROM methodology_condition WHERE methodology_id = $1 ORDER BY position`, id)
	if err != nil {
		return err
	}
	m.Conditions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (methodology.Condition, error) {
		var c methodology.Condition
		return c, r.Scan(&c.Name, &c.Description, &c.Expr)
	})
	if err != nil {
		return err
	}
	rows, err = s.Pool.Query(ctx, `SELECT name, description, kind, pre, effects, cost, expects, permission, model, prompt, tool, builtin, instructions, params,
		language, code, utility FROM methodology_action WHERE methodology_id = $1 ORDER BY position`, id)
	if err != nil {
		return err
	}
	m.Actions, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (methodology.Action, error) {
		var a methodology.Action
		var pre, effects, expects, params []byte
		if err := r.Scan(&a.Name, &a.Description, &a.Kind, &pre, &effects, &a.Cost, &expects, &a.Permission, &a.Model, &a.Prompt,
			&a.Tool, &a.Builtin, &a.Instructions, &params, &a.Language, &a.Code, &a.Utility); err != nil {
			return a, err
		}
		_ = json.Unmarshal(pre, &a.Pre)
		_ = json.Unmarshal(effects, &a.Effects)
		_ = json.Unmarshal(params, &a.Params)
		if len(expects) > 0 {
			a.Expects = &condition.Expectation{}
			_ = json.Unmarshal(expects, a.Expects)
		}
		a.Pre, a.Effects, a.Params = nilIfEmpty(a.Pre), nilIfEmpty(a.Effects), nilIfEmpty(a.Params)
		return a, nil
	})
	if err != nil {
		return err
	}
	rows, err = s.Pool.Query(ctx, `SELECT name, description, examples, planner, actions, goals FROM methodology_agent WHERE methodology_id = $1 ORDER BY position`, id)
	if err != nil {
		return err
	}
	m.Agents, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (methodology.Agent, error) {
		var a methodology.Agent
		err := r.Scan(&a.Name, &a.Description, &a.Examples, &a.Planner, &a.Actions, &a.Goals)
		a.Examples, a.Actions, a.Goals = nilIfNoStrings(a.Examples), nilIfNoStrings(a.Actions), nilIfNoStrings(a.Goals)
		return a, err
	})
	if err != nil {
		return err
	}
	if len(m.Agents) == 0 {
		m.Agents = nil
	}
	rows, err = s.Pool.Query(ctx, `SELECT name, description, examples, pre, value FROM methodology_goal WHERE methodology_id = $1 ORDER BY position`, id)
	if err != nil {
		return err
	}
	m.Goals, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (methodology.Goal, error) {
		var g methodology.Goal
		var pre []byte
		if err := r.Scan(&g.Name, &g.Description, &g.Examples, &pre, &g.Value); err != nil {
			return g, err
		}
		_ = json.Unmarshal(pre, &g.Pre)
		if len(g.Examples) == 0 {
			g.Examples = nil
		}
		return g, nil
	})
	return err
}

func nilIfNoStrings(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}

func nilIfEmpty[M ~map[K]V, K comparable, V any](m M) M {
	if len(m) == 0 {
		return nil
	}
	return m
}

func (s PostgresStore) List(ctx context.Context) ([]Record, error) {
	rows, err := s.Pool.Query(ctx, `SELECT `+headerCols+` FROM methodology ORDER BY name, created_at`)
	if err != nil {
		return nil, err
	}
	var hs []header
	for rows.Next() {
		h, err := scanHeader(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		hs = append(hs, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Record, len(hs))
	for i, h := range hs {
		if err := s.loadSections(ctx, h.id, &h.r.Methodology); err != nil {
			return nil, err
		}
		out[i] = h.r
	}
	return out, nil
}

func (s PostgresStore) SetStatus(ctx context.Context, name, version string, st Status, at time.Time) error {
	q := `UPDATE methodology SET status = $3, updated_at = $4 WHERE name = $1 AND version = $2`
	if st == StatusPublished {
		q = `UPDATE methodology SET status = $3, updated_at = $4, published_at = $4 WHERE name = $1 AND version = $2`
	}
	tag, err := s.Pool.Exec(ctx, q, name, version, string(st), at)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	return nil
}

func (s PostgresStore) Delete(ctx context.Context, name, version string) error {
	var status string
	err := s.Pool.QueryRow(ctx, `SELECT status FROM methodology WHERE name = $1 AND version = $2`, name, version).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%s: %w", key(name, version), ErrNotFound)
	}
	if err != nil {
		return err
	}
	if Status(status) != StatusDraft {
		return fmt.Errorf("%s: %w", key(name, version), ErrImmutable)
	}
	_, err = s.Pool.Exec(ctx, `DELETE FROM methodology WHERE name = $1 AND version = $2 AND status = 'draft'`, name, version)
	return err
}
