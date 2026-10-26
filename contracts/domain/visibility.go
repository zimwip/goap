package domain

import (
	"fmt"

	"github.com/zimwip/goap/contracts/lnk"
)

// How far a committed version is seen (ADR 0161 §4): declared by the domain on a lifecycle state, else on the node
// type (inherited through extends), stamped on the version at commit (Node.Visibility).
const (
	// VisibilityPersonal: its creator alone, inside the owning project.
	VisibilityPersonal = "personal"
	// VisibilityPrivate: the members of its responsible unit and of the units below it, inside the owning project.
	VisibilityPrivate = "private"
	// VisibilityProject (the default): everyone who works on the owning project or a project above it.
	VisibilityProject = "project"
	// VisibilityTree: everyone who works on a project of the owning project's parent tree, siblings included.
	VisibilityTree = "tree"
	// VisibilityPublic: every identified person.
	VisibilityPublic = "public"
)

// Visibilities are the scopes, narrowest first.
var Visibilities = []string{VisibilityPersonal, VisibilityPrivate, VisibilityProject, VisibilityTree, VisibilityPublic}

// VisibilityOf is the scope a stamped value says: empty is the default, project.
func VisibilityOf(v string) string {
	if v == "" {
		return VisibilityProject
	}
	return v
}

// CheckVisibility refuses a value that names no scope (empty: not declared).
func CheckVisibility(v string) error {
	if v == "" {
		return nil
	}
	for _, s := range Visibilities {
		if s == v {
			return nil
		}
	}
	return fmt.Errorf("visibility %q: one of %v", v, Visibilities)
}

// Creator is who created a node (ADR 0161 §3): the subject and its unit at creation, the same for every version.
type Creator struct {
	// Subject is the person who created it, the end user when an agent created it for them.
	Subject string `json:"subject,omitempty"`
	// Unit is the unit the node was created in: the unit holding the change that created it (a logical URL, as
	// Node.Owner).
	Unit lnk.URL `json:"unit,omitzero"`
}

// IsZero reports no creator recorded.
func (c Creator) IsZero() bool { return c.Subject == "" && c.Unit.IsZero() }
