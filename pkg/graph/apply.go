package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"github.com/zimwip/goap/pkg/domain"
)

// A change lands in two acts (ADR 0056, 0102), the same for every change and every caller:
//
//   - CommitChange writes the modifications the change accepted on its own branch, as versions (ADR 0079), and reverts
//     there the nodes it no longer changes (an impact withdrawn after its commit). It checks the drafts as versions (their
//     attributes, validators, required links, the moves of their lifecycle) and nothing else: neither the methodology
//     nor the target branch. The change stays open; it is edited and committed again as often as needed. A commit that
//     writes and reverts nothing is refused (ErrNothingToCommit). A sub-change has no branch of its own: it does not
//     commit, its merge takes its accepted modifications into its parent (ADR 0081).
//   - IntegrateChange (the merge) takes the work into what the change was forked from, once everything in it is
//     settled: every impact committed (a sub-change: accepted), no open flow, decision point or sub-change. It first
//     brings the change up to date (a root change: its branch rebased onto the head of its target, rebase_committed.go;
//     a sub-change: rebased onto its parent, ADR 0082), asks the merge guards (mergeguard.go: the methodology's say),
//     then fast-forwards; the change is applied, closed. Only a target moving in between falls back to the three-way
//     merge, which leaves the change committed until it is resolved.
//
// Apply is both: it commits what is pending, then merges.

// ErrNothingToCommit refuses a commit that would write nothing: no accepted modification, nothing to revert (ADR 0102).
var ErrNothingToCommit = fmt.Errorf("nothing to commit: %w", ErrInvalid)

// Apply commits what the change has pending, then merges it (IntegrateChange). It returns the baseline the change left
// on the branch it was forked from, or its last commit when the merge waits for a resolution; nothing for a sub-change,
// which lands in its parent (ADR 0081).
func (g *Graph) Apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	c, err := g.Change(ctx, id)
	if err != nil {
		return result, err
	}
	if c.ParentID == "" {
		if _, err := g.CommitChange(ctx, id, baselineName); err != nil && !errors.Is(err, ErrNothingToCommit) {
			return result, err
		}
	}
	if c, err = g.IntegrateChange(ctx, id, nil); err != nil || c.ResultBaselineID == "" {
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

// CommitChange writes the accepted modifications of a root change on its own branch and reverts there the nodes it no
// longer changes; it returns the commit baseline. The change stays open (ADR 0102).
func (g *Graph) CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (result domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c.ParentID != "" {
			return fmt.Errorf("change %s is a sub-change: it has no branch of its own, merge it into its parent: %w", id, ErrInvalid)
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
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil {
		return domain.Baseline{}, err
	}
	reverted := 0
	if ok {
		if reverted, err = a.revertWithdrawn(own); err != nil {
			return domain.Baseline{}, err
		}
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
	if ok {
		result.RebasedFrom = own.RebasedFrom // the commit of a rebased change keeps where it was forked from before (ADR 0101 §4)
	}
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
func (a *applier) revertWithdrawn(own domain.Branch) (int, error) {
	fork, err := a.tx.Baseline(a.ctx, own.ForkBaseline)
	if err != nil {
		return 0, err
	}
	held := map[domain.NodeID]bool{}
	for _, cn := range a.mainImpacts() {
		held[nodeOf(cn)] = true
	}
	n := 0
	for id, v := range a.target {
		if held[id] || fork.Nodes[id] == v {
			continue
		}
		if fv, ok := fork.Nodes[id]; ok {
			a.target[id] = fv
		} else {
			delete(a.target, id)
		}
		n++
	}
	return n, nil
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
