package graphapi

import (
	"slices"

	"github.com/zimwip/goap/contracts/domain"
	"github.com/zimwip/goap/contracts/lnk"
)

// Browsing a view without shipping it whole: list the node types, page through the nodes of one type with a text
// filter, and show the neighbourhood of a selected node (ADR 0137, 0157).

// MaxBrowseLimit caps the page size of BaselineNodes.
const MaxBrowseLimit = 500

// NodeQuery selects a page of the nodes of a baseline.
type NodeQuery struct {
	// Type keeps the nodes of this qualified type (empty: every type).
	Type string
	// Text keeps the nodes whose key, type or a string property contains it (case-insensitive).
	Text string
	// IncludeDeleted keeps the tombstones.
	IncludeDeleted bool
	// Change keeps the nodes the impacts of that change act on, at their version in the baseline: they are read node
	// by node, so the answer and its cost follow the change, not the size of the baseline.
	Change domain.ChangeID
	// Reach keeps the versions the caller reads (nil: every version, ADR 0161 §5), set by the graph's view reader,
	// never by a caller.
	Reach         *ReachFilter
	Offset, Limit int
}

// TypeCount is the number of nodes of a type in a baseline.
type TypeCount struct {
	Type  string
	Count int
}

// NodePage is a page of the nodes of a baseline.
type NodePage struct {
	Nodes []domain.Node
	// Total is the number of nodes matching the query (all pages).
	Total int
	// Types counts the nodes of each type matching the text filter (the type filter aside), sorted by type.
	Types    []TypeCount // Baseline is the baseline the page was read at (a change view: its fork).
	Baseline domain.BaselineID
}

// Neighbourhood is a node of a baseline and the nodes it is linked to.
type Neighbourhood struct {
	Node domain.Node
	// Nodes are the neighbours: the targets of its outgoing links (at the baseline's version, or the linked one for a
	// node the baseline does not hold, such as a node of another namespace) and the sources of its incoming links
	// held by the baseline.
	Nodes []domain.Node
	// Links are the links between Node and its neighbours.
	Links []domain.Link
	// Suspect are the links whose target changed since they were created (the baseline holds another version).
	Suspect []domain.LinkID
}

// ReachFilter is the reach of a caller as the stores filter the versions they read (ADR 0161 §5): the keys as the
// nodes store them (logical URLs and bare keys), set by the graph's view reader, never by a caller. A version is held
// by its scope (Node.Visibility): public by every identified person, tree by the projects of Trees, project by those of
// Projects, private by those of Projects when its responsible unit is one of Units, personal by those of Projects when
// its creator is Subject.
type ReachFilter struct {
	Projects []string
	Trees    []string
	Units    []string
	Subject  string
}

// Holds reports whether the reach holds a version (nil: every version).
func (f *ReachFilter) Holds(n domain.Node) bool {
	if f == nil {
		return true
	}
	in := func(list []string, u lnk.URL) bool {
		return !u.IsZero() && (slices.Contains(list, u.String()) || slices.Contains(list, u.Key))
	}
	switch domain.VisibilityOf(n.Visibility) {
	case domain.VisibilityPublic:
		return true
	case domain.VisibilityTree:
		return in(f.Trees, n.Project) || in(f.Projects, n.Project)
	case domain.VisibilityPrivate:
		return in(f.Projects, n.Project) && in(f.Units, n.Owner)
	case domain.VisibilityPersonal:
		return in(f.Projects, n.Project) && f.Subject != "" && n.Creator.Subject == f.Subject
	default:
		return in(f.Projects, n.Project)
	}
}
