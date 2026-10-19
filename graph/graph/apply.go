package graph

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/events"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/operations"
)

// A change lands in two acts (ADR 0056, 0102), the same for every change and every caller:
//
//   - CommitChange writes the modifications the change accepted on its own branch, as versions (ADR 0079), and reverts
//     there the nodes it no longer changes (an impact withdrawn after its commit). It checks the drafts as versions (their
//     attributes, validators, required links, the moves of their lifecycle) and nothing else: neither the methodology
//     nor the target branch. The change stays open; it is edited and committed again as often as needed. A commit that
//     writes and reverts nothing is refused (ErrNothingToCommit). A sub-change is a change like any other, whose branch
//     is forked from the branch of its parent change (ADR 0103).
//   - IntegrateChange (the merge) takes the work into what the change was forked from, once everything in it is
//     settled: every impact committed, no open flow, decision point or sub-change. It first rebases its branch onto the
//     head of its target (the branch it was forked from: main, a release branch, the branch of its parent change;
//     rebase_committed.go), asks the guardian of the change (mergecheck.go: its lifecycle, the say of its methodology,
//     the landable states), then fast-forwards; the change is applied, closed, and a sub-change's parent holds its work
//     as its own (recordInParent). Only a root change's target moving in between falls back to the three-way merge,
//     which leaves the change committed until it is resolved.
//
// Apply is both: it commits what is pending, then merges.

// ErrNothingToCommit refuses a commit that would write nothing: no accepted modification, nothing to revert (ADR 0102).
var ErrNothingToCommit = graphapi.ErrNothingToCommit

// Apply commits what the change has pending, then merges it (IntegrateChange). It returns the baseline the change left
// on the branch it was forked from, or its last commit when the merge waits for a resolution.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	return g.apply(ctx, id, baselineName, true)
}

// apply is Apply; without whole the baseline is returned without its state (Commit: reading it costs the size of the
// namespace, ADR 0137).
func (g *Graph) apply(ctx context.Context, id domain.ChangeID, baselineName string, whole bool) (domain.Baseline, error) {
	var result domain.Baseline
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return result, err
	}
	if _, err := g.commitChange(ctx, id, baselineName); err != nil && !errors.Is(err, ErrNothingToCommit) {
		return result, err
	}
	c, err := g.IntegrateChange(ctx, id, nil)
	if err != nil || c.ResultBaselineID == "" {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		read := tx.BaselineHeader
		if whole {
			read = tx.Baseline
		}
		if result, err = read(ctx, c.ResultBaselineID); err != nil {
			return err
		}
		// a name other than the title the change already carries is a tag on the state it leaves (ADR 0056)
		if c.Status == domain.ChangeApplied && baselineName != "" && baselineName != c.Title {
			_, err = g.tagTx(ctx, tx, c, baselineName, "")
		}
		return err
	})
	return result, err
}

// CommitChange writes the accepted modifications of a change on its own branch and reverts there the nodes it no
// longer changes; it returns the commit baseline. The change stays open (ADR 0102).
//
//goap:operation change.commit doer
func (g *Graph) CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return domain.Baseline{}, err
	}
	result, err := g.commitChange(ctx, id, baselineName)
	if err != nil {
		return result, err
	}
	return g.Baseline(ctx, result.ID)
}

// CommitChangeHeader is CommitChange answering the header of the baseline it records, without its state: what a caller
// that does not read the nodes asks (the graph service), at a cost that does not grow with the namespace.
func (g *Graph) CommitChangeHeader(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return domain.Baseline{}, err
	}
	return g.commitChange(ctx, id, baselineName)
}

// commitChange is CommitChange once allowed, its baseline returned without its state (ADR 0137).
func (g *Graph) commitChange(ctx context.Context, id domain.ChangeID, baselineName string) (result domain.Baseline, err error) {
	err = g.changeTx(ctx, id, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		// the change is its branch: one that wrote nothing has none yet
		if _, own, err := ownBranch(ctx, tx, c); err != nil {
			return err
		} else if !own {
			if _, _, err := g.ensureOwnBranch(ctx, tx, c); err != nil {
				return err
			}
		}
		if result, err = g.commitOnBranch(ctx, tx, id, baselineName); err != nil {
			return err
		}
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		g.raiseBaseline(ctx, tx, events.ChangeCommitted, c, result)
		return nil
	})
	return result, err
}

// errCollected rolls back a pass that only reads what an authorization or a hook needs before the transaction that
// writes (the stores are not reentrant).
var errCollected = errors.New("transitions collected")

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
	base, err := headerOf(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, "", err
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, "", err
	}
	parentBaseline := base.ID
	// A change on its own branch starts from the head of that branch (moved by its own commits and by the merges of its
	// sub-changes, ADR 0102, 0103).
	if _, isOwn, err := ownBranch(ctx, tx, c); err != nil {
		return nil, "", err
	} else if isOwn {
		head, err := branchHeadHeader(ctx, tx, c.Namespace, c.Branch)
		if err != nil {
			return nil, "", err
		}
		parentBaseline = head.ID
	}
	// the state it builds on is read node by node, never whole (ADR 0137)
	return &applier{g: g, tx: tx, ctx: ctx, change: c, ix: ix, branch: domain.BranchOf(c.Branch), target: newStateView(ctx, tx, parentBaseline)}, parentBaseline, nil
}

