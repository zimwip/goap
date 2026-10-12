package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
	"github.com/zimwip/goap/pkg/operations"
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
var ErrNothingToCommit = fmt.Errorf("nothing to commit: %w", ErrInvalid)

// Apply commits what the change has pending, then merges it (IntegrateChange). It returns the baseline the change left
// on the branch it was forked from, or its last commit when the merge waits for a resolution.
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	if _, err := g.CommitChange(ctx, id, baselineName); err != nil && !errors.Is(err, ErrNothingToCommit) {
		return result, err
	}
	c, err := g.IntegrateChange(ctx, id, nil)
	if err != nil || c.ResultBaselineID == "" {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if result, err = tx.Baseline(ctx, c.ResultBaselineID); err != nil {
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
func (g *Graph) CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (result domain.Baseline, err error) {
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
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
		result, err = g.commitOnBranch(ctx, tx, id, baselineName)
		return err
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
	base, err := tx.Baseline(ctx, c.BaselineID)
	if err != nil {
		return nil, "", err
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, "", err
	}
	target, parentBaseline := maps.Clone(base.Nodes), base.ID
	// A change on its own branch starts from the head of that branch (moved by its own commits and by the merges of its
	// sub-changes, ADR 0102, 0103).
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
	result := domain.Baseline{ID: domain.BaselineID(g.newID()), Name: baselineName, Namespace: c.Namespace, Branch: a.branch, ParentID: parentBaseline, ChangeID: c.ID, Kind: domain.BaselineCommit, Nodes: a.target, CreatedAt: g.now()}
	if reverted > 0 {
		result.Kind = domain.BaselineRevert // a revert is no landed event: the state is stored whole
	}
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
			a.target[id] = v
		} else {
			delete(a.target, id)
		}
	}
	return len(ids), err
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
	// impact is the change impact of the node a transition moves (ImpactNodeTransition): its guard sees it (ADR 0076),
	// draft the draft of the node in the state it goes to and drafts the drafts the flow sees (ADR 0079).
	impact *domain.ChangeImpact
	draft  *domain.Draft
	drafts *draftReader
	// events caches the impact log of the change for the walk of the transitions
	events *[]domain.ImpactEvent
}
