package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/zimwip/goap/pkg/domain"
)

// ---- 3-way merge primitives ----------------------------------------------

// DeletedKey is reported as a conflict when one side deleted the node and the
// other side modified it.
const DeletedKey = "(deleted)"

// MergeProps merges two property maps against their common ancestor, key by
// key: equal sides win; a side equal to the ancestor takes the other one;
// otherwise the key is a conflict and ours is kept. A key absent from a side
// is a deletion on that side.
func MergeProps(anc, ours, theirs map[string]any) (map[string]any, []string) {
	merged := map[string]any{}
	var conflicts []string
	keys := map[string]bool{}
	for _, m := range []map[string]any{anc, ours, theirs} {
		for k := range m {
			keys[k] = true
		}
	}
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		a, aok := anc[k]
		o, ook := ours[k]
		t, tok := theirs[k]
		var v any
		var ok bool
		switch {
		case same(o, ook, t, tok):
			v, ok = o, ook
		case same(o, ook, a, aok):
			v, ok = t, tok
		case same(t, tok, a, aok):
			v, ok = o, ook
		default:
			conflicts = append(conflicts, k)
			v, ok = o, ook
		}
		if ok {
			merged[k] = v
		}
	}
	return merged, conflicts
}

func same(a any, aok bool, b any, bok bool) bool {
	if aok != bok {
		return false
	}
	return !aok || jsonEqual(a, b)
}

// jsonEqual compares values by their JSON encoding (numbers read back from
// storage may differ in Go type).
func jsonEqual(a, b any) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && bytes.Equal(ja, jb)
}

// MergeLinks merges outgoing link sets keyed by (type, target node): a link is
// kept when present on both sides, or added on one side since the ancestor; it
// is dropped when removed on one side. Ours is preferred when both have it.
func MergeLinks(anc, ours, theirs []domain.Link) []domain.Link {
	key := func(l domain.Link) string { return l.Type + "\x00" + string(l.To.ID) }
	index := func(ls []domain.Link) map[string]domain.Link {
		m := make(map[string]domain.Link, len(ls))
		for _, l := range ls {
			m[key(l)] = l
		}
		return m
	}
	a, o, t := index(anc), index(ours), index(theirs)
	keys := map[string]bool{}
	for k := range o {
		keys[k] = true
	}
	for k := range t {
		keys[k] = true
	}
	var out []domain.Link
	for _, k := range slices.Sorted(maps.Keys(keys)) {
		ol, inO := o[k]
		tl, inT := t[k]
		_, inA := a[k]
		switch {
		case inO && inT, inO && !inA:
			out = append(out, ol)
		case inT && !inA:
			out = append(out, tl)
		}
	}
	return out
}

// parentsOf returns the parents of a version (legacy versions without
// parents descend from the previous version).
func parentsOf(n domain.Node) []domain.Version {
	if len(n.Parents) > 0 || n.Version <= 1 {
		return n.Parents
	}
	return []domain.Version{n.Version - 1}
}

// CommonAncestor returns the most recent common ancestor of two versions of
// a node (a version is its own ancestor).
func CommonAncestor(ctx context.Context, tx Tx, id domain.NodeID, a, b domain.Version) (domain.NodeRef, error) {
	vs, err := tx.Versions(ctx, id)
	if err != nil {
		return domain.NodeRef{}, err
	}
	parents := map[domain.Version][]domain.Version{}
	for _, n := range vs {
		parents[n.Version] = parentsOf(n)
	}
	ancestors := func(v domain.Version) map[domain.Version]bool {
		seen := map[domain.Version]bool{}
		stack := []domain.Version{v}
		for len(stack) > 0 {
			x := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[x] {
				continue
			}
			seen[x] = true
			stack = append(stack, parents[x]...)
		}
		return seen
	}
	sa, sb := ancestors(a), ancestors(b)
	var best domain.Version
	for v := range sa {
		if sb[v] && v > best {
			best = v
		}
	}
	if best == 0 {
		return domain.NodeRef{}, fmt.Errorf("versions %d and %d of node %s have no common ancestor: %w", a, b, id, ErrNotFound)
	}
	return domain.NodeRef{ID: id, Version: best}, nil
}

