package methodology

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/def"
)

// lifecycles of the tests: a release (planning → integrating → delivered) and a piece of work (open → review → done).
var (
	releaseLC = &domain.Lifecycle{Name: "delivery", Initial: "planning",
		States: []domain.LifecycleState{{Name: "planning"}, {Name: "integrating"}, {Name: "delivered", Final: true}},
		Transitions: []domain.Transition{{Name: "integrate", From: "planning", To: "integrating"},
			{Name: "deliver", From: "integrating", To: "delivered"}}}
	workLC = &domain.Lifecycle{Name: "work", Initial: "open",
		States: []domain.LifecycleState{{Name: "open"}, {Name: "review"}, {Name: "done", Final: true}},
		Transitions: []domain.Transition{{Name: "submit", From: "open", To: "review"},
			{Name: "approve", From: "review", To: "done"}}}
)

func composition(others ...*Methodology) Composition {
	byName := map[string]*Methodology{}
	for _, o := range others {
		byName[o.Name] = o
	}
	return Composition{Others: byName, Lifecycle: func(m *Methodology) *domain.Lifecycle {
		switch m.Lifecycle {
		case "delivery":
			return releaseLC
		case "work":
			return workLC
		}
		return nil
	}}
}

func work(lifecycle string) *Methodology {
	return &Methodology{Name: "work", Lifecycle: lifecycle,
		Processes: []Process{{Name: "build", Steps: []Step{{Name: "code"}, {Name: "test"}}}}}
}

func release(mut func(*Methodology)) *Methodology {
	m := &Methodology{Name: "release", Lifecycle: "delivery",
		Processes:  []Process{{Name: "release", Steps: []Step{{Name: "plan"}}}},
		Integrates: []Integration{{Methodologies: []string{"work"}, Exclusive: true}},
		Guardian:   &Guardian{Merge: `integrator.state == "integrating"`}}
	if mut != nil {
		mut(m)
	}
	return m
}

// issueAt is the issue at path whose message holds every part ("" fails).
func issueAt(is def.Issues, path string, parts ...string) *def.Issue {
	for i, x := range is {
		if x.Path != path {
			continue
		}
		ok := true
		for _, p := range parts {
			ok = ok && strings.Contains(x.Message+" | "+x.Hint, p)
		}
		if ok {
			return &is[i]
		}
	}
	return nil
}

func TestCompositionSound(t *testing.T) {
	r := release(func(m *Methodology) {
		m.On = []Subscription{{Event: "change.transitioned", From: FromIntegrated, Filter: `event.change.state == "review"`},
			{Event: "step.completed", From: FromIntegrated, Filter: `event.step.name in ["code", "test"]`}}
		m.Guardian.Transitions = map[string]string{"approve": `!integrator.attached || integrator.state != "delivered"`}
	})
	if is := r.CompositionIssues(composition(work("work"))); len(is) != 0 {
		t.Fatalf("a sound composition: %v", is)
	}
	if is := work("work").CompositionIssues(composition(r)); len(is) != 0 {
		t.Fatalf("seen from the integrated one: %v", is)
	}
}

func TestCompositionMissingEvents(t *testing.T) {
	r := release(func(m *Methodology) {
		m.On = []Subscription{{Event: "change.transitioned", From: FromIntegrated}}
	})
	is := r.CompositionIssues(composition(work("")))
	i := issueAt(is, "on[0].event", "never comes from", "work names no lifecycle", "give work a lifecycle")
	if i == nil || !i.IsWarning() {
		t.Fatalf("change.transitioned of a methodology without lifecycle: %v", is)
	}
	// reported on the integrated one too, as what this version breaks
	if is := work("").CompositionIssues(composition(r)); issueAt(is, "lifecycle", "release follows change.transitioned") == nil {
		t.Fatalf("on the source: %v", is)
	}

	// from: integrator, while nothing integrates the methodology
	w := work("work")
	w.On = []Subscription{{Event: "change.transitioned", From: FromIntegrator}}
	if is := w.CompositionIssues(composition()); issueAt(is, "on[0].from", "no methodology integrates work") == nil {
		t.Fatalf("from integrator: %v", is)
	}
	if is := w.CompositionIssues(composition(release(nil))); len(is) != 0 {
		t.Fatalf("integrated by release: %v", is)
	}

	// a filter naming a state, a step that do not exist
	r = release(func(m *Methodology) {
		m.On = []Subscription{{Event: "change.transitioned", From: FromIntegrated, Filter: `event.change.state == "reveiw"`},
			{Event: "step.completed", From: FromIntegrated, Filter: `"deploy" == event.step.name`}}
	})
	is = r.CompositionIssues(composition(work("work")))
	if issueAt(is, "on[0].filter", `"reveiw"`, `did you mean "review"`) == nil || issueAt(is, "on[1].filter", `"deploy"`, "steps: code, test") == nil {
		t.Fatalf("filters: %v", is)
	}

	// an integration naming an unknown methodology
	r = release(func(m *Methodology) { m.Integrates[0].Methodologies = []string{"wrok"} })
	if is := r.CompositionIssues(composition(work("work"))); issueAt(is, "integrates[0].methodologies[0]", `did you mean "work"`) == nil {
		t.Fatalf("unknown integrated: %v", is)
	}
}

