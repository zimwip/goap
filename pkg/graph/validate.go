package graph

import (
	"context"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// ValidateBoard checks the consistency of the blackboard as a process on a flow
// sees it: every item of the view is structurally valid, the items it refers
// to exist and are in effect, the nodes it refers to are in the reference
// baseline and have not moved, the proposals obey the lifecycle and namespace
// rules, impacts have a coherent pre and post, and no node is created twice.
// The issues come in log order; each names the item to blame (the culprit).
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

func (g *Graph) validateBoard(ctx context.Context, tx Tx, c domain.ChangeSet, flow string) ([]domain.BoardIssue, error) {
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
		out = append(out, domain.BoardIssue{Item: item, Culprit: culprit, Code: code, Message: msg, Severity: domain.IssueError})
	}
	seenKeys := map[string]domain.ItemID{}
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
		for _, e := range endpointsOf(it) {
			if e.Item != "" {
				refs = append(refs, e.Item)
			}
		}
		if d := it.Decision; d != nil {
			// a decision may target an item that is out of effect (it is what rejected it): it only has to exist
			if _, ok := byID[d.Item]; !ok {
				add(it.ID, "", "dangling", fmt.Sprintf("decides on item %s, which is not on the blackboard", d.Item))
			}
		}
		if it.Post != nil && it.Post.Item != "" {
			refs = append(refs, it.Post.Item)
		}
		for _, r := range slices.Compact(refs) {
			ref, ok := byID[r]
			switch {
			case !ok:
				add(it.ID, "", "dangling", fmt.Sprintf("refers to item %s, which is not on the blackboard", r))
			case !v.InEffect(r):
				add(it.ID, r, "derived_from_invalid", fmt.Sprintf("builds on item %s, which is %s", r, v.EffectiveStatus(r)))
			default:
				_ = ref
			}
		}
		// nodes must be part of the reference baseline
		for _, r := range refsOf(it) {
			if !base.Contains(r) {
				add(it.ID, "", "reference", fmt.Sprintf("refers to %s which is not in the reference baseline", r))
			}
		}
		// the same node created twice
		if p := it.Proposal; p != nil && p.Op == domain.OpCreateNode && p.Node != nil && p.Node.Key != "" {
			k := firstNonEmpty(p.Node.Namespace, c.Namespace) + "/" + p.Node.Key
			if first, dup := seenKeys[k]; dup {
				add(it.ID, first, "duplicate", fmt.Sprintf("creates %s, already created by item %s", p.Node.Key, first))
			} else {
				seenKeys[k] = it.ID
			}
		}
		if err := g.checkImpact(ctx, tx, base, byID, it); err != nil {
			add(it.ID, "", "impact", err.Error())
		}
	}
	// nodes that moved since the version the proposals are based on
	ds, err := divergences(ctx, tx, v)
	if err != nil {
		return nil, err
	}
	for _, d := range ds {
		add(d.Item, "", "outdated", fmt.Sprintf("based on %s, but %s is now at %s", d.Base, d.Base.ID, d.Head))
	}
	// lifecycle, namespace and link rules: one replay, then locate the failing proposals
	if _, err := g.walk(ctx, tx, v, false); err != nil {
		bad, err := g.failingProposals(ctx, tx, v)
		if err != nil {
			return nil, err
		}
		for _, b := range bad {
			add(b.item, "", "rule", b.msg)
		}
	}
	// log order
	pos := map[domain.ItemID]int{}
	for i, it := range v.Items {
		pos[it.ID] = i
	}
	slices.SortStableFunc(out, func(a, b domain.BoardIssue) int { return pos[a.Item] - pos[b.Item] })
	return out, nil
}

type failing struct {
	item domain.ItemID
	msg  string
}

// failingProposals replays the proposals one by one and reports the ones the
// rules refuse (a proposal that fails is left out of the following replays).
func (g *Graph) failingProposals(ctx context.Context, tx Tx, v domain.ChangeSet) ([]failing, error) {
	var out []failing
	skipped := map[domain.ItemID]bool{}
	for i, it := range v.Items {
		if it.Kind != domain.KindProposal || !v.InEffect(it.ID) {
			continue
		}
		prefix := v
		prefix.Items = nil
		for _, o := range v.Items[:i+1] {
			if !skipped[o.ID] {
				prefix.Items = append(prefix.Items, o)
			}
		}
		if _, err := g.walk(ctx, tx, prefix, false); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			skipped[it.ID] = true
			out = append(out, failing{item: it.ID, msg: err.Error()})
		}
	}
	return out, nil
}
