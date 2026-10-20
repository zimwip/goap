package jobsvc_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/labstack/echo/v5"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/lib/identity"
	"github.com/zimwip/goap/lib/jobkit"
	"github.com/zimwip/goap/lib/pgtest"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/lib/sqlschematest"
	"github.com/zimwip/goap/platform/jobsvc"
)

func TestSchemasAligned(t *testing.T) {
	sqlschematest.AssertAligned(t, "migrations", "migrations_sqlite")
}

func stores(t *testing.T) map[string]jobsvc.Store {
	t.Helper()
	ctx := context.Background()
	db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "j.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := platform.MigrateSQLite(ctx, db, "jobs", jobsvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
		t.Fatal(err)
	}
	all := map[string]jobsvc.Store{"memory": jobsvc.NewMemoryStore(), "sqlite": jobsvc.SQLStore{DB: db}}
	if os.Getenv("GOAP_TEST_PG_DSN") != "" {
		all["postgres"] = jobsvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, jobsvc.Migrations)), Dollar: true}
	}
	return all
}

var alice = authz.Principal{Subject: "alice", Project: "PROJ-ROOT"}

func as(p authz.Principal) context.Context { return authz.With(context.Background(), p) }

func wait(t *testing.T, s *jobsvc.Service, id string, final bool) jobsvc.Record {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		r, err := s.Get(as(alice), id)
		if err != nil {
			t.Fatal(err)
		}
		if !final || jobkit.Final(r.Status) {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("job %s did not end", id)
	return jobsvc.Record{}
}

// A job is created for a person, claimed by a worker of its kind, which reads its input, reports its progress, writes
// its result and ends it; the person reads it, the result included.
func TestJobRunsOnAWorker(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := &jobsvc.Service{Store: st}
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			input := bytes.Repeat([]byte("snapshot "), 300_000) // spans several chunks
			var seen authz.Principal
			w := &jobkit.Worker{Queue: s, ID: "w1", Wait: 200 * time.Millisecond, Kinds: map[string]jobkit.Runner{
				"test.copy": func(ctx context.Context, j *jobkit.Job) error {
					seen = authz.From(ctx)
					in, err := j.Input(ctx)
					if err != nil {
						return err
					}
					j.Step(1, 2)
					j.Progress("copying")
					out := j.Result(ctx, "copy.txt", "text/plain")
					if _, err := out.Write(in); err != nil {
						return err
					}
					if err := out.Close(); err != nil {
						return err
					}
					j.Set("bytes", int64(len(in)))
					j.Output(map[string]any{"ok": true})
					return nil
				}}}
			go w.Run(ctx)
			job, err := s.Create(ctx, jobkit.Spec{Kind: "test.copy", Label: "Copy", Target: "code", By: "alice", Principal: alice, Input: input})
			if err != nil {
				t.Fatal(err)
			}
			if job.Status != jobkit.StatusQueued {
				t.Fatalf("created %s", job.Status)
			}
			r := wait(t, s, job.Id, true)
			if r.Status != jobkit.StatusDone || r.Stats["bytes"] != int64(len(input)) || r.Output["ok"] != true || r.Done != 2 || r.Worker != "w1" {
				t.Fatalf("ended %+v", r)
			}
			if len(r.Log) != 1 || r.Log[0].Text != "copying" || r.Log[0].At.IsZero() {
				t.Fatalf("log %+v", r.Log)
			}
			if seen.Subject != "alice" {
				t.Fatalf("the run acts as %q", seen.Subject)
			}
			rec, read, err := s.Result(as(alice), job.Id)
			if err != nil || rec.ResultName != "copy.txt" || rec.ResultSize != int64(len(input)) {
				t.Fatalf("result %+v %v", rec, err)
			}
			var got []byte
			for off := int64(0); ; {
				data, eof, err := read(off)
				if err != nil {
					t.Fatal(err)
				}
				got, off = append(got, data...), off+int64(len(data))
				if eof {
					break
				}
			}
			if !bytes.Equal(got, input) {
				t.Fatalf("result of %d bytes, want %d", len(got), len(input))
			}
			// another person sees nothing of it
			if _, err := s.Get(as(authz.Principal{Subject: "bob"}), job.Id); !errors.Is(err, jobkit.ErrNotFound) {
				t.Fatalf("bob reads alice's job: %v", err)
			}
			if js, _ := s.List(as(authz.Principal{Subject: "bob"}), jobsvc.ListRequest{}); len(js) != 0 {
				t.Fatalf("bob lists %d jobs", len(js))
			}
			if _, err := s.List(as(authz.Principal{Subject: "bob"}), jobsvc.ListRequest{All: true}); !errors.Is(err, jobkit.ErrForbidden) {
				t.Fatalf("bob lists every job: %v", err)
			}
			if js, _ := s.List(as(authz.Principal{Subject: "root", Roles: []string{"admin"}}), jobsvc.ListRequest{All: true}); len(js) != 1 {
				t.Fatalf("the administrator lists %d jobs", len(js))
			}
			ws, _ := s.Workers(ctx)
			if len(ws) != 1 || ws[0].Kinds[0] != "test.copy" {
				t.Fatalf("workers %v", ws)
			}
		})
	}
}

