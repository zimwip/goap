package modelgw

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
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

func (s SQLStore) Usage(ctx context.Context, model, period string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT tokens FROM llm_usage WHERE model_key = ? AND period = ?`), model, period).Scan(&n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func (s SQLStore) AddUsage(ctx context.Context, model, period string, tokens int64) error {
	_, err := s.DB.ExecContext(ctx, s.q(`INSERT INTO llm_usage (model_key, period, tokens) VALUES (?, ?, ?)
		ON CONFLICT (model_key, period) DO UPDATE SET tokens = llm_usage.tokens + excluded.tokens`), model, period, tokens)
	return err
}

const callColumns = `seq, at_ms, duration_ms, subject, project, org, alias, provider, model, kind, input_tokens, output_tokens, error,
	source, conversation_id, process_id, change_id, step, action, agent, call_index, behaviors, behavior_tokens,
	CASE WHEN EXISTS (SELECT 1 FROM llm_call_exchange x WHERE x.seq = llm_call.seq) THEN 1 ELSE 0 END`

// scanCall reads a row of callColumns.
func scanCall(sc interface{ Scan(...any) error }) (Call, error) {
	var c Call
	var ms int64
	var has int
	var behaviors string
	if err := sc.Scan(&c.Seq, &ms, &c.DurationMs, &c.Subject, &c.Project, &c.Org, &c.Alias, &c.Provider, &c.Model, &c.Kind, &c.InputTokens,
		&c.OutputTokens, &c.Error, &c.Source, &c.ConversationID, &c.ProcessID, &c.ChangeID, &c.Step, &c.Action, &c.Agent, &c.CallIndex, &behaviors, &c.BehaviorTokens, &has); err != nil {
		return c, err
	}
	c.At, c.HasExchange = time.UnixMilli(ms).UTC(), has == 1
	if behaviors != "" {
		c.Behaviors = strings.Split(behaviors, ",")
	}
	return c, nil
}

func (s SQLStore) Call(ctx context.Context, seq int64) (Call, error) {
	c, err := scanCall(s.DB.QueryRowContext(ctx, s.q(`SELECT `+callColumns+` FROM llm_call WHERE seq = ?`), seq))
	if errors.Is(err, sql.ErrNoRows) {
		return Call{}, ErrNotFound
	}
	return c, err
}

func (s SQLStore) Exchange(ctx context.Context, seq int64) (Exchange, error) {
	x := Exchange{Seq: seq}
	var msgs string
	var trunc int
	err := s.DB.QueryRowContext(ctx, s.q(`SELECT system, messages, response, truncated FROM llm_call_exchange WHERE seq = ?`), seq).Scan(&x.System, &msgs, &x.Response, &trunc)
	if errors.Is(err, sql.ErrNoRows) {
		return Exchange{}, ErrNotFound
	}
	if err != nil {
		return Exchange{}, err
	}
	x.Truncated = trunc == 1
	if err := json.Unmarshal([]byte(msgs), &x.Messages); err != nil {
		return Exchange{}, fmt.Errorf("exchange %d: %w", seq, err)
	}
	return x, nil
}

func (s SQLStore) AppendCall(ctx context.Context, c Call, x *Exchange) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var seq int64
	err = tx.QueryRowContext(ctx, s.q(`INSERT INTO llm_call (at_ms, duration_ms, subject, project, org, alias, provider, model, kind,
		input_tokens, output_tokens, error, source, conversation_id, process_id, change_id, step, action, agent, call_index, behaviors, behavior_tokens)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq`),
		c.At.UnixMilli(), c.DurationMs, c.Subject, c.Project, c.Org, c.Alias, c.Provider, c.Model, c.Kind, c.InputTokens, c.OutputTokens,
		c.Error, c.Source, c.ConversationID, c.ProcessID, c.ChangeID, c.Step, c.Action, c.Agent, c.CallIndex, strings.Join(c.Behaviors, ","), c.BehaviorTokens).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if x != nil {
		msgs, err := json.Marshal(x.Messages)
		if err != nil {
			return 0, err
		}
		trunc := 0
		if x.Truncated {
			trunc = 1
		}
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO llm_call_exchange (seq, system, messages, response, truncated) VALUES (?, ?, ?, ?, ?)`),
			seq, x.System, string(msgs), x.Response, trunc); err != nil {
			return 0, err
		}
	}
	return seq, tx.Commit()
}