// ---- Branches --------------------------------------------------------------

// NewBranch describes a branch to open from a baseline.
type NewBranch struct {
	Name   string
	From   domain.BaselineID
	Origin string // change / option that opens the branch
}

// CreateBranch opens a branch forked from a baseline.
func (g *Graph) CreateBranch(ctx context.Context, in NewBranch) (b domain.Branch, err error) {
	if in.Name == "" || in.Name == domain.MainBranch || strings.ContainsAny(in.Name, " \t\n") {
		return b, fmt.Errorf("invalid branch name %q: %w", in.Name, ErrInvalid)
	}
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Branch(ctx, in.Name); err == nil {
			return fmt.Errorf("branch %s already exists: %w", in.Name, ErrConflict)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		fork, err := tx.Baseline(ctx, in.From)
		if err != nil {
			return err
		}
		b = domain.Branch{Name: in.Name, Parent: domain.BranchOf(fork.Branch), ForkBaseline: fork.ID, Head: fork.ID,
			Origin: in.Origin, Status: domain.BranchOpen, CreatedAt: g.now()}
		return tx.PutBranch(ctx, b)
	})
	return
}

// Branches lists the branches (main appears once it has been applied to).
func (g *Graph) Branches(ctx context.Context) (bs []domain.Branch, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { bs, err = tx.Branches(ctx); return err })
	return
}

// Branch returns a branch.
func (g *Graph) Branch(ctx context.Context, name string) (b domain.Branch, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = branchOf(ctx, tx, name); return err })
	return
}

// SetBranchStatus closes a branch (merged / abandoned) or reopens it.
func (g *Graph) SetBranchStatus(ctx context.Context, name, status string) error {
	switch status {
	case domain.BranchOpen, domain.BranchMerged, domain.BranchAbandoned:
	default:
		return fmt.Errorf("invalid branch status %q: %w", status, ErrInvalid)
	}
	return g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Branch(ctx, name)
		if err != nil {
			return err
		}
		b.Status = status
		return tx.PutBranch(ctx, b)
	})
}

// BranchHead returns the latest baseline of a branch.
func (g *Graph) BranchHead(ctx context.Context, name string) (b domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = branchHead(ctx, tx, name); return err })
	return
}

// Versions returns every version of a node across branches (version graph).
func (g *Graph) Versions(ctx context.Context, id domain.NodeID) (vs []domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { vs, err = tx.Versions(ctx, id); return err })
	return
}

// branchOf returns a branch; main exists implicitly.
func branchOf(ctx context.Context, tx Tx, name string) (domain.Branch, error) {
	name = domain.BranchOf(name)
	b, err := tx.Branch(ctx, name)
	if errors.Is(err, ErrNotFound) && name == domain.MainBranch {
		head, herr := branchHead(ctx, tx, name)
		if herr != nil && !errors.Is(herr, ErrNotFound) {
			return b, herr
		}
		return domain.Branch{Name: name, Head: head.ID, Status: domain.BranchOpen}, nil
	}
	return b, err
}

func branchHead(ctx context.Context, tx Tx, name string) (domain.Baseline, error) {
	name = domain.BranchOf(name)
	b, err := tx.Branch(ctx, name)
	if err == nil && b.Head != "" {
		return tx.Baseline(ctx, b.Head)
	}
	if err != nil && (!errors.Is(err, ErrNotFound) || name != domain.MainBranch) {
		return domain.Baseline{}, err
	}
	// main without applied changes: its latest baseline
	bs, err := tx.Baselines(ctx)
	if err != nil {
		return domain.Baseline{}, err
	}
	for i := len(bs) - 1; i >= 0; i-- {
		if domain.BranchOf(bs[i].Branch) == name {
			return bs[i], nil
		}
	}
	return domain.Baseline{}, fmt.Errorf("branch %s has no baseline: %w", name, ErrNotFound)
}

// ---- Branch merge ------------------------------------------------------------

