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

// flowChain lists the flows from f up to the main flow (excluded), innermost first.
func flowChain(c domain.ChangeSet, flow string) []domain.Flow {
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
		for _, e := range f.StaleExecutions {
			out[e] = true
		}
	}
	return out
}

// flowNodes is what the process running on a flow sees of the change nodes.
type flowNodes struct {
	g      *Graph
	tx     Tx
	c      domain.ChangeSet
	flow   string
	chain  []domain.Flow
	inFlow map[string]bool
	stale  map[string]bool
	branch string // the change branch
}

func (g *Graph) newFlowNodes(tx Tx, c domain.ChangeSet, flow string) *flowNodes {
	v := &flowNodes{g: g, tx: tx, c: c, flow: flow, chain: flowChain(c, flow), inFlow: map[string]bool{}, branch: domain.BranchOf(c.Branch)}
	for _, f := range v.chain {
		v.inFlow[f.ID] = true
	}
	v.stale = staleOf(v.chain)
	return v
}

func (v *flowNodes) isStale(execution string) bool { return execution != "" && v.stale[execution] }

// visible tells whether a stored change node exists for the flow.
func (v *flowNodes) visible(cn domain.ChangeNode) bool {
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

// latest resolves the version of a node the flow sees: its latest version on the
// flow branches (innermost first), else its latest non-stale version on the change branch.
func (v *flowNodes) latest(ctx context.Context, node domain.NodeID) (*domain.Node, error) {
	for _, f := range v.chain {
		n, err := v.tx.LatestOn(ctx, node, flowBranchName(f.ID))
		if err == nil {
			return &n, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	}
	return v.onChangeBranch(ctx, node)
}

// onChangeBranch is the latest version of a node on the change branch that no stale run wrote.
func (v *flowNodes) onChangeBranch(ctx context.Context, node domain.NodeID) (*domain.Node, error) {
	vs, err := v.tx.Versions(ctx, node)
	if err != nil {
		return nil, err
	}
	for _, n := range slices.Backward(vs) {
		if domain.BranchOf(n.Branch) == v.branch && !v.isStale(n.Execution) {
			return &n, nil
		}
	}
	return nil, nil
}

func nodeOf(cn domain.ChangeNode) domain.NodeID {
	switch {
	case cn.Post != nil:
		return cn.Post.ID
	case cn.Pre != nil:
		return cn.Pre.ID
	}
	return ""
}

// nodes returns the change nodes the flow sees, with the post version and the review it resolves to.
func (v *flowNodes) nodes(ctx context.Context) ([]domain.ChangeNode, error) {
	var out []domain.ChangeNode
	for _, cn := range v.c.Nodes {
		if !v.visible(cn) {
			continue
		}
		if v.flow == "" {
			out = append(out, cn)
			continue
		}
		if id := nodeOf(cn); id != "" {
			n, err := v.latest(ctx, id)
			if err != nil {
				return nil, err
			}
			cn.Post = nil
			// a post is a version this change wrote: not the released pre version
			if n != nil && (cn.Pre == nil || n.Version > cn.Pre.Version) && (cn.Pre == nil || n.ID == cn.Pre.ID) {
				ref := n.Ref()
				cn.Post = &ref
			}
		}
		cn.Review = domain.ReviewProposed
		for _, r := range cn.Reviews {
			if r.Superseded || (r.Flow != "" && !v.inFlow[r.Flow]) || v.isStale(r.Execution) {
				continue
			}
			cn.Review = r.Status
		}
		out = append(out, cn)
	}
	return out, nil
}

// find returns the change node a flow sees under an id.
func (v *flowNodes) find(ctx context.Context, id domain.ChangeNodeID) (domain.ChangeNode, error) {
	list, err := v.nodes(ctx)
	if err != nil {
		return domain.ChangeNode{}, err
	}
	for _, cn := range list {
		if cn.ID == id {
			return cn, nil
		}
	}
	return domain.ChangeNode{}, fmt.Errorf("change node %s is not on the flow %q of change %s: %w", id, v.flow, v.c.ID, ErrNotFound)
}

// ensureFlowBranch creates the graph branch of a flow on its first write.
func (g *Graph) ensureFlowBranch(ctx context.Context, tx Tx, c domain.ChangeSet, own domain.Branch, flow string) (string, error) {
	name := flowBranchName(flow)
	if _, err := tx.Branch(ctx, name); err == nil {
		return name, nil
	} else if !errors.Is(err, ErrNotFound) {
		return "", err
	}
	f, _ := c.Flow(flow)
	parent := own.Name
	if f.Parent != "" {
		parent = flowBranchName(f.Parent)
		if _, err := tx.Branch(ctx, parent); err != nil {
			parent = own.Name // the parent flow wrote nothing: this one forks from the change branch
		}
	}
	head, err := branchHead(ctx, tx, own.Name)
	if err != nil {
		return "", err
	}
	return name, tx.PutBranch(ctx, domain.Branch{Name: name, Parent: parent, ForkBaseline: head.ID, Head: head.ID,
		Origin: "flow:" + string(c.ID) + ":" + flow, Status: domain.BranchOpen, CreatedAt: g.now()})
}

// adoptNodes makes the change branch equal to the flow: for every node the stale runs or the
// flow wrote, a new version copies what the flow sees (ADR 0025 §5). Change nodes of the flow
// become the change's, the stale ones are superseded.
func (g *Graph) adoptNodes(ctx context.Context, tx Tx, c domain.ChangeSet, f domain.Flow, by string) error {
	own, hasOwn, err := ownBranch(ctx, tx, c)
	if err != nil {
		return err
	}
	stale := map[string]bool{}
	for _, e := range f.StaleExecutions {
		stale[e] = true
	}
	isStale := func(e string) bool { return e != "" && stale[e] }
	if !hasOwn {
		// nothing was written on a branch: only the change nodes move
		return g.adoptChangeNodes(ctx, tx, c, f, map[domain.NodeID]domain.NodeRef{}, isStale)
	}
	changeBranch, flowBranch := own.Name, flowBranchName(f.ID)
	view := &flowNodes{g: g, tx: tx, c: c, flow: "", branch: changeBranch, stale: stale}
	newRefs := map[domain.NodeID]domain.NodeRef{}
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
			switch domain.BranchOf(n.Branch) {
			case changeBranch:
				n := n
				head = &n
				staleHead = staleHead || isStale(n.Execution)
			case flowBranch:
				n := n
				flowVer = &n
			}
		}
		if flowVer == nil && !staleHead {
			continue
		}
		wrote = wrote || flowVer != nil
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
		newRefs[id] = n.Ref()
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
				to = r
			}
			if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: newRefs[id], To: to, Properties: l.Properties, ChangeID: c.ID}); err != nil {
				return err
			}
		}
	}
	if b, err := tx.Branch(ctx, flowBranch); err == nil && wrote {
		b.Status = domain.BranchMerged
		if err := tx.PutBranch(ctx, b); err != nil {
			return err
		}
	}
	return g.adoptChangeNodes(ctx, tx, c, f, newRefs, isStale)
}

