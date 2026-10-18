//! The built-in platform roles (ADR 0046, 0047).

use std::sync::LazyLock;

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::access::{ROLE_ADMIN, ROLE_READER};

/// NodeTypeRole is the type of the node of a built-in platform role (namespace platform).
pub const NODE_TYPE_ROLE: &str = "platform@Role";

/// RoleKey is the key of the node of a built-in platform role (ADR 0046).
pub fn role_key(name: &str) -> String {
    format!("ROLE:{name}")
}

/// Role is a built-in platform-wide role: granted by an organisation@Assignment naming no project (assigns_org only),
/// it holds everywhere, independent of any project's methodologies (ADR 0046). The node is a fixed catalog entry
/// (like an MCP), kept in sync with the code by SeedBuiltins; it documents the role for the IDE, it is not consulted to
/// decide what a role may do (that stays in `goap_authz::DEFAULT_POLICIES`) or whether an Assignment's roles are valid
/// (not checked server-side, same as methodology roles, ADR 0043).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Role {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
}

impl Role {
    /// Props returns the properties of the Role node.
    pub fn props(&self) -> Map<String, Value> {
        let mut m = Map::new();
        m.insert("name".into(), self.name.clone().into());
        if !self.description.is_empty() {
            m.insert("description".into(), self.description.clone().into());
        }
        m
    }
}

/// BuiltinRoles are the platform roles shipped with the platform (ADR 0046, 0047). Administration ("admin",
/// [`ROLE_ADMIN`]) is one of them: granted by a platform Assignment, checked by the compiled-in floor policy
/// (`goap_authz::FLOOR_POLICIES`) ahead of every stored policy, so it can never be denied by one (ADR 0043).
pub fn builtin_roles() -> Vec<Role> {
    vec![
        Role {
            name: ROLE_ADMIN.into(),
            description: "Administers the platform: organisation, projects, methodologies, domains, policies, adapters, and everything else.".into(),
        },
        Role {
            name: ROLE_READER.into(),
            description: "Reads everything on the platform, past the usual organisation/project scoping.".into(),
        },
    ]
}

/// PlatformRoles are the built-in roles an Assignment can grant platform-wide (no assigns_project link),
/// [`builtin_roles`] by name. Unlike methodology roles, this is a fixed set, not read from the graph for enforcement
/// (`goap_authz::DEFAULT_POLICIES` names them directly); the platform@Role nodes SeedBuiltins keeps in sync only
/// document them for the IDE.
pub static PLATFORM_ROLES: LazyLock<Vec<String>> =
    LazyLock::new(|| builtin_roles().into_iter().map(|r| r.name).collect());