// Merge candidate kinds.
const (
	MergeAdded       = "added"        // node created on the source branch
	MergeFastForward = "fast_forward" // unchanged on the target branch
	MergeThreeWay    = "merge"        // changed on both branches
)

// MergeCandidate is a node changed on the source branch since its fork.
type MergeCandidate struct {
	Node     domain.NodeID   `json:"node"`
	Key      string          `json:"key"`
	Type     string          `json:"type"`
	Kind     string          `json:"kind"`
	Ancestor *domain.NodeRef `json:"ancestor,omitempty"`
	// Ours is the version on the target branch (nil when added or deleted there).
	Ours   *domain.NodeRef `json:"ours,omitempty"`
	Theirs domain.NodeRef  `json:"theirs"`
	// Deleted is set when the source branch deleted the node.
	Deleted   bool           `json:"deleted,omitempty"`
	Base      map[string]any `json:"base,omitempty"`
	OursProps map[string]any `json:"oursProps,omitempty"`
	// TheirsProps and Merged are the source and proposed merged properties.
	TheirsProps map[string]any `json:"theirsProps,omitempty"`
	Merged      map[string]any `json:"merged,omitempty"`
	Conflicts   []string       `json:"conflicts,omitempty"`
}

// MergePlan lists what merging a branch into another would do.
type MergePlan struct {
	From       string            `json:"from"`
	Into       string            `json:"into"`
	IntoHead   domain.BaselineID `json:"intoHead"`
	Candidates []MergeCandidate  `json:"candidates"`
}

// Conflicting returns the candidates that need a resolution.
func (p MergePlan) Conflicting() []MergeCandidate {
	var out []MergeCandidate
	for _, c := range p.Candidates {
		if len(c.Conflicts) > 0 {
			out = append(out, c)
		}
	}
	return out
}

// PlanMerge computes the 3-way merge of branch from into branch into.
func (g *Graph) PlanMerge(ctx context.Context, from, into string) (p MergePlan, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { p, err = planMerge(ctx, tx, from, into); return err })
	return
}

func planMerge(ctx context.Context, tx Tx, from, into string) (MergePlan, error) {
	into = domain.BranchOf(into)
	fb, err := tx.Branch(ctx, from)
	if err != nil {
		return MergePlan{}, err
	}
	if fb.Status != domain.BranchOpen {
		return MergePlan{}, fmt.Errorf("branch %s is %s: %w", from, fb.Status, ErrConflict)
	}
	fork, err := tx.Baseline(ctx, fb.ForkBaseline)
	if err != nil {
		return MergePlan{}, err
	}
	fromHead, err := tx.Baseline(ctx, fb.Head)
	if err != nil {
		return MergePlan{}, err
	}
	intoHead, err := branchHead(ctx, tx, into)
	if err != nil {
		return MergePlan{}, err
	}
	plan := MergePlan{From: from, Into: into, IntoHead: intoHead.ID}
	var theirs []domain.Node
	for id, v := range fromHead.Nodes {
		if fork.Nodes[id] != v {
			n, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: v})
			if err != nil {
				return plan, err
			}
			theirs = append(theirs, n)
		}
	}
	for id := range fork.Nodes {
		if _, ok := fromHead.Nodes[id]; ok {
			continue
		}
		n, err := tx.LatestOn(ctx, id, from)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return plan, err
		}
		if n.Deleted {
			theirs = append(theirs, n)
		}
	}
	slices.SortFunc(theirs, func(a, b domain.Node) int { return strings.Compare(a.Key, b.Key) })
	for _, t := range theirs {
		c := MergeCandidate{Node: t.ID, Key: t.Key, Type: t.Type, Theirs: t.Ref(), Deleted: t.Deleted, TheirsProps: t.Properties}
		ov, inInto := intoHead.Nodes[t.ID]
		_, inFork := fork.Nodes[t.ID]
		switch {
		case !inInto && !inFork:
			if t.Deleted {
				continue
			}
			c.Kind, c.Merged = MergeAdded, t.Properties
		case !inInto:
			// deleted on the target branch, changed on the source branch
			if t.Deleted {
				continue
			}
			anc := domain.NodeRef{ID: t.ID, Version: fork.Nodes[t.ID]}
			c.Kind, c.Ancestor, c.Merged, c.Conflicts = MergeThreeWay, &anc, t.Properties, []string{DeletedKey}
		default:
			ours, err := tx.Node(ctx, domain.NodeRef{ID: t.ID, Version: ov})
			if err != nil {
				return plan, err
			}
			anc, err := CommonAncestor(ctx, tx, t.ID, ov, t.Version)
			if err != nil {
				return plan, err
			}
			if anc.Version == t.Version {
				continue // already merged
			}
			an, err := tx.Node(ctx, anc)
			if err != nil {
				return plan, err
			}
			or := ours.Ref()
			c.Ours, c.Ancestor, c.Base, c.OursProps = &or, &anc, an.Properties, ours.Properties
			if anc.Version == ov {
				c.Kind, c.Merged = MergeFastForward, t.Properties
			} else {
				c.Kind = MergeThreeWay
				c.Merged, c.Conflicts = MergeProps(an.Properties, ours.Properties, t.Properties)
				if t.Deleted {
					c.Conflicts = []string{DeletedKey}
				}
			}
		}
		plan.Candidates = append(plan.Candidates, c)
	}
	return plan, nil
}

