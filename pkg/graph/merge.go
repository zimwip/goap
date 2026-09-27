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
	Name string
	// Namespace the branch belongs to (default: domain.DefaultNamespace).
	Namespace string
	From      domain.BaselineID
	Origin    string // change / option that opens the branch
}

// CreateBranch opens a branch forked from a baseline.
func (g *Graph) CreateBranch(ctx context.Context, in NewBranch) (b domain.Branch, err error) {
	if in.Name == "" || in.Name == domain.MainBranch || strings.ContainsAny(in.Name, " \t\n") {
		return b, fmt.Errorf("invalid branch name %q: %w", in.Name, ErrInvalid)
	}
	namespace := domain.NamespaceOf(in.Namespace)
	err = g.repo.InTx(ctx, func(tx Tx) error {
		if _, err := tx.Branch(ctx, namespace, in.Name); err == nil {
			return fmt.Errorf("branch %s already exists in namespace %s: %w", in.Name, namespace, ErrConflict)
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		fork, err := tx.Baseline(ctx, in.From)
		if err != nil {
			return err
		}
		if fork.Namespace != namespace {
			return fmt.Errorf("baseline %s is of namespace %s, not %s: %w", in.From, fork.Namespace, namespace, ErrInvalid)
		}
		b = domain.Branch{Name: in.Name, Namespace: namespace, Parent: domain.BranchOf(fork.Branch), ForkBaseline: fork.ID, Head: fork.ID,
			Origin: in.Origin, Status: domain.BranchOpen, CreatedAt: g.now()}
		return tx.PutBranch(ctx, b)
	})
	return
}

// Branches lists the branches of a namespace (main appears once it has been applied to).
func (g *Graph) Branches(ctx context.Context, namespace string) (bs []domain.Branch, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { bs, err = tx.Branches(ctx, namespace); return err })
	return
}

// Branch returns a branch of a namespace.
func (g *Graph) Branch(ctx context.Context, namespace, name string) (b domain.Branch, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = branchOf(ctx, tx, namespace, name); return err })
	return
}

// SetBranchStatus closes a branch (merged / abandoned) or reopens it.
func (g *Graph) SetBranchStatus(ctx context.Context, namespace, name, status string) error {
	switch status {
	case domain.BranchOpen, domain.BranchMerged, domain.BranchAbandoned:
	default:
		return fmt.Errorf("invalid branch status %q: %w", status, ErrInvalid)
	}
	return g.repo.InTx(ctx, func(tx Tx) error {
		b, err := tx.Branch(ctx, namespace, name)
		if err != nil {
			return err
		}
		b.Status = status
		return tx.PutBranch(ctx, b)
	})
}

// BranchHead returns the latest baseline of a branch of a namespace.
func (g *Graph) BranchHead(ctx context.Context, namespace, name string) (b domain.Baseline, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { b, err = branchHead(ctx, tx, namespace, name); return err })
	return
}

// Versions returns every version of a node across branches (version graph).
func (g *Graph) Versions(ctx context.Context, id domain.NodeID) (vs []domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { vs, err = tx.Versions(ctx, id); return err })
	return
}

// branchOf returns a branch of a namespace; main exists implicitly.
func branchOf(ctx context.Context, tx Tx, namespace, name string) (domain.Branch, error) {
	namespace, name = domain.NamespaceOf(namespace), domain.BranchOf(name)
	b, err := tx.Branch(ctx, namespace, name)
	if errors.Is(err, ErrNotFound) && name == domain.MainBranch {
		head, herr := branchHead(ctx, tx, namespace, name)
		if herr != nil && !errors.Is(herr, ErrNotFound) {
			return b, herr
		}
		return domain.Branch{Name: name, Namespace: namespace, Head: head.ID, Status: domain.BranchOpen}, nil
	}
	return b, err
}

func branchHead(ctx context.Context, tx Tx, namespace, name string) (domain.Baseline, error) {
	namespace, name = domain.NamespaceOf(namespace), domain.BranchOf(name)
	b, err := tx.Branch(ctx, namespace, name)
	if err == nil && b.Head != "" {
		return tx.Baseline(ctx, b.Head)
	}
	if err != nil && (!errors.Is(err, ErrNotFound) || name != domain.MainBranch) {
		return domain.Baseline{}, err
	}
	// main without applied changes: its latest baseline
	bs, err := tx.Baselines(ctx, namespace)
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

// PlanMerge computes the 3-way merge of branch from into branch into, both of namespace.
func (g *Graph) PlanMerge(ctx context.Context, namespace, from, into string) (p MergePlan, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { p, err = planMerge(ctx, tx, namespace, from, into); return err })
	return
}

