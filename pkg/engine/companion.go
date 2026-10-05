package engine

import (
	"context"
	"fmt"
	"github.com/zimwip/goap/pkg/events"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/condition"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// CompanionPrefix starts the Trigger of a companion run: "companion:<methodology>/<process>" (ADR 0036 §3).
const CompanionPrefix = "companion:"

// companions serializes the start of companion runs, so two events of one change never start two.
var companions sync.Mutex

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
	companion := false
	switch {
	case ev.Process != nil:
		if ev.Process.ChangeID == "" {
			return
		}
		changeID, of, from, companion = ev.Process.ChangeID, ev.Process.Methodology, ev.Process.ID, ev.Process.IsCompanion()
	case ev.Change != nil:
		changeID, of = ev.Change.ID, ev.Change.Methodology
	default:
		return
	}
	// what someone else did may be what a process waits for
	switch ev.Type {
	case events.StepCompleted, events.ProcessCompleted, events.ChangeItemAdded:
		e.retryBlocked(ctx, changeID, from)
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
			of = bb.Change.Methodology
		}
	}
	ms, err := e.Methodologies.List(ctx)
	if err != nil {
		e.log().Warn("companions: methodologies", "err", err)
		return
	}
	act := ev.activation()
	for _, m := range ms {
		if !slices.Contains(m.AppliesTo, of) || !subscribed(m, ev, act) {
			continue
		}
		if ev.Type == events.ChangeItemAdded && !slices.ContainsFunc(ev.Items, func(it domain.ChangeItem) bool { return !ownItem(m, it) }) {
			continue // its own productions do not wake it
		}
		event := maps.Clone(act)
		event["id"], event["at"] = uuid.NewString(), e.clock().UnixMilli()
		for _, proc := range m.Processes {
			if err := e.accompany(ctx, m, proc.Name, changeID, event); err != nil {
				e.log().Warn("companion", "methodology", m.Name, "process", proc.Name, "change", changeID, "err", err)
			}
		}
	}
}

// subscribed reports whether a transverse methodology reacts to the event.
func subscribed(m *methodology.Compiled, ev TriggerEvent, act map[string]any) bool {
	for _, sub := range m.Subscriptions() {
		if sub.Event != ev.Type {
			continue
		}
		f, err := condition.CompileEventFilter(sub.Filter)
		if err == nil && f.Match(act) {
			return true
		}
	}
	return false
}

// ownItem reports whether an item was produced by an action of the methodology m.
func ownItem(m *methodology.Compiled, it domain.ChangeItem) bool {
	_, ok := m.Action(it.ProducedBy)
	return ok
}

// accompany runs the companion of a process on a change for an event: it starts it, runs it again when it has ended,
// or puts the event in its inbox when it is at work.
func (e *Engine) accompany(ctx context.Context, m *methodology.Compiled, process string, changeID domain.ChangeID, event map[string]any) error {
	companions.Lock()
	defer companions.Unlock()
	key := CompanionPrefix + m.Name + "/" + process
	all, err := e.Store.List(ctx)
	if err != nil {
		return err
	}
	for _, p := range all {
		if p.ChangeID != changeID || p.Trigger != key {
			continue
		}
		if p.Status == StatusCompleted || p.Status == StatusStuck || p.Status == StatusFailed || p.WaitsForConditions() {
			return e.rerun(ctx, p.ID, event)
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
	who := authz.System(key)
	who.Org = "system"
	p, err := e.Start(authz.With(ctx, who), StartRequest{Methodology: m.Name, Agent: process, Goal: process, ChangeID: changeID,
		OwnerOrg: bb.Change.OwnerOrg, Trigger: key, Title: bb.Change.Title, Vars: map[string]any{"event": event},
		Intent: fmt.Sprintf("%s alongside the change: %s", process, bb.Change.Intent)})
	if err != nil {
		return err
	}
	if p.Status == StatusRunning {
		e.schedule(p.ID)
	}
	return nil
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
	companions.Lock()
	defer companions.Unlock()
	p, err := e.Store.Get(ctx, id)
	if err != nil || len(p.Inbox) == 0 {
		return err
	}
	return e.rerun(ctx, id, nil)
}

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
	}
	p.Status, p.Error, p.Plan, p.Pending = StatusRunning, "", nil, nil
	p.Disabled = map[string]bool{} // what failed before may work on the change as it is now
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
func (e *Engine) stepCompleted(ctx context.Context, p *Process, a methodology.Action, st *Step) {
	if e.Events == nil {
		return
	}
	process, _, _ := strings.Cut(a.Step, "/")
	name := a.Step[strings.LastIndex(a.Step, "/")+1:]
	ev := ProcessEvent{Event: events.BrokerStepCompleted, Process: p, Time: e.clock(),
		Step: &StepEvent{Path: a.Step, Process: process, Name: name, Action: a.Name, Method: st.Specialization}}
	if err := e.Events.Publish(ctx, fmt.Sprintf("goap.process.%s.%s", p.ID, events.BrokerStepCompleted), ev); err != nil {
		e.log().Warn("publish failed", "err", err)
	}
}
