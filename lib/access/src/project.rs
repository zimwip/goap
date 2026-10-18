//! Projects (ADR 0039) and the Assignments that meet the organisation and the projects.

use serde_json::{Map, Value};

use crate::Error;
use crate::access::str_prop;

/// DefaultProject is the key of the root project (where a caller with no active project works, ADR 0091) the built-in
/// organisation domain tags (`structure.root`, ADR 0039), created by the bootstrap of the graph; like
/// [`crate::DEFAULT_ORG`], for the seeds and tests of the built-in organisation.
pub const DEFAULT_PROJECT: &str = "PROJ-ROOT";

/// Types and links of the project graph objects (ADR 0039): a project mirrors OrgUnit's hierarchy, and Assignment is
/// the meeting point of organisation and project (same shape as an adapter instance, the meeting point of organisation,
/// MCP and connector): the roles an org unit or user locally holds on a project.
pub const NODE_TYPE_PROJECT_UNIT: &str = "organisation@ProjectUnit";
pub const NODE_TYPE_ASSIGNMENT: &str = "organisation@Assignment";
pub const LINK_PROJECT_PART_OF: &str = "organisation@project_part_of";
pub const LINK_ASSIGNS_ORG: &str = "organisation@assigns_org";
pub const LINK_ASSIGNS_PROJECT: &str = "organisation@assigns_project";

/// AssignmentPrefix starts the key of an Assignment node, PlatformScope stands for the project of a platform-wide one.
pub const ASSIGNMENT_PREFIX: &str = "ASG:";
pub const PLATFORM_SCOPE: &str = "PLATFORM";

/// ProjectUnit is a project or sub-project.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProjectUnit {
    pub name: String,
    pub description: String,
    pub kind: String,
    pub status: String,
    pub methodologies: Vec<String>,
}

impl ProjectUnit {
    /// Props returns the properties of the ProjectUnit node.
    pub fn props(&self) -> Map<String, Value> {
        let mut m = Map::new();
        for (k, v) in [
            ("name", &self.name),
            ("description", &self.description),
            ("kind", &self.kind),
            ("status", &self.status),
        ] {
            if !v.is_empty() {
                m.insert(k.into(), v.clone().into());
            }
        }
        if !self.methodologies.is_empty() {
            m.insert("methodologies".into(), to_any_list(&self.methodologies));
        }
        m
    }
}

/// ProjectFromProps reads a project from the properties of its node.
pub fn project_from_props(props: &Map<String, Value>) -> Result<ProjectUnit, Error> {
    Ok(ProjectUnit {
        name: str_prop(props, "name"),
        description: str_prop(props, "description"),
        kind: str_prop(props, "kind"),
        status: str_prop(props, "status"),
        methodologies: str_list(props, "methodologies")
            .map_err(|e| Error::Invalid(format!("project: {e}")))?,
    })
}

/// AssignmentKey is the key of the Assignment node granting an org unit (or user) roles on a project.
pub fn assignment_key(org: &str, project: &str) -> String {
    format!("{ASSIGNMENT_PREFIX}{org}/{project}")
}

/// PlatformAssignmentKey is the key of the Assignment node granting an org unit (or user) a platform-wide role (ADR
/// 0046): no assigns_project link, so it never collides with a per-project [`assignment_key`].
pub fn platform_assignment_key(org: &str) -> String {
    assignment_key(org, PLATFORM_SCOPE)
}

/// Assignment grants an organisational unit (or, through subtyping, a user) the roles it locally holds on a project —
/// a subset of the roles declared by the project's applicable methodologies — or, when it names no project, one of
/// the built-in platform roles (ADR 0046), held everywhere.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Assignment {
    pub roles: Vec<String>,
    pub description: String,
}

impl Assignment {
    /// Props returns the properties of the Assignment node.
    pub fn props(&self) -> Map<String, Value> {
        let mut m = Map::new();
        if !self.description.is_empty() {
            m.insert("description".into(), self.description.clone().into());
        }
        if !self.roles.is_empty() {
            m.insert("roles".into(), to_any_list(&self.roles));
        }
        m
    }
}

/// AssignmentFromProps reads an assignment from the properties of its node.
pub fn assignment_from_props(props: &Map<String, Value>) -> Result<Assignment, Error> {
    Ok(Assignment {
        description: str_prop(props, "description"),
        roles: str_list(props, "roles").map_err(|e| Error::Invalid(format!("assignment: {e}")))?,
    })
}

pub(crate) fn to_any_list(ss: &[String]) -> Value {
    Value::Array(ss.iter().map(|s| Value::from(s.as_str())).collect())
}

/// strList reads a property as a list of strings (absent or null: none; the empty strings and the other values of the
/// list left out).
pub(crate) fn str_list(props: &Map<String, Value>, key: &str) -> Result<Vec<String>, String> {
    match props.get(key) {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(v)) => Ok(v
            .iter()
            .filter_map(|x| x.as_str())
            .filter(|s| !s.is_empty())
            .map(str::to_string)
            .collect()),
        Some(_) => Err(format!("{key} must be a list of strings")),
    }
}
