package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// changeOpen loads a change and checks that it still accepts change nodes.
func changeOpen(ctx context.Context, tx Tx, id domain.ChangeID) (domain.ChangeSet, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return c, err
	}
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeMergePending:
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	return c, nil
}

func findChangeNode(c domain.ChangeSet, id domain.ChangeNodeID) (int, error) {
	for i, cn := range c.Nodes {
		if cn.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("change node %s of change %s: %w", id, c.ID, ErrNotFound)
}

// AddNodes adds change nodes to a change (ADR 0024): each names a node with an
// intent and a rationale. A modified node gives its pre version, which must be
// in the reference baseline; the post version is set later by RealizeNode.
func (g *Graph) AddNodes(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeNode) ([]domain.ChangeNode, error) {
	out := make([]domain.ChangeNode, 0, len(nodes))
	err := g.repo.InTx(ctx, func(tx Tx) error {
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		b, err := tx.Baseline(ctx, c.BaselineID)
		if err != nil {
			return err
		}
		known := map[domain.ChangeNodeID]bool{}
		for _, cn := range c.Nodes {
			known[cn.ID] = true
		}
		// a node appears once per flow: what the flow sees (the stale change nodes of a relaunched step do not count)
		presByFlow := map[string]map[domain.NodeID]bool{}
		presOf := func(flow string) map[domain.NodeID]bool {
			if set, ok := presByFlow[flow]; ok {
				return set
			}
			set := map[domain.NodeID]bool{}
			fv := g.newFlowNodes(tx, c, flow)
			for _, cn := range c.Nodes {
				if cn.Pre != nil && fv.visible(cn) {
					set[cn.Pre.ID] = true
				}
			}
			presByFlow[flow] = set
			return set
		}
		batch := slices.Clone(nodes)
		for i := range batch {
			if batch[i].ID == "" {
				batch[i].ID = domain.ChangeNodeID(g.newID())
			}
			known[batch[i].ID] = true
		}
		for _, cn := range batch {
			if cn.Post != nil || cn.Landed != nil || len(cn.Reviews) > 0 || cn.Recheck {
				return fmt.Errorf("change node %s: post, landed and reviews are set by RealizeNode, ReviewNode and Apply: %w", cn.ID, ErrInvalid)
			}
			if cn.Review == "" {
				cn.Review = domain.ReviewProposed
			}
			if cn.Review != domain.ReviewProposed {
				return fmt.Errorf("change node %s starts proposed, it is accepted or rejected by ReviewNode: %w", cn.ID, ErrInvalid)
			}
			if cn.Flow != "" {
				if _, ok := c.Flow(cn.Flow); !ok {
					return fmt.Errorf("change node %s: unknown flow %s: %w", cn.ID, cn.Flow, ErrNotFound)
				}
				if c.FlowStatusOf(cn.Flow) != domain.FlowOpen {
					return fmt.Errorf("change node %s: flow %s is not open: %w", cn.ID, cn.Flow, ErrConflict)
				}
			}
			if cn.Via != "" && !known[cn.Via] {
				return fmt.Errorf("change node %s: unknown via %s: %w", cn.ID, cn.Via, ErrInvalid)
			}
			if cn.Pre != nil {
				if !b.Contains(*cn.Pre) {
					return fmt.Errorf("change node %s: pre %s is not in baseline %s (the node moved on): %w", cn.ID, cn.Pre, b.ID, ErrConflict)
				}
				pres := presOf(cn.Flow)
				if pres[cn.Pre.ID] {
					return fmt.Errorf("change node %s: node %s is already in the change: %w", cn.ID, cn.Pre.ID, ErrConflict)
				}
				pres[cn.Pre.ID] = true
				n, err := tx.Node(ctx, *cn.Pre)
				if err != nil {
					return err
				}
				if (cn.Key != "" && cn.Key != n.Key) || (cn.Type != "" && cn.Type != n.Type) {
					return fmt.Errorf("change node %s: key / type do not match node %s: %w", cn.ID, n.Ref(), ErrInvalid)
				}
				cn.Key, cn.Type = n.Key, n.Type
				if got, want := domain.NamespaceOf(n.Namespace), domain.NamespaceOf(c.Namespace); got != want {
					return fmt.Errorf("change node %s: %s belongs to namespace %q, the change acts on %q: %w", cn.ID, n.Key, got, want, ErrInvalid)
				}
			}
			cn.CreatedAt = g.now()
			if err := cn.Validate(); err != nil {
				return fmt.Errorf("change node %s: %v: %w", cn.ID, err, ErrInvalid)
			}
			if err := tx.PutChangeNode(ctx, id, cn); err != nil {
				return err
			}
			out = append(out, cn)
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			return tx.PutChange(ctx, c)
		}
		return nil
	})
	return out, err
}

// ListChangeNodes returns the change nodes of a change.
func (g *Graph) ListChangeNodes(ctx context.Context, id domain.ChangeID) (out []domain.ChangeNode, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		out = c.Nodes
		return nil
	})
	return
}

