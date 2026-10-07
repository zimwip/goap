package graph

import "github.com/zimwip/goap/pkg/domain"

// The names of the organisation domain the tests use (domains/builtin/organisation.yaml): the graph core names none of
// them, the fixtures do.
const (
	NamespaceOrganisation = "organisation"
	NodeTypeOrgUnit       = "organisation@OrgUnit"
	NodeTypeProjectUnit   = "organisation@ProjectUnit"
	NodeTypeUser          = "organisation@User"
	LinkMemberOf          = "organisation@member_of"
	LinkPartOf            = "organisation@part_of"
	LinkProjectPartOf     = "organisation@project_part_of"
)

// rootOrg and rootProject are the keys of the roots the bootstrap creates, as the structures in force name them.
func rootOrg(g *Graph) string { return g.Structure(domain.StructureOrganisation).Root }

func rootProject(g *Graph) string { return g.Structure(domain.StructureProject).Root }
