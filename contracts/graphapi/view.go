package graphapi

import (
	"context"
	"errors"
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// View is what a read of the current graph is scoped to (ADR 0157): a lasting branch at one of its baselines, or a
// change on a flow (its pending work over the branch it forked from). Only the reads of the history (a node's versions,
// a diff, a change-stack) name versions instead.
type View struct {
	// Namespace and Branch name a lasting branch, both required: no branch is assumed; Baseline one of its baselines
	// (empty: its head when the read starts).
	Namespace string
	Branch    string
	Baseline  domain.BaselineID
	// Change and Flow name a change on a flow ("main" or an option id; empty: the change's active option). A change
	// view takes no branch: it is the change's.
	Change domain.ChangeID
	Flow   string
}

// ErrNoView refuses a read of the current graph that names no view.
var ErrNoView = fmt.Errorf("no view: name a branch (its namespace and its name) and optionally a baseline, or a change and its option: %w", ErrInvalid)

// IsChange tells a change view.
func (v View) IsChange() bool { return v.Change != "" }

// Check refuses a view that names neither a change nor a branch (its namespace and its name), or both.
func (v View) Check() error {
	switch {
	case v.Change != "" && (v.Namespace != "" || v.Branch != "" || v.Baseline != ""):
		return fmt.Errorf("a view names a change or a branch, not both: %w", ErrInvalid)
	case v.Change == "" && (v.Namespace == "" || v.Branch == ""):
		return ErrNoView
	}
	return nil
}

// String is a short form of the view, for messages.
func (v View) String() string {
	if v.IsChange() {
		if v.Flow != "" {
			return fmt.Sprintf("change %s on %s", v.Change, v.Flow)
		}
		return fmt.Sprintf("change %s", v.Change)
	}
	if v.Baseline != "" {
		return fmt.Sprintf("%s/%s at %s", v.Namespace, v.Branch, v.Baseline)
	}
	return fmt.Sprintf("%s/%s", v.Namespace, v.Branch)
}

// ViewState is what a view holds of some nodes (Graph.ViewVersions): its namespace, the baseline it reads (a change
// view: its fork), whether it is the head of main, and the version of each node asked that it holds (0: a change's
// draft; absent: not in it).
type ViewState struct {
	Namespace string
	Baseline  domain.BaselineID
	// MainHead: a branch view of main at its head, the versions the index flags as heads of main.
	MainHead bool
	Versions map[domain.NodeID]domain.Version
}

// ForkNode reads a node by key as a change was forked from it (ADR 0157), from the blackboard of the change on a flow:
// the version an impact of the change checked out (hydrated by the blackboard), none for a node the change creates,
// else the version the change's view holds of a node it does not act on, read by key. found is false for a node the
// fork does not hold.
func ForkNode(ctx context.Context, g Port, bb domain.Blackboard, flow, key string) (n domain.Node, found bool, err error) {
	for _, cn := range bb.Change.Nodes {
		if cn.Key != key || cn.Superseded {
			continue
		}
		if cn.Pre == nil {
			return domain.Node{}, false, nil
		}
		if nv, ok := bb.Nodes[*cn.Pre]; ok {
			return nv.Node, !nv.Deleted, nil
		}
		return domain.Node{ID: cn.Pre.ID, Version: cn.Pre.Version, Key: key, Type: cn.Type}, true, nil
	}
	nv, err := g.ViewNode(ctx, View{Change: bb.Change.ID, Flow: flow}, "", key)
	if errors.Is(err, ErrNotFound) {
		return domain.Node{}, false, nil
	}
	if err != nil {
		return domain.Node{}, false, err
	}
	return nv.Node, !nv.Deleted, nil
}
