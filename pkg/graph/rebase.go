package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// Rebasing a sub-change onto its parent (ADR 0082): the drafts of the sub-change the parent changed since they were taken
// are merged three-way with the parent's (domain.MergeDrafts), the base folded from the log at the position the draft was
// copied; the fields changed on both sides are conflicts the sub-change settles by editing its draft, and its integration
// into the parent is then a fast-forward.
//
// Rebasing a root change onto its target branch (ADR 0101): the same merge against the versions on the head of the branch
// the change was forked from, then the change and its branch move to that head, so that its landing is a fast-forward.

// RebasedImpact is what a rebase did to a change impact of the sub-change.
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

// Changed reports a rebase that changed a draft: the sub-change has impacts to review again.
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
	out := fmt.Sprintf("rebased onto parent change %s: review again %s", r.Parent, strings.Join(review, ", "))
	if r.Parent == "" {
		out = fmt.Sprintf("rebased onto branch %s: review again %s", r.Branch, strings.Join(review, ", "))
	}
	if len(conflicts) > 0 {
		out += "; settle the conflicts " + strings.Join(conflicts, ", ")
	}
	return out
}

// RebaseChange brings a sub-change up to date with its parent (ADR 0082 §1), a root change with the head of its target
// branch (ADR 0101 §1). A committed sub-change whose rebase changes a draft goes back to active: its impacts are to be
// reviewed again; a committed root change is not rebased.
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

// parentState is what a sub-change is checked against: its parent, the parent's drafts and log.
type parentState struct {
	c      domain.Change
	rows   []domain.Draft
	events []domain.ImpactEvent
}

func (g *Graph) parentOf(ctx context.Context, tx Tx, c domain.Change) (parentState, error) {
	var p parentState
	if c.ParentID == "" {
		return p, invalidf("change %s is not a sub-change", c.ID)
	}
	var err error
	if p.c, err = changeOpen(ctx, tx, c.ParentID); err != nil {
		return p, err
	}
	if p.rows, err = g.drafts(ctx, tx, p.c.ID); err != nil {
		return p, err
	}
	p.events, err = impactEvents(ctx, tx, p.c.ID)
	return p, err
}

// impactOf is the change impact the parent holds of a node on its main flow, if any.
func (p parentState) impactOf(node domain.NodeID) *domain.ChangeImpact {
	var out *domain.ChangeImpact
	for i, x := range p.c.Nodes {
		if x.Flow == "" && !x.Superseded && nodeOf(x) == node {
			out = &p.c.Nodes[i]
		}
	}
	return out
}

// lastSeq is the position of the last event of a change impact on the main flow of a log.
func lastSeq(events []domain.ImpactEvent, impact domain.ChangeImpactID) int {
	seq := 0
	for _, e := range events {
		if e.Impact == impact && e.Flow == "" && e.Seq > seq {
			seq = e.Seq
		}
	}
	return seq
}

// behind reports whether the parent changed a node since the sub-change took it (ADR 0081 §5): the draft was copied from
// the parent's and an event changed that draft after, or it was taken from the stored version (or from a grandparent) and
// the parent holds a draft of the node now; or the parent rejected or withdrew the impact the draft was copied from.
func (p parentState) behind(d domain.Draft, pi *domain.ChangeImpact) bool {
	if pi != nil && pi.Review == domain.ReviewRejected {
		return true
	}
	if o := d.Inherited; o != nil && o.Change == p.c.ID {
		if pi == nil || pi.ID != o.Impact {
			return true
		}
		for _, e := range p.events {
			if e.Impact == o.Impact && e.Flow == "" && e.Seq > o.Seq && changesDraft(e) {
				return true
			}
		}
		return false
	}
	return pi != nil && ownDraft(p.rows, pi.ID, "") != nil
}

