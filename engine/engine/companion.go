package engine

import (
	"context"
	"fmt"
	"github.com/zimwip/goap/contracts/events"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/engine/condition"
	"github.com/zimwip/goap/engine/methodology"
)

// CompanionPrefix starts the Trigger of a companion run: "companion:<methodology>/<process>" (ADR 0036 §3).
const CompanionPrefix = "companion:"

// companionLock prefixes the lock serializing the companion runs of a change, so two events of one change never
// start two, whichever replica handles them (ADR 0124).
const companionLock = "companions:"

// IsCompanion reports whether a run is the companion of a change (a process of a transverse methodology).
func (p *Process) IsCompanion() bool { return strings.HasPrefix(p.Trigger, CompanionPrefix) }

// Accompany is the choreography of a change (ADR 0036 §3): no process orchestrates another, they react to the events
// of the change they share. The processes of the transverse methodologies run alongside the changes of the
// methodologies they apply to (appliesTo): each event they subscribe to (on) runs their companion run on the change
// again, with the event in vars.event (the events that arrive while it is at work wait in its inbox); and when the
// change moves because of someone else, the processes waiting on it for conditions they do not establish (such as
// risks_under_control), and those stuck on it, are tried again.
func (e *Engine) Accompany(ctx context.Context, ev TriggerEvent) {
	var changeID domain.ChangeID
	var of, from string // the methodology the change is worked with, the process the event comes from
	var ofRef string    // of at the version the change or the process follows (ADR 0113)
	companion := false
	switch {
	case ev.Process != nil:
		if ev.Process.ChangeID == "" {
			return
		}
		changeID, of, from, companion = ev.Process.ChangeID, ev.Process.Methodology, ev.Process.ID, ev.Process.IsCompanion()
		ofRef = domain.MethodologyRef(of, ev.Process.MethodologyVersion)
	case ev.Change != nil:
		changeID, of, ofRef = ev.Change.ID, ev.Change.Methodology, ev.Change.MethodologyRef()
	default:
		return
	}
	// what someone else did may be what a process waits for
	switch ev.Type {
	case events.StepCompleted, events.ProcessCompleted, events.ChangeItemAdded:
		e.retryBlocked(ctx, changeID, from)
	}
	if ev.Process != nil && ev.Process.IsInstance() && !companion && ev.Type == events.ProcessCompleted {
		// a persistent instance takes the next execution of its inbox, or hands it to the next instance (ADR 0112)
		if err := e.nextInInbox(ctx, ev.Process.ID); err != nil {
			e.log().Warn("instance inbox", "process", ev.Process.ID, "err", err)
		}
	}
	if companion {
		// a companion's own events do not wake companions; when it completes, it takes the next event of its inbox
		if ev.Type == events.ProcessCompleted {
			if err := e.nextInInbox(ctx, ev.Process.ID); err != nil {
				e.log().Warn("companion inbox", "process", ev.Process.ID, "err", err)
			}
		}
		return
	}
	if of == "" {
		if bb, err := e.Graph.Blackboard(ctx, changeID); err == nil {
			of, ofRef = bb.Change.Methodology, bb.Change.MethodologyRef()
		}
	}
	ms, err := e.Methodologies.List(ctx)
	if err != nil {
		e.log().Warn("companions: methodologies", "err", err)
		return
	}
	act := ev.activation()
	e.relay(ctx, ev, act, changeID)
	if own, err := e.Methodologies.Methodology(ctx, ofRef); of != "" && err == nil {
		e.react(ctx, own, ev, act, changeID, methodology.FromSelf, nil)
	}
	for _, m := range ms {
		ok, supersede := match(m.Subscriptions(), ev, act, methodology.FromSelf)
		if !slices.Contains(m.AppliesTo(), of) || !ok {
			continue
		}
		if ev.Type == events.ChangeItemAdded && !slices.ContainsFunc(ev.Items, func(it domain.ChangeItem) bool { return !ownItem(m, it) }) {
			continue // its own productions do not wake it
		}
		event := maps.Clone(act)
		event["id"], event["at"] = uuid.NewString(), e.clock().UnixMilli()
		for _, proc := range m.Processes() {
			if err := e.accompany(ctx, m, proc.Name, changeID, event, supersede); err != nil {
				e.log().Warn("companion", "methodology", m.Name(), "process", proc.Name, "change", changeID, "err", err)
			}
		}
	}
}

