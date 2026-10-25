//! What the graph reads of the organisation (Go's `lib/access` snapshot, ADR 0039, 0054): the projects as the head of
//! main holds them, in force (a retired node left out), each with the methodologies it names and its parent, so that
//! a project's chain and the methodologies applying to it are answered as the Go service's directory answers them.
use std::collections::HashMap;

use graph_catalog::{Catalog, catalog::STRUCTURE_PROJECT};
use serde_json::Value;

use graph_script::abac;

use crate::{
    GraphError, Result,
    model::{MAIN_BRANCH, NodeRef},
    read,
    store::ReadTx,
};

/// The state of a node its lifecycle retires (`access.StateRetired`).
pub const STATE_RETIRED: &str = "retired";

/// A project of the organisation.
#[derive(Debug, Clone, Default)]
pub struct Project {
    pub methodologies: Vec<String>,
    pub stopped: bool,
    /// the source mapping of the project (ADR 0154 §8): the name of a source its URLs name to the declared source
    pub sources: HashMap<String, String>,
}

/// The projects of the organisation, read at the head of main.
#[derive(Debug, Clone, Default)]
pub struct Directory {
    projects: HashMap<String, Project>,
    parent: HashMap<String, String>,
    root: String,
}

impl Directory {
    /// Reads the projects at the head of main of the namespace of their structure.
    pub async fn read<T: ReadTx>(tx: &T, cat: &Catalog) -> Result<Directory> {
        let Some(st) = cat.structure(STRUCTURE_PROJECT) else {
            return Ok(Directory::default());
        };
        let mut out = Directory {
            root: st.root.clone(),
            ..Directory::default()
        };
        let head = read::branch_head(tx, &st.namespace, MAIN_BRANCH).await?;
        let at = read::select(tx, &head, None).await?;
        let refs: Vec<NodeRef> = at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
        let nodes: Vec<_> = tx
            .nodes_at(&refs, "")
            .await?
            .into_iter()
            .filter(|n| !n.deleted && n.state != STATE_RETIRED && cat.is_a(&n.typ, &st.typ))
            .collect();
        let keys: HashMap<_, _> = nodes.iter().map(|n| (n.id.clone(), n.key.clone())).collect();
        for n in &nodes {
            let props = n.props.as_ref();
            let methodologies = props
                .and_then(|p| p.get("methodologies"))
                .and_then(Value::as_array)
                .map(|l| {
                    l.iter()
                        .filter_map(Value::as_str)
                        .filter(|s| !s.is_empty())
                        .map(str::to_string)
                        .collect()
                })
                .unwrap_or_default();
            let stopped = props.and_then(|p| p.get("stopped")).and_then(Value::as_bool) == Some(true);
            // a malformed mapping is one of the snapshot's problems: none (`access.sourceMap`)
            let sources = match props.and_then(|p| p.get("sources")) {
                Some(Value::Object(m)) => m
                    .iter()
                    .map(|(k, v)| {
                        v.as_str()
                            .filter(|v| !k.is_empty() && !v.is_empty())
                            .map(|v| (k.clone(), v.to_string()))
                    })
                    .collect::<Option<HashMap<_, _>>>()
                    .unwrap_or_default(),
                _ => HashMap::new(),
            };
            out.projects.insert(
                n.key.clone(),
                Project {
                    methodologies,
                    stopped,
                    sources,
                },
            );
        }
        let held: Vec<NodeRef> = nodes.iter().map(|n| n.node_ref()).collect();
        for l in tx.links_from(&held, Some(std::slice::from_ref(&st.parent))).await? {
            let (Some(from), crate::model::Target::Node(to)) = (keys.get(&l.from.id), &l.to) else {
                continue;
            };
            let Some(to) = keys.get(&to.id) else { continue };
            if from != to {
                out.parent.insert(from.clone(), to.clone());
            }
        }
        Ok(out)
    }

    /// A project followed by its ancestors, nearest first, ending with the root (`domain.Hierarchy.Chain`).
    pub fn chain(&self, key: &str) -> Vec<String> {
        let key = if key.is_empty() { self.root.as_str() } else { key };
        let mut out = vec![key.to_string()];
        let mut cur = key;
        while let Some(p) = self.parent.get(cur) {
            if p.is_empty() || out.contains(p) {
                break;
            }
            out.push(p.clone());
            cur = p;
        }
        if !out.contains(&self.root) {
            out.push(self.root.clone());
        }
        out
    }

