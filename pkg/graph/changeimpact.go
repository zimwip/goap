package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// changeOpen loads a change and checks that it still accepts change impacts.
func changeOpen(ctx context.Context, tx Tx, id domain.ChangeID) (domain.Change, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return c, err
	}
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return c, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	return c, nil
}

func findChangeImpact(c domain.Change, id domain.ChangeImpactID) (int, error) {
	for i, cn := range c.Nodes {
		if cn.ID == id {
			return i, nil
		}
	}
	return -1, fmt.Errorf("change impact %s of change %s: %w", id, c.ID, ErrNotFound)
}

// ProposeImpact adds change impacts to a change (ADR 0024): each names an existing node (intent modified) and a
// rationale; its pre version must be in the reference baseline. The impact has no version until a ImpactNodeCheckout
// writes one (ADR 0076). A new node is not proposed: it is created by ImpactNodeCreate, which adds its impact and its
// first version in one event (ADR 0077).
func (g *Graph) ProposeImpact(ctx context.Context, id domain.ChangeID, nodes []domain.ChangeImpact) (out []domain.ChangeImpact, err error) {
	for _, cn := range nodes {
		if cn.Intent == domain.IntentCreated {
			return nil, fmt.Errorf("change impact of %s: a new node is created with ImpactNodeCreate; ProposeImpact is for existing nodes: %w", cn.Key, ErrInvalid)
		}
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		out, err = g.declareTx(ctx, tx, id, nodes, true)
		return err
	})
	return out, err
}

// declareTx declares change impacts in a transaction (ProposeImpact, and the impact an operation on an existing node
// declares). With propose false it only checks and returns them: the caller records them itself (a creation, whose
// created event carries the impact).
func (g *Graph) declareTx(ctx context.Context, tx Tx, id domain.ChangeID, nodes []domain.ChangeImpact, propose bool) ([]domain.ChangeImpact, error) {
	out := make([]domain.ChangeImpact, 0, len(nodes))
	err := func() error {
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		b, err := tx.Baseline(ctx, c.BaselineID)
		if err != nil {
			return err
		}
		ix, err := g.typesAt(ctx, tx, c.BaselineID)
		if err != nil {
			return err
		}
		known := map[domain.ChangeImpactID]bool{}
		for _, cn := range c.Nodes {
			known[cn.ID] = true
		}
		// a node appears once per flow: what the flow sees (the stale change impacts of a relaunched step do not count)
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
				batch[i].ID = domain.ChangeImpactID(g.newID())
			}
			batch[i].Flow = c.ResolveFlow(batch[i].Flow) // no flow: the active option (ADR 0032 §6)
			known[batch[i].ID] = true
		}
		for _, cn := range batch {
			if cn.Post != nil || cn.Landed != nil || len(cn.Reviews) > 0 || cn.Recheck {
				return fmt.Errorf("change impact %s: post, landed and reviews are set by ImpactNodeCreate / ImpactNodeCheckout, ImpactNodeReview and Apply: %w", cn.ID, ErrInvalid)
			}
			if cn.Review == "" {
				cn.Review = domain.ReviewProposed
			}
			if cn.Review != domain.ReviewProposed {
				return fmt.Errorf("change impact %s starts proposed, it is accepted or rejected by ImpactNodeReview: %w", cn.ID, ErrInvalid)
			}
			if cn.Flow != "" {
				if _, ok := c.Flow(cn.Flow); !ok {
					return fmt.Errorf("change impact %s: unknown flow %s: %w", cn.ID, cn.Flow, ErrNotFound)
				}
				if c.FlowStatusOf(cn.Flow) != domain.FlowOpen {
					return fmt.Errorf("change impact %s: flow %s is not open: %w", cn.ID, cn.Flow, ErrConflict)
				}
			}
			if cn.Via != "" && !known[cn.Via] {
				return fmt.Errorf("change impact %s: unknown via %s: %w", cn.ID, cn.Via, ErrInvalid)
			}
			if cn.Pre != nil {
				if !b.Contains(*cn.Pre) {
					return fmt.Errorf("change impact %s: pre %s is not in baseline %s (the node moved on): %w", cn.ID, cn.Pre, b.ID, ErrConflict)
				}
				pres := presOf(cn.Flow)
				if pres[cn.Pre.ID] {
					return fmt.Errorf("change impact %s: node %s is already in the change: %w", cn.ID, cn.Pre.ID, ErrConflict)
				}
				pres[cn.Pre.ID] = true
				n, err := tx.Node(ctx, *cn.Pre)
				if err != nil {
					return err
				}
				if (cn.Key != "" && cn.Key != n.Key) || (cn.Type != "" && cn.Type != n.Type) {
					return fmt.Errorf("change impact %s: key / type do not match node %s: %w", cn.ID, n.Ref(), ErrInvalid)
				}
				cn.Key, cn.Type = n.Key, n.Type
				if got, want := domain.NamespaceOf(n.Namespace), domain.NamespaceOf(c.Namespace); got != want {
					return fmt.Errorf("change impact %s: %s belongs to namespace %q, the change acts on %q: %w", cn.ID, n.Key, got, want, ErrInvalid)
				}
			}
			if cn.Pre == nil && cn.Type != "" {
				if err := ix.checkNode(domain.NamespaceOf(c.Namespace), cn.Type); err != nil {
					return fmt.Errorf("change impact %s: %w", cn.ID, err)
				}
			}
			cn.CreatedAt = g.now()
			if err := cn.Validate(); err != nil {
				return fmt.Errorf("change impact %s: %v: %w", cn.ID, err, ErrInvalid)
			}
			cn := cn
			if propose {
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactProposed, Flow: cn.Flow, Execution: cn.Execution, State: &cn}); err != nil {
					return err
				}
			}
			out = append(out, cn)
		}
		if c.Status == domain.ChangeDraft {
			c.Status = domain.ChangeActive
			return tx.PutChange(ctx, c)
		}
		return nil
	}()
	return out, err
}