// Resolution resolves a merge conflict: Props is the resolved property map
// (for a deletion conflict, any resolution with Skip unset applies the
// deletion); Skip keeps the target version unchanged.
type Resolution struct {
	Props map[string]any `json:"props,omitempty"`
	Skip  bool           `json:"skip,omitempty"`
}

// MergeRequest merges a branch into another one.
type MergeRequest struct {
	From, Into  string
	Title       string
	Resolutions map[domain.NodeID]Resolution
}

// MergeResult is the merge change and the resulting baseline of the target branch.
type MergeResult struct {
	Change   domain.ChangeSet `json:"change"`
	Baseline domain.Baseline  `json:"baseline"`
	Plan     MergePlan        `json:"plan"`
}

// MergeBranch merges branch From into Into: a merge change with one
// merge_node proposal per candidate is created and applied on Into, and From
// is marked merged. Conflicts without a resolution fail with ErrConflict.
func (g *Graph) MergeBranch(ctx context.Context, in MergeRequest) (res MergeResult, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		plan, err := planMerge(ctx, tx, in.From, in.Into)
		if err != nil {
			return err
		}
		res.Plan = plan
		var unresolved []string
		for _, c := range plan.Conflicting() {
			if _, ok := in.Resolutions[c.Node]; !ok {
				unresolved = append(unresolved, c.Key)
			}
		}
		if len(unresolved) > 0 {
			return fmt.Errorf("merge %s into %s: unresolved conflicts on %s: %w", in.From, plan.Into, strings.Join(unresolved, ", "), ErrConflict)
		}
		title := in.Title
		if title == "" {
			title = fmt.Sprintf("merge %s into %s", in.From, plan.Into)
		}
		c := domain.ChangeSet{ID: domain.ChangeID(g.newID()), Title: title, Intent: title, Status: domain.ChangeActive,
			BaselineID: plan.IntoHead, Branch: plan.Into, CreatedAt: g.now(),
			Data: map[string]any{"merge": map[string]any{"from": in.From, "into": plan.Into}}}
		if err := tx.PutChange(ctx, c); err != nil {
			return err
		}
		for _, cand := range plan.Candidates {
			r, resolved := in.Resolutions[cand.Node]
			if r.Skip || (cand.Ours == nil && cand.Ancestor != nil) {
				continue // kept as is on the target (or deleted there)
			}
			props := cand.Merged
			if resolved && r.Props != nil {
				props = r.Props
			}
			theirs := cand.Theirs
			it := domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindProposal, Type: "merge", Status: domain.ItemAccepted,
				ProducedBy: "graph.merge", CreatedAt: g.now(),
				Proposal: &domain.Proposal{Op: domain.OpMergeNode, Node: &domain.NodeDraft{
					Base: cand.Ours, From: &theirs, Ancestor: cand.Ancestor, Key: cand.Key, Type: cand.Type, Properties: props}}}
			if len(cand.Conflicts) > 0 {
				it.Data = map[string]any{"conflicts": cand.Conflicts}
			}
			if err := tx.PutItem(ctx, c.ID, it); err != nil {
				return err
			}
		}
		if res.Baseline, err = g.applyTx(ctx, tx, c.ID, title); err != nil {
			return err
		}
		if res.Change, err = tx.Change(ctx, c.ID); err != nil {
			return err
		}
		fb, err := tx.Branch(ctx, in.From)
		if err != nil {
			return err
		}
		fb.Status = domain.BranchMerged
		return tx.PutBranch(ctx, fb)
	})
	return
}

