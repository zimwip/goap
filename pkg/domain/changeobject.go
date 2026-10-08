package domain

import (
	"encoding/json"
	"maps"
	"strings"
	"time"
)

// LogObject is the stream of the change objects in the log of a change (ADR 0098): object.<ns>@<ChangeObjectType>, one
// entry per version of a change object, its payload the whole ChangeObject. Written by the graph only.
const LogObject = "object"

// ChangeObject is a version of a change object (ADR 0098): what a change carries beyond its impacts, typed by a change
// object type of a domain. A change object is identified in its change by its type, its key and, for a type scoped to
// the workspace, its workspace; every write is a new version holding the whole value, the last one being the change object.
type ChangeObject struct {
	Change ChangeID `json:"change"`
	// Type is the qualified change object type ("risks@Risk").
	Type string `json:"type"`
	// Key identifies the change object among those of its type (its key type says how it is made).
	Key string `json:"key"`
	// Workspace is the workspace (flow) a workspace-scoped change object belongs to; empty for a change-scoped one and
	// for the main workspace.
	Workspace string `json:"workspace,omitempty"`
	// Version counts the writes of the change object, from 1.
	Version int `json:"version"`
	// Seq is the position of the version in the log of the change.
	Seq int64 `json:"seq,omitempty"`
	// State is the state of the change object in the lifecycle of its type; empty when the type has none.
	State string `json:"state,omitempty"`
	// Value is the whole value of the version, checked against the attributes of the type.
	Value map[string]any `json:"value,omitempty"`
	// Labels are opaque labels of the write (process, execution, step...), indexed in the log.
	Labels map[string]string `json:"labels,omitempty"`
	By     string            `json:"by,omitempty"`
	At     time.Time         `json:"at"`
}

// ID names the change object in its change: "<type>/<key>", and "@<workspace>" for a workspace-scoped one off the
// main workspace.
func (o ChangeObject) ID() string {
	id := o.Type + "/" + o.Key
	if o.Workspace != "" {
		id += "@" + o.Workspace
	}
	return id
}

// ObjectWrite writes a change object: a new one, or a new version of the one of the same type, key and workspace.
type ObjectWrite struct {
	// Type is the qualified change object type.
	Type string `json:"type"`
	// Key of the change object; empty for a singleton, for a natural key (made from the value) and for the first
	// write of a sequence key (allocated).
	Key string `json:"key,omitempty"`
	// Value is the value written: the whole value, or merged into the last one when Merge is set (a nil value of a
	// merge removes the property).
	Value map[string]any `json:"value,omitempty"`
	Merge bool           `json:"merge,omitempty"`
	// Transition names a transition of the lifecycle of the type, out of the current state; the first write of a
	// change object puts it in the initial state.
	Transition string `json:"transition,omitempty"`
	// Workspace is the workspace (flow) of a workspace-scoped type; empty: the active option of the change, else the
	// main workspace. Ignored for a change-scoped type.
	Workspace string `json:"workspace,omitempty"`
	// Labels are opaque labels recorded with the write.
	Labels map[string]string `json:"labels,omitempty"`
}

// ObjectFilter selects the change objects of a change.
type ObjectFilter struct {
	// Types are qualified change object types; empty: every type.
	Types []string
	// KeyPrefix keeps the keys starting with it.
	KeyPrefix string
	// Workspaces keeps the change objects of these workspaces ("" the main one and the change-scoped ones); nil: every
	// workspace.
	Workspaces []string
	// Labels keeps the change objects whose last write carries these labels.
	Labels map[string]string
	// AtSeq reads the change objects as they were at that position of the log; 0: the last versions.
	AtSeq int64
}

// Match tells whether a change object is selected (AtSeq aside).
func (f ObjectFilter) Match(o ChangeObject) bool {
	if len(f.Types) > 0 && !containsString(f.Types, o.Type) {
		return false
	}
	if f.KeyPrefix != "" && !strings.HasPrefix(o.Key, f.KeyPrefix) {
		return false
	}
	if f.Workspaces != nil && !containsString(f.Workspaces, o.Workspace) {
		return false
	}
	return LabelsMatch(o.Labels, f.Labels)
}

// LabelsMatch tells whether labels hold every label of want.
func LabelsMatch(labels, want map[string]string) bool {
	for k, v := range want {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func containsString(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// ObjectEntry is the log entry of a version of a change object: type object.<type>, its workspace as flow, its key as
// subject.
func ObjectEntry(id string, o ChangeObject) (LogEntry, error) {
	raw, err := json.Marshal(o)
	return LogEntry{ID: id, Change: o.Change, Type: LogObject + "." + o.Type, Flow: o.Workspace, Subject: o.Key, By: o.By, At: o.At,
		Labels: maps.Clone(o.Labels), Payload: raw}, err
}

// FoldObjects replays the object entries of a log (in order) into the last version of each change object, in the order
// they were first written.
func FoldObjects(entries []LogEntry) ([]ChangeObject, error) {
	var order []string
	last := map[string]ChangeObject{}
	for _, e := range entries {
		if e.Stream() != LogObject {
			continue
		}
		var o ChangeObject
		if err := json.Unmarshal(e.Payload, &o); err != nil {
			return nil, err
		}
		o.Seq = e.Seq
		if _, ok := last[o.ID()]; !ok {
			order = append(order, o.ID())
		}
		last[o.ID()] = o
	}
	out := make([]ChangeObject, 0, len(order))
	for _, id := range order {
		out = append(out, last[id])
	}
	return out, nil
}
