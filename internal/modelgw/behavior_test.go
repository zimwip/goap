package modelgw

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// behaviorService is a ledger service whose provider records the system text it is sent.
func behaviorService(t *testing.T, st Store) (*Service, *graph.Graph, *[]llm.Request) {
	t.Helper()
	svc, _ := ledgerService(t, st)
	sent := &[]llm.Request{}
	inner := svc.Router.Instrument
	svc.Router.Instrument = func(ctx context.Context, p, m string, req llm.Request, call func(context.Context) (llm.Response, error)) (llm.Response, error) {
		if llm.MetaFrom(ctx).Source != llm.SourceCalibration { // the calibration calls have their own tests
			*sent = append(*sent, req)
		}
		return inner(ctx, p, m, req, call)
	}
	return svc, svc.Config.Graph.(*graph.Graph), sent
}

func addBehaviors(t *testing.T, g *graph.Graph, bs ...llmcfg.Behavior) {
	t.Helper()
	var edits []graph.NodeEdit
	for _, b := range bs {
		edits = append(edits, graph.NodeEdit{Key: llmcfg.BehaviorKey(b.Name), Type: llmcfg.NodeTypeBehavior, Props: b.Props(), Rationale: "t"})
	}
	change(t, g, edits...)
}

func TestBehaviorsApplyToCompletions(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			svc, g, sent := behaviorService(t, st)
			addBehaviors(t, g,
				llmcfg.Behavior{Name: "b-tail", Instruction: "TAIL", Enabled: true, Order: 2},
				llmcfg.Behavior{Name: "a-head", Instruction: "HEAD", Enabled: true, Position: llmcfg.PositionPrepend, Order: 1},
				llmcfg.Behavior{Name: "off", Instruction: "OFF", Enabled: false},
				llmcfg.Behavior{Name: "only-helper", Instruction: "HELPER", Enabled: true, Sources: []string{llm.SourceHelper}},
				llmcfg.Behavior{Name: "json-ok", Instruction: "JSONSTYLE", Enabled: true, AppliesToJSON: true, Order: 5},
			)
			ctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceAssistant})
			req := llm.Request{System: "SYS", Messages: []llm.Message{{Role: "user", Content: "hi"}}}
			resp, err := svc.Complete(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if got := (*sent)[0].System; got != "HEAD\n\nSYS\n\nTAIL\n\nJSONSTYLE" {
				t.Fatalf("system = %q", got)
			}
			if req.System != "SYS" {
				t.Fatalf("the caller's request was changed: %q", req.System)
			}
			if strings.Join(resp.Behaviors, ",") != "a-head,b-tail,json-ok" || resp.BehaviorTokens == 0 {
				t.Fatalf("response: %+v", resp)
			}
			// a call of the helper also gets the one scoped to it
			hctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceHelper})
			if _, err := svc.Complete(hctx, req); err != nil {
				t.Fatal(err)
			}
			if got := (*sent)[1].System; !strings.Contains(got, "HELPER") {
				t.Fatalf("helper: %q", got)
			}
			// a JSON call gets only the behaviours that ask for it, wrapped
			jreq := req
			jreq.JSON = true
			if _, err := svc.Complete(ctx, jreq); err != nil {
				t.Fatal(err)
			}
			want := "SYS\n\n" + llmcfg.JSONNotice + "JSONSTYLE"
			if got := (*sent)[2].System; got != want {
				t.Fatalf("json system = %q want %q", got, want)
			}
			// the ledger: names, an estimate of the tokens, and the system as sent in the stored exchange
			cs := userCalls(t, svc)
			if len(cs) != 3 || strings.Join(cs[0].Behaviors, ",") != "a-head,b-tail,json-ok" || cs[0].BehaviorTokens <= 0 {
				t.Fatalf("ledger: %+v", cs)
			}
			_, x, err := svc.CallExchange(authz.With(context.Background(), authz.Principal{Subject: "root"}), cs[0].Seq, true)
			if err != nil || x.System != "HEAD\n\nSYS\n\nTAIL\n\nJSONSTYLE" {
				t.Fatalf("exchange: %v %q", err, x.System)
			}
		})
	}
}

