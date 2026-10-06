package graph

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// requirement is a number of outgoing links of one type a node must have: what the parent of a structure (ADR 0054)
// and the required links a node type declares (ADR 0065) come down to. of describes it in error messages.
type requirement struct {
	linkType string
	count    int
	of       string
}

// requirementsOf returns the links a node of type typ and key must carry: the parent of a node of a structure,
// except the root of it, and the links its node type requires. Nil for any other node.
func (g *Graph) requirementsOf(typ, key string) []requirement {
	var out []requirement
	for _, st := range g.structures() {
		if typ == st.Type && key != st.Root {
			out = append(out, requirement{st.Parent, 1, fmt.Sprintf("a parent %s (%s)", st.Type, st.Parent)})
		}
	}
	for _, r := range g.requires(typ) {
		out = append(out, requirement{r.Link, r.Exactly(), fmt.Sprintf("exactly %d %s", r.Exactly(), r.Link)})
	}
	return out
}

// checkRequiredParent enforces that a created node names the links its type requires: exactly one parent link for a
// node of a structure (ADR 0054: a unit of the organisation, a project), the links a node type declares through `requires`
// (ADR 0065: a User needs one member_of, ADR 0040) — except the roots of the structures, created by the bootstrap.
// Only creation is checked here: an edit that modifies an existing node's membership (the move action) is
// responsible for its own atomicity (removing the old link and adding the new one in the same edit).
func (g *Graph) checkRequiredParent(e NodeEdit) error {
	if e.Pre != nil {
		return nil
	}
	for _, r := range g.requirementsOf(e.Type, e.Key) {
		n := 0
		for _, l := range e.Links {
			if l.Type == r.linkType {
				n++
			}
		}
		if n != r.count {
			return fmt.Errorf("%s needs %s, got %d: %w", nodeName(e), r.of, n, ErrInvalid)
		}
	}
	return nil
}

// checkRequiredLinks checks that a version or a draft carries the links its type requires (the parent of a node of a
// structure, the membership of a User): read back from the node itself, so a client that built its edits from a stale
// read (a link it did not know of) fails instead of leaving the node with zero or two of them. Checked when a draft is
// accepted (ADR 0079) and when the version lands (Apply), never while the draft is edited, which may be on its way there.
func (g *Graph) checkRequiredLinks(n domain.Node, links []domain.Link) error {
	reqs := g.requirementsOf(n.Type, n.Key)
	if len(reqs) == 0 || n.Deleted {
		return nil
	}
	for _, r := range reqs {
		count := 0
		for _, l := range links {
			if l.Type == r.linkType {
				count++
			}
		}
		if count != r.count {
			return fmt.Errorf("%s needs %s, got %d: %w", n.Key, r.of, count, ErrInvalid)
		}
	}
	return nil
}

// checkVersion is what a draft must satisfy to be accepted and the version written from it to land (ADR 0076, 0079): its
// properties (attributes and validators of its type), the links its type requires and the attributes of its links.
func (g *Graph) checkVersion(ctx context.Context, ix *typeIndex, n domain.Node, links []domain.Link) error {
	if err := g.validateProps(ctx, ix, n, n.Properties); err != nil {
		return err
	}
	if err := g.checkRequiredLinks(n, links); err != nil {
		return err
	}
	for _, l := range links {
		if err := ix.checkLinkAttributes(l.Type, l.Properties); err != nil {
			return fmt.Errorf("%s: %w", n.Key, err)
		}
	}
	return nil
}

// checkWritten checks a version a landing wrote, with its links.
func (g *Graph) checkWritten(ctx context.Context, tx Tx, ix *typeIndex, n domain.Node) error {
	links, err := tx.OutLinks(ctx, n.Ref())
	if err != nil {
		return err
	}
	return g.checkVersion(ctx, ix, n, links)
}

// checkDraft checks a draft as the version written from it will be.
func (g *Graph) checkDraft(ctx context.Context, ix *typeIndex, c domain.Change, d domain.Draft) error {
	return g.checkVersion(ctx, ix, draftNode(c, d), d.OutLinks())
}

