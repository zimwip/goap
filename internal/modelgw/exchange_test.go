package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/engine"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/graph/graphtest"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

func as(sub string) context.Context {
	return authz.With(context.Background(), authz.Principal{Subject: sub})
}

// The exchange of a call is stored by the gateway unless a change log keeps it (ADR 0089): an engine call of a change.
func TestExchangeStoredWhereNoChangeLogKeepsIt(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, echo := ledgerService(t, st)
			msgs := []llm.Message{{Role: "user", Content: "hello"}, {Role: "assistant", Content: "hi"}, {Role: "user", Content: "again"}}
			do := func(ctx context.Context, meta llm.CallMeta, req llm.Request) {
				t.Helper()
				_, _ = svc.Complete(llm.WithMeta(ctx, meta), req)
			}
			do(as("alice"), llm.CallMeta{Source: llm.SourceAssistant, ConversationID: "c1"}, llm.Request{System: "be brief", Messages: msgs})
			do(as("alice"), llm.CallMeta{Source: llm.SourceEngine, ProcessID: "p", ChangeID: "CHG-1", Step: 0, Call: 0}, llm.Request{Messages: msgs[:1]})
			do(as("alice"), llm.CallMeta{Source: llm.SourceEngine, ProcessID: "p", Step: 0, Call: 0}, llm.Request{Messages: msgs[:1]})     // no change
			do(as("alice"), llm.CallMeta{Source: llm.SourceHelper}, llm.Request{Messages: []llm.Message{{Role: "user", Content: "boom"}}}) // provider failure
			do(as("alice"), llm.CallMeta{Source: llm.SourceHelper}, llm.Request{Model: "nope", Messages: msgs[:1]})                        // refused
			long := strings.Repeat("é", journal.MaxExchangeText)                                                                           // 2 bytes each
			do(as("alice"), llm.CallMeta{Source: llm.SourceAssistant}, llm.Request{System: long, Messages: []llm.Message{{Role: "user", Content: long}}})
			texts := []string{"alpha", strings.Repeat("b", 1000), "c", "d", "e", "f", "g"}
			if _, err := svc.Embed(llm.WithMeta(as("alice"), llm.CallMeta{Source: llm.SourceIndexer}), llm.EmbedRequest{Texts: texts}); err != nil {
				t.Fatal(err)
			}
			got := calls(t, svc, UsageFilter{})
			if len(got) != 7 {
				t.Fatalf("%+v", got)
			}
			want := []bool{true, false, true, true, true, true, true}
			for i, c := range got {
				if c.HasExchange != want[i] {
					t.Fatalf("row %d (%s): has_exchange %v", i, c.Source, c.HasExchange)
				}
				_, err := st.Exchange(context.Background(), c.Seq)
				if (err == nil) != want[i] {
					t.Fatalf("row %d: stored %v", i, err)
				}
			}
			x, _ := st.Exchange(context.Background(), got[0].Seq)
			if x.System != "be brief" || len(x.Messages) != 3 || x.Messages[2].Content != "again" || x.Messages[1].Role != "assistant" || x.Response == "" || x.Truncated {
				t.Fatalf("round trip: %+v", x)
			}
			if x, _ := st.Exchange(context.Background(), got[3].Seq); len(x.Messages) != 1 || got[3].Error == "" {
				t.Fatalf("a failed call keeps its request: %+v %q", x, got[3].Error)
			}
			if x, _ := st.Exchange(context.Background(), got[4].Seq); len(x.Messages) != 1 || !strings.Contains(got[4].Error, "nope") {
				t.Fatalf("a refused call keeps its request: %+v", x)
			}
			x, _ = st.Exchange(context.Background(), got[5].Seq)
			if !x.Truncated || len(x.System) > journal.MaxExchangeText || len(x.Messages[0].Content) > journal.MaxExchangeText || len(x.System) < journal.MaxExchangeText-4 {
				t.Fatalf("caps: truncated %v, %d %d", x.Truncated, len(x.System), len(x.Messages[0].Content))
			}
			x, _ = st.Exchange(context.Background(), got[6].Seq)
			if len(x.Messages) != 1 || x.Messages[0].Role != "texts" || !strings.HasPrefix(x.Messages[0].Content, "7 texts\n- alpha\n- bbb") ||
				!strings.Contains(x.Messages[0].Content, "... 2 more") || len(x.Messages[0].Content) > 5*maxPreviewText+200 {
				t.Fatalf("embed preview: %q", x.Messages[0].Content)
			}
			if !strings.Contains(x.Response, "vectors") && x.Response != "" {
				t.Fatalf("embed answer: %q", x.Response)
			}
			_ = echo
		})
	}
}

