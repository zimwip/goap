package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// ValidateBoard checks the consistency of the blackboard as a process on a flow
// sees it: every fact of the view is structurally valid, the facts it builds on
// exist and are in effect, and the change impacts it holds are coherent (their pre
// version is in the reference baseline, they are not waiting to be re-checked).
// The issues come in log order; each names the item (or change impact) to blame (the
// culprit). Only the outdated change impacts are warnings: they are fixed by
// re-checking the impact, not by relaunching a step.
func (g *Graph) ValidateBoard(ctx context.Context, id domain.ChangeID, flow string) (out []domain.BoardIssue, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		out, err = g.validateBoard(ctx, tx, c, flow)
		return err
	})
	return
}

func (g *Graph) validateBoard(ctx context.Context, tx Tx, c domain.Change, flow string) ([]domain.BoardIssue, error) {
	nodes, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
	if err != nil {
		return nil, err
	}
	v := c.View(flow)
	base, err := tx.Baseline(ctx, c.BaselineID)
	if err != nil {
		return nil, err
	}
	byID := map[domain.ItemID]domain.ChangeItem{}
	for _, it := range v.Items {
		byID[it.ID] = it
	}
	var out []domain.BoardIssue
	add := func(item, culprit domain.ItemID, code, msg string) {
		if culprit == "" {
			culprit = item
		}
		sev := domain.IssueError
		if code == "outdated" {
			// relaunching a step cannot fix it (the reference baseline stays the same): the impact is
			// re-checked, so it is reported without blocking the run
			sev = domain.IssueWarning
		}
		out = append(out, domain.BoardIssue{Item: item, Culprit: culprit, Code: code, Message: msg, Severity: sev})
	}
	for _, it := range v.Items {
		if it.Kind == domain.KindFlow || !v.InEffect(it.ID) {
			continue
		}
		if err := it.Validate(); err != nil {
			add(it.ID, "", "structure", err.Error())
		}
		// provenance and structure references
		var refs []domain.ItemID
		refs = append(refs, it.DerivedFrom...)
		if d := it.Decision; d != nil {
			// a decision may target an item that is out of effect (it is what rejected it): it only has to exist
			if _, ok := byID[d.Item]; !ok {
				add(it.ID, "", "dangling", fmt.Sprintf("decides on item %s, which is not on the blackboard", d.Item))
			}
		}
		for _, r := range slices.Compact(refs) {
			_, ok := byID[r]
			switch {
			case !ok:
				add(it.ID, "", "dangling", fmt.Sprintf("refers to item %s, which is not on the blackboard", r))
			case !v.InEffect(r):
				add(it.ID, r, "derived_from_invalid", fmt.Sprintf("builds on item %s, which is %s", r, v.EffectiveStatus(r)))
			}
		}
	}
	// the change impacts: the id of a change impact stands where an item id does
	for _, cn := range nodes {
		id := domain.ItemID(cn.ID)
		if err := cn.Validate(); err != nil {
			add(id, "", "structure", err.Error())
		}
		if cn.Pre != nil && !base.Contains(*cn.Pre) && !cn.Recheck {
			add(id, "", "reference", fmt.Sprintf("refers to %s which is not in the reference baseline", cn.Pre))
		}
		if cn.Recheck {
			add(id, "", "outdated", fmt.Sprintf("the impact on %s was written against a version that is no longer the head", cn.Key))
		}
	}
	// log order: the facts, then the change impacts
	pos := map[domain.ItemID]int{}
	for i, it := range v.Items {
		pos[it.ID] = i
	}
	slices.SortStableFunc(out, func(a, b domain.BoardIssue) int { return pos[a.Item] - pos[b.Item] })
	return out, nil
}
