package engine

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graph"
	"github.com/zimwip/goap/pkg/llm"
	"github.com/zimwip/goap/pkg/methodology"
)

// scopedYAML qualifies every impact of the change, one run per impact (ADR 0109 §3.5).
const scopedYAML = `
name: scoped
version: 1.0.0
namespace: alm
conditions:
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: qualified, scope: impact, expr: 'artifacts.exists(a, a.type == "qualified" && a.data.impact == self.key)'}
  - {name: all_qualified, all: qualified}
actions:
  - {name: qualify, kind: human, scope: impact, description: Qualify the impact, pre: {impacted: true}, effects: {qualified: true}}
processes:
  - name: analysis
    steps:
      - {name: qualification, action: qualify}
`

func TestScopedActionRunsOncePerSubject(t *testing.T) {
	ctx := context.Background()
	e, g, base := setup(t)
	m, err := methodology.Parse([]byte(scopedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	if a, _ := c.Action("analysis/qualification"); !a.IsScoped() || !a.Pre["pending:qualify"] || !a.Effects["all_qualified"] || !a.ScopedEffects["qualified"] {
		t.Fatalf("lifted action %+v", a)
	}
	e.Methodologies.(StaticMethodologies)["scoped"] = c
	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "Q", Intent: "qualify", Methodology: "scoped", Namespace: "alm", ProjectID: testProject, BaselineID: base})
	if err != nil {
		t.Fatal(err)
	}
	var impacts []domain.ChangeImpact
	for _, key := range []string{"REQ-1", "CMP-1"} {
		n, err := g.NodeByKey(ctx, "alm", key)
		if err != nil {
			t.Fatal(err)
		}
		ref := n.Ref()
		impacts = append(impacts, domain.ChangeImpact{Intent: domain.IntentModified, Pre: &ref, Rationale: "in scope"})
	}
	if _, err := g.ProposeImpact(ctx, ch.ID, impacts); err != nil {
		t.Fatal(err)
	}
	p, err := e.Start(ctx, StartRequest{Methodology: "scoped", Goal: "analysis", ChangeID: ch.ID, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 2 {
		p, err = e.Run(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if p.Status != StatusWaiting || p.Pending == nil {
			t.Fatalf("waits for a person to qualify a subject: %s %+v", p.Status, p.Pending)
		}
		subject := p.Steps[p.Pending.Step].Subject
		if subject == "" || seen[subject] {
			t.Fatalf("one run per subject, got %q after %v", subject, seen)
		}
		seen[subject] = true
		if p, err = e.Submit(ctx, p.ID, []ItemInput{{Kind: "artifact", Type: "qualified", Data: map[string]any{"impact": subject}}}); err != nil {
			t.Fatal(err)
		}
	}
	p, err = e.Run(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Status != StatusCompleted || !p.World["all_qualified"] || !seen["REQ-1"] || !seen["CMP-1"] {
		t.Fatalf("completed once every impact is qualified: %s %v %v (%s)", p.Status, p.World, seen, p.Error)
	}
}

// narrowedYAML qualifies the impacts in two passes, the requirements first, the components next: two steps run one
// scoped action, each over its part of the subjects (Step.Where).
const narrowedYAML = `
name: narrowed
version: 1.0.0
namespace: alm
conditions:
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: qualified, scope: impact, expr: 'artifacts.exists(a, a.type == "qualified" && a.data.impact == self.key)'}
  - {name: requirement, scope: impact, expr: '"alm@Requirement" in self.types'}
  - {name: component, scope: impact, expr: '"alm@Component" in self.types'}
  - {name: requirements_qualified, all: qualified, where: self.conditions.requirement, empty: true}
  - {name: components_qualified, all: qualified, where: self.conditions.component, empty: true}
actions:
  - {name: qualify, kind: human, scope: impact, description: Qualify the impact, pre: {impacted: true}, effects: {qualified: true}}
processes:
  - name: analysis
    steps:
      - {name: requirements, action: qualify, where: self.conditions.requirement, done: {requirements_qualified: true}}
      - {name: components, action: qualify, where: self.conditions.component, pre: {requirements_qualified: true}, done: {components_qualified: true}}
`

func TestStepNarrowsTheSubjectsOfAScopedAction(t *testing.T) {
	m, err := methodology.Parse([]byte(narrowedYAML))
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.Compile()
	if err != nil {
		t.Fatal(err)
	}
	req, _ := c.Action("analysis/requirements")
	cmp, _ := c.Action("analysis/components")
	if !req.Narrowed || !req.Pre["pending:analysis/requirements"] || !req.Effects["requirements_qualified"] || req.Effects["components_qualified"] {
		t.Fatalf("requirements pass %+v", req)
	}
	if !cmp.Pre["pending:analysis/components"] || !cmp.Effects["components_qualified"] || cmp.Effects["requirements_qualified"] {
		t.Fatalf("components pass %+v", cmp)
	}
	ctx := context.Background()
	e, g, base := setup(t)
	e.Methodologies.(StaticMethodologies)["narrowed"] = c
	ch, err := g.CreateChange(ctx, graph.NewChange{Title: "Q", Intent: "qualify", Methodology: "narrowed", Namespace: "alm", ProjectID: testProject, BaselineID: base})
	if err != nil {
		t.Fatal(err)
	}
	var impacts []domain.ChangeImpact
	for _, key := range []string{"CMP-1", "REQ-1"} {
		n, err := g.NodeByKey(ctx, "alm", key)
		if err != nil {
			t.Fatal(err)
		}
		ref := n.Ref()
		impacts = append(impacts, domain.ChangeImpact{Intent: domain.IntentModified, Pre: &ref, Rationale: "in scope"})
	}
	if _, err := g.ProposeImpact(ctx, ch.ID, impacts); err != nil {
		t.Fatal(err)
	}
	p, err := e.Start(ctx, StartRequest{Methodology: "narrowed", Goal: "analysis", ChangeID: ch.ID, ProjectID: testProject})
	if err != nil {
		t.Fatal(err)
	}
	var passes []string
	for range 2 {
		if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusWaiting || p.Pending == nil {
			t.Fatalf("waits for a person to qualify a subject: %v %s %+v", err, p.Status, p.Pending)
		}
		s := p.Steps[p.Pending.Step]
		passes = append(passes, s.Action+" "+s.Subject)
		if p, err = e.Submit(ctx, p.ID, []ItemInput{{Kind: "artifact", Type: "qualified", Data: map[string]any{"impact": s.Subject}}}); err != nil {
			t.Fatal(err)
		}
	}
	// the requirement first, whatever the order of the impacts
	if passes[0] != "analysis/requirements REQ-1" || passes[1] != "analysis/components CMP-1" {
		t.Fatalf("passes %v", passes)
	}
	if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
		t.Fatalf("analysis: %v %s %s", err, p.Status, p.Error)
	}
}

// parallelYAML qualifies every impact with a model, several impacts at once.
const parallelYAML = `
name: parallel
version: 1.0.0
namespace: alm
conditions:
  - {name: impacted, expr: 'changeImpacts.exists(i, i.intent == "modified")'}
  - {name: qualified, scope: impact, expr: 'artifacts.exists(a, a.type == "qualified" && a.data.impact == self.key)'}
  - {name: all_qualified, all: qualified}
actions:
  - name: qualify
    kind: llm
    scope: impact
    parallel: %d
    description: Qualify the impact
    pre: {impacted: true}
    effects: {qualified: true}
    prompt: 'Qualify impact {{ .Subject.key }}.'
processes:
  - name: analysis
    steps:
      - {name: qualification, action: qualify}
`

// TestScopedActionRunsSubjectsInParallel checks that the model calls of a scoped action for its subjects are in flight
// at once (the model answers only once every subject called it), and one after the other with parallel: 1.
func TestScopedActionRunsSubjectsInParallel(t *testing.T) {
	keys := []string{"REQ-1", "CMP-1", "TST-1"}
	for _, parallel := range []int{0, 1} {
		t.Run(fmt.Sprint("parallel=", parallel), func(t *testing.T) {
			ctx := context.Background()
			e, g, base := setup(t)
			m, err := methodology.Parse([]byte(fmt.Sprintf(parallelYAML, parallel)))
			if err != nil {
				t.Fatal(err)
			}
			c, err := m.Compile()
			if err != nil {
				t.Fatal(err)
			}
			e.Methodologies.(StaticMethodologies)["parallel"] = c
			var mu sync.Mutex
			inFlight, most := 0, 0
			// all reports whether every subject called the model while the others were still waiting for their answer
			all := func() bool {
				mu.Lock()
				defer mu.Unlock()
				return most == len(keys)
			}
			subject := regexp.MustCompile(`Qualify impact (\S+)\.`)
			e.Executors[methodology.KindLLM] = LLMExecutor{Client: llm.ClientFunc(func(_ context.Context, req llm.Request) (llm.Response, error) {
				mu.Lock()
				inFlight++
				most = max(most, inFlight)
				mu.Unlock()
				// hold the answer until every subject called, or give up (the runs are then one after the other)
				for deadline := time.Now().Add(2 * time.Second); parallel != 1 && !all() && time.Now().Before(deadline); {
					time.Sleep(5 * time.Millisecond)
				}
				mu.Lock()
				inFlight--
				mu.Unlock()
				var key string
				for _, msg := range req.Messages {
					if m := subject.FindStringSubmatch(msg.Content); m != nil {
						key = m[1]
					}
				}
				out := `{"items":[{"kind":"artifact","type":"qualified","data":{"impact":"` + key + `"}}]}`
				return llm.Response{Text: out, Provider: "test", Model: req.Model}, nil
			})}
			ch, err := g.CreateChange(ctx, graph.NewChange{Title: "Q", Intent: "qualify", Methodology: "parallel", Namespace: "alm", ProjectID: testProject, BaselineID: base})
			if err != nil {
				t.Fatal(err)
			}
			var impacts []domain.ChangeImpact
			for _, key := range keys {
				n, err := g.NodeByKey(ctx, "alm", key)
				if err != nil {
					t.Fatal(err)
				}
				ref := n.Ref()
				impacts = append(impacts, domain.ChangeImpact{Intent: domain.IntentModified, Pre: &ref, Rationale: "in scope"})
			}
			if _, err := g.ProposeImpact(ctx, ch.ID, impacts); err != nil {
				t.Fatal(err)
			}
			p, err := e.Start(ctx, StartRequest{Methodology: "parallel", Goal: "analysis", ChangeID: ch.ID, ProjectID: testProject})
			if err != nil {
				t.Fatal(err)
			}
			if p, err = e.Run(ctx, p.ID); err != nil || p.Status != StatusCompleted {
				t.Fatalf("analysis: %v %s %s", err, p.Status, p.Error)
			}
			want := len(keys)
			if parallel == 1 {
				want = 1
			}
			if most != want {
				t.Fatalf("%d model calls in flight at most, want %d", most, want)
			}
			seen := map[string]bool{}
			for _, s := range p.Steps {
				if s.Subject == "" || seen[s.Subject] || !s.EffectsMet {
					t.Fatalf("one successful run per subject: %+v", p.Steps)
				}
				seen[s.Subject] = true
			}
			if len(seen) != len(keys) {
				t.Fatalf("runs for %v, want %v", seen, keys)
			}
		})
	}
}