// adoptChangeNodes moves the change nodes and reviews of an adopted flow to the main flow.
func (g *Graph) adoptChangeNodes(ctx context.Context, tx Tx, c domain.ChangeSet, f domain.Flow, newRefs map[domain.NodeID]domain.NodeRef, isStale func(string) bool) error {
	var changed []domain.ChangeNode
	for _, cn := range c.Nodes {
		before := cn
		cn.Reviews = slices.Clone(cn.Reviews)
		switch {
		case cn.Flow == f.ID:
			cn.Flow = ""
		case cn.Flow == "" && !cn.Superseded && isStale(cn.Execution):
			cn.Superseded = true
		}
		for i, r := range cn.Reviews {
			switch {
			case r.Flow == f.ID:
				cn.Reviews[i].Flow = ""
			case r.Flow == "" && isStale(r.Execution):
				cn.Reviews[i].Superseded = true
			}
		}
		if id := nodeOf(cn); id != "" {
			if r, ok := newRefs[id]; ok && !cn.Superseded {
				cn.Post = &r
			}
		}
		if !cn.Superseded {
			cn.Review = domain.ReviewProposed
			for _, r := range cn.Reviews {
				if !r.Superseded && r.Flow == "" {
					cn.Review = r.Status
				}
			}
		}
		if before.Flow != cn.Flow || before.Superseded != cn.Superseded || before.Review != cn.Review || !sameRef(before.Post, cn.Post) || !slices.Equal(before.Reviews, cn.Reviews) {
			changed = append(changed, cn)
		}
	}
	// the superseded ones first: a node has one live change node per flow
	slices.SortStableFunc(changed, func(a, b domain.ChangeNode) int {
		if a.Superseded == b.Superseded {
			return 0
		}
		if a.Superseded {
			return -1
		}
		return 1
	})
	for _, cn := range changed {
		if err := tx.PutChangeNode(ctx, c.ID, cn); err != nil {
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
// change nodes they declared.
func (g *Graph) discardNodes(ctx context.Context, tx Tx, c domain.ChangeSet, f domain.Flow, by string) error {
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
		if b, err := tx.Branch(ctx, flowBranchName(id)); err == nil && b.Status == domain.BranchOpen {
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
		cn.Review = domain.ReviewRejected
		cn.Reviews = append(cn.Reviews, domain.Review{Status: domain.ReviewRejected, By: by, Comment: "flow discarded", At: g.now(), Flow: cn.Flow})
		if err := tx.PutChangeNode(ctx, c.ID, cn); err != nil {
			return err
		}
	}
	return nil
}
