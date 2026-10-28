package graph

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pipeTx is a write transaction on one connection that sends the statements nothing waits on (its BEGIN, an advisory
// lock, an insert whose answer is known beforehand) with the next statement, in one round trip: the statements of a
// pipeline run one after the other on the server, each with a snapshot taken when it starts (READ COMMITTED), so a
// read sent after a lock in the same pipeline reads what the lock holds. Its COMMIT goes with the statements still
// queued. An error of a statement queued is the error of the statement sent with it.
type pipeTx struct {
	conn *pgx.Conn
	// queued are the statements waiting for the next one, in order
	queued []queuedExec
	// begun tells a BEGIN was sent (queued or run)
	begun bool
}

// queuedExec is a statement queued: what an error of it is about.
type queuedExec struct {
	sql  string
	args []any
	what string
}

// begin queues the BEGIN of the transaction.
func (p *pipeTx) begin() {
	p.queue("begin", nil, "begin")
	p.begun = true
}

// queue queues a statement whose answer is no more than its error.
func (p *pipeTx) queue(sql string, args []any, what string) {
	p.queued = append(p.queued, queuedExec{sql: sql, args: args, what: what})
}

// batch is a batch of the statements queued, emptied.
func (p *pipeTx) batch() *pgx.Batch {
	b := &pgx.Batch{}
	for _, q := range p.queued {
		b.Queue(q.sql, q.args...)
	}
	return b
}

// sent reads the answers of the statements queued, sent in br first: the first error, about its statement.
func (p *pipeTx) sent(br pgx.BatchResults, queued []queuedExec) error {
	for _, q := range queued {
		if _, err := br.Exec(); err != nil {
			return mapErr(err, q.what)
		}
	}
	return nil
}

// flushQueued sends the statements queued alone.
func (p *pipeTx) flushQueued(ctx context.Context) error {
	if len(p.queued) == 0 {
		return nil
	}
	queued := p.queued
	br := p.conn.SendBatch(ctx, p.batch())
	p.queued = nil
	err := p.sent(br, queued)
	return errors.Join(err, closeQuiet(br, err))
}

// closeQuiet closes a batch, its error only when none came before (the statements after a failed one are not run).
func closeQuiet(br pgx.BatchResults, before error) error {
	err := br.Close()
	if before != nil {
		return nil
	}
	return err
}

// withMode tells the statements carrying their own execution mode (customPlan): a batch runs its statements in the
// connection's, so the statements queued go before them, alone.
func withMode(args []any) bool {
	if len(args) == 0 {
		return false
	}
	_, ok := args[0].(pgx.QueryExecMode)
	return ok
}

func (p *pipeTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	if len(p.queued) == 0 || withMode(args) {
		if err := p.flushQueued(ctx); err != nil {
			return pgconn.CommandTag{}, err
		}
		return p.conn.Exec(ctx, sql, args...)
	}
	b, queued := p.batch(), p.queued
	b.Queue(sql, args...)
	p.queued = nil
	br := p.conn.SendBatch(ctx, b)
	if err := p.sent(br, queued); err != nil {
		return pgconn.CommandTag{}, errors.Join(err, closeQuiet(br, err))
	}
	tag, err := br.Exec()
	return tag, errors.Join(err, closeQuiet(br, err))
}

func (p *pipeTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if len(p.queued) == 0 || withMode(args) {
		if err := p.flushQueued(ctx); err != nil {
			return nil, err
		}
		return p.conn.Query(ctx, sql, args...)
	}
	b, queued := p.batch(), p.queued
	b.Queue(sql, args...)
	p.queued = nil
	br := p.conn.SendBatch(ctx, b)
	if err := p.sent(br, queued); err != nil {
		return nil, errors.Join(err, closeQuiet(br, err))
	}
	rows, err := br.Query()
	if err != nil {
		return nil, errors.Join(err, closeQuiet(br, err))
	}
	return &batchRows{Rows: rows, br: br}, nil
}

func (p *pipeTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if len(p.queued) == 0 || withMode(args) {
		if err := p.flushQueued(ctx); err != nil {
			return errRow{err}
		}
		return p.conn.QueryRow(ctx, sql, args...)
	}
	b, queued := p.batch(), p.queued
	b.Queue(sql, args...)
	p.queued = nil
	return &batchRow{p: p, br: p.conn.SendBatch(ctx, b), queued: queued}
}

// commit sends the statements queued and the COMMIT in one round trip; a transaction that sent nothing but its BEGIN
// sends nothing.
func (p *pipeTx) commit(ctx context.Context) error {
	if len(p.queued) == 1 && p.queued[0].sql == "begin" {
		p.queued, p.begun = nil, false
		return nil
	}
	p.queue("commit", nil, "commit")
	b, queued := p.batch(), p.queued
	p.queued = nil
	br := p.conn.SendBatch(ctx, b)
	err := p.sent(br, queued[:len(queued)-1])
	if err == nil {
		var tag pgconn.CommandTag
		if tag, err = br.Exec(); err == nil && tag.String() == "ROLLBACK" {
			err = pgx.ErrTxCommitRollback
		}
	}
	err = errors.Join(err, closeQuiet(br, err))
	if err == nil {
		p.begun = false
	}
	return err
}

// rollback ends a transaction that failed: what is queued is dropped.
func (p *pipeTx) rollback(ctx context.Context) error {
	p.queued = nil
	if !p.begun {
		return nil
	}
	p.begun = false
	_, err := p.conn.Exec(context.WithoutCancel(ctx), "rollback")
	return err
}

// batchRows are the rows of a statement sent in a batch: closing them closes the batch.
type batchRows struct {
	pgx.Rows
	br     pgx.BatchResults
	closed bool
	err    error
}

func (r *batchRows) Close() {
	r.Rows.Close()
	if !r.closed {
		r.closed = true
		r.err = r.br.Close()
	}
}

func (r *batchRows) Next() bool {
	if r.Rows.Next() {
		return true
	}
	r.Close()
	return false
}

func (r *batchRows) Err() error {
	if err := r.Rows.Err(); err != nil {
		return err
	}
	return r.err
}

// batchRow is the row of a statement sent in a batch, read when scanned.
type batchRow struct {
	p      *pipeTx
	br     pgx.BatchResults
	queued []queuedExec
}

func (r *batchRow) Scan(dest ...any) error {
	if err := r.p.sent(r.br, r.queued); err != nil {
		return errors.Join(err, closeQuiet(r.br, err))
	}
	err := r.br.QueryRow().Scan(dest...)
	return errors.Join(err, closeQuiet(r.br, err))
}
