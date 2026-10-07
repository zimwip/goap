package modelgw

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	modelv1 "github.com/zimwip/goap/gen/goap/model/v1"
	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/llmcfg"
)

// costHarness is a service on two fake models whose "provider" reports 10 input tokens plus one a byte of system text,
// and records the calibration requests it receives.
type costHarness struct {
	svc *Service
	g   *graph.Graph

	mu   sync.Mutex
	sent []llm.Request // the requests of the callers
	cal  []llm.Request // the calibration requests
	now  time.Time

	// usage, when set, replaces the reported input tokens of a calibration.
	usage func(req llm.Request) int
	// fail, when set, fails the calibration calls.
	fail func() error
	// gate, when set, blocks the calibration calls until it is closed.
	gate chan struct{}
}

func newCostHarness(t *testing.T, st Store, quota int64) *costHarness {
	t.Helper()
	svc, g := newService(st)
	h := &costHarness{svc: svc, g: g, now: time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)}
	svc.Now = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	seed(t, g, []ProviderRecord{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}},
		[]ModelEntry{{Provider: "fake", Model: "echo", Enabled: true, QuotaTokens: quota, QuotaPeriod: PeriodTotal},
			{Provider: "fake", Model: "other", Enabled: true}},
		[]AliasEntry{{Alias: "default", Target: "fake/echo"}})
	svc.Router.Instrument = func(ctx context.Context, _, _ string, req llm.Request, call func(context.Context) (llm.Response, error)) (llm.Response, error) {
		r, err := call(ctx)
		if llm.MetaFrom(ctx).Source != llm.SourceCalibration {
			h.mu.Lock()
			h.sent = append(h.sent, req)
			h.mu.Unlock()
			r.Usage = llm.Usage{InputTokens: 10 + len(req.System), OutputTokens: 1}
			return r, err
		}
		h.mu.Lock()
		h.cal = append(h.cal, req)
		fail, usage, gate := h.fail, h.usage, h.gate
		h.mu.Unlock()
		if gate != nil {
			<-gate
		}
		if fail != nil {
			if e := fail(); e != nil {
				return llm.Response{}, e
			}
		}
		in := 10 + len(req.System)
		if usage != nil {
			in = usage(req)
		}
		r.Usage = llm.Usage{InputTokens: in, OutputTokens: 1}
		return r, err
	}
	return h
}

func (h *costHarness) calibrations() []llm.Request {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]llm.Request(nil), h.cal...)
}

func (h *costHarness) advance(d time.Duration) { h.mu.Lock(); h.now = h.now.Add(d); h.mu.Unlock() }

func (h *costHarness) complete(t *testing.T) llm.Response {
	t.Helper()
	r, err := h.svc.Complete(llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceAssistant}),
		llm.Request{System: "S", Messages: []llm.Message{{Role: "user", Content: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

const terseText = "Answer tersely, drop the filler."

func TestCostArithmetic(t *testing.T) {
	cases := []struct {
		in, base int64
		instr    string
		tokens   int64
		source   string
	}{
		{in: 35, base: 10, instr: terseText, tokens: 25, source: CostMeasured},
		{in: 10, base: 10, instr: terseText, tokens: 9, source: CostEstimated}, // nothing between the two: no measure
		{in: 8, base: 10, instr: terseText, tokens: 9, source: CostEstimated},  // never negative
		{in: 0, base: 0, instr: terseText, tokens: 9, source: CostEstimated},   // no usage numbers
		{in: 30, base: 0, instr: terseText, tokens: 9, source: CostEstimated},  // no baseline
	}
	for _, c := range cases {
		if tok, src := costOf(c.in, c.base, c.instr); tok != c.tokens || src != c.source {
			t.Errorf("costOf(%d,%d) = %d %s, want %d %s", c.in, c.base, tok, src, c.tokens, c.source)
		}
	}
}

// The first call is priced by the estimate and asks for a calibration; once it ran, the measured cost prices the next.
func TestCostMeasuredOnFirstUse(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			h := newCostHarness(t, st, 0)
			addBehaviors(t, h.g,
				llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true, Order: 1},
				llmcfg.Behavior{Name: "polite", Instruction: "Be polite.", Enabled: true, Order: 2})
			r := h.complete(t)
			if !r.BehaviorsEstimated || r.BehaviorTokens == 0 {
				t.Fatalf("first call: %+v", r)
			}
			h.svc.WaitCosts()
			// the calibration calls bypass the behaviours: the instruction alone, then nothing (the baseline)
			var systems []string
			for _, c := range h.calibrations() {
				if c.MaxTokens != 1 || c.JSON || len(c.Messages) != 1 {
					t.Fatalf("not a minimal request: %+v", c)
				}
				systems = append(systems, c.System)
			}
			for _, want := range []string{terseText, "Be polite.", ""} {
				found := false
				for _, s := range systems {
					found = found || s == want
				}
				if !found {
					t.Fatalf("no calibration with system %q in %q", want, systems)
				}
			}
			if len(systems) != 3 {
				t.Fatalf("%d calibrations: %q", len(systems), systems)
			}
			r = h.complete(t)
			if r.BehaviorsEstimated || r.BehaviorTokens != len(terseText)+len("Be polite.") {
				t.Fatalf("second call: %+v", r)
			}
			// the ledger: the caller's rows carry the flag, the calibration rows their own source and subject
			var user, cal int
			for _, c := range calls(t, h.svc, UsageFilter{}) {
				switch c.Source {
				case llm.SourceCalibration:
					cal++
					if c.Subject != "system:modelgw" || len(c.Behaviors) != 0 || c.BehaviorTokens != 0 || c.Kind != KindComplete {
						t.Fatalf("calibration row: %+v", c)
					}
				case llm.SourceAssistant:
					user++
				}
			}
			cs := userCalls(t, h.svc)
			if cal != 3 || user != 2 || !cs[0].BehaviorTokensEstimated || cs[1].BehaviorTokensEstimated {
				t.Fatalf("ledger: %d calibrations, %d callers: %+v", cal, user, cs)
			}
			// stored: both costs and the baseline
			costs, _ := h.svc.Store.BehaviorCosts(context.Background())
			bases, _ := h.svc.Store.ModelBaselines(context.Background())
			if len(costs) != 2 || len(bases) != 1 || bases[0].Tokens != 10 {
				t.Fatalf("stored: %+v %+v", costs, bases)
			}
			for _, c := range costs {
				if c.Source != CostMeasured || c.Baseline != 10 || c.Hash != llmcfg.InstructionHash(map[string]string{"terse": terseText, "polite": "Be polite."}[c.Behavior]) {
					t.Fatalf("cost: %+v", c)
				}
			}
		})
	}
}