func TestBehaviorScopes(t *testing.T) {
	svc, g, sent := behaviorService(t, NewMemoryStore())
	addBehaviors(t, g,
		llmcfg.Behavior{Name: "by-alias", Instruction: "ALIAS", Enabled: true, Aliases: []string{"fast"}},
		llmcfg.Behavior{Name: "by-model", Instruction: "MODEL", Enabled: true, Models: []string{"fake/echo"}},
		llmcfg.Behavior{Name: "other-model", Instruction: "NOPE", Enabled: true, Models: []string{"fake/other"}},
	)
	change(t, g, graph.NodeEdit{Key: llmcfg.AliasKey("fast"), Type: llmcfg.NodeTypeAlias, Props: llmcfg.Alias{Alias: "fast", Target: "fake/echo"}.Props(), Rationale: "t"})
	ctx := context.Background()
	send := func(model string) string {
		if _, err := svc.Complete(ctx, llm.Request{Model: model, System: "S", Messages: []llm.Message{{Role: "user", Content: "x"}}}); err != nil {
			t.Fatal(err)
		}
		return (*sent)[len(*sent)-1].System
	}
	if got := send("default"); got != "S\n\nMODEL" {
		t.Fatalf("default: %q", got)
	}
	if got := send("fast"); got != "S\n\nALIAS\n\nMODEL" {
		t.Fatalf("fast: %q", got)
	}
	if got := send("fake/echo"); got != "S\n\nMODEL" { // a literal model matches no alias
		t.Fatalf("literal: %q", got)
	}
}