func TestCompositionGuardian(t *testing.T) {
	// a state the integrator's lifecycle does not have; a transition the integrated lifecycle does not have
	r := release(func(m *Methodology) {
		m.Guardian.Merge = `integrator.state == "integrate"`
		m.Guardian.Transitions = map[string]string{"aprove": `integrator.attached`}
	})
	is := r.CompositionIssues(composition(work("work")))
	if issueAt(is, "guardian.merge", `integrator.state "integrate"`, `did you mean "integrating"`) == nil {
		t.Fatalf("unknown integrator state: %v", is)
	}
	if issueAt(is, "guardian.transitions.aprove", "no transition aprove", `did you mean "approve"`) == nil {
		t.Fatalf("unknown transition: %v", is)
	}
	if issueAt(is, "guardian.merge", "never holds") == nil {
		t.Fatalf("a merge guard on a state that does not exist never holds: %v", is)
	}

	// an exclusive integration contradicted by its merge guard
	r = release(func(m *Methodology) { m.Guardian.Merge = `!integrator.attached` })
	if is := r.CompositionIssues(composition(work("work"))); issueAt(is, "guardian.merge", "never holds", "exclusive integration") == nil {
		t.Fatalf("exclusive and unattached: %v", is)
	}

	// a transition guard on a state the change cannot be in when it takes the transition
	r = release(func(m *Methodology) { m.Guardian.Transitions = map[string]string{"approve": `change.state == "open"`} })
	if is := r.CompositionIssues(composition(work("work"))); issueAt(is, "guardian.transitions.approve", "never holds", "approve leaves review") == nil {
		t.Fatalf("the from state: %v", is)
	}

	// a world condition the integrated methodology does not hold
	r = release(func(m *Methodology) { m.Guardian.Merge = `integrator.state == "integrating" && world.reviewed` })
	if is := r.CompositionIssues(composition(work("work"))); issueAt(is, "guardian.merge", "world.reviewed") == nil {
		t.Fatalf("unknown condition: %v", is)
	}
}

// Two guardians of a chain, each satisfiable alone, that never hold together on the same change.
func TestCompositionCompositeGuardian(t *testing.T) {
	r := release(func(m *Methodology) {
		m.Guardian.Transitions = map[string]string{"approve": `change.state == "review"`}
	})
	portfolio := &Methodology{Name: "portfolio", Lifecycle: "delivery",
		Processes:  []Process{{Name: "portfolio", Steps: []Step{{Name: "steer"}}}},
		Integrates: []Integration{{Methodologies: []string{"release"}}},
		Guardian:   &Guardian{Transitions: map[string]string{"approve": `change.state == "open"`}}}
	// portfolio frames the work changes through release: its guard on approve (which leaves review) never holds
	if is := portfolio.CompositionIssues(composition(r, work("work"))); issueAt(is, "guardian.transitions.approve", "never holds", "work") == nil {
		t.Fatalf("portfolio's own guard: %v", is)
	}

	// approve leaves open or review: each guard holds alone, never together
	wlc := *workLC
	wlc.Transitions = append(slices.Clone(workLC.Transitions), domain.Transition{Name: "approve", From: "open", To: "done"})
	with := func(others ...*Methodology) Composition {
		c := composition(others...)
		c.Lifecycle = func(m *Methodology) *domain.Lifecycle {
			if m.Lifecycle == "work" {
				return &wlc
			}
			return composition().Lifecycle(m)
		}
		return c
	}
	conflict := "never hold together on the transition approve of a change of work"
	if is := r.CompositionIssues(with(portfolio, work("work"))); issueAt(is, "guardian.transitions.approve", conflict, `portfolio requires change.state == "open"`) == nil {
		t.Fatalf("on release: %v", is)
	}
	if is := portfolio.CompositionIssues(with(r, work("work"))); issueAt(is, "guardian.transitions.approve", conflict) == nil {
		t.Fatalf("on portfolio: %v", is)
	}
	if is := work("work").CompositionIssues(with(r, portfolio)); issueAt(is, "", conflict) == nil {
		t.Fatalf("on work, the guardians above it: %v", is)
	}
	// once aligned, nothing
	portfolio.Guardian.Transitions["approve"] = `change.state == "review" || !integrator.attached`
	if is := r.CompositionIssues(with(portfolio, work("work"))); len(is) != 0 {
		t.Fatalf("aligned: %v", is)
	}
}