// relay carries the event of a change across the integration relation (ADR 0106): to the change integrating it (its
// parent), whose methodology subscribes to the events of the changes it integrates (on.from integrated), and to the
// changes it integrates (its open sub-changes), whose methodologies subscribe to the events of their integrator
// (on.from integrator), and to the open changes depending on it (ADR 0108, on.from dependency): a dependency merging
// wakes the runs of its dependents waiting for it (`dependencies_met`). Each runs the processes of that methodology on its own change, as a companion run, with the
// event in vars.event (its "relation" says which side it came from); the runs of those changes waiting for conditions
// are tried again.
func (e *Engine) relay(ctx context.Context, ev TriggerEvent, act map[string]any, changeID domain.ChangeID) {
	var parent domain.ChangeID
	if ev.Change != nil && ev.Change.ID == changeID {
		parent = ev.Change.ParentID
	} else if bb, err := e.Graph.Blackboard(ctx, changeID); err == nil {
		parent = bb.Change.ParentID
	}
	var targets []domain.Change
	var from []string
	if parent != "" {
		if p, err := e.Graph.Change(ctx, parent); err == nil {
			targets, from = append(targets, p), append(from, methodology.FromIntegrated)
		}
	}
	if subs, err := e.Graph.ListChanges(ctx, graphapi.ChangesFilter{Status: []domain.ChangeStatus{domain.ChangeDraft, domain.ChangeActive}}); err == nil {
		for _, s := range subs {
			if s.ParentID == changeID {
				targets, from = append(targets, s), append(from, methodology.FromIntegrator)
			}
			if slices.Contains(s.DependsOn, changeID) { // a change depending on it (ADR 0108)
				targets, from = append(targets, s), append(from, methodology.FromDependency)
			}
		}
	}
	for i, t := range targets {
		e.retryBlocked(ctx, t.ID, "")
		if t.Methodology == "" {
			continue
		}
		m, err := e.Methodologies.Methodology(ctx, t.MethodologyRef())
		if err != nil {
			continue
		}
		e.react(ctx, m, ev, act, t.ID, from[i], map[string]any{"relation": from[i]})
		ok, supersede := match(m.Subscriptions(), ev, act, from[i])
		if !ok {
			continue
		}
		event := maps.Clone(act)
		event["id"], event["at"], event["relation"] = uuid.NewString(), e.clock().UnixMilli(), from[i]
		for _, proc := range m.Processes() {
			if err := e.accompany(ctx, m, proc.Name, t.ID, event, supersede); err != nil {
				e.log().Warn("relay", "methodology", m.Name(), "process", proc.Name, "change", t.ID, "err", err)
			}
		}
	}
}

// react runs the reactive activities of a methodology (ADR 0110) that subscribe to the event coming from the given
// side of the change: each as its own companion run on that change, kept and run again in place per event.
func (e *Engine) react(ctx context.Context, m *methodology.Compiled, ev TriggerEvent, act map[string]any, changeID domain.ChangeID, from string, extra map[string]any) {
	for _, r := range m.Reactions() {
		ok, supersede := match(r.On, ev, act, from)
		if !ok {
			continue
		}
		raised := ev.Type == events.ChangeItemAdded || (len(ev.Items) > 0 && !methodology.StandardEvent(ev.Type))
		if raised && !slices.ContainsFunc(ev.Items, func(it domain.ChangeItem) bool { return !r.Owns(it.ProducedBy) }) {
			continue // its own productions (and the events they raise) do not wake it
		}
		event := maps.Clone(act)
		maps.Copy(event, extra)
		event["id"], event["at"] = uuid.NewString(), e.clock().UnixMilli()
		key := CompanionPrefix + m.Name() + "/" + r.Activity
		intent := fmt.Sprintf("%s %s on %s", r.Kind, r.Activity, ev.Type)
		if err := e.companionRun(ctx, m, key, r.Agent, r.Goal, intent, changeID, event, supersede); err != nil {
			e.log().Warn("reaction", "methodology", m.Name(), "activity", r.Activity, "change", changeID, "err", err)
		}
	}
}

// match reports whether one of the subscriptions takes the event coming from the given side, and whether one that
// takes it supersedes the work in flight (Subscription.Supersede).
func match(subs []methodology.Subscription, ev TriggerEvent, act map[string]any, from string) (ok, supersede bool) {
	for _, sub := range subs {
		if sub.Event != ev.Type || sub.Origin() != from {
			continue
		}
		f, err := condition.CompileEventFilter(sub.Filter)
		if err == nil && f.Match(act) {
			ok, supersede = true, supersede || sub.Supersede
		}
	}
	return ok, supersede
}

// ownItem reports whether an item was produced by an action of the methodology m.
func ownItem(m *methodology.Compiled, it domain.ChangeItem) bool {
	_, ok := m.Action(it.ProducedBy)
	return ok
}

