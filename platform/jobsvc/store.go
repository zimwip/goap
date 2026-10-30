// Package jobsvc is the job service (ADR 0149): the long runs a person starts (an import from a source, the import of
// a graph snapshot, an export) as records a worker claims, reports on and ends, followed by the person over a
// Server-Sent Events stream and downloaded from it. It never interprets a kind: the service owning one authorizes the
// run and creates its job; a worker (lib/jobkit) runs it.
package jobsvc

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/outbox"
)

// Migrations holds the PostgreSQL schema; SQLiteMigrations the local mode one.
//
//go:embed migrations/*.sql
var Migrations embed.FS

//go:embed migrations_sqlite/*.sql
var SQLiteMigrations embed.FS

// OutboxTable is the outbox table of the jobs (ADR 0136).
const OutboxTable = "job_outbox"

// The blobs of a job.
const (
	BlobInput  = "input"
	BlobResult = "result"
)

// blobChunk is the size of the chunks an input is stored and read in.
const blobChunk = 1 << 20

// Record is a stored job.
type Record struct {
	ID, Kind, Label, Target string
	Status                  string
	CancelRequested         bool
	Params                  map[string]any
	By, ProjectID, OwnerOrg string
	// Principal is the JSON of the principal the run acts as (jobkit.EncodePrincipal).
	Principal   string
	Progress    string
	Done, Total int64
	Stats       map[string]int64
	Output      map[string]any
	// Log is the feedback of the run, its last KeepLog lines.
	Log []jobkit.LogLine
	// Request is the last request the run asked the person (pending while it has no answer).
	Request    *jobkit.Request
	ResultName string
	ResultType string
	ResultSize int64
	Error      string
	Worker     string
	Created    time.Time
	Started    time.Time
	Ended      time.Time
	Updated    time.Time
	LeaseUntil time.Time
}

func (r Record) clone() Record {
	r.Params, r.Stats, r.Output, r.Log = maps.Clone(r.Params), maps.Clone(r.Stats), maps.Clone(r.Output), slices.Clone(r.Log)
	r.Request = r.Request.Clone()
	return r
}

// Filter selects jobs, newest first.
type Filter struct {
	// By: only the jobs of this person ("" for everyone's).
	By         string
	KindPrefix string
	Target     string
	Limit      int
}

func (f Filter) match(r Record) bool {
	return (f.By == "" || r.By == f.By) && strings.HasPrefix(r.Kind, f.KindPrefix) && (f.Target == "" || r.Target == f.Target)
}

// Store keeps the jobs. Every write takes the events of its fact (ADR 0136).
type Store interface {
	Create(ctx context.Context, r Record, input []byte, msgs []outbox.Message) error
	Get(ctx context.Context, id string) (Record, error)
	List(ctx context.Context, f Filter) ([]Record, error)
	// Claim takes the oldest queued job of kinds: fn stamps it (running, worker, lease) and returns its events; false
	// when none is queued.
	Claim(ctx context.Context, kinds []string, fn func(*Record) []outbox.Message) (Record, bool, error)
	// Update changes a job atomically; an error of fn writes nothing.
	Update(ctx context.Context, id string, fn func(*Record) ([]outbox.Message, error)) (Record, error)
	// Expired lists the running jobs whose lease lapsed before now.
	Expired(ctx context.Context, now time.Time) ([]string, error)
	// ReadBlob reads up to max bytes of a blob from offset; eof when nothing is left past them.
	ReadBlob(ctx context.Context, id, role string, offset int64, max int) ([]byte, bool, error)
	// AppendBlob appends to a blob and returns its size.
	AppendBlob(ctx context.Context, id, role string, data []byte) (int64, error)
	// Purge forgets the jobs ended before a time, with their blobs.
	Purge(ctx context.Context, before time.Time) (int, error)
	outbox.Store
}

// MemoryStore is an in-memory Store (tests, no database).
type MemoryStore struct {
	mu    sync.Mutex
	jobs  map[string]Record
	blobs map[[2]string][]byte
	out   outbox.Memory
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{jobs: map[string]Record{}, blobs: map[[2]string][]byte{}}
}

