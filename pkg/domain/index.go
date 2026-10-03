package domain

import "time"

// Subjects of the events the graph publishes for the node index (ADR 0026).
const (
	// SubjectNodeWritten is "goap.node.<namespace>.<type>.<id>.written".
	SubjectNodeWritten = "goap.node.%s.%s.%s.written"
	// SubjectBaselineAdvanced is "goap.baseline.<branch>.advanced".
	SubjectBaselineAdvanced = "goap.baseline.%s.advanced"
	// SubjectChangeTouched is "goap.changed.<id>": the header, the impacts or the log of a change were written (a
	// domain.ChangeEvent of type "change.updated", the header only when it was the header). Not under goap.change.>,
	// which the trigger manager follows for the lifecycle of changes.
	SubjectChangeTouched = "goap.changed.%s"
)

// SearchProperty declares how the node index uses a property of a node type.
type SearchProperty struct {
	Property string `json:"property"`
	Text     bool   `json:"text,omitempty"`
	Facet    bool   `json:"facet,omitempty"`
}

// NodeEvent is published when a node version is written (on any branch), after the commit.
// Text and Facets are the searchable properties of the node type, already resolved.
type NodeEvent struct {
	ID        NodeID            `json:"id"`
	Version   Version           `json:"version"`
	Branch    string            `json:"branch"`
	Namespace string            `json:"namespace"`
	Key       string            `json:"key"`
	Type      string            `json:"type"`
	State     string            `json:"state,omitempty"`
	Deleted   bool              `json:"deleted,omitempty"`
	ChangeID  ChangeID          `json:"changeId,omitempty"`
	Text      map[string]string `json:"text,omitempty"`
	Facets    map[string]any    `json:"facets,omitempty"`
	Time      time.Time         `json:"time"`
}

// BaselineEvent is published when a baseline is created: the diff with its parent
// tells which node versions became the head of the branch.
type BaselineEvent struct {
	ID      BaselineID         `json:"id"`
	Branch  string             `json:"branch"`
	Parent  BaselineID         `json:"parent,omitempty"`
	Set     map[NodeID]Version `json:"set,omitempty"`
	Removed []NodeID           `json:"removed,omitempty"`
	Time    time.Time          `json:"time"`
}