func TestBehaviorsNeverApplyToEmbeddings(t *testing.T) {
	svc, g, sent := behaviorService(t, NewMemoryStore())
	addBehaviors(t, g, llmcfg.Behavior{Name: "all", Instruction: "X", Enabled: true})
	if _, err := svc.Embed(context.Background(), llm.EmbedRequest{Texts: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 0 {
		t.Fatalf("completions sent: %+v", *sent)
	}
	cs := userCalls(t, svc)
	if len(cs) != 1 || len(cs[0].Behaviors) != 0 || cs[0].BehaviorTokens != 0 {
		t.Fatalf("%+v", cs)
	}
	if err := (llmcfg.Behavior{Name: "e", Instruction: "x", Kinds: []string{"embed"}}).Validate(); err == nil {
		t.Fatal("a behavior scoped to embeddings must be refused")
	}
}

func TestBehaviorCap(t *testing.T) {
	svc, g, sent := behaviorService(t, NewMemoryStore())
	big := strings.Repeat("x", 3000)
	addBehaviors(t, g,
		llmcfg.Behavior{Name: "one", Instruction: big, Enabled: true, Order: 1},
		llmcfg.Behavior{Name: "two", Instruction: big, Enabled: true, Order: 2},
		llmcfg.Behavior{Name: "three", Instruction: big, Enabled: true, Order: 3},
	)
	if _, err := svc.Complete(context.Background(), llm.Request{System: "S", Messages: []llm.Message{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if got := (*sent)[0].System; len(got) > len("S")+llmcfg.MaxBehaviorBytes+2 {
		t.Fatalf("the cap is exceeded: %d bytes", len(got))
	}
	cs := userCalls(t, svc)
	if strings.Join(cs[0].Behaviors, ",") != "one,two,!three" {
		t.Fatalf("ledger: %v", cs[0].Behaviors)
	}
}

func TestBehaviorsFollowTheGraph(t *testing.T) {
	svc, g, sent := behaviorService(t, NewMemoryStore())
	addBehaviors(t, g, llmcfg.Behavior{Name: "x", Instruction: "X", Enabled: true})
	ctx := context.Background()
	req := llm.Request{System: "S", Messages: []llm.Message{{Role: "user", Content: "x"}}}
	_, _ = svc.Complete(ctx, req)
	change(t, g, retireNode(t, g, llmcfg.BehaviorKey("x")))
	_, _ = svc.Complete(ctx, req)
	if (*sent)[0].System != "S\n\nX" || (*sent)[1].System != "S" {
		t.Fatalf("%q %q", (*sent)[0].System, (*sent)[1].System)
	}
}

func TestBehaviorRPC(t *testing.T) {
	svc, g, _ := behaviorService(t, NewMemoryStore())
	addBehaviors(t, g,
		llmcfg.Behavior{Name: "on", Instruction: "ON", Enabled: true, Order: 2},
		llmcfg.Behavior{Name: "off", Instruction: "OFF", Order: 1},
	)
	_, rpc := rpcClient(t, svc, adminOnly{})
	as := func(sub string) context.Context {
		return authz.With(context.Background(), authz.Principal{Subject: sub})
	}
	if _, err := rpc.ListBehaviors(as("alice"), connect.NewRequest(&modelv1.ListBehaviorsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("list by a non-admin: %v", err)
	}
	if _, err := rpc.PreviewBehaviors(as("alice"), connect.NewRequest(&modelv1.PreviewBehaviorsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("preview by a non-admin: %v", err)
	}
	l, err := rpc.ListBehaviors(as("root"), connect.NewRequest(&modelv1.ListBehaviorsRequest{}))
	if err != nil || len(l.Msg.Behaviors) != 2 || l.Msg.Behaviors[0].Name != "off" || l.Msg.Behaviors[1].Name != "on" || l.Msg.MaxTotalBytes != llmcfg.MaxBehaviorBytes {
		t.Fatalf("list: %v %+v", err, l)
	}
	p, err := rpc.PreviewBehaviors(as("root"), connect.NewRequest(&modelv1.PreviewBehaviorsRequest{Alias: "default", Source: "engine", System: "S"}))
	if err != nil || p.Msg.System != "S\n\nON" || len(p.Msg.Applied) != 1 || p.Msg.AddedTokens == 0 {
		t.Fatalf("preview: %v %+v", err, p)
	}
	if p, err := rpc.PreviewBehaviors(as("root"), connect.NewRequest(&modelv1.PreviewBehaviorsRequest{Alias: "default", Json: true, System: "S"})); err != nil || p.Msg.System != "S" {
		t.Fatalf("a JSON preview skips the behaviours that do not ask for it: %v %+v", err, p)
	}
	if _, err := rpc.PreviewBehaviors(as("root"), connect.NewRequest(&modelv1.PreviewBehaviorsRequest{Alias: "nope"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown alias: %v", err)
	}
	// the preview calls no model: nothing is in the ledger
	if cs := userCalls(t, svc); len(cs) != 0 {
		t.Fatalf("%+v", cs)
	}
}

// The behaviours the gateway added travel back to the caller over RPC (the engine's change log records them).
func TestBehaviorsOverRPCResponse(t *testing.T) {
	svc, g, _ := behaviorService(t, NewMemoryStore())
	addBehaviors(t, g, llmcfg.Behavior{Name: "x", Instruction: "X", Enabled: true})
	c, _ := rpcClient(t, svc, nil)
	r, err := c.Complete(context.Background(), llm.Request{System: "S", Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if err != nil || len(r.Behaviors) != 1 || r.Behaviors[0] != "x" || r.BehaviorTokens == 0 {
		t.Fatalf("%v %+v", err, r)
	}
}

// userCalls is the ledger without the gateway's own calibration calls (they have their own tests).
func userCalls(t *testing.T, s *Service) []Call {
	t.Helper()
	s.WaitCosts()
	var out []Call
	for _, c := range calls(t, s, UsageFilter{}) {
		if c.Source != llm.SourceCalibration {
			out = append(out, c)
		}
	}
	return out
}
