package graphapi

import "github.com/zimwip/goap/contracts/domain"

// NodeCreate is a node ImpactNodeCreate creates.
type NodeCreate struct {
	Key, Type  string
	Properties map[string]any
	// Owner is the key of the organisational unit owning the node (ADR 0054); empty: the unit holding the change.
	Owner     string
	Rationale string
	// Links are outgoing links of the draft.
	Links []LinkWrite
	// Flow is the flow the call works on ("": the active option, else the main flow; domain.MainFlow names the main
	// flow) and Execution the action run that makes it (ADR 0025).
	Flow, Execution string
	ProducedBy      string
	DerivedFrom     []domain.ItemID
}

// NodeCheckout names the node ImpactNodeCheckout or ImpactNodeTransition works on: a change impact, or a node (then the impact the
// flow sees on it, declared when the change holds none).
type NodeCheckout struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	// Key names the node by its key, when neither Impact nor Node is given.
	Key string
	// Rationale says why, when the call declares the impact.
	Rationale       string
	Flow, Execution string
	ProducedBy      string
}

// NodeTransition moves a node along its lifecycle (ImpactNodeTransition): To is the state it goes to.
type NodeTransition struct {
	NodeCheckout
	To string
}

// NodeUpdate edits a draft.
type NodeUpdate struct {
	// Node and Key name the node when ImpactNodeUpdate is given no change impact (see resolve).
	Node domain.NodeID
	Key  string
	// Properties are merged over the ones of the draft.
	Properties map[string]any
	// Owner transfers the node to another organisational unit (its key, ADR 0054).
	Owner           string
	Flow, Execution string
}

// LinkWrite is an outgoing link of a draft: to an exact node version, or, with Version 0, to the draft of a node the
// change holds (resolved to the version landing writes for it).
type LinkWrite struct {
	Type       string
	To         domain.NodeRef
	Properties map[string]any
}

// NodeName names a node a merge or a split works on: a change impact, or a node (its id, or its key).
type NodeName struct {
	Impact domain.ChangeImpactID
	Node   domain.NodeID
	Key    string
}

// MergeInput is a merge (ImpactNodeMerge): Sources are replaced, in their parents, by the new node Into.
type MergeInput struct {
	Sources []NodeName
	Into    NodeCreate
	// Rationale says why, on the impacts the merge declares (the sources, the parents); empty: the one of Into.
	Rationale string
	// Flow and Execution replace the ones of Into (see NodeCreate).
	Flow, Execution string
	// Gate, when set, is asked about the type of every node the call writes or modifies, the parents it discovers
	// included (the access nodes, ADR 0068, are not the caller's to change): its error refuses the call. It runs inside
	// the transaction and must not read the graph.
	Gate func(nodeType string) error
}

// SplitInput is a split (ImpactNodeSplit): Source is replaced, in its parents, by the new nodes Into.
type SplitInput struct {
	Source          NodeName
	Into            []NodeCreate
	Rationale       string
	Flow, Execution string
	// Gate is the one of MergeInput.
	Gate func(nodeType string) error
}

// SuspectLink is a link of another node to a source that a split leaves as it is: which successor it should follow
// is for the reviewer to say (ADR 0003: it becomes suspect).
type SuspectLink struct {
	From    domain.NodeRef
	FromKey string
	Type    string
	To      domain.NodeRef
	ToKey   string
}

// Restructured is what a merge or a split did to the change.
type Restructured struct {
	// Successors are the impacts of the new nodes (created, checked out, with their origins).
	Successors []domain.ChangeImpact
	// Sources are the impacts of the merged or split nodes, Via the impact of their parent.
	Sources []domain.ChangeImpact
	// Parents are the impacts of the nodes whose links moved (each checked out): the parents, and for a merge the nodes
	// holding other links to the sources.
	Parents []domain.ChangeImpact
	// Suspect lists the links a split leaves to its source (see SuspectLink).
	Suspect []SuspectLink
}
