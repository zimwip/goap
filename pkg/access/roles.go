package access

// NodeTypeRole is the type of the node of a built-in platform role (namespace platform).
const NodeTypeRole = "platform@Role"

// RoleKey is the key of the node of a built-in platform role (ADR 0046).
func RoleKey(name string) string { return "ROLE:" + name }

// Role is a built-in platform-wide role: granted by an organisation@Assignment naming no project
// (assigns_org only), it holds everywhere, independent of any project's methodologies (ADR 0046). The node
// is a fixed catalog entry (like an MCP), kept in sync with the code by SeedBuiltins; it documents the role
// for the IDE, it is not consulted to decide what a role may do (that stays in authz.DefaultPolicies) or
// whether an Assignment's roles are valid (not checked server-side, same as methodology roles, ADR 0043).
type Role struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Props returns the properties of the Role node.
func (r Role) Props() map[string]any {
	m := map[string]any{"name": r.Name}
	if r.Description != "" {
		m["description"] = r.Description
	}
	return m
}

// BuiltinRoles are the platform roles shipped with the platform (ADR 0046, 0047). Administration ("admin",
// RoleAdmin) is one of them: granted by a platform Assignment, checked by the compiled-in floor
// policy (authz.FloorPolicies) ahead of every stored policy, so it can never be denied by one (ADR 0043).
func BuiltinRoles() []Role {
	return []Role{
		{Name: RoleAdmin, Description: "Administers the platform: organisation, projects, methodologies, domains, policies, adapters, and everything else."},
		{Name: RoleReader, Description: "Reads everything on the platform, past the usual organisation/project scoping."},
		{Name: RoleTriage, Description: "Triages the requests: sees every request, links it to the changes that answer it, closes or rejects it."},
	}
}

// PlatformRoles are the built-in roles an Assignment can grant platform-wide (no assigns_project link),
// BuiltinRoles by name. Unlike methodology roles, this is a fixed set, not read from the graph for
// enforcement (authz.DefaultPolicies names them directly); the platform@Role nodes SeedBuiltins keeps in
// sync only document them for the IDE.
var PlatformRoles = func() []string {
	roles := BuiltinRoles()
	out := make([]string, len(roles))
	for i, r := range roles {
		out[i] = r.Name
	}
	return out
}()
