package graph

import (
	"context"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Rebasing a change (ADR 0101, 0102, 0103): what it committed on its branch is brought onto the head of the branch it was
// forked from (main, a release branch, the branch of its parent change), the nodes changed on both sides merged
// three-way (domain.MergeDrafts, rebaseCommittedTx); the fields changed on both sides are conflicts the change settles by
// editing its draft, and its merge is then a fast-forward. A change that committed nothing has nothing to rebase.

// RebasedImpact is what a rebase did to a change impact.
type RebasedImpact struct {
	Impact domain.ChangeImpactID `json:"impactId"`
	Key    string                `json:"key"`
	// Changed is set when its review went back to proposed: the merged draft of a sub-change differs from its own, a
	// root change has conflicts (a clean rebase keeps the reviews, ADR 0101 §2).
	Changed bool `json:"changed"`
	// Merged is set when the draft took values from the target (a root change).
	Merged bool `json:"merged,omitempty"`
	// Conflicts are the fields changed on both sides (domain.ConflictProp, ...), kept as the sub-change had them.
	Conflicts []string `json:"conflicts,omitempty"`
}

// RebaseResult is what RebaseChange did.
type RebaseResult struct {
	// Parent is the parent change of a sub-change, empty for a root change.
	Parent domain.ChangeID `json:"parentId"`
	// Branch is the target branch of a root change; From the baseline it was forked from and Onto the head it moved to
	// (equal: it was not behind).
	Branch  string            `json:"branch,omitempty"`
	From    domain.BaselineID `json:"from,omitempty"`
	Onto    domain.BaselineID `json:"onto,omitempty"`
	Impacts []RebasedImpact   `json:"impacts,omitempty"`
}

// Changed reports a rebase that left conflicts: the change has impacts to settle and review again.
func (r RebaseResult) Changed() bool {
	for _, i := range r.Impacts {
		if i.Changed || len(i.Conflicts) > 0 {
			return true
		}
	}
	return false
}

func (r RebaseResult) summary() string {
	var review, conflicts []string
	for _, i := range r.Impacts {
		if i.Changed || len(i.Conflicts) > 0 {
			review = append(review, i.Key)
		}
		for _, c := range i.Conflicts {
			conflicts = append(conflicts, i.Key+" "+c)
		}
	}
	out := fmt.Sprintf("rebased onto branch %s: review again %s", r.Branch, strings.Join(review, ", "))
	if len(conflicts) > 0 {
		out += "; settle the conflicts " + strings.Join(conflicts, ", ")
	}
	return out
}

// RebaseChange brings what a change committed up to date with the head of the branch it was forked from (the branch of
// its parent change for a sub-change, ADR 0103).
func (g *Graph) RebaseChange(ctx context.Context, id domain.ChangeID) (res RebaseResult, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		res, err = g.rebaseTx(ctx, tx, c)
		return err
	})
	return
}

// rebaseTx is RebaseChange inside a transaction.
func (g *Graph) rebaseTx(ctx context.Context, tx Tx, c domain.Change) (RebaseResult, error) {
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned:
		return RebaseResult{Parent: c.ParentID}, fmt.Errorf("change %s is %s: %w", c.ID, c.Status, ErrConflict)
	}
	own, ok, err := ownBranch(ctx, tx, c)
	if err != nil || !ok || !hasCommits(c) {
		return RebaseResult{Parent: c.ParentID}, err
	}
	res, err := g.rebaseCommittedTx(ctx, tx, c, own)
	res.Parent = c.ParentID
	return res, err
}

// numberLinks gives the links a merge brought (they have no id) one.
func (g *Graph) numberLinks(d *domain.Draft) {
	for i := range d.Links {
		if d.Links[i].ID == "" {
			d.Links[i].ID = domain.LinkID(g.newID())
		}
	}
}

// installRebased replaces the draft of a change impact by the merged one (a transitioned event carrying it, with the
// rebase and its conflicts in the patch), moves its pre with it when pre is given, and sends its review back to
// proposed when asked (the reason is the comment).
func (g *Graph) installRebased(ctx context.Context, tx Tx, c domain.Change, cn domain.ChangeImpact, d, merged domain.Draft, pre *domain.NodeRef, rebased map[string]any, conflicts []string, backToProposed bool, why string) error {
	ref := merged.Ref()
	patch := map[string]any{"rebased": rebased, "conflicts": conflicts}
	if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactTransitioned, Execution: d.Execution, Post: &ref, Draft: &merged, Pre: pre, Patch: patch}); err != nil {
		return err
	}
	if !backToProposed || cn.Review == domain.ReviewProposed {
		return nil
	}
	r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: why}
	return g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r})
}

