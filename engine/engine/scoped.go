package engine

import (
	"context"
	"fmt"
	"maps"
	"sync"

	"github.com/google/uuid"
	"github.com/zimwip/goap/contracts/domain"

	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
)

// Scoped actions (ADR 0109 §3.5): an action of a scope runs once per pending subject, one subject per step, the
// planner seeing it lifted (its pre "some subject is pending", its effects the aggregates of its scoped effects). The
// engine picks the subject, checks the effects on it, and gives a subject up once it failed (Engine.MaxFailures).

func (e *Engine) maxFailures() int {
	if e.MaxFailures == 0 {
		return 1
	}
	return e.MaxFailures
}

// pendingSubjects returns up to n subjects the scoped action is pending for at the last observation, in the order of
// its scope, leaving out those it failed on too often in this process.
func (e *Engine) pendingSubjects(p *Process, a methodology.Action, n int) []string {
	failures := map[string]int{}
	for _, s := range p.Steps {
		if s.Action == a.Name && s.Subject != "" && !s.EffectsMet && !s.Progress && !s.EndedAt.IsZero() {
			failures[s.Subject]++
		}
	}
	var out []string
	for _, sub := range p.Subjects[a.Scope] {
		if len(out) == n {
			break
		}
		if sub.Values[a.SubjectCondition()] && failures[sub.ID] < e.maxFailures() {
			out = append(out, sub.ID)
		}
	}
	return out
}

// parallelism is the number of subjects a scoped action runs for at once: the engine's MaxParallel (default 4),
// fewer when the action asks for it (`parallel`); one for an action that is not an llm or tool action (a person
// takes one task at a time, a script or builtin may run sub-agents of the process).
func (e *Engine) parallelism(a methodology.Action) int {
	if a.Kind != methodology.KindLLM && a.Kind != methodology.KindTool {
		return 1
	}
	n := e.MaxParallel
	if n <= 0 {
		n = 4
	}
	if a.Parallel > 0 && a.Parallel < n {
		n = a.Parallel
	}
	return n
}

// bindSubject gives the blackboard of a run of a scoped action its subject (`vars.item`, `vars.subject`) and records
// the subject on the step.
func bindSubject(bb domain.Blackboard, a methodology.Action, id string, step *Step) domain.Blackboard {
	step.Subject = id
	bb.Vars = maps.Clone(bb.Vars)
	if bb.Vars == nil {
		bb.Vars = map[string]any{}
	}
	entry := condition.SubjectEntry(bb, a.Scope, id)
	bb.Vars["item"], bb.Vars["subject"] = entry, id
	step.SubjectRef = subjectRef(a.Scope, entry)
	return bb
}

// runScoped runs a scoped action for several pending subjects at once (ADR 0109 §3.5): one step per subject, their
// executors called concurrently (the model calls, the slow part), then what each returned settled on the process one
// after the other, in the order of the subjects, so that each observation sees the work of the steps before it. A
// subject whose implementation asks for another permission than the first one's waits for a later cycle; the first
// one's gate applies to all (same action, same initiator).
func (e *Engine) runScoped(ctx context.Context, p *Process, m *methodology.Compiled, bb domain.Blackboard, action methodology.Action,
	calls []LLMCall, ids []string) error {
	type job struct {
		i       int
		bb      domain.Blackboard
		impl    methodology.Action
		specErr error
		r       ran
		err     error
	}
	var jobs []job
	var permission string
	for n, id := range ids {
		var planning []LLMCall
		if n == 0 {
			planning = calls // the planning that chose the action belongs to its first run
		}
		step := newStep(p, bb, action, planning, e.clock())
		sbb := bindSubject(bb, action, id, &step)
		impl, spec, specErr := e.specialize(ctx, p, m, action, sbb)
		perm := action.Permission
		if specErr == nil && spec != "" {
			perm = impl.Permission
			step.Specialization = spec
		}
		if n == 0 {
			if waiting, err := e.gateAction(ctx, p, m, action, step, perm); err != nil || waiting {
				return err
			}
			permission = perm
		} else if perm != permission {
			break
		}
		step.Execution = uuid.NewString()
		p.Steps = append(p.Steps, step)
		jobs = append(jobs, job{i: len(p.Steps) - 1, bb: sbb, impl: impl, specErr: specErr})
	}
	if err := e.checkpoint(ctx, p); err != nil {
		return err
	}
	var wg sync.WaitGroup
	for k := range jobs {
		if jobs[k].specErr != nil {
			continue
		}
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			j.r, j.err = e.runAction(ctx, p, m, j.bb, j.impl, j.i)
		}(&jobs[k])
	}
	wg.Wait()
	var first error
	for _, j := range jobs {
		step := &p.Steps[j.i]
		var err error
		switch {
		case j.specErr != nil:
			step.Error, step.EndedAt = j.specErr.Error(), e.clock()
			err = e.recordFailure(p, action.Name)
			j.impl = action
		case j.err != nil:
			err = j.err
		default:
			err = e.settleAction(p, m, j.impl, j.i, j.r)
		}
		e.journal(ctx, p, actionRecord(p, j.i, j.impl.Kind, action.Step, step.Execution))
		p.Steps[j.i].dropExchanges()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
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
			return fmt.Sprintf("%s:%s", str(entry, "type"), k)
		}
		return str(entry, "id")
	case condition.ScopeRisk, condition.ScopeAction:
		return scope + ":" + str(entry, "key")
	}
	return ""
}

// actionVars are the variables an action sees: the process's, plus `item` and `subject` for a scoped run.
func actionVars(ac ActionContext) map[string]any {
	if !ac.Action.IsScoped() && ac.Form == nil {
		return ac.Process.Vars
	}
	out := maps.Clone(ac.Process.Vars)
	if out == nil {
		out = map[string]any{}
	}
	if ac.Action.IsScoped() {
		out["item"], out["subject"] = ac.Blackboard.Vars["item"], ac.Blackboard.Vars["subject"]
	}
	if ac.Form != nil {
		out["form"] = ac.Form
	}
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