// where is the WHERE clause of a filter (AfterSeq included) with its arguments.
func (f UsageFilter) where() (string, []any) {
	var conds []string
	var args []any
	add := func(cond string, arg ...any) {
		conds = append(conds, cond)
		args = append(args, arg...)
	}
	if f.AfterSeq > 0 {
		add("seq > ?", f.AfterSeq)
	}
	if !f.From.IsZero() {
		add("at_ms >= ?", f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		add("at_ms < ?", f.To.UnixMilli())
	}
	for _, e := range []struct{ col, val string }{{"subject", f.Subject}, {"project", f.Project}, {"alias", f.Alias}, {"source", f.Source},
		{"process_id", f.ProcessID}, {"change_id", f.ChangeID}, {"conversation_id", f.ConversationID}} {
		if e.val != "" {
			add(e.col+" = ?", e.val)
		}
	}
	if f.Model != "" {
		add("(model = ? OR provider || '/' || model = ?)", f.Model, f.Model)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func (s SQLStore) Calls(ctx context.Context, f UsageFilter) ([]Call, bool, error) {
	where, args := f.where()
	n := f.limit()
	order := "ASC"
	if f.AfterSeq == 0 {
		order = "DESC" // the latest ones, put back in order below
	}
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT `+callColumns+` FROM llm_call`+where+` ORDER BY seq `+order+` LIMIT ?`), append(args, n+1)...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Call
	for rows.Next() {
		c, err := scanCall(rows)
		if err != nil {
			return nil, false, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	more := len(out) > n
	if more {
		out = out[:n]
	}
	if order == "DESC" {
		slices.Reverse(out)
	}
	return out, more, nil
}

// groupColumns are the SQL expressions of the groupings; a time grouping is the bucket number, formatted by bucketKey.
var groupColumns = map[string]string{
	GroupModel: "model", GroupAlias: "alias", GroupSource: "source", GroupSubject: "subject", GroupProcess: "process_id",
	GroupAction: "action", GroupAgent: "agent", GroupDay: fmt.Sprintf("at_ms / %d", dayMs), GroupHour: fmt.Sprintf("at_ms / %d", hourMs),
}

func (s SQLStore) Summary(ctx context.Context, f UsageFilter, group string) ([]SummaryRow, error) {
	col, ok := groupColumns[group]
	if !ok {
		return nil, fmt.Errorf("%w: group_by %q", ErrInvalid, group)
	}
	where, args := f.where()
	order := "k"
	if group != GroupDay && group != GroupHour {
		order = "CAST(SUM(input_tokens + output_tokens) AS bigint) DESC, k"
	}
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT `+col+` AS k, COUNT(*), CAST(SUM(input_tokens) AS bigint), CAST(SUM(output_tokens) AS bigint),
		CAST(SUM(CASE WHEN error <> '' THEN 1 ELSE 0 END) AS bigint), CAST(SUM(duration_ms) AS bigint) FROM llm_call`+where+` GROUP BY k ORDER BY `+order), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SummaryRow
	for rows.Next() {
		var r SummaryRow
		var key any
		if err := rows.Scan(&key, &r.Calls, &r.Input, &r.Output, &r.Errors, &r.DurationMs); err != nil {
			return nil, err
		}
		switch k := key.(type) {
		case int64:
			r.Key = bucketKey(group, k)
		case string:
			r.Key = k
		case []byte:
			r.Key = string(k)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s SQLStore) PurgeCalls(ctx context.Context, before time.Time) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM llm_call_exchange WHERE seq IN (SELECT seq FROM llm_call WHERE at_ms < ?)`), before.UnixMilli()); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, s.q(`DELETE FROM llm_call WHERE at_ms < ?`), before.UnixMilli())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