// prepareSubChange applies the rules of a sub-change to c: its parent must be
// open and have a branch of its own, the namespace is the parent's, and the
// change starts from the head of the parent branch; it lands in the parent's log
// (ADR 0081). Its organisational unit must be inside the unit of the parent.
func (g *Graph) prepareSubChange(ctx context.Context, tx Tx, c *domain.Change, in *NewChange) error {
	if c.ParentID == "" {
		return nil
	}
	parent, err := tx.Change(ctx, c.ParentID)
	if err != nil {
		return err
	}
	switch parent.Status {
	case domain.ChangeApplied, domain.ChangeAbandoned, domain.ChangeCommitted:
		return fmt.Errorf("parent change %s is %s: %w", parent.ID, parent.Status, ErrConflict)
	}
	own, ok, err := ownBranch(ctx, tx, parent)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("parent change %s has no branch of its own to merge sub-changes into: %w", parent.ID, ErrInvalid)
	}
	if in.Namespace != "" && domain.NamespaceOf(in.Namespace) != domain.NamespaceOf(parent.Namespace) {
		return fmt.Errorf("a sub-change acts on the namespace of its parent (%s): %w", domain.NamespaceOf(parent.Namespace), ErrInvalid)
	}
	c.Namespace = domain.NamespaceOf(parent.Namespace)
	c.Branch = own.Name
	in.OwnBranch = true
	if c.BaselineID == "" {
		head, err := branchHead(ctx, tx, c.Namespace, own.Name)
		if err != nil {
			return err
		}
		c.BaselineID = head.ID
	}
	if c.Methodology == "" {
		c.Methodology = parent.Methodology
	}
	if c.OwnerOrg == "" {
		c.OwnerOrg = parent.OwnerOrg
	} else if c.OwnerOrg != parent.OwnerOrg {
		ok, err := g.within(ctx, tx, domain.StructureOrganisation, c.OwnerOrg, parent.OwnerOrg)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("unit %s is not part of %s, the unit of the parent change: %w", c.OwnerOrg, parent.OwnerOrg, ErrInvalid)
		}
	}
	if c.ProjectID == "" {
		c.ProjectID = parent.ProjectID
	} else if c.ProjectID != parent.ProjectID {
		ok, err := g.within(ctx, tx, domain.StructureProject, c.ProjectID, parent.ProjectID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("project %s is not part of %s, the project of the parent change: %w", c.ProjectID, parent.ProjectID, ErrInvalid)
		}
	}
	return nil
}

// SubChanges lists the sub-changes of a change, oldest first.
func (g *Graph) SubChanges(ctx context.Context, id domain.ChangeID) (out []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { out, err = subChanges(ctx, tx, id); return err })
	return
}

func subChanges(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.Change, error) {
	all, err := tx.Changes(ctx)
	if err != nil {
		return nil, err
	}
	var out []domain.Change
	for _, c := range all {
		if c.ParentID == id {
			out = append(out, c)
		}
	}
	return out, nil
}