// rebaseTx is RebaseChange inside a transaction.
func (g *Graph) rebaseTx(ctx context.Context, tx Tx, c domain.Change) (RebaseResult, error) {
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned:
		return RebaseResult{Parent: c.ParentID}, fmt.Errorf("change %s is %s: %w", c.ID, c.Status, ErrConflict)
	}
	if c.ParentID == "" {
		return g.rebaseRootTx(ctx, tx, c)
	}
	res := RebaseResult{Parent: c.ParentID}
	p, err := g.parentOf(ctx, tx, c)
	if err != nil {
		return res, err
	}
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return res, err
	}
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded {
			continue
		}
		d := ownDraft(rows, cn.ID, "")
		if d == nil {
			continue // nothing written: the sub-change sees the parent's draft as it is
		}
		pi := p.impactOf(d.Node)
		if !p.behind(*d, pi) {
			continue
		}
		base, err := g.rebaseBase(ctx, tx, *d)
		if err != nil {
			return res, err
		}
		var merged domain.Draft
		var conflicts []string
		var onto *domain.DraftOrigin
		switch {
		case pi == nil || pi.Review == domain.ReviewRejected:
			// the parent withdrew or rejected what the sub-change works on: the whole node is in conflict
			merged, conflicts = d.Clone(), []string{domain.ConflictNode}
			if pi != nil {
				onto = &domain.DraftOrigin{Change: p.c.ID, Impact: pi.ID, Seq: lastSeq(p.events, pi.ID)}
			}
		default:
			// the parent's draft; a parent holding none (it cancelled its checkout) sees the stored version again
			theirs := base
			if pd := ownDraft(p.rows, pi.ID, ""); pd != nil {
				theirs = *pd
			} else if pi.Pre != nil {
				if theirs, err = g.storedDraft(ctx, tx, *pi.Pre); err != nil {
					return res, err
				}
			}
			merged, conflicts = domain.MergeDrafts(base, *d, theirs)
			g.numberLinks(&merged)
			onto = &domain.DraftOrigin{Change: p.c.ID, Impact: pi.ID, Seq: lastSeq(p.events, pi.ID)}
		}
		merged.Inherited = onto
		changed := !sameDraft(withoutOrigin(merged), withoutOrigin(*d))
		rebased := map[string]any{"change": string(p.c.ID)}
		if onto != nil {
			rebased["seq"] = onto.Seq
		}
		back := changed || len(conflicts) > 0
		if err := g.installRebased(ctx, tx, c, cn, *d, merged, nil, rebased, conflicts, back, "rebased onto parent change "+string(p.c.ID)); err != nil {
			return res, err
		}
		res.Impacts = append(res.Impacts, RebasedImpact{Impact: cn.ID, Key: cn.Key, Changed: changed, Conflicts: conflicts})
	}
	if res.Changed() && c.Status == domain.ChangeCommitted {
		c, err := tx.Change(ctx, c.ID)
		if err != nil {
			return res, err
		}
		c.Status = domain.ChangeActive
		if err := tx.PutChange(ctx, c); err != nil {
			return res, err
		}
	}
	return res, nil
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

