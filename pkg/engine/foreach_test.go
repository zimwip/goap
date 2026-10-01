package engine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/pkg/methodology"
)

// built is true per element: an artifact "build" naming the component, read through vars.item.key.
const foreachYAML = `
name: forked
version: 1.0.0
namespace: alm
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.type == "build" && a.data.component == vars.item.key)'}
actions:
  - {name: build_java, kind: human, description: Build with maven, effects: {built: true}}
  - {name: build_go, kind: human, description: Build with go, effects: {built: true}}
  - {name: build_any, kind: human, description: Build by hand, effects: {built: true}}
methods:
  - name: java
    for: building
    when: 'vars.item.key == "java" || vars.item.lang == "java"'
    actions: [build_java]
    done: {built: true}
  - name: golang
    for: building
    when: 'vars.item.key == "go" || vars.item.lang == "go"'
    actions: [build_go]
    done: {built: true}
  - name: generic
    for: building
    actions: [build_any]
    done: {built: true}
processes:
  - name: delivery
    steps:
      - {name: build, method: building, %s}
`

func forkedEngine(t *testing.T, step string) *Engine {
	t.Helper()
	e, _, _ := setup(t)
	m, err := methodology.Parse([]byte(strings.Replace(foreachYAML, "%s", step, 1)))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	e.Methodologies.(StaticMethodologies)["forked"] = c
	e.Schedule = func(id string) { e.background(func() { _, _ = e.Run(context.Background(), id) }) }
	return e
}

// streams returns the children of the step, by element.
func streams(t *testing.T, e *Engine, p *Process) map[string]*Process {
	t.Helper()
	out := map[string]*Process{}
	for key, id := range p.Children {
		if _, el, ok := strings.Cut(key, foreachSuffix); ok {
			c, err := e.Store.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			out[el] = c
		}
	}
	return out
}

func finishStreams(t *testing.T, e *Engine, p *Process, components ...string) {
	t.Helper()
	ctx := context.Background()
	for _, el := range components {
		c := streams(t, e, p)[el]
		if c == nil || c.Pending == nil {
			t.Fatalf("no stream waiting for %s: %+v", el, c)
		}
		if _, err := e.Submit(ctx, c.ID, []ItemInput{{Kind: "artifact", Type: "build", Data: map[string]any{"component": el}}}); err != nil {
			t.Fatal(err)
		}
		e.schedule(c.ID)
		e.Drain()
	}
}

// One stream per element, each with the most specific method for it, all in flight at once; the step is done when
// every stream is.
func TestForeachRunsOneStreamPerElement(t *testing.T) {
	ctx := context.Background()
	e := forkedEngine(t, `foreach: 'vars.components'`)
	p, err := e.Start(ctx, StartRequest{Methodology: "forked", Goal: "delivery", Intent: "build", ProjectID: testProject,
		Vars: map[string]any{"components": []any{
			map[string]any{"key": "api", "lang": "go"},
			map[string]any{"key": "web", "lang": "java"},
			map[string]any{"key": "docs", "lang": "md"},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusWaiting || p.Pending.Kind != TaskAgent {
		t.Fatalf("expected the step waiting for its streams: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	got := streams(t, e, p)
	if len(got) != 3 {
		t.Fatalf("streams %v", got)
	}
	for el, want := range map[string]string{"api": "golang", "web": "java", "docs": "generic"} {
		c := got[el]
		if c.Agent != want || c.Step == nil || c.Step.Method != want || c.Step.ItemKey != el || c.Vars["item"] == nil {
			t.Fatalf("stream %s: agent %s step %+v vars %v, want method %s", el, c.Agent, c.Step, c.Vars, want)
		}
		if c.Pending == nil {
			t.Fatalf("stream %s is not at work: %s", el, c.Status)
		}
	}
	var lanes []Lane
	for _, st := range mustProgress(t, e, p.ID).Steps {
		lanes = append(lanes, st.Lanes...)
	}
	if len(lanes) != 3 || lanes[0].Item != "api" || lanes[0].Method != "golang" {
		t.Fatalf("progress shows one lane per stream: %+v", lanes)
	}
	// the step is not done until every stream is
	finishStreams(t, e, p, "api", "web")
	if p, _ = e.Store.Get(ctx, p.ID); p.Status == StatusCompleted {
		t.Fatal("done with a stream still at work")
	}
	finishStreams(t, e, p, "docs")
	p, _ = e.Store.Get(ctx, p.ID)
	if p.Status != StatusCompleted {
		t.Fatalf("not completed: %s %+v %s", p.Status, p.Pending, p.Error)
	}
	bb, _ := e.Graph.Blackboard(ctx, p.ChangeID)
	for _, it := range bb.Change.Items {
		if it.Type == methodology.ArtifactStepDone && it.Data["step"] == "delivery/build" {
			ids, _ := it.Data["items"].([]any)
			var s []string
			for _, x := range ids {
				s = append(s, x.(string))
			}
			slices.Sort(s)
			if strings.Join(s, ",") != "api,docs,web" {
				t.Fatalf("items %v", s)
			}
			return
		}
	}
	t.Fatal("no step_done for the step")
}

// groupBy: one stream per group, whose item is {key, items}.
func TestForeachGroupsTheElements(t *testing.T) {
	ctx := context.Background()
	e := forkedEngine(t, `foreach: 'vars.components', groupBy: 'vars.item.lang'`)
	p, err := e.Start(ctx, StartRequest{Methodology: "forked", Goal: "delivery", Intent: "build", ProjectID: testProject,
		Vars: map[string]any{"components": []any{
			map[string]any{"key": "a", "lang": "java"}, map[string]any{"key": "b", "lang": "go"}, map[string]any{"key": "c", "lang": "java"},
		}}})
	if err != nil {
		t.Fatal(err)
	}
	p, _ = e.Run(ctx, p.ID)
	got := streams(t, e, p)
	if len(got) != 2 || got["java"] == nil || got["go"] == nil {
		t.Fatalf("one stream per language: %v", got)
	}
	item, _ := got["java"].Vars["item"].(map[string]any)
	if members, _ := item["items"].([]any); item["key"] != "java" || len(members) != 2 || got["java"].Agent != "java" || got["go"].Agent != "golang" {
		t.Fatalf("group %v agents %s/%s", item, got["java"].Agent, got["go"].Agent)
	}
	finishStreams(t, e, p, "java", "go")
	if p, _ = e.Store.Get(ctx, p.ID); p.Status != StatusCompleted {
		t.Fatalf("not completed: %s %+v %s", p.Status, p.Pending, p.Error)
	}
}

func TestForeachValidation(t *testing.T) {
	for name, tc := range map[string]struct{ step, want string }{
		"not a list":       {`{name: s, method: building, foreach: '1 + 1'}`, "must return a list"},
		"bad expression":   {`{name: s, method: building, foreach: 'nope('}`, "foreach"},
		"groupBy alone":    {`{name: s, method: building, groupBy: 'vars.item.k'}`, "declare foreach"},
		"not a capability": {`{name: s, action: build_any, foreach: 'vars.components'}`, "naming a capability"},
	} {
		t.Run(name, func(t *testing.T) {
			m, err := methodology.Parse([]byte(strings.Replace(foreachYAML, "{name: build, method: building, %s}", tc.step, 1)))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.Compile(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
}
