package engine

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/metamodel"
	"github.com/zimwip/goap/pkg/methodology"
	"github.com/zimwip/goap/pkg/observe"
)

// memDrafts is an in-memory registry of definitions.
type memDrafts struct {
	defs  map[string]methodology.Methodology // name@version
	saved []methodology.Methodology
	who   []authz.Principal
}

func (d *memDrafts) Definition(_ context.Context, name, version string) (methodology.Methodology, bool, error) {
	if version == "" {
		version = "1.1.0"
	}
	m, ok := d.defs[name+"@"+version]
	return m, ok, nil
}

func (d *memDrafts) SaveDraft(ctx context.Context, m methodology.Methodology) (methodology.Issues, error) {
	d.saved = append(d.saved, m)
	d.who = append(d.who, authz.From(ctx))
	d.defs[m.Name+"@"+m.Version] = m
	return m.Validate(), nil
}

func TestSelfObservationProposesAndDrafts(t *testing.T) {
	ctx := authz.With(context.Background(), authz.Principal{Subject: "mia", Org: "acme", Roles: []string{"methodologist"}})
	e, g, base := setup(t)
	// the observed run; the intent text deliberately echoes the
	// "assess_impact" goal example in methodologies/impact-analysis.yaml.
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("observed run: %v %+v", err, p)
	}
	// the observer methodology, and the observed one projected onto the graph
	data, _ := os.ReadFile("../../methodologies/methodology-improvement.yaml")
	obsM, err := methodology.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := obsM.Compile()
	if err != nil {
		t.Fatal(err)
	}
	impact, _ := e.Methodologies.Methodology(ctx, "impact-analysis")
	e.Methodologies = StaticMethodologies{"impact-analysis": impact, obs.Name: obs}
	res, err := metamodel.Sync(ctx, g, impact.Methodology)
	if err != nil || !res.Changed() {
		t.Fatalf("sync: %+v %v", res, err)
	}
	drafts := &memDrafts{defs: map[string]methodology.Methodology{"impact-analysis@1.1.0": *impact.Methodology}}
	builtins := e.Executors[methodology.KindBuiltin].(BuiltinExecutor)
	for k, v := range e.SelfImprovementBuiltins(SelfImprovement{Drafts: drafts, Thresholds: observe.Thresholds{LLMCalls: 1, MinSystematized: 1}}) {
		builtins[k] = v
	}

	op, err := e.Start(ctx, StartRequest{Methodology: obs.Name, Agent: "observer", Goal: "improve_methodology", BaselineID: res.Baseline,
		Intent: "observe", Vars: map[string]any{"event": map[string]any{"process": map[string]any{"id": p.ID}}}})
	if err != nil {
		t.Fatal(err)
	}
	if op, err = e.Run(ctx, op.ID); err != nil || op.Status != StatusWaiting || op.Pending.Action != "review_improvements" {
		t.Fatalf("observer must wait for the review: %v %+v", err, op)
	}
	if s := op.Steps[1]; s.Action != "propose_improvements" || s.Specialization != "propose_by_rules" {
		t.Fatalf("rules specialization expected: %+v", s)
	}
	c, _ := g.Change(ctx, op.ChangeID)
	var report map[string]any
	var decisions []ItemInput
	var titles []string
	for _, it := range c.Items {
		switch {
		case it.Type == "cost_report":
			report = it.Data
		case it.Kind == domain.KindProposal:
			titles = append(titles, it.Data["title"].(string))
			decisions = append(decisions, ItemInput{Kind: "decision", Decision: &DecisionInput{Item: "@" + string(it.ID), Accept: true}})
		}
	}
	findings, _ := report["findings"].([]any)
	if len(findings) < 2 || report["methodology"] != "impact-analysis" {
		t.Fatalf("report: %+v", report)
	}
	joined := strings.Join(titles, " | ")
	if !strings.Contains(joined, "Specialize identify_impacts with a script") {
		t.Fatalf("proposals: %s", joined)
	}
	if op, err = e.Submit(ctx, op.ID, decisions); err != nil {
		t.Fatal(err)
	}
	if op, err = e.Run(ctx, op.ID); err != nil || op.Status != StatusCompleted {
		t.Fatalf("observer end: %v %+v", err, op)
	}
	if len(drafts.saved) != 1 {
		t.Fatalf("drafts: %+v", drafts.saved)
	}
	d := drafts.saved[0]
	if d.Version != "1.1.1" || drafts.who[0].Subject != "mia" {
		t.Fatalf("draft version %s by %+v", d.Version, drafts.who[0])
	}
	var spec *methodology.Action
	for i, a := range d.Actions {
		if a.Name == "identify_impacts_script" {
			spec = &d.Actions[i]
		}
	}
	if spec == nil || spec.Specializes != "identify_impacts" || spec.Kind != "script" || !strings.Contains(spec.Code, "function run(ctx)") {
		t.Fatalf("specialization not drafted: %+v", d.Actions)
	}
	if is := d.Validate(); len(is) > 0 {
		t.Fatalf("draft must be valid: %v", is)
	}
	// the observer itself is journaled on its change
	recs, _ := g.Journal(ctx, domain.ExecutionFilter{ChangeID: op.ChangeID})
	if len(recs) == 0 || recs[len(recs)-1].Kind != domain.ExecProcessEnded {
		t.Fatalf("observer journal: %+v", recs)
	}
}
