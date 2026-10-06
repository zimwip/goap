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

// RebasedImpact is what a rebase did to a change impact of the sub-change.
type RebasedImpact struct {
	Impact domain.ChangeImpactID `json:"impactId"`
	Key    string                `json:"key"`
	// Changed is set when the merged draft differs from the sub-change's: its review went back to proposed.
	Changed bool `json:"changed"`
	// Conflicts are the fields changed on both sides (domain.ConflictProp, ...), kept as the sub-change had them.
	Conflicts []string `json:"conflicts,omitempty"`
}

// RebaseResult is what RebaseChange did.
type RebaseResult struct {
	Parent  domain.ChangeID `json:"parentId"`
	Impacts []RebasedImpact `json:"impacts,omitempty"`
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
	if len(conflicts) > 0 {
		out += "; settle the conflicts " + strings.Join(conflicts, ", ")
	}
	return out
}

// RebaseChange brings a sub-change up to date with its parent (ADR 0082 §1). A committed sub-change whose rebase changes a
// draft goes back to active: its impacts are to be reviewed again.
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
	res := RebaseResult{Parent: c.ParentID}
	switch c.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned:
		return res, fmt.Errorf("change %s is %s: %w", c.ID, c.Status, ErrConflict)
	}
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
			for i := range merged.Links {
				if merged.Links[i].ID == "" {
					merged.Links[i].ID = domain.LinkID(g.newID())
				}
			}
			onto = &domain.DraftOrigin{Change: p.c.ID, Impact: pi.ID, Seq: lastSeq(p.events, pi.ID)}
		}
		merged.Inherited = onto
		changed := !sameDraft(withoutOrigin(merged), withoutOrigin(*d))
		ref := merged.Ref()
		rebased := map[string]any{"change": string(p.c.ID)}
		if onto != nil {
			rebased["seq"] = onto.Seq
		}
		patch := map[string]any{"rebased": rebased, "conflicts": conflicts}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactTransitioned, Execution: d.Execution, Post: &ref, Draft: &merged, Patch: patch}); err != nil {
			return res, err
		}
		if (changed || len(conflicts) > 0) && cn.Review != domain.ReviewProposed {
			r := domain.Review{Status: domain.ReviewProposed, By: g.caller(ctx), At: g.now(), Comment: fmt.Sprintf("rebased onto parent change %s", p.c.ID)}
			if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r}); err != nil {
				return res, err
			}
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

// checkSettled refuses to accept a change impact whose conflicts from a rebase are not settled (ADR 0082 §2).
func (g *Graph) checkSettled(ctx context.Context, tx Tx, c domain.Change, impact domain.ChangeImpactID) error {
	if c.ParentID == "" {
		return nil
	}
	events, err := impactEvents(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	if pending := domain.PendingConflicts(events, impact); len(pending) > 0 {
		return fmt.Errorf("change impact %s has conflicts with its parent change to settle first (edit the fields, or keep them with ImpactNodeResolve): %s: %w",
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

// RebaseState is where a sub-change stands against its parent (ADR 0082): the change impacts whose draft the parent
// changed since the sub-change took it (a rebase brings them up to date), and the conflicts left to settle.
type RebaseState struct {
	Parent    domain.ChangeID         `json:"parentId"`
	Behind    []domain.ChangeImpactID `json:"behind,omitempty"`
	Conflicts []ImpactConflicts       `json:"conflicts,omitempty"`
}

// RebaseState reads where a sub-change stands against its parent. A change that is not a sub-change, or whose parent is
// no longer open, has nothing behind.
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
		if c.ParentID == "" || c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
			return nil
		}
		p, err := g.parentOf(ctx, tx, c)
		if errors.Is(err, ErrConflict) {
			return nil // the parent is no longer open
		} else if err != nil {
			return err
		}
		rows, err := g.drafts(ctx, tx, c.ID)
		if err != nil {
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
