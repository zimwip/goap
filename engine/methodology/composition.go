package methodology

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/lib/def"
	"github.com/zimwip/goap/lib/guard"
)

// The check of the composition of methodologies (ADR 0106): a methodology alone compiles, but what it expects of the
// methodologies it is composed with (the events it follows through `on.from`, the states and transitions its guardian
// names, the guards the chain of guardians adds up) holds only against their definitions. CompositionIssues reads
// them and reports, as warnings with a hint to fix them (the others change on their own: never an error):
//   - an event a subscription waits for that its source cannot emit (change.transitioned of a methodology without a
//     lifecycle, events from an integrator when nothing integrates the methodology...);
//   - a state, transition, process, step or methodology a filter or a guard names that its source does not declare;
//   - a guardian that never holds, alone or with the guardians above it on the same change, found by trying every
//     value of the states and flags it reads (a guard reading anything else is not judged).

// Composition is what the check of a methodology's composition knows of the others.
type Composition struct {
	// Others is the latest version of each other methodology, by name.
	Others map[string]*Methodology
	// Lifecycle resolves the lifecycle the changes of a methodology follow (nil: none, or unknown).
	Lifecycle func(*Methodology) *domain.Lifecycle
}

// maxComposedChain bounds the walk up the integrators.
const maxComposedChain = 8

// maxWorldRefs bounds the conditions of the world a guard may read for its satisfiability to be judged (2^n tries).
const maxWorldRefs = 4

func (c Composition) lifecycle(m *Methodology) *domain.Lifecycle {
	if c.Lifecycle == nil || m == nil || m.Lifecycle == "" {
		return nil
	}
	return c.Lifecycle(m)
}

// CompositionIssues checks m against the methodologies it is composed with (see above). Every issue is a warning
// carrying a Hint.
func (m *Methodology) CompositionIssues(c Composition) def.Issues {
	ck := &compositionCheck{m: m, c: c}
	ck.integrations()
	for _, ps := range m.AllSubscriptions() {
		ck.subscription(ps.Path, ps.Sub)
	}
	ck.guardian()
	ck.consumers()
	ck.composite()
	ck.gates()
	return ck.out
}

// gates: the transitions a gate takes and goes back by are transitions of the lifecycle of m (ADR 0132).
func (ck *compositionCheck) gates() {
	lc := ck.c.lifecycle(ck.m)
	if lc == nil {
		return
	}
	var walk func(path string, steps []Step)
	walk = func(path string, steps []Step) {
		for i, st := range steps {
			p := fmt.Sprintf("%s.steps[%d]", path, i)
			if g := st.Gate; g != nil {
				if !hasTransition(lc, g.Transition) {
					ck.add(p+".gate.transition", suggest(g.Transition, transitionNames(lc), "transitions"),
						"no transition %s in the lifecycle %s: the gate never passes", g.Transition, lc.Name)
				}
				if d := g.Decision; d != nil && d.Back != "" && !hasTransition(lc, d.Back) {
					ck.add(p+".gate.decision.back", suggest(d.Back, transitionNames(lc), "transitions"),
						"no transition %s in the lifecycle %s: the decision cannot send the change back", d.Back, lc.Name)
				}
			}
			walk(p, st.Steps)
		}
	}
	for i, pr := range ck.m.Processes {
		walk(fmt.Sprintf("processes[%d]", i), pr.Steps)
	}
}

type compositionCheck struct {
	m   *Methodology
	c   Composition
	out def.Issues
}

func (ck *compositionCheck) add(path, hint, format string, args ...any) {
	ck.out = append(ck.out, def.Issue{Path: path, Message: fmt.Sprintf(format, args...), Hint: hint, Severity: def.SeverityWarning})
}

func (ck *compositionCheck) other(name string) *Methodology {
	if name == ck.m.Name {
		return ck.m
	}
	return ck.c.Others[name]
}

// integrated are the known methodologies m integrates (the wildcard: unknown, wild true).
func (ck *compositionCheck) integrated() (out []*Methodology, wild bool) {
	for _, in := range ck.m.Integrates {
		for _, n := range in.Methodologies {
			if n == AnyMethodology {
				wild = true
			} else if o := ck.c.Others[n]; o != nil && !slices.Contains(out, o) {
				out = append(out, o)
			}
		}
	}
	return out, wild
}

