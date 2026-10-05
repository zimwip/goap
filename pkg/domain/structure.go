package domain

import "slices"

// Structures (ADR 0054): the two hierarchies every node version is placed in. A node version is owned by an
// organisational unit (WHO is responsible for it) and its node was created in a project (WHERE the work happens).
// Both hierarchies are node types a domain tags (`structure:` on a node type, pkg/domain/def), so the graph knows
// them, their parent link and their root from its type catalogue (pkg/typecat) and never from hard-coded names.
const (
	// StructureOrganisation names the axis of the organisational units: the owners of node versions and of changes.
	StructureOrganisation = "organisation"
	// StructureProject names the axis of the projects: where a node is created and a change acts.
	StructureProject = "project"
)

// Structure is a hierarchy the graph places every node version in: the node type that builds it (and its subtypes),
// the link type from a child to its parent, and the key of its root, created by the bootstrap of the graph.
type Structure struct {
	Kind string `json:"kind"`
	// Type is the qualified node type of the hierarchy; its subtypes belong to it too.
	Type string `json:"type"`
	// Namespace is the namespace of Type: where the nodes of the hierarchy live.
	Namespace string `json:"namespace"`
	// Parent is the qualified link type from a child to its parent. The root links to nothing or to itself
	// (SelfParent).
	Parent string `json:"parent"`
	// Root is the key of the root node, created by the bootstrap.
	Root string `json:"root"`
	// SelfParent: the root is its own parent, rather than rootless.
	SelfParent bool `json:"selfParent,omitempty"`
	// Default names the boolean property that flags the default member of the hierarchy (the one a change naming
	// none resolves to; the smallest key when several are flagged, the root when none is); empty: the root is.
	Default string `json:"default,omitempty"`
	// Bootstrap are the initial properties of the root node.
	Bootstrap map[string]any `json:"bootstrap,omitempty"`
}

// StructureSet is a structure with the node types belonging to it (the tagged type and its subtypes, sorted).
type StructureSet struct {
	Structure
	Types []string `json:"types,omitempty"`
}

// RequiredLink is a link a node of a type must carry (`requires:` on a node type, ADR 0065): exactly Count outgoing
// links of type Link (the qualified link type once resolved by the type catalogue; a bare name of the domain in its
// definition). It is checked when a node is created and after each write, as the parent of a structure is (ADR 0054).
type RequiredLink struct {
	Link string `yaml:"link" json:"link"`
	// Count is the number of links of the type the node must have; 0 in a definition means 1.
	Count int `yaml:"count,omitempty" json:"count,omitempty"`
}

// Exactly is the number of links required: Count, 1 when unset.
func (r RequiredLink) Exactly() int {
	if r.Count <= 0 {
		return 1
	}
	return r.Count
}

// Structures are the structures in force, one per kind, with the node types belonging to each: what the services
// reading the organisation (pkg/access, internal/mcpsvc) learn from the graph service (GetStructures), so that they
// never name the types themselves.
type Structures []StructureSet

// Of returns the structure of a kind (zero when none is declared).
func (s Structures) Of(kind string) Structure {
	for _, x := range s {
		if x.Kind == kind {
			return x.Structure
		}
	}
	return Structure{}
}

// Organisation is the structure of the organisational units, Project the structure of the projects.
func (s Structures) Organisation() Structure { return s.Of(StructureOrganisation) }
func (s Structures) Project() Structure      { return s.Of(StructureProject) }

// TypesOf returns the node types belonging to the structure of a kind.
func (s Structures) TypesOf(kind string) []string {
	for _, x := range s {
		if x.Kind == kind {
			return x.Types
		}
	}
	return nil
}

// In reports whether a node type belongs to the structure of a kind (its tagged type or a subtype).
func (s Structures) In(kind, typ string) bool { return slices.Contains(s.TypesOf(kind), typ) }