    /// The methodologies that apply to a project: its own and those of its ancestors, nearest first, no repeat.
    pub fn applicable(&self, project: &str) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for k in self.chain(project) {
            for m in self
                .projects
                .get(&k)
                .map(|p| p.methodologies.as_slice())
                .unwrap_or_default()
            {
                if !out.contains(m) {
                    out.push(m.clone());
                }
            }
        }
        out
    }

    /// The source mapping in force on a project: its own and its ancestors', the nearest wins
    /// (`access.Snapshot.ProjectSources`).
    pub fn project_sources(&self, project: &str) -> HashMap<String, String> {
        let mut out = HashMap::new();
        for k in self.chain(project) {
            for (name, to) in self.projects.get(&k).map(|p| &p.sources).into_iter().flatten() {
                out.entry(name.clone()).or_insert_with(|| to.clone());
            }
        }
        out
    }

    pub fn has_project(&self, key: &str) -> bool {
        self.projects.contains_key(key)
    }

    pub fn stopped(&self, key: &str) -> bool {
        self.projects.get(key).is_some_and(|p| p.stopped)
    }
}

/// The platform roles (`access.PlatformRoles`): held everywhere, never on a project.
pub const PLATFORM_ROLES: [&str; 2] = ["admin", "reader"];
/// The platform role of the administrators (`access.RoleAdmin`).
pub const ROLE_ADMIN: &str = "admin";
/// The scope `AssignmentProject` names a platform Assignment's (`access.PlatformScope`).
pub const PLATFORM_SCOPE: &str = "PLATFORM";
pub const TYPE_USER: &str = "organisation@User";
pub const TYPE_ASSIGNMENT: &str = "organisation@Assignment";
pub const TYPE_PROJECT_UNIT: &str = "organisation@ProjectUnit";
/// The resource of the rules of the organisation (`access.ResourcePolicy`).
pub const RESOURCE_POLICY: &str = "policy";
pub const TYPE_POLICY: &str = "organisation@Policy";
pub const TYPE_CRITICALITY_POLICY: &str = "organisation@CriticalityPolicy";
pub const LINK_MEMBER_OF: &str = "organisation@member_of";
pub const LINK_ASSIGNS_ORG: &str = "organisation@assigns_org";
pub const LINK_ASSIGNS_PROJECT: &str = "organisation@assigns_project";
pub const LINK_PROJECT_PART_OF: &str = "organisation@project_part_of";
/// The key prefix of a user's node, its personal unit (`access.UserPrefix`).
pub const USER_PREFIX: &str = "USR:";

/// Who a read or a write is decided for (`authz.Principal`, the part the access asks): the subject, the unit it names
/// and the roles its request carries.
#[derive(Debug, Clone, Default)]
pub struct Principal {
    pub subject: String,
    pub org: String,
    pub roles: Vec<String>,
    /// the active project of its token (`X-Goap-Project`)
    pub project: String,
    /// the person a platform service acts for (`X-Goap-On-Behalf-Of`, ADR 0114)
    pub on_behalf_of: String,
}

/// A hierarchy of a structure by key (`domain.Hierarchy`).
#[derive(Debug, Clone, Default)]
pub struct Hierarchy {
    root: String,
    parent: HashMap<String, String>,
}

impl Hierarchy {
    /// A node followed by its ancestors, nearest first, the root last; cycle-safe (`Hierarchy.Chain`).
    pub fn chain(&self, key: &str) -> Vec<String> {
        let key = if key.is_empty() { self.root.as_str() } else { key };
        let mut out = vec![key.to_string()];
        let mut cur = key.to_string();
        while let Some(p) = self.parent.get(&cur) {
            if p.is_empty() || out.contains(p) {
                break;
            }
            out.push(p.clone());
            cur = p.clone();
        }
        if !out.contains(&self.root) {
            out.push(self.root.clone());
        }
        out
    }
}

/// An Assignment as the snapshot resolves it: the roles a unit holds on a project, or platform-wide when it names
/// none.
#[derive(Debug, Clone, Default)]
struct Assignment {
    key: String,
    org: String,
    project: String,
    roles: Vec<String>,
    owner: bool,
    admin: bool,
}

