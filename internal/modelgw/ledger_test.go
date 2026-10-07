package modelgw

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/gen/goap/model/v1/modelv1connect"
	"github.com/zimwip/goap/internal/identity"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/llm"
)

// ledgerService is a service with a "default" alias on a fake model reporting 3 input and 4 output tokens.
func ledgerService(t *testing.T, st Store, roles ...string) (*Service, ModelEntry) {
	t.Helper()
	svc, g := newService(st)
	echo := ModelEntry{Provider: "fake", Model: "echo", Enabled: true, QuotaTokens: 100, QuotaPeriod: PeriodTotal, Roles: roles}
	seed(t, g, []ProviderRecord{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}}, []ModelEntry{echo},
		[]AliasEntry{{Alias: "default", Target: "fake/echo"}, {Alias: "embed", Target: "fake/echo"}})
	svc.Router.Instrument = func(ctx context.Context, _, _ string, _ llm.Request, call func(context.Context) (llm.Response, error)) (llm.Response, error) {
		r, err := call(ctx)
		if strings.Contains(r.Text, "boom") {
			return r, errors.New("provider failed")
		}
		r.Usage = llm.Usage{InputTokens: 3, OutputTokens: 4}
		return r, err
	}
	return svc, echo
}

func calls(t *testing.T, s *Service, f UsageFilter) []Call {
	t.Helper()
	cs, _, _, err := s.ListCalls(authz.With(context.Background(), authz.Principal{Subject: "root"}), f, true)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

func TestLedgerRecordsOkErrorAndRefused(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, echo := ledgerService(t, st, "methodologist")
			meth := authz.With(context.Background(), authz.Principal{Subject: "m", Org: "ORG-A", Project: "P1", Roles: []string{"methodologist"}})
			meta := llm.CallMeta{Source: llm.SourceEngine, ProcessID: "proc-1", ChangeID: "CHG-1", Step: 2, Call: 1, Action: "draft", Agent: "writer", ConversationID: "c-1"}
			ok := llm.WithMeta(meth, meta)

			if _, err := svc.Complete(ok, llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
				t.Fatal(err)
			}
			if _, err := svc.Complete(llm.WithMeta(meth, llm.CallMeta{Source: llm.SourceAssistant}), llm.Request{Model: "fake/echo", Messages: []llm.Message{{Role: "user", Content: "boom"}}}); err == nil {
				t.Fatal("the provider failure must be returned")
			}
			// refused: forbidden, unknown alias, quota
			forb := authz.With(context.Background(), authz.Principal{Subject: "u", Roles: []string{"contributor"}})
			if _, err := svc.Complete(forb, llm.Request{}); !errors.Is(err, ErrForbidden) {
				t.Fatalf("forbidden: %v", err)
			}
			if _, err := svc.Complete(meth, llm.Request{Model: "nope"}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("unknown alias: %v", err)
			}
			_ = st.AddUsage(context.Background(), echo.Key(), PeriodTotal, 100)
			if _, err := svc.Complete(meth, llm.Request{}); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("quota must still be enforced: %v", err)
			}
			if _, err := svc.Embed(meth, llm.EmbedRequest{Texts: []string{"x"}}); !errors.Is(err, ErrQuotaExceeded) {
				t.Fatalf("embed quota: %v", err)
			}

			got := calls(t, svc, UsageFilter{})
			if len(got) != 6 {
				t.Fatalf("every call is a row, refusals included: %+v", got)
			}
			a := got[0]
			if a.Subject != "m" || a.Project != "P1" || a.Org != "ORG-A" || a.Alias != "default" || a.Provider != "fake" || a.Model != "echo" || a.Kind != KindComplete ||
				a.InputTokens != 3 || a.OutputTokens != 4 || a.Error != "" || a.Source != "engine" || a.ProcessID != "proc-1" || a.ChangeID != "CHG-1" ||
				a.Step != 2 || a.CallIndex != 1 || a.Action != "draft" || a.Agent != "writer" || a.ConversationID != "c-1" || a.Seq == 0 || a.At.IsZero() {
				t.Fatalf("ok row: %+v", a)
			}
			if b := got[1]; b.Error == "" || b.Alias != "" || b.Source != "assistant" || b.Step != llm.NoStep || b.CallIndex != llm.NoStep {
				t.Fatalf("a literal model, a failure, no process: %+v", b)
			}
			if c := got[2]; !strings.Contains(c.Error, "forbidden") || c.Subject != "u" || c.Source != llm.SourceOther || c.InputTokens != 0 {
				t.Fatalf("forbidden row: %+v", c)
			}
			if d := got[3]; !strings.Contains(d.Error, "nope") || d.Alias != "nope" || d.Provider != "" {
				t.Fatalf("unresolved row: %+v", d)
			}
			if e := got[4]; !strings.Contains(e.Error, "quota") || e.Provider != "fake" {
				t.Fatalf("quota row: %+v", e)
			}
			if f := got[5]; f.Kind != KindEmbed || f.Alias != "embed" || !strings.Contains(f.Error, "quota") {
				t.Fatalf("embed row: %+v", f)
			}
			if got[5].Seq <= got[0].Seq {
				t.Fatal("seq must grow")
			}
		})
	}
}