// ---- Change divergence and rebase ---------------------------------------------

// Divergence is a node referenced by an active proposal of a change that
// moved on the change branch since the version the proposal is based on.
type Divergence struct {
	Item domain.ItemID     `json:"item"`
	Op   domain.ProposalOp `json:"op"`
	// Role of the reference in the proposal: base, from or to.
	Role string         `json:"role"`
	Base domain.NodeRef `json:"base"`
	Head domain.NodeRef `json:"head"`
	// HeadDeleted is set when the node was deleted on the branch.
	HeadDeleted bool `json:"headDeleted,omitempty"`
	// Update proposals: the 3-way merge of the base, the proposal and the head.
	Theirs    map[string]any `json:"theirs,omitempty"`
	Ours      map[string]any `json:"ours,omitempty"`
	Merged    map[string]any `json:"merged,omitempty"`
	Conflicts []string       `json:"conflicts,omitempty"`
}

// Divergences returns the divergences of a change against its branch.
func (g *Graph) Divergences(ctx context.Context, id domain.ChangeID) (ds []Divergence, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		ds, err = divergences(ctx, tx, c)
		return err
	})
	return
}

func activeProposals(c domain.ChangeSet) []domain.ChangeItem {
	var out []domain.ChangeItem
	for _, it := range c.Items {
		if st := c.EffectiveStatus(it.ID); it.Kind == domain.KindProposal && st != domain.ItemRejected && st != domain.ItemSuperseded {
			out = append(out, it)
		}
	}
	return out
}

