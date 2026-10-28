package graphapi

import "github.com/zimwip/goap/contracts/domain"

// QueryOptions bound a query and say what a caller may read.
type QueryOptions struct {
	// MaxRows, MaxReads, MaxAnchors: zero takes the default cap.
	MaxRows, MaxReads, MaxAnchors int
	// Bind binds variables to nodes before the match (the node a person queries from, `n`): a node the view does not
	// hold is ErrNotFound.
	Bind map[string]domain.NodeID
	// Visible tells whether the caller reads a node (nil: every node): a node it may not read is not in the graph the
	// query sees (no pattern, EXISTS or OPTIONAL MATCH meets it). It is asked outside the read's transaction (an access
	// check reads the graph, and nothing in a graph transaction may wait on such a read): the query runs once, the
	// nodes it met are asked, and it runs again in the same commit when one of them is hidden.
	Visible func(domain.Node) bool
}

// QueryResult is what a query answered.
type QueryResult struct {
	Columns []string
	// Rows hold JSON values; a node, a relationship and a path are objects naming them ({"$node": id},
	// {"$link": id}, {"$path": {"nodes": [ids], "links": [ids]}}), the nodes and links themselves in Nodes and Links.
	Rows  [][]any
	Nodes []domain.Node
	Links []domain.Link
	// Truncated is set when a cap stopped the match: the rows are those found until then; Notes say which cap, and
	// what the query was read as (an unbounded `*`).
	Truncated bool
	Notes     []string
	// Reads is the number of reads the query made.
	Reads int
	// Commit is the commit the view read (a change view: its fork).
	Commit domain.CommitID
}
