package graph

import (
	"context"
	"database/sql"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zimwip/goap/contracts/domain"
)

// decodeStoredLog gives back the JSON payloads of entries read as stored.
func decodeStoredLog(ctx context.Context, c *logCodec, store logDictStore, out []domain.LogEntry) ([]domain.LogEntry, error) {
	for i := range out {
		p, err := c.decode(ctx, store, out[i].Payload)
		if err != nil {
			return nil, err
		}
		out[i].Payload = p
	}
	return out, nil
}

// pgDicts keeps the dictionaries of the log in PostgreSQL, read and written on connections of their own.
type pgDicts struct{ pool *pgxpool.Pool }

func (s pgDicts) dicts(ctx context.Context, after uint32) ([]logDict, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, type, dict FROM log_dictionary WHERE id > $1 ORDER BY id`, int64(after))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []logDict
	for rows.Next() {
		var d logDict
		var id int64
		if err := rows.Scan(&id, &d.Type, &d.Dict); err != nil {
			return nil, err
		}
		d.ID = uint32(id)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s pgDicts) samples(ctx context.Context, typ string, n int) ([][]byte, error) {
	rows, err := s.pool.Query(ctx, `SELECT payload FROM change_log WHERE type = $1 ORDER BY seq DESC LIMIT $2`, typ, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s pgDicts) addDict(ctx context.Context, typ string, build func(id uint32) ([]byte, error)) (logDict, error) {
	var id int64
	if err := s.pool.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('log_dictionary', 'id'))`).Scan(&id); err != nil {
		return logDict{}, err
	}
	d, err := build(uint32(id))
	if err != nil {
		return logDict{}, err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO log_dictionary (id, type, dict, created_at) VALUES ($1, $2, $3, $4)`, id, typ, d, time.Now().UTC())
	return logDict{ID: uint32(id), Type: typ, Dict: d}, err
}

// sqliteDicts keeps the dictionaries of the log in SQLite: trained on the writer, read in the transaction asking.
type sqliteDicts struct {
	db *sql.DB
	// q reads the dictionaries (a transaction; nil: db)
	q interface {
		QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	}
}

func (s sqliteDicts) dicts(ctx context.Context, after uint32) ([]logDict, error) {
	q := s.q
	if q == nil {
		q = s.db
	}
	rows, err := q.QueryContext(ctx, `SELECT id, type, dict FROM log_dictionary WHERE id > ? ORDER BY id`, int64(after))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []logDict
	for rows.Next() {
		var d logDict
		var id int64
		if err := rows.Scan(&id, &d.Type, &d.Dict); err != nil {
			return nil, err
		}
		d.ID = uint32(id)
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s sqliteDicts) samples(ctx context.Context, typ string, n int) ([][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM change_log WHERE type = ? ORDER BY seq DESC LIMIT ?`, typ, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var p []byte
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s sqliteDicts) addDict(ctx context.Context, typ string, build func(id uint32) ([]byte, error)) (logDict, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return logDict{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT coalesce(max(id), 0) + 1 FROM log_dictionary`).Scan(&id); err != nil {
		return logDict{}, err
	}
	d, err := build(uint32(id))
	if err != nil {
		return logDict{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO log_dictionary (id, type, dict, created_at) VALUES (?, ?, ?, ?)`, id, typ, d, tsText(time.Now())); err != nil {
		return logDict{}, err
	}
	return logDict{ID: uint32(id), Type: typ, Dict: d}, tx.Commit()
}
