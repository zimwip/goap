package domain

import (
	"slices"
	"sort"
	"time"
)

// The change-stack (ADR 0133): a node version and a baseline are the application of the changes that led to them. The
// graph already records, for every version, the change that wrote it (Node.ChangeID) and the changes whose merge made
// it join other branches (node_branch), and for every baseline the change that left it, its parent and the head it
// merged; the stack is what these records compose into, read back as a lineage. A version's lineage is the DAG of its
// ancestors (Parents: two for a merge or a rebase); a baseline's is the DAG of the baselines it follows from (parent,
// merged head) and, for a baseline that took a change in (fast-forward or merge), the baselines of that change's own
// branch: its commits, its rebases, the sub-changes merged into it.

// VersionJoin is a version joining a branch it was not written on, by the merge of a change (ADR 0032).
type VersionJoin struct {
	Version Version  `json:"version"`
	Branch  string   `json:"branch"`
	Change  ChangeID `json:"change"`
}

// VersionStep is a version of the lineage of a node version.
type VersionStep struct {
	Version Version   `json:"version"`
	Branch  string    `json:"branch"`
	Reason  string    `json:"reason"`
	Parents []Version `json:"parents,omitempty"`
	// Change wrote the version.
	Change ChangeID `json:"change"`
	// Joined are the branches it joined and the changes whose merge made it join them.
	Joined    []VersionJoin `json:"joined,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
}

// BaselineStep is a baseline of the lineage of a baseline.
type BaselineStep struct {
	ID          BaselineID `json:"id"`
	Name        string     `json:"name,omitempty"`
	Branch      string     `json:"branch"`
	Kind        string     `json:"kind,omitempty"`
	Change      ChangeID   `json:"change"`
	Parent      BaselineID `json:"parent,omitempty"`
	MergedFrom  BaselineID `json:"mergedFrom,omitempty"`
	RebasedFrom BaselineID `json:"rebasedFrom,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
}

// StackChange is a change of a stack, as its readers name it.
type StackChange struct {
	ID ChangeID `json:"id"`
	// Parent is the change it is a sub-change of (ADR 0103).
	Parent ChangeID     `json:"parent,omitempty"`
	Title  string       `json:"title,omitempty"`
	Status ChangeStatus `json:"status,omitempty"`
	// OwnerOrg is the unit holding the change, for the edges deciding who may read its title (a personal change).
	OwnerOrg string `json:"ownerOrg,omitempty"`
}

// ChangeStack is the stack of changes producing a node version or a baseline: the changes, oldest first, and the
// lineage they are read from (Versions for a node version, Baselines for a baseline), oldest first. Truncated: the
// lineage was cut at the limit asked, its oldest part left out.
type ChangeStack struct {
	Changes   []StackChange  `json:"changes"`
	Versions  []VersionStep  `json:"versions,omitempty"`
	Baselines []BaselineStep `json:"baselines,omitempty"`
	Truncated bool           `json:"truncated,omitempty"`
}

// DefaultStackLimit is how many steps of a lineage a stack holds when none is asked.
const DefaultStackLimit = 500

// VersionLineage returns the lineage of version v of a node, oldest first: v and its ancestors through Parents, each
// with the joins of joins naming it, at most limit (the most recent kept; 0: DefaultStackLimit). versions are every
// version of the node.
func VersionLineage(versions []Node, joins []VersionJoin, v Version, limit int) (out []VersionStep, truncated bool) {
	if limit <= 0 {
		limit = DefaultStackLimit
	}
	byVersion := make(map[Version]Node, len(versions))
	for _, n := range versions {
		byVersion[n.Version] = n
	}
	joinsOf := map[Version][]VersionJoin{}
	for _, j := range joins {
		joinsOf[j.Version] = append(joinsOf[j.Version], j)
	}
	seen := map[Version]bool{}
	todo := []Version{v}
	for len(todo) > 0 {
		x := todo[0]
		todo = todo[1:]
		n, ok := byVersion[x]
		if seen[x] || !ok {
			continue
		}
		seen[x] = true
		js := slices.Clone(joinsOf[x])
		sort.Slice(js, func(i, j int) bool { return js[i].Branch < js[j].Branch })
		out = append(out, VersionStep{Version: x, Branch: BranchOf(n.Branch), Reason: n.Reason, Parents: slices.Clone(n.Parents),
			Change: n.ChangeID, Joined: js, CreatedAt: n.CreatedAt})
		todo = append(todo, n.Parents...)
	}
	// a parent has a lower number than its child (versions are numbered in the order they are written)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	if len(out) > limit {
		out, truncated = out[len(out)-limit:], true
	}
	return out, truncated
}

