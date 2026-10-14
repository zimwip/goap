package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/form"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/methodology"
)

const formMethodology = `
name: linking
version: 1.0.0
namespace: alm
conditions:
  - name: linked
    expr: artifacts.exists(a, a.type == "link_request") || changeImpacts.exists(n, n.key == "REQ-1")
goals:
  - name: link
    examples: [link the requirement]
    pre: {linked: true}
actions:
  - name: link_requirement
    kind: human
    effects: {linked: true}
    form:
      title: Link the requirement
      artifact: link_request
      fields:
        - {name: requirement, widget: node, required: true, constraints: {types: [alm@Requirement]}}
        - {name: tests, widget: link, constraints: {from: requirement, linkTypes: [alm@verifies], toTypes: [alm@TestCase], max: 3}}
        - {name: why, widget: textarea, required: true}
`

// startForm starts the linking methodology on the graph of setup, with the code given to its human action.
func startForm(t *testing.T, code string) (*Engine, *graph.Graph, *Process) {
	t.Helper()
	ctx := context.Background()
	e, g, base := setup(t)
	src := formMethodology
	if code != "" {
		src += "    language: javascript\n    code: |\n      " + strings.ReplaceAll(code, "\n", "\n      ") + "\n"
	}
	m, err := methodology.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies = StaticMethodologies{cm.Name: cm}
	e.Executors[methodology.KindScript] = ScriptExecutor{Sandboxes: InprocSandboxes{}}
	p, err := e.Start(ctx, StartRequest{Methodology: "linking", Goal: "link", BaselineID: base, ProjectID: testProject, Intent: "link the requirement"})
	if err != nil {
		t.Fatal(err)
	}
	if p, err = e.Run(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Form == nil || p.Pending.Form.Title != "Link the requirement" {
		t.Fatalf("the task carries its form: %s %+v", p.Status, p.Pending)
	}
	return e, g, p
}

func TestSubmitFormWithoutCode(t *testing.T) {
	ctx := context.Background()
	e, g, p := startForm(t, "")
	// the constraints are checked by the engine
	_, err := e.SubmitForm(ctx, p.ID, map[string]any{"requirement": "CMP-1", "tests": []any{map[string]any{"to": "REQ-1"}}})
	if !errors.Is(err, graph.ErrInvalid) {
		t.Fatalf("refused: %v", err)
	}
	for _, want := range []string{"requirement: CMP-1 is a alm@Component", "tests: link 1: REQ-1 is a alm@Requirement", "why: required"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
	if _, err := e.Submit(ctx, p.ID, nil); err != nil {
		t.Fatalf("items still accepted: %v", err)
	}
	e2, g2, p2 := startForm(t, "")
	e, g, p = e2, g2, p2
	p, err = e.SubmitForm(ctx, p.ID, map[string]any{"requirement": "REQ-1", "tests": []any{map[string]any{"to": "TST-1"}}, "why": "covered"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	var art *domain.ChangeItem
	for i, it := range c.Items {
		if it.Type == "link_request" {
			art = &c.Items[i]
		}
	}
	if art == nil || art.Data["why"] != "covered" {
		t.Fatalf("the values are an artifact: %+v", c.Items)
	}
	// the link field wrote the link on the draft of its source
	var req *domain.ChangeImpact
	for i, n := range c.Nodes {
		if n.Key == "REQ-1" {
			req = &c.Nodes[i]
		}
	}
	if req == nil || req.Post == nil {
		t.Fatalf("REQ-1 is written by the change: %+v", c.Nodes)
	}
	v, err := g.ChangeNodeView(ctx, p.ChangeID, "", *req.Post)
	if err != nil {
		t.Fatal(err)
	}
	nodes, _, _ := g.BaselineGraph(ctx, c.BaselineID)
	var tst domain.NodeID
	for _, n := range nodes {
		if n.Key == "TST-1" {
			tst = n.ID
		}
	}
	found := false
	for _, l := range v.Out {
		if l.Type == "alm@verifies" && l.To.ID == tst {
			found = true
		}
	}
	if !found {
		t.Fatalf("link written: %+v", v.Out)
	}
}

func TestSubmitFormRunsItsCode(t *testing.T) {
	ctx := context.Background()
	e, g, p := startForm(t, `const f = ctx.var("form");
ctx.addArtifact("link_request", {about: f.requirement, reason: f.why.toUpperCase()});`)
	p, err := e.SubmitForm(ctx, p.ID, map[string]any{"requirement": "REQ-1", "why": "covered"})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	for _, it := range c.Items {
		if it.Type == "link_request" && it.Data["reason"] == "COVERED" && it.Data["about"] == "REQ-1" {
			return
		}
	}
	t.Fatalf("the code made the items: %+v", c.Items)
}

func TestFormCompileIssues(t *testing.T) {
	m, err := methodology.Parse([]byte(strings.Replace(formMethodology, "widget: textarea", "widget: slider", 1) +
		"    language: javascript\n    code: x\n  - name: other\n    kind: llm\n    prompt: p\n    effects: {linked: true}\n    form: {fields: [{name: a, widget: text}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Compile()
	if err == nil {
		t.Fatal("compiled")
	}
	for _, want := range []string{`actions[0].form.fields[2].widget: unknown widget "slider"`, "actions[1].form: a form belongs to a human action"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestSubmitFormCreatesAndRules(t *testing.T) {
	ctx := context.Background()
	e, g, p := startForm(t, "")
	m, _ := e.methodologyOf(ctx, p)
	a, _ := m.Action("link_requirement")
	a.Form.Fields = []form.Field{
		{Name: "req", Widget: form.WidgetNewNode, Required: true, Constraints: map[string]any{"types": []any{"alm@Requirement"}}},
		{Name: "tests", Widget: form.WidgetLink, Constraints: map[string]any{"from": "req", "linkTypes": []any{"alm@verifies"}}},
		{Name: "choice", Widget: form.WidgetDecision},
	}
	d, err := g.OpenDecision(ctx, p.ChangeID, graph.OpenDecisionRequest{Question: "ship?", Options: []string{}, By: "t"})
	if err != nil {
		t.Fatal(err)
	}
	p, err = e.SubmitForm(ctx, p.ID, map[string]any{
		"req":    map[string]any{"key": "REQ-NEW", "type": "alm@Requirement", "props": map[string]any{"title": "A new need"}},
		"tests":  []any{map[string]any{"to": "TST-1"}},
		"choice": map[string]any{"point": d.ID, "option": "go", "justification": "ready"},
	})
	if err != nil {
		t.Fatal(err)
	}
	c, _ := g.Change(ctx, p.ChangeID)
	var created *domain.ChangeImpact
	for i, n := range c.Nodes {
		if n.Key == "REQ-NEW" {
			created = &c.Nodes[i]
		}
	}
	if created == nil || created.Intent != domain.IntentCreated || created.Post == nil {
		t.Fatalf("REQ-NEW is created by the change: %+v", c.Nodes)
	}
	v, err := g.ChangeNodeView(ctx, p.ChangeID, "", *created.Post)
	if err != nil {
		t.Fatal(err)
	}
	if v.Node.Properties["title"] != "A new need" || len(v.Out) != 1 || v.Out[0].Type != "alm@verifies" {
		t.Fatalf("created with its properties and link: %+v %+v", v.Node.Properties, v.Out)
	}
	bb, _ := g.BlackboardIn(ctx, p.ChangeID, "")
	for _, pt := range domain.DecisionPointsOf(bb) {
		if pt.ID == d.ID && pt.Ruling != nil && pt.Ruling.Option == "go" {
			return
		}
	}
	t.Fatalf("the point is ruled: %+v", domain.DecisionPointsOf(bb))
}