func TestCostCalibrationDeduplicatedAndNotBlocking(t *testing.T) {
	h := newCostHarness(t, NewMemoryStore(), 0)
	addBehaviors(t, h.g, llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true})
	h.gate = make(chan struct{}) // the calibrations hang
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for range 20 {
			wg.Add(1)
			go func() { defer wg.Done(); h.complete(t) }()
		}
		wg.Wait()
		close(done)
	}()
	select {
	case <-done: // the callers did not wait for the calibration
	case <-time.After(5 * time.Second):
		t.Fatal("the callers are blocked by the calibration")
	}
	close(h.gate)
	h.svc.WaitCosts()
	if n := len(h.calibrations()); n != 2 { // the instruction and the baseline, once
		t.Fatalf("%d calibrations for 20 calls", n)
	}
}

func TestCostFailureBacksOff(t *testing.T) {
	h := newCostHarness(t, NewMemoryStore(), 0)
	h.svc.CostBackoff = time.Hour
	addBehaviors(t, h.g, llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true})
	var fails atomic.Int32
	h.fail = func() error { fails.Add(1); return errors.New("provider down") }
	for range 5 {
		if r := h.complete(t); !r.BehaviorsEstimated || r.BehaviorTokens == 0 { // the caller never sees it
			t.Fatalf("%+v", r)
		}
		h.svc.WaitCosts()
	}
	if n := fails.Load(); n != 1 {
		t.Fatalf("%d calibration attempts inside the backoff", n)
	}
	if costs, _ := h.svc.Store.BehaviorCosts(context.Background()); len(costs) != 0 {
		t.Fatalf("a failure stored %+v", costs)
	}
	h.advance(2 * time.Hour)
	h.fail = nil
	h.complete(t)
	h.svc.WaitCosts()
	if r := h.complete(t); r.BehaviorsEstimated {
		t.Fatalf("not measured after the backoff: %+v", r)
	}
}

// A provider with no usage numbers gives no measure: the estimate is stored once and not measured at every call.
func TestCostWithoutUsageStoredOnce(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			h := newCostHarness(t, st, 0)
			h.usage = func(llm.Request) int { return 0 }
			addBehaviors(t, h.g, llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true})
			h.complete(t)
			h.svc.WaitCosts()
			n := len(h.calibrations())
			for range 5 {
				if r := h.complete(t); !r.BehaviorsEstimated || r.BehaviorTokens == 0 {
					t.Fatalf("%+v", r)
				}
				h.svc.WaitCosts()
			}
			if got := len(h.calibrations()); got != n {
				t.Fatalf("measured again: %d then %d calibrations", n, got)
			}
			costs, _ := h.svc.Store.BehaviorCosts(context.Background())
			if len(costs) != 1 || costs[0].Source != CostEstimated || costs[0].Tokens != int64(llmcfg.EstimateTokens(len(terseText)+2)) {
				t.Fatalf("%+v", costs)
			}
		})
	}
}