// RealizeNode sets the post version of a change node: a version created by the
// change (its ChangeID) that succeeds the pre version, or the first version of
// a created node. The version records the change node and its comment.
func (g *Graph) RealizeNode(ctx context.Context, id domain.ChangeID, node domain.ChangeNodeID, post domain.NodeRef) (cn domain.ChangeNode, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		i, err := findChangeNode(c, node)
		if err != nil {
			return err
		}
		cn = c.Nodes[i]
		if cn.Post != nil {
			return fmt.Errorf("change node %s is already realized as %s: %w", node, cn.Post, ErrConflict)
		}
		n, err := tx.Node(ctx, post)
		if err != nil {
			return err
		}
		switch {
		case n.ChangeID != id:
			return fmt.Errorf("version %s was not created by change %s: %w", n.Ref(), id, ErrInvalid)
		case n.Key != cn.Key || n.Type != cn.Type:
			return fmt.Errorf("version %s is %s %s, not %s %s: %w", n.Ref(), n.Type, n.Key, cn.Type, cn.Key, ErrInvalid)
		case cn.Intent == domain.IntentModified && !slices.Contains(n.Parents, cn.Pre.Version):
			return fmt.Errorf("version %s does not succeed %s: %w", n.Ref(), cn.Pre, ErrInvalid)
		case cn.Intent == domain.IntentCreated && n.Reason != domain.ReasonCreate:
			return fmt.Errorf("version %s is not the creation of the node: %w", n.Ref(), ErrInvalid)
		}
		cn.Post = &domain.NodeRef{ID: n.ID, Version: n.Version}
		if err := cn.Validate(); err != nil {
			return fmt.Errorf("change node %s: %v: %w", node, err, ErrInvalid)
		}
		comment := cn.Rationale
		if len(cn.Reviews) > 0 && cn.Review == domain.ReviewAccepted {
			comment = cn.Reviews[len(cn.Reviews)-1].Comment
		}
		if err := tx.SetNodeOrigin(ctx, *cn.Post, id, cn.ID, comment); err != nil {
			return err
		}
		return tx.PutChangeNode(ctx, id, cn)
	})
	return
}

// ReviewNode accepts or rejects a proposed change node. The comment is
// mandatory: it is kept in the review history and, once the node is realized,
// on the version itself, so the origin of a version can be read from the node.
func (g *Graph) ReviewNode(ctx context.Context, id domain.ChangeID, node domain.ChangeNodeID, status domain.NodeReview, by, comment string) (domain.ChangeNode, error) {
	return g.ReviewNodeOn(ctx, id, "", "", node, status, by, comment)
}

// ReviewNodeOn reviews a change node as a flow sees it (ADR 0025): on a flow the review is a
// candidate until the flow is adopted, and execution is the action run that made it.
func (g *Graph) ReviewNodeOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeNodeID, status domain.NodeReview, by, comment string) (cn domain.ChangeNode, err error) {
	if status != domain.ReviewAccepted && status != domain.ReviewRejected {
		return cn, fmt.Errorf("a change node is reviewed as accepted or rejected, not %q: %w", status, ErrInvalid)
	}
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return cn, fmt.Errorf("a review needs a comment: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		if flow != "" {
			if _, ok := c.Flow(flow); !ok {
				return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
			}
			if c.FlowStatusOf(flow) != domain.FlowOpen {
				return fmt.Errorf("flow %s is not open: %w", flow, ErrConflict)
			}
		}
		i, err := findChangeNode(c, node)
		if err != nil {
			return err
		}
		cn = c.Nodes[i]
		seen, err := g.newFlowNodes(tx, c, flow).find(ctx, node)
		if err != nil {
			return err
		}
		if seen.Review != domain.ReviewProposed {
			return fmt.Errorf("change node %s is already %s: %w", node, seen.Review, ErrConflict)
		}
		if flow == "" {
			cn.Review = status
		}
		cn.Reviews = append(cn.Reviews, domain.Review{Status: status, By: by, Comment: comment, At: g.now(), Flow: flow, Execution: execution})
		if flow == "" && status == domain.ReviewAccepted && cn.Post != nil {
			if err := tx.SetNodeOrigin(ctx, *cn.Post, id, cn.ID, comment); err != nil {
				return err
			}
		}
		return tx.PutChangeNode(ctx, id, cn)
	})
	return
}

