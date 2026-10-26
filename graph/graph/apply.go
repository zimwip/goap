package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/graphapi"
	"github.com/zimwip/goap/contracts/operations"
)

// A change commits in two acts (ADR 0056, 0102), the same for every change and every caller:
//
//   - CommitChange writes the modifications the change accepted on its own branch, as versions (ADR 0079), and reverts
//     there the nodes it no longer changes (an impact withdrawn after its commit). It checks the drafts as versions (their
//     attributes, validators, required links, the moves of their lifecycle), then asks the guardian of the change
//     (commitcheck.go: the say of its methodology, the committable states, the outer guardians; ADR 0146), never the
//     target branch. The change stays open; it is edited and committed again as often as needed. A commit that
//     writes and reverts nothing is refused (ErrNothingToCommit). A sub-change is a change like any other, whose branch
//     is forked from the branch of its parent change (ADR 0103).
//   - IntegrateChange (the merge) takes the work into what the change was forked from, once everything in it is
//     settled: every impact committed, no open flow, decision point or sub-change. It first rebases its branch onto the
//     head of its target (the branch it was forked from: main, a release branch, the branch of its parent change;
//     rebase_committed.go), checks its dependencies and the final state of its lifecycle (mergecheck.go), then
//     fast-forwards; it asks no methodology (ADR 0146); the change is applied, closed, and a sub-change's parent holds its work
//     as its own (recordInParent). Only a root change's target moving in between falls back to the three-way merge,
//     which leaves the change committed until it is resolved.
//
// The two acts are the only way in: a caller commits (CommitChange), then merges (IntegrateChange). Graph.Commit, the
// producer path, does both at once (apply).

// ErrNothingToCommit refuses a commit that would write nothing: no accepted modification, nothing to revert (ADR 0102).
var ErrNothingToCommit = graphapi.ErrNothingToCommit

// apply commits what the change has pending, then merges it (IntegrateChange): Graph.Commit's landing. It returns the
// header of the baseline the change left on the branch it was forked from (reading its state costs the size of the
// namespace, ADR 0137), none when the merge waits for a resolution.
func (g *Graph) apply(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	var result domain.Baseline
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return result, err
	}
	if _, err := g.commitChange(ctx, id, baselineName, nil); err != nil && !errors.Is(err, ErrNothingToCommit) {
		return result, err
	}
	c, err := g.IntegrateChange(ctx, id, nil)
	if err != nil {
		return result, err
	}
	if c.ResultBaselineID == "" {
		if c.Status != domain.ChangeApplied || c.ParentID == "" {
			return result, nil
		}
		// a sub-change merged into the branch of its parent, which holds no baseline (ADR 0140): its state
		err = g.repo.InTx(ctx, func(tx Tx) error {
			own, _, err := ownBranch(ctx, tx, c)
			if err != nil {
				return err
			}
			p, err := tx.Branch(ctx, own.Namespace, own.Parent)
			if err != nil {
				return err
			}
			result, err = changeBranchHead(ctx, tx, p, false)
			return err
		})
		return result, err
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		result, err = tx.BaselineHeader(ctx, c.ResultBaselineID)
		return err
	})
	return result, err
}

// CommitChange writes the accepted modifications of a change on its own branch and reverts there the nodes it no
// longer changes; it returns the header of the commit baseline, without its state (ADR 0137): a caller that reads the
// nodes asks for them (ChangeNodeView, BranchHead, the versions of the change). The change stays open (ADR 0102).
//
//goap:operation change.commit doer
func (g *Graph) CommitChange(ctx context.Context, id domain.ChangeID, baselineName string) (domain.Baseline, error) {
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return domain.Baseline{}, err
	}
	return g.commitChange(ctx, id, baselineName, nil)
}

