package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/lib/platform"
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
	key := func(l domain.Link) string {
		if l.External() {
			return l.Type + "\x00" + l.Target.Logical().String()
		}
		return l.Type + "\x00" + string(l.To.ID)
	}
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

// CommonAncestor returns the most recent common ancestor of two versions of
// a node (a version is its own ancestor).
func CommonAncestor(ctx context.Context, tx Tx, id domain.NodeID, a, b domain.Version) (domain.NodeRef, error) {
	vs, err := tx.Versions(ctx, id)
	if err != nil {
		return domain.NodeRef{}, err
	}
	parents := map[domain.Version][]domain.Version{}
	for _, n := range vs {
		parents[n.Version] = n.Parents
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

// CreateBranch opens a branch forked from a commit.
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
		fork, err := tx.Commit(ctx, in.From)
		if err != nil {
			return err
		}
		if fork.ID != "" && fork.Namespace != namespace {
			return fmt.Errorf("commit %s is of namespace %s, not %s: %w", in.From, fork.Namespace, namespace, ErrInvalid)
		}
		b = domain.Branch{Name: in.Name, Namespace: namespace, Parent: domain.BranchOf(fork.Branch), ForkCommit: fork.ID, Head: fork.ID,
			Origin: in.Origin, Status: domain.BranchOpen, CreatedAt: g.now(), Description: in.Description}
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

// SetBranchDescription sets the free-text description of a branch (main included: unlike CreateBranch, this may
// target main, persisting its row on first use).
func (g *Graph) SetBranchDescription(ctx context.Context, namespace, name, description string) error {
	return g.repo.InTx(ctx, func(tx Tx) error {
		b, err := branchOf(ctx, tx, namespace, name)
		if err != nil {
			return err
		}
		b.Description = description
		return tx.PutBranch(ctx, b)
	})
}

// BranchHead returns the latest commit of a branch of a namespace; the head of main before any change committed is the
// empty state (empty id, no nodes), the head of a branch nothing committed on yet its fork (ADR 0175).
func (g *Graph) BranchHead(ctx context.Context, namespace, name string) (b domain.Commit, err error) {
	err = g.repo.InTx(platform.ReadOnly(ctx), func(tx Tx) error {
		b, err = branchHead(ctx, tx, namespace, name)
		return err
	})
	return
}

// Versions returns every version of a node across branches (version graph).
func (g *Graph) Versions(ctx context.Context, id domain.NodeID) (vs []domain.Node, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error { vs, err = tx.Versions(ctx, id); return err })
	return
}

// VersionPage returns a page of the history of a node (ADR 0140): its last limit versions (0: 50, at most 500) below
// the version before (0: up to its latest), by version, and the before of the next older page (0: none).
func (g *Graph) VersionPage(ctx context.Context, id domain.NodeID, before domain.Version, limit int) (vs []domain.Node, next domain.Version, err error) {
	err = g.repo.InTx(ctx, func(tx Tx) error {
		vs, err = tx.VersionRange(ctx, id, before, pageLimit(limit))
		if err == nil && len(vs) == 0 && before == 0 {
			err = fmt.Errorf("node %s: %w", id, ErrNotFound)
		}
		return err
	})
	if len(vs) > 0 && vs[0].Version > 1 {
		next = vs[0].Version
	}
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

func branchHead(ctx context.Context, tx Tx, namespace, name string) (domain.Commit, error) {
	namespace, name = domain.NamespaceOf(namespace), domain.BranchOf(name)
	b, err := tx.Branch(ctx, namespace, name)
	if err == nil && b.HeadOrFork() != "" {
		return tx.Commit(ctx, b.HeadOrFork())
	}
	if err != nil && (!errors.Is(err, ErrNotFound) || name != domain.MainBranch) {
		return domain.Commit{}, err
	}
	// main without applied changes: its latest commit, if any change left one
	bs, err := tx.Commits(ctx, namespace)
	if err != nil {
		return domain.Commit{}, err
	}
	for i := len(bs) - 1; i >= 0; i-- {
		if domain.BranchOf(bs[i].Branch) == name {
			return bs[i], nil
		}
	}
	// no change has committed in the namespace yet: its state is the empty one (ADR 0056), the empty commit id
	return domain.Commit{Namespace: namespace, Branch: name, Nodes: map[domain.NodeID]domain.Version{}}, nil
}

// branchStates reads the whole state a branch was forked from and the one it holds: its fork and head commits; for the
// branch of a change, its fork with what committed on it as it stands in the transaction (ADR 0175).
func branchStates(ctx context.Context, tx Tx, b domain.Branch) (fork, head map[domain.NodeID]domain.Version, err error) {
	if b.Ephemeral {
		if fork, err = baseVersions(ctx, tx, b, nil); err != nil {
			return nil, nil, err
		}
		head, err = branchVersions(ctx, tx, b, nil)
		return fork, head, err
	}
	f, err := tx.Commit(ctx, b.ForkCommit)
	if err != nil {
		return nil, nil, err
	}
	h, err := tx.Commit(ctx, b.Head)
	return f.Nodes, h.Nodes, err
}

// ---- Branch merge ------------------------------------------------------------

// Merge candidate kinds.
const (
	MergeAdded       = "added"        // node created on the source branch
	MergeFastForward = "fast_forward" // unchanged on the target branch
	MergeThreeWay    = "merge"        // changed on both branches
)

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
	fork, fromHead, err := branchStates(ctx, tx, fb)
	if err != nil {
		return MergePlan{}, err
	}
	intoHead, err := branchHead(ctx, tx, namespace, into)
	if err != nil {
		return MergePlan{}, err
	}
	plan := MergePlan{From: from, Into: into, IntoHead: intoHead.ID}
	var theirs []domain.Node
	for id, v := range fromHead {
		if fork[id] != v {
			n, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: v})
			if err != nil {
				return plan, err
			}
			theirs = append(theirs, n)
		}
	}
	for id := range fork {
		if _, ok := fromHead[id]; ok {
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
		_, inFork := fork[t.ID]
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
			anc := domain.NodeRef{ID: t.ID, Version: fork[t.ID]}
			c.Kind, c.Ancestor, c.Merged, c.Conflicts = MergeThreeWay, &anc, t.Properties, []string{DeletedKey}
		default:
			ours, err := tx.Node(ctx, domain.NodeRef{ID: t.ID, Version: ov})
			if err != nil {
				return plan, err
			}
			or := ours.Ref()
			anc, err := CommonAncestor(ctx, tx, t.ID, ov, t.Version)
			if errors.Is(err, ErrNotFound) {
				// the node created on both lines of work, each its own history (takeKey): a merge with no base
				c.Ours, c.OursProps, c.Kind = &or, ours.Properties, MergeThreeWay
				c.Merged, c.Conflicts = MergeProps(nil, ours.Properties, t.Properties)
				if t.Deleted {
					c.Conflicts = []string{DeletedKey}
				}
				plan.Candidates = append(plan.Candidates, c)
				continue
			}
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

// MergeRequest merges a branch into another one.
type MergeRequest struct {
	From, Into string
	Title      string
	// Namespace of the merge change (default: domain.DefaultNamespace).
	Namespace   string
	Resolutions map[domain.NodeID]Resolution
	// OwnerOrg is the unit holding the merge change and ProjectID the project it acts in (ADR 0054); empty: the root
	// unit. The project is required (ADR 0091).
	OwnerOrg, ProjectID string
}

// MergeResult is the merge change and the resulting commit of the target branch.
type MergeResult struct {
	Change domain.Change `json:"change"`
	Commit domain.Commit `json:"commit"`
	Plan   MergePlan     `json:"plan"`
	// merged are the versions the merge wrote or joined on the target (change.merged, ADR 0159)
	merged []domain.Node
}

// MergeBranch merges branch From into Into: a merge change with one change
// node per merged node is created, the merge versions are written on Into, and
// From is marked merged. Conflicts without a resolution fail with ErrConflict.
func (g *Graph) MergeBranch(ctx context.Context, in MergeRequest) (res MergeResult, err error) {
	if err := g.Bootstrap(ctx); err != nil {
		return res, err
	}
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
	owner, err := g.StructureRef(domain.StructureOrganisation, in.OwnerOrg)
	if err != nil {
		return res, err
	}
	project, err := g.StructureRef(domain.StructureProject, in.ProjectID)
	if err != nil {
		return res, err
	}
	// the merge is a change of the platform (journal): one change impact per merged node, whose
	// version records the origin; the merge versions are written here, not proposed
	c := domain.Change{ID: domain.ChangeID(g.newID()), Title: title, Intent: title, Status: domain.ChangeActive,
		Namespace: domain.NamespaceOf(in.Namespace), ForkCommit: plan.IntoHead, Branch: plan.Into, CreatedAt: g.now(),
		Data: map[string]any{"merge": map[string]any{"from": in.From, "into": plan.Into}}, OwnerOrg: owner, ProjectID: project}
	if err := g.scopeChange(ctx, tx, &c); err != nil {
		return res, err
	}
	if err := tx.PutChange(ctx, c); err != nil {
		return res, err
	}
	base, err := tx.Commit(ctx, plan.IntoHead)
	if err != nil {
		return res, err
	}
	fb, err := tx.Branch(ctx, namespace, in.From)
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
		// join: the source version commits as is (added or fast-forward, no resolution), no merge version (ADR 0032)
		join bool
		// keep: the target keeps what it holds (a node skipped, or deleted there): the commits of the source the
		// target takes bring their version, the target's own version at the merge keeps its own (ADR 0176)
		keep bool
	}
	var todo []*merged
	var joined []domain.NodeRef
	for _, cand := range plan.Candidates {
		r, resolved := in.Resolutions[cand.Node]
		if r.Skip || (cand.Ours == nil && cand.Ancestor != nil) {
			// kept as is on the target (or deleted there)
			from, err := tx.Node(ctx, cand.Theirs)
			if err != nil {
				return res, err
			}
			m := &merged{cand: cand, from: &from, keep: true}
			if cand.Ours != nil {
				ours, err := tx.Node(ctx, *cand.Ours)
				if err != nil {
					return res, err
				}
				m.ours = &ours
			}
			todo = append(todo, m)
			continue
		}
		from, err := tx.Node(ctx, cand.Theirs)
		if err != nil {
			return res, err
		}
		m := &merged{cand: cand, from: &from, join: (cand.Kind == MergeAdded || cand.Kind == MergeFastForward) && !(resolved && r.Props != nil)}
		props := cand.Merged
		if resolved && r.Props != nil {
			props = r.Props
		}
		if cand.Ours != nil {
			if v, ok := target[cand.Ours.ID]; !ok || v != cand.Ours.Version {
				return res, fmt.Errorf("node %s is not in the reference commit: %w", cand.Ours, ErrConflict)
			}
			if head, err := tx.LatestOn(ctx, cand.Ours.ID, plan.Into); err == nil && head.Version != cand.Ours.Version {
				return res, fmt.Errorf("node %s was modified on %s since the reference commit (now v%d): %w", cand.Ours, plan.Into, head.Version, ErrConflict)
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
				if err != nil && !errors.Is(err, ErrNotFound) {
					return res, err
				}
				if err == nil {
					anc = &a
				}
			}
			var base map[string]any
			if anc != nil {
				an, err := tx.Node(ctx, *anc)
				if err != nil {
					return res, err
				}
				m.anc, base = &an, an.Properties
			}
			if props == nil {
				props, _ = MergeProps(base, ours.Properties, from.Properties)
			}
		} else {
			if _, ok := target[from.ID]; ok {
				return res, fmt.Errorf("node %s is already in the reference commit, merge needs its version there: %w", from.ID, ErrConflict)
			}
			if props == nil {
				props = from.Properties
			}
		}
		m.from.Properties = props // the merged properties, carried by the version below
		todo = append(todo, m)
	}
	// a revert the source holds of a node no candidate names (the source holds it as its fork does, ADR 0140) is a version
	// the target takes with the commits of the source as it is (ADR 0176): when it is no copy of what the target holds,
	// the target keeps its own by a version of the merge
	named := make(map[domain.NodeID]bool, len(todo))
	for _, m := range todo {
		named[m.cand.Node] = true
	}
	for _, cand := range plan.Candidates {
		named[cand.Node] = true
	}
	revs, err := tx.BranchCommits(ctx, namespace, in.From, math.MaxInt64, nil)
	if err != nil {
		return res, err
	}
	for _, id := range slices.Sorted(maps.Keys(revs)) {
		l := revs[id]
		if !l.Revert || named[id] {
			continue
		}
		from, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: l.Version})
		if err != nil {
			return res, err
		}
		m := &merged{cand: MergeCandidate{Node: id, Key: from.Key, Type: from.Type, Theirs: from.Ref()}, from: &from, keep: true}
		if tv, ok := target[id]; ok {
			ours, err := tx.Node(ctx, domain.NodeRef{ID: id, Version: tv})
			if err != nil {
				return res, err
			}
			if !from.Deleted && from.State == ours.State && reflect.DeepEqual(from.Properties, ours.Properties) {
				continue // a copy of what the target holds
			}
			m.ours = &ours
		} else if from.Deleted {
			continue
		}
		todo = append(todo, m)
	}
	// 1. the merge versions, with the change impact that explains each: a node changed on one side only joins the
	// target as is (ADR 0032), a node changed on both sides gets a merge version
	res.Commit.ID = domain.CommitID(g.newID())
	// the merge accepts what it commits in one review, recorded before the impacts it decides (ADR 0105)
	whyOf := func(m *merged) string {
		why := fmt.Sprintf("merge of %s into %s", in.From, plan.Into)
		if len(m.cand.Conflicts) > 0 {
			why += fmt.Sprintf(" (conflicts on %s, resolved)", strings.Join(m.cand.Conflicts, ", "))
		}
		return why
	}
	impactIDs := make([]domain.ChangeImpactID, len(todo))
	entries := make([]domain.ReviewEntry, len(todo))
	for i, m := range todo {
		impactIDs[i] = domain.ChangeImpactID(g.newID())
		entries[i] = domain.ReviewEntry{Impact: string(impactIDs[i]), Comment: whyOf(m), Outcome: domain.ReviewOutcomeAccept}
	}
	var stuck []string
	var phases [4][]domain.ImpactEvent
	// writes are the merge versions, written once the impacts are declared and reviewed, each naming the entry
	// committing it (ADR 0174 addendum)
	var writes []domain.Node
	for i, m := range todo {
		n := *m.from
		n.Reason, n.Parents = domain.ReasonMerge, []domain.Version{m.from.Version}
		deleted := m.from.Deleted
		if m.ours != nil {
			n.Parents = []domain.Version{m.ours.Version, m.from.Version}
			deleted = deleted || m.ours.Deleted
		}
		if m.keep {
			// what the target holds, or nothing: a tombstone of a node it does not hold
			deleted = true
			if m.ours != nil {
				n = *m.ours
				n.Reason, n.Parents, deleted = domain.ReasonMerge, []domain.Version{m.ours.Version, m.from.Version}, m.ours.Deleted
			}
		}
		why := whyOf(m)
		cn := domain.ChangeImpact{ID: impactIDs[i], Key: n.Key, Type: n.Type, Intent: domain.IntentCreated, Rationale: why,
			Review: domain.ReviewAccepted, ProducedBy: "graph.merge", CreatedAt: g.now()}
		if m.ours != nil {
			cn.Intent, cn.Pre = domain.IntentModified, m.cand.Ours
		}
		if m.join {
			n = *m.from
			joined = append(joined, n.Ref())
		} else {
			v, err := nextVersion(ctx, tx, n.ID)
			if err != nil {
				return res, err
			}
			n.Version, n.Branch, n.Deleted, n.ChangeID, n.ChangeImpact, n.Comment, n.CreatedAt = v, plan.Into, deleted, c.ID, cn.ID, why, g.now()
			n.Editor, n.Visibility = "", "" // the merger's version, seen as the domain says now (ADR 0161)
			writes = append(writes, n)
		}
		m.next = n.Ref()
		// the merge records its change impact as events, accepted and committed at once: a node the merge creates on the
		// target is a created event (the impact and its version), one it modifies is proposed, then written
		declared := cn
		declared.Review = domain.ReviewProposed
		first := domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactProposed, By: "graph.merge", State: &declared}
		var written []domain.ImpactEvent
		if cn.Intent == domain.IntentCreated {
			first.Op, first.Post = domain.ImpactCreated, &m.next
		} else {
			written = append(written, domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactTransitioned, By: "graph.merge", Post: &m.next})
		}
		// by phase, so that the declarations and the versions of the commit are one entry each (ADR 0144); the review
		// accepting them is written between, the verdicts derived from it (ADR 0105 addendum)
		phases[0] = append(phases[0], first)
		phases[2] = append(phases[2], written...)
		phases[3] = append(phases[3], domain.ImpactEvent{Change: c.ID, Impact: cn.ID, Op: domain.ImpactCommitted, By: "graph.merge", Committed: &m.next, Commit: res.Commit.ID, Branch: domain.BranchOf(plan.Into)})
		res.merged = append(res.merged, n)
		if n.Deleted {
			delete(target, n.ID)
			continue
		}
		target[n.ID] = n.Version
		if lc := ix.lifecycleOf(n.Type); lc != nil && !lc.Committable(n.State) {
			stuck = append(stuck, fmt.Sprintf("%s (%s) in %s", n.Key, n.Type, n.State))
		}
	}
	// their links: a 3-way merge keyed by (type, target node), retargeted to the merged versions, planned before the
	// versions are written so that each is written whole (ADR 0174 addendum)
	plans := map[domain.NodeID]linkPlan{}
	newRef := map[domain.NodeID]domain.NodeRef{}
	for _, m := range todo {
		newRef[m.next.ID] = m.next
	}
	for _, m := range todo {
		if v, ok := target[m.next.ID]; !ok || v != m.next.Version || m.join {
			continue // deleted, or joined with the links it has
		}
		if m.keep {
			// the links of what the target holds
			links, err := tx.OutLinks(ctx, m.ours.Ref())
			if err != nil {
				return res, err
			}
			for i := range links {
				links[i].ID = domain.LinkID(g.newID())
			}
			p, err := g.planLinks(ctx, tx, domain.Node{ID: m.next.ID, Version: m.next.Version}, links, m.ours.Ref())
			if err != nil {
				return res, err
			}
			plans[m.next.ID] = p
			continue
		}
		theirs, err := tx.OutLinks(ctx, m.from.Ref())
		if err != nil {
			return res, err
		}
		ours, anc := theirs, theirs // a node new on the target: take the links of the source
		if m.ours != nil {
			if ours, err = tx.OutLinks(ctx, m.ours.Ref()); err != nil {
				return res, err
			}
			anc = nil // created on both lines with no common version: the links of both
			if m.anc != nil {
				if anc, err = tx.OutLinks(ctx, m.anc.Ref()); err != nil {
					return res, err
				}
			}
		}
		var links []domain.Link
		for _, l := range MergeLinks(anc, ours, theirs) {
			if l.External() {
				links = append(links, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, Target: l.Target, Properties: l.Properties})
				continue
			}
			to := l.To
			if r, ok := newRef[to.ID]; ok {
				if v, live := target[r.ID]; !live || v != r.Version {
					continue // the target was deleted
				}
				to = r
			} else if _, ok := target[to.ID]; !ok {
				continue
			}
			links = append(links, domain.Link{ID: domain.LinkID(g.newID()), Type: l.Type, To: to, Properties: l.Properties})
		}
		like := []domain.NodeRef{m.from.Ref()}
		if m.ours != nil {
			like = []domain.NodeRef{m.ours.Ref(), m.from.Ref()}
		}
		p, err := g.planLinks(ctx, tx, domain.Node{ID: m.next.ID, Version: m.next.Version}, links, like...)
		if err != nil {
			return res, err
		}
		plans[m.next.ID] = p
	}
	for i, events := range phases[:3] {
		if err := g.emit(ctx, tx, events...); err != nil {
			return res, err
		}
		if i == 0 && len(entries) > 0 {
			// the merge accepts what it commits in one review, once its impacts are declared (ADR 0105)
			if err := g.putReviewRecord(ctx, tx, c.ID, "graph.merge", entries); err != nil {
				return res, err
			}
		}
	}
	// the entry of the commit of the merge, its position reserved first: its versions name it, the commits the target
	// takes are recorded by it
	var seq int64
	if len(phases[3]) > 0 {
		var err error
		if seq, err = tx.ReserveLogSeq(ctx, c.ID); err != nil {
			return res, err
		}
		for _, n := range writes {
			n.LogSeq = seq
			if err := tx.PutNode(ctx, n, plans[n.ID].set); err != nil {
				return res, err
			}
			if err := g.writeLinks(ctx, tx, n, plans[n.ID]); err != nil {
				return res, err
			}
		}
		if err := g.emitAt(ctx, tx, seq, phases[3]...); err != nil {
			return res, err
		}
	}

	if len(stuck) > 0 {
		return res, invalidf("the change leaves nodes in a state that cannot commit, move them to a committable state first: %s", strings.Join(stuck, ", "))
	}
	// the target takes the commits of the source (ADR 0176): the versions joined as they are, the others outrun by the
	// merge's own versions, written at the same order
	if err := takeCommits(ctx, tx, namespace, plan.Into, in.From, c.ID, seq, joined); err != nil {
		return res, err
	}
	res.Commit = domain.Commit{ID: res.Commit.ID, Name: title, Namespace: namespace, Branch: plan.Into, ParentID: base.ID, MergedFrom: fb.Head, ChangeID: c.ID, Kind: domain.CommitKindMerge, Nodes: target, CreatedAt: g.now()}
	if err := tx.PutCommit(ctx, res.Commit); err != nil {
		return res, err
	}
	if err := g.advanceBranch(ctx, tx, namespace, plan.Into, res.Commit); err != nil {
		return res, err
	}
	c.Status, c.LandedCommit = domain.ChangeApplied, res.Commit.ID
	if err := tx.PutChange(ctx, c); err != nil {
		return res, err
	}
	g.raiseMerged(ctx, tx, c, plan.Into, &res.Commit, res.merged)
	g.raiseApplied(ctx, tx, c, res.Commit)
	res.Change = c
	fb.Status = domain.BranchMerged
	return res, tx.PutBranch(ctx, fb)
}
