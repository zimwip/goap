package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/zimwip/goap/contracts/authz"
)

// Cancel stops a process that has not ended: whoever answers for the run may (as for Unblock: its initiator or the
// initiator of a run above it, the accountable role of its step, a holder of process:unblock). A run in flight is
// interrupted (its model and tool calls end with their context) and Cancel waits for it to let go; the process is
// then cancelled, with what it waited for dropped, and so are its live sub-processes. A parent waiting on a cancelled
// sub-process resumes, its step ending like a failed one.
func (e *Engine) Cancel(ctx context.Context, id, reason string) (*Process, error) {
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status.Terminal() {
		return nil, fmt.Errorf("process %s has ended (%s): %w", id, p.Status, ErrInvalidState)
	}
	who := authz.From(ctx)
	if ok, err := e.mayUnblock(ctx, p, who); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%q does not answer for process %s: %w", who.Subject, id, authz.ErrForbidden)
	}
	return e.cancel(ctx, id, who.Subject, strings.TrimSpace(reason))
}

func (e *Engine) cancel(ctx context.Context, id, by, reason string) (*Process, error) {
	e.cancelling.Store(id, by)
	defer e.cancelling.Delete(id)
	e.interrupt(ctx, id, by)
	unlock := e.lock(id)
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		unlock()
		return nil, err
	}
	if p.Status.Terminal() { // it ended on its own meanwhile
		unlock()
		return p, nil
	}
	p.Status, p.Pending, p.Plan, p.Queued = StatusCancelled, nil, nil, nil
	p.Error = "cancelled by " + by
	if reason != "" {
		p.Error += ": " + reason
	}
	// the parent is cancelled first: a sub-process ending wakes a parent that still waits on it
	err = e.save(ctx, p, string(StatusCancelled))
	unlock()
	if err != nil {
		return p, err
	}
	all, err := e.Store.List(ctx)
	if err != nil {
		return p, err
	}
	for _, c := range all {
		if c.ParentID == id && !c.Status.Terminal() {
			if _, err := e.cancel(ctx, c.ID, by, "its parent process was cancelled"); err != nil {
				return p, fmt.Errorf("sub-process %s: %w", c.ID, err)
			}
		}
	}
	return p, nil
}

// cancelRequested reports whether Cancel was asked for the process.
func (e *Engine) cancelRequested(id string) bool {
	_, ok := e.cancelling.Load(id)
	return ok
}

// runHandle is a run in flight (a pointer: sync.Map compares the values it deletes).
type runHandle struct{ cancel context.CancelFunc }