// accompany runs the companion of a process on a change for an event: it starts it, runs it again when it has ended,
// or puts the event in its inbox when it is at work.
func (e *Engine) accompany(ctx context.Context, m *methodology.Compiled, process string, changeID domain.ChangeID, event map[string]any, supersede bool) error {
	return e.companionRun(ctx, m, CompanionPrefix+m.Name()+"/"+process, process, process, process+" alongside the change", changeID, event, supersede)
}

// companionRun runs the companion of key on a change for an event, towards goal with agent: it starts it, runs it
// again when it has ended, or puts the event in its inbox when it is at work (ADR 0036 §3, ADR 0110); an event of a
// subscription that supersedes interrupts the work and runs it again (restartFor).
func (e *Engine) companionRun(ctx context.Context, m *methodology.Compiled, key, agent, goal, intent string, changeID domain.ChangeID, event map[string]any, supersede bool) error {
	defer e.lock(companionLock + string(changeID))()
	all, err := e.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range all {
		if p.ChangeID != changeID || p.Trigger != key {
			continue
		}
		if p.Status == StatusCancelled {
			return nil // a person stopped it: the events of the change do not bring it back
		}
		if p.Status == StatusCompleted || p.Status == StatusStuck || p.Status == StatusFailed || p.WaitsForConditions() {
			return e.rerun(ctx, p.ID, event)
		}
		if supersede {
			// waiting for the run to let go must not hold the companions, nor the publisher of the event
			id := p.ID
			e.background(func() {
				if err := e.restartFor(context.WithoutCancel(ctx), id, event); err != nil {
					e.log().Warn("supersede", "process", id, "err", err)
				}
			})
			return nil
		}
		return e.enqueue(ctx, p.ID, event) // at work, or waiting for someone: after this run
	}
	bb, err := e.Graph.Blackboard(ctx, changeID)
	if err != nil {
		return err
	}
	if bb.Change.Status == domain.ChangeApplied || bb.Change.Status == domain.ChangeAbandoned {
		return nil
	}
	who := authz.System(key).For(eventUser(event)) // a consequence of what the end user did (ADR 0114)
	who.Org = "system"
	p, err := e.Start(authz.With(ctx, who), StartRequest{Methodology: m.Name(), Agent: agent, Goal: goal, ChangeID: changeID,
		OwnerOrg: bb.Change.OwnerOrg, Trigger: key, Title: bb.Change.Title, Vars: map[string]any{"event": event},
		Intent: fmt.Sprintf("%s: %s", intent, bb.Change.Intent)})
	if err != nil {
		return err
	}
	if p.Status == StatusRunning {
		e.schedule(p.ID)
	}
	return nil
}

// eventUser is the end user of an event as a companion run gets it (TriggerEvent.activation, ADR 0114).
func eventUser(event map[string]any) string {
	u, _ := event["endUser"].(string)
	return u
}

// enqueue keeps an event for a companion at work.
func (e *Engine) enqueue(ctx context.Context, id string, event map[string]any) error {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	p.Inbox = append(p.Inbox, event)
	return e.Store.Put(ctx, p)
}

// nextInInbox runs a completed companion again for the next event of its inbox.
func (e *Engine) nextInInbox(ctx context.Context, id string) error {
	p, err := e.Store.Get(ctx, id)
	if err != nil || len(p.Inbox) == 0 {
		return err
	}
	defer e.lock(companionLock + string(p.ChangeID))()
	if p, err = e.Store.Get(ctx, id); err != nil || len(p.Inbox) == 0 {
		return err
	}
	if p.Closed {
		return e.handOver(ctx, p)
	}
	return e.rerun(ctx, id, nil)
}

// restartFor interrupts a companion at work and runs it again for the event, on the change as it is now (ADR 0110):
// its sub-processes (the agent of the step it carries out, a task waiting for a person) are cancelled, its run in
// flight is interrupted (its model and tool calls end with their context) and let go without failing, then it starts
// again with what it waited for, what it tried and its inbox dropped: the new run sees every earlier event's change.
func (e *Engine) restartFor(ctx context.Context, id string, event map[string]any) error {
	defer e.lock(supersedeLock + id)() // one at a time per run: the last one runs on the latest change
	e.cancelling.Store(id, supersededBy)
	stored := true
	defer func() {
		if stored {
			e.cancelling.Delete(id)
		}
	}()
	all, err := e.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, c := range all {
		if c.ParentID == id && !c.Status.Terminal() {
			if _, err := e.cancel(ctx, c.ID, supersededBy, "superseded by a newer event of the change"); err != nil {
				return fmt.Errorf("sub-process %s: %w", c.ID, err)
			}
		}
	}
	e.interrupt(ctx, id, supersededBy)
	unlock := e.lock(id) // the run lets go
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		unlock()
		return err
	}
	if p.Status.Terminal() && p.Status != StatusCompleted && p.Status != StatusFailed {
		unlock()
		return nil // cancelled meanwhile: a person stopped it
	}
	vars := maps.Clone(p.Vars)
	if vars == nil {
		vars = map[string]any{}
	}
	vars["event"] = event
	p.Vars, p.Inbox, p.Children = vars, nil, nil
	p.Initiator = p.Initiator.For(eventUser(event)) // the run again is a consequence of the newer event's end user
	p.Status, p.Error, p.Plan, p.Pending = StatusRunning, "", nil, nil
	p.Disabled = map[string]bool{}
	p.Runs++
	e.queue(ctx, p, "superseded", map[string]any{"change": string(p.ChangeID), "event": event["type"]})
	err = e.save(ctx, p, "step")
	e.cancelling.Delete(id) // before the new run can look
	stored = false
	unlock()
	if err == nil {
		e.schedule(id)
	}
	return err
}

