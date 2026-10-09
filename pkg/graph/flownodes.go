package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/changeapi"
	"github.com/zimwip/goap/pkg/domain"
)

// This file is the part of the flows (ADR 0017) that concerns change nodes (ADR 0025, 0079): a flow keeps its own drafts
// of the nodes it checks out, forked from what it sees; what the relaunched steps wrote is stale; adopting the flow
// installs its drafts on the main flow, and resets the drafts the stale runs wrote to what the others left.

func flowBranchName(flow string) string { return changeapi.FlowBranchName(flow) }

// IsWorking is changeapi.IsWorking.
func IsWorking(c domain.Change, flow string, n domain.Node) bool {
	return changeapi.IsWorking(c, flow, n)
}

// flowChain lists the flows from f up to the main flow (excluded), innermost first.
func flowChain(c domain.Change, flow string) []domain.Flow {
	var out []domain.Flow
	for hops := 0; flow != "" && hops < 64; hops++ {
		f, ok := c.Flow(flow)
		if !ok {
			break
		}
		out = append(out, f)
		flow = f.Parent
	}
	return out
}

// staleOf are the action runs invalidated on the chain of a flow.
func staleOf(chain []domain.Flow) map[string]bool {
	out := map[string]bool{}
	for _, f := range chain {
		for _, e := range f.StaleRuns {
			out[e] = true
		}
	}
	return out
}

// flowNodes is what the process running on a flow sees of the change impacts.
type flowNodes struct {
	g      *Graph
	tx     Tx
	c      domain.Change
	flow   string
	chain  []domain.Flow
	inFlow map[string]bool
	stale  map[string]bool
}

func (g *Graph) newFlowNodes(tx Tx, c domain.Change, flow string) *flowNodes {
	v := &flowNodes{g: g, tx: tx, c: c, flow: flow, chain: flowChain(c, flow), inFlow: map[string]bool{}}
	for _, f := range v.chain {
		v.inFlow[f.ID] = true
	}
	v.stale = staleOf(v.chain)
	return v
}

func (v *flowNodes) isStale(execution string) bool { return execution != "" && v.stale[execution] }

// visible tells whether a stored change impact exists for the flow.
func (v *flowNodes) visible(cn domain.ChangeImpact) bool {
	if cn.Superseded {
		return false
	}
	if cn.Flow != "" && !v.inFlow[cn.Flow] {
		return false
	}
	if v.flow != "" && v.isStale(cn.Execution) {
		return false
	}
	return true
}

func nodeOf(cn domain.ChangeImpact) domain.NodeID {
	switch {
	case cn.Post != nil:
		return cn.Post.ID
	case cn.Pre != nil:
		return cn.Pre.ID
	}
	return ""
}

// nodes returns the change impacts the flow sees, with the post version and the review it resolves to: a fold of the
// impact log (ADR 0029 §3).
func (v *flowNodes) nodes(ctx context.Context) ([]domain.ChangeImpact, error) {
	if v.flow == "" {
		return domain.ImpactsSeenBy(v.c.Nodes, nil, nil, v.stale), nil
	}
	events, err := impactEvents(ctx, v.tx, v.c.ID)
	if err != nil {
		return nil, err
	}
	chain := make([]string, len(v.chain))
	for i, f := range v.chain {
		chain[i] = f.ID
	}
	return domain.ImpactsSeenBy(v.c.Nodes, events, chain, v.stale), nil
}

// find returns the change impact a flow sees under an id.
func (v *flowNodes) find(ctx context.Context, id domain.ChangeImpactID) (domain.ChangeImpact, error) {
	list, err := v.nodes(ctx)
	if err != nil {
		return domain.ChangeImpact{}, err
	}
	for _, cn := range list {
		if cn.ID == id {
			return cn, nil
		}
	}
	return domain.ChangeImpact{}, fmt.Errorf("change impact %s is not on the flow %q of change %s: %w", id, v.flow, v.c.ID, ErrNotFound)
}

