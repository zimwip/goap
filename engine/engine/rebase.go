package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/zimwip/goap/contracts/authz"
	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/journal"
)

// target names what the change was rebased onto, for the description of the task.
func (t *RebaseTask) target() string {
	switch {
	case t.Onto != "" && t.Branch != "":
		return t.Branch + " (" + t.Onto + ")"
	case t.Onto != "":
		return t.Onto
	case t.Branch != "":
		return t.Branch
	}
	return "its target"
}

// rebaseTaskOf describes the conflicts the rebase of a change left to settle (ADR 0101); nil when there are none (the
// refusal of the apply is then something else: impacts awaiting their review, for instance).
func rebaseTaskOf(ctx context.Context, g GraphPort, id domain.ChangeID) *RebaseTask {
	st, err := g.RebaseState(ctx, id)
	if err != nil || len(st.Conflicts) == 0 {
		return nil
	}
	t := &RebaseTask{Branch: st.Branch, Onto: string(st.Onto)}
	if st.Parent != "" {
		t.Onto = string(st.Parent)
		if t.Branch == "" {
			t.Branch = "parent change"
		}
	}
	for _, c := range st.Conflicts {
		t.Impacts = append(t.Impacts, RebaseConflict{Impact: string(c.Impact), Key: c.Key, Conflicts: c.Conflicts})
	}
	return t
}

// ResolveRebase continues a process waiting on a TaskRebase once the person settled the conflicts and reviewed the
// impacts again (in the change tab); the apply then runs again. It refuses while a conflict or an impact awaiting its
// review remains.
func (e *Engine) ResolveRebase(ctx context.Context, id, comment string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskRebase {
		return nil, fmt.Errorf("process %s has no rebase pending: %w", id, ErrInvalidState)
	}
	pend := p.Pending
	st, err := e.Graph.RebaseState(ctx, p.ChangeID)
	if err != nil {
		return nil, err
	}
	if len(st.Conflicts) > 0 {
		var names []string
		for _, c := range st.Conflicts {
			names = append(names, fmt.Sprintf("%s (%s)", c.Key, strings.Join(c.Conflicts, ", ")))
		}
		return nil, fmt.Errorf("conflicts remain to settle: %s: %w", strings.Join(names, "; "), ErrInvalidState)
	}
	ch, err := e.Graph.Change(ctx, p.ChangeID)
	if err != nil {
		return nil, err
	}
	var open []string
	for _, cn := range ch.Nodes {
		if cn.Flow == "" && !cn.Superseded && cn.Review == domain.ReviewProposed {
			open = append(open, cn.Key)
		}
	}
	if len(open) > 0 {
		return nil, fmt.Errorf("impacts still await their review: %s: %w", strings.Join(open, ", "), ErrInvalidState)
	}
	by := authz.From(ctx).Subject
	if pend.Step < len(p.Steps) {
		p.Steps[pend.Step].Output = fmt.Sprintf("rebase settled by %s", by)
	}
	p.Pending, p.Status = nil, StatusRunning
	e.queue(ctx, p, "rebased", map[string]any{"impacts": len(pend.Rebase.Impacts)})
	e.journal(ctx, p, journal.Record{Kind: journal.KindApproval, Step: pend.Step, Action: pend.Action, Actor: by,
		Data: map[string]any{"decision": "rebase-settled", "comment": comment, "impacts": len(pend.Rebase.Impacts)}})
	return p, e.save(ctx, p, "step")
}
