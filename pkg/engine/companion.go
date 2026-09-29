package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/methodology"
)

// CompanionPrefix starts the Trigger of a companion run: "companion:<methodology>/<process>" (ADR 0036 §3).
const CompanionPrefix = "companion:"

// companions serializes the start of companion runs, so two events of one change never start two.
var companions sync.Mutex

// IsCompanion reports whether a run is the companion of a change (a process of a transverse methodology).
func (p *Process) IsCompanion() bool { return strings.HasPrefix(p.Trigger, CompanionPrefix) }

// Accompany runs the processes of the transverse methodologies alongside the changes of the methodologies they apply
// to (appliesTo, ADR 0036 §3): one companion run per change and process, started when a process is attached to the
// change, run again when the change moves (a process on it completes, someone else adds a risk or an action), and
// completing as soon as its goal holds.
func (e *Engine) Accompany(ctx context.Context, ev TriggerEvent) {
	var changeID domain.ChangeID
	var of string // the methodology the change is worked with
	switch ev.Type {
	case "process.attached", "process.completed":
		p := ev.Process
		if p == nil || p.ChangeID == "" || p.IsCompanion() {
			return
		}
		changeID, of = p.ChangeID, p.Methodology
	case "change.created", "change.item_added":
		if ev.Change == nil {
			return
		}
		changeID, of = ev.Change.ID, ev.Change.Methodology
		if ev.Type == "change.item_added" && !slices.ContainsFunc(ev.Items, func(it domain.ChangeItem) bool {
			return it.Kind == domain.KindRisk || it.Kind == domain.KindAction
		}) {
			return
		}
	default:
		return
	}
	if of == "" {
		return
	}
	ms, err := e.Methodologies.List(ctx)
	if err != nil {
		e.log().Warn("companions: methodologies", "err", err)
		return
	}
	for _, m := range ms {
		if !slices.Contains(m.AppliesTo, of) {
			continue
		}
		for _, proc := range m.Processes {
			if ev.Type == "change.item_added" && !slices.ContainsFunc(ev.Items, func(it domain.ChangeItem) bool { return !ownItem(m, it) }) {
				continue // its own productions do not wake it
			}
			if err := e.accompany(ctx, m, proc.Name, changeID); err != nil {
				e.log().Warn("companion", "methodology", m.Name, "process", proc.Name, "change", changeID, "err", err)
			}
		}
	}
}

// ownItem reports whether an item was produced by an action of the methodology m.
func ownItem(m *methodology.Compiled, it domain.ChangeItem) bool {
	_, ok := m.Action(it.ProducedBy)
	return ok
}

// accompany starts the companion run of a process on a change, or runs it again when it has ended.
func (e *Engine) accompany(ctx context.Context, m *methodology.Compiled, process string, changeID domain.ChangeID) error {
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
		switch p.Status {
		case StatusCompleted, StatusStuck, StatusFailed:
			return e.rerun(ctx, p.ID)
		}
		return nil // at work, or waiting for someone
	}
	bb, err := e.Graph.Blackboard(ctx, changeID)
	if err != nil {
		return err
	}
	if bb.Change.Status == domain.ChangeApplied || bb.Change.Status == domain.ChangeAbandoned {
		return nil
	}
	who := authz.Principal{Subject: "system:" + key, Org: "system"}
	p, err := e.Start(authz.With(ctx, who), StartRequest{Methodology: m.Name, Agent: process, Goal: process, ChangeID: changeID,
		OwnerOrg: bb.Change.OwnerOrg, Trigger: key, Title: bb.Change.Title,
		Intent: fmt.Sprintf("%s alongside the change: %s", process, bb.Change.Intent)})
	if err != nil {
		return err
	}
	if p.Status == StatusRunning {
		e.schedule(p.ID)
	}
	return nil
}

// rerun runs an ended companion again: its goal is evaluated anew on the change as it is now.
func (e *Engine) rerun(ctx context.Context, id string) error {
	unlock := e.lock(id)
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		unlock()
		return err
	}
	if p.Status != StatusCompleted && p.Status != StatusStuck && p.Status != StatusFailed {
		unlock()
		return nil
	}
	p.Status, p.Error, p.Plan, p.Pending = StatusRunning, "", nil, nil
	p.Disabled = map[string]bool{} // what failed before may work on the change as it is now
	e.queue(ctx, p, "companion", map[string]any{"change": string(p.ChangeID)})
	err = e.save(ctx, p, "step")
	unlock()
	if err == nil {
		e.schedule(id)
	}
	return err
}