// adoptNodes installs, on the main flow, what the adopted flow saw of the nodes (ADR 0025 §5, 0079). For each change
// impact: when the flow holds a draft of the node it becomes the main flow's (a conflict when the main flow's moved on
// since the flow checked the node out, by a run that is not stale); else when the main flow's draft was written in part
// by stale runs, it is reset to what the other runs left, or dropped when they left nothing. The change impacts of the
// flow become the change's, the stale ones are superseded (their drafts go with them).
func (g *Graph) adoptNodes(ctx context.Context, tx Tx, c domain.Change, f domain.Flow, by string) error {
	stale := map[string]bool{}
	for _, e := range f.StaleRuns {
		stale[e] = true
	}
	isStale := func(e string) bool { return e != "" && stale[e] }
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	events, err := impactEvents(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	var installs []domain.ImpactEvent
	for _, cn := range c.Nodes {
		fd, md := ownDraft(rows, cn.ID, f.ID), ownDraft(rows, cn.ID, "")
		switch {
		case fd != nil:
			if !acceptedOnFlow(c, nodeOf(cn), f.ID) {
				return fmt.Errorf("node %s is a draft on flow %s that the flow did not accept: accept it, or cancel the checkout, before the flow is adopted (ADR 0079): %w", fd.Key, f.ID, ErrConflict)
			}
			// the flow was forked from the node as it was: it is a conflict when the main flow moved on since, by a run that is
			// not stale
			seq := 0
			for _, e := range events {
				if e.Impact != cn.ID {
					continue
				}
				switch {
				case e.Flow == f.ID && e.StartsDraft() && seq == 0:
					seq = e.Seq
				case seq > 0 && e.Flow == "" && e.Seq > seq && changesDraft(e) && !isStale(e.Execution):
					return fmt.Errorf("node %s was changed on the main flow since the flow forked (checked out again in the meantime): %w", fd.Key, ErrConflict)
				}
			}
			ref := fd.Ref()
			inst := fd.Clone()
			installs = append(installs, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactTransitioned, Execution: fd.Execution, By: by, Post: &ref, Draft: &inst,
				Patch: map[string]any{"adopted": f.ID}})
		case md != nil && !(cn.Flow == "" && !cn.Superseded && isStale(cn.Execution)):
			// the main flow's draft, without what the stale runs wrote
			kept := domain.DraftSeenBy(events, cn.ID, nil, isStale)
			switch {
			case kept == nil:
				installs = append(installs, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactCancelled, By: by})
			case !sameDraft(*kept, *md):
				ref := kept.Ref()
				installs = append(installs, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactTransitioned, Execution: kept.Execution, By: by, Post: &ref, Draft: kept,
					Patch: map[string]any{"adopted": f.ID}})
			}
		}
	}
	return g.adoptChangeImpacts(ctx, tx, c, f, installs, by)
}

// changesDraft reports an event that changes what a flow holds of a node (not a review).
func changesDraft(e domain.ImpactEvent) bool {
	switch e.Op {
	case domain.ImpactCreated, domain.ImpactCheckedOut, domain.ImpactUpdated, domain.ImpactTransitioned, domain.ImpactCancelled:
		return true
	}
	return false
}

// sameDraft reports drafts that hold the same: the flow, the run and the ids of the links are not what a draft holds.
func sameDraft(a, b domain.Draft) bool {
	norm := func(d domain.Draft) []byte {
		d = d.Clone()
		d.Flow, d.Execution = "", ""
		for i := range d.Links {
			d.Links[i].ID = ""
		}
		b, _ := json.Marshal(d)
		return b
	}
	return string(norm(a)) == string(norm(b))
}

// adoptChangeImpacts moves the change impacts and reviews of an adopted flow to the main flow (ADR 0025 §5.3): one
// adopted event, folded over every change impact, then the drafts it installs on the main flow (ADR 0029, 0079). An
// install for an impact the adoption supersedes is left out.
func (g *Graph) adoptChangeImpacts(ctx context.Context, tx Tx, c domain.Change, f domain.Flow, installs []domain.ImpactEvent, by string) error {
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Op: domain.ImpactAdopted, Flow: f.ID, Stale: f.StaleRuns, By: by}); err != nil {
		return err
	}
	impacts, err := tx.ChangeImpacts(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, e := range installs {
		i := slices.IndexFunc(impacts, func(cn domain.ChangeImpact) bool { return cn.ID == e.Impact })
		if i < 0 || impacts[i].Superseded {
			continue
		}
		if err := g.emit(ctx, tx, e); err != nil {
			return err
		}
	}
	return nil
}

// discardNodes rejects the change impacts a discarded flow and its descendants declared. What they drafted stays in
// the log, for the audit; nothing sees it any more.
func (g *Graph) discardNodes(ctx context.Context, tx Tx, c domain.Change, f domain.Flow, by string) error {
	gone := map[string]bool{f.ID: true}
	for changed := true; changed; {
		changed = false
		for _, o := range c.Flows() {
			if !gone[o.ID] && gone[o.Parent] {
				gone[o.ID], changed = true, true
			}
		}
	}
	for _, cn := range c.Nodes {
		if !gone[cn.Flow] || cn.Review == domain.ReviewRejected {
			continue
		}
		r := domain.Review{Status: domain.ReviewRejected, By: by, Comment: "flow discarded", At: g.now(), Flow: cn.Flow}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactDiscarded, Flow: cn.Flow, By: by, Review: &r}); err != nil {
			return err
		}
	}
	return nil
}

// acceptedOnFlow reports whether the last review of a node of the change made on a flow accepts it (ADR 0079: a draft
// is not written until the change lands, so adopting a flow asks its acceptance).
func acceptedOnFlow(c domain.Change, node domain.NodeID, flow string) bool {
	accepted := false
	for _, cn := range c.Nodes {
		if nodeOf(cn) != node {
			continue
		}
		for _, r := range cn.Reviews {
			if r.Flow == flow && !r.Superseded {
				accepted = r.Status == domain.ReviewAccepted
			}
		}
	}
	return accepted
}
