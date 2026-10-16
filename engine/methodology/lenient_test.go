package methodology

import (
	"testing"
)

// A draft with broken rules still compiles as far as it stands: the issues come tied to the step they are about, and
// the graph and levels of what is left can be drawn.
func TestCompileLenientKeepsWhatStands(t *testing.T) {
	m, err := Parse([]byte(processBase + `
methods:
  - {name: by_worker, for: working, actions: [do_c], done: {c: true}}
  - {name: empty, for: working}
processes:
  - name: flow
    steps:
      - {name: first, action: do_a}
      - {name: broken, action: nope}
      - name: phase
        steps:
          - {name: deep, action: do_b}
          - {name: worse, method: missing}
      - {name: work, method: working, foreach: '1 + 1'}
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, strict := m.CompileWith(CompileOptions{}); len(strict) == 0 {
		t.Fatal("the draft is broken")
	}
	c, issues := m.CompileLenient()
	if c == nil || len(issues) == 0 {
		t.Fatalf("compiled %v, issues %v", c != nil, issues)
	}
	byActivity := map[string]string{}
	for _, is := range issues {
		byActivity[is.Activity] = is.Message
	}
	for _, want := range []string{"flow/broken", "flow/phase/worse", "flow/work", "empty"} {
		if _, ok := byActivity[want]; !ok {
			t.Errorf("no issue tied to %s: %v", want, issues)
		}
	}
	// what stands is drawn
	g, ok := c.ProcessGraph("flow")
	if !ok || len(g.Steps) == 0 {
		t.Fatalf("graph %+v %v", g, ok)
	}
	if _, ok := c.CheckLevels("flow"); !ok {
		t.Fatal("levels")
	}
	for _, name := range []string{"by_worker", "empty"} {
		c.ProcessGraph(name)
		c.CheckLevels(name)
	}
}

func TestActivityOf(t *testing.T) {
	m := &Methodology{
		Processes: []Process{{Name: "flow", Steps: []Step{{Name: "a"}, {Name: "b", Steps: []Step{{Name: "x"}, {Name: "y"}}}}}},
		Methods:   []Method{{Name: "meth", Steps: []Step{{Name: "s"}}}},
	}
	for path, want := range map[string]string{
		"processes[0].steps[1].steps[1].pre": "flow/b/y",
		"processes[0].steps[0]":              "flow/a",
		"processes[0].name":                  "flow",
		"methods[0].steps[0].action":         "meth/s",
		"methods[0].actions[2]":              "meth",
		"processes[4].steps[0]":              "",
		"agents[0].name":                     "",
	} {
		if got := m.ActivityOf(path); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