func TestExchangeStorageCanBeOff(t *testing.T) {
	svc, _ := ledgerService(t, NewMemoryStore())
	svc.CallPrompts = false
	if _, err := svc.Complete(llm.WithMeta(as("alice"), llm.CallMeta{Source: llm.SourceAssistant}), llm.Request{Messages: []llm.Message{{Role: "user", Content: "secret"}}}); err != nil {
		t.Fatal(err)
	}
	cs := calls(t, svc, UsageFilter{})
	if len(cs) != 1 || cs[0].HasExchange || cs[0].InputTokens != 3 {
		t.Fatalf("counters only: %+v", cs)
	}
	if _, _, err := svc.CallExchange(as("alice"), cs[0].Seq, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestExchangeVisibility(t *testing.T) {
	svc, _ := ledgerService(t, NewMemoryStore())
	_, rpc := rpcClient(t, svc, adminOnly{})
	for _, sub := range []string{"alice", "bob"} {
		if _, err := svc.Complete(llm.WithMeta(as(sub), llm.CallMeta{Source: llm.SourceAssistant}), llm.Request{Messages: []llm.Message{{Role: "user", Content: "q of " + sub}}}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(ctx context.Context, seq int64) (*modelv1.GetCallExchangeResponse, error) {
		r, err := rpc.GetCallExchange(ctx, connect.NewRequest(&modelv1.GetCallExchangeRequest{Seq: seq}))
		if err != nil {
			return nil, err
		}
		return r.Msg, nil
	}
	r, err := get(as("alice"), 1)
	if err != nil || r.Call.Subject != "alice" || !r.Call.HasExchange || len(r.Messages) != 1 || r.Messages[0].Content != "q of alice" || r.Response == "" {
		t.Fatalf("own call: %v %+v", err, r)
	}
	if _, err := get(as("alice"), 2); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("another user's call must not be revealed: %v", err)
	}
	if _, err := get(as("alice"), 99); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown seq must look the same: %v", err)
	}
	if r, err := get(as("root"), 2); err != nil || r.Call.Subject != "bob" || r.Messages[0].Content != "q of bob" {
		t.Fatalf("an administrator reads any: %v %+v", err, r)
	}
	if _, err := get(context.Background(), 1); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("anonymous: %v", err)
	}
}

func TestExchangeIsPurgedWithItsRow(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, _ := ledgerService(t, st)
			ctx := context.Background()
			now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
			svc.Now = func() time.Time { return now }
			old, _ := st.AppendCall(ctx, Call{At: now.AddDate(0, 0, -200), Source: "assistant", Step: -1, CallIndex: -1}, &Exchange{Response: "old"})
			recent, _ := st.AppendCall(ctx, Call{At: now.AddDate(0, 0, -1), Source: "assistant", Step: -1, CallIndex: -1}, &Exchange{Response: "recent"})
			if n, err := svc.PurgeCalls(ctx, 90); err != nil || n != 1 {
				t.Fatalf("%d %v", n, err)
			}
			if _, err := st.Exchange(ctx, old); !errors.Is(err, ErrNotFound) {
				t.Fatalf("the exchange goes with its row: %v", err)
			}
			if x, err := st.Exchange(ctx, recent); err != nil || x.Response != "recent" {
				t.Fatalf("%v %+v", err, x)
			}
		})
	}
}

