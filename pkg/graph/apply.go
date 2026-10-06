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
// left on the target branch, or its commit baseline when the integration waits for a resolution. A sub-change is
// integrated into its parent change and leaves no baseline (ADR 0081): the result is empty.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	if err := g.checkFinalState(ctx, id); err != nil {
		return result, err
	}
	landing, err := g.askLandingGate(ctx, id)
	if err != nil {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		if result, err = g.commitTx(ctx, tx, id, baselineName, landing); err != nil {
			return err
		}
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c, err = g.integrateTx(ctx, tx, c, nil); err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied && c.ResultBaselineID != "" { // a sub-change leaves no baseline (ADR 0081)
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
	landing, err := g.askLandingGate(ctx, id)
	if err != nil {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) (err error) {
		result, err = g.commitTx(ctx, tx, id, baselineName, landing)
		return err
	})
	return result, err
}

// commitTx validates a change and records its commit baseline on its own branch.
func (g *Graph) commitTx(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string, landing *landingDecision) (domain.Baseline, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	if of := openFlows(c); len(of) > 0 {
		return domain.Baseline{}, fmt.Errorf("change %s has an open flow (%s): adopt or discard it first: %w", id, of[0].ID, ErrConflict)
	} else if pd := pendingDecisions(c, g.now(), g.DecisionPolicy); len(pd) > 0 {
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
	if c.ParentID != "" {
		// a sub-change lands in its parent's log, it writes no version (ADR 0081)
		return domain.Baseline{}, g.commitSubTx(ctx, tx, c, landing)
	}
	// the change is its branch: one that wrote nothing has none yet
	if _, own, err := ownBranch(ctx, tx, c); err != nil {
		return domain.Baseline{}, err
	} else if !own {
		if _, _, err := g.ensureOwnBranch(ctx, tx, c); err != nil {
			return domain.Baseline{}, err
		}
	}
	return g.commitOnBranch(ctx, tx, id, baselineName, landing)
}

// errCollected rolls back a pass that only reads what an authorization or a hook needs before the transaction that
// writes (the stores are not reentrant).
var errCollected = errors.New("transitions collected")

// landingDecision is what Graph.LandingGate answered about a change.
type landingDecision struct{ decided, ok bool }

// askLandingGate asks LandingGate about the change before the transaction that applies it: the gate may read the graph
// itself, which a transaction held by the apply would block (the stores are not reentrant). A pass that is rolled back
// builds the blackboard it decides against. The lifecycle transitions are authorized when they are taken
// (ImpactNodeTransition, ADR 0076), not here.
func (g *Graph) askLandingGate(ctx context.Context, id domain.ChangeID) (landing *landingDecision, err error) {
	if g.LandingGate == nil {
		return nil, nil
	}
	var change domain.Change
	var bb domain.Blackboard
	var haveBB bool
	var subErr error
	// An error other than errCollected is met again by the apply in its own transaction.
	_ = g.repo.InTx(ctx, func(tx Tx) error {
		if c, err := tx.Change(ctx, id); err == nil && c.ParentID != "" {
			// a sub-change writes no version (ADR 0081): the gate decides on its drafts
			change = c
			if bb, subErr = g.subLandingBlackboard(ctx, tx, c); subErr == nil {
				haveBB = true
			}
			return errCollected
		}
		a, _, err := g.newApplier(ctx, tx, id)
		if err != nil {
			return err
		}
		a.collect = true
		if err := a.prepareChangeImpacts(); err != nil {
			return err
		}
		if err := a.checkChangeImpacts(); err != nil {
			return err
		}
		change, bb, haveBB = a.change, a.landingBlackboard(), true
		return errCollected
	})
	if subErr != nil {
		return nil, subErr
	}
	if !haveBB {
		return nil, nil
	}
	decided, ok, err := g.LandingGate(ctx, change, bb)
	if err != nil {
		return nil, err
	}
	return &landingDecision{decided: decided, ok: ok}, nil
}

// pendingMove is a lifecycle transition, from the state of node.
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
	// A change on its own branch starts from the head of that branch (its sub-changes land in its log, ADR 0081: the
	// head moves only by other means).
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

// commitOnBranch validates a change on its own branch and records its commit baseline there.
func (g *Graph) commitOnBranch(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string, landing *landingDecision) (domain.Baseline, error) {
	a, parentBaseline, err := g.newApplier(ctx, tx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
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
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cp.cn.ID, Op: domain.ImpactLanded, Landed: &ref, Baseline: result.ID, Branch: domain.BranchOf(a.branch)}); err != nil {
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
	// collect marks the rolled-back pass of askLandingGate, which only builds the blackboard the gate decides against
	collect bool
	// landing is the answer of LandingGate, asked by askLandingGate's rolled-back pass before this transaction (the
	// hook may itself read the graph).
	landing *landingDecision
	// impact is the change impact of the node a transition moves (ImpactNodeTransition): its guard sees it (ADR 0076),
	// draft the draft of the node in the state it goes to and drafts the drafts the flow sees (ADR 0079).
	impact *domain.ChangeImpact
	draft  *domain.Draft
	drafts *draftReader
	// events caches the impact log of the change for the walk of the transitions
	events *[]domain.ImpactEvent
}
