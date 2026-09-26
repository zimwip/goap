package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
)

// issuesKey identifies a set of blackboard issues: a human who chose to ignore
// them is not asked again until the set changes.
func issuesKey(is []domain.BoardIssue) string {
	keys := make([]string, len(is))
	for i, x := range is {
		keys[i] = string(x.Item) + "|" + string(x.Culprit) + "|" + x.Code
	}
	slices.Sort(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return hex.EncodeToString(sum[:8])
}

// checkBoard validates the blackboard the process reads before it asks for an
// action. On errors the process waits (TaskBoard) with the issues and the
// earliest step to relaunch. It reports whether the process is blocked. A
// validation failure never blocks the process.
func (e *Engine) checkBoard(ctx context.Context, p *Process, bb domain.Blackboard) (bool, error) {
	if p.ChangeID == "" {
		return false, nil
	}
	all, err := e.Graph.ValidateBoard(ctx, p.ChangeID, p.Flow)
	if err != nil {
		e.log().Warn("blackboard validation failed", "process", p.ID, "err", err)
		return false, nil
	}
	var errs []domain.BoardIssue
	for _, i := range all {
		if i.Severity == domain.IssueError {
			errs = append(errs, i)
		}
	}
	if len(errs) == 0 || p.Dismissed[issuesKey(errs)] {
		return false, nil
	}
	prop := e.proposeRelaunch(ctx, p, bb.Change, errs)
	desc := fmt.Sprintf("The blackboard is inconsistent (%d issue(s)): %s", len(errs), errs[0].Message)
	if prop != nil {
		desc += fmt.Sprintf(". Restart from step %d (%s) of process %s.", prop.Step, prop.Action, prop.Process)
	}
	p.Status, p.Plan = StatusWaiting, nil
	p.Pending = &HumanTask{Kind: TaskBoard, Action: "validate_board", Step: len(p.Steps), Description: desc, Issues: errs, Proposal: prop}
	e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecTick, Step: len(p.Steps),
		Data: map[string]any{"boardInvalid": len(errs), "issues": errs, "proposal": prop}})
	return true, nil
}

// proposeRelaunch finds the earliest step, over every run of the change, that
// produced content at fault: the step to restart from. It is nil when no step
// can be relaunched (content of a human or trigger, a flow already open).
func (e *Engine) proposeRelaunch(ctx context.Context, p *Process, view domain.ChangeSet, issues []domain.BoardIssue) *RelaunchProposal {
	execOf := map[domain.ItemID]string{}
	for _, it := range view.Items {
		execOf[it.ID] = it.Execution
	}
	procs, err := e.Store.List(ctx)
	if err != nil {
		return nil
	}
	byID := map[string]*Process{}
	type slot struct {
		p *Process
		i int
	}
	byExec := map[string]slot{}
	for _, q := range procs {
		if q.ChangeID != p.ChangeID {
			continue
		}
		byID[q.ID] = q
		for i, s := range q.Steps {
			if s.Execution != "" {
				byExec[s.Execution] = slot{q, i}
			}
		}
	}
	var best *RelaunchProposal
	var bestAt int64
	culprits := map[domain.ItemID]bool{}
	for _, is := range issues {
		exec := execOf[is.Culprit]
		s, ok := byExec[exec]
		if !ok {
			continue
		}
		top, idx := s.p, s.i
		for top.ParentID != "" { // a sub-agent step: restart the step of the parent that started it
			parent, ok := byID[top.ParentID]
			if !ok {
				top = nil
				break
			}
			j := slices.IndexFunc(parent.Steps, func(st Step) bool { return slices.Contains(st.Children, top.ID) })
			if j < 0 {
				top = nil
				break
			}
			top, idx = parent, j
		}
		if top == nil || top.Status == StatusSuperseded || top.Status == StatusClarifying || (top.Status == StatusRunning && top.ID != p.ID) {
			continue
		}
		at := top.Steps[idx].StartedAt.UnixNano()
		if best == nil || at < bestAt {
			best, bestAt = &RelaunchProposal{Process: top.ID, Step: idx, Action: top.Steps[idx].Action}, at
			culprits = map[domain.ItemID]bool{}
		}
		if best.Process == top.ID && best.Step == idx {
			culprits[is.Culprit] = true
		}
	}
	if best == nil {
		return nil
	}
	best.Culprits = slices.Sorted(func(yield func(domain.ItemID) bool) {
		for c := range culprits {
			if !yield(c) {
				return
			}
		}
	})
	best.Reason = "blackboard inconsistent: " + issues[0].Message
	return best
}

