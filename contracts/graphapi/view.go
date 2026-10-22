package graphapi

import (
	"fmt"

	"github.com/zimwip/goap/contracts/domain"
)

// View is what a read of the current graph is scoped to (ADR 0157): a lasting branch at one of its baselines, or a
// change on a flow (its pending work over the branch it forked from). Only the reads of the history (a node's versions,
// a diff, a change-stack) name versions instead.
type View struct {
	// Namespace and Branch name a lasting branch (Branch empty: main); Baseline one of its baselines (empty: its head
	// when the read starts).
	Namespace string
	Branch    string
	Baseline  domain.BaselineID
	// Change and Flow name a change on a flow ("main" or an option id; empty: the change's active option). A change
	// view takes no branch: it is the change's.
	Change domain.ChangeID
	Flow   string
}

// ErrNoView refuses a read of the current graph that names no view.
var ErrNoView = fmt.Errorf("no view: name a branch and a baseline, or a change and its option: %w", ErrInvalid)

// IsChange tells a change view.
func (v View) IsChange() bool { return v.Change != "" }

// Check refuses a view that names neither a change nor a branch (a namespace, or a baseline), or both.
func (v View) Check() error {
	switch {
	case v.Change != "" && (v.Namespace != "" || v.Branch != "" || v.Baseline != ""):
		return fmt.Errorf("a view names a change or a branch, not both: %w", ErrInvalid)
	case v.Change == "" && v.Namespace == "" && v.Baseline == "":
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
	b := v.Branch
	if b == "" {
		b = domain.MainBranch
	}
	if v.Baseline != "" {
		return fmt.Sprintf("%s/%s at %s", v.Namespace, b, v.Baseline)
	}
	return fmt.Sprintf("%s/%s", v.Namespace, b)
}
