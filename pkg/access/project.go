package access

import (
	"fmt"
)

// DefaultProject is the key of the root project the built-in organisation domain tags (`structure.root`, ADR 0039),
// created by the bootstrap of the graph; like DefaultOrg, for the seeds and tests of the built-in organisation.
const DefaultProject = "PROJ-ROOT"

// Types and links of the project graph objects (ADR 0039): a project mirrors OrgUnit's hierarchy, and
// Assignment is the meeting point of organisation and project (same shape as adapter.Instance, the meeting point
// of organisation, MCP and connector): the roles an org unit or user locally holds on a project.
const (
	NodeTypeProjectUnit = "organisation@ProjectUnit"
	NodeTypeAssignment  = "organisation@Assignment"
	LinkProjectPartOf   = "organisation@project_part_of"
	LinkAssignsOrg      = "organisation@assigns_org"
	LinkAssignsProject  = "organisation@assigns_project"
)

// ProjectUnit is a project or sub-project.
type ProjectUnit struct {
	Name          string
	Description   string
	Kind          string
	Status        string
	Methodologies []string
}

// Props returns the properties of the ProjectUnit node.
func (p ProjectUnit) Props() map[string]any {
	m := map[string]any{}
	for k, v := range map[string]string{"name": p.Name, "description": p.Description, "kind": p.Kind, "status": p.Status} {
		if v != "" {
			m[k] = v
		}
	}
	if len(p.Methodologies) > 0 {
		m["methodologies"] = toAnyList(p.Methodologies)
	}
	return m
}

// ProjectFromProps reads a project from the properties of its node.
func ProjectFromProps(props map[string]any) (ProjectUnit, error) {
	p := ProjectUnit{Name: str(props, "name"), Description: str(props, "description"), Kind: str(props, "kind"), Status: str(props, "status")}
	ms, err := strList(props, "methodologies")
	if err != nil {
		return p, fmt.Errorf("project: %w", err)
	}
	p.Methodologies = ms
	return p, nil
}

// AssignmentKey is the key of the Assignment node granting an org unit (or user) roles on a project.
func AssignmentKey(org, project string) string { return "ASG:" + org + "/" + project }

// PlatformAssignmentKey is the key of the Assignment node granting an org unit (or user) a platform-wide
// role (ADR 0046): no assigns_project link, so it never collides with a per-project AssignmentKey.
func PlatformAssignmentKey(org string) string { return "ASG:" + org + "/PLATFORM" }

// Assignment grants an organisational unit (or, through subtyping, a user) the roles it locally
// holds on a project — a subset of the roles declared by the project's applicable methodologies —
// or, when it names no project, one of the built-in platform roles (ADR 0046), held everywhere.
type Assignment struct {
	Roles       []string
	Description string
}

// Props returns the properties of the Assignment node.
func (a Assignment) Props() map[string]any {
	m := map[string]any{}
	if a.Description != "" {
		m["description"] = a.Description
	}
	if len(a.Roles) > 0 {
		m["roles"] = toAnyList(a.Roles)
	}
	return m
}

// AssignmentFromProps reads an assignment from the properties of its node.
func AssignmentFromProps(props map[string]any) (Assignment, error) {
	a := Assignment{Description: str(props, "description")}
	roles, err := strList(props, "roles")
	if err != nil {
		return a, fmt.Errorf("assignment: %w", err)
	}
	a.Roles = roles
	return a, nil
}

func toAnyList(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// strList reads a property as a list of strings ([]any or []string; nil: none).
func strList(props map[string]any, key string) ([]string, error) {
	switch v := props[key].(type) {
	case nil:
		return nil, nil
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case []string:
		out := make([]string, len(v))
		copy(out, v)
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a list of strings", key)
	}
}