// integrators are the known methodologies integrating the changes of the methodology named name, by name order.
func (ck *compositionCheck) integrators(name string) []*Methodology {
	var out []*Methodology
	for _, n := range ck.otherNames() {
		if o := ck.c.Others[n]; o.IntegratesChangesOf(name) {
			out = append(out, o)
		}
	}
	if name != ck.m.Name && ck.m.IntegratesChangesOf(name) {
		out = append(out, ck.m)
	}
	return out
}

func (ck *compositionCheck) otherNames() []string {
	names := make([]string, 0, len(ck.c.Others))
	for n := range ck.c.Others {
		if n != ck.m.Name {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// integrations: a methodology integrates names no known methodology.
func (ck *compositionCheck) integrations() {
	known := ck.otherNames()
	for i, in := range ck.m.Integrates {
		for j, n := range in.Methodologies {
			if n == AnyMethodology || n == ck.m.Name || !def.NameRE.MatchString(n) || ck.c.Others[n] != nil {
				continue
			}
			ck.add(fmt.Sprintf("integrates[%d].methodologies[%d]", i, j), suggest(n, known, "known methodologies"),
				"no methodology %s is known: nothing is integrated through this name", n)
		}
	}
}

// --- events -------------------------------------------------------------------------------------------------------

// sources are the methodologies the events of a subscription come from, by its origin; ok false when they cannot be
// known (a wildcard integration).
func (ck *compositionCheck) sources(sub Subscription) (out []*Methodology, ok bool) {
	switch sub.Origin() {
	case FromIntegrated:
		in, wild := ck.integrated()
		return in, !wild
	case FromIntegrator:
		return ck.integrators(ck.m.Name), true
	case FromDependency:
		return nil, false // any change may be depended on (ADR 0108): its methodology is not known here
	default:
		if len(ck.m.AppliesTo) == 0 {
			return []*Methodology{ck.m}, true
		}
		for _, n := range ck.m.AppliesTo {
			if o := ck.other(n); o != nil {
				out = append(out, o)
			}
		}
		return out, len(out) > 0
	}
}

// emits reports whether the changes (or the processes) of src can raise the event; when not, why and how to fix it.
func (ck *compositionCheck) emits(src *Methodology, event string) (bool, string, string) {
	switch event {
	case events.ChangeTransitioned:
		if ck.c.lifecycle(src) == nil {
			return false, fmt.Sprintf("%s names no lifecycle: its changes never transition", src.Name),
				fmt.Sprintf("give %s a lifecycle (lifecycle: <a lifecycle of its domain>), or follow %s instead", src.Name, events.ChangeCommitted)
		}
	case events.StepCompleted, events.ProcessAttached, events.ProcessCompleted, events.ProcessFailed, events.ProcessStuck:
		if len(src.Processes) == 0 && len(src.Methods) == 0 && len(src.Agents) == 0 {
			return false, fmt.Sprintf("%s runs no process on its changes", src.Name),
				fmt.Sprintf("declare a process in %s, or follow a change event (%s, %s)", src.Name, events.ChangeCommitted, events.ChangeTransitioned)
		}
	case events.ChangeAttached, events.ChangeDetached:
		if len(ck.integrators(src.Name)) == 0 {
			return false, fmt.Sprintf("no methodology integrates %s: its changes are never attached", src.Name),
				fmt.Sprintf("list %s in the integrates of the methodology that frames its changes", src.Name)
		}
	default:
		if !StandardEvent(event) && !src.Declares(event) {
			return false, fmt.Sprintf("%s declares no event %s", src.Name, event),
				fmt.Sprintf("declare %s in the events of %s and emit it (an action's emits), or follow another event", event, src.Name)
		}
	}
	return true, "", ""
}

// subscription checks one `on` entry of m: its source can emit its event, and the names its filter compares exist.
func (ck *compositionCheck) subscription(path string, sub Subscription) {
	if !ck.m.KnowsEvent(sub.Event) {
		return // the compiler reports it
	}
	if sub.Event == events.MethodologyPublished && sub.Origin() != FromSelf {
		ck.add(path+".from", "follow methodology.published with from: self (a trigger), or follow a change event",
			"%s is about no change: it never comes from an %s change", sub.Event, sub.Origin())
		return
	}
	srcs, ok := ck.sources(sub)
	if !ok {
		return
	}
	if len(srcs) == 0 {
		if sub.Origin() == FromIntegrator {
			ck.add(path+".from", fmt.Sprintf("list %s in the integrates of the methodology that should frame its changes, or follow from: self", ck.m.Name),
				"no methodology integrates %s: no event ever comes from an integrator", ck.m.Name)
		}
		return
	}
	var whys, hints []string
	emitting := 0
	for _, s := range srcs {
		if ok, why, hint := ck.emits(s, sub.Event); ok {
			emitting++
		} else {
			whys, hints = append(whys, why), append(hints, hint)
		}
	}
	if emitting == 0 {
		ck.add(path+".event", strings.Join(hints, "; "), "%s never comes from %s: %s", sub.Event, sourceLabel(sub, srcs), strings.Join(whys, "; "))
		return
	}
	for _, f := range ck.filterFindings(sub, srcs) {
		ck.add(path+".filter", f.hint, "%s", f.msg)
	}
}

func sourceLabel(sub Subscription, srcs []*Methodology) string {
	names := make([]string, len(srcs))
	for i, s := range srcs {
		names[i] = s.Name
	}
	switch sub.Origin() {
	case FromIntegrated:
		return "the integrated changes (" + strings.Join(names, ", ") + ")"
	case FromIntegrator:
		return "the integrators (" + strings.Join(names, ", ") + ")"
	}
	return strings.Join(names, ", ")
}

type finding struct{ msg, hint string }

// filterFindings are the values a filter compares that no source can produce: a state, a process, a step, a step
// path, a methodology, an event type.
func (ck *compositionCheck) filterFindings(sub Subscription, srcs []*Methodology) []finding {
	refs := literalRefs(sub.Filter)
	var out []finding
	names, states, procs, steps, paths := []string{}, []string{}, []string{}, []string{}, []string{}
	for _, s := range srcs {
		names = append(names, s.Name)
		if lc := ck.c.lifecycle(s); lc != nil {
			states = append(states, stateNames(lc)...)
		}
		p, st, pa := activityNames(s)
		procs, steps, paths = append(procs, p...), append(steps, st...), append(paths, pa...)
	}
	check := func(sel, what string, known []string) {
		for _, v := range refs[sel] {
			if v == "" || slices.Contains(known, v) {
				continue
			}
			out = append(out, finding{fmt.Sprintf("%s %q: no %s of %s", sel, v, what, sourceLabel(sub, srcs)), suggest(v, known, what+"s")})
		}
	}
	check("event.change.methodology", "methodology", names)
	check("event.process.methodology", "methodology", names)
	if len(states) > 0 {
		check("event.change.state", "state", states)
	}
	check("event.step.process", "process", procs)
	check("event.process.agent", "process", procs)
	check("event.step.name", "step", steps)
	check("event.step.path", "step path", paths)
	for _, v := range refs["event.type"] {
		if v != sub.Event {
			out = append(out, finding{fmt.Sprintf("event.type %q: the subscription only receives %s", v, sub.Event), "compare event.type with " + sub.Event + ", or subscribe to " + v})
		}
	}
	return out
}

// consumers: the other methodologies following the events of m's changes or of its integrators, against what this
// version of m can emit (a lifecycle removed, a step renamed...).
func (ck *compositionCheck) consumers() {
	for _, n := range ck.otherNames() {
		o := ck.c.Others[n]
		for _, ps := range o.AllSubscriptions() {
			sub := ps.Sub
			var follows bool
			switch sub.Origin() {
			case FromIntegrated:
				follows = slices.Contains(integratedNames(o), ck.m.Name)
			case FromIntegrator:
				follows = ck.m.IntegratesChangesOf(o.Name) && !slices.Contains(integratedNames(ck.m), AnyMethodology)
			case FromDependency:
				follows = false // the changes depended on are not known by methodology (ADR 0108)
			default:
				follows = slices.Contains(o.AppliesTo, ck.m.Name)
			}
			if !follows || !o.KnowsEvent(sub.Event) {
				continue
			}
			if ok, why, hint := ck.emits(ck.m, sub.Event); !ok {
				path := ""
				if sub.Event == events.ChangeTransitioned {
					path = "lifecycle"
				}
				ck.add(path, hint, "%s follows %s %s: %s", o.Name, sub.Event, originOf(sub, ck.m.Name), why)
				continue
			}
			for _, f := range (&compositionCheck{m: o, c: ck.c}).filterFindings(sub, []*Methodology{ck.m}) {
				ck.add("", f.hint, "%s follows %s %s, filtered on %s", o.Name, sub.Event, originOf(sub, ck.m.Name), f.msg)
			}
		}
	}
}

func originOf(sub Subscription, name string) string {
	switch sub.Origin() {
	case FromIntegrated:
		return "from the " + name + " changes it integrates"
	case FromIntegrator:
		return "from the " + name + " changes integrating its own"
	}
	return "of the " + name + " changes"
}

// integratedNames are the names m lists in its integrations, the wildcard included.
func integratedNames(m *Methodology) []string {
	var out []string
	for _, in := range m.Integrates {
		out = append(out, in.Methodologies...)
	}
	return out
}

// --- guardian -----------------------------------------------------------------------------------------------------

// guardian checks the guardian of m against the lifecycles of the changes it integrates and its own.
func (ck *compositionCheck) guardian() {
	g := ck.m.Guardian
	if g == nil && !ck.exclusive() {
		return
	}
	framed, wild := ck.below()
	var in []*Methodology
	for _, b := range framed {
		in = append(in, b.m)
	}
	own := ck.c.lifecycle(ck.m)
	if g != nil {
		if strings.TrimSpace(g.Commit) != "" {
			ck.guardRefs("guardian.commit", g.Commit, own, in, wild)
		}
		for _, t := range slices.Sorted(maps.Keys(g.Transitions)) {
			path := "guardian.transitions." + t
			ck.guardRefs(path, g.Transitions[t], own, in, wild)
			if wild {
				continue
			}
			var all []string
			withLC := 0
			for _, x := range in {
				if lc := ck.c.lifecycle(x); lc != nil {
					withLC++
					all = append(all, transitionNames(lc)...)
				}
			}
			switch {
			case len(in) == 0:
			case withLC == 0:
				ck.add(path, "name a transition of their lifecycle, or move the guard to guardian.commit",
					"the changes %s integrates (%s) follow no lifecycle: this guard is never asked", ck.m.Name, names(in))
			case !slices.Contains(all, t):
				ck.add(path, suggest(t, all, "transitions"), "no transition %s in the lifecycle of %s: this guard is never asked", t, names(in))
			}
		}
	}
	if wild {
		return
	}
	for _, b := range framed {
		x := b.m
		if expr := ck.m.CommitGuard(b.via(x)); expr != "" {
			if res := ck.satisfiable(expr, x, ""); res == unsat {
				ck.add("guardian.commit", commitHint(ck.m, b.via(x)), "the commit guard %s never holds for a change of %s: it can never commit", expr, x.Name)
			}
		}
		if g == nil {
			continue
		}
		for _, t := range slices.Sorted(maps.Keys(g.Transitions)) {
			if !hasTransition(ck.c.lifecycle(x), t) {
				continue
			}
			if res := ck.satisfiable(g.Transitions[t], x, t); res == unsat {
				ck.add("guardian.transitions."+t, fmt.Sprintf("the transition %s leaves %s: test the state the change is in when it takes it", t, strings.Join(fromStates(ck.c.lifecycle(x), t), ", ")),
					"the guard of %s never holds for a change of %s: the transition is never taken", t, x.Name)
			}
		}
	}
}

func (ck *compositionCheck) exclusive() bool {
	for _, in := range ck.m.Integrates {
		if in.Exclusive {
			return true
		}
	}
	return false
}

func commitHint(m *Methodology, via string) string {
	if in := m.IntegrationOf(via); in != nil && in.Exclusive {
		return "an exclusive integration already requires integrator.attached: the commit guard must hold for an attached change, in a state of " + m.Name
	}
	return "relax the commit guard: some state of " + m.Name + " and of the changes it integrates must satisfy it"
}

// framed is a methodology whose changes the guardian of m frames: one m integrates, or one integrated by those, down
// the integrations (each guardian of a change's chain is asked on its moves and its commits, ADR 0106, 0146).
type framed struct {
	m *Methodology
	// between are the guardians from its integrator up to m, m left out, innermost first.
	between []*Methodology
}

// via is the methodology m integrates on the way down to x (x itself when m integrates it).
func (f framed) via(x *Methodology) string {
	if len(f.between) == 0 {
		return x.Name
	}
	return f.between[len(f.between)-1].Name
}

// below are the methodologies m's guardian frames, nearest first; wild when m integrates every methodology.
func (ck *compositionCheck) below() (out []framed, wild bool) {
	direct, wild := ck.integrated()
	if wild {
		return nil, true
	}
	seen := map[string]bool{ck.m.Name: true}
	queue := []framed{}
	for _, x := range direct {
		seen[x.Name] = true
		queue = append(queue, framed{m: x})
	}
	for len(queue) > 0 {
		f := queue[0]
		queue = queue[1:]
		out = append(out, f)
		if len(f.between) >= maxComposedChain {
			continue
		}
		for _, in := range f.m.Integrates {
			for _, n := range in.Methodologies {
				o := ck.c.Others[n]
				if o == nil || seen[n] {
					continue
				}
				seen[n] = true
				queue = append(queue, framed{m: o, between: append([]*Methodology{f.m}, f.between...)})
			}
		}
	}
	return out, false
}

// guardRefs checks the names a guard compares: integrator.state against m's lifecycle, change.state against the
// lifecycles of the integrated changes, integrator.methodology against m, world.<condition> against their conditions.
func (ck *compositionCheck) guardRefs(path, expr string, own *domain.Lifecycle, in []*Methodology, wild bool) {
	refs := literalRefs(expr)
	if vs := refs["integrator.state"]; len(vs) > 0 {
		if own == nil {
			ck.add(path, fmt.Sprintf("give %s a lifecycle (lifecycle: <a lifecycle of its domain>), or test integrator.attached", ck.m.Name),
				"%s names no lifecycle: integrator.state is always empty", ck.m.Name)
		} else {
			for _, v := range vs {
				if v != "" && !hasState(own, v) {
					ck.add(path, suggest(v, stateNames(own), "states of "+own.Name), "integrator.state %q: no state of the lifecycle %s of %s", v, own.Name, ck.m.Name)
				}
			}
		}
	}
	for _, v := range refs["integrator.methodology"] {
		if v != ck.m.Name {
			ck.add(path, fmt.Sprintf("the integrator a guardian sees is always a change of %s", ck.m.Name),
				"integrator.methodology %q: the guardian of %s only sees %s changes", v, ck.m.Name, ck.m.Name)
		}
	}
	if wild || len(in) == 0 {
		return
	}
	var states []string
	for _, x := range in {
		if lc := ck.c.lifecycle(x); lc != nil {
			states = append(states, stateNames(lc)...)
		}
	}
	for _, v := range refs["change.state"] {
		if v == "" || slices.Contains(states, v) {
			continue
		}
		if len(states) == 0 {
			ck.add(path, "test change.status, or give them a lifecycle", "change.state %q: the changes %s integrates (%s) follow no lifecycle", v, ck.m.Name, names(in))
		} else {
			ck.add(path, suggest(v, states, "states"), "change.state %q: no state of the lifecycle of %s", v, names(in))
		}
	}
	var conds []string
	for _, x := range in {
		conds = append(conds, conditionNames(x)...)
	}
	for _, v := range refs["world"] {
		if !slices.Contains(conds, v) {
			ck.add(path, suggest(v, conds, "conditions"), "world.%s: no condition %s in %s, the guard reads it as unknown", v, v, names(in))
		}
	}
}

// --- satisfiability -----------------------------------------------------------------------------------------------

type satisfiability int

const (
	unknown satisfiability = iota
	sat
	unsat
)

// level is one guardian of a chain over a change: the guard it adds and the methodology its integrator is a change of.
type level struct {
	expr     string
	guardian *Methodology
}

// satisfiable tries the guard of m's guardian over a change of x (transition "" : a commit) for every state x may
// be in, every state of m's lifecycle its integrator may be in, attached or not, and the conditions of the world it
// reads.
func (ck *compositionCheck) satisfiable(expr string, x *Methodology, transition string) satisfiability {
	return ck.together([]level{{expr: expr, guardian: ck.m}}, x, transition)
}

// together reports whether the guards of a chain of guardians hold together on a change of x for some assignment:
// the change (its state, the world) is shared, each guardian sees its own integrator. Unknown when a guard reads
// anything else, or the world is too large to try.
func (ck *compositionCheck) together(levels []level, x *Methodology, transition string) satisfiability {
	guards := make([]*guard.Guard, len(levels))
	var world []string
	for i, l := range levels {
		g, err := guard.Compile(l.expr)
		if err != nil {
			return unknown
		}
		guards[i] = g
		for _, w := range literalRefs(l.expr)["world"] {
			if !slices.Contains(world, w) {
				world = append(world, w)
			}
		}
		if len(world) > maxWorldRefs {
			return unknown
		}
	}
	states := []string{""}
	if lc := ck.c.lifecycle(x); lc != nil {
		states = stateNames(lc)
		if transition != "" {
			states = fromStates(lc, transition)
		}
	}
	decided := true
	for _, st := range states {
		for bits := 0; bits < 1<<len(world); bits++ {
			w := map[string]bool{}
			for i, n := range world {
				w[n] = bits&(1<<i) != 0
			}
			change := map[string]any{"id": "c", "title": "", "intent": "", "status": string(domain.ChangeActive), "methodology": x.Name,
				"state": st, "transition": transition, "decision": "", "goal": "", "parent": "p", "project": "", "data": map[string]any{}}
			all := true
			for i, l := range levels {
				r := ck.someIntegrator(guards[i], l.guardian, change, w)
				if r == unknown {
					decided = false
				}
				if r != sat {
					all = false
					break
				}
			}
			if all {
				return sat
			}
		}
	}
	if !decided {
		return unknown
	}
	return unsat
}

// someIntegrator reports whether the guard holds for some integrator the guardian's change may be: none, or a change
// of the guardian in any state of its lifecycle.
func (ck *compositionCheck) someIntegrator(g *guard.Guard, guardian *Methodology, change map[string]any, world map[string]bool) satisfiability {
	cands := []map[string]any{nil}
	states := []string{""}
	if lc := ck.c.lifecycle(guardian); lc != nil {
		states = stateNames(lc)
	}
	for _, st := range states {
		for _, status := range []domain.ChangeStatus{domain.ChangeDraft, domain.ChangeActive} {
			cands = append(cands, map[string]any{"attached": true, "id": "i", "methodology": guardian.Name, "state": st, "status": string(status)})
		}
	}
	res := unsat
	for _, in := range cands {
		ok, err := g.CheckChange(change, guard.Facts{Integrator: in}, world)
		switch {
		case err != nil:
			res = unknown
		case ok:
			return sat
		}
	}
	return res
}

// composite checks the guards the chain of guardians adds up on the same change: m's own guardian with the guardians
// of the methodologies integrating m (on the changes m integrates), and the guardians above m on m's own changes.
// Each satisfiable alone, they may never hold together.
func (ck *compositionCheck) composite() {
	above := ck.chainAbove(ck.m)
	// on m's own changes: the guardians above it
	if len(above) > 1 {
		ck.compositeOn(ck.m, above, "", "", "")
	}
	if ck.m.Guardian == nil && !ck.exclusive() {
		return
	}
	framed, _ := ck.below()
	for _, f := range framed {
		levels := append(append(slices.Clone(f.between), ck.m), above...)
		if len(levels) < 2 {
			continue
		}
		ck.compositeOn(f.m, levels, f.via(f.m), "guardian.commit", "guardian.transitions.")
	}
}

// chainAbove are the methodologies integrating m, then the ones integrating those..., one per level (the first by name,
// as a project in force would pick one), m left out.
func (ck *compositionCheck) chainAbove(m *Methodology) []*Methodology {
	var out []*Methodology
	seen := map[string]bool{m.Name: true}
	for cur := m.Name; len(out) < maxComposedChain; {
		var next *Methodology
		for _, o := range ck.integrators(cur) {
			if !seen[o.Name] {
				next = o
				break
			}
		}
		if next == nil {
			break
		}
		out = append(out, next)
		seen[next.Name], cur = true, next.Name
	}
	return out
}

// compositeOn checks the guardians (innermost first) over a change of x, for the commit and each transition of x's
// lifecycle more than one of them guards, when m's own guard takes part (via: what m integrates on the way to x);
// commitPath / transPrefix: where to report on m ("": on the methodology).
func (ck *compositionCheck) compositeOn(x *Methodology, chain []*Methodology, via, commitPath, transPrefix string) {
	var commit []level
	inner := x.Name
	for _, g := range chain {
		if e := g.CommitGuard(inner); e != "" {
			commit = append(commit, level{expr: e, guardian: g})
		}
		inner = g.Name
	}
	report := func(path, what string, levels []level) {
		var parts []string
		for _, l := range levels {
			parts = append(parts, fmt.Sprintf("%s requires %s", l.guardian.Name, l.expr))
		}
		hint := "each holds alone but not together: align the states they require (" + strings.Join(parts, "; ") + ")"
		ck.add(path, hint, "the guardians of %s never hold together on %s of a change of %s", names(chain), what, x.Name)
	}
	own := x == ck.m // the guardians above m on its own changes: reported on the methodology
	if len(commit) > 1 && (own || ck.m.CommitGuard(via) != "") && ck.allAlone(commit, x, "") && ck.together(commit, x, "") == unsat {
		report(commitPath, "the commit", commit)
	}
	lc := ck.c.lifecycle(x)
	if lc == nil {
		return
	}
	for _, t := range transitionNames(lc) {
		var levels []level
		for _, g := range chain {
			if e := g.TransitionGuard(t); e != "" {
				levels = append(levels, level{expr: e, guardian: g})
			}
		}
		if len(levels) > 1 && (own || ck.m.TransitionGuard(t) != "") && ck.allAlone(levels, x, t) && ck.together(levels, x, t) == unsat {
			path := transPrefix + t
			if transPrefix == "" {
				path = ""
			}
			report(path, "the transition "+t, levels)
		}
	}
}

// allAlone: each guard holds alone (one that never does is reported by guardian(), on the methodology declaring it).
func (ck *compositionCheck) allAlone(levels []level, x *Methodology, transition string) bool {
	for _, l := range levels {
		if ck.together([]level{l}, x, transition) != sat {
			return false
		}
	}
	return true
}

// --- names --------------------------------------------------------------------------------------------------------

func names(ms []*Methodology) string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Name
	}
	return strings.Join(out, ", ")
}

func stateNames(lc *domain.Lifecycle) []string {
	out := make([]string, len(lc.States))
	for i, s := range lc.States {
		out[i] = s.Name
	}
	return out
}

func hasState(lc *domain.Lifecycle, name string) bool {
	return lc != nil && slices.Contains(stateNames(lc), name)
}

func transitionNames(lc *domain.Lifecycle) []string {
	var out []string
	for _, t := range lc.Transitions {
		if !slices.Contains(out, t.Name) {
			out = append(out, t.Name)
		}
	}
	return out
}

func hasTransition(lc *domain.Lifecycle, name string) bool {
	return lc != nil && slices.Contains(transitionNames(lc), name)
}

// fromStates are the states a transition leaves ("*" or "": every state).
func fromStates(lc *domain.Lifecycle, name string) []string {
	var out []string
	for _, t := range lc.Transitions {
		if t.Name != name {
			continue
		}
		if t.From == "" || t.From == "*" {
			return stateNames(lc)
		}
		if !slices.Contains(out, t.From) {
			out = append(out, t.From)
		}
	}
	return out
}

// activityNames are the processes (and methods, which run as agents), the step names and the step paths of m.
func activityNames(m *Methodology) (procs, steps, paths []string) {
	var walk func(prefix string, ss []Step)
	walk = func(prefix string, ss []Step) {
		for _, s := range ss {
			steps = append(steps, s.Name)
			paths = append(paths, prefix+"/"+s.Name)
			walk(prefix+"/"+s.Name, s.Steps)
		}
	}
	for _, p := range m.Processes {
		procs = append(procs, p.Name)
		walk(p.Name, p.Steps)
	}
	for _, x := range m.Methods {
		procs = append(procs, x.Name)
		walk(x.Name, x.Steps)
	}
	for _, a := range m.Agents {
		procs = append(procs, a.Name)
	}
	return procs, steps, paths
}

// conditionNames are the conditions the world of a change of m holds: declared, generated by the compiler (steps,
// processes...) and imported from the libraries.
func conditionNames(m *Methodology) []string {
	var out []string
	if c, _ := m.CompileLenient(); c != nil && c.Conditions != nil {
		out = append(out, c.Conditions.Names()...)
	}
	for _, x := range m.Conditions {
		out = append(out, x.Name)
	}
	for _, lib := range m.Imports {
		defs, _ := condition.Library(lib)
		for _, d := range defs {
			out = append(out, d.Name)
		}
	}
	return out
}

// literalRefs are the string literals an expression compares with a selector (sel == "x", "x" != sel, sel in ["x",
// "y"]), by selector; "world" holds the names read as world.<name> or world["<name>"].
func literalRefs(expr string) map[string][]string {
	out := map[string][]string{}
	if strings.TrimSpace(expr) == "" {
		return out
	}
	add := func(sel, v string) {
		if !slices.Contains(out[sel], v) {
			out[sel] = append(out[sel], v)
		}
	}
	for _, g := range cmpRE.FindAllStringSubmatch(expr, -1) {
		add(g[1], unquote(g[3]))
	}
	for _, g := range cmpRevRE.FindAllStringSubmatch(expr, -1) {
		add(g[3], unquote(g[1]))
	}
	for _, g := range inRE.FindAllStringSubmatch(expr, -1) {
		for _, q := range quotedRE.FindAllString(g[2], -1) {
			add(g[1], unquote(q))
		}
	}
	for _, g := range worldRE.FindAllStringSubmatch(expr, -1) {
		if g[1] != "" {
			add("world", g[1])
		} else {
			add("world", unquote(g[2]))
		}
	}
	return out
}

const selector = `((?:event\.)?(?:change|step|process|integrator)\.[A-Za-z_]\w*|event\.type)`
const quoted = `("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*')`

var (
	cmpRE    = regexp.MustCompile(`(?:^|[^\w.])` + selector + `\s*(==|!=)\s*` + quoted)
	cmpRevRE = regexp.MustCompile(quoted + `\s*(==|!=)\s*` + selector + `(?:[^\w.(]|$)`)
	inRE     = regexp.MustCompile(`(?:^|[^\w.])` + selector + `\s+in\s+\[([^\]]*)\]`)
	quotedRE = regexp.MustCompile(quoted)
	worldRE  = regexp.MustCompile(`(?:^|[^\w.])world(?:\.([A-Za-z_]\w*)|\[\s*` + quoted + `\s*\])`)
)

func unquote(q string) string {
	if len(q) >= 2 {
		return q[1 : len(q)-1]
	}
	return q
}

// suggest is the hint for a name not found among known: the closest one, else the list (what: what they are).
func suggest(v string, known []string, what string) string {
	if len(known) == 0 {
		return "there are no " + what
	}
	uniq := slices.Compact(slices.Sorted(slices.Values(known)))
	best, bestD := "", len(v)/2+2
	for _, k := range uniq {
		if d := levenshtein(strings.ToLower(v), strings.ToLower(k)); d < bestD {
			best, bestD = k, d
		}
	}
	list := uniq
	if len(list) > 12 {
		list = append(list[:12:12], "…")
	}
	if best != "" {
		return fmt.Sprintf("did you mean %q? (%s: %s)", best, what, strings.Join(list, ", "))
	}
	return what + ": " + strings.Join(list, ", ")
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur := make([]int, len(rb)+1)
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(rb)]
}
