package engine

import (
	"context"
	"fmt"

	"github.com/zimwip/goap/pkg/authz"
	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/graphapi"
	"github.com/zimwip/goap/pkg/journal"
)

// mergeTaskOf describes the merge a committed change waits for: the properties both sides changed apart, with the
// value of each side and the one proposed (the change's). The three-way merge settles the rest by itself.
func mergeTaskOf(ctx context.Context, g GraphPort, c domain.Change) (*MergeTask, error) {
	br, err := g.Branch(ctx, c.Namespace, c.Branch)
	if err != nil {
		return nil, err
	}
	plan, err := g.PlanMerge(ctx, c.Namespace, br.Name, br.Parent)
	if err != nil {
		return nil, err
	}
	t := &MergeTask{Namespace: domain.NamespaceOf(c.Namespace), From: plan.From, Into: plan.Into}
	for _, cand := range plan.Conflicting() {
		mc := MergeConflict{Node: string(cand.Node), Key: cand.Key, Deleted: cand.Deleted}
		for _, name := range cand.Conflicts {
			mc.Properties = append(mc.Properties, MergeProperty{Name: name, Base: cand.Base[name], Ours: cand.OursProps[name], Theirs: cand.TheirsProps[name], Proposed: cand.TheirsProps[name]})
		}
		t.Conflicts = append(t.Conflicts, mc)
	}
	if len(t.Conflicts) == 0 {
		return nil, fmt.Errorf("change %s is committed on %s but its merge into %s has no conflict to settle", c.ID, plan.From, plan.Into)
	}
	return t, nil
}

// ResolveMerge settles the conflicts of a TaskMerge (by node id: the properties to keep, or skip the node) and
// completes the merge of the change; the process goes on (the caller runs it). Conflicts that remain, because the
// target branch moved meanwhile, keep the process waiting with the new ones.
func (e *Engine) ResolveMerge(ctx context.Context, id string, resolutions map[domain.NodeID]graphapi.Resolution, comment string) (*Process, error) {
	defer e.lock(id)()
	p, err := e.Store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.Status != StatusWaiting || p.Pending == nil || p.Pending.Kind != TaskMerge {
		return nil, fmt.Errorf("process %s has no merge pending: %w", id, ErrInvalidState)
	}
	pend := p.Pending
	for _, c := range pend.Merge.Conflicts {
		if _, ok := resolutions[domain.NodeID(c.Node)]; !ok {
			return nil, fmt.Errorf("no resolution for %s: %w", c.Key, ErrInvalidState)
		}
	}
	by := authz.From(ctx).Subject
	if _, err := e.Graph.IntegrateChange(ctx, p.ChangeID, resolutions); err != nil {
		if cur, cerr := e.Graph.Change(ctx, p.ChangeID); cerr == nil && cur.Status == domain.ChangeCommitted {
			// the target moved: what is left to settle changed
			if t, terr := mergeTaskOf(ctx, e.Graph, cur); terr == nil {
				pend.Merge = t
				_ = e.save(ctx, p, "step")
			}
		}
		return nil, err
	}
	if pend.Step < len(p.Steps) {
		st := &p.Steps[pend.Step]
		st.Output = fmt.Sprintf("merged into %s by %s", pend.Merge.Into, by)
	}
	p.Pending, p.Status = nil, StatusRunning
	e.queue(ctx, p, "merged", map[string]any{"into": pend.Merge.Into})
	e.journal(ctx, p, journal.Record{Kind: journal.KindApproval, Step: pend.Step, Action: pend.Action, Actor: by,
		Data: map[string]any{"decision": "merged", "comment": comment, "from": pend.Merge.From, "into": pend.Merge.Into, "nodes": len(pend.Merge.Conflicts)}})
	return p, e.save(ctx, p, "step")
}