// openSubChanges are the sub-changes that still have to be applied or abandoned.
func openSubChanges(ctx context.Context, tx Tx, id domain.ChangeID) ([]domain.Change, error) {
	subs, err := subChanges(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(subs, func(c domain.Change) bool {
		return c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned
	}), nil
}

// abandonSubChanges abandons the open sub-changes of a change (and their branches), recursively.
func (g *Graph) abandonSubChanges(ctx context.Context, tx Tx, id domain.ChangeID) error {
	open, err := openSubChanges(ctx, tx, id)
	if err != nil {
		return err
	}
	for _, sc := range open {
		if err := g.abandonSubChanges(ctx, tx, sc.ID); err != nil {
			return err
		}
		if own, ok, err := ownBranch(ctx, tx, sc); err != nil {
			return err
		} else if ok {
			own.Status = domain.BranchAbandoned
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
		}
		sc.Status = domain.ChangeAbandoned
		if err := tx.PutChange(ctx, sc); err != nil {
			return err
		}
	}
	return nil
}

// SplitByOwner splits a change along organisational boundaries: one sub-change per unit owning nodes the change has
// an impact on (the owner of the pre version of a modified change impact, ADR 0024, 0054). The sub-change gets a copy
// of the change impacts of its nodes, derived from the parent's. Units that already have an open sub-change are left
// as they are. Impacts on nodes the unit holding the change owns, or a unit outside it, stay with the parent.
func (g *Graph) SplitByOwner(ctx context.Context, id domain.ChangeID) (created []domain.Change, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		parent, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		existing, err := openSubChanges(ctx, tx, id)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, sc := range existing {
			have[sc.OwnerOrg] = true
		}
		type group struct {
			org, name string
			nodes     []domain.ChangeImpact
		}
		var groups []*group
		byOrg := map[string]*group{}
		seen, err := g.newFlowNodes(tx, parent, "").nodes(ctx) // the change impacts of the main flow: stored and derived from items
		if err != nil {
			return err
		}
		for _, cn := range parent.Nodes {
			if !slices.ContainsFunc(seen, func(x domain.ChangeImpact) bool { return x.ID == cn.ID }) && len(cn.Items) == 0 {
				continue // a candidate of a flow, or replaced by one
			}
			if cn.Pre == nil || cn.Intent != domain.IntentModified || cn.Review == domain.ReviewRejected || cn.Superseded {
				continue
			}
			pre, err := tx.Node(ctx, *cn.Pre)
			if err != nil {
				return err
			}
			unit, err := tx.Node(ctx, domain.NodeRef{ID: pre.Owner})
			if err != nil {
				return err
			}
			if unit.Key == parent.OwnerOrg {
				continue // the unit holding the change owns it
			}
			if below, err := g.within(ctx, tx, domain.StructureOrganisation, unit.Key, parent.OwnerOrg); err != nil {
				return err
			} else if !below {
				continue // owned outside the unit holding the change: nothing to delegate to
			}
			gr := byOrg[unit.Key]
			if gr == nil {
				name, _ := unit.Properties["name"].(string)
				gr = &group{org: unit.Key, name: firstNonEmpty(name, unit.Key)}
				byOrg[unit.Key] = gr
				groups = append(groups, gr)
			}
			gr.nodes = append(gr.nodes, cn)
		}
		for _, gr := range groups {
			if have[gr.org] {
				continue
			}
			in := NewChange{Title: parent.Title + " / " + gr.name, Intent: parent.Intent, Methodology: parent.Methodology, ParentID: id, OwnerOrg: gr.org}
			sub := domain.Change{ID: domain.ChangeID(g.newID()), Title: in.Title, Intent: in.Intent, Methodology: in.Methodology, Status: domain.ChangeDraft,
				ParentID: id, OwnerOrg: gr.org, CreatedAt: g.now()}
			if err := g.prepareSubChange(ctx, tx, &sub, &in); err != nil {
				return err
			}
			if err := g.scopeChange(ctx, tx, &sub); err != nil {
				return err
			}
			fork, err := tx.Baseline(ctx, sub.BaselineID)
			if err != nil {
				return err
			}
			own := domain.Branch{Name: changeBranchName(sub.ID), Parent: sub.Branch, ForkBaseline: fork.ID, Head: fork.ID,
				Origin: domain.ChangeBranchOrigin(sub.ID), Status: domain.BranchOpen, CreatedAt: g.now()}
			if err := tx.PutBranch(ctx, own); err != nil {
				return err
			}
			sub.Branch = own.Name
			if err := tx.PutChange(ctx, sub); err != nil {
				return err
			}
			for _, cn := range gr.nodes {
				// the impact is confirmed for the parent, the work moves to the sub-change
				if stored := slices.IndexFunc(parent.Nodes, func(x domain.ChangeImpact) bool { return x.ID == cn.ID && len(x.Items) == 0 }); stored >= 0 && cn.Review == domain.ReviewProposed && cn.Post == nil {
					r := domain.Review{Status: domain.ReviewAccepted, By: "graph.split_by_owner", At: g.now(),
						Comment: fmt.Sprintf("delegated to %s (sub-change %s)", gr.org, sub.ID)}
					if err := g.emit(ctx, tx, domain.ImpactEvent{Change: id, Impact: cn.ID, Op: domain.ImpactReviewed, By: r.By, Review: &r}); err != nil {
						return err
					}
				}
				// DerivedFrom holds the id of the parent's change impact it is copied from
				cp := domain.ChangeImpact{ID: domain.ChangeImpactID(g.newID()), Key: cn.Key, Type: cn.Type, Intent: domain.IntentModified, Rationale: cn.Rationale,
					Pre: cn.Pre, Review: domain.ReviewProposed, ProducedBy: "graph.split_by_owner", DerivedFrom: []domain.ItemID{domain.ItemID(cn.ID)}, CreatedAt: g.now()}
				if err := g.emit(ctx, tx, domain.ImpactEvent{Change: sub.ID, Impact: cp.ID, Op: domain.ImpactProposed, By: cp.ProducedBy, State: &cp}); err != nil {
					return err
				}
			}
			sub.Status = domain.ChangeActive
			if err := tx.PutChange(ctx, sub); err != nil {
				return err
			}
			created = append(created, sub)
		}
		return nil
	})
	return
}

