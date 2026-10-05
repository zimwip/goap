package engine

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/goap"
	"github.com/zimwip/goap/pkg/journal"
	"github.com/zimwip/goap/pkg/risk"
)

// Unblock decisions (ADR 0036 §3): a run waiting for conditions established outside it, or stuck, is never left
// without a way out.
const (
	// UnblockWaive declares conditions established for the run: a waiver item on the change, with its reason.
	UnblockWaive = "waive"
	// UnblockRetry tries again the actions the run gave up on (disabled after failures or a rejection).
	UnblockRetry = "retry"
	// UnblockAbandon ends the run: it fails, with the reason (its parent step fails with it).
	UnblockAbandon = "abandon"
)

// UnblockRequest is a person's decision on a blocked run.
type UnblockRequest struct {
	Decision string
	// Conditions declared established (UnblockWaive): "name" true, "!name" false; conditions of the methodology.
	Conditions []string
	Reason     string
}

// PermissionUnblock lets a principal unblock runs beyond those who answer for a run (by default, process:* of the
// members of the run's project, ADR 0043).
const PermissionUnblock = "process:unblock"

// Unblock decides on a run that waits for conditions established outside it, or is stuck. Whoever answers for the run
// may: the initiator of the run or of a run above it, the accountable role of the step it carries out, or a holder of
// process:unblock (by default the members of its project, the administrators). Call Run (or schedule) afterwards to
// continue.
func (e *Engine) Unblock(ctx context.Context, id string, req UnblockRequest) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusStuck && !p.WaitsForConditions() {
		return nil, fmt.Errorf("process %s is neither stuck nor waiting for conditions: %w", id, ErrInvalidState)
	}
	who := authz.From(ctx)
	if ok, err := e.mayUnblock(ctx, p, who); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("%q does not answer for process %s: %w", who.Subject, id, authz.ErrForbidden)
	}
	reason := strings.TrimSpace(req.Reason)
	data := map[string]any{"decision": req.Decision, "reason": reason, "status": string(p.Status)}
	switch req.Decision {
	case UnblockWaive:
		if p.ChangeID == "" {
			return nil, fmt.Errorf("process %s has no change to record the waiver on: %w", id, ErrInvalidState)
		}
		if reason == "" || len(req.Conditions) == 0 {
			return nil, fmt.Errorf("a waiver names the conditions and gives its reason: %w", ErrInvalidState)
		}
		m, err := e.Methodologies.Methodology(ctx, p.Methodology)
		if err != nil {
			return nil, err
		}
		for _, c := range req.Conditions {
			if name := strings.TrimPrefix(c, "!"); !m.Conditions.Has(name) {
				return nil, fmt.Errorf("unknown condition %q: %w", name, ErrInvalidState)
			}
		}
		rec := uuid.NewString()
		if _, err := e.addPlainItems(ctx, p, []ItemInput{{Kind: string(risk.KindWaiver),
			Data: map[string]any{"process": p.ID, "conditions": slices.Clone(req.Conditions), "reason": reason, "by": who.Subject}}}, "unblock", rec); err != nil {
			return nil, err
		}
		data["conditions"] = slices.Clone(req.Conditions)
		e.journal(ctx, p, journal.Record{ID: rec, Kind: journal.KindUnblock, Step: len(p.Steps), Actor: who.Subject, Data: data})
	case UnblockRetry:
		data["disabled"] = slices.Sorted(maps.Keys(p.Disabled))
		p.Disabled = map[string]bool{}
		e.journal(ctx, p, journal.Record{Kind: journal.KindUnblock, Step: len(p.Steps), Actor: who.Subject, Data: data})
	case UnblockAbandon:
		if reason == "" {
			return nil, fmt.Errorf("abandoning a run gives its reason: %w", ErrInvalidState)
		}
		e.journal(ctx, p, journal.Record{Kind: journal.KindUnblock, Step: len(p.Steps), Actor: who.Subject, Data: data})
		p.Pending, p.Plan = nil, nil
		p.Status, p.Error = StatusFailed, fmt.Sprintf("abandoned by %s: %s", who.Subject, reason)
		return p, e.save(ctx, p, string(StatusFailed))
	default:
		return nil, fmt.Errorf("unknown decision %q (waive, retry or abandon): %w", req.Decision, ErrInvalidState)
	}
	p.Status, p.Error, p.Plan, p.Pending = StatusRunning, "", nil, nil
	e.queue(ctx, p, "unblocked", map[string]any{"decision": req.Decision, "by": who.Subject})
	return p, e.save(ctx, p, "step")
}

// mayUnblock reports whether who answers for the run p.
func (e *Engine) mayUnblock(ctx context.Context, p *Process, who authz.Principal) (bool, error) {
	// the initiator of the run, or of a run above it (the owner of the work)
	for q, depth := p, 0; q != nil && depth < 16; depth++ {
		if who.Subject != "" && q.Initiator.Subject == who.Subject {
			return true, nil
		}
		if q.ParentID == "" {
			break
		}
		parent, err := e.Store.Get(ctx, q.ParentID)
		if err != nil {
			break
		}
		q = parent
	}
	// the accountable role of the step the run carries out
	if sc := p.Step; sc != nil && sc.Roles != nil && sc.Roles.Accountable != "" {
		if ok, err := e.stepAllowed(ctx, p, who, sc, "approve"); err != nil || ok {
			return ok, err
		}
	}
	return e.allowed(ctx, p, who, PermissionUnblock)
}

// unblocking are the conditions that would unblock a stuck process if a person declared them established: those no
// admissible action establishes, when assuming them lets the admissible actions reach the goal; else the goal's own
// conditions that do not hold.
func (e *Engine) unblocking(world goap.WorldState, admissible []goap.Action, goal goap.Goal) []string {
	if out := e.awaited(world, admissible, admissible, goal); len(out) > 0 {
		return out
	}
	var out []string
	for k, v := range goal.Pre {
		if have, ok := world[k]; !ok || have != v {
			out = append(out, map[bool]string{true: k, false: "!" + k}[v])
		}
	}
	slices.Sort(out)
	return out
}