// A queued job is cancelled at once; a running one when its worker reports, which cancels the context of its run.
func TestCancel(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			s := &jobsvc.Service{Store: st}
			queued, err := s.Create(context.Background(), jobkit.Spec{Kind: "test.none", By: "alice"})
			if err != nil {
				t.Fatal(err)
			}
			if r, err := s.Cancel(as(alice), queued.Id); err != nil || r.Status != jobkit.StatusCancelled {
				t.Fatalf("cancelled %+v %v", r, err)
			}
			ctx, stop := context.WithCancel(context.Background())
			defer stop()
			started := make(chan struct{})
			w := &jobkit.Worker{Queue: s, ID: "w1", Wait: 200 * time.Millisecond, Kinds: map[string]jobkit.Runner{
				"test.wait": func(ctx context.Context, j *jobkit.Job) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				}}}
			go w.Run(ctx)
			job, err := s.Create(ctx, jobkit.Spec{Kind: "test.wait", By: "alice"})
			if err != nil {
				t.Fatal(err)
			}
			<-started
			if r, err := s.Cancel(as(alice), job.Id); err != nil || !r.CancelRequested || r.Status != jobkit.StatusRunning {
				t.Fatalf("cancel asked %+v %v", r, err)
			}
			if r := wait(t, s, job.Id, true); r.Status != jobkit.StatusCancelled {
				t.Fatalf("ended %s", r.Status)
			}
		})
	}
}

// A running job whose worker stops reporting fails at the sweep once its lease lapsed; the worker's late report is
// refused.
func TestLeaseLapses(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			now := time.Now().UTC()
			var clock atomic.Int64
			clock.Store(now.UnixNano())
			s := &jobsvc.Service{Store: st, Now: func() time.Time { return time.Unix(0, clock.Load()) }}
			job, err := s.Create(context.Background(), jobkit.Spec{Kind: "test.lost", By: "alice"})
			if err != nil {
				t.Fatal(err)
			}
			c, err := s.Claim(context.Background(), "w1", []string{"test.lost"}, 0)
			if err != nil || c == nil || c.Job.Id != job.Id {
				t.Fatalf("claim %v %v", c, err)
			}
			clock.Add(int64(time.Minute))
			if err := s.Sweep(context.Background()); err != nil {
				t.Fatal(err)
			}
			if r, _ := s.Get(as(alice), job.Id); r.Status != jobkit.StatusFailed || !strings.Contains(r.Error, "lost") {
				t.Fatalf("after the sweep %+v", r)
			}
			if _, err := s.Report(context.Background(), job.Id, "w1", jobkit.Report{Progress: "late"}); !errors.Is(err, jobkit.ErrLost) {
				t.Fatalf("late report: %v", err)
			}
			// nothing left to claim
			if c, err := s.Claim(context.Background(), "w1", []string{"test.lost"}, 0); err != nil || c != nil {
				t.Fatalf("second claim %v %v", c, err)
			}
		})
	}
}

// The stream starts with the caller's recent jobs and sends every write of one.
func TestEventStream(t *testing.T) {
	s := &jobsvc.Service{Store: jobsvc.NewMemoryStore()}
	if _, err := s.Create(context.Background(), jobkit.Spec{Kind: "test.one", Label: "First", By: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), jobkit.Spec{Kind: "test.one", Label: "Bob's", By: "bob"}); err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	h := &jobsvc.Handler{Service: s, Identity: identity.Extractor{}}
	h.Mount(e)
	srv := httptest.NewServer(e)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/jobs/events", nil)
	identity.SetHeaders(alice, req.Header)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	events := make(chan [2]string, 8)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(nil, 1<<20)
		var ev string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				ev = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				events <- [2]string{ev, strings.TrimPrefix(line, "data: ")}
			}
		}
		close(events)
	}()
	next := func() [2]string {
		select {
		case ev := <-events:
			return ev
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return [2]string{}
	}
	snap := next()
	if snap[0] != "snapshot" || !strings.Contains(snap[1], "First") || strings.Contains(snap[1], "Bob") {
		t.Fatalf("snapshot %v", snap)
	}
	if _, err := s.Create(context.Background(), jobkit.Spec{Kind: "test.one", Label: "Second", By: "alice"}); err != nil {
		t.Fatal(err)
	}
	if ev := next(); ev[0] != "job" || !strings.Contains(ev[1], `"label":"Second"`) || !strings.Contains(ev[1], `"status":"queued"`) {
		t.Fatalf("event %v", ev)
	}
	// nobody's stream without a caller
	r, _ := http.Get(srv.URL + "/api/jobs/events")
	_, _ = io.Copy(io.Discard, r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous stream: %d", r.StatusCode)
	}
}