// Every ledger row of the engine finds its exchange in the log of its change (ADR 0059), by process, step and call,
// through the real gateway: the other way round, nothing is stored twice.
func TestEngineRowsFindTheirExchangeInTheChangeLog(t *testing.T) {
	ctx := context.Background()
	svc, cfg := newService(NewMemoryStore())
	seed(t, cfg, []ProviderRecord{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}}, []ModelEntry{{Provider: "fake", Model: "echo", Enabled: true}},
		[]AliasEntry{{Alias: "default", Target: "fake/echo"}, {Alias: "fast", Target: "fake/echo"}})
	svc.Router.Instrument = func(ctx context.Context, _, _ string, req llm.Request, _ func(context.Context) (llm.Response, error)) (llm.Response, error) {
		p := req.Messages[0].Content
		var out string
		switch {
		case strings.Contains(p, "DIRECTLY impacted"):
			out = `{"items":[{"kind":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"REQ-1","rationale":"PSP API change"}}]}`
		case strings.Contains(p, "new version"):
			out = `{"items":[{"kind":"changeImpact","changeImpact":{"op":"write","node":"REQ-1","props":{"title":"Use PSP v2"}}}]}`
		case strings.Contains(p, "test case"):
			out = `{"items":[{"kind":"changeImpact","changeImpact":{"op":"declare","ref":"#t1","intent":"created","type":"TestCase","key":"TST-9","rationale":"cover REQ-1"}},
			{"kind":"changeImpact","changeImpact":{"op":"write","node":"#t1","props":{"title":"PSP v2 test"},"links":[{"type":"verifies","to":"REQ-1"}]}}]}`
		case strings.Contains(p, "report"):
			out = `{"items":[{"kind":"artifact","type":"report","data":{"markdown":"# Impact"}}]}`
		default:
			t.Errorf("unexpected prompt %s", p)
		}
		return llm.Response{Text: out, Usage: llm.Usage{InputTokens: 3, OutputTokens: 4}}, nil
	}
	m, err := methodology.LoadFile("../../methodologies/examples/impact-analysis.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	g := graph.New(graph.NewMemory())
	if _, err := graphtest.Project(ctx, g, "PROJ-TEST", "Test"); err != nil {
		t.Fatal(err)
	}
	need, _ := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "NEED-1", Type: "alm@Need", Properties: map[string]any{"title": "Pay online"}})
	req, _ := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "REQ-1", Type: "alm@Requirement", Properties: map[string]any{"title": "Use PSP v1"},
		Links: []graph.LinkWrite{{Type: "alm@satisfies", To: need.Ref()}}})
	_, _ = graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "TST-1", Type: "alm@TestCase", Links: []graph.LinkWrite{{Type: "alm@verifies", To: req.Ref()}}})
	_, _ = graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "CMP-1", Type: "alm@Component", Links: []graph.LinkWrite{{Type: "alm@implements", To: req.Ref()}}})
	b, err := g.BranchHead(ctx, "alm", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	e := &engine.Engine{
		Graph:         g,
		Methodologies: engine.StaticMethodologies{cm.Name: cm},
		Executors: map[string]engine.Executor{
			methodology.KindLLM:     engine.LLMExecutor{Client: llm.ClientFunc(svc.Complete)},
			methodology.KindHuman:   engine.HumanExecutor{},
			methodology.KindBuiltin: engine.DefaultBuiltins(),
		},
		Intent: intent.Resolver{Ranker: intent.Lexical{}},
		Store:  engine.NewMemoryStore(),
	}
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "impact-analysis", BaselineID: b.ID, ProjectID: "PROJ-TEST", Intent: "The PSP changes its API, what does this break?"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != engine.StatusCompleted {
		t.Fatalf("run: %v %+v", err, p)
	}
	rows := calls(t, svc, UsageFilter{Source: llm.SourceEngine})
	entries, _, err := g.ChangeLog(ctx, domain.LogFilter{Change: p.ChangeID, Types: []string{journal.StreamModel + ".call"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 || len(rows) != len(entries) {
		t.Fatalf("%d ledger rows, %d exchanges in the change log", len(rows), len(entries))
	}
	for _, r := range rows {
		if r.HasExchange || r.ChangeID != string(p.ChangeID) || r.ProcessID != p.ID {
			t.Fatalf("an engine call of a change keeps its exchange in the change log, not in the ledger: %+v", r)
		}
		found := 0
		for _, en := range entries {
			var ex journal.ModelExchange
			if err := json.Unmarshal(en.Payload, &ex); err != nil {
				t.Fatal(err)
			}
			if en.Process == r.ProcessID && ex.Step == r.Step && ex.Call == r.CallIndex {
				found++
				if len(ex.Messages) == 0 || ex.Response == "" {
					t.Fatalf("empty exchange for %+v: %+v", r, ex)
				}
			}
		}
		if found != 1 {
			t.Fatalf("row seq %d (step %d call %d %s): %d exchanges", r.Seq, r.Step, r.CallIndex, r.Action, found)
		}
	}
}
