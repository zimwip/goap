package methodology

import (
	"slices"
	"strings"
	"testing"
)

func levelOf(t *testing.T, ls []LevelCheck, path string) LevelCheck {
	t.Helper()
	for _, l := range ls {
		if l.Path == path {
			return l
		}
	}
	t.Fatalf("no level %s in %+v", path, ls)
	return LevelCheck{}
}

func TestCheckLevelsChainsInputsToOutputs(t *testing.T) {
	c, issues := compileProcess(t, `
processes:
  - name: flow
    steps:
      - name: phase
        pre: {c: false}
        steps:
          - {name: first, action: do_a}
          - {name: second, action: do_b}
      - {name: last, action: do_c, pre: {b: true}}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ls, ok := c.CheckLevels("flow")
	// the process and its phase: an action is a node, not a level
	if !ok || len(ls) != 2 {
		t.Fatalf("levels %+v, %v", ls, ok)
	}
	root := levelOf(t, ls, "flow")
	if !root.OK() || len(root.Order) != 2 || root.Order[0].Name != "phase" || root.Order[1].Name != "last" || root.Order[1].Layer != 1 {
		t.Fatalf("root: %+v", root)
	}
	phase := levelOf(t, ls, "flow/phase")
	if !phase.OK() || phase.Order[0].Name != "first" || phase.Order[1].Name != "second" {
		t.Fatalf("phase: %+v", phase)
	}
}

func TestCheckLevelsFindsACycleAndAMissingInput(t *testing.T) {
	c, issues := compileProcess(t, `
processes:
  - name: loop
    steps:
      - {name: x, action: do_a, done: {a: true}, pre: {b: true}}
      - {name: y, action: do_b, done: {b: true}}
  - name: missing
    steps:
      - name: phase
        pre: {a: true}
        steps:
          - {name: only, action: do_c, pre: {b: true}}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ls, _ := c.CheckLevels("loop")
	root := levelOf(t, ls, "loop")
	if root.OK() {
		t.Fatalf("x needs b, y needs a (do_b pre a), nothing starts: %+v", root)
	}
	if g := root.Gaps[0]; !strings.Contains(g.Message, "cycle") {
		t.Fatalf("gaps %+v", root.Gaps)
	}
	// what a sub-step needs and nothing gives is demanded of the context, level after level, up to the root
	ls, _ = c.CheckLevels("missing")
	phase := levelOf(t, ls, "missing/phase")
	rootLevel := levelOf(t, ls, "missing")
	if !phase.OK() || len(phase.Gaps) != 0 || !phase.Inputs["b"] {
		t.Fatalf("phase: %+v", phase)
	}
	if len(rootLevel.Gaps) != 1 || rootLevel.Gaps[0].Kind != GapExternal || !slices.Contains(rootLevel.Gaps[0].Missing, "b") {
		t.Fatalf("root: %+v", rootLevel)
	}
}

// A condition a sub-step takes from a sibling of its parent is chained at the level above, not reported as missing.
func TestCheckLevelsLiftsWhatASubStepTakesFromASibling(t *testing.T) {
	c, issues := compileProcess(t, `
processes:
  - name: lift
    steps:
      - name: analysis
        steps:
          - {name: impacts, action: do_a}
          - name: specification
            steps:
              - {name: requirements, action: do_b}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ls, _ := c.CheckLevels("lift")
	for _, l := range ls {
		if !l.OK() || len(l.Gaps) > 0 {
			t.Fatalf("%s: %+v", l.Path, l)
		}
	}
	an := levelOf(t, ls, "lift/analysis")
	if an.Order[0].Name != "impacts" || an.Order[1].Name != "specification" {
		t.Fatalf("specification waits for what impacts makes: %+v", an.Order)
	}
}

// A step not resolved inside is flagged in the level above, which then cannot run through it.
func TestCheckLevelsSurfacesAnIncompleteSubStep(t *testing.T) {
	c, issues := compileProcess(t, `
processes:
  - name: inner
    steps:
      - name: phase
        steps:
          - {name: x, action: do_a, done: {a: true}, pre: {b: true}}
          - {name: y, action: do_b, done: {b: true}}
      - {name: after, action: do_c}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ls, _ := c.CheckLevels("inner")
	root := levelOf(t, ls, "inner")
	if root.OK() || len(root.Steps) != 2 || !root.Steps[0].Broken || !root.Steps[0].Composite || root.Steps[1].Broken {
		t.Fatalf("root: %+v", root)
	}
	found := false
	for _, g := range root.Gaps {
		found = found || (g.Kind == GapInner && g.Step == "phase")
	}
	if !found {
		t.Fatalf("gaps %+v", root.Gaps)
	}
}

// A method of actions has, as its level, the actions its agent carries out; an action itself is a node.
func TestCheckLevelsDownToTheActions(t *testing.T) {
	c, issues := compileProcess(t, `
methods:
  - name: by_agent
    for: working
    actions: [do_c]
    done: {c: true}
processes:
  - name: flow
    steps:
      - {name: work, method: working}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ms, ok := c.CheckLevels("by_agent")
	if !ok || len(ms) != 1 || ms[0].Kind != LevelMethod || ms[0].Agent != "by_agent" || len(ms[0].Steps) == 0 || ms[0].Steps[0].Name != "do_c" {
		t.Fatalf("method level: %+v, %v", ms, ok)
	}
	if g, ok := c.ProcessGraph("by_agent"); !ok || g.Process != "by_agent" {
		t.Fatalf("method graph: %+v, %v", g, ok)
	}
}

// A step naming a capability opens on the methods that specialize it, not on the process around it.
func TestCheckLevelsOfACapabilityStepListsItsMethods(t *testing.T) {
	c, issues := compileProcess(t, `
methods:
  - name: by_agent
    for: working
    actions: [do_c]
    done: {c: true}
  - name: by_steps
    for: working
    steps:
      - {name: one, action: do_c}
processes:
  - name: flow
    steps:
      - {name: work, method: working}
`)
	if len(issues) > 0 {
		t.Fatal(issues)
	}
	ls, _ := c.CheckLevels("flow")
	lvl := levelOf(t, ls, "flow/work")
	if len(lvl.Steps) != 2 || lvl.Steps[0].Method != MethodVariant || lvl.Steps[0].Name != "by_agent" || !lvl.Steps[0].Exit["c"] {
		t.Fatalf("capability level: %+v", lvl)
	}
	root := levelOf(t, ls, "flow")
	if !root.Steps[0].Composite || root.Steps[0].SubSteps != 2 {
		t.Fatalf("root: %+v", root.Steps)
	}
}