// commitSubTx commits a sub-change (ADR 0081): the rules of a landing are checked on its drafts, as the versions written
// from them would be, and nothing is written to a branch: the sub-change hands its drafts to its parent when it is
// integrated. The states its nodes are left in are the parent's to land.
func (g *Graph) commitSubTx(ctx context.Context, tx Tx, c domain.Change, landing *landingDecision) error {
	if g.LandingGate != nil && landing == nil {
		return fmt.Errorf("change %s: the landing gate was not asked before this transaction: %w", c.ID, ErrInvalid)
	}
	if landing != nil && landing.decided && !landing.ok {
		return invalidf("the change does not satisfy the goal of its landing gate")
	}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return err
	}
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return err
	}
	impacts := slices.DeleteFunc(slices.Clone(c.Nodes), func(cn domain.ChangeImpact) bool { return cn.Flow != "" || cn.Superseded })
	for _, cn := range impacts {
		if len(cn.Items) > 0 {
			continue
		}
		switch cn.Review {
		case domain.ReviewRejected:
			continue
		case domain.ReviewProposed:
			return fmt.Errorf("change impact %s (%s) awaits its review: %w", cn.Key, cn.ID, ErrConflict)
		}
		d := ownDraft(rows, cn.ID, "")
		if d == nil {
			if cn.Intent == domain.IntentModified {
				continue // an impact confirmed, with nothing written
			}
			return invalidf("change impact %s (%s) is accepted but its node is not written: write it or reject it", cn.Key, cn.ID)
		}
		if err := g.checkOrigins(impacts, cn, d); err != nil {
			return err
		}
		if err := g.checkDraft(ctx, ix, c, *d); err != nil {
			return err
		}
	}
	c.Status = domain.ChangeCommitted
	return tx.PutChange(ctx, c)
}

// subInstall is a draft of a sub-change its integration installs in the parent change.
type subInstall struct {
	cn     domain.ChangeImpact  // the change impact of the sub-change
	d      domain.Draft         // its draft
	parent *domain.ChangeImpact // the change impact the parent holds of the node, if any
}

// integrateSub integrates a committed sub-change into its parent change (ADR 0081 §4, ADR 0082 §3): every accepted draft
// of its main flow is installed in the parent's main flow by events of the parent's log, accepted there with the review
// the sub-change made, and recorded integrated in the sub-change's log. It is a fast-forward: a draft the parent changed
// since the sub-change took it is refused (rebase the sub-change first), and nothing is written.
func (g *Graph) integrateSub(ctx context.Context, tx Tx, c domain.Change) (domain.Change, error) {
	p, err := g.parentOf(ctx, tx, c)
	if err != nil {
		return c, err
	}
	rows, err := g.drafts(ctx, tx, c.ID)
	if err != nil {
		return c, err
	}
	var plan []subInstall
	var behind, rejected []string
	for _, cn := range c.Nodes {
		if len(cn.Items) > 0 || cn.Flow != "" || cn.Superseded || cn.Review != domain.ReviewAccepted {
			continue
		}
		d := ownDraft(rows, cn.ID, "")
		if d == nil {
			continue // confirmed, nothing written
		}
		in := subInstall{cn: cn, d: *d, parent: p.impactOf(d.Node)}
		switch {
		case in.parent != nil && in.parent.Review == domain.ReviewRejected:
			rejected = append(rejected, d.Key)
		case p.behind(*d, in.parent):
			behind = append(behind, d.Key)
		}
		plan = append(plan, in)
	}
	if len(rejected) > 0 {
		slices.Sort(rejected)
		return c, fmt.Errorf("parent change %s rejected %v: withdraw them from sub-change %s, or have the parent reopen them: %w", p.c.ID, rejected, c.ID, ErrConflict)
	}
	if len(behind) > 0 {
		slices.Sort(behind)
		return c, fmt.Errorf("parent change %s changed %v since sub-change %s took them: rebase it onto the parent first: %w", p.c.ID, behind, c.ID, ErrConflict)
	}
	for _, in := range plan {
		into := domain.ImpactRef{Change: p.c.ID}
		if into.Impact, err = g.installSub(ctx, tx, c, p.c.ID, in); err != nil {
			return c, err
		}
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: c.ID, Impact: in.cn.ID, Op: domain.ImpactIntegrated, Into: &into}); err != nil {
			return c, err
		}
	}
	// its branch received nothing: it is closed with the change
	if own, ok, err := ownBranch(ctx, tx, c); err != nil {
		return c, err
	} else if ok {
		own.Status = domain.BranchMerged
		if err := tx.PutBranch(ctx, own); err != nil {
			return c, err
		}
	}
	c.Status = domain.ChangeApplied
	return c, tx.PutChange(ctx, c)
}