func divergences(ctx context.Context, tx Tx, c domain.ChangeSet) ([]Divergence, error) {
	if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
		return nil, nil
	}
	branch := domain.BranchOf(c.Branch)
	var out []Divergence
	check := func(it domain.ChangeItem, role string, ref *domain.NodeRef) error {
		if ref == nil {
			return nil
		}
		head, err := tx.LatestOn(ctx, ref.ID, branch)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if head.Version == ref.Version {
			return nil
		}
		base, err := tx.Node(ctx, *ref)
		if err != nil {
			return err
		}
		d := Divergence{Item: it.ID, Op: it.Proposal.Op, Role: role, Base: *ref, Head: head.Ref(), HeadDeleted: head.Deleted}
		switch {
		case role != "base":
		case it.Proposal.Op == domain.OpUpdateNode:
			ours := maps.Clone(base.Properties)
			if ours == nil {
				ours = map[string]any{}
			}
			maps.Copy(ours, it.Proposal.Node.Properties)
			d.Theirs, d.Ours = head.Properties, ours
			d.Merged, d.Conflicts = MergeProps(base.Properties, ours, head.Properties)
			if head.Deleted {
				d.Conflicts = []string{DeletedKey}
			}
		case it.Proposal.Op == domain.OpDeleteNode && !head.Deleted:
			d.Theirs, d.Conflicts = head.Properties, []string{DeletedKey}
		}
		out = append(out, d)
		return nil
	}
	for _, it := range activeProposals(c) {
		p := it.Proposal
		if p.Node != nil {
			if err := check(it, "base", p.Node.Base); err != nil {
				return nil, err
			}
		}
		if p.Link != nil && p.Op == domain.OpAddLink {
			if err := check(it, "from", p.Link.From.Node); err != nil {
				return nil, err
			}
			if err := check(it, "to", p.Link.To.Node); err != nil {
				return nil, err
			}
		}
		if p.Op == domain.OpRemoveLink {
			l, err := linkIn(ctx, tx, c.BaselineID, p.Link.LinkID)
			if err != nil {
				return nil, err
			}
			from := l.From
			if err := check(it, "from", &from); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func linkIn(ctx context.Context, tx Tx, baseline domain.BaselineID, id domain.LinkID) (domain.Link, error) {
	b, err := tx.Baseline(ctx, baseline)
	if err != nil {
		return domain.Link{}, err
	}
	for nid, v := range b.Nodes {
		out, err := tx.OutLinks(ctx, domain.NodeRef{ID: nid, Version: v})
		if err != nil {
			return domain.Link{}, err
		}
		for _, l := range out {
			if l.ID == id {
				return l, nil
			}
		}
	}
	return domain.Link{}, fmt.Errorf("link %s not in baseline %s: %w", id, baseline, ErrNotFound)
}

// RebaseResult reports a rebase.
type RebaseResult struct {
	Change domain.ChangeSet `json:"change"`
	// Superseded maps each replaced item to its replacement ("" when dropped).
	Superseded  map[domain.ItemID]domain.ItemID `json:"superseded"`
	Divergences []Divergence                    `json:"divergences"`
}

// Rebase moves a change onto the head of its branch. Every diverged proposal
// is replaced by a proposal based on the head versions (update proposals
// carry the 3-way merged properties, or the resolution given for the item),
// which supersedes it; a merge item records each replacement. Conflicting
// divergences need a resolution (ErrConflict otherwise); proposals on nodes
// deleted on the branch are dropped.
func (g *Graph) Rebase(ctx context.Context, id domain.ChangeID, resolutions map[domain.ItemID]map[string]any) (res RebaseResult, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		c, err := tx.Change(ctx, id)
		if err != nil {
			return err
		}
		if c.Status == domain.ChangeApplied || c.Status == domain.ChangeAbandoned {
			return fmt.Errorf("change %s is %s: %w", id, c.Status, ErrConflict)
		}
		ds, err := divergences(ctx, tx, c)
		if err != nil {
			return err
		}
		head, err := branchHead(ctx, tx, c.Branch)
		if err != nil {
			return err
		}
		res.Divergences, res.Superseded = ds, map[domain.ItemID]domain.ItemID{}
		byItem := map[domain.ItemID][]Divergence{}
		var order []domain.ItemID
		for _, d := range ds {
			if len(d.Conflicts) > 0 {
				if _, ok := resolutions[d.Item]; !ok {
					return fmt.Errorf("item %s conflicts on %s (%s): %w", d.Item, d.Head, strings.Join(d.Conflicts, ", "), ErrConflict)
				}
			}
			if _, ok := byItem[d.Item]; !ok {
				order = append(order, d.Item)
			}
			byItem[d.Item] = append(byItem[d.Item], d)
		}
		for _, itemID := range order {
			old, _ := c.Item(itemID)
			repl, keep, err := rebaseItem(ctx, tx, old, byItem[itemID], resolutions[itemID])
			if err != nil {
				return err
			}
			record := domain.ChangeItem{ID: domain.ItemID(g.newID()), Kind: domain.KindMerge, Type: "rebase", Status: domain.ItemAccepted,
				ProducedBy: "graph.rebase", DerivedFrom: []domain.ItemID{itemID}, CreatedAt: g.now(),
				Data: map[string]any{"item": string(itemID), "divergences": toMaps(byItem[itemID]), "dropped": !keep}}
			if keep {
				repl.ID = domain.ItemID(g.newID())
				repl.Status = c.EffectiveStatus(itemID)
				if hasConflicts(byItem[itemID]) {
					repl.Status = domain.ItemProposed // resolved content must be reviewed again
				}
				repl.DerivedFrom = []domain.ItemID{itemID}
				repl.Supersedes = []domain.ItemID{itemID}
				repl.CreatedAt = g.now()
				if err := repl.Validate(); err != nil {
					return fmt.Errorf("rebased item %s: %v: %w", itemID, err, ErrInvalid)
				}
				if err := tx.PutItem(ctx, id, repl); err != nil {
					return err
				}
				record.Data["replacement"] = string(repl.ID)
				res.Superseded[itemID] = repl.ID
			} else {
				record.Supersedes = []domain.ItemID{itemID}
				res.Superseded[itemID] = ""
			}
			if err := tx.PutItem(ctx, id, record); err != nil {
				return err
			}
		}
		if c.Data == nil {
			c.Data = map[string]any{}
		}
		rebases, _ := c.Data["rebases"].([]any)
		superseded := map[string]any{}
		for k, v := range res.Superseded {
			superseded[string(k)] = string(v)
		}
		c.Data["rebases"] = append(rebases, map[string]any{"from": string(c.BaselineID), "to": string(head.ID),
			"at": g.now().Format("2006-01-02T15:04:05Z07:00"), "superseded": superseded})
		c.BaselineID = head.ID
		if err := tx.PutChange(ctx, c); err != nil {
			return err
		}
		res.Change, err = tx.Change(ctx, id)
		return err
	})
	return
}