func (s *MemoryStore) Create(_ context.Context, r Record, input []byte, msgs []outbox.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[r.ID]; ok {
		return fmt.Errorf("job %s exists: %w", r.ID, jobkit.ErrInvalid)
	}
	s.jobs[r.ID] = r.clone()
	if len(input) > 0 {
		s.blobs[[2]string{r.ID, BlobInput}] = slices.Clone(input)
	}
	s.out.Append(msgs...)
	return nil
}

func (s *MemoryStore) Get(_ context.Context, id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.jobs[id]
	if !ok {
		return Record{}, fmt.Errorf("job %s: %w", id, jobkit.ErrNotFound)
	}
	return r.clone(), nil
}

func newestFirst(rs []Record) {
	sort.Slice(rs, func(i, j int) bool {
		if !rs[i].Created.Equal(rs[j].Created) {
			return rs[i].Created.After(rs[j].Created)
		}
		return rs[i].ID > rs[j].ID
	})
}

func (s *MemoryStore) List(_ context.Context, f Filter) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Record
	for _, r := range s.jobs {
		if f.match(r) {
			out = append(out, r.clone())
		}
	}
	newestFirst(out)
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s *MemoryStore) Claim(_ context.Context, kinds []string, fn func(*Record) []outbox.Message) (Record, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *Record
	for _, r := range s.jobs {
		if r.Status != jobkit.StatusQueued || !slices.Contains(kinds, r.Kind) {
			continue
		}
		if best == nil || r.Created.Before(best.Created) || (r.Created.Equal(best.Created) && r.ID < best.ID) {
			c := r
			best = &c
		}
	}
	if best == nil {
		return Record{}, false, nil
	}
	s.out.Append(fn(best)...)
	s.jobs[best.ID] = best.clone()
	return best.clone(), true, nil
}

func (s *MemoryStore) Update(_ context.Context, id string, fn func(*Record) ([]outbox.Message, error)) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.jobs[id]
	if !ok {
		return Record{}, fmt.Errorf("job %s: %w", id, jobkit.ErrNotFound)
	}
	r = r.clone()
	msgs, err := fn(&r)
	if err != nil {
		return Record{}, err
	}
	s.jobs[id] = r.clone()
	s.out.Append(msgs...)
	return r, nil
}

func (s *MemoryStore) Expired(_ context.Context, now time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, r := range s.jobs {
		if r.Status == jobkit.StatusRunning && r.LeaseUntil.Before(now) {
			out = append(out, r.ID)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (s *MemoryStore) ReadBlob(_ context.Context, id, role string, offset int64, max int) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.blobs[[2]string{id, role}]
	if offset >= int64(len(b)) {
		return nil, true, nil
	}
	end := min(int64(len(b)), offset+int64(max))
	return slices.Clone(b[offset:end]), end == int64(len(b)), nil
}

func (s *MemoryStore) AppendBlob(_ context.Context, id, role string, data []byte) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := [2]string{id, role}
	s.blobs[k] = append(s.blobs[k], data...)
	return int64(len(s.blobs[k])), nil
}

func (s *MemoryStore) Purge(_ context.Context, before time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, r := range s.jobs {
		if jobkit.Final(r.Status) && r.Ended.Before(before) {
			delete(s.jobs, id)
			delete(s.blobs, [2]string{id, BlobInput})
			delete(s.blobs, [2]string{id, BlobResult})
			n++
		}
	}
	return n, nil
}

// Pending implements outbox.Store.
func (s *MemoryStore) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return s.out.Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s *MemoryStore) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return s.out.MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s *MemoryStore) PurgePublished(ctx context.Context, before time.Time) error {
	return s.out.PurgePublished(ctx, before)
}

// SQLStore is the Store on database/sql, for SQLite (local mode) and PostgreSQL (through the pgx stdlib adapter,
// Dollar set).
type SQLStore struct {
	DB *sql.DB
	// Read, when set, serves the plain reads (a pool of read-only connections in the local mode,
	// platform.OpenSQLiteReader): they never wait behind a write on DB.
	Read *sql.DB
	// Dollar rewrites "?" placeholders as $1, $2… (PostgreSQL).
	Dollar bool
}