// CommitChangeNodes is a partial commit: CommitChange of the nodes named only, each an accepted modification of the
// change or a node withdrawn since its commit (an impact merged or split via one named follows it); none named: every
// one. A node named that the commit would not write is refused (ErrInvalid), so is a version linking to the draft of a
// node left out.
func (g *Graph) CommitChangeNodes(ctx context.Context, id domain.ChangeID, baselineName string, nodes []domain.NodeID) (domain.Baseline, error) {
	if err := g.allow(ctx, id, operations.ChangeCommit); err != nil {
		return domain.Baseline{}, err
	}
	return g.commitChange(ctx, id, baselineName, nodes)
}

// commitChange is CommitChange once allowed, of the nodes named (none: every one), its baseline returned without its
// state (ADR 0137).
func (g *Graph) commitChange(ctx context.Context, id domain.ChangeID, baselineName string, nodes []domain.NodeID) (result domain.Baseline, err error) {
	// one transaction: the guardian judges the versions written and a refusal undoes them (ADR 0146)
	err = g.inChange(ctx, id, func(ctx context.Context) error {
		if err := g.commitTx(ctx, id, baselineName, nodes, &result); err != nil {
			return err
		}
		return g.checkCommit(ctx, id)
	})
	if errors.Is(err, errRebaseLeftConflicts) {
		// the drafts rebased onto the head with conflicts: the rebase is kept, in a transaction of its own, for the
		// person or the agent to settle (ADR 0165)
		err = g.rebaseDrafts(ctx, id, nodes)
	}
	return result, err
}

// errRebaseLeftConflicts stops a commit whose drafts the rebase left with conflicts (rebaseDraftsTx): the commit's
// transaction rolls back, the rebase is made again on its own.
var errRebaseLeftConflicts = errors.New("the rebase of the drafts left conflicts")