func TestLedgerEmbedTokens(t *testing.T) {
	svc, _ := ledgerService(t, NewMemoryStore())
	if _, err := svc.Embed(context.Background(), llm.EmbedRequest{Texts: []string{"a b c"}}); err != nil {
		t.Fatal(err)
	}
	cs := calls(t, svc, UsageFilter{})
	if len(cs) != 1 || cs[0].Kind != KindEmbed || cs[0].Subject != "" {
		t.Fatalf("%+v", cs)
	}
}

// rpcClient serves the handler over HTTP; the client forwards the principal of its context.
func rpcClient(t *testing.T, svc *Service, authzr authz.Authorizer) (*Client, modelv1connect.ModelServiceClient) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(modelv1connect.NewModelServiceHandler(&Handler{Service: svc, Authz: authzr}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient(srv.Client(), srv.URL, identity.Forward()), modelv1connect.NewModelServiceClient(srv.Client(), srv.URL, identity.Forward())
}

func TestLedgerOverRPC(t *testing.T) {
	svc, _ := ledgerService(t, NewMemoryStore())
	c, _ := rpcClient(t, svc, nil)
	ctx := authz.With(context.Background(), authz.Principal{Subject: "alice", Project: "P9"})
	ctx = llm.WithMeta(ctx, llm.CallMeta{Source: "Evil Source!", ProcessID: "p-7", ChangeID: "CHG-7", Step: 4, Call: 2, Action: "act", Agent: "ag", ConversationID: "cv"})
	if _, err := c.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	ctx = llm.WithMeta(authz.With(context.Background(), authz.Principal{Subject: "alice"}), llm.CallMeta{Source: llm.SourceIndexer})
	if _, err := c.Embed(ctx, llm.EmbedRequest{Texts: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	cs := calls(t, svc, UsageFilter{})
	if len(cs) != 2 {
		t.Fatalf("%+v", cs)
	}
	a := cs[0]
	if a.Subject != "alice" || a.Project != "P9" {
		t.Fatalf("the subject and the project are the principal's: %+v", a)
	}
	if a.Source != llm.SourceOther {
		t.Fatalf("an invalid source is sanitised: %q", a.Source)
	}
	if a.ProcessID != "p-7" || a.ChangeID != "CHG-7" || a.Step != 4 || a.CallIndex != 2 || a.Action != "act" || a.Agent != "ag" || a.ConversationID != "cv" {
		t.Fatalf("correlation over RPC: %+v", a)
	}
	if cs[1].Source != llm.SourceIndexer || cs[1].Kind != KindEmbed || cs[1].Step != llm.NoStep {
		t.Fatalf("embed over RPC: %+v", cs[1])
	}
}

func TestMetaIsSanitised(t *testing.T) {
	long := strings.Repeat("x", 500)
	m := llm.CallMeta{Source: strings.Repeat("a", 41), ProcessID: "p\x00\n1", ChangeID: long, Action: long, Step: 1, Call: 1}.Clean()
	if m.Source != llm.SourceOther || m.ProcessID != "p1" || len(m.ChangeID) != llm.MaxIDLen || len(m.Action) != llm.MaxNameLen {
		t.Fatalf("%+v", m)
	}
	if m := (llm.CallMeta{Source: "assistant", Step: 3, Call: 3}).Clean(); m.Step != llm.NoStep || m.Call != llm.NoStep {
		t.Fatalf("step and call mean something with a process only: %+v", m)
	}
	if m := (llm.CallMeta{Source: " Engine ", ProcessID: "p"}).Clean(); m.Source != "engine" || m.Step != 0 {
		t.Fatalf("%+v", m)
	}
}

// adminOnly authorizes the admin action on the platform to the subject "root".
type adminOnly struct{}

func (adminOnly) Authorize(_ context.Context, r authz.Request) (bool, error) {
	return r.Subject.Subject == "root", nil
}

func TestLedgerVisibility(t *testing.T) {
	svc, _ := ledgerService(t, NewMemoryStore())
	c, rpc := rpcClient(t, svc, adminOnly{})
	as := func(sub string) context.Context {
		return authz.With(context.Background(), authz.Principal{Subject: sub})
	}
	for _, sub := range []string{"alice", "alice", "bob"} {
		if _, err := c.Complete(as(sub), llm.Request{}); err != nil {
			t.Fatal(err)
		}
	}
	list := func(ctx context.Context, f *modelv1.UsageFilter) (*modelv1.ListUsageResponse, error) {
		r, err := rpc.ListUsage(ctx, connect.NewRequest(&modelv1.ListUsageRequest{Filter: f}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	if r, err := list(as("alice"), nil); err != nil || len(r.Calls) != 2 {
		t.Fatalf("a caller reads its own calls only: %v %+v", err, r)
	}
	if r, err := list(as("alice"), &modelv1.UsageFilter{Subject: "alice"}); err != nil || len(r.Calls) != 2 {
		t.Fatalf("naming oneself: %v", err)
	}
	if _, err := list(as("alice"), &modelv1.UsageFilter{Subject: "bob"}); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("another subject is refused: %v", err)
	}
	if r, err := list(as("root"), nil); err != nil || len(r.Calls) != 3 {
		t.Fatalf("an administrator reads the platform: %v %+v", err, r)
	}
	if r, err := list(as("root"), &modelv1.UsageFilter{Subject: "bob"}); err != nil || len(r.Calls) != 1 || r.Calls[0].Subject != "bob" {
		t.Fatalf("an administrator reads any subject: %v %+v", err, r)
	}
	// the summary has the same visibility
	sum := func(ctx context.Context, group string, f *modelv1.UsageFilter) ([]*modelv1.UsageSummaryRow, error) {
		r, err := rpc.UsageSummary(ctx, connect.NewRequest(&modelv1.UsageSummaryRequest{Filter: f, GroupBy: group}))
		if err != nil {
			return nil, err
		}
		return r.Msg.Rows, nil
	}
	if rows, err := sum(as("alice"), "subject", nil); err != nil || len(rows) != 1 || rows[0].Key != "alice" || rows[0].Calls != 2 || rows[0].InputTokens != 6 || rows[0].OutputTokens != 8 {
		t.Fatalf("own summary: %v %+v", err, rows)
	}
	if rows, err := sum(as("root"), "subject", nil); err != nil || len(rows) != 2 || rows[0].Key != "alice" {
		t.Fatalf("platform summary: %v %+v", err, rows)
	}
	if _, err := sum(as("alice"), "bogus", nil); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("group_by is validated: %v", err)
	}
	// without identity nothing is readable
	if _, err := list(context.Background(), nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestLedgerCursorAndSummary(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, _ := ledgerService(t, st)
			ctx := context.Background()
			day := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
			add := func(at time.Time, c Call) {
				c.At = at
				if c.Source == "" {
					c.Source = "engine"
				}
				c.Step, c.CallIndex = -1, -1
				if _, err := st.AppendCall(ctx, c, nil); err != nil {
					t.Fatal(err)
				}
			}
			add(day, Call{Subject: "a", Provider: "p", Model: "m1", Alias: "default", InputTokens: 10, OutputTokens: 5, DurationMs: 100, ProcessID: "x", Action: "A1", Agent: "G"})
			add(day.Add(20*time.Minute), Call{Subject: "a", Provider: "p", Model: "m1", Alias: "fast", InputTokens: 1, OutputTokens: 1, DurationMs: 50, Error: "e", Source: "assistant"})
			add(day.Add(2*time.Hour), Call{Subject: "b", Provider: "p", Model: "m2", Alias: "default", InputTokens: 100, DurationMs: 10, ProcessID: "x", Action: "A2"})
			add(day.Add(26*time.Hour), Call{Subject: "b", Provider: "p", Model: "m2", Alias: "default", InputTokens: 7, OutputTokens: 3, DurationMs: 1})

			group := func(g string, f UsageFilter) []SummaryRow {
				rows, err := svc.Summary(ctx, f, g, true)
				if err != nil {
					t.Fatal(err)
				}
				return rows
			}
			byModel := group(GroupModel, UsageFilter{})
			if len(byModel) != 2 || byModel[0] != (SummaryRow{Key: "m2", Calls: 2, Input: 107, Output: 3, DurationMs: 11}) ||
				byModel[1] != (SummaryRow{Key: "m1", Calls: 2, Input: 11, Output: 6, Errors: 1, DurationMs: 150}) {
				t.Fatalf("model: %+v", byModel)
			}
			if d := group(GroupDay, UsageFilter{}); len(d) != 2 || d[0].Key != "2026-09-01" || d[0].Calls != 3 || d[1].Key != "2026-09-02" {
				t.Fatalf("day: %+v", d)
			}
			if h := group(GroupHour, UsageFilter{}); len(h) != 3 || h[0].Key != "2026-09-01T10" || h[0].Calls != 2 || h[1].Key != "2026-09-01T12" {
				t.Fatalf("hour: %+v", h)
			}
			if s := group(GroupSource, UsageFilter{}); len(s) != 2 || s[0].Key != "engine" || s[1].Key != "assistant" {
				t.Fatalf("source: %+v", s)
			}
			if s := group(GroupAlias, UsageFilter{Subject: "a"}); len(s) != 2 || s[0].Key != "default" {
				t.Fatalf("alias: %+v", s)
			}
			if s := group(GroupProcess, UsageFilter{ProcessID: "x"}); len(s) != 1 || s[0].Calls != 2 {
				t.Fatalf("process: %+v", s)
			}
			if s := group(GroupAction, UsageFilter{ProcessID: "x"}); len(s) != 2 || s[0].Key != "A2" {
				t.Fatalf("action: %+v", s)
			}
			if s := group(GroupAgent, UsageFilter{ProcessID: "x"}); len(s) != 2 {
				t.Fatalf("agent: %+v", s)
			}
			// filters: time window, model as provider/model, source
			if s := group(GroupModel, UsageFilter{From: day.Add(time.Hour), To: day.Add(24 * time.Hour)}); len(s) != 1 || s[0].Key != "m2" || s[0].Calls != 1 {
				t.Fatalf("window: %+v", s)
			}
			if s := group(GroupModel, UsageFilter{Model: "p/m1", Source: "engine"}); len(s) != 1 || s[0].Calls != 1 {
				t.Fatalf("model filter: %+v", s)
			}

			// the cursor: latest N without after_seq, the next ones after it
			all := calls(t, svc, UsageFilter{})
			if len(all) != 4 {
				t.Fatalf("%+v", all)
			}
			last2, _, more, err := svc.ListCalls(ctx, UsageFilter{Limit: 2}, true)
			if err != nil || !more || len(last2) != 2 || last2[0].Seq != all[2].Seq || last2[1].Seq != all[3].Seq {
				t.Fatalf("latest two, ascending: %v %v %+v", err, more, last2)
			}
			first2, next, more, _ := svc.ListCalls(ctx, UsageFilter{AfterSeq: all[0].Seq, Limit: 2}, true)
			if !more || len(first2) != 2 || first2[0].Seq != all[1].Seq || next != all[2].Seq {
				t.Fatalf("after the first, two: %v %+v %d", more, first2, next)
			}
			rest, next, more, _ := svc.ListCalls(ctx, UsageFilter{AfterSeq: next, Limit: 2}, true)
			if more || len(rest) != 1 || next != all[3].Seq {
				t.Fatalf("the rest: %v %+v %d", more, rest, next)
			}
			none, next, more, _ := svc.ListCalls(ctx, UsageFilter{AfterSeq: next}, true)
			if len(none) != 0 || more || next != all[3].Seq {
				t.Fatalf("nothing new keeps the cursor: %+v %d", none, next)
			}
			if got := all[0]; !got.At.Equal(day) || got.Model != "m1" || got.Provider != "p" {
				t.Fatalf("round trip: %+v", got)
			}
		})
	}
}

func TestLedgerRetention(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, _ := ledgerService(t, st)
			ctx := context.Background()
			now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
			svc.Now = func() time.Time { return now }
			for _, age := range []int{200, 91, 89, 1} {
				if _, err := st.AppendCall(ctx, Call{At: now.AddDate(0, 0, -age), Source: "engine", Step: -1, CallIndex: -1}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if n, err := svc.PurgeCalls(ctx, 0); err != nil || n != 0 {
				t.Fatalf("0 keeps everything: %d %v", n, err)
			}
			if n, err := svc.PurgeCalls(ctx, 90); err != nil || n != 2 {
				t.Fatalf("purge: %d %v", n, err)
			}
			if got := calls(t, svc, UsageFilter{}); len(got) != 2 {
				t.Fatalf("%+v", got)
			}
			// the quota counter is not the ledger: it outlives the purge
			_ = st.AddUsage(ctx, "LLM:x", PeriodTotal, 5)
			if _, err := svc.PurgeCalls(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if n, _ := st.Usage(ctx, "LLM:x", PeriodTotal); n != 5 {
				t.Fatalf("usage counter: %d", n)
			}
		})
	}
}
