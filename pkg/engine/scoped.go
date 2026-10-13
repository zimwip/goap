package engine

import (
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/methodology"
)

// Scoped actions (ADR 0109 §3.5): an action of a scope runs once per pending subject, one subject per step, the
// planner seeing it lifted (its pre "some subject is pending", its effects the aggregates of its scoped effects). The
// engine picks the subject, checks the effects on it, and gives a subject up after repeated failures.

func (e *Engine) maxFailures() int {
	if e.MaxFailures == 0 {
		return 2
	}
	return e.MaxFailures
}

// pendingSubject returns the first subject the scoped action is pending for at the last observation, leaving out
// those it failed on too often in this process.
func (e *Engine) pendingSubject(p *Process, a methodology.Action) (string, bool) {
	failures := map[string]int{}
	for _, s := range p.Steps {
		if s.Action == a.Name && s.Subject != "" && !s.EffectsMet && !s.Progress && !s.EndedAt.IsZero() {
			failures[s.Subject]++
		}
	}
	for _, sub := range p.Subjects[a.Scope] {
		if sub.Values[a.SubjectCondition()] && failures[sub.ID] < e.maxFailures() {
			return sub.ID, true
		}
	}
	return "", false
}

// disable takes an action out of the plans of the process.
func (e *Engine) disable(p *Process, action, why string) {
	if p.Disabled == nil {
		p.Disabled = map[string]bool{}
	}
	p.Disabled[action] = true
	e.log().Warn("action disabled", "process", p.ID, "action", action, "reason", why)
}

// subjectHolds reports whether the scoped effects of the action hold on the subject.
func subjectHolds(p *Process, a methodology.Action, id string) bool {
	for _, sub := range p.Subjects[a.Scope] {
		if sub.ID == id {
			return sub.Holds(a.ScopedEffects)
		}
	}
	// the subject is gone (an impact withdrawn): nothing is left to do for it
	return true
}

// changeEffects are the effects of the change of a scoped action, without the aggregates its scoped effects lift to.
func changeEffects(a methodology.Action) map[string]bool {
	out := map[string]bool{}
	for k, v := range a.Effects {
		if !a.Lifted[k] {
			out[k] = v
		}
	}
	return out
}

// subjectRef is what the log of the change names a subject by: the id of an impact, "<kind>:<key>" of a keyed item
// or record (the history reads a scoped run's inputs as that subject alone).
func subjectRef(scope string, entry map[string]any) string {
	if entry == nil {
		return ""
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	switch scope {
	case condition.ScopeImpact:
		return str(entry, "id")
	case condition.ScopeItem:
		data, _ := entry["data"].(map[string]any)
		if k := str(data, "key"); k != "" {
			return fmt.Sprintf("%s:%s", str(entry, "kind"), k)
		}
		return str(entry, "id")
	case condition.ScopeRisk, condition.ScopeAction:
		return scope + ":" + str(entry, "key")
	}
	return ""
}

// actionVars are the variables an action sees: the process's, plus `item` and `subject` for a scoped run.
func actionVars(ac ActionContext) map[string]any {
	if !ac.Action.IsScoped() {
		return ac.Process.Vars
	}
	out := maps.Clone(ac.Process.Vars)
	if out == nil {
		out = map[string]any{}
	}
	out["item"], out["subject"] = ac.Blackboard.Vars["item"], ac.Blackboard.Vars["subject"]
	return out
}

// counted is the number of steps the step limit of a process counts: the runs of a scoped action for its subjects
// count once per action (their number is the number of subjects, bounded by the failures allowed per subject).
func counted(steps []Step) int {
	n, scoped := 0, map[string]bool{}
	for _, s := range steps {
		if s.Subject == "" {
			n++
		} else if !scoped[s.Action] {
			scoped[s.Action] = true
			n++
		}
	}
	return n
}