var _ Store = SQLStore{}

func (s SQLStore) outbox() outbox.SQL {
	return outbox.SQL{DB: s.DB, Postgres: s.Dollar, Table: OutboxTable}
}

// Pending implements outbox.Store.
func (s SQLStore) Pending(ctx context.Context, limit int) ([]outbox.Message, error) {
	return s.outbox().Pending(ctx, limit)
}

// MarkPublished implements outbox.Store.
func (s SQLStore) MarkPublished(ctx context.Context, seqs []int64, at time.Time) error {
	return s.outbox().MarkPublished(ctx, seqs, at)
}

// PurgePublished implements outbox.Store.
func (s SQLStore) PurgePublished(ctx context.Context, before time.Time) error {
	return s.outbox().PurgePublished(ctx, before)
}

func (s SQLStore) q(query string) string { return outbox.Rebind(query, s.Dollar) }

// ts is a time as the dialect stores it (SQLite: a text that sorts as the time does); nil for the zero time.
func (s SQLStore) ts(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	if s.Dollar {
		return t.UTC()
	}
	return t.UTC().Format(outbox.SQLiteTime)
}

// sqlTime reads a stored time of either dialect.
type sqlTime struct{ t *time.Time }

func (x sqlTime) Scan(v any) error {
	switch v := v.(type) {
	case nil:
		*x.t = time.Time{}
	case time.Time:
		*x.t = v.UTC()
	case string:
		t, err := time.Parse(outbox.SQLiteTime, v)
		if err != nil {
			if t, err = time.Parse(time.RFC3339Nano, v); err != nil {
				return err
			}
		}
		*x.t = t.UTC()
	case []byte:
		return x.Scan(string(v))
	default:
		return fmt.Errorf("time: %T", v)
	}
	return nil
}

// jsonOf is the JSON of a map ("{}" for none).
func jsonOf(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if string(b) == "null" {
		return "{}", nil
	}
	return string(b), nil
}

const columns = `id, kind, label, target, status, cancel_requested, params, by_subject, project_id, owner_org, principal, progress,
	done, total, stats, output, result_name, result_type, result_size, error, worker, created_at, started_at, ended_at,
	updated_at, lease_until, log, request`

type scanner interface{ Scan(...any) error }