/// What the access of the organisation reads of a set of its nodes (Go's `access.Snapshot`, the part the validators of
/// a commit ask): the users and their units, the hierarchies, the projects and the Assignments, the retired nodes out.
#[derive(Debug, Clone, Default)]
pub struct Snapshot {
    users: HashMap<String, String>,
    units: Hierarchy,
    projects: Hierarchy,
    project_keys: std::collections::HashSet<String>,
    assignments: Vec<Assignment>,
    org_root: String,
    /// The rules stored as `Policy` nodes, one `authz.Validate` refuses left out (`PolicyFromProps`).
    policies: Vec<abac::Policy>,
    /// The policies of the criticality levels, by the unit owning them and the level (`readCriticality`).
    criticality: HashMap<String, HashMap<&'static str, crate::criticality::Policy>>,
}

fn flag(props: &crate::model::Props, k: &str) -> bool {
    props.as_ref().and_then(|p| p.get(k)).and_then(Value::as_bool) == Some(true)
}

fn text<'a>(props: &'a crate::model::Props, k: &str) -> &'a str {
    props
        .as_ref()
        .and_then(|p| p.get(k))
        .and_then(Value::as_str)
        .unwrap_or("")
}

/// A property as a list of strings (`strList`): none, or a list of strings only.
fn strings(props: &crate::model::Props, k: &str) -> Option<Vec<String>> {
    match props.as_ref().and_then(|p| p.get(k)) {
        None | Some(Value::Null) => Some(Vec::new()),
        Some(Value::Array(l)) => l.iter().map(|v| v.as_str().map(str::to_string)).collect(),
        Some(_) => None,
    }
}

impl Snapshot {
    /// Reads the snapshot of some nodes of the organisation and their outgoing links (`BuildSnapshot`, the types of the
    /// structures those of the built-in catalogue).
    pub fn build(nodes: &[crate::model::Node], links: &[crate::model::Link]) -> Snapshot {
        use graph_catalog::catalog::STRUCTURE_ORGANISATION;
        use std::collections::HashSet;
        let cat = Catalog::builtin();
        let (Some(org), Some(proj)) = (cat.structure(STRUCTURE_ORGANISATION), cat.structure(STRUCTURE_PROJECT)) else {
            return Snapshot::default();
        };
        let retired: HashSet<_> = nodes
            .iter()
            .filter(|n| n.state == STATE_RETIRED)
            .map(|n| n.id.clone())
            .collect();
        let nodes: Vec<_> = nodes.iter().filter(|n| !retired.contains(&n.id)).collect();
        let links: Vec<_> = links
            .iter()
            .filter(|l| !retired.contains(&l.from.id) && l.to_node().is_none_or(|t| !retired.contains(&t.id)))
            .collect();
        let by_id: HashMap<_, _> = nodes.iter().map(|n| (n.id.clone(), *n)).collect();
        let mut s = Snapshot {
            units: Hierarchy {
                root: org.root.clone(),
                ..Hierarchy::default()
            },
            projects: Hierarchy {
                root: proj.root.clone(),
                ..Hierarchy::default()
            },
            org_root: org.root.clone(),
            ..Snapshot::default()
        };
        let mut assigned: HashMap<_, Assignment> = HashMap::new();
        for n in &nodes {
            if cat.is_a(&n.typ, &proj.typ) {
                s.project_keys.insert(n.key.clone());
            }
            match n.typ.as_str() {
                TYPE_USER => {
                    let subject = text(&n.props, "subject");
                    if !subject.is_empty() {
                        s.users.insert(subject.to_string(), String::new());
                    }
                }
                TYPE_POLICY => {
                    let p = abac::Policy {
                        rule: text(&n.props, "rule").to_string(),
                        resource: text(&n.props, "resource").to_string(),
                        action: text(&n.props, "action").to_string(),
                        effect: text(&n.props, "effect").to_string(),
                    };
                    if abac::validate(&p).is_ok() {
                        s.policies.push(p);
                    }
                }
                TYPE_ASSIGNMENT => {
                    if let Some(roles) = strings(&n.props, "roles") {
                        assigned.insert(
                            n.id.clone(),
                            Assignment {
                                key: n.key.clone(),
                                roles,
                                owner: flag(&n.props, "owner"),
                                admin: flag(&n.props, "admin"),
                                ..Assignment::default()
                            },
                        );
                    }
                }
                _ => {}
            }
        }
        for l in &links {
            let Some(to) = l.to_node() else { continue };
            let (from, to) = (by_id.get(&l.from.id).copied(), by_id.get(&to.id).copied());
            let (from_typ, to_typ) = (from.map_or("", |n| n.typ.as_str()), to.map_or("", |n| n.typ.as_str()));
            let to_key = to.map_or(String::new(), |n| n.key.clone());
            let from_key = from.map_or("", |n| n.key.as_str());
            for (h, st) in [(&mut s.units, org), (&mut s.projects, proj)] {
                if l.typ == st.parent && cat.is_a(from_typ, &st.typ) && cat.is_a(to_typ, &st.typ) && from_key != to_key
                {
                    h.parent.insert(from_key.to_string(), to_key.clone());
                }
            }
            if l.typ == LINK_MEMBER_OF
                && from_typ == TYPE_USER
                && cat.is_a(to_typ, &org.typ)
                && let Some(u) = s.users.get_mut(text(&from.expect("typed").props, "subject"))
            {
                *u = to_key.clone();
            }
            if from_typ == TYPE_ASSIGNMENT
                && let Some(a) = assigned.get_mut(&l.from.id)
            {
                if l.typ == LINK_ASSIGNS_ORG {
                    a.org = to_key.clone();
                } else if l.typ == LINK_ASSIGNS_PROJECT {
                    a.project = to_key.clone();
                }
            }
        }
        // a policy of a level belongs to the unit owning it, the first of a unit and a level standing
        let units: std::collections::HashSet<_> = nodes
            .iter()
            .filter(|n| cat.is_a(&n.typ, &org.typ))
            .map(|n| n.key.as_str())
            .collect();
        for n in nodes.iter().filter(|n| n.typ == TYPE_CRITICALITY_POLICY) {
            let unit = n.owner.key.as_str();
            if !units.contains(unit) {
                continue;
            }
            if let Ok((level, p)) = crate::criticality::Policy::from_props(n.props.as_ref()) {
                s.criticality
                    .entry(unit.to_string())
                    .or_default()
                    .entry(level)
                    .or_insert(p);
            }
        }
        for mut a in assigned.into_values() {
            if a.project.is_empty() {
                (a.owner, a.admin) = (false, false);
            }
            if !a.org.is_empty() && (!a.roles.is_empty() || a.owner || a.admin) {
                s.assignments.push(a);
            }
        }
        s
    }