// HeaderBaselineID is the field of a change.updated log entry that records the move of a rebase (ADR 0101).
const HeaderBaselineID = "baselineId"

// storedDraft is a stored version seen as a draft, with its outgoing links: what a three-way merge compares.
func (g *Graph) storedDraft(ctx context.Context, tx Tx, ref domain.NodeRef) (domain.Draft, error) {
	n, err := tx.Node(ctx, ref)
	if err != nil {
		return domain.Draft{}, err
	}
	links, err := tx.OutLinks(ctx, n.Ref())
	if err != nil {
		return domain.Draft{}, err
	}
	b := n.Ref()
	out := domain.Draft{Node: n.ID, Key: n.Key, Type: n.Type, State: n.State, Owner: n.Owner, Properties: cloneMap(n.Properties), Base: &b}
	for _, l := range links {
		out.Links = append(out.Links, domain.DraftLink{ID: l.ID, Type: l.Type, To: l.To, Properties: cloneMap(l.Properties)})
	}
	return out, nil
}

// ImpactNodeResolve settles the conflicts a rebase left on a change impact by keeping the change's values (ADR 0082 §2):
// an updated event with {"resolved": true}. The impact is then reviewed as any.
func (g *Graph) ImpactNodeResolve(ctx context.Context, id domain.ChangeID, impact domain.ChangeImpactID, execution string) (cn domain.ChangeImpact, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		w, err := g.workOn(ctx, tx, id, domain.MainFlow)
		if err != nil {
			return err
		}
		if err := g.impact(ctx, tx, w, impact); err != nil {
			return err
		}
		d, err := g.working(ctx, tx, w)
		if err != nil {
			return err
		}
		events, err := impactEvents(ctx, tx, id)
		if err != nil {
			return err
		}
		if len(domain.PendingConflicts(events, impact)) == 0 {
			return fmt.Errorf("change impact %s (%s) has no conflict to settle: %w", impact, w.cn.Key, ErrConflict)
		}
		ref := d.Ref()
		if err := g.emitUpdated(ctx, tx, w, ref, execution, map[string]any{"resolved": true}); err != nil {
			return err
		}
		cn = w.seenAs(&ref)
		return nil
	})
	return
}

// checkSettled refuses to accept a change impact whose conflicts from a rebase are not settled (ADR 0082 §2, ADR 0101 §2).
func (g *Graph) checkSettled(ctx context.Context, tx Tx, c domain.Change, impact domain.ChangeImpactID) error {
	events, err := impactEvents(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	if pending := domain.PendingConflicts(events, impact); len(pending) > 0 {
		return fmt.Errorf("change impact %s has conflicts with the branch it was forked from to settle first (edit the fields, or keep them with ImpactNodeResolve): %s: %w",
			impact, strings.Join(pending, ", "), ErrConflict)
	}
	return nil
}

// ImpactConflicts are the conflicts a rebase left on a change impact, not settled yet.
type ImpactConflicts struct {
	Impact    domain.ChangeImpactID `json:"impactId"`
	Key       string                `json:"key"`
	Conflicts []string              `json:"conflicts"`
}

// RebaseState is where a change stands against the branch it was forked from (ADR 0101, 0103): the change impacts whose
// node that branch changed since the fork, a rebase bringing them up to date, and the conflicts left to settle.
type RebaseState struct {
	Parent    domain.ChangeID         `json:"parentId"`
	Behind    []domain.ChangeImpactID `json:"behind,omitempty"`
	Conflicts []ImpactConflicts       `json:"conflicts,omitempty"`
	// Branch and Onto, for a change behind the head of the branch it was forked from: that branch and the head baseline a
	// rebase moves it to (empty: not behind).
	Branch string            `json:"branch,omitempty"`
	Onto   domain.BaselineID `json:"onto,omitempty"`
}

// RebaseState reads where a change stands against the branch it was forked from. A change that committed nothing, or a
// closed one, has nothing behind.
func (g *Graph) RebaseState(ctx context.Context, id domain.ChangeID) (st RebaseState, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		st.Parent = c.ParentID
		events, err := impactEvents(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		for _, cn := range c.Nodes {
			if pending := domain.PendingConflicts(events, cn.ID); len(pending) > 0 {
				st.Conflicts = append(st.Conflicts, ImpactConflicts{Impact: cn.ID, Key: cn.Key, Conflicts: pending})
			}
		}
		if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
			return nil
		}
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil || !ok || !hasCommits(c) {
			return err
		}
		return g.committedBehind(ctx, tx, c, own, &st)
	})
	return
}