// ResolveBoard answers a blackboard inconsistency: relaunch the proposed step (returns the new
// process, to run) or ignore the issues and go on (the process is running again, to run).
func (e *Engine) ResolveBoard(ctx context.Context, id string, relaunch bool, comment string) (*Process, *Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskBoard {
		return nil, nil, fmt.Errorf("process %s has no blackboard issue pending: %w", id, ErrInvalidState)
	}
	pend := p.Pending
	by := authz.From(ctx).Subject
	if !relaunch {
		if p.Dismissed == nil {
			p.Dismissed = map[string]bool{}
		}
		p.Dismissed[issuesKey(pend.Issues)] = true
		p.Pending, p.Status = nil, StatusRunning
		e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecApproval, Step: pend.Step, Action: "validate_board", Actor: by,
			Data: map[string]any{"decision": "ignored", "comment": comment, "issues": len(pend.Issues)}})
		return p, nil, e.save(ctx, p, "step")
	}
	prop := pend.Proposal
	if prop == nil {
		return nil, nil, fmt.Errorf("no step can be relaunched to fix the blackboard of process %s: %w", id, ErrInvalidState)
	}
	var np *Process
	if prop.Process == p.ID {
		np, err = e.relaunchLocked(ctx, p.ID, prop.Step, prop.Reason, comment)
	} else {
		np, err = e.Relaunch(ctx, prop.Process, prop.Step, prop.Reason, comment)
	}
	if err != nil {
		return nil, nil, err
	}
	p.Pending = &HumanTask{Kind: TaskRelaunched, Action: "validate_board", Step: pend.Step, Issues: pend.Issues, FlowID: np.Flow,
		Description: fmt.Sprintf("Relaunched from step %d (%s) as process %s: waiting for the decision on flow %s.", prop.Step, prop.Action, np.ID, np.Flow)}
	e.journal(ctx, p, domain.ExecutionRecord{Kind: domain.ExecApproval, Step: pend.Step, Action: "validate_board", Actor: by,
		Data: map[string]any{"decision": "relaunched", "comment": comment, "process": prop.Process, "fromStep": prop.Step, "flow": np.Flow}})
	return p, np, e.save(ctx, p, "step")
}

// resumeWaiting resumes the processes that waited for the decision on p's flow
// to fix their blackboard. When the flow was discarded they go on regardless:
// the issues are marked as ignored.
func (e *Engine) resumeWaiting(ctx context.Context, p *Process, discarded bool) {
	procs, err := e.Store.List(ctx)
	if err != nil {
		return
	}
	for _, q := range procs {
		if q.ID == p.ID || q.ChangeID != p.ChangeID || q.Status != StatusWaiting || q.Pending == nil || q.Pending.Kind != TaskRelaunched || q.Pending.FlowID != p.Flow {
			continue
		}
		q := q
		func() {
			defer e.lock(q.ID)()
			cur, err := e.Store.Get(ctx, q.ID)
			if err != nil || cur.Status != StatusWaiting || cur.Pending == nil || cur.Pending.Kind != TaskRelaunched {
				return
			}
			if discarded {
				if cur.Dismissed == nil {
					cur.Dismissed = map[string]bool{}
				}
				cur.Dismissed[issuesKey(cur.Pending.Issues)] = true
			}
			cur.Pending, cur.Status = nil, StatusRunning
			if err := e.save(ctx, cur, "step"); err != nil {
				e.log().Warn("resume after flow decision", "process", cur.ID, "err", err)
				return
			}
			e.schedule(cur.ID)
		}()
	}
}