// rebaseRootTx rebases a root change onto the head of the branch it was forked from (ADR 0101 §1). The change is behind
// when that head is not its fork baseline; each draft of its main flow whose node changed on the head since it was
// checked out is merged three-way, then the change and its branch move to the head: possible since an uncommitted change
// has no version on its branch (ADR 0079).
func (g *Graph) rebaseRootTx(ctx context.Context, tx Tx, c domain.Change) (RebaseResult, error) {
	res := RebaseResult{}
	if c.Status == domain.ChangeCommitted {
		return res, fmt.Errorf("change %s is committed: its versions are written, it is integrated, not rebased: %w", c.ID, ErrConflict)
	}
	if hasCommits(c) {
		// its branch holds its commits: the branch moves onto the head of its target (ADR 0102)
		own, ok, err := ownBranch(ctx, tx, c)
		if err != nil {
			return res, err
		}
		if ok {
			return g.rebaseCommittedTx(ctx, tx, c, own)
		}
	}
	target, own, hasOwn, err := rebaseTarget(ctx, tx, c)
	if err != nil {
		return res, err
	}
	head, err := branchHead(ctx, tx, c.Namespace, target)
	if err != nil {
		return res, err
	}
	res.Branch, res.From, res.Onto = target, c.BaselineID, head.ID
	if head.ID == c.BaselineID {
		return res, nil
	}
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return res, err
	}
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded {
			continue
		}
		d := ownDraft(rows, cn.ID, "")
		if d == nil {
			// nothing written: the pre moves to the version on the head, to check again (the existing rebased event)
			if cn.Pre != nil {
				if v, ok := head.Nodes[cn.Pre.ID]; ok && v != cn.Pre.Version {
					ref := domain.NodeRef{ID: cn.Pre.ID, Version: v}
					if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactRebased, Pre: &ref}); err != nil {
						return res, err
					}
				}
			}
			continue
		}
		var merged domain.Draft
		var conflicts []string
		var pre *domain.NodeRef
		if d.Base == nil {
			// a creation: the key now exists on the target, the whole node is in conflict
			id, err := tx.NodeIDByKey(ctx, c.Namespace, d.Key)
			if errors.Is(err, ErrNotFound) {
				continue
			} else if err != nil {
				return res, err
			}
			if _, ok := head.Nodes[id]; !ok {
				continue
			}
			merged, conflicts = d.Clone(), []string{domain.ConflictNode}
		} else {
			v, ok := head.Nodes[d.Node]
			if !ok || v == d.Base.Version {
				continue // not changed on the target since it was checked out
			}
			theirsRef := domain.NodeRef{ID: d.Node, Version: v}
			base, err := g.storedDraft(ctx, tx, *d.Base)
			if err != nil {
				return res, err
			}
			theirs, err := g.storedDraft(ctx, tx, theirsRef)
			if err != nil {
				return res, err
			}
			merged, conflicts = domain.MergeDrafts(base, *d, theirs)
			g.numberLinks(&merged)
			merged.Base = &theirsRef
			if cn.Pre != nil {
				pre = &theirsRef
			}
		}
		rebased := map[string]any{"branch": target, "baseline": string(head.ID)}
		if err := g.installRebased(ctx, tx, c, cn, *d, merged, pre, rebased, conflicts, len(conflicts) > 0, "rebased onto "+target); err != nil {
			return res, err
		}
		res.Impacts = append(res.Impacts, RebasedImpact{Impact: cn.ID, Key: cn.Key, Changed: len(conflicts) > 0 && cn.Review != domain.ReviewProposed,
			Merged: !sameDraft(merged, *d), Conflicts: conflicts})
	}
	// the change and its branch move to the head
	e, err := domain.HeaderEntry(g.newID(), c.ID, g.caller(ctx), g.now(),
		domain.HeaderEdit{Fields: map[string]domain.HeaderValue{HeaderBaselineID: {From: string(c.BaselineID), To: string(head.ID)}}})
	if err != nil {
		return res, err
	}
	if _, err := tx.AppendLog(ctx, e); err != nil {
		return res, err
	}
	if hasOwn {
		if own.RebasedFrom == "" {
			own.RebasedFrom = own.ForkBaseline
		}
		own.ForkBaseline, own.Head = head.ID, head.ID
		if err := tx.PutBranch(ctx, own); err != nil {
			return res, err
		}
	}
	c, err = tx.Change(ctx, c.ID)
	if err != nil {
		return res, err
	}
	c.BaselineID = head.ID
	return res, tx.PutChange(ctx, c)
}

// rebaseTarget is the branch a root change is rebased onto: the one its own branch was forked from, or the one it works
// on while it has no branch of its own. A branch of its own that holds a baseline (the change wrote on it) cannot move.
func rebaseTarget(ctx context.Context, tx Tx, c domain.Change) (target string, own domain.Branch, hasOwn bool, err error) {
	own, hasOwn, err = ownBranch(ctx, tx, c)
	if err != nil {
		return "", own, false, err
	}
	if !hasOwn {
		return domain.BranchOf(c.Branch), own, false, nil
	}
	if own.Head != own.ForkBaseline {
		return "", own, true, fmt.Errorf("change %s: its branch %s holds a baseline of its own, it is not rebased: %w", c.ID, own.Name, ErrConflict)
	}
	return domain.BranchOf(own.Parent), own, true, nil
}

func withoutOrigin(d domain.Draft) domain.Draft {
	d = d.Clone()
	d.Inherited = nil
	return d
}

// rebaseBase is the state a draft of a sub-change started from: the draft it was copied from, folded from that change's
// log up to the position it was copied at; else the stored version it was checked out from (with its outgoing links);
// else (a creation of its own) an empty draft.
func (g *Graph) rebaseBase(ctx context.Context, tx Tx, d domain.Draft) (domain.Draft, error) {
	if o := d.Inherited; o != nil {
		events, err := impactEvents(ctx, tx, o.Change)
		if err != nil {
			return domain.Draft{}, err
		}
		upTo := events[:0:0]
		for _, e := range events {
			if e.Seq <= o.Seq {
				upTo = append(upTo, e)
			}
		}
		if b := domain.DraftSeenBy(upTo, o.Impact, nil, func(string) bool { return false }); b != nil {
			return *b, nil
		}
	}
	if d.Base == nil {
		return domain.Draft{Node: d.Node, Key: d.Key, Type: d.Type}, nil
	}
	return g.storedDraft(ctx, tx, *d.Base)
}

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