func hasConflicts(ds []Divergence) bool {
	for _, d := range ds {
		if len(d.Conflicts) > 0 {
			return true
		}
	}
	return false
}

// rebaseItem returns the replacement of an item, or keep=false when the item
// must be dropped (its node was deleted on the branch).
func rebaseItem(ctx context.Context, tx Tx, old domain.ChangeItem, ds []Divergence, resolved map[string]any) (domain.ChangeItem, bool, error) {
	it := old
	p := *old.Proposal
	it.Proposal = &p
	if p.Node != nil {
		n := *p.Node
		p.Node = &n
	}
	if p.Link != nil {
		l := *p.Link
		p.Link = &l
	}
	for _, d := range ds {
		if d.HeadDeleted {
			return it, false, nil
		}
		head := d.Head
		switch d.Role {
		case "base":
			p.Node.Base = &head
			if p.Op == domain.OpUpdateNode {
				props := d.Merged
				if resolved != nil {
					props = resolved
				}
				diff := map[string]any{}
				for k, v := range props {
					if tv, ok := d.Theirs[k]; !ok || !jsonEqual(tv, v) {
						diff[k] = v
					}
				}
				p.Node.Properties = diff
			}
		case "from":
			if p.Link.From.Node != nil {
				p.Link.From.Node = &head
			}
		case "to":
			p.Link.To.Node = &head
		}
	}
	if p.Op == domain.OpRemoveLink {
		// find the link carried forward on the head version of the source
		for _, d := range ds {
			if d.Role != "from" {
				continue
			}
			l, err := sameLink(ctx, tx, p.Link.LinkID, d)
			if err != nil {
				return it, false, err
			}
			if l == "" {
				return it, false, nil
			}
			p.Link.LinkID = l
		}
	}
	return it, true, nil
}

// sameLink returns the id of the link equivalent to id (same type and target
// node) on the head version of its source, "" when it no longer exists.
func sameLink(ctx context.Context, tx Tx, id domain.LinkID, d Divergence) (domain.LinkID, error) {
	out, err := tx.OutLinks(ctx, d.Base)
	if err != nil {
		return "", err
	}
	var old *domain.Link
	for i := range out {
		if out[i].ID == id {
			old = &out[i]
		}
	}
	if old == nil {
		return "", nil
	}
	cur, err := tx.OutLinks(ctx, d.Head)
	if err != nil {
		return "", err
	}
	for _, l := range cur {
		if l.Type == old.Type && l.To.ID == old.To.ID {
			return l.ID, nil
		}
	}
	return "", nil
}

func toMaps(ds []Divergence) []any {
	out := make([]any, 0, len(ds))
	for _, d := range ds {
		var m map[string]any
		b, _ := json.Marshal(d)
		_ = json.Unmarshal(b, &m)
		out = append(out, m)
	}
	return out
}
