package mcpsvc_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/stdlib"

	"github.com/zimwip/goap/lib/outbox"
	"github.com/zimwip/goap/lib/pgtest"
	"github.com/zimwip/goap/lib/platform"
	"github.com/zimwip/goap/mcp/mcpsvc"
)

type outboxStore interface {
	mcpsvc.Store
	outbox.Store
}

func outboxStores(t *testing.T) map[string]func(t *testing.T) outboxStore {
	return map[string]func(t *testing.T) outboxStore{
		"memory": func(*testing.T) outboxStore { return mcpsvc.NewMemoryStore() },
		"sqlite": func(t *testing.T) outboxStore {
			ctx := context.Background()
			db, err := platform.OpenSQLite(ctx, filepath.Join(t.TempDir(), "goap.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if err := platform.MigrateSQLite(ctx, db, "mcp", mcpsvc.SQLiteMigrations, "migrations_sqlite"); err != nil {
				t.Fatal(err)
			}
			return mcpsvc.SQLStore{DB: db}
		},
		"postgres": func(t *testing.T) outboxStore {
			return mcpsvc.SQLStore{DB: stdlib.OpenDBFromPool(pgtest.Pool(t, mcpsvc.Migrations)), Dollar: true}
		},
	}
}

func TestStoreOutbox(t *testing.T) {
	for name, open := range outboxStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			st := open(t)
			now := time.Now().UTC().Truncate(time.Millisecond)
			msg := func(subject string) outbox.Message {
				m, err := outbox.New(ctx, subject, map[string]string{"s": subject}, now)
				if err != nil {
					t.Fatal(err)
				}
				return m
			}
			pending := func() []string {
				ms, err := st.Pending(ctx, 10)
				if err != nil {
					t.Fatal(err)
				}
				var out []string
				for _, m := range ms {
					out = append(out, m.Subject)
				}
				return out
			}
			refused := errors.New("refused")
			reg := mcpsvc.ConnectorReg{Info: localfsInfo(), Endpoint: "http://a", LastSeen: now}
			if err := st.SaveConnector(ctx, reg, func(mcpsvc.ConnectorReg, bool) ([]outbox.Message, error) {
				return []outbox.Message{msg("refused")}, refused
			}); !errors.Is(err, refused) {
				t.Fatalf("refused save = %v", err)
			}
			if _, err := st.Connector(ctx, "localfs"); !errors.Is(err, mcpsvc.ErrNotFound) {
				t.Fatalf("refused save wrote the row: %v", err)
			}
			if p := pending(); len(p) != 0 {
				t.Fatalf("refused save wrote events: %v", p)
			}
			if err := st.SaveConnector(ctx, reg, func(prev mcpsvc.ConnectorReg, found bool) ([]outbox.Message, error) {
				if found {
					t.Errorf("first save found %+v", prev)
				}
				return []outbox.Message{msg("first")}, nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := st.SaveConnector(ctx, reg, func(prev mcpsvc.ConnectorReg, found bool) ([]outbox.Message, error) {
				if !found || prev.Endpoint != "http://a" || !prev.LastSeen.Equal(now) {
					t.Errorf("second save prev = %+v %v", prev, found)
				}
				return nil, nil
			}); err != nil {
				t.Fatal(err)
			}
			if ok, err := st.MarkLost(ctx, "localfs", now.Add(-time.Second), now, msg("stale")); err != nil || ok {
				t.Fatalf("lost on a stale renewal = %v %v", ok, err)
			}
			if ok, err := st.MarkLost(ctx, "localfs", now, now.Add(time.Minute), msg("lost")); err != nil || !ok {
				t.Fatalf("lost = %v %v", ok, err)
			}
			if ok, err := st.MarkLost(ctx, "localfs", now, now.Add(time.Minute), msg("again")); err != nil || ok {
				t.Fatalf("lost twice = %v %v", ok, err)
			}
			if r, err := st.Connector(ctx, "localfs"); err != nil || r.LostAt.IsZero() {
				t.Fatalf("lost registration = %+v %v", r, err)
			}
			if p := pending(); !slices.Equal(p, []string{"first", "lost"}) {
				t.Fatalf("pending = %v", p)
			}
			ms, _ := st.Pending(ctx, 10)
			if err := st.MarkPublished(ctx, []int64{ms[0].Seq, ms[1].Seq}, now); err != nil {
				t.Fatal(err)
			}
			if p := pending(); len(p) != 0 {
				t.Fatalf("published still pending: %v", p)
			}
			if err := st.PurgePublished(ctx, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type recorder struct {
	mu   sync.Mutex
	msgs []outbox.Message
}

func (r *recorder) Deliver(_ context.Context, m outbox.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *recorder) wait(t *testing.T, n int) []outbox.Message {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		r.mu.Lock()
		got := slices.Clone(r.msgs)
		r.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
	}
}

func TestServiceEmits(t *testing.T) {
	for name, open := range outboxStores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Millisecond)
			svc := &mcpsvc.Service{Store: open(t), Now: func() time.Time { return now }, Lease: time.Minute}
			rec := &recorder{}
			if svc.Emit(rec) == nil {
				t.Fatal("no relay")
			}
			register := func(endpoint string) {
				t.Helper()
				if _, err := svc.RegisterConnector(ctx, localfsInfo(), endpoint); err != nil {
					t.Fatal(err)
				}
			}
			register("http://a")
			rec.wait(t, 1)
			now = now.Add(time.Second)
			register("http://a") // a renewal: no event
			register("http://b") // another endpoint: registered
			rec.wait(t, 2)
			now = now.Add(2 * time.Minute)
			if err := svc.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if err := svc.Sweep(ctx); err != nil { // reported once
				t.Fatal(err)
			}
			rec.wait(t, 3)
			register("http://b") // back
			rec.wait(t, 4)
			now = now.Add(2 * time.Minute)
			register("http://b") // expired, not swept: lost then back
			got := rec.wait(t, 6)
			time.Sleep(20 * time.Millisecond) // nothing more comes
			if extra := rec.wait(t, 0); len(extra) != len(got) {
				got = extra
			}
			var subjects []string
			for _, m := range got {
				subjects = append(subjects, m.Subject)
			}
			want := []string{"registered", "registered", "lost", "back", "lost", "back"}
			for i := range want {
				want[i] = mcpsvc.ConnectorSubject("localfs", want[i])
			}
			if !slices.Equal(subjects, want) {
				t.Fatalf("subjects = %v, want %v", subjects, want)
			}
			var ev mcpsvc.ConnectorEvent
			if err := json.Unmarshal(got[1].Data, &ev); err != nil || ev.Connector != "localfs" || ev.Endpoint != "http://b" || len(ev.Operations) != 3 {
				t.Fatalf("registered payload = %s %v", got[1].Data, err)
			}
			if got[0].EventID() != "mcp:1" {
				t.Fatalf("event id = %s", got[0].EventID())
			}
		})
	}
}

func TestServiceWithoutEmitWritesNoEvent(t *testing.T) {
	ctx := context.Background()
	st := mcpsvc.NewMemoryStore()
	svc := &mcpsvc.Service{Store: st}
	if _, err := svc.RegisterConnector(ctx, localfsInfo(), "http://a"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Pending(ctx, 10); len(p) != 0 {
		t.Fatalf("events without Emit: %+v", p)
	}
}