func TestCostRefreshOnInstructionModelAndTTL(t *testing.T) {
	for name, st := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			h := newCostHarness(t, st, 0)
			h.svc.CostTTL = 24 * time.Hour
			addBehaviors(t, h.g, llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true})
			h.complete(t)
			h.svc.WaitCosts()
			// a new instruction text is another key: measured again, the old row is purged
			change(t, h.g, updateNode(t, h.g, llmcfg.BehaviorKey("terse"), llmcfg.Behavior{Name: "terse", Instruction: "Shorter.", Enabled: true}.Props()))
			h.svc.Config.TTL = 1
			r := h.complete(t)
			if !r.BehaviorsEstimated {
				t.Fatalf("a new instruction priced as measured: %+v", r)
			}
			h.svc.WaitCosts()
			if r := h.complete(t); r.BehaviorsEstimated || r.BehaviorTokens != len("Shorter.") {
				t.Fatalf("after the instruction changed: %+v", r)
			}
			costs, _ := h.svc.Store.BehaviorCosts(ctx)
			if len(costs) != 1 || costs[0].Hash != llmcfg.InstructionHash("Shorter.") {
				t.Fatalf("the cost of the old text stays: %+v", costs)
			}
			// the alias retargeted to another model: measured there, the old model's rows are not read
			change(t, h.g, updateNode(t, h.g, llmcfg.AliasKey("default"), llmcfg.Alias{Alias: "default", Target: "fake/other"}.Props()))
			if r := h.complete(t); !r.BehaviorsEstimated {
				t.Fatalf("a new model priced as measured: %+v", r)
			}
			h.svc.WaitCosts()
			if r := h.complete(t); r.BehaviorsEstimated {
				t.Fatalf("after the model changed: %+v", r)
			}
			costs, _ = h.svc.Store.BehaviorCosts(ctx)
			if len(costs) != 2 {
				t.Fatalf("%+v", costs)
			}
			// older than the TTL: the stored cost still prices the call, and is measured again
			before := len(h.calibrations())
			h.advance(25 * time.Hour)
			if r := h.complete(t); r.BehaviorsEstimated {
				t.Fatalf("a stale cost is still a measure: %+v", r)
			}
			h.svc.WaitCosts()
			if len(h.calibrations()) <= before {
				t.Fatal("a cost older than the TTL was not measured again")
			}
			costs, _ = h.svc.Store.BehaviorCosts(ctx)
			for _, c := range costs {
				if c.Model == "other" && !c.MeasuredAt.After(h.now.Add(-time.Hour)) {
					t.Fatalf("not refreshed: %+v", c)
				}
			}
		})
	}
}

// An exhausted quota refuses the calibration like any call: the estimate stays and nothing is stored.
func TestCostQuotaExhaustedKeepsEstimate(t *testing.T) {
	h := newCostHarness(t, NewMemoryStore(), 5)
	addBehaviors(t, h.g, llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true})
	if err := h.svc.Store.AddUsage(context.Background(), llmcfg.ModelKey("fake", "echo"), PeriodTotal, 5); err != nil {
		t.Fatal(err)
	}
	// the caller's own call is refused too, so price through the preview and the explicit trigger
	costs, err := h.svc.MeasureBehaviors(context.Background(), nil, nil)
	if err != nil || len(costs) != 1 {
		t.Fatalf("%v %+v", err, costs)
	}
	if !strings.Contains(costs[0].Error, "quota") || costs[0].Source != CostEstimated || costs[0].Tokens == 0 {
		t.Fatalf("%+v", costs[0])
	}
	if stored, _ := h.svc.Store.BehaviorCosts(context.Background()); len(stored) != 0 {
		t.Fatalf("%+v", stored)
	}
	if n := len(h.calibrations()); n != 0 {
		t.Fatalf("the provider was called: %d", n)
	}
}