// ImpactNodeResolve settles the conflicts a rebase left on a change impact of a sub-change by keeping the sub-change's
// values (ADR 0082 §2): an updated event with {"resolved": true}. The impact is then reviewed as any.
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
		with := "its parent change"
		if c.ParentID == "" {
			with = "its target branch"
		}
		return fmt.Errorf("change impact %s has conflicts with %s to settle first (edit the fields, or keep them with ImpactNodeResolve): %s: %w",
			impact, with, strings.Join(pending, ", "), ErrConflict)
	}
	return nil
}

// ImpactConflicts are the conflicts a rebase left on a change impact, not settled yet.
type ImpactConflicts struct {
	Impact    domain.ChangeImpactID `json:"impactId"`
	Key       string                `json:"key"`
	Conflicts []string              `json:"conflicts"`
}

// RebaseState is where a change stands against what it builds on (ADR 0082, 0101): the change impacts whose draft the
// parent change (a sub-change) or the head of the target branch (a root change) changed since the change took it, a
// rebase bringing them up to date, and the conflicts left to settle.
type RebaseState struct {
	Parent    domain.ChangeID         `json:"parentId"`
	Behind    []domain.ChangeImpactID `json:"behind,omitempty"`
	Conflicts []ImpactConflicts       `json:"conflicts,omitempty"`
	// Branch and Onto, for a root change behind the head of its target branch: the branch and the head baseline a
	// rebase moves it to (empty: not behind).
	Branch string            `json:"branch,omitempty"`
	Onto   domain.BaselineID `json:"onto,omitempty"`
}

// RebaseState reads where a change stands against what it builds on. A sub-change whose parent is no longer open, a
// committed or closed root change, has nothing behind.
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
		rows, err := g.drafts(ctx, tx, c.ID)
		if err != nil {
			return err
		}
		if c.ParentID == "" {
			return g.rootBehind(ctx, tx, c, rows, &st)
		}
		p, err := g.parentOf(ctx, tx, c)
		if errors.Is(err, ErrConflict) {
			return nil // the parent is no longer open
		} else if err != nil {
			return err
		}
		for _, cn := range c.Nodes {
			if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded {
				continue
			}
			if d := ownDraft(rows, cn.ID, ""); d != nil && p.behind(*d, p.impactOf(d.Node)) {
				st.Behind = append(st.Behind, cn.ID)
			}
		}
		return nil
	})
	return
}

// rootBehind fills the state of a root change: the head it would be rebased onto and the impacts whose node changed there.
func (g *Graph) rootBehind(ctx context.Context, tx Tx, c domain.Change, rows []domain.Draft, st *RebaseState) error {
	if c.Status == domain.ChangeCommitted {
		return nil // its versions are written: it is integrated, not rebased
	}
	if hasCommits(c) {
		if own, ok, err := ownBranch(ctx, tx, c); err != nil || ok {
			if err != nil {
				return err
			}
			return g.committedBehind(ctx, tx, c, own, st)
		}
	}
	target, _, _, err := rebaseTarget(ctx, tx, c)
	if errors.Is(err, ErrConflict) {
		return nil
	} else if err != nil {
		return err
	}
	head, err := branchHead(ctx, tx, c.Namespace, target)
	if err != nil {
		return err
	}
	if head.ID == c.BaselineID {
		return nil
	}
	st.Branch, st.Onto = target, head.ID
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded {
			continue
		}
		d := ownDraft(rows, cn.ID, "")
		if d == nil {
			continue
		}
		if d.Base != nil {
			if v, ok := head.Nodes[d.Node]; ok && v != d.Base.Version {
				st.Behind = append(st.Behind, cn.ID)
			}
		} else if id, err := tx.NodeIDByKey(ctx, c.Namespace, d.Key); err == nil {
			if _, ok := head.Nodes[id]; ok {
				st.Behind = append(st.Behind, cn.ID)
			}
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	return nil
}
