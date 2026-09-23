package observe

import (
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

func rec(kind, action, actionKind string, mut func(*domain.ExecutionRecord)) domain.ExecutionRecord {
	r := domain.ExecutionRecord{ID: action + kind, ProcessID: "p", Methodology: "m", MethodologyVersion: "1.0.0", Agent: "ag", Kind: kind, Action: action, ActionKind: actionKind}
	if mut != nil {
		mut(&r)
	}
	return r
}

func TestAnalyzeFindings(t *testing.T) {
	no, yes := false, true
	items := map[domain.ItemID]domain.ChangeItem{"i1": {Kind: domain.KindImpact, Type: "direct"}, "i2": {Kind: domain.KindImpact, Type: "direct"}}
	recs := []domain.ExecutionRecord{
		rec(domain.ExecProcessStarted, "", "", nil),
		rec(domain.ExecTick, "", "", func(r *domain.ExecutionRecord) { r.Data = map[string]any{"replanned": false} }),
		rec(domain.ExecAction, "classify", "llm", func(r *domain.ExecutionRecord) {
			r.ModelCalls = []domain.ModelCall{{InputTokens: 900, OutputTokens: 100}}
			r.InputTokens, r.OutputTokens, r.Items, r.EffectsMet = 900, 100, []domain.ItemID{"i1", "i2"}, &yes
		}),
	}
	for i := 0; i < 3; i++ {
		recs = append(recs, rec(domain.ExecAction, "check", "human", func(r *domain.ExecutionRecord) { r.EffectsMet = &no; r.DurationMs = 40_000 }))
	}
	recs = append(recs, rec(domain.ExecProcessEnded, "", "", func(r *domain.ExecutionRecord) { r.Status = "stuck" }))
	spans := []Span{{Name: "execute_tool crm/search", DurationMs: 12_000, Attributes: map[string]string{"gen_ai.tool.name": "crm/search"}}}
	r := Analyze(recs, items, spans, Thresholds{})
	kinds := map[string]string{}
	for _, f := range r.Findings {
		kinds[f.Kind] = f.Action + f.Span
	}
	for k, want := range map[string]string{FindSystematizable: "classify", FindLoop: "check", FindFailure: "check", FindSlowAction: "check", FindSlowSpan: "execute_tool crm/search"} {
		if kinds[k] != want {
			t.Errorf("finding %s = %q, want %q (%+v)", k, kinds[k], want, r.Findings)
		}
	}
	if r.Status != "stuck" || r.Tokens != 1000 || r.Steps != 4 || r.Actions[0].Outputs["impact/direct"] != 2 {
		t.Fatalf("report: %+v", r)
	}
}

func TestProposeAndApply(t *testing.T) {
	m := methodology.Methodology{Name: "m", Version: "1.0.0",
		Conditions: []methodology.Condition{{Name: "c", Expr: "true"}, {Name: "d", Expr: "true"}},
		Actions: []methodology.Action{
			{Name: "classify", Kind: "llm", Prompt: "Classify", Effects: map[string]bool{"c": true}},
			{Name: "check", Kind: "human", Effects: map[string]bool{"d": true}, Cost: 1.5},
			{Name: "summarize", Kind: "llm", Prompt: "Summarize", Effects: map[string]bool{"d": true}},
		},
		Goals:  []methodology.Goal{{Name: "g", Pre: map[string]bool{"c": true}}},
		Agents: []methodology.Agent{{Name: "ag"}},
	}
	r := Report{Methodology: "m", Agent: "ag", Findings: []Finding{
		{Kind: FindSystematizable, Action: "classify", Agent: "ag"},
		{Kind: FindLLMHeavy, Action: "summarize", Agent: "ag"},
		{Kind: FindLoop, Action: "check", Agent: "ag"},
		{Kind: FindDisabled, Action: "check", Agent: "ag"},
		{Kind: FindSlowSpan, Span: "execute_tool crm/search"},
		{Kind: FindReplanning, Agent: "ag", Evidence: "the plan changed"},
	}, Actions: []ActionStats{{Action: "classify", Outputs: map[string]int{"impact/direct": 2}}}}
	props, notes := Propose(r, &m)
	if len(props) != 5 || len(notes) != 1 {
		t.Fatalf("proposals %+v notes %v", props, notes)
	}
	var edits []Edit
	for _, p := range props {
		edits = append(edits, Edit{Op: p.Op, Key: p.Key, Type: p.Type, Props: p.Props})
	}
	edits = append(edits, Edit{Op: "update_node", Key: "M:other/action/x", Props: map[string]any{"cost": 1}})
	d, err := Apply(m, edits)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Applied) != 5 || len(d.Skipped) != 1 || len(d.ToolRequests) != 1 {
		t.Fatalf("draft: %+v", d)
	}
	out := d.Methodology
	if is := out.Validate(); len(is) > 0 {
		t.Fatalf("draft invalid: %v", is)
	}
	if out.Actions[1].Cost != 3 || out.Actions[2].Model != "fast" || !slices.Equal(out.Agents[0].Actions, []string{"classify", "summarize"}) {
		t.Fatalf("edits not applied: %+v %+v", out.Actions, out.Agents)
	}
	spec := out.Actions[3]
	if spec.Specializes != "classify" || spec.Priority != 10 || !strings.Contains(spec.Code, "ctx.addImpact") {
		t.Fatalf("specialization: %+v", spec)
	}
	if m.Actions[1].Cost != 1.5 || len(m.Actions) != 3 {
		t.Fatal("Apply must not modify its input")
	}
}

func TestNextVersion(t *testing.T) {
	taken := map[string]bool{"1.2.4": true}
	if v := NextVersion("1.2.3", func(v string) bool { return taken[v] }); v != "1.2.5" {
		t.Fatal(v)
	}
	if v := NextVersion("2024-q1", func(string) bool { return false }); v != "2024-q1.1" {
		t.Fatal(v)
	}
}