    /// Reads the snapshot at the head of main of the namespace of the organisation (Go's `Directory.Snapshot`: what
    /// the last change applied there wrote).
    pub async fn read<T: ReadTx>(tx: &T) -> Result<Snapshot> {
        use graph_catalog::catalog::STRUCTURE_ORGANISATION;
        let Some(ns) = Catalog::builtin()
            .structure(STRUCTURE_ORGANISATION)
            .map(|s| s.namespace.clone())
        else {
            return Ok(Snapshot::default());
        };
        let head = read::branch_head(tx, &ns, MAIN_BRANCH).await?;
        let at = read::select(tx, &head, None).await?;
        let refs: Vec<NodeRef> = at.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
        let nodes: Vec<_> = tx
            .nodes_at(&refs, "")
            .await?
            .into_iter()
            .filter(|n| !n.deleted)
            .collect();
        let held: Vec<NodeRef> = nodes.iter().map(|n| n.node_ref()).collect();
        let links = tx.links_from(&held, None).await?;
        Ok(Snapshot::build(&nodes, &links))
    }

    /// What a level of criticality requires of the changes held by a unit: the policy of the nearest unit of its chain
    /// holding one for the level, else the compiled-in table (`Snapshot.CriticalityPolicy`).
    pub fn criticality_policy(&self, unit: &str, level: &str) -> crate::criticality::Policy {
        self.units
            .chain(unit)
            .iter()
            .find_map(|u| self.criticality.get(u).and_then(|m| m.get(level)))
            .cloned()
            .unwrap_or_else(|| crate::criticality::Policy::default_of(level))
    }

    /// The units an Assignment names to grant a subject roles: its user, then its unit and the unit's ancestors
    /// (`SubjectChain`).
    pub fn subject_chain(&self, subject: &str) -> Vec<String> {
        self.chain_of(&Principal {
            subject: subject.to_string(),
            ..Principal::default()
        })
    }