// commitTx writes the commit of a change, in the transaction of ctx.
func (g *Graph) commitTx(ctx context.Context, id domain.ChangeID, baselineName string, nodes []domain.NodeID, result *domain.Baseline) error {
	return g.changeTx(ctx, id, func(tx Tx) error {
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
		// what the commit writes is brought up to date with the head first (ADR 0165)
		if _, left, err := g.rebaseDraftsTx(ctx, tx, c, nodes); err != nil {
			return err
		} else if len(left) > 0 {
			return errRebaseLeftConflicts
		}
		if *result, _, err = g.commitOnBranch(ctx, tx, id, baselineName, false, nodes); err != nil {
			return err
		}
		if c, err = tx.Change(ctx, id); err != nil {
			return err
		}
		return nil
	})
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

// newApplier prepares what applying a change starts from: its reference baseline, or the state of its own branch (its
// fork with what it committed, ADR 0140), returned too.
func (g *Graph) newApplier(ctx context.Context, tx Tx, id domain.ChangeID) (*applier, domain.Branch, error) {
	c, err := tx.Change(ctx, id)
	if err != nil {
		return nil, domain.Branch{}, err
	}
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned || c.Status == domain.ChangeCommitted {
		return nil, domain.Branch{}, fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return nil, domain.Branch{}, err
	}
	a := &applier{g: g, tx: tx, ctx: ctx, change: c, ix: ix, branch: domain.BranchOf(c.Branch)}
	// A change on its own branch starts from the state of that branch (moved by its own commits and by the merges of its
	// sub-changes, ADR 0102, 0103); the state it builds on is read node by node, never whole (ADR 0137)
	own, isOwn, err := ownBranch(ctx, tx, c)
	if err != nil {
		return nil, own, err
	}
	if isOwn {
		a.target = newBranchView(ctx, tx, own)
	} else {
		a.target = newStateView(ctx, tx, c.BaselineID)
	}
	return a, own, nil
}

// commitOnBranch writes the accepted modifications of a change on its own branch and reverts the nodes it no longer
// changes. A rebase (mechanical) writes its merges and checks nothing of what they mean (ADR 0146): the lifecycle moves
// and the validators judged the work of each side when it was committed. The branch of a change holds no baseline (ADR 0140): the header returned is the commit as an event tells it,
// its order the commit of the versions written, stored nowhere. Nodes named make it a partial commit (none: every node).
func (g *Graph) commitOnBranch(ctx context.Context, tx Tx, id domain.ChangeID, baselineName string, mechanical bool, nodes []domain.NodeID) (domain.Baseline, []domain.Node, error) {
	a, own, err := g.newApplier(ctx, tx, id)
	if err != nil {
		return domain.Baseline{}, nil, err
	}
	c := a.change
	if len(nodes) > 0 {
		a.only = map[domain.NodeID]bool{}
		for _, n := range nodes {
			a.only[n] = true
		}
		a.taken = map[domain.NodeID]bool{}
	}
	if err := a.prepareChangeImpacts(); err != nil {
		return domain.Baseline{}, nil, err
	}
	reverted, err := a.revertWithdrawn(own)
	if err != nil {
		return domain.Baseline{}, nil, err
	}
	if left := a.notTaken(); len(left) > 0 {
		return domain.Baseline{}, nil, invalidf("change %s: nothing to commit for node(s) %s: neither an accepted modification of the change nor a node withdrawn since its commit", id, strings.Join(left, ", "))
	}
	if len(a.cposts) == 0 && len(reverted) == 0 {
		return domain.Baseline{}, nil, fmt.Errorf("change %s: no accepted change impact modifies a node, and nothing it committed is withdrawn: %w", id, ErrNothingToCommit)
	}
	if !mechanical {
		if _, err := a.walkChangeImpacts(); err != nil {
			return domain.Baseline{}, nil, err
		}
		if err := g.runNodeValidators(ctx, tx, a.target, a.cposts); err != nil {
			return domain.Baseline{}, nil, err
		}
	}
	if baselineName == "" {
		baselineName = c.Title
	}
	result := domain.Baseline{Name: baselineName, Namespace: c.Namespace, Branch: a.branch, ParentID: own.ForkBaseline, ChangeID: c.ID, Kind: domain.BaselineCommit,
		Seq: endCommit(tx, a.branch), CreatedAt: g.now()}
	switch {
	case mechanical:
		result.Kind = domain.BaselineRebase
	case len(reverted) > 0:
		result.Kind = domain.BaselineRevert
	}
	// each version the change applied has committed on its own branch; it commits again on the branch it is integrated into
	// (integrate, commit)
	committed := make([]domain.ImpactEvent, 0, len(a.cposts))
	for _, cp := range a.cposts {
		ref := cp.post.Ref()
		committed = append(committed, domain.ImpactEvent{Change: c.ID, Impact: cp.cn.ID, Op: domain.ImpactCommitted, Committed: &ref, Branch: domain.BranchOf(a.branch)})
	}
	// one entry for the commit (ADR 0144)
	if err := g.emit(ctx, tx, committed...); err != nil {
		return domain.Baseline{}, nil, err
	}
	// the change stays open (ADR 0102): it is edited and committed again until it is merged
	if c, err = tx.Change(ctx, id); err != nil {
		return domain.Baseline{}, nil, err
	}
	if c.Status == domain.ChangeDraft {
		c.Status = domain.ChangeActive
	}
	if err := tx.PutChange(ctx, c); err != nil {
		return domain.Baseline{}, nil, err
	}
	// a commit names the versions it wrote (ADR 0159); the commit of a rebase is told by the rebase (change.rebased)
	wrote := make([]domain.Node, 0, len(a.cposts)+len(reverted))
	for _, cp := range a.cposts {
		wrote = append(wrote, cp.post)
	}
	wrote = append(wrote, reverted...)
	if !mechanical {
		g.raiseCommitted(ctx, tx, c, result, wrote)
	}
	return result, wrote, nil
}

// revertWithdrawn writes, on the branch of a change, a revert of every node the branch changed that no change impact
// holds any more (withdrawn after its commit, ADR 0102): a version with the properties and links of the version the
// branch was forked from (a deletion for a node the fork does not hold), which the branch reads as that version (ADR
// 0140); it returns the versions it wrote.
func (a *applier) revertWithdrawn(own domain.Branch) ([]domain.Node, error) {
	ids, fork, err := a.g.withdrawnSince(a.ctx, a.tx, a.change)
	if err != nil {
		return nil, err
	}
	var wrote []domain.Node
	for _, id := range ids {
		if !a.takes(id) {
			continue
		}
		latest, err := a.tx.LatestOn(a.ctx, id, a.branch)
		if err != nil {
			return nil, err
		}
		next, err := nextVersion(a.ctx, a.tx, id)
		if err != nil {
			return nil, err
		}
		n := domain.Node{ID: id, Version: next, Branch: a.branch, Namespace: latest.Namespace, Key: latest.Key, Type: latest.Type, ChangeID: a.change.ID,
			Reason: domain.ReasonRevert, Parents: []domain.Version{latest.Version}, Owner: latest.Owner, Project: latest.Project,
			Comment: "reverted: its impact was withdrawn from the change", CreatedAt: a.g.now()}
		v, inFork := fork[id]
		var links []domain.Link
		like := []domain.NodeRef{latest.Ref()}
		if inFork {
			src, err := a.tx.Node(a.ctx, domain.NodeRef{ID: id, Version: v})
			if err != nil {
				return nil, err
			}
			n.Properties, n.State, n.Owner, n.Origins = cloneMap(src.Properties), src.State, src.Owner, slices.Clone(src.Origins)
			if links, err = a.tx.OutLinks(a.ctx, src.Ref()); err != nil {
				return nil, err
			}
			like = []domain.NodeRef{src.Ref(), latest.Ref()} // the set of the fork's version, shared as is (ADR 0141)
			a.target.set(id, v)
		} else {
			n.Properties, n.State, n.Deleted = cloneMap(latest.Properties), latest.State, true
			a.target.remove(id)
		}
		if err := a.tx.PutNode(a.ctx, n); err != nil {
			return nil, err
		}
		if err := a.g.putLinks(a.ctx, a.tx, n, links, like...); err != nil {
			return nil, err
		}
		wrote = append(wrote, n)
	}
	return wrote, nil
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

// applier checks and gathers what a change commits: the versions its accepted change impacts wrote.
type applier struct {
	g      *Graph
	tx     Tx
	ctx    context.Context
	change domain.Change
	ix     *typeIndex
	branch string
	// latest is the latest version of the nodes the commit writes, latestOn their latest on branch
	latest, latestOn map[domain.NodeID]domain.Version
	target           *stateView
	// cposts are the versions produced by the accepted change impacts (ADR 0024).
	cposts []cpost
	// impact is the change impact of the node a transition moves (ImpactNodeTransition): its guard sees it (ADR 0076),
	// draft the draft of the node in the state it goes to and drafts the drafts the flow sees (ADR 0079).
	impact *domain.ChangeImpact
	draft  *domain.Draft
	drafts *draftReader
	// events caches the impact log of the change for the walk of the transitions
	events *[]domain.ImpactEvent
	// only names the nodes a partial commit writes (nil: every one), taken those it found to write
	only, taken map[domain.NodeID]bool
}

// takes reports whether the commit writes the node, noting it found when it is one named.
func (a *applier) takes(id domain.NodeID) bool {
	if a.only == nil {
		return true
	}
	if a.only[id] {
		a.taken[id] = true
		return true
	}
	return false
}

// takesImpact reports whether the commit writes the node of an impact: its node is named, or the impact it was merged or
// split via is.
func (a *applier) takesImpact(cn domain.ChangeImpact, impacts []domain.ChangeImpact) bool {
	if a.only == nil || a.takes(nodeOf(cn)) {
		return true
	}
	for _, p := range impacts {
		if cn.Via != "" && p.ID == cn.Via {
			return a.takesImpact(p, impacts)
		}
	}
	return false
}

// notTaken lists the nodes named that the commit found nothing to write for.
func (a *applier) notTaken() []string {
	var out []string
	for id := range a.only {
		if !a.taken[id] {
			out = append(out, string(id))
		}
	}
	slices.Sort(out)
	return out
}
