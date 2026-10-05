package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// This file is the part of the flow branches (ADR 0017) that concerns change
// nodes (ADR 0025): a flow is a graph branch forked from the change branch; what
// the relaunched steps wrote is stale; adopting the flow makes the change
// branch equal to what the flow sees.

func flowBranchName(flow string) string { return "flow-" + shortID(flow) }

// IsWorking reports whether a version is the working version of the flow of a change (ADR 0077): checked out, and written
// on the branch the flow writes on (flow "": the active option, else the main flow). A working version of a parent flow
// is not: the flow checks the node out to write its own. Callers use it to decide between a checkout and an update.
func IsWorking(c domain.Change, flow string, n domain.Node) bool {
	branch := domain.BranchOf(c.Branch)
	if f := c.ResolveFlow(flow); f != "" {
		branch = flowBranchName(f)
	}
	return n.CheckedOut && domain.BranchOf(n.Branch) == branch
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
	branch string // the change branch
}

func (g *Graph) newFlowNodes(tx Tx, c domain.Change, flow string) *flowNodes {
	v := &flowNodes{g: g, tx: tx, c: c, flow: flow, chain: flowChain(c, flow), inFlow: map[string]bool{}, branch: domain.BranchOf(c.Branch)}
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

// onChangeBranch is the latest version of a node on the change branch that no stale run wrote.
func (v *flowNodes) onChangeBranch(ctx context.Context, node domain.NodeID) (*domain.Node, error) {
	vs, err := v.tx.Versions(ctx, node)
	if err != nil {
		return nil, err
	}
	for _, n := range slices.Backward(vs) {
		if n.On(v.branch) && !v.isStale(n.Execution) {
			return &n, nil
		}
	}
	return nil, nil
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

// ensureFlowBranch creates the graph branch of a flow on its first write.
func (g *Graph) ensureFlowBranch(ctx context.Context, tx Tx, c domain.Change, own domain.Branch, flow string) (string, error) {
	name := flowBranchName(flow)
	if _, err := tx.Branch(ctx, c.Namespace, name); err == nil {
		return name, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	f, _ := c.Flow(flow)
	parent := own.Name
	if f.Parent != "" {
		parent = flowBranchName(f.Parent)
		if _, err := tx.Branch(ctx, c.Namespace, parent); err != nil {
			parent = own.Name // the parent flow wrote nothing: this one forks from the change branch
		}
	}
	head, err := branchHead(ctx, tx, c.Namespace, own.Name)
	if err != nil {
		return "", err
	}
	return name, tx.PutBranch(ctx, domain.Branch{Name: name, Namespace: c.Namespace, Parent: parent, ForkBaseline: head.ID, Head: head.ID,
		Origin: "flow:" + string(c.ID) + ":" + flow, Status: domain.BranchOpen, CreatedAt: g.now()})
}

// adoptNodes makes the change branch equal to the flow: for every node the stale runs or the
// flow wrote, a new version copies what the flow sees (ADR 0025 §5). Change impacts of the flow
// become the change's, the stale ones are superseded.
func (g *Graph) adoptNodes(ctx context.Context, tx Tx, c domain.Change, f domain.Flow, by string) error {
	own, hasOwn, err := ownBranch(ctx, tx, c)
	if err != nil {
		return err
	}
	stale := map[string]bool{}
	for _, e := range f.StaleRuns {
		stale[e] = true
	}
	isStale := func(e string) bool { return e != "" && stale[e] }
	if !hasOwn {
		// nothing was written on a branch: only the change impacts move
		return g.adoptChangeImpacts(ctx, tx, c, f, nil, by)
	}
	changeBranch, flowBranch := own.Name, flowBranchName(f.ID)
	view := &flowNodes{g: g, tx: tx, c: c, flow: "", branch: changeBranch, stale: stale}
	newRefs := map[domain.NodeID]domain.Node{}
	type plan struct {
		desired, head *domain.Node
	}
	plans := map[domain.NodeID]plan{}
	var order []domain.NodeID
	wrote := false
	seen := map[domain.NodeID]bool{}
	for _, cn := range c.Nodes {
		id := nodeOf(cn)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		vs, err := tx.Versions(ctx, id)
		if err != nil {
			return err
		}
		var head, flowVer *domain.Node
		staleHead := false
		for _, n := range vs {
			switch {
			case n.On(changeBranch):
				n := n
				head = &n
				staleHead = staleHead || isStale(n.Execution)
			case domain.BranchOf(n.Branch) == flowBranch:
				n := n
				flowVer = &n
			}
		}
		if flowVer == nil && !staleHead {
			continue
		}
		wrote = wrote || flowVer != nil
		if flowVer != nil && flowVer.CheckedOut && !acceptedOnFlow(c, id, f.ID) {
			return fmt.Errorf("node %s is a working version on flow %s that the flow did not accept: accept it, or cancel the checkout, before the flow is adopted (ADR 0076, 0077): %w", flowVer.Key, f.ID, ErrConflict)
		}
		if flowVer != nil {
			// the flow was forked from a version of the node: it is a conflict when someone else moved on since
			var first *domain.Node
			for _, n := range vs {
				if domain.BranchOf(n.Branch) == flowBranch {
					n := n
					first = &n
					break
				}
			}
			if first != nil && head != nil && len(first.Parents) > 0 && head.Version != first.Parents[0] && !isStale(head.Execution) {
				return fmt.Errorf("node %s was changed on the change branch since the flow forked (v%d, the flow started from v%d): %w", head.Key, head.Version, first.Parents[0], ErrConflict)
			}
		}
		desired := flowVer
		if desired == nil {
			if desired, err = view.onChangeBranch(ctx, id); err != nil {
				return err
			}
		}
		if desired != nil && head != nil && desired.Version == head.Version {
			continue
		}
		if desired == nil && (head == nil || head.Deleted) {
			continue
		}
		plans[id] = plan{desired, head}
		order = append(order, id)
	}
	// A flow that invalidated nothing (an option) and wrote every node it changes, each derived from the head of the
	// change branch, lands as is: its versions join the change branch, no copy (ADR 0032 §2). Otherwise its versions
	// are copied as adopt versions (a reset to an older version needs a new one).
	join := len(f.StaleRuns) == 0
	for _, id := range order {
		if p := plans[id]; p.desired == nil || domain.BranchOf(p.desired.Branch) != flowBranch {
			join = false
		}
	}
	if join {
		for _, id := range order {
			d := *plans[id].desired
			if err := tx.JoinBranch(ctx, d.Ref(), changeBranch, c.ID); err != nil {
				return err
			}
			newRefs[id] = d
		}
		order = nil
	}
	// 1. the versions
	for _, id := range order {
		p := plans[id]
		var n domain.Node
		switch {
		case p.desired == nil: // created by runs that no longer exist: retire it
			n = *p.head
			n.Deleted = true
			n.Parents = []domain.Version{p.head.Version}
		default:
			n = *p.desired
			n.Parents = []domain.Version{p.desired.Version}
			if p.head != nil {
				n.Parents = []domain.Version{p.head.Version, p.desired.Version}
			}
		}
		v, err := nextVersion(ctx, tx, id)
		if err != nil {
			return err
		}
		n.Version, n.Branch, n.Reason, n.ChangeID, n.CreatedAt = v, changeBranch, domain.ReasonAdopt, c.ID, g.now()
		if err := tx.PutNode(ctx, n); err != nil {
			return err
		}
		newRefs[id] = n
	}
	// 2. their links, retargeted to the new versions
	for _, id := range order {
		p := plans[id]
		if p.desired == nil {
			continue
		}
		out, err := tx.OutLinks(ctx, p.desired.Ref())
		if err != nil {
			return err
		}
		for _, l := range out {
			to := l.To
			if r, ok := newRefs[to.ID]; ok {
				to = r.Ref()
			}
			if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: newRefs[id].Ref(), To: to, Properties: l.Properties, ChangeID: c.ID}); err != nil {
				return err
			}
		}
	}
	if b, err := tx.Branch(ctx, c.Namespace, flowBranch); err == nil && wrote {
		b.Status = domain.BranchMerged
		if err := tx.PutBranch(ctx, b); err != nil {
			return err
		}
	}
	return g.adoptChangeImpacts(ctx, tx, c, f, newRefs, by)
}

// adoptChangeImpacts moves the change impacts and reviews of an adopted flow to the main flow (ADR 0025 §5.3): one
// adopted event, folded over every change impact, then the version each one now resolves to (ADR 0029).
func (g *Graph) adoptChangeImpacts(ctx context.Context, tx Tx, c domain.Change, f domain.Flow, newRefs map[domain.NodeID]domain.Node, by string) error {
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Op: domain.ImpactAdopted, Flow: f.ID, Stale: f.StaleRuns, By: by}); err != nil {
		return err
	}
	impacts, err := tx.ChangeImpacts(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, cn := range impacts {
		id := nodeOf(cn)
		n, ok := newRefs[id]
		if id == "" || !ok || cn.Superseded {
			continue
		}
		ref := n.Ref()
		if sameRef(cn.Post, &ref) {
			continue
		}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactTransitioned, Execution: n.Execution, By: by, Post: &ref}); err != nil {
			return err
		}
	}
	return nil
}

func sameRef(a, b *domain.NodeRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// discardNodes abandons the branches of a discarded flow and its descendants and rejects the
// change impacts they declared.
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
	for id := range gone {
		if b, err := tx.Branch(ctx, c.Namespace, flowBranchName(id)); err == nil && b.Status == domain.BranchOpen {
			b.Status = domain.BranchAbandoned
			if err := tx.PutBranch(ctx, b); err != nil {
				return err
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

// acceptedOnFlow reports whether the last review of a node of the change made on a flow accepts it (ADR 0077: a working version
// stays one until the change lands, so adopting a flow asks its acceptance).
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