    /// The units of a principal (`SubjectChain`): its unit is the one its user belongs to, else the one it names,
    /// else the root.
    fn chain_of(&self, p: &Principal) -> Vec<String> {
        let subject = p.subject.as_str();
        let unit = self
            .users
            .get(subject)
            .filter(|u| !u.is_empty())
            .map_or(p.org.as_str(), String::as_str);
        let unit = if unit.is_empty() { self.org_root.as_str() } else { unit };
        let mut out = vec![format!("{USER_PREFIX}{subject}")];
        out.extend(self.units.chain(unit));
        out
    }

    /// The platform roles held by a unit of a chain (`PlatformRoles`).
    pub fn platform_roles(&self, chain: &[String]) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for a in &self.assignments {
            if a.project.is_empty() && chain.contains(&a.org) {
                for r in &a.roles {
                    if !out.contains(r) {
                        out.push(r.clone());
                    }
                }
            }
        }
        out
    }

    /// Whether a subject holds the administration of the platform (`Enrich(...).HasRole(RoleAdmin)`).
    pub fn administers(&self, subject: &str) -> bool {
        self.platform_roles(&self.subject_chain(subject))
            .iter()
            .any(|r| r == ROLE_ADMIN)
    }

    /// The root of the projects (`Structure(project).Root`).
    pub fn project_root(&self) -> &str {
        &self.projects.root
    }

    pub fn has_project(&self, key: &str) -> bool {
        self.project_keys.contains(key)
    }

    /// The roles an Assignment grants a unit of a chain on a project of a chain (`ProjectRoles`).
    pub fn project_roles(&self, org_chain: &[String], project_chain: &[String]) -> Vec<String> {
        let mut out: Vec<String> = Vec::new();
        for a in &self.assignments {
            if org_chain.contains(&a.org) && project_chain.contains(&a.project) {
                for r in &a.roles {
                    if !out.contains(r) {
                        out.push(r.clone());
                    }
                }
            }
        }
        out
    }

    /// Whether an Assignment of a unit of a chain on a project of a chain makes its subject an administrator, the
    /// owner, of the project (`ProjectFlags`): the owner is an administrator too.
    pub fn project_flags(&self, org_chain: &[String], project_chain: &[String]) -> (bool, bool) {
        let (mut admin, mut owner) = (false, false);
        for a in &self.assignments {
            if a.project.is_empty() || !org_chain.contains(&a.org) || !project_chain.contains(&a.project) {
                continue;
            }
            owner = owner || a.owner;
            admin = admin || a.admin || a.owner;
        }
        (admin, owner)
    }

    /// Whether a principal works on a project (`MayAccessProject`, ADR 0039, 0138): an administrator of it, or one
    /// holding a platform role, or a role on it or a project above it.
    pub fn may_access_project(&self, p: &Principal, project: &str) -> bool {
        if p.subject.is_empty() {
            return false;
        }
        let chain = self.chain_of(p);
        let projects = self.projects.chain(project);
        if self.project_flags(&chain, &projects).0 {
            return true;
        }
        !self.platform_roles(&chain).is_empty()
            || !self.project_roles(&chain, &projects).is_empty()
            || p.roles.iter().any(|r| r == ROLE_ADMIN)
    }

    /// The principal completed with what the organisation knows of its subject (`Enrich`): its unit when it names
    /// none, the platform roles apart from the others, those its unit chain holds added.
    fn enrich(&self, p: &Principal) -> abac::Subject {
        let mut out = abac::Subject {
            subject: p.subject.clone(),
            org: p.org.clone(),
            project: p.project.clone(),
            on_behalf_of: p.on_behalf_of.clone(),
            ..abac::Subject::default()
        };
        if out.org.is_empty()
            && let Some(u) = self.users.get(&p.subject)
        {
            out.org = u.clone();
        }
        let add = |l: &mut Vec<String>, r: &str| {
            if !l.iter().any(|x| x == r) {
                l.push(r.to_string());
            }
        };
        for r in &p.roles {
            if PLATFORM_ROLES.contains(&r.as_str()) {
                add(&mut out.global, r);
            } else {
                add(&mut out.roles, r);
            }
        }
        let chain = self.chain_of(&Principal {
            subject: out.subject.clone(),
            org: out.org.clone(),
            ..Principal::default()
        });
        for r in self.platform_roles(&chain) {
            add(&mut out.global, &r);
        }
        out
    }

    /// Decides a request as Go's `access.Authorizer` does (ADR 0020, 0043, 0138): the principal completed, the roles
    /// it holds on the resource's project (the root when it names none) merged, the flags of that project's
    /// Assignments when the resource names one; the floor first (an administrator may do everything), then the
    /// stored rules, the compiled-in defaults while none is stored, evaluated as Casbin and govaluate evaluate them.
    pub fn authorize(&self, p: &Principal, mut res: abac::Resource, act: &str) -> Result<bool> {
        if p.subject.is_empty() {
            return Ok(false);
        }
        let mut sub = self.enrich(p);
        if !res.org.is_empty() && res.org_chain.is_empty() {
            res.org_chain = self.units.chain(&res.org);
        }
        let projects = self.projects.chain(&res.project_id);
        let chain = self.chain_of(&Principal {
            subject: sub.subject.clone(),
            org: sub.org.clone(),
            ..Principal::default()
        });
        for r in self.project_roles(&chain, &projects) {
            if !sub.roles.contains(&r) {
                sub.roles.push(r);
            }
        }
        if !res.project_id.is_empty() {
            (sub.project_admin, sub.project_owner) = self.project_flags(&chain, &projects);
        }
        let floor = abac::Enforcer::new(&abac::floor_policies());
        if floor.authorize(&sub, &res, act).map_err(GraphError::internal)? {
            return Ok(true);
        }
        let rules = if self.policies.is_empty() {
            abac::Enforcer::new(&abac::default_policies())
        } else {
            abac::Enforcer::new(&self.policies)
        };
        rules.authorize(&sub, &res, act).map_err(GraphError::internal)
    }

    /// Whether the floor lets a principal through (`Authorizer.Floor`, ADR 0020): completed with what the organisation
    /// knows of it, it administers the platform, whatever the stored rules say.
    pub fn floor(&self, p: &Principal, res: &abac::Resource, act: &str) -> Result<bool> {
        if p.subject.is_empty() {
            return Ok(false);
        }
        let floor = abac::Enforcer::new(&abac::floor_policies());
        floor.authorize(&self.enrich(p), res, act).map_err(GraphError::internal)
    }

    /// The projects whose nodes a principal reads (`ReachableProjects`, ADR 0161 §5): those it works on, sorted.
    pub fn reachable_projects(&self, p: &Principal) -> Vec<String> {
        let mut out: Vec<String> = self
            .project_keys
            .iter()
            .filter(|k| self.may_access_project(p, k))
            .cloned()
            .collect();
        out.sort();
        out
    }

    /// Whether a project, or one above it, is owned by an owner Assignment (`ProjectOwned`).
    pub fn project_owned(&self, project: &str) -> bool {
        let chain = self.projects.chain(project);
        self.assignments
            .iter()
            .any(|a| a.owner && !a.project.is_empty() && chain.contains(&a.project))
    }

    /// The chain of a project, itself first, the root last (`ProjectChain`).
    pub fn project_chain(&self, project: &str) -> Vec<String> {
        self.projects.chain(project)
    }

    /// The parent of a project, none for the root (`ProjectOf`).
    pub fn project_of(&self, project: &str) -> String {
        self.project_chain(project).get(1).cloned().unwrap_or_default()
    }

    /// The units of a principal, its user first (`SubjectChain`).
    pub fn principal_chain(&self, p: &Principal) -> Vec<String> {
        self.chain_of(p)
    }

    /// The project an Assignment of a key names ([`PLATFORM_SCOPE`] for a platform one) and its owner and admin
    /// flags, when the snapshot holds it (`AssignmentProject`).
    pub fn assignment_flags(&self, key: &str) -> Option<(String, bool, bool)> {
        self.assignments.iter().find(|a| a.key == key).map(|a| {
            if a.project.is_empty() {
                (PLATFORM_SCOPE.to_string(), false, false)
            } else {
                (a.project.clone(), a.owner, a.admin)
            }
        })
    }

    /// The project an Assignment of a key names ([`PLATFORM_SCOPE`] for a platform one) and its owner flag, when the
    /// snapshot holds it (`AssignmentProject`).
    pub fn assignment_project(&self, key: &str) -> Option<(String, bool)> {
        self.assignments.iter().find(|a| a.key == key).map(|a| {
            if a.project.is_empty() {
                (PLATFORM_SCOPE.to_string(), false)
            } else {
                (a.project.clone(), a.owner)
            }
        })
    }
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;
    use crate::model::{Link, Node, NodeId, NodeRef, Target};

    fn node(id: &str, key: &str, typ: &str, state: &str, props: Value) -> Node {
        Node {
            id: NodeId(id.into()),
            version: 1,
            key: key.into(),
            typ: typ.into(),
            state: state.into(),
            props: props.as_object().cloned(),
            ..Node::default()
        }
    }

    fn link(typ: &str, from: &str, to: &str) -> Link {
        Link {
            typ: typ.into(),
            from: NodeRef::new(NodeId(from.into()), 1),
            to: Target::Node(NodeRef::new(NodeId(to.into()), 1)),
            id: Default::default(),
            props: None,
            change_id: Default::default(),
        }
    }

    #[test]
    fn resolves_platform_roles_and_project_owners() {
        let nodes = vec![
            node("o", "ORG-DEFAULT", "organisation@OrgUnit", "", json!({})),
            node("t", "TEAM", "organisation@OrgUnit", "", json!({})),
            node("u", "USR:ann", TYPE_USER, "active", json!({"subject": "ann"})),
            node(
                "a",
                "ASG:TEAM/PLATFORM",
                TYPE_ASSIGNMENT,
                "active",
                json!({"roles": ["admin"]}),
            ),
            node("p", "PROJ-ROOT", TYPE_PROJECT_UNIT, "", json!({})),
            node("q", "PROJ-Q", TYPE_PROJECT_UNIT, "", json!({})),
            node(
                "b",
                "ASG:USR:ann/PROJ-ROOT",
                TYPE_ASSIGNMENT,
                "active",
                json!({"owner": true}),
            ),
            node(
                "r",
                "ASG:USR:ann/OLD",
                TYPE_ASSIGNMENT,
                "retired",
                json!({"roles": ["admin"]}),
            ),
            node("c", "USR:cy", TYPE_USER, "active", json!({"subject": "cy"})),
            node(
                "d",
                "ASG:USR:cy/PROJ-Q",
                TYPE_ASSIGNMENT,
                "active",
                json!({"roles": ["dev"]}),
            ),
        ];
        let links = vec![
            link("organisation@part_of", "t", "o"),
            link(LINK_MEMBER_OF, "u", "t"),
            link(LINK_ASSIGNS_ORG, "a", "t"),
            link(LINK_PROJECT_PART_OF, "q", "p"),
            link(LINK_ASSIGNS_ORG, "b", "u"),
            link(LINK_ASSIGNS_PROJECT, "b", "p"),
            link(LINK_ASSIGNS_ORG, "r", "u"),
            link(LINK_MEMBER_OF, "c", "o"),
            link(LINK_ASSIGNS_ORG, "d", "c"),
            link(LINK_ASSIGNS_PROJECT, "d", "q"),
        ];
        let s = Snapshot::build(&nodes, &links);
        assert_eq!(s.subject_chain("ann"), ["USR:ann", "TEAM", "ORG-DEFAULT"]);
        assert!(s.administers("ann"));
        assert!(!s.administers("bob"));
        assert!(s.project_owned("PROJ-Q"));
        assert_eq!(
            s.assignment_project("ASG:USR:ann/PROJ-ROOT"),
            Some(("PROJ-ROOT".into(), true))
        );
        assert_eq!(
            s.assignment_project("ASG:TEAM/PLATFORM"),
            Some((PLATFORM_SCOPE.into(), false))
        );
        assert_eq!(s.assignment_project("ASG:USR:ann/OLD"), None);
        let who = |subject: &str, roles: &[&str]| Principal {
            subject: subject.into(),
            roles: roles.iter().map(|r| r.to_string()).collect(),
            ..Principal::default()
        };
        assert_eq!(s.reachable_projects(&who("cy", &[])), ["PROJ-Q"]);
        assert!(!s.may_access_project(&who("cy", &[]), "PROJ-ROOT"));
        assert_eq!(s.reachable_projects(&who("ann", &[])), ["PROJ-Q", "PROJ-ROOT"]);
        assert!(!s.may_access_project(&who("zed", &[]), "PROJ-Q"));
        assert!(s.may_access_project(&who("zed", &["admin"]), "PROJ-Q"));
        assert!(!s.may_access_project(&who("", &["admin"]), "PROJ-Q"));
    }
}
