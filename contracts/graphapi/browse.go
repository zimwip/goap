package graphapi

import "github.com/zimwip/goap/contracts/domain"

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
	Change        domain.ChangeID
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