// commitOnBranch writes the accepted modifications of a change on its own branch and reverts the nodes it no longer
// changes, then records its commit baseline there.
func (g *Graph) commitOnBranch(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	a, parentBaseline, err := g.newApplier(ctx, tx, id)
	if err != nil {
		return domain.Baseline{}, err
	}
	c := a.change
	if err := a.prepareChangeImpacts(); err != nil {
		return domain.Baseline{}, err
	}
	reverted, err := a.revertWithdrawn()
	if err != nil {
		return domain.Baseline{}, err
	}
	if len(a.cposts) == 0 && reverted == 0 {
		return domain.Baseline{}, fmt.Errorf("change %s: no accepted change impact modifies a node, and nothing it committed is withdrawn: %w", id, ErrNothingToCommit)
	}
	if _, err := a.walkChangeImpacts(); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.runNodeValidators(ctx, tx, a.target, a.cposts); err != nil {
		return domain.Baseline{}, err
	}
	if baselineName == "" {
		baselineName = c.Title
	}
	result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: baselineName, Namespace: c.Namespace, Branch: a.branch, ParentID: parentBaseline, ChangeID: c.ID, Kind: domain.BaselineCommit, CreatedAt: g.now()}
	if reverted > 0 {
		result.Kind = domain.BaselineRevert // a revert is no landed event: the state is stored whole
	}
	if err := putBaselineOver(ctx, tx, result, parentBaseline, a.target.delta()); err != nil {
		return domain.Baseline{}, err
	}
	if err := g.advanceBranch(ctx, tx, c.Namespace, a.branch, result); err != nil {
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
	// the change stays open (ADR 0102): it is edited and committed again until it is merged
	if c.Status == domain.ChangeDraft {
		c.Status = domain.ChangeActive
	}
	c.ResultBaselineID = result.ID
	return result, tx.PutChange(ctx, c)
}

// revertWithdrawn puts back, in what the commit leaves, the version the branch was forked from for every node the branch
// changed that no change impact holds any more (withdrawn after its commit, ADR 0102); it returns how many.
func (a *applier) revertWithdrawn() (int, error) {
	ids, fork, err := a.g.withdrawnSince(a.ctx, a.tx, a.change)
	for _, id := range ids {
		if v, ok := fork[id]; ok {
			a.target.set(id, v)
		} else {
			a.target.remove(id)
		}
	}
	return len(ids), err
}

// advanceBranch moves the head of a branch to a baseline built on it (main is created on first use). The head moves only
// from the baseline the new one follows (ADR 0127): read under the lock of the branch, a head another transaction moved
// meanwhile is a conflict, never a head silently replaced.
func (g *Graph) advanceBranch(ctx context.Context, tx Tx, namespace, name string, head domain.Baseline) error {
	namespace = domain.NamespaceOf(namespace)
	if err := tx.LockBranch(ctx, namespace, name); err != nil {
		return err
	}
	b, err := tx.Branch(ctx, namespace, name)
	switch {
	case errors.Is(err, ErrNotFound) && name == domain.MainBranch:
		b = domain.Branch{Name: name, Namespace: namespace, Status: domain.BranchOpen, CreatedAt: g.now()}
	case err != nil:
		return err
	}
	if b.Head != head.ParentID {
		return fmt.Errorf("branch %s moved to %s while baseline %s was built on %s: merge again: %w", name, b.Head, head.ID, head.ParentID, ErrConflict)
	}
	b.Head = head.ID
	return tx.PutBranch(ctx, b)
}

// nextVersion returns the next version number of a node (numbered across branches), reading its latest version only.
func nextVersion(ctx context.Context, tx Tx, id domain.NodeID) (domain.Version, error) {
	vs, err := tx.VersionRange(ctx, id, 0, 1)
	if err != nil {
		return 0, err
	}
	if len(vs) == 0 {
		return 0, fmt.Errorf("node %s: %w", id, ErrNotFound)
	}
	return vs[0].Version + 1, nil
}

// applier checks and gathers what a change lands: the versions its accepted change impacts wrote.
type applier struct {
	g      *Graph
	tx     Tx
	ctx    context.Context
	change domain.Change
	ix     *typeIndex
	branch string
	target *stateView
	// cposts are the versions produced by the accepted change impacts (ADR 0024).
	cposts []cpost
	// impact is the change impact of the node a transition moves (ImpactNodeTransition): its guard sees it (ADR 0076),
	// draft the draft of the node in the state it goes to and drafts the drafts the flow sees (ADR 0079).
	impact *domain.ChangeImpact
	draft  *domain.Draft
	drafts *draftReader
	// events caches the impact log of the change for the walk of the transitions
	events *[]domain.ImpactEvent
}