// NodeWrite is one edit of the version a change node produces.
type NodeWrite struct {
	// Properties are merged over the ones of the current version.
	Properties map[string]any
	// State moves the node to a lifecycle state, after the other edits of the write.
	State string
	// AddLinks are outgoing links of the new version to exact node versions.
	AddLinks []LinkWrite
	// RemoveLinks are the outgoing links of the current version left out of the new one.
	RemoveLinks []domain.LinkID
	// Flow is the flow branch the write belongs to ("" = the main flow) and Execution the action
	// run that writes (ADR 0025): the version is written on the branch of the flow, and what a
	// relaunch marks stale is found through its execution.
	Flow, Execution string
	// Retire ends the node: the new version is a tombstone with no outgoing link. It is the
	// clean-up of a node a projection no longer owns (an orphan), never an intent of a change.
	Retire bool
}

// LinkWrite is an outgoing link added by a NodeWrite.
type LinkWrite struct {
	Type       string
	To         domain.NodeRef
	Properties map[string]any
}

// WriteNode creates the next version of a change node's node on the branch of
// the change and makes it the post version: a modified node derives from its
// pre version, a created node starts at version 1. Outgoing links are carried
// forward (re-targeted to the versions this change produced). A node is edited
// only in an editable state; the transitions, their guards and actions are
// checked when the change is applied (ADR 0014, ADR 0024).
func (g *Graph) WriteNode(ctx context.Context, id domain.ChangeID, node domain.ChangeNodeID, w NodeWrite) (cn domain.ChangeNode, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return err
		} else if !ok {
			if c, own, err = g.ensureOwnBranch(ctx, tx, c); err != nil {
				return err
			}
		}
		flow := w.Flow
		if flow != "" {
			if _, ok := c.Flow(flow); !ok {
				return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
			}
			if c.FlowStatusOf(flow) != domain.FlowOpen {
				return fmt.Errorf("flow %s is not open: %w", flow, ErrConflict)
			}
		}
		i, err := findChangeNode(c, node)
		if err != nil {
			return err
		}
		cn = c.Nodes[i]
		fv := g.newFlowNodes(tx, c, flow)
		seen, err := fv.nodes(ctx) // the change nodes as this flow sees them
		if err != nil {
			return err
		}
		vi := slices.IndexFunc(seen, func(x domain.ChangeNode) bool { return x.ID == node })
		if vi < 0 {
			return fmt.Errorf("change node %s is not on the flow %q of change %s: %w", node, flow, id, ErrNotFound)
		}
		vcn := seen[vi]
		if vcn.Review == domain.ReviewRejected {
			return fmt.Errorf("change node %s is rejected: %w", node, ErrConflict)
		}
		ix, err := g.typesAt(ctx, tx, c.BaselineID)
		if err != nil {
			return err
		}
		branch := domain.BranchOf(c.Branch)
		if flow != "" {
			if branch, err = g.ensureFlowBranch(ctx, tx, c, own, flow); err != nil {
				return err
			}
		}
		ns := domain.NamespaceOf(c.Namespace)

		var base *domain.Node
		switch {
		case flow != "":
			if nid := nodeOf(vcn); nid != "" {
				if base, err = fv.latest(ctx, nid); err != nil {
					return err
				}
			}
			if base == nil && cn.Pre != nil {
				n, err := tx.Node(ctx, *cn.Pre)
				if err != nil {
					return err
				}
				base = &n
			}
		case cn.Post != nil:
			n, err := tx.LatestOn(ctx, cn.Post.ID, branch)
			if err != nil {
				return err
			}
			base = &n
		case cn.Pre != nil:
			n, err := tx.Node(ctx, *cn.Pre)
			if err != nil {
				return err
			}
			if latest, err := tx.LatestOn(ctx, n.ID, branch); err == nil {
				n = latest
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
			base = &n
		}
		if base != nil && domain.NamespaceOf(base.Namespace) != ns {
			return invalidf("node %s is in namespace %s, the change acts on %s", base.Key, domain.NamespaceOf(base.Namespace), ns)
		}
		typ := cn.Type
		lc := ix.lifecycleOf(typ)

		n := domain.Node{Branch: branch, Namespace: ns, Key: cn.Key, Type: typ, ChangeID: id, ChangeNode: cn.ID, CreatedAt: g.now(), Comment: cn.Rationale, Execution: w.Execution}
		if vcn.Review == domain.ReviewAccepted && len(cn.Reviews) > 0 {
			n.Comment = cn.Reviews[len(cn.Reviews)-1].Comment
		}
		var cur string // state the edits and the transition start from
		if base == nil {
			n.ID, n.Version, n.Reason = domain.NodeID(g.newID()), 1, domain.ReasonCreate
			if lc != nil {
				cur = lc.Initial
			}
		} else {
			v, err := nextVersion(ctx, tx, base.ID)
			if err != nil {
				return err
			}
			n.ID, n.Version, n.Parents, n.Properties = base.ID, v, []domain.Version{base.Version}, maps.Clone(base.Properties)
			n.Reason = domain.ReasonRevise
			if domain.BranchOf(base.Branch) != branch {
				n.Reason = domain.ReasonDerive
			}
			cur = base.State
			if lc != nil && cur == "" {
				cur = lc.Initial
			}
		}
		if lc == nil && w.State != "" {
			return invalidf("node type %s has no lifecycle: state %q", typ, w.State)
		}
		if w.Retire && (base == nil || len(w.Properties) > 0 || len(w.AddLinks) > 0 || len(w.RemoveLinks) > 0 || w.State != "") {
			return invalidf("a node is retired on its own, from an existing version")
		}
		edits := len(w.Properties) > 0 || len(w.AddLinks) > 0 || len(w.RemoveLinks) > 0
		// a node the change creates is its working copy: the editable rule is about the versions it starts from
		if edits && base != nil && cn.Intent != domain.IntentCreated && lc != nil && base.State != "" && !lc.Editable(base.State) {
			return invalidf("cannot edit %s (%s): it is %s, not editable; reopen it first with a State write", base.Key, typ, base.State)
		}
		if n.Properties == nil && len(w.Properties) > 0 {
			n.Properties = map[string]any{}
		}
		maps.Copy(n.Properties, w.Properties)
		if edits || base == nil {
			if err := g.validateProps(ctx, ix, n, n.Properties); err != nil {
				return err
			}
		}
		if base != nil {
			n.State = base.State
		} else if lc != nil {
			n.State = lc.Initial
		}
		if w.State != "" {
			if _, ok := lc.State(w.State); !ok {
				return invalidf("%s has no state %q", typ, w.State)
			}
			if w.State != cur {
				if _, ok := lc.Move(cur, w.State); !ok {
					return invalidf("%s (%s) cannot go from %s to %s", cn.Key, typ, cur, w.State)
				}
			}
			n.State = w.State
		}

		n.Deleted = w.Retire
		if err := tx.PutNode(ctx, n); err != nil {
			return err
		}
		ref := n.Ref()
		if base != nil && !w.Retire {
			out, err := tx.OutLinks(ctx, base.Ref())
			if err != nil {
				return err
			}
			drop := map[domain.LinkID]bool{}
			for _, l := range w.RemoveLinks {
				drop[l] = true
			}
			for _, l := range out {
				if drop[l.ID] {
					delete(drop, l.ID)
					continue
				}
				if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: ref, To: retarget(seen, l.To), Properties: l.Properties, ChangeID: id}); err != nil {
					return err
				}
			}
			if len(drop) > 0 {
				return invalidf("links %v are not outgoing links of %s", slices.Collect(maps.Keys(drop)), base.Ref())
			}
		} else if len(w.RemoveLinks) > 0 {
			return invalidf("a new node has no link to remove")
		}
		for _, l := range w.AddLinks {
			if l.Type == "" || l.To.Version == 0 {
				return invalidf("a link needs a type and an exact target version")
			}
			if _, err := tx.Node(ctx, l.To); err != nil {
				return err
			}
			if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: ref, To: retarget(seen, l.To), Properties: l.Properties, ChangeID: id}); err != nil {
				return err
			}
		}
		if flow != "" && cn.Flow != flow {
			// a change node of the main flow (or of an ancestor) written on the flow: its stored post is not this flow's
			cn = vcn
			cn.Post = &ref
			return nil
		}
		cn.Post = &ref
		if err := cn.Validate(); err != nil {
			return invalidf("change node %s: %v", node, err)
		}
		return tx.PutChangeNode(ctx, id, cn)
	})
	return
}

// retarget points a link to the post version of a node this change produced
// when the link targets its pre version.
func retarget(nodes []domain.ChangeNode, to domain.NodeRef) domain.NodeRef {
	for _, cn := range nodes {
		if cn.Pre != nil && cn.Post != nil && *cn.Pre == to {
			return *cn.Post
		}
	}
	return to
}