// ListChangeImpacts returns the change impacts of a change.
func (g *Graph) ListChangeImpacts(ctx context.Context, id domain.ChangeID) (out []domain.ChangeImpact, err error) {
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

// ImpactsOf returns the change impacts one execution (an Activity Run, architecture plan "Activity concept")
// declared: its Pre (the context graph) and Post (the modified graph) node versions, scoped to exactly what that
// run touched rather than the whole change. Empty when the execution declared none.
func (g *Graph) ImpactsOf(ctx context.Context, id domain.ChangeID, execution string) (out []domain.ChangeImpact, err error) {
	all, err := g.ListChangeImpacts(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, cn := range all {
		if cn.Execution == execution {
			out = append(out, cn)
		}
	}
	return out, nil
}

// ImpactNodeReview accepts or rejects a proposed change impact; accepting checks its working version, if any, as a
// frozen one would be (ADR 0077) and freezes nothing: landing does. The comment is mandatory: it is kept in the review history and, once the node is realized,
// on the version itself, so the origin of a version can be read from the node.
func (g *Graph) ImpactNodeReview(ctx context.Context, id domain.ChangeID, node domain.ChangeImpactID, status domain.NodeReview, by, comment string) (domain.ChangeImpact, error) {
	return g.ImpactNodeReviewOn(ctx, id, "", "", node, status, by, comment)
}

// ImpactNodeReviewOn reviews a change impact as a flow sees it (ADR 0025): on a flow the review is a
// candidate until the flow is adopted, and execution is the action run that made it.
func (g *Graph) ImpactNodeReviewOn(ctx context.Context, id domain.ChangeID, flow, execution string, node domain.ChangeImpactID, status domain.NodeReview, by, comment string) (cn domain.ChangeImpact, err error) {
	if status != domain.ReviewAccepted && status != domain.ReviewRejected {
		return cn, fmt.Errorf("a change impact is reviewed as accepted or rejected, not %q: %w", status, ErrInvalid)
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
		flow := c.ResolveFlow(flow)
		if flow != "" {
			if _, ok := c.Flow(flow); !ok {
				return fmt.Errorf("flow %s: %w", flow, ErrNotFound)
			}
			if c.FlowStatusOf(flow) != domain.FlowOpen {
				return fmt.Errorf("flow %s is not open: %w", flow, ErrConflict)
			}
		}
		i, err := findChangeImpact(c, node)
		if err != nil {
			return err
		}
		cn = c.Nodes[i]
		seen, err := g.newFlowNodes(tx, c, flow).find(ctx, node)
		if err != nil {
			return err
		}
		if seen.Review != domain.ReviewProposed {
			return fmt.Errorf("change impact %s is already %s: %w", node, seen.Review, ErrConflict)
		}
		if status == domain.ReviewAccepted {
			all, err := g.newFlowNodes(tx, c, flow).nodes(ctx)
			if err != nil {
				return err
			}
			if err := g.checkOrigins(ctx, tx, id, all, seen); err != nil {
				return err
			}
		}
		if g.ReviewPolicy != nil {
			entries, err := tx.Log(ctx, factsFilter(id))
			if err != nil {
				return err
			}
			facts, err := itemsOf(entries)
			if err != nil {
				return err
			}
			if err := g.ReviewPolicy.Review(domain.ReviewRequest{Impact: seen, Reviewer: by, Status: status, Items: facts}); err != nil {
				return fmt.Errorf("review of %s refused: %v: %w", node, err, ErrInvalid)
			}
		}
		// an accepted review is gated by what a frozen version must satisfy (ADR 0077): validators, required links, link
		// attributes, the origins gate; a refusal leaves the review proposed. Nothing is frozen: the version stays a
		// working version until the change lands
		if status == domain.ReviewAccepted && seen.Post != nil {
			if err := g.checkAccepted(ctx, tx, id, flow, node); err != nil {
				return err
			}
		}
		r := domain.Review{Status: status, By: by, Comment: comment, At: g.now(), Flow: flow, Execution: execution}
		if flow == "" {
			cn.Review = status
		}
		cn.Reviews = append(cn.Reviews, r)
		if flow == "" && status == domain.ReviewAccepted && cn.Post != nil {
			if err := tx.SetNodeOrigin(ctx, *cn.Post, id, cn.ID, comment); err != nil {
				return err
			}
		}
		return g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactReviewed, Flow: flow, Execution: execution, By: by, Review: &r})
	})
	return
}