// BaselineLineage returns the lineage of baseline from, oldest first, read from the headers of the baselines of its
// namespace (their nodes are not needed): the baselines it follows from (parent, merged head) and, for one taking a
// change in, the baselines of that change's own branch (branchOf names it, "" for none) written before it: the
// change's commits and rebases, the sub-changes merged into it. At most limit (the most recent kept; 0:
// DefaultStackLimit).
func BaselineLineage(headers []Baseline, from BaselineID, branchOf func(ChangeID) string, limit int) (out []BaselineStep, truncated bool) {
	if limit <= 0 {
		limit = DefaultStackLimit
	}
	byID := make(map[BaselineID]Baseline, len(headers))
	onBranch := map[string][]Baseline{}
	for _, b := range headers {
		byID[b.ID] = b
		onBranch[BranchOf(b.Branch)] = append(onBranch[BranchOf(b.Branch)], b)
	}
	seen := map[BaselineID]bool{}
	todo := []BaselineID{from}
	for len(todo) > 0 {
		id := todo[0]
		todo = todo[1:]
		b, ok := byID[id]
		if id == "" || seen[id] || !ok {
			continue
		}
		seen[id] = true
		out = append(out, BaselineStep{ID: b.ID, Name: b.Name, Branch: BranchOf(b.Branch), Kind: b.Kind, Change: b.ChangeID, Parent: b.ParentID,
			MergedFrom: b.MergedFrom, RebasedFrom: b.RebasedFrom, CreatedAt: b.CreatedAt})
		todo = append(todo, b.ParentID, b.MergedFrom)
		if b.Kind == BaselineFastForward || b.Kind == BaselineMerge {
			// the change it took in worked on a branch of its own: its baselines there are the work it brought
			if own := branchOf(b.ChangeID); own != "" && BranchOf(own) != BranchOf(b.Branch) {
				for _, o := range onBranch[BranchOf(own)] {
					if !o.CreatedAt.After(b.CreatedAt) {
						todo = append(todo, o.ID)
					}
				}
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	if len(out) > limit {
		out, truncated = out[len(out)-limit:], true
	}
	return out, truncated
}

// StackChangeIDs returns the changes of a lineage, in the order they first appear (oldest first): for a version,
// the change that wrote it, then the changes whose merge made it join a branch.
func StackChangeIDs(versions []VersionStep, baselines []BaselineStep) []ChangeID {
	var out []ChangeID
	seen := map[ChangeID]bool{}
	add := func(c ChangeID) {
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, v := range versions {
		add(v.Change)
		for _, j := range v.Joined {
			add(j.Change)
		}
	}
	for _, b := range baselines {
		add(b.Change)
	}
	return out
}

// DiffStacks returns the changes of stack to that stack from does not hold (added) and the ones of from that to does
// not hold (removed), each in the order of its stack: the changes that explain the difference of two states.
func DiffStacks(from, to []ChangeID) (added, removed []ChangeID) {
	in := func(s []ChangeID) map[ChangeID]bool {
		m := make(map[ChangeID]bool, len(s))
		for _, c := range s {
			m[c] = true
		}
		return m
	}
	inFrom, inTo := in(from), in(to)
	for _, c := range to {
		if !inFrom[c] {
			added = append(added, c)
		}
	}
	for _, c := range from {
		if !inTo[c] {
			removed = append(removed, c)
		}
	}
	return added, removed
}