// installSub installs a draft of a sub-change in its parent's main flow (ADR 0081 §4) and accepts it there with the
// review of the sub-change; it returns the parent's change impact.
func (g *Graph) installSub(ctx context.Context, tx Tx, c domain.Change, parent domain.ChangeID, in subInstall) (domain.ChangeImpactID, error) {
	d := in.d.Clone()
	if d.Inherited != nil && d.Inherited.Change == parent {
		d.Inherited = nil // the parent's own draft now; one copied from a grandparent keeps its origin
	}
	ref := d.Ref()
	patch := map[string]any{"integrated": map[string]any{"change": string(c.ID), "impact": string(in.cn.ID)}}
	var id domain.ChangeImpactID
	if in.parent != nil {
		id = in.parent.ID
		if err := g.emit(ctx, tx, domain.ImpactEvent{Change: parent, Impact: id, Op: domain.ImpactTransitioned, Execution: d.Execution, Post: &ref, Draft: &d, Patch: patch}); err != nil {
			return "", err
		}
	} else {
		id = domain.ChangeImpactID(g.newID())
		st := domain.ChangeImpact{ID: id, Key: in.cn.Key, Type: in.cn.Type, Intent: in.cn.Intent, Rationale: in.cn.Rationale, Review: domain.ReviewProposed,
			ProducedBy: "graph.integrate", DerivedFrom: []domain.ItemID{domain.ItemID(in.cn.ID)}, Execution: in.cn.Execution, CreatedAt: g.now()}
		if in.cn.Pre != nil {
			p := *in.cn.Pre
			st.Pre = &p
		}
		var events []domain.ImpactEvent
		if st.Intent == domain.IntentCreated {
			events = append(events, domain.ImpactEvent{Change: parent, Impact: id, Op: domain.ImpactCreated, Execution: d.Execution, State: &st, Post: &ref, Draft: &d, Patch: patch})
		} else {
			events = append(events, domain.ImpactEvent{Change: parent, Impact: id, Op: domain.ImpactProposed, Execution: st.Execution, State: &st},
				domain.ImpactEvent{Change: parent, Impact: id, Op: domain.ImpactCheckedOut, Execution: d.Execution, Post: &ref, Draft: &d, Patch: patch})
		}
		if err := g.emit(ctx, tx, events...); err != nil {
			return "", err
		}
	}
	last := lastReview(in.cn)
	r := domain.Review{Status: domain.ReviewAccepted, By: last.By, At: g.now(), ReviewID: last.ReviewID,
		Comment: strings.TrimSpace(fmt.Sprintf("integrated from sub-change %s (%s): %s", c.Title, c.ID, last.Comment))}
	return id, g.emit(ctx, tx, domain.ImpactEvent{Change: parent, Impact: id, Op: domain.ImpactReviewed, By: r.By, Review: &r})
}

// lastReview is the review that decides a change impact on the main flow.
func lastReview(cn domain.ChangeImpact) domain.Review {
	var out domain.Review
	for _, r := range cn.Reviews {
		if r.Flow == "" && !r.Superseded {
			out = r
		}
	}
	return out
}

// subLandingBlackboard is the blackboard the landing gate decides a sub-change against (ADR 0081): the change with its
// impacts, the pre version and the draft of each, as the change sees them (no version is written for a sub-change).
func (g *Graph) subLandingBlackboard(ctx context.Context, tx Tx, c domain.Change) (domain.Blackboard, error) {
	bb := domain.Blackboard{Change: c, Nodes: map[domain.NodeRef]domain.NodeView{}}
	ix, err := g.typesAt(ctx, tx, c.BaselineID)
	if err != nil {
		return bb, err
	}
	impacts := slices.DeleteFunc(slices.Clone(c.Nodes), func(cn domain.ChangeImpact) bool { return cn.Flow != "" || cn.Superseded })
	r, err := g.newDraftReader(ctx, tx, c, "", impacts)
	if err != nil {
		return bb, err
	}
	for _, cn := range impacts {
		for _, ref := range []*domain.NodeRef{cn.Pre, cn.Post} {
			if ref == nil {
				continue
			}
			v, err := viewIn(ctx, tx, r, *ref)
			if err != nil {
				return bb, err
			}
			if lc := ix.lifecycleOf(v.Type); lc != nil && v.State != "" {
				v.NotLandable = !lc.Landable(v.State)
			}
			bb.Nodes[*ref] = v
		}
	}
	return bb, nil
}
