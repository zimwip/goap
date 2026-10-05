package engine

import (
	"context"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/domain/def"
	"github.com/zimwip/goap/pkg/dsl"
	"github.com/zimwip/goap/pkg/graph"
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
		version = "1.2.0"
	}
	m, ok := d.defs[name+"@"+version]
	return m, ok, nil
}

func (d *memDrafts) SaveDraft(ctx context.Context, m methodology.Methodology) (def.Issues, error) {
	d.saved = append(d.saved, m)
	d.who = append(d.who, authz.From(ctx))
	d.defs[m.Name+"@"+m.Version] = m
	return m.Validate(), nil
}

// storeDefinition writes the definition nodes of a methodology version as the registry does (ADR 0023): the version
// node and one node per element, typed by the meta-domain methodology.
func storeDefinition(ctx context.Context, g *graph.Graph, base domain.BaselineID, m *methodology.Methodology) (graph.CommitResult, error) {
	hkey := observe.ElementKey(m.Name, m.Version, "methodology", "")
	head := graph.NodeEdit{Key: hkey, Type: "methodology@MethodologyVersion", Props: map[string]any{"name": m.Name, "version": m.Version, "status": "published"}}
	edits := []graph.NodeEdit{}
	el := func(kind, typ, name string, props map[string]any) {
		k := observe.ElementKey(m.Name, m.Version, kind, name)
		props["name"] = name
		edits = append(edits, graph.NodeEdit{Key: k, Type: typ, Props: props})
		head.Links = append(head.Links, graph.LinkEdit{Type: "methodology@defines", ToKey: k})
	}
	for _, a := range m.Actions {
		el("action", "methodology@Action", a.Name, map[string]any{"kind": a.Kind, "cost": a.Cost, "model": a.Model})
	}
	for _, a := range m.Agents {
		el("agent", "methodology@Agent", a.Name, map[string]any{"actions": a.Actions})
	}
	return g.Commit(ctx, graph.Commit{Namespace: "methodology", Title: "Methodology " + m.Name, Baseline: base, By: "test", Edits: append(edits, head)})
}

func TestSelfObservationProposesAndDrafts(t *testing.T) {
	// drafting a methodology is administration (ADR 0043)
	ctx := authz.With(context.Background(), authz.Principal{Subject: "mia", Org: "acme", Roles: []string{"admin"}})
	e, g, base := setup(t)
	// the observed run; the intent text deliberately echoes the
	// "assess_impact" goal example in methodologies/examples/impact-analysis.yaml.
	p, err := e.Start(ctx, StartRequest{Methodology: "impact-analysis", BaselineID: base, Intent: "The PSP changes its API, what does this break?", ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("observed run: %v %+v", err, p)
	}
	// the observer methodology, and the definition nodes of the observed one in the graph
	obsM, err := methodology.LoadFile("../../methodologies/methodology-improvement.yaml")
	if err != nil {
		t.Fatal(err)
	}
	obs, err := obsM.Compile()
	if err != nil {
		t.Fatal(err)
	}
	impact, _ := e.Methodologies.Methodology(ctx, "impact-analysis")
	e.Methodologies = StaticMethodologies{"impact-analysis": impact, obs.Name: obs}
	// the definition nodes live in the methodology namespace, not the observed run's (alm)
	mbase, err := g.BranchHead(ctx, "methodology", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	res, err := storeDefinition(ctx, g, mbase.ID, impact.Methodology)
	if err != nil {
		t.Fatalf("definition nodes: %v", err)
	}
	drafts := &memDrafts{defs: map[string]methodology.Methodology{"impact-analysis@1.2.0": *impact.Methodology}}
	builtins := e.Executors[methodology.KindBuiltin].(BuiltinExecutor)
	for k, v := range e.SelfImprovementBuiltins(SelfImprovement{Drafts: drafts, Thresholds: observe.Thresholds{LLMCalls: 1, MinSystematized: 1}}) {
		builtins[k] = v
	}

	op, err := e.Start(ctx, StartRequest{Methodology: obs.Name, Agent: "observer", Goal: "improve_methodology", BaselineID: res.Baseline.ID,
		Intent: "observe", Vars: map[string]any{"event": map[string]any{"process": map[string]any{"id": p.ID}}}, ProjectID: testProject})
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
		if it.Type == "cost_report" {
			report = it.Data
		}
	}
	// every improvement is a change impact on an element of the methodology, with the proposed version written
	for _, n := range c.Nodes {
		titles = append(titles, n.Rationale)
		if n.Post == nil || n.Review != domain.ReviewProposed {
			t.Fatalf("an improvement is written and awaits its review: %+v", n)
		}
		decisions = append(decisions, ItemInput{Kind: "changeImpact", ChangeImpact: &dsl.NodeOp{Op: "review", Node: n.Key, Accept: true, Comment: "worth it"}})
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
	if d.Version != "1.2.1" || drafts.who[0].Subject != "mia" {
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
