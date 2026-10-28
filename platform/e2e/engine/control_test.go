package enginee2e

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/engine/engine"
	"github.com/zimwip/goap/engine/methodology"
	"github.com/zimwip/goap/graph/graph"
	"github.com/zimwip/goap/graph/graph/graphtest"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/typecat"
)

// controlledYAML builds under the architecture decisions of the project (ADR 0162): the design collects them (the
// condition is its output), the build uses that condition as a control, saying which rules govern it, and its check
// measures them on each component.
const controlledYAML = `
name: controlled
version: 1.0.0
namespace: alm
roles:
  - {name: dev}
conditions:
  - name: architecture_decided
    description: The architecture decisions of the change are collected (there may be none)
    expr: 'artifacts.exists(a, a.target == "architecture")'
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: rules_checked, scope: impact, expr: 'artifacts.exists(a, a.target == "control_report" && a.data.subject == self.key)'}
  - {name: all_checked, all: rules_checked}
actions:
  - {name: decide, kind: human, description: Collect the architecture decisions, effects: {architecture_decided: true}}
  - {name: verify_rules, kind: builtin, builtin: control.check, scope: impact, pre: {impacted: true}, effects: {rules_checked: true}}
processes:
  - name: delivery
    roles: {responsible: dev}
    steps:
      - {name: design, action: decide}
      - name: build
        action: verify_rules
        controls:
          - condition: architecture_decided
            types: [alm@ArchitectureDecision]
            via: alm@governs
            where: 'self.state == "accepted"'
            rules:
              - {id: DOC, statement: Every component says what it is for}
`

// the check of ADR-1: no component the subject depends on (a violation per row)
const noDependency = "---\nlanguage: cypher\n---\n```cypher\nMATCH (subject)-[:depends_on]->(d) RETURN d.key AS at, 'depends on another component' AS msg\n```"

