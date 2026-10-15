package graphapi

import (
	"slices"

	"github.com/zimwip/goap/pkg/domain"
)

// ChangeDependencies are the dependencies of a change around it (ADR 0108).
type ChangeDependencies struct {
	// DependsOn are the changes it depends on, as it sees them.
	DependsOn []domain.Dependency
	// Dependents are the open changes depending on it, each as that change sees it.
	Dependents []domain.Dependency
	// Order is the order in which its open sub-changes merge into it (domain.DependencyOrder), each with whether its
	// own dependencies are satisfied.
	Order []domain.Dependency
}

// NewChange describes a change to open.
type NewChange struct {
	Title       string
	Intent      string
	Methodology string
	// MethodologyVersion is the version of the methodology the change follows (ADR 0113). Empty: a sub-change of its
	// parent's methodology takes its parent's version, any other change the latest published one
	// (ChangeLifecycles.MethodologyVersion).
	MethodologyVersion string
	// Goal the change works towards (the goal of a methodology or a process of it). Empty: the main goal of the
	// methodology (ChangeLifecycles.DefaultGoal, ADR 0096), a sub-change's parent goal.
	Goal string
	// Namespace the change acts on (default: domain.DefaultNamespace).
	Namespace  string
	BaselineID domain.BaselineID
	// Branch the change applies to (default main); it must be open. With
	// OwnBranch it is the branch the change is finally merged into.
	Branch string
	// OwnBranch gives the change a branch of its own, named after it and forked
	// from BaselineID: its versions live there until the change is merged.
	OwnBranch bool
	// BranchIntent says why the own branch exists relative to its parent (derive/revise/refine, same vocabulary
	// as an Option's); only meaningful with OwnBranch or ParentID. "" (the default) is domain.IntentDerive.
	BranchIntent domain.OptionIntent
	// ParentID makes the change a sub-change of another one (see prepareSubChange).
	ParentID domain.ChangeID
	// OwnerOrg is the key of the unit responsible for the change (empty: the default organisation).
	OwnerOrg string
	// ProjectID is the key of the project this change acts in and its new nodes belong to (ADR 0039, 0091). A
	// change cannot be created without one: a sub-change inherits it from its parent when unset (and must stay
	// within the parent's project when set), any other change names it, and an empty one is refused with
	// ErrInvalid. The edges resolve a caller's active project (empty: the root project), the graph does not.
	ProjectID string
	Data      map[string]any
}

// ChangesFilter narrows ListChanges; the zero value matches every change.
type ChangesFilter struct {
	Namespace string
	OwnerOrg  string
	// Status: draft, active, committed, applied, abandoned (empty: every status).
	Status []domain.ChangeStatus
}

// ChangePatch updates mutable fields of a change header. Nil fields are kept. Title and Intent are
// the current definition of the change; goap-change/reformulate is the only caller that sets them,
// after superseding the previous definition as an "intent" item so the history is kept.
type ChangePatch struct {
	Title  *string
	Intent *string
	Goal   *string
	Status *domain.ChangeStatus
	Data   map[string]any // merged
}

// Match reports whether a change satisfies the filter.
func (f ChangesFilter) Match(c domain.Change) bool {
	if f.Namespace != "" && c.Namespace != f.Namespace {
		return false
	}
	if f.OwnerOrg != "" && c.OwnerOrg != f.OwnerOrg {
		return false
	}
	if len(f.Status) > 0 && !slices.Contains(f.Status, c.Status) {
		return false
	}
	return true
}
