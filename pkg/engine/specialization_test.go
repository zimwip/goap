package engine

import (
	"context"
	"testing"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/intent"
	"github.com/zimwip/goap/pkg/methodology"
)

const buildMethodology = `
name: demo
version: "1.0.0"
domain: {nodeTypes: [Component]}
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.type == "build")'}
actions:
  - {name: build, kind: abstract, effects: {built: true}, cost: 1}
  - {name: build_java, kind: builtin, builtin: build.java, specializes: build, when: 'vars.lang == "java"', priority: 1}
  - {name: build_any, kind: builtin, builtin: build.any, specializes: build}
goals:
  - {name: build_it, pre: {built: true}}
`

const goMethodology = `
name: golang
version: "1.0.0"
conditions:
  - {name: x, expr: 'true'}
actions:
  - {name: noop, kind: human, effects: {x: true}}
  - {name: build_go, kind: builtin, builtin: build.go, specializes: demo/build, when: 'vars.lang == "go"', priority: 5}
goals:
  - {name: g, pre: {x: true}}
`

func compile(t *testing.T, src string) *methodology.Compiled {
	t.Helper()
	m, err := methodology.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	cm, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	return cm
}

func TestSpecializationChosenAtExecution(t *testing.T) {
	ctx := context.Background()
	demo, golang := compile(t, buildMethodology), compile(t, goMethodology)
	if acts := demo.PlanningActions(); len(acts) != 1 || acts[0].Name != "build" {
		t.Fatalf("specializations must not be planned: %+v", acts)
	}
	builtin := func(tag string) BuiltinFunc {
		return func(context.Context, ActionContext) (ActionResult, error) {
			return ActionResult{Items: []ItemInput{{Kind: "artifact", Type: "build", Data: map[string]any{"by": tag}}}}, nil
		}
	}
	g := graph.New(graph.NewMemory())
	b, _ := g.CreateBaseline(ctx, "B0", nil)
	e := &Engine{Graph: g, Methodologies: StaticMethodologies{"demo": demo, "golang": golang},
		Executors: map[string]Executor{methodology.KindBuiltin: BuiltinExecutor{"build.java": builtin("java"), "build.any": builtin("any"), "build.go": builtin("go")}},
		Intent:    intent.Resolver{Ranker: intent.Lexical{}}, Store: NewMemoryStore()}
	for lang, want := range map[string]string{"java": "build_java", "c": "build_any", "go": "golang/build_go"} {
		p, err := e.Start(ctx, StartRequest{Methodology: "demo", Goal: "build_it", BaselineID: b.ID, Intent: "build", Vars: map[string]any{"lang": lang}})
		if err != nil {
			t.Fatal(err)
		}
		if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
			t.Fatalf("%s: %v %+v", lang, err, p)
		}
		if s := p.Steps[0]; s.Action != "build" || s.Specialization != want {
			t.Fatalf("%s: step %+v", lang, s)
		}
		recs, _ := g.Journal(ctx, domain.ExecutionFilter{ProcessIDs: []string{p.ID}})
		var found bool
		for _, r := range recs {
			found = found || (r.Kind == domain.ExecAction && r.Specialization == want && r.ActionKind == "builtin")
		}
		if !found {
			t.Fatalf("%s: specialization not journaled: %+v", lang, recs)
		}
	}
}

func TestAbstractActionWithoutSpecialization(t *testing.T) {
	src := `
name: abs
version: "1.0.0"
conditions: [{name: done, expr: 'artifacts.size() > 0'}]
actions: [{name: work, kind: abstract, effects: {done: true}}]
goals: [{name: g, pre: {done: true}}]
`
	ctx := context.Background()
	g := graph.New(graph.NewMemory())
	b, _ := g.CreateBaseline(ctx, "B0", nil)
	e := &Engine{Graph: g, Methodologies: StaticMethodologies{"abs": compile(t, src)}, Executors: map[string]Executor{},
		Intent: intent.Resolver{Ranker: intent.Lexical{}}, Store: NewMemoryStore(), MaxFailures: 1}
	p, _ := e.Start(ctx, StartRequest{Methodology: "abs", Goal: "g", BaselineID: b.ID, Intent: "x"})
	p, _ = e.Run(ctx, p.ID)
	if p.Status != StatusStuck || p.Steps[0].Error == "" {
		t.Fatalf("abstract action must fail then disable: %+v", p)
	}
}

func TestSpecializationValidation(t *testing.T) {
	bad := `
name: bad
version: "1.0.0"
conditions: [{name: c, expr: 'true'}]
actions:
  - {name: a, kind: human, effects: {c: true}}
  - {name: s1, kind: human, specializes: missing}
  - {name: s2, kind: human, specializes: a, effects: {c: true}}
  - {name: s3, kind: abstract, specializes: a}
  - {name: s4, kind: human, when: 'true'}
  - {name: s5, kind: human, specializes: a, when: 'nope('}
agents: [{name: ag, actions: [s1]}]
goals: [{name: g, pre: {c: true}}]
`
	m, err := methodology.Parse([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	issues := m.Validate()
	want := map[string]bool{"actions[1].specializes": false, "actions[2].specializes": false, "actions[3].kind": false,
		"actions[4].when": false, "actions[5].when": false, "agents[0].actions[0]": false}
	for _, is := range issues {
		if _, ok := want[is.Path]; ok {
			want[is.Path] = true
		}
	}
	for path, seen := range want {
		if !seen {
			t.Errorf("missing issue at %s: %v", path, issues)
		}
	}
}