func TestAControlGovernsAndMeasuresAnActivity(t *testing.T) {
	ctx := authz.With(context.Background(), admin)
	e, g, _ := setup(t)
	typed(t, g)
	cmp2, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "CMP-2", Type: "alm@Component"})
	if err != nil {
		t.Fatal(err)
	}
	cmp1, err := graphtest.Import(ctx, g, graphtest.Node{Namespace: "alm", Key: "CMP-PAY", Type: "alm@Component",
		Links: []graph.LinkWrite{{Type: "alm@depends_on", To: cmp2.Ref()}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []graphtest.Node{
		{Key: "ADR-1", State: "accepted", Properties: map[string]any{"statement": "A component depends on no other component", "check": noDependency, "fix": "go through an interface"},
			Links: []graph.LinkWrite{{Type: "alm@governs", To: cmp1.Ref()}}},
		{Key: "ADR-2", State: "accepted", Properties: map[string]any{"statement": "Governs nothing here"}},
		{Key: "ADR-3", State: "proposed", Properties: map[string]any{"statement": "Not in force yet"}, Links: []graph.LinkWrite{{Type: "alm@governs", To: cmp1.Ref()}}},
	} {
		r.Namespace, r.Type = "alm", "alm@ArchitectureDecision"
		if _, err := graphtest.Import(ctx, g, r); err != nil {
			t.Fatal(err)
		}
	}
	head, err := g.BranchHead(ctx, "alm", domain.MainBranch)
	if err != nil {
		t.Fatal(err)
	}
	m, err := methodology.Parse([]byte(controlledYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["controlled"] = c
	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "Pay", Intent: "build the payment", Methodology: "controlled", Namespace: "alm", ProjectID: testProject, ForkCommit: head.ID})
	if err != nil {
		t.Fatal(err)
	}
	ref := cmp1.Ref()
	if _, err := g.ProposeImpact(ctx, ch.ID, []domain.ChangeImpact{{Intent: domain.IntentModified, Pre: &ref, Rationale: "built"}}); err != nil {
		t.Fatal(err)
	}

	// before the decisions are collected, the control is pending: the build waits for it
	set, err := e.ControlOf(ctx, ch.ID, "verify_rules", "delivery/build", "CMP-PAY")
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Controls) != 1 || set.Controls[0].State != engine.ControlPending || len(set.Rules()) != 0 {
		t.Fatalf("a control not collected is pending, with no rule: %+v", set)
	}
	p, err := e.Start(ctx, engine.StartRequest{Methodology: "controlled", Goal: "delivery", ChangeID: ch.ID, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusWaiting || p.Pending == nil || p.Steps[p.Pending.Step].Action != "delivery/design" {
		t.Fatalf("the design collects the decisions first: %s %+v", p.Status, p.Pending)
	}
	if p, err = e.Submit(ctx, p.ID, []engine.ItemInput{{Type: "artifact", Target: "architecture", Data: map[string]any{"decisions": []any{"ADR-1"}}}}); err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != engine.StatusCompleted {
		t.Fatalf("the build ran its check: %s %s", p.Status, p.Error)
	}

	// once collected: the general rule and ADR-1, linked to the component and accepted; not ADR-2 (not linked), nor
	// ADR-3 (proposed, not in force)
	set, err = e.ControlOf(ctx, ch.ID, "verify_rules", "delivery/build", "CMP-PAY")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range set.Rules() {
		ids = append(ids, r.ID)
	}
	if set.Controls[0].State != engine.ControlEstablished || strings.Join(ids, ",") != "DOC,ADR-1" {
		t.Fatalf("established control, its rules: %v %+v", ids, set)
	}
	var run *engine.Step
	for i := range p.Steps {
		if p.Steps[i].Subject == "CMP-PAY" {
			run = &p.Steps[i]
		}
	}
	if run == nil || !slices.Contains(run.Controls, "ADR-1@1") || !slices.Contains(run.Controls, "architecture_decided/DOC") {
		t.Fatalf("the run records the rules it was given: %+v", run)
	}
	cur, err := g.Change(ctx, ch.ID)
	if err != nil {
		t.Fatal(err)
	}
	var report map[string]any
	for _, it := range cur.Items {
		if it.Target == "control_report" {
			report = it.Data
		}
	}
	if report == nil || report["subject"] != "CMP-PAY" || report["ok"] != false || report["open"] != float64(1) || report["unmeasured"] != float64(1) {
		t.Fatalf("the report answers with the rule broken: %+v", report)
	}
	vs, _ := report["violations"].([]any)
	v, _ := vs[0].(map[string]any)
	if len(vs) != 1 || v["rule"] != "ADR-1" || v["where"] != "CMP-2" || v["fix"] != "go through an interface" {
		t.Fatalf("one violation, where it is and how to fix it: %+v", vs)
	}
	if !strings.Contains(run.Output, "ADR-1 [veto] CMP-2: depends on another component → go through an interface") {
		t.Fatalf("the output says it in a line: %q", run.Output)
	}
}

// Collected and empty: the control is established with no rule of the domain, the activity runs (ADR 0162: no rule
// is a valid answer, not a missing collection).
func TestAControlCollectedWithNoRuleIsEstablished(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	m, err := methodology.Parse([]byte(strings.Replace(controlledYAML, "            rules:\n              - {id: DOC, statement: Every component says what it is for}\n", "", 1)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(engine.StaticMethodologies)["controlled"] = c
	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "Pay", Intent: "build", Methodology: "controlled", Namespace: "alm", ProjectID: testProject, ForkCommit: base})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.AddItems(ctx, ch.ID, []domain.ChangeItem{{Type: domain.TypeArtifact, Target: "architecture", Data: map[string]any{}}}); err != nil {
		t.Fatal(err)
	}
	set, err := e.ControlOf(ctx, ch.ID, "verify_rules", "delivery/build", "CMP-PAY")
	if err != nil {
		t.Fatal(err)
	}
	if !set.Established() || len(set.Rules()) != 0 {
		t.Fatalf("collected, no rule: %+v", set)
	}
}

// typed makes the graph judge by the types of the repository's domains (the lifecycle of the rules, ADR 0162).
func typed(t *testing.T, g *graph.Graph) {
	t.Helper()
	ds, err := def.LoadDomains("../../../domains")
	if err != nil {
		t.Fatal(err)
	}
	cat, err := typecat.New(ds...)
	if err != nil {
		t.Fatal(err)
	}
	g.Types = func() graph.TypeCatalog { return cat }
}