// supersedeRuns cancels the runs of a trigger still at work on a change, before the trigger starts a new one for a
// newer event (methodology.Trigger.Supersede): what they do is about a state of the change that is gone.
func (e *Engine) supersedeRuns(ctx context.Context, changeID domain.ChangeID, trigger, by string) {
	all, err := e.Store.List(ctx)
	if err != nil {
		return
	}
	for _, p := range all {
		if p.ChangeID == changeID && p.Trigger == trigger && p.ParentID == "" && !p.Status.Terminal() {
			if _, err := e.cancel(ctx, p.ID, by, "superseded by a newer event of the change"); err != nil {
				e.log().Warn("supersede", "process", p.ID, "err", err)
			}
		}
	}
}

const (
	supersededBy  = "a newer event of the change"
	supersedeLock = "supersede:"
)

// retryBlocked tries again the processes of a change that wait for conditions established outside them, or are stuck
// (companions aside, their events run them again, and the one the event comes from).
func (e *Engine) retryBlocked(ctx context.Context, changeID domain.ChangeID, except string) {
	all, err := e.Store.List(ctx)
	if err != nil {
		return
	}
	for _, p := range all {
		if p.ChangeID == changeID && (p.Status == StatusStuck || p.WaitsForConditions()) && p.ID != except && !p.IsCompanion() {
			if err := e.rerun(ctx, p.ID, nil); err != nil {
				e.log().Warn("retry", "process", p.ID, "err", err)
			}
		}
	}
}

// rerun runs an ended process, or one waiting for conditions, again on the change as it is now: for a companion, for
// the event given, else the next of its inbox.
func (e *Engine) rerun(ctx context.Context, id string, event map[string]any) error {
	unlock := e.lock(id)
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		unlock()
		return err
	}
	if p.Status != StatusCompleted && p.Status != StatusStuck && p.Status != StatusFailed && !p.WaitsForConditions() {
		unlock()
		return nil
	}
	if event == nil && len(p.Inbox) > 0 {
		event, p.Inbox = p.Inbox[0], p.Inbox[1:]
	}
	if event != nil {
		vars := maps.Clone(p.Vars)
		if vars == nil {
			vars = map[string]any{}
		}
		vars["event"] = event
		p.Vars = vars
		p.Initiator = p.Initiator.For(eventUser(event)) // this run is a consequence of the event's end user
	}
	p.Status, p.Error, p.Plan, p.Pending = StatusRunning, "", nil, nil
	p.Disabled = map[string]bool{} // what failed before may work on the change as it is now
	p.Runs++
	e.queue(ctx, p, "change moved", map[string]any{"change": string(p.ChangeID), "event": event["type"]})
	err = e.save(ctx, p, "step")
	unlock()
	if err == nil {
		e.schedule(id)
	}
	return err
}

// StepEvent is a step of a process that completed: what the transverse methodologies react to (ADR 0036 §3).
type StepEvent struct {
	// Path of the step ("software_delivery/design"), its Process and Name, the Action that ran and the Method chosen.
	Path, Process, Name, Action, Method string
}

// stepCompleted publishes that a step of p's process completed (step.completed).
func (e *Engine) stepCompleted(ctx context.Context, p *Process, m *methodology.Compiled, a methodology.Action, st *Step) {
	if e.Events == nil {
		return
	}
	step := &StepEvent{Path: a.Step, Action: a.Name, Method: st.Specialization}
	if s, ok := m.StepByPath(a.Step); ok {
		step.Process, step.Name = s.Owner, s.Name
	}
	ev := ProcessEvent{Event: events.BrokerStepCompleted, Process: p, Time: e.clock(), Step: step}
	if err := e.Events.Publish(ctx, fmt.Sprintf("goap.process.%s.%s", p.ID, events.BrokerStepCompleted), ev); err != nil {
		e.log().Warn("publish failed", "err", err)
	}
}
