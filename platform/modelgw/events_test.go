package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/llm"
	"github.com/zimwip/goap/lib/outbox"
)

func TestStoreWritesEventsWithTheRow(t *testing.T) {
	ctx := context.Background()
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			es := st.(EmittingStore)
			at := time.Now().UTC()
			// without events: the row only
			if _, err := es.AppendCall(ctx, Call{At: at, Source: "assistant", Step: -1, CallIndex: -1}, nil); err != nil {
				t.Fatal(err)
			}
			if p, _ := es.Pending(ctx, 10); len(p) != 0 {
				t.Fatalf("a row written without events writes none: %+v", p)
			}
			seq, err := es.AppendCallEmitting(ctx, Call{At: at, Subject: "u", Source: "assistant", Step: -1, CallIndex: -1}, &Exchange{Response: "secret answer"},
				callEvents(ctx, OutcomeServed, nil))
			if err != nil {
				t.Fatal(err)
			}
			p, err := es.Pending(ctx, 10)
			if err != nil || len(p) != 1 || p[0].Subject != CallSubject(seq) {
				t.Fatalf("the event is written with its row: %+v %v", p, err)
			}
			var ev CallRecorded
			if err := json.Unmarshal(p[0].Data, &ev); err != nil || ev.Seq != seq || ev.Subject != "u" || ev.Outcome != OutcomeServed {
				t.Fatalf("payload: %+v %v", ev, err)
			}
			if strings.Contains(string(p[0].Data), "secret answer") {
				t.Fatal("an event never carries the exchange")
			}
			if err := es.MarkPublished(ctx, []int64{p[0].Seq}, time.Now()); err != nil {
				t.Fatal(err)
			}

			// a row that is not written writes no event
			boom := errors.New("boom")
			if _, err := es.AppendCallEmitting(ctx, Call{At: at, Subject: "v", Step: -1, CallIndex: -1}, nil,
				func(Call) ([]outbox.Message, error) { return nil, boom }); !errors.Is(err, boom) {
				t.Fatalf("the failure is returned: %v", err)
			}
			if p, _ := es.Pending(ctx, 10); len(p) != 0 {
				t.Fatalf("no event for a row not written: %+v", p)
			}
			if got, _, _ := es.Calls(ctx, UsageFilter{Subject: "v"}); len(got) != 0 {
				t.Fatalf("no row either: %+v", got)
			}
			if err := es.PurgePublished(ctx, time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// eventRecorder is a sink keeping what it is given.
type eventRecorder struct {
	mu   sync.Mutex
	msgs []outbox.Message
}

func (r *eventRecorder) Deliver(_ context.Context, m outbox.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, m)
	return nil
}

func (r *eventRecorder) subjects() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.msgs {
		out = append(out, m.Subject)
	}
	return out
}

func TestServiceEmitsLedgerEvents(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, echo := ledgerService(t, st)
			rec := &eventRecorder{}
			relay := svc.Emit(rec)
			if relay == nil || svc.Relay() != relay || svc.Emit(nil) != nil {
				t.Fatal("Emit returns the relay; a nil sink none")
			}
			who := authz.With(context.Background(), authz.Principal{Subject: "a.b@x.org", Project: "P1"})
			ctx := llm.WithMeta(who, llm.CallMeta{Source: llm.SourceAssistant, ChangeID: "CHG-1", Step: llm.NoStep, Call: llm.NoStep})
			if _, err := svc.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "the prompt"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "boom"}}}); err == nil {
				t.Fatal("the provider failure is returned")
			}
			_ = st.AddUsage(context.Background(), echo.Key(), PeriodTotal, 100)
			if _, err := svc.Complete(ctx, llm.Request{}); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("quota: %v", err)
			}
			svc.drains.Wait()
			if err := relay.Drain(context.Background()); err != nil {
				t.Fatal(err)
			}
			rows := calls(t, svc, UsageFilter{})
			if len(rows) != 3 {
				t.Fatalf("rows: %+v", rows)
			}
			want := []string{CallSubject(rows[0].Seq), CallSubject(rows[1].Seq), CallSubject(rows[2].Seq), "goap.model.quota.a_b@x_org.exceeded"}
			if got := rec.subjects(); strings.Join(got, " ") != strings.Join(want, " ") {
				t.Fatalf("subjects, in order:\n got %v\nwant %v", got, want)
			}
			for i, outcome := range []string{OutcomeServed, OutcomeFailed, OutcomeRefused} {
				var ev CallRecorded
				if err := json.Unmarshal(rec.msgs[i].Data, &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Outcome != outcome || ev.Seq != rows[i].Seq || ev.Subject != "a.b@x.org" || ev.ChangeID != "CHG-1" || ev.Source != llm.SourceAssistant {
					t.Fatalf("event %d: %+v", i, ev)
				}
				if rec.msgs[i].EventID() != OutboxOrigin+":"+strconv.FormatInt(rec.msgs[i].Seq, 10) {
					t.Fatalf("event id: %s", rec.msgs[i].EventID())
				}
			}
			var first CallRecorded
			_ = json.Unmarshal(rec.msgs[0].Data, &first)
			if first.InputTokens != 3 || first.OutputTokens != 4 || first.Provider != "fake" || first.Model != "echo" || first.Alias != "default" {
				t.Fatalf("served call: %+v", first)
			}
			for _, m := range rec.msgs {
				if strings.Contains(string(m.Data), "the prompt") {
					t.Fatalf("an event never carries the prompt: %s", m.Data)
				}
			}
			var q QuotaExceeded
			if err := json.Unmarshal(rec.msgs[3].Data, &q); err != nil {
				t.Fatal(err)
			}
			if q.Seq != rows[2].Seq || q.Subject != "a.b@x.org" || q.Limit != 100 || q.Used < 100 || q.Period != PeriodTotal || q.Model != "echo" {
				t.Fatalf("quota event: %+v", q)
			}
			if p, _ := st.(EmittingStore).Pending(context.Background(), 10); len(p) != 0 {
				t.Fatalf("everything was delivered: %+v", p)
			}
		})
	}
}

func TestServiceWithoutEmitWritesNoEvent(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, _ := ledgerService(t, st)
			if _, err := svc.Complete(authz.With(context.Background(), authz.Principal{Subject: "u"}), llm.Request{}); err != nil {
				t.Fatal(err)
			}
			if p, _ := st.(EmittingStore).Pending(context.Background(), 10); len(p) != 0 {
				t.Fatalf("no outbox without Emit: %+v", p)
			}
		})
	}
}