func scanRecord(row scanner) (Record, error) {
	var r Record
	var params, stats, output, logs, request string
	var cancel any
	if err := row.Scan(&r.ID, &r.Kind, &r.Label, &r.Target, &r.Status, &cancel, &params, &r.By, &r.ProjectID, &r.OwnerOrg,
		&r.Principal, &r.Progress, &r.Done, &r.Total, &stats, &output, &r.ResultName, &r.ResultType, &r.ResultSize, &r.Error,
		&r.Worker, sqlTime{&r.Created}, sqlTime{&r.Started}, sqlTime{&r.Ended}, sqlTime{&r.Updated}, sqlTime{&r.LeaseUntil}, &logs, &request); err != nil {
		return r, err
	}
	switch c := cancel.(type) {
	case bool:
		r.CancelRequested = c
	case int64:
		r.CancelRequested = c != 0
	}
	if err := json.Unmarshal([]byte(params), &r.Params); err != nil {
		return r, fmt.Errorf("job %s params: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(stats), &r.Stats); err != nil {
		return r, fmt.Errorf("job %s stats: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(output), &r.Output); err != nil {
		return r, fmt.Errorf("job %s output: %w", r.ID, err)
	}
	if err := json.Unmarshal([]byte(logs), &r.Log); err != nil {
		return r, fmt.Errorf("job %s log: %w", r.ID, err)
	}
	if request != "" && request != "{}" && request != "null" {
		r.Request = &jobkit.Request{}
		if err := json.Unmarshal([]byte(request), r.Request); err != nil {
			return r, fmt.Errorf("job %s request: %w", r.ID, err)
		}
	}
	return r, nil
}

func (s SQLStore) args(r Record) ([]any, error) {
	params, err := jsonOf(r.Params)
	if err != nil {
		return nil, err
	}
	stats, err := jsonOf(r.Stats)
	if err != nil {
		return nil, err
	}
	output, err := jsonOf(r.Output)
	if err != nil {
		return nil, err
	}
	logs, err := json.Marshal(r.Log)
	if err != nil {
		return nil, err
	}
	if r.Log == nil {
		logs = []byte("[]")
	}
	request := []byte("{}")
	if r.Request != nil {
		if request, err = json.Marshal(r.Request); err != nil {
			return nil, err
		}
	}
	var cancel any = r.CancelRequested
	if !s.Dollar {
		cancel = 0
		if r.CancelRequested {
			cancel = 1
		}
	}
	return []any{r.ID, r.Kind, r.Label, r.Target, r.Status, cancel, params, r.By, r.ProjectID, r.OwnerOrg, r.Principal, r.Progress,
		r.Done, r.Total, stats, output, r.ResultName, r.ResultType, r.ResultSize, r.Error, r.Worker, s.ts(r.Created), s.ts(r.Started),
		s.ts(r.Ended), s.ts(r.Updated), s.ts(r.LeaseUntil), string(logs), string(request)}, nil
}

func (s SQLStore) Create(ctx context.Context, r Record, input []byte, msgs []outbox.Message) error {
	args, err := s.args(r)
	if err != nil {
		return err
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO job (`+columns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), args...); err != nil {
		return err
	}
	for seq, off := 0, 0; off < len(input); seq, off = seq+1, off+blobChunk {
		end := min(len(input), off+blobChunk)
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO job_blob (job_id, role, seq, data) VALUES (?, ?, ?, ?)`), r.ID, BlobInput, seq, input[off:end]); err != nil {
			return err
		}
	}
	if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
		return err
	}
	return tx.Commit()
}