// A calibration counts its few tokens against the quota of the model and is admitted like any call, but a model whose
// roles exclude the gateway is still calibrated: the roles are for the callers.
func TestCostCalibrationCountsQuotaAndSkipsRoles(t *testing.T) {
	svc, g := newService(NewMemoryStore())
	seed(t, g, []ProviderRecord{{Name: "fake", Kind: "fake", Protocol: "fake", Enabled: true}},
		[]ModelEntry{{Provider: "fake", Model: "echo", Enabled: true, QuotaTokens: 1000, QuotaPeriod: PeriodTotal, Roles: []string{"methodologist"}}},
		[]AliasEntry{{Alias: "default", Target: "fake/echo"}})
	addBehaviors(t, g, llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true})
	costs, err := svc.MeasureBehaviors(authz.With(context.Background(), authz.Principal{Subject: "root", Roles: []string{"admin"}}), nil, nil)
	if err != nil || len(costs) != 1 || costs[0].Error != "" || costs[0].Source != CostMeasured {
		t.Fatalf("%v %+v", err, costs)
	}
	if used, _ := svc.Store.Usage(context.Background(), llmcfg.ModelKey("fake", "echo"), PeriodTotal); used == 0 {
		t.Fatal("the calibration is not counted against the quota")
	}
}

func TestMeasureBehaviorsRPCAndListCosts(t *testing.T) {
	h := newCostHarness(t, NewMemoryStore(), 0)
	addBehaviors(t, h.g,
		llmcfg.Behavior{Name: "terse", Instruction: terseText, Enabled: true},
		llmcfg.Behavior{Name: "off", Instruction: "Off text", Enabled: false},
		llmcfg.Behavior{Name: "other-only", Instruction: "Other", Enabled: true, Models: []string{"fake/other"}})
	change(t, h.g, graph.NodeEdit{Key: llmcfg.AliasKey("fast"), Type: llmcfg.NodeTypeAlias, Props: llmcfg.Alias{Alias: "fast", Target: "fake/echo"}.Props(), Rationale: "t"})
	_, rpc := rpcClient(t, h.svc, adminOnly{})
	as := func(sub string) context.Context {
		return authz.With(context.Background(), authz.Principal{Subject: sub})
	}
	if _, err := rpc.MeasureBehaviors(as("alice"), connect.NewRequest(&modelv1.MeasureBehaviorsRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("measure by a non-admin: %v", err)
	}
	if n := len(h.calibrations()); n != 0 {
		t.Fatalf("a refused request called the model: %d", n)
	}
	// before any measure: estimates, per model, with the aliases that reach it
	l, err := rpc.ListBehaviors(as("root"), connect.NewRequest(&modelv1.ListBehaviorsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]*modelv1.Behavior{}
	for _, b := range l.Msg.Behaviors {
		by[b.Name] = b
	}
	tc := by["terse"].Costs
	if len(tc) != 1 || tc[0].Model != "fake/echo" || strings.Join(tc[0].Aliases, ",") != "default,fast" || tc[0].Source != CostEstimated || tc[0].MeasuredAtMs != 0 || tc[0].Tokens == 0 {
		t.Fatalf("terse costs: %+v", tc)
	}
	if oc := by["other-only"].Costs; len(oc) != 1 || oc[0].Model != "fake/other" || len(oc[0].Aliases) != 0 {
		t.Fatalf("other-only costs: %+v", oc)
	}
	// measure the disabled one only, on one model
	m, err := rpc.MeasureBehaviors(as("root"), connect.NewRequest(&modelv1.MeasureBehaviorsRequest{Names: []string{"off"}, Models: []string{"fake/echo"}}))
	if err != nil || len(m.Msg.Costs) != 1 || m.Msg.Costs[0].Behavior != "off" || m.Msg.Costs[0].Source != CostMeasured || m.Msg.Costs[0].Tokens != int64(len("Off text")) || m.Msg.Costs[0].MeasuredAtMs == 0 {
		t.Fatalf("%v %+v", err, m)
	}
	l, _ = rpc.ListBehaviors(as("root"), connect.NewRequest(&modelv1.ListBehaviorsRequest{}))
	for _, b := range l.Msg.Behaviors {
		if b.Name == "off" && (len(b.Costs) != 1 || b.Costs[0].Source != CostMeasured) {
			t.Fatalf("the listing does not show the measure: %+v", b.Costs)
		}
	}
	// the preview shows the cost of the previewed alias
	p, err := rpc.PreviewBehaviors(as("root"), connect.NewRequest(&modelv1.PreviewBehaviorsRequest{Alias: "default", System: "S"}))
	if err != nil || !p.Msg.AddedTokensEstimated || p.Msg.AddedTokens == 0 {
		t.Fatalf("%v %+v", err, p)
	}
	if _, err := rpc.MeasureBehaviors(as("root"), connect.NewRequest(&modelv1.MeasureBehaviorsRequest{Names: []string{"terse"}})); err != nil {
		t.Fatal(err)
	}
	p, _ = rpc.PreviewBehaviors(as("root"), connect.NewRequest(&modelv1.PreviewBehaviorsRequest{Alias: "default", System: "S"}))
	if p.Msg.AddedTokensEstimated || p.Msg.AddedTokens != int32(len(terseText)) {
		t.Fatalf("%+v", p)
	}
}