func TestLiteralRefs(t *testing.T) {
	refs := literalRefs(`integrator.state == "a" && "b" != change.state && event.step.name in ["x", 'y'] && world.ok && world["k"]`)
	for sel, want := range map[string]string{"integrator.state": "a", "change.state": "b", "event.step.name": "x,y", "world": "ok,k"} {
		if got := strings.Join(refs[sel], ","); got != want {
			t.Errorf("%s: %q, want %q", sel, got, want)
		}
	}
	if refs := literalRefs(`event.change.state == "s"`); len(refs["change.state"]) != 0 || refs["event.change.state"][0] != "s" {
		t.Errorf("event.change is not change: %v", refs)
	}
}

// The methodologies of the repository, against the domain of domains/: their composition holds, but for the
// lifecycle sdlc does not have yet (release-based follows its change.transitioned).
func TestCompositionOfTheRepository(t *testing.T) {
	ds, err := def.LoadDomains("../../domains")
	if err != nil {
		t.Fatal(err)
	}
	others := map[string]*Methodology{}
	for _, f := range []string{"sdlc", "release-based", "risk-management", "methodology-improvement"} {
		m, err := LoadFile("../../methodologies/" + f + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		others[m.Name] = m
	}
	c := Composition{Others: others, Lifecycle: func(m *Methodology) *domain.Lifecycle {
		for _, d := range ds {
			if lc := d.Lifecycle(m.Lifecycle); lc != nil {
				return lc
			}
		}
		return nil
	}}
	var got []string
	for _, n := range slices.Sorted(maps.Keys(others)) {
		for _, i := range others[n].CompositionIssues(c) {
			got = append(got, n+": "+i.Path)
		}
	}
	if want := []string{"release-based: on[1].event", "sdlc: lifecycle"}; !slices.Equal(got, want) {
		t.Fatalf("composition of the repository: %v, want %v", got, want)
	}
}

// the events of the changes a change depends on (ADR 0108) come from changes of any methodology: nothing to check.
func TestCompositionFromDependency(t *testing.T) {
	m := work("work")
	m.On = []Subscription{{Event: "change.applied", From: FromDependency}, {Event: "change.transitioned", From: FromDependency}}
	if is := m.CompositionIssues(composition(release(nil))); len(is) != 0 {
		t.Fatalf("from: dependency is not checked: %v", is)
	}
	if is := release(nil).CompositionIssues(composition(m)); len(is) != 0 {
		t.Fatalf("no consumer of the release: %v", is)
	}
}

// TestCompositionDeclaredEvents: an event of a methodology (ADR 0110) followed from another one must be declared by the
// source; a reactive activity's subscriptions are checked like the methodology's own.
func TestCompositionDeclaredEvents(t *testing.T) {
	follow := func(at func(m *Methodology)) *Methodology {
		return release(func(m *Methodology) {
			m.Events = []Event{{Name: "work.tested"}}
			at(m)
		})
	}
	viaOn := follow(func(m *Methodology) { m.On = []Subscription{{Event: "work.tested", From: FromIntegrated}} })
	viaStep := follow(func(m *Methodology) {
		m.Processes[0].Steps[0].On = []Subscription{{Event: "work.tested", From: FromIntegrated}}
	})
	for path, m := range map[string]*Methodology{"on[0].event": viaOn, "processes[0].steps[0].on[0].event": viaStep} {
		if issueAt(m.CompositionIssues(composition(work("work"))), path, "work declares no event work.tested") == nil {
			t.Errorf("%s: the source declares no such event", path)
		}
		declared := work("work")
		declared.Events = []Event{{Name: "work.tested"}}
		if is := m.CompositionIssues(composition(declared)); issueAt(is, path) != nil {
			t.Errorf("%s: declared by the source, got %v", path, is)
		}
	}
}
