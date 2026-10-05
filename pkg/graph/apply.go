package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// A change is applied in two steps (ADR 0056), two concepts that Apply runs one after the other:
//
//   - CommitChange validates it: every rule of a change (reviews, properties, lifecycle and transitions, validators,
//     the activity goals) is checked against the versions its accepted change impacts wrote, and the state it leaves
//     is recorded on its own branch (its commit baseline, one `landed` event per impact). That depends on the change
//     alone. The change is then `committed`.
//   - IntegrateChange integrates a committed change into the branch it was forked from (fast-forward or 3-way
//     merge, a baseline on that branch, `landed` again on it), and the change is `applied`. Conflicts without a
//     resolution leave it committed: the integration waits.
//
// Apply is both, in one transaction. The branch of a change is its workspace: a change without one gets it when it is
// committed (a change that wrote a node has it already).

// Apply commits a change and integrates it into the branch it was forked from; it returns the baseline the change
// left on the target branch, or its commit baseline when the integration waits for a resolution.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	if err := g.checkFinalState(ctx, id); err != nil {
		return result, err
	}
	authorized, landing, err := g.authorizeMoves(ctx, id)
	if err != nil {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		if result, err = g.commitTx(ctx, tx, id, baselineName, authorized, landing); err != nil {
			return err
		}
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c, err = g.integrateTx(ctx, tx, c, nil); err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied {
			if result, err = tx.Baseline(ctx, c.ResultBaselineID); err != nil {
				return err
			}
			// a name other than the title the change already carries is a tag on the state it leaves (ADR 0056)
			if baselineName != "" && baselineName != c.Title {
				_, err = g.tagTx(ctx, tx, c, baselineName, "")
			}
		}
		return err
	})
	return result, err
}

// CommitChange validates a change and records the state it leaves on its own branch: the change is committed, not
// integrated; IntegrateChange does that. It returns the commit baseline.
func (g *Graph) CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	if err := g.checkFinalState(ctx, id); err != nil {
		return result, err
	}
	authorized, landing, err := g.authorizeMoves(ctx, id)
	if err != nil {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		result, err = g.commitTx(ctx, tx, id, baselineName, authorized, landing)
		return err
	})
	return result, err
}

// commitTx validates a change and records its commit baseline on its own branch.
func (g *Graph) commitTx(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string, authorized map[string]bool, landing *landingDecision) (domain.Baseline, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	if of := openFlows(c); len(of) > 0 {
		return domain.Baseline{}, fmt.Errorf("change %s has an open flow (%s): adopt or discard it first: %w", id, of[0].ID, ErrConflict)
	} else if pd := pendingDecisions(c, g.now()); len(pd) > 0 {
		return domain.Baseline{}, fmt.Errorf("change %s has a pending decision point (%q): decide it first: %w", id, pd[0].Question, ErrConflict)
	}
	if open, err := openSubChanges(ctx, tx, id); err != nil {
		return domain.Baseline{}, err
	} else if len(open) > 0 {
		return domain.Baseline{}, fmt.Errorf("change %s has %d open sub-change(s), apply or abandon them first: %w", id, len(open), ErrConflict)
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeCommitted {
		return domain.Baseline{}, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	// the change is its branch: one that wrote nothing has none yet
	if _, own, err := ownBranch(ctx, tx, c); err != nil {
		return domain.Baseline{}, err
	} else if !own {
		if _, _, err := g.ensureOwnBranch(ctx, tx, c); err != nil {
			return domain.Baseline{}, err
		}
	}
	return g.commitOnBranch(ctx, tx, id, baselineName, authorized, landing)
}

// errCollected rolls back the pass that only collects the transitions to authorize.
var errCollected = errors.New("transitions collected")

// landingDecision is what Graph.LandingGate answered about a change.
type landingDecision struct{ decided, ok bool }

// authorizeMoves asks the authorizer about every lifecycle transition the change makes, and LandingGate about the
// change itself, before the transaction that applies it: both may need to read the graph themselves (the access
// graph; whatever the gate consults), which a transaction held by the apply would block (the stores are not
// reentrant). A pass that is rolled back collects what they need; the apply then checks it makes good on what was
// authorized / decided.
func (g *Graph) authorizeMoves(ctx context.Context, id domain.ChangeID) (authorized map[string]bool, landing *landingDecision, err error) {
	if g.Authorizer == nil && g.LandingGate == nil {
		return nil, nil, nil
	}
	var moves []pendingMove
	var change domain.Change
	var bb domain.Blackboard
	var haveBB bool
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
		if g.LandingGate != nil {
			change, bb, haveBB = a.change, a.landingBlackboard(), true
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
	if haveBB {
		decided, ok, err := g.LandingGate(ctx, change, bb)
		if err != nil {
			return nil, nil, err
		}
		landing = &landingDecision{decided: decided, ok: ok}
	}
	return authorized, landing, nil
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
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeCommitted {
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

// commitOnBranch validates a change on its own branch and records its commit baseline there; authorized are the
// transitions authorizeMoves let through (nil: no authorizer).
func (g *Graph) commitOnBranch(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string, authorized map[string]bool, landing *landingDecision) (domain.Baseline, error) {
	a, parentBaseline, err := g.newApplier(ctx, tx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	a.authorized = authorized
	a.landing = landing
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
	result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: baselineName, Namespace: c.Namespace, Branch: a.branch, ParentID: parentBaseline, ChangeID: c.ID, Kind: domain.BaselineCommit, Nodes: a.target, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, result); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.advanceBranch(ctx, tx, c.Namespace, a.branch, result.ID); err != nil {
		return domain.Baseline{}, err
	}
	// each version the change applied has landed on its own branch; it lands again on the branch it is integrated into
	// (integrate, land)
	for _, cp := range a.cposts {
		ref := cp.post.Ref()
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cp.cn.ID, Op: domain.ImpactLanded, Landed: &ref, Baseline: result.ID}); err != nil {
			return domain.Baseline{}, err
		}
	}
	c.Status = domain.ChangeCommitted
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
	// landing is the answer of LandingGate, asked by authorizeMoves's rolled-back pass before this transaction
	// (same reason as authorized: the hook may itself read the graph); nil in that earlier pass itself, where
	// checkChangeImpacts only leaves the blackboard for the caller to evaluate outside it.
	landing *landingDecision
}