func (s SQLStore) Get(ctx context.Context, id string) (Record, error) {
	r, err := scanRecord(s.reader().QueryRowContext(ctx, s.q(`SELECT `+columns+` FROM job WHERE id = ?`), id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("job %s: %w", id, jobkit.ErrNotFound)
	}
	return r, err
}

func (s SQLStore) List(ctx context.Context, f Filter) ([]Record, error) {
	where, args := []string{"1 = 1"}, []any{}
	if f.By != "" {
		where, args = append(where, "by_subject = ?"), append(args, f.By)
	}
	if f.KindPrefix != "" {
		where, args = append(where, "substr(kind, 1, ?) = ?"), append(args, len(f.KindPrefix), f.KindPrefix)
	}
	if f.Target != "" {
		where, args = append(where, "target = ?"), append(args, f.Target)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 1000
	}
	rows, err := s.reader().QueryContext(ctx, s.q(`SELECT `+columns+` FROM job WHERE `+strings.Join(where, " AND ")+
		` ORDER BY created_at DESC, id DESC LIMIT `+fmt.Sprint(limit)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// put writes a whole record in a transaction.
func (s SQLStore) put(ctx context.Context, tx *sql.Tx, r Record) error {
	args, err := s.args(r)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, s.q(`UPDATE job SET kind = ?, label = ?, target = ?, status = ?, cancel_requested = ?, params = ?,
		by_subject = ?, project_id = ?, owner_org = ?, principal = ?, progress = ?, done = ?, total = ?, stats = ?, output = ?,
		result_name = ?, result_type = ?, result_size = ?, error = ?, worker = ?, created_at = ?, started_at = ?, ended_at = ?,
		updated_at = ?, lease_until = ?, log = ?, request = ? WHERE id = ?`), append(args[1:], r.ID)...)
	return err
}

func (s SQLStore) Claim(ctx context.Context, kinds []string, fn func(*Record) []outbox.Message) (Record, bool, error) {
	if len(kinds) == 0 {
		return Record{}, false, nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if s.Dollar {
		lock = " FOR UPDATE SKIP LOCKED" // PostgreSQL: two replicas claiming at once take two jobs
	}
	args := []any{jobkit.StatusQueued}
	for _, k := range kinds {
		args = append(args, k)
	}
	r, err := scanRecord(tx.QueryRowContext(ctx, s.q(`SELECT `+columns+` FROM job WHERE status = ? AND kind IN (?`+strings.Repeat(", ?", len(kinds)-1)+
		`) ORDER BY created_at, id LIMIT 1`+lock), args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	msgs := fn(&r)
	if err := s.put(ctx, tx, r); err != nil {
		return Record{}, false, err
	}
	if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
		return Record{}, false, err
	}
	return r, true, tx.Commit()
}

func (s SQLStore) Update(ctx context.Context, id string, fn func(*Record) ([]outbox.Message, error)) (Record, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = tx.Rollback() }()
	lock := ""
	if s.Dollar {
		lock = " FOR UPDATE"
	}
	r, err := scanRecord(tx.QueryRowContext(ctx, s.q(`SELECT `+columns+` FROM job WHERE id = ?`+lock), id))
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("job %s: %w", id, jobkit.ErrNotFound)
	}
	if err != nil {
		return Record{}, err
	}
	msgs, err := fn(&r)
	if err != nil {
		return Record{}, err
	}
	if err := s.put(ctx, tx, r); err != nil {
		return Record{}, err
	}
	if err := s.outbox().Append(ctx, tx, msgs...); err != nil {
		return Record{}, err
	}
	return r, tx.Commit()
}

func (s SQLStore) Expired(ctx context.Context, now time.Time) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, s.q(`SELECT id FROM job WHERE status = ? AND lease_until < ? ORDER BY id`), jobkit.StatusRunning, s.ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s SQLStore) ReadBlob(ctx context.Context, id, role string, offset int64, max int) ([]byte, bool, error) {
	rows, err := s.reader().QueryContext(ctx, s.q(`SELECT data FROM job_blob WHERE job_id = ? AND role = ? ORDER BY seq`), id, role)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []byte
	var pos int64
	for rows.Next() {
		var chunk []byte
		if err := rows.Scan(&chunk); err != nil {
			return nil, false, err
		}
		start, end := pos, pos+int64(len(chunk))
		pos = end
		if end <= offset {
			continue
		}
		if len(out) >= max {
			return out, false, rows.Err()
		}
		from := max64(0, offset-start)
		out = append(out, chunk[from:]...)
		if len(out) > max {
			return out[:max], false, rows.Err()
		}
	}
	return out, true, rows.Err()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (s SQLStore) AppendBlob(ctx context.Context, id, role string, data []byte) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var seq, size int64
	if err := tx.QueryRowContext(ctx, s.q(`SELECT COALESCE(MAX(seq) + 1, 0), COALESCE(SUM(length(data)), 0) FROM job_blob WHERE job_id = ? AND role = ?`), id, role).Scan(&seq, &size); err != nil {
		return 0, err
	}
	if len(data) > 0 {
		if _, err := tx.ExecContext(ctx, s.q(`INSERT INTO job_blob (job_id, role, seq, data) VALUES (?, ?, ?, ?)`), id, role, seq, data); err != nil {
			return 0, err
		}
	}
	return size + int64(len(data)), tx.Commit()
}

func (s SQLStore) Purge(ctx context.Context, before time.Time) (int, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	final := []any{jobkit.StatusDone, jobkit.StatusFailed, jobkit.StatusCancelled, s.ts(before)}
	if _, err := tx.ExecContext(ctx, s.q(`DELETE FROM job_blob WHERE job_id IN (SELECT id FROM job WHERE status IN (?, ?, ?) AND ended_at < ?)`), final...); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, s.q(`DELETE FROM job WHERE status IN (?, ?, ?) AND ended_at < ?`), final...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// reader is the database the plain reads go to.
func (s SQLStore) reader() *sql.DB {
	if s.Read != nil {
		return s.Read
	}
	return s.DB
}