// ReopenImpacts sends decided change impacts of the main flow back to proposed (a review event, as when a change goes
// back to a state, ADR 0058): what was accepted must be reviewed again, what was rejected is reworked (its working
// version was kept, ADR 0076 §5b). A use case calls it when the ground of an acceptance falls (ADR 0075: a derogation
// expired); the graph reads no clock and names no reason. A proposed impact is left as it is; the comment is
// mandatory.
func (g *Graph) ReopenImpacts(ctx context.Context, id domain.ChangeID, impacts []domain.ChangeImpactID, comment string) (reopened []domain.ChangeImpactID, err error) {
	comment = strings.TrimSpace(comment)
	if comment == "" {
		return nil, fmt.Errorf("reopening a review needs a comment: %w", ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		reopened = nil
		c, err := changeOpen(ctx, tx, id)
		if err != nil {
			return err
		}
		for _, imp := range impacts {
			i, err := findChangeImpact(c, imp)
			if err != nil {
				return err
			}
			cn := c.Nodes[i]
			if cn.Review != domain.ReviewAccepted && cn.Review != domain.ReviewRejected {
				continue
			}
			r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: comment}
			if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r}); err != nil {
				return err
			}
			reopened = append(reopened, cn.ID)
		}
		return nil
	})
	return
}

// retarget points a link to the post version of a node this change produced
// when the link targets its pre version.
func retarget(nodes []domain.ChangeImpact, to domain.NodeRef) domain.NodeRef {
	for _, cn := range nodes {
		if cn.Pre != nil && cn.Post != nil && *cn.Pre == to {
			return *cn.Post
		}
	}
	return to
}
