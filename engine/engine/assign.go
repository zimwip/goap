package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/journal"
)

// Who holds a human task (ADR 0158). A task a run waits on a person for is addressed to roles, or to the initiator of
// the run; anyone it is addressed to may answer it. One of them accepts it to make it theirs: while they hold it, the
// others may no longer answer it. They may give it back, and anyone may turn down a task offered to them, so that it
// is no longer offered. An administrator of the project of the run, or of a project above it, assigns it to a person
// who may answer it (permission task:assign, PermissionAssignTask), or takes it back from its holder.

// PermissionAssignTask lets a principal give a human task of a run to someone (by default the administrators and the
// owners of the project of the run or of a project above it, the platform administrators).
const PermissionAssignTask = "task:assign"

// Decisions on a task, in the journal (journal.KindTask).
const (
	TaskAccepted = "accepted"
	TaskDeclined = "declined"
	TaskReleased = "released"
	TaskAssigned = "assigned"
)

// assignable reports whether a pending task of p is one a person holds: an input or an approval.
func assignable(p *Process) bool {
	return p.Status == StatusWaiting && p.Pending != nil && (p.Pending.Kind == TaskInput || p.Pending.Kind == TaskApproval)
}

// mayAnswerTask checks that who may answer the pending task of p by the rules of the call answering it, whoever holds
// it.
func (e *Engine) mayAnswerTask(ctx context.Context, p *Process, who authz.Principal) error {
	if p.Pending.Kind == TaskApproval {
		return yes(e.mayApprove(ctx, p, who))
	}
	m, err := e.methodologyOf(ctx, p)
	if err != nil {
		return err
	}
	return e.maySubmit(ctx, p, m, who)
}

// mayAssign reports whether who may give the pending task of p to someone.
func (e *Engine) mayAssign(ctx context.Context, p *Process, who authz.Principal) (bool, error) {
	return e.allowed(ctx, p, who, PermissionAssignTask)
}

// holds checks that the task of p is not held by someone other than who: an assigned task is its holder's to answer,
// or of one who may reassign it.
func (e *Engine) holds(ctx context.Context, p *Process, who authz.Principal) error {
	t := p.Pending
	if t == nil || t.Assignee == "" || t.Assignee == who.EndUser() {
		return nil
	}
	if ok, err := e.mayAssign(ctx, p, who); err != nil || ok {
		return err
	}
	return fmt.Errorf("the task of process %s is held by %s: %w", p.ID, t.Assignee, authz.ErrForbidden)
}

// AcceptTask makes the pending task of a run the caller's: they must be able to answer it, and nobody else may hold it.
func (e *Engine) AcceptTask(ctx context.Context, id string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.pendingTask(ctx, id)
	if err != nil {
		return nil, err
	}
	who := authz.From(ctx)
	me := who.EndUser()
	if p.Pending.Assignee == me {
		return p, nil
	}
	if p.Pending.Assignee != "" {
		return nil, fmt.Errorf("the task of process %s is held by %s: %w", id, p.Pending.Assignee, ErrInvalidState)
	}
	if err := e.mayAnswerTask(ctx, p, who); err != nil {
		return nil, err
	}
	e.assign(ctx, p, me, who.Subject, TaskAccepted)
	return p, e.save(ctx, p, "step")
}

// RejectTask turns down the pending task of a run: its holder gives it back, or a person it is offered to declines it.
// Either way it is no longer offered to them.
func (e *Engine) RejectTask(ctx context.Context, id, reason string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.pendingTask(ctx, id)
	if err != nil {
		return nil, err
	}
	who := authz.From(ctx)
	me := who.EndUser()
	t := p.Pending
	decision := TaskDeclined
	switch {
	case t.Assignee == me:
		decision = TaskReleased
	case t.Assignee != "":
		return nil, fmt.Errorf("the task of process %s is held by %s: %w", id, t.Assignee, ErrInvalidState)
	case slices.Contains(t.Declined, me):
		return p, nil
	default:
		if err := e.mayAnswerTask(ctx, p, who); err != nil {
			return nil, err
		}
	}
	t.Assignee, t.AssignedBy, t.AssignedAt = "", "", time.Time{}
	if !slices.Contains(t.Declined, me) {
		t.Declined = append(t.Declined, me)
	}
	e.journal(ctx, p, journal.Record{Kind: journal.KindTask, Step: t.Step, Action: t.Action, Actor: who.Subject,
		Data: map[string]any{"decision": decision, "reason": strings.TrimSpace(reason), "kind": t.Kind}})
	return p, e.save(ctx, p, "step")
}

// AssignTask gives the pending task of a run to a person who may answer it, or takes it back from its holder when
// assignee is empty: the caller holds task:assign on the project of the run (an administrator of it or of a project
// above it).
func (e *Engine) AssignTask(ctx context.Context, id, assignee string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.pendingTask(ctx, id)
	if err != nil {
		return nil, err
	}
	who := authz.From(ctx)
	if ok, err := e.mayAssign(ctx, p, who); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%q may not assign the tasks of project %s: %w", who.Subject, p.Project, authz.ErrForbidden)
	}
	assignee = strings.TrimSpace(assignee)
	if assignee == p.Pending.Assignee {
		return p, nil
	}
	if assignee != "" {
		if err := e.mayAnswerTask(ctx, p, authz.Principal{Subject: assignee}); err != nil {
			return nil, fmt.Errorf("%s may not answer this task: %w", assignee, err)
		}
	}
	e.assign(ctx, p, assignee, who.Subject, TaskAssigned)
	return p, e.save(ctx, p, "step")
}

// TaskCandidates are those of the given subjects who may answer the pending task of a run, in their order: whom a
// project administrator may assign it to. The caller holds task:assign.
func (e *Engine) TaskCandidates(ctx context.Context, id string, subjects []string) ([]string, error) {
	p, err := e.pendingTask(ctx, id)
	if err != nil {
		return nil, err
	}
	who := authz.From(ctx)
	if ok, err := e.mayAssign(ctx, p, who); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%q may not assign the tasks of project %s: %w", who.Subject, p.Project, authz.ErrForbidden)
	}
	out := []string{}
	for _, s := range subjects {
		if s == "" || slices.Contains(out, s) {
			continue
		}
		if err := e.mayAnswerTask(ctx, p, authz.Principal{Subject: s}); err == nil {
			out = append(out, s)
		} else if !errors.Is(err, authz.ErrForbidden) {
			return nil, err
		}
	}
	return out, nil
}

// pendingTask reads a run waiting on a person for an input or an approval.
func (e *Engine) pendingTask(ctx context.Context, id string) (*Process, error) {
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if !assignable(p) {
		return nil, fmt.Errorf("process %s waits on no human task: %w", id, ErrInvalidState)
	}
	return p, nil
}

// assign records the holder of the pending task of p (empty: nobody holds it).
func (e *Engine) assign(ctx context.Context, p *Process, assignee, by, decision string) {
	t := p.Pending
	from := t.Assignee
	t.Assignee, t.AssignedBy, t.AssignedAt = assignee, "", time.Time{}
	if assignee != "" {
		t.AssignedBy, t.AssignedAt = by, e.clock()
		t.Declined = slices.DeleteFunc(t.Declined, func(s string) bool { return s == assignee })
	}
	data := map[string]any{"decision": decision, "assignee": assignee, "kind": t.Kind}
	if from != "" {
		data["from"] = from
	}
	e.journal(ctx, p, journal.Record{Kind: journal.KindTask, Step: t.Step, Action: t.Action, Actor: by, Data: data})
}
