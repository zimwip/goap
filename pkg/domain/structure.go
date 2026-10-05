package domain

import "slices"

// Structures (ADR 0054): the two hierarchies every node version is placed in. A node version is owned by an
// organisational unit (WHO is responsible for it) and its node was created in a project (WHERE the work happens).
// Both hierarchies are node types a domain tags (`structure:` on a node type, pkg/domain/def), so the graph knows
// them from its type catalogue and never from hard-coded names; the built-in organisation domain tags OrgUnit and
// ProjectUnit.
const (
	// StructureOrganisation tags the node type of the organisational units: the owners of node versions and of
	// changes.
	StructureOrganisation = "organisation"
	// StructureProject tags the node type of the projects: where a node is created and a change acts.
	StructureProject = "project"
	// PropDefaultProject flags the default project (a boolean property of a project node): the project a change that
	// names none acts in. An administrator moves it; with none flagged the root project is the default.
	PropDefaultProject = "default"
)

// StructureKinds lists the structure kinds, in bootstrap order.
var StructureKinds = []string{StructureOrganisation, StructureProject}

// Structure is a hierarchy the graph places every node version in: the node type that builds it (and its subtypes),
// the link type from a child to its parent, and the key of its root, created by the bootstrap of the graph.
type Structure struct {
	Kind string `json:"kind"`
	// Type is the qualified node type of the hierarchy; its subtypes belong to it too.
	Type string `json:"type"`
	// Namespace is the namespace of Type: where the nodes of the hierarchy live.
	Namespace string `json:"namespace"`
	// Parent is the qualified link type from a child to its parent. The root links to nothing (organisation) or to
	// itself (project).
	Parent string `json:"parent"`
	// Root is the key of the root node, created by the bootstrap.
	Root string `json:"root"`
	// SelfParent: the root is its own parent (a project root, ADR 0039), rather than rootless.
	SelfParent bool `json:"selfParent,omitempty"`
}

// The structures of the built-in organisation domain (domains/builtin/organisation.yaml): the one place their names
// are written in Go; the services reading the organisation (pkg/access, pkg/mcp) name them through these.
const (
	NamespaceOrganisation = "organisation"
	TypeOrgUnit           = "organisation@OrgUnit"
	TypeProjectUnit       = "organisation@ProjectUnit"
	LinkPartOf            = "organisation@part_of"
	LinkProjectPartOf     = "organisation@project_part_of"
)

// BuiltinStructures are the structures of the built-in organisation domain: what an untyped graph (tests, tools)
// uses; the type catalogue resolves the same ones, since no other domain may tag a structure again.
var BuiltinStructures = map[string]Structure{
	StructureOrganisation: {Kind: StructureOrganisation, Type: TypeOrgUnit, Namespace: NamespaceOrganisation, Parent: LinkPartOf, Root: DefaultOrg},
	StructureProject:      {Kind: StructureProject, Type: TypeProjectUnit, Namespace: NamespaceOrganisation, Parent: LinkProjectPartOf, Root: DefaultProject, SelfParent: true},
}

// Structures are the two hierarchies in force with the node types belonging to each (the tagged type and its
// subtypes): what the services reading the organisation (pkg/access, internal/mcpsvc) learn from the graph service
// (GetStructures), so that they never name the types themselves.
type Structures struct {
	Organisation, Project Structure
	// OrganisationTypes and ProjectTypes are the tagged type of each structure and its subtypes.
	OrganisationTypes, ProjectTypes []string
}

// Of returns the structure of a kind.
func (s Structures) Of(kind string) Structure {
	if kind == StructureProject {
		return s.Project
	}
	return s.Organisation
}

// In reports whether a node type belongs to the structure of a kind (its tagged type or a subtype).
func (s Structures) In(kind, typ string) bool {
	types := s.OrganisationTypes
	if kind == StructureProject {
		types = s.ProjectTypes
	}
	return slices.Contains(types, typ)
}

// BuiltinStructureSet is the structures of the built-in organisation domain, with its subtypes (a User is a unit).
func BuiltinStructureSet() Structures {
	return Structures{Organisation: BuiltinStructures[StructureOrganisation], Project: BuiltinStructures[StructureProject],
		OrganisationTypes: []string{TypeOrgUnit, NamespaceOrganisation + TypeSep + "User"}, ProjectTypes: []string{TypeProjectUnit}}
}
