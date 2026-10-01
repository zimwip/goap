package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// Apply lands a change: the versions its accepted change impacts wrote on its branch
// (ADR 0024) are checked (properties, lifecycle, transitions) and merged into the
// branch it was forked from, giving the resulting baseline. A change without a branch
// of its own of accepted change impacts applies as an empty baseline.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	authorized, activityMet, err := g.authorizeMoves(ctx, id)
	if err != nil {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		if c, err := tx.Change(ctx, id); err != nil {
			return err
		} else if of := openFlows(c); len(of) > 0 {
			return fmt.Errorf("change %s has an open flow (%s): adopt or discard it first: %w", id, of[0].ID, ErrConflict)
		} else if pd := pendingDecisions(c, g.now()); len(pd) > 0 {
			return fmt.Errorf("change %s has a pending decision point (%q): decide it first: %w", id, pd[0].Question, ErrConflict)
		}
		if open, err := openSubChanges(ctx, tx, id); err != nil {
			return err
		} else if len(open) > 0 {
			return fmt.Errorf("change %s has %d open sub-change(s), apply or abandon them first: %w", id, len(open), ErrConflict)
		}
		if result, err = g.applyTx(ctx, tx, id, baselineName, authorized, activityMet); err != nil {
			return err
		}
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		// A change with its own branch is merged into the branch it was forked
		// from once applied; conflicts leave it merge_pending (see MergeChange).
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil || !ok {
			return err
		}
		c, err = g.integrate(ctx, tx, c, own, nil)
		if err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied {
			if result, err = tx.Baseline(ctx, c.ResultBaselineID); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// errCollected rolls back the pass that only collects the transitions to authorize.
var errCollected = errors.New("transitions collected")

// authorizeMoves asks the authorizer about every lifecycle transition the change makes, and ActivityGoalsMet
// about the change's own activity (if any), before the transaction that applies it: both may need to read the
// graph themselves (the access graph; the methodology namespace), which a transaction held by the apply would
// block (the stores are not reentrant). A pass that is rolled back collects what they need; the apply then
// checks it makes good on what was authorized / evaluated.
func (g *Graph) authorizeMoves(ctx context.Context, id domain.ChangeID) (authorized map[string]bool, activityMet *bool, err error) {
	if g.Authorizer == nil && g.ActivityGoalsMet == nil {
		return nil, nil, nil
	}
	var moves []pendingMove
	var activityRef string
	var bb domain.Blackboard
	var haveActivity bool
	// An error other than errCollected is met again by the apply in its own transaction, after the transitions
	// collected before it: those are authorized all the same.
	_ = g.repo.InTx(ctx, func(tx Tx) error {
		a, _, err := g.newApplier(ctx, tx, id)
		if err != nil {
			return err
		}
		a.collect = &moves
		if err := a.prepareChangeImpacts(); err != nil {
			return err
		}
		if err := a.checkChangeImpacts(); err != nil {
			return err
		}
		if a.activityGated() {
			activityRef, bb, haveActivity = a.change.ActivityRef, a.activityBlackboard(), true
		}
		return errCollected
	})
	authorized = map[string]bool{}
	for _, m := range moves {
		// moves is only ever populated when g.Authorizer is set (checkChangeImpacts collects transitions under
		// the same guard it uses to call the authorizer directly).
		if err := g.Authorizer(ctx, m.node, m.t); err != nil {
			return nil, nil, err
		}
		authorized[m.key()] = true
	}
	if haveActivity {
		met, err := g.ActivityGoalsMet(ctx, activityRef, bb)
		if err != nil {
			return nil, nil, err
		}
		activityMet = &met
	}
	return authorized, activityMet, nil
}

// pendingMove is a lifecycle transition a change makes, from the state of node.
type pendingMove struct {
	node domain.Node
	t    domain.Transition
}

func (m pendingMove) key() string {
	return fmt.Sprintf("%s@%d:%s>%s", m.node.ID, m.node.Version, m.t.From, m.t.To)
}

// newApplier prepares what applying a change starts from: its reference baseline, or the head of its own branch.
func (g *Graph) newApplier(ctx context.Context, tx Tx, id domain.ChangeID) (*applier, domain.BaselineID, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeMergePending {
		return nil, "", fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	base, err := tx.Baseline(ctx, c.BaselineID)
	if err != nil {
		return nil, "", err
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, "", err
	}
	target, parentBaseline := maps.Clone(base.Nodes), base.ID
	// A change on its own branch starts from the head of that branch: what its
	// sub-changes merged into it since the fork is part of the result.
	if _, isOwn, err := ownBranch(ctx, tx, c); err != nil {
		return nil, "", err
	} else if isOwn {
		head, err := branchHead(ctx, tx, c.Namespace, c.Branch)
		if err != nil {
			return nil, "", err
		}
		target, parentBaseline = maps.Clone(head.Nodes), head.ID
	}
	return &applier{g: g, tx: tx, ctx: ctx, change: c, ix: ix, branch: domain.BranchOf(c.Branch), target: target}, parentBaseline, nil
}

// applyTx applies a change; authorized are the transitions authorizeMoves let through (nil: no authorizer).
func (g *Graph) applyTx(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string, authorized map[string]bool, activityMet *bool) (domain.Baseline, error) {
	a, parentBaseline, err := g.newApplier(ctx, tx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	a.authorized = authorized
	a.activityMet = activityMet
	c := a.change
	if err := a.prepareChangeImpacts(); err != nil {
		return domain.Baseline{}, err
	}
	if err := a.checkChangeImpacts(); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.runNodeValidators(ctx, tx, a.target, a.cposts); err != nil {
		return domain.Baseline{}, err
	}
	if baselineName == "" {
		baselineName = c.Title
	}
	result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: baselineName, Namespace: c.Namespace, Branch: a.branch, ParentID: parentBaseline, ChangeID: c.ID, Nodes: a.target, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, result); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.advanceBranch(ctx, tx, c.Namespace, a.branch, result.ID); err != nil {
		return domain.Baseline{}, err
	}
	c.Status = domain.ChangeApplied
	c.ResultBaselineID = result.ID
	return result, tx.PutChange(ctx, c)
}

// advanceBranch moves the head of a branch (main is created on first use).
func (g *Graph) advanceBranch(ctx context.Context, tx Tx, namespace, name string, head domain.BaselineID) error {
	namespace = domain.NamespaceOf(namespace)
	b, err := tx.Branch(ctx, namespace, name)
	switch {
	case errors.Is(err, ErrNotFound) && name == domain.MainBranch:
		b = domain.Branch{Name: name, Namespace: namespace, Status: domain.BranchOpen, CreatedAt: g.now()}
	case err != nil:
		return err
	}
	b.Head = head
	return tx.PutBranch(ctx, b)
}

// nextVersion returns the next version number of a node (numbered across branches).
func nextVersion(ctx context.Context, tx Tx, id domain.NodeID) (domain.Version, error) {
	vs, err := tx.Versions(ctx, id)
	if err != nil {
		return 0, err
	}
	return domain.Version(len(vs) + 1), nil
}

// applier checks and gathers what a change lands: the versions its accepted change impacts wrote.
type applier struct {
	g      *Graph
	tx     Tx
	ctx    context.Context
	change domain.Change
	ix     *typeIndex
	branch string
	target map[domain.NodeID]domain.Version
	// cposts are the versions produced by the accepted change impacts (ADR 0024).
	cposts []cpost
	// collect gathers the transitions to authorize (the pass of authorizeMoves); authorized are the ones let through
	collect    *[]pendingMove
	authorized map[string]bool
	// activityMet is the result of ActivityGoalsMet, evaluated by authorizeMoves's rolled-back pass before this
	// transaction (same reason as authorized: the hook may itself read the graph); nil in that earlier pass
	// itself, where checkChangeImpacts only builds the blackboard for the caller to evaluate outside it.
	activityMet *bool
}