func planMerge(ctx context.Context, tx Tx, namespace, from, into string) (MergePlan, error) {
	namespace, into = domain.NamespaceOf(namespace), domain.BranchOf(into)
	fb, err := tx.Branch(ctx, namespace, from)
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
	intoHead, err := branchHead(ctx, tx, namespace, into)
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
	From, Into string
	Title      string
	// Namespace of the merge change (default: domain.DefaultNamespace).
	Namespace   string
	Resolutions map[domain.NodeID]Resolution
}

// MergeResult is the merge change and the resulting baseline of the target branch.
type MergeResult struct {
	Change   domain.Change   `json:"change"`
	Baseline domain.Baseline `json:"baseline"`
	Plan     MergePlan       `json:"plan"`
}

// MergeBranch merges branch From into Into: a merge change with one change
// node per merged node is created, the merge versions are written on Into, and
// From is marked merged. Conflicts without a resolution fail with ErrConflict.
func (g *Graph) MergeBranch(ctx context.Context, in MergeRequest) (res MergeResult, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		res, err = g.mergeBranchTx(ctx, tx, in)
		return err
	})
	return
}

func (g *Graph) mergeBranchTx(ctx context.Context, tx Tx, in MergeRequest) (res MergeResult, err error) {
	namespace := domain.NamespaceOf(in.Namespace)
	plan, err := planMerge(ctx, tx, namespace, in.From, in.Into)
	if err != nil {
		return res, err
	}
	res.Plan = plan
	if un := unresolved(plan, in.Resolutions); len(un) > 0 {
		return res, fmt.Errorf("merge %s into %s: unresolved conflicts on %s: %w", in.From, plan.Into, strings.Join(un, ", "), ErrConflict)
	}
	title := in.Title
	if title == "" {
		title = fmt.Sprintf("merge %s into %s", in.From, plan.Into)
	}
	// the merge is a change of the platform (journal): one change impact per merged node, whose
	// version records the origin; the merge versions are written here, not proposed
	c := domain.Change{ID: domain.ChangeID(g.newID()), Title: title, Intent: title, Status: domain.ChangeActive,
		Namespace: domain.NamespaceOf(in.Namespace), BaselineID: plan.IntoHead, Branch: plan.Into, CreatedAt: g.now(),
		Data: map[string]any{"merge": map[string]any{"from": in.From, "into": plan.Into}}}
	if err := tx.PutChange(ctx, c); err != nil {
		return res, err
	}
	base, err := tx.Baseline(ctx, plan.IntoHead)
	if err != nil {
		return res, err
	}
	ix, err := g.typesAt(ctx, tx, plan.IntoHead)
	if err != nil {
		return res, err
	}
	target := maps.Clone(base.Nodes)
	type merged struct {
		cand       MergeCandidate
		from, ours *domain.Node
		anc        *domain.Node
		next       domain.NodeRef
	}
	var todo []*merged
	for _, cand := range plan.Candidates {
		r, resolved := in.Resolutions[cand.Node]
		if r.Skip || (cand.Ours == nil && cand.Ancestor != nil) {
			continue // kept as is on the target (or deleted there)
		}
		from, err := tx.Node(ctx, cand.Theirs)
		if err != nil {
			return res, err
		}
		m := &merged{cand: cand, from: &from}
		props := cand.Merged
		if resolved && r.Props != nil {
			props = r.Props
		}
		if cand.Ours != nil {
			if v, ok := target[cand.Ours.ID]; !ok || v != cand.Ours.Version {
				return res, fmt.Errorf("node %s is not in the reference baseline: %w", cand.Ours, ErrConflict)
			}
			if head, err := tx.LatestOn(ctx, cand.Ours.ID, plan.Into); err == nil && head.Version != cand.Ours.Version {
				return res, fmt.Errorf("node %s was modified on %s since the reference baseline (now v%d): %w", cand.Ours, plan.Into, head.Version, ErrConflict)
			} else if err != nil && !errors.Is(err, ErrNotFound) {
				return res, err
			}
			ours, err := tx.Node(ctx, *cand.Ours)
			if err != nil {
				return res, err
			}
			m.ours = &ours
			anc := cand.Ancestor
			if anc == nil {
				a, err := CommonAncestor(ctx, tx, from.ID, cand.Ours.Version, from.Version)
				if err != nil {
					return res, err
				}
				anc = &a
			}
			an, err := tx.Node(ctx, *anc)
			if err != nil {
				return res, err
			}
			m.anc = &an
			if props == nil {
				props, _ = MergeProps(an.Properties, ours.Properties, from.Properties)
			}
		} else {
			if _, ok := target[from.ID]; ok {
				return res, fmt.Errorf("node %s is already in the reference baseline, merge needs its version there: %w", from.ID, ErrConflict)
			}
			if props == nil {
				props = from.Properties
			}
		}
		m.from.Properties = props // the merged properties, carried by the version below
		todo = append(todo, m)
	}
	// 1. the merge versions, with the change impact that explains each
	var editable []string
	for _, m := range todo {
		n := *m.from
		n.Reason, n.Parents = domain.ReasonMerge, []domain.Version{m.from.Version}
		deleted := m.from.Deleted
		if m.ours != nil {
			n.Parents = []domain.Version{m.ours.Version, m.from.Version}
			deleted = deleted || m.ours.Deleted
		}
		v, err := nextVersion(ctx, tx, n.ID)
		if err != nil {
			return res, err
		}
		why := fmt.Sprintf("merge of %s into %s", in.From, plan.Into)
		if len(m.cand.Conflicts) > 0 {
			why += fmt.Sprintf(" (conflicts on %s, resolved)", strings.Join(m.cand.Conflicts, ", "))
		}
		cn := domain.ChangeImpact{ID: domain.ChangeImpactID(g.newID()), Key: n.Key, Type: n.Type, Intent: domain.IntentCreated, Rationale: why,
			Review: domain.ReviewAccepted, ProducedBy: "graph.merge", CreatedAt: g.now()}
		if m.ours != nil {
			cn.Intent, cn.Pre = domain.IntentModified, m.cand.Ours
		}
		n.Version, n.Branch, n.Deleted, n.ChangeID, n.ChangeImpact, n.Comment, n.CreatedAt = v, plan.Into, deleted, c.ID, cn.ID, why, g.now()
		if err := tx.PutNode(ctx, n); err != nil {
			return res, err
		}
		m.next = n.Ref()
		cn.Post, cn.Landed = &m.next, &m.next
		cn.Reviews = []domain.Review{{Status: domain.ReviewAccepted, By: "graph.merge", Comment: why, At: g.now()}}
		if err := tx.PutChangeImpact(ctx, c.ID, cn); err != nil {
			return res, err
		}
		if deleted {
			delete(target, n.ID)
			continue
		}
		target[n.ID] = n.Version
		if lc := ix.lifecycleOf(n.Type); lc != nil && n.State != "" && lc.Editable(n.State) {
			editable = append(editable, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	if len(editable) > 0 {
		return res, invalidf("the change leaves nodes in an editable state, move them out of it before applying: %s", strings.Join(editable, ", "))
	}
	// 2. their links: a 3-way merge keyed by (type, target node), retargeted to the merged versions
	newRef := map[domain.NodeID]domain.NodeRef{}
	for _, m := range todo {
		newRef[m.next.ID] = m.next
	}
	for _, m := range todo {
		if v, ok := target[m.next.ID]; !ok || v != m.next.Version {
			continue // deleted
		}
		theirs, err := tx.OutLinks(ctx, m.from.Ref())
		if err != nil {
			return res, err
		}
		ours, anc := theirs, theirs // a node new on the target: take the links of the source
		if m.anc != nil {
			if ours, err = tx.OutLinks(ctx, m.ours.Ref()); err != nil {
				return res, err
			}
			if anc, err = tx.OutLinks(ctx, m.anc.Ref()); err != nil {
				return res, err
			}
		}
		for _, l := range MergeLinks(anc, ours, theirs) {
			to := l.To
			if r, ok := newRef[to.ID]; ok {
				if v, live := target[r.ID]; !live || v != r.Version {
					continue // the target was deleted
				}
				to = r
			} else if _, ok := target[to.ID]; !ok {
				continue
			}
			if err := tx.PutLink(ctx, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, From: m.next, To: to, Properties: l.Properties, ChangeID: c.ID}); err != nil {
				return res, err
			}
		}
	}
	res.Baseline = domain.Baseline{ID: domain.BaselineID(g.newID()), Name: title, Namespace: namespace, Branch: plan.Into, ParentID: base.ID, ChangeID: c.ID, Nodes: target, CreatedAt: g.now()}
	if err := tx.PutBaseline(ctx, res.Baseline); err != nil {
		return res, err
	}
	if err := g.advanceBranch(ctx, tx, namespace, plan.Into, res.Baseline.ID); err != nil {
		return res, err
	}
	c.Status, c.ResultBaselineID = domain.ChangeApplied, res.Baseline.ID
	if err := tx.PutChange(ctx, c); err != nil {
		return res, err
	}
	res.Change = c
	fb, err := tx.Branch(ctx, namespace, in.From)
	if err != nil {
		return res, err
	}
	fb.Status = domain.BranchMerged
	return res, tx.PutBranch(ctx, fb)
}
