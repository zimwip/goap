//! Who may do what as graph data (organisation namespace): User nodes (profile, membership of a unit through
//! member_of) and Policy nodes (the ABAC rules), read into a [`Snapshot`] by a [`Directory`], decided by an
//! [`Authorizer`]; the compiled-in floor keeps administrators in whatever the stored policies say.

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_authz::{Casbin, DEFAULT_POLICIES, FLOOR_POLICIES, Policy, Principal, Request};
use goap_criticality::{Level as CriticalityLevel, Policy as CriticalityPolicy};
use goap_ctx::Ctx;
use goap_domain::{
    BaselineID, Hierarchy, Link, Node, NodeID, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT,
    Structures,
};
use goap_graphsnap::{Cache, ReadError};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use sha2::{Digest, Sha256};

use crate::Error;
use crate::personal::user_key;
use crate::project::{
    LINK_ASSIGNS_ORG, LINK_ASSIGNS_PROJECT, NODE_TYPE_ASSIGNMENT, assignment_from_props,
};
use crate::retired::in_force;

/// NamespaceOrganisation is the namespace of the built-in organisation domain (lib/domains/builtin/organisation.yaml):
/// the one place its names are written in Rust besides the yaml. The graph core names none of them: it learns the
/// structures from the type catalogue.
pub const NAMESPACE_ORGANISATION: &str = "organisation";
pub const NODE_TYPE_ORG_UNIT: &str = "organisation@OrgUnit";
pub const NODE_TYPE_USER: &str = "organisation@User";
pub const NODE_TYPE_POLICY: &str = "organisation@Policy";
/// NodeTypeCriticalityPolicy is what a criticality level requires of the changes of the unit that owns the node (ADR
/// 0075 §3, criticality.rs).
pub const NODE_TYPE_CRITICALITY_POLICY: &str = "organisation@CriticalityPolicy";
pub const LINK_MEMBER_OF: &str = "organisation@member_of";
pub const LINK_PART_OF: &str = "organisation@part_of";

/// DefaultOrg is the key of the root unit the built-in organisation domain tags (`structure.root`): created by the
/// bootstrap of the graph, it holds the changes that name no unit and is the root of the resolution of the adapters of
/// every unit. Code that reads the structures takes the root from them (`goap_domain::Structures`); these constants
/// are for the seeds and tests of the built-in organisation.
pub const DEFAULT_ORG: &str = "ORG-DEFAULT";

/// ResourcePolicy is the ABAC resource that guards changes to User and Policy nodes.
pub const RESOURCE_POLICY: &str = "policy";

/// PropWaitingUnit is the OrgUnit property that flags the waiting unit (ADR 0042): a unit an administrator creates, at
/// their discretion, for users signing in for the first time — they are linked member_of it until an administrator
/// moves them. Without one, new users join [`DEFAULT_ORG`].
pub const PROP_WAITING_UNIT: &str = "waiting";

/// IsWaitingUnit reports whether the properties of an OrgUnit flag it as the waiting unit of new users.
pub fn is_waiting_unit(props: &Map<String, Value>) -> bool {
    props
        .get(PROP_WAITING_UNIT)
        .and_then(Value::as_bool)
        .unwrap_or(false)
}

/// User is a person or a service account. It holds no role of its own held *on it* (ADR 0043): every role,
/// administration included, is granted by an Assignment — a project-scoped one, or, for a platform role, one naming no
/// project (ADR 0046, 0047).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct User {
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub display_name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub email: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub locale: String,
    /// Unit is the organisational unit the user belongs to (the member_of link); never serialized.
    #[serde(skip)]
    pub unit: String,
}

/// RoleAdmin is the platform role (ADR 0046) of an administrator: granted by a platform Assignment, it is the one role
/// the compiled-in floor policy checks (`goap_authz::FLOOR_POLICIES`), ahead of every stored policy, so it can never
/// be denied by one.
pub const ROLE_ADMIN: &str = "admin";

/// RoleReader is a built-in platform role (ADR 0046): granted by an Assignment naming no project (`assigns_org` only),
/// it holds everywhere, regardless of what methodologies a project applies — unlike a methodology role, which only
/// exists where a project names the methodology declaring it. A policy that wants it scoped to a project reads that
/// project's own Assignments itself (ProjectRoles); the grant itself is project-independent.
pub const ROLE_READER: &str = "reader";

/// PolicyPrefix starts the key of a Policy node.
pub const POLICY_PREFIX: &str = "POL:";

/// PolicyKey is the key of the node of a policy: its target plus a digest of the rule.
pub fn policy_key(p: &Policy) -> String {
    let h = Sha256::digest(format!("{}\x00{}", p.rule, p.effect).as_bytes());
    let hex: String = h[..4].iter().map(|b| format!("{b:02x}")).collect();
    format!("{POLICY_PREFIX}{}/{}/{hex}", p.resource, p.action)
}

impl User {
    /// Props returns the properties of the User node.
    pub fn props(&self) -> Map<String, Value> {
        let mut m = Map::new();
        m.insert("subject".into(), self.subject.clone().into());
        for (k, v) in [
            ("displayName", &self.display_name),
            ("email", &self.email),
            ("locale", &self.locale),
        ] {
            if !v.is_empty() {
                m.insert(k.into(), v.clone().into());
            }
        }
        m
    }
}

/// The string property k ("" when absent or not a string).
pub(crate) fn str_prop(props: &Map<String, Value>, k: &str) -> String {
    props
        .get(k)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

/// UserFromProps reads a user from the properties of its node.
pub fn user_from_props(props: &Map<String, Value>) -> Result<User, Error> {
    let u = User {
        subject: str_prop(props, "subject"),
        display_name: str_prop(props, "displayName"),
        email: str_prop(props, "email"),
        locale: str_prop(props, "locale"),
        unit: String::new(),
    };
    if u.subject.is_empty() {
        return Err(Error::Invalid("user without subject".into()));
    }
    Ok(u)
}

/// PolicyProps returns the properties of the Policy node.
pub fn policy_props(p: &Policy) -> Map<String, Value> {
    let mut m = Map::new();
    m.insert("rule".into(), p.rule.clone().into());
    m.insert("resource".into(), p.resource.clone().into());
    m.insert("action".into(), p.action.clone().into());
    m.insert("effect".into(), p.effect.clone().into());
    m
}

/// PolicyFromProps reads a policy from the properties of its node and validates it.
pub fn policy_from_props(props: &Map<String, Value>) -> Result<Policy, goap_authz::Error> {
    let p = Policy {
        rule: str_prop(props, "rule"),
        resource: str_prop(props, "resource"),
        action: str_prop(props, "action"),
        effect: str_prop(props, "effect"),
    };
    goap_authz::validate(&p)?;
    Ok(p)
}

/// Graph is the part of the graph the directory reads: the head of a branch and its graph (`goap_graphsnap::Graph`),
/// and the structures. Every `goap_graphapi::Port` is one.
#[async_trait]
pub trait Graph: goap_graphsnap::Graph {
    /// Structures names the organisation and the projects (ADR 0054): the snapshot never names their types itself.
    async fn structures(&self, ctx: &Ctx) -> Result<Structures, goap_graphapi::Error>;
}

#[async_trait]
impl<T: goap_graphapi::Port + ?Sized> Graph for T {
    async fn structures(&self, ctx: &Ctx) -> Result<Structures, goap_graphapi::Error> {
        goap_graphapi::Port::structures(self, ctx).await
    }
}

/// Snapshot is the users and policies as of one baseline.
#[derive(Debug, Clone, Default)]
pub struct Snapshot {
    pub baseline: BaselineID,
    /// Problems lists the nodes that could not be read.
    pub problems: Vec<String>,
    pub policies: Vec<Policy>,

    /// the organisation and the projects the snapshot was read with (ADR 0054)
    pub(crate) structures: Structures,
    users: HashMap<String, User>,
    /// the hierarchies of the organisation (part_of) and of the projects (project_part_of, ADR 0039), resolved by
    /// `goap_domain::Hierarchy`
    units: Hierarchy,
    projects: Hierarchy,
    /// the methodologies each project names (ADR 0039), by project key; a key is present for every project
    project_methods: HashMap<String, Vec<String>>,
    /// the Assignment nodes (ADR 0039), resolved from their assigns_org/assigns_project links
    assignments: Vec<ResolvedAssignment>,
    /// the policies of the criticality levels, by the unit that owns them and the level
    pub(crate) criticality: HashMap<String, HashMap<CriticalityLevel, CriticalityPolicy>>,
}

/// A resolved Assignment node: the roles an org unit holds on a project, or, when project is empty (no
/// assigns_project link), platform-wide (ADR 0046).
#[derive(Debug, Clone, Default)]
struct ResolvedAssignment {
    org: String,
    project: String,
    roles: Vec<String>,
}

fn push_unique(out: &mut Vec<String>, r: &str) {
    if !out.iter().any(|x| x == r) {
        out.push(r.to_string());
    }
}

/// BuildSnapshot reads the users and policies of a baseline graph of the namespace of the structures st: the units and
/// projects are the nodes of their types (and subtypes), their hierarchies the parent links st names (ADR 0054).
pub fn build_snapshot(
    st: &Structures,
    id: &BaselineID,
    nodes: &[Node],
    links: &[Link],
) -> Snapshot {
    let (nodes, links) = in_force(nodes, links);
    let mut s = Snapshot {
        baseline: id.clone(),
        structures: st.clone(),
        units: st.hierarchy(STRUCTURE_ORGANISATION, &nodes, &links),
        projects: st.hierarchy(STRUCTURE_PROJECT, &nodes, &links),
        ..Snapshot::default()
    };
    let mut by_id: HashMap<&NodeID, &Node> = HashMap::new();
    let mut crit: Vec<&Node> = Vec::new();
    let mut assign_build: BTreeMap<&NodeID, ResolvedAssignment> = BTreeMap::new();
    for n in &nodes {
        by_id.insert(&n.id, n);
        // nodes is scoped to the namespace of the structures by the Directory's cache; this does not need to filter
        // it again.
        if st.includes(STRUCTURE_PROJECT, &n.r#type) {
            s.project_methods.insert(
                n.key.clone(),
                string_list(n.properties.get("methodologies")),
            );
        }
        match n.r#type.as_str() {
            NODE_TYPE_USER => match user_from_props(&n.properties) {
                Ok(u) => {
                    s.users.insert(u.subject.clone(), u);
                }
                Err(e) => s.problems.push(format!("{}: {e}", n.key)),
            },
            NODE_TYPE_POLICY => match policy_from_props(&n.properties) {
                Ok(p) => s.policies.push(p),
                Err(e) => s.problems.push(format!("{}: {e}", n.key)),
            },
            NODE_TYPE_CRITICALITY_POLICY => crit.push(n),
            NODE_TYPE_ASSIGNMENT => match assignment_from_props(&n.properties) {
                Ok(a) => {
                    assign_build.insert(
                        &n.id,
                        ResolvedAssignment {
                            roles: a.roles,
                            ..ResolvedAssignment::default()
                        },
                    );
                }
                Err(e) => s.problems.push(format!("{}: {e}", n.key)),
            },
            _ => {}
        }
    }
    let none = Node::default();
    for l in &links {
        let from = by_id.get(&l.from.id).copied().unwrap_or(&none);
        let to = by_id.get(&l.to.id).copied().unwrap_or(&none);
        if l.r#type == LINK_MEMBER_OF
            && from.r#type == NODE_TYPE_USER
            && st.includes(STRUCTURE_ORGANISATION, &to.r#type)
        {
            let subject = str_prop(&from.properties, "subject");
            if let Some(u) = s.users.get_mut(&subject) {
                u.unit = to.key.clone();
            }
        }
        if l.r#type == LINK_ASSIGNS_ORG
            && from.r#type == NODE_TYPE_ASSIGNMENT
            && let Some(a) = assign_build.get_mut(&l.from.id)
        {
            a.org = to.key.clone();
        }
        if l.r#type == LINK_ASSIGNS_PROJECT
            && from.r#type == NODE_TYPE_ASSIGNMENT
            && let Some(a) = assign_build.get_mut(&l.from.id)
        {
            a.project = to.key.clone();
        }
    }
    // project is empty for a platform-wide assignment (no assigns_project link, ADR 0046); still kept.
    s.assignments = assign_build
        .into_values()
        .filter(|a| !a.org.is_empty() && !a.roles.is_empty())
        .collect();
    s.read_criticality(&crit, &by_id);
    s.policies.sort_by_key(policy_key);
    s
}

fn string_list(v: Option<&Value>) -> Vec<String> {
    match v {
        Some(Value::Array(l)) => l
            .iter()
            .filter_map(Value::as_str)
            .filter(|s| !s.is_empty())
            .map(str::to_string)
            .collect(),
        _ => Vec::new(),
    }
}

impl Snapshot {
    /// RootProject is the key of the root project: where a caller with no active project works (ADR 0091).
    pub fn root_project(&self) -> String {
        self.structures.project().root
    }

    /// Structures are the organisation and the projects the snapshot was read with.
    pub fn structures(&self) -> &Structures {
        &self.structures
    }

    /// HasProject reports whether a project of that key exists (as of the snapshot).
    pub fn has_project(&self, key: &str) -> bool {
        self.project_methods.contains_key(key)
    }

    /// SubProjects returns a project and every project below it (project_part_of), sorted: the projects a search "in a
    /// project and its sub-projects" covers (ADR 0095). An unknown project yields just itself.
    pub fn sub_projects(&self, project: &str) -> Vec<String> {
        let mut below: Vec<String> = self
            .project_methods
            .keys()
            .filter(|k| k.as_str() != project && self.project_chain(k).iter().any(|p| p == project))
            .cloned()
            .collect();
        below.sort();
        let mut out = vec![project.to_string()];
        out.extend(below);
        out
    }

    /// ApplicableMethodologies returns the methodologies that apply to a project: its own and those of its ancestors,
    /// nearest first, without repeats (ADR 0039; the server-side twin of the web's applicableMethodologies).
    pub fn applicable_methodologies(&self, project: &str) -> Vec<String> {
        let mut out = Vec::new();
        for k in self.project_chain(project) {
            for m in self.project_methods.get(&k).into_iter().flatten() {
                push_unique(&mut out, m);
            }
        }
        out
    }

    /// Chain returns a unit followed by its ancestors (part_of), nearest first, ending with the default unit: where a
    /// role held in a unit holds (design rule 3, ADR 0035 §2).
    pub fn chain(&self, unit: &str) -> Vec<String> {
        self.units.chain(unit)
    }

    /// ProjectChain returns a project followed by its ancestors (project_part_of), nearest first, ending with the root
    /// project (ADR 0039; mirrors Chain). The root project links project_part_of to itself, but that self-link is never
    /// recorded as a parent, so it terminates the walk the same way ORG-DEFAULT's absent part_of link terminates Chain.
    pub fn project_chain(&self, project: &str) -> Vec<String> {
        self.projects.chain(project)
    }

    /// SubjectChain is what an Assignment can name to grant a subject roles (ADR 0039, 0043): the subject's own User
    /// node (User extends OrgUnit), then the unit it belongs to and that unit's ancestors.
    pub fn subject_chain(&self, p: &Principal) -> Vec<String> {
        let mut unit = p.org.clone();
        if let Some(u) = self.users.get(&p.subject)
            && !u.unit.is_empty()
        {
            unit = u.unit.clone();
        }
        if unit.is_empty() {
            unit = self.structures.organisation().root;
        }
        let mut out = vec![user_key(&p.subject)];
        out.extend(self.chain(&unit));
        out
    }

    /// ProjectRoles returns the roles granted, by an Assignment node, to any unit of org_chain on any project of
    /// project_chain (ADR 0039): "resolve the role of a user in a project" resolves org_chain (the subject's chain,
    /// SubjectChain) and project_chain (the change's project chain) first, then unions the roles of every matching
    /// Assignment. Unscoped: the membership test already happened, so the result can be merged straight into a
    /// Principal's roles (`Principal::has_role_in`).
    pub fn project_roles(&self, org_chain: &[String], project_chain: &[String]) -> Vec<String> {
        let mut out = Vec::new();
        for a in &self.assignments {
            if !org_chain.contains(&a.org) || !project_chain.contains(&a.project) {
                continue;
            }
            for r in &a.roles {
                push_unique(&mut out, r);
            }
        }
        out
    }

    /// PlatformRoles returns the platform-wide roles (ADR 0046) granted to any unit of org_chain by an Assignment
    /// naming no project: unlike ProjectRoles, these hold everywhere, not just on a project chain.
    pub fn platform_roles(&self, org_chain: &[String]) -> Vec<String> {
        let mut out = Vec::new();
        for a in &self.assignments {
            if !a.project.is_empty() || !org_chain.contains(&a.org) {
                continue;
            }
            for r in &a.roles {
                push_unique(&mut out, r);
            }
        }
        out
    }

    /// User returns the user of a subject.
    pub fn user(&self, subject: &str) -> Option<&User> {
        self.users.get(subject)
    }

    /// Users lists the users by subject.
    pub fn users(&self) -> Vec<User> {
        let mut out: Vec<User> = self.users.values().cloned().collect();
        out.sort_by(|a, b| a.subject.cmp(&b.subject));
        out
    }

    /// Enrich completes a principal with what the graph knows of its subject: it gets the platform-wide roles (ADR
    /// 0046) granted to it or to a unit it belongs to — administration ([`ROLE_ADMIN`]) included, one of them (ADR
    /// 0047); the unit it belongs to is its organisation when the token names none. This is resource-independent, so
    /// the floor (which calls Enrich alone, never the fuller Authorize) sees a platform-granted admin too: no stored
    /// policy can lock one out. Platform roles are resolved from the principal's org chain, not its User node, so they
    /// reach a principal with no User node of its own (a trigger's service identity, a token naming only an org) the
    /// same way ProjectRoles already does. Its project-scoped roles are held on projects: the Authorizer adds those of
    /// the resource's project (ADR 0043).
    pub fn enrich(&self, p: &Principal) -> Principal {
        let mut p = p.clone();
        if p.anonymous() {
            return p;
        }
        if let Some(u) = self.users.get(&p.subject)
            && p.org.is_empty()
        {
            p.org = u.unit.clone();
        }
        let mut roles = p.roles.clone();
        for r in self.platform_roles(&self.subject_chain(&p)) {
            push_unique(&mut roles, &r);
        }
        p.roles = roles;
        p
    }

    /// MayAccessProject reports whether a principal may work on a project (ADR 0039): it holds a platform role
    /// (administration included, ADR 0047), or a role an Assignment grants it on the project or one above it. Taking
    /// the active project of a token is the only thing it gates: what the principal may then do there is the
    /// policies' business.
    pub fn may_access_project(&self, p: &Principal, project: &str) -> bool {
        let p = self.enrich(p);
        if p.anonymous() {
            return false;
        }
        let chain = self.subject_chain(&p);
        !self.platform_roles(&chain).is_empty()
            || !self
                .project_roles(&chain, &self.project_chain(project))
                .is_empty()
            || p.roles.iter().any(|r| r == ROLE_ADMIN)
    }
}

/// The error of reading the directory: the last snapshot read, if any, and why the graph could not be read.
pub type SnapshotError = ReadError<Arc<Snapshot>>;

/// Directory reads the snapshot of the head of the main branch (see `goap_graphsnap::Cache`); TTL is how often the
/// head is looked at, one second by default (zero).
pub struct Directory {
    pub graph: Arc<dyn Graph>,
    pub ttl: Duration,

    cache: parking_lot::Mutex<Option<Arc<Cache<Arc<Snapshot>>>>>,
}

impl std::fmt::Debug for Directory {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Directory")
            .field("ttl", &self.ttl)
            .finish_non_exhaustive()
    }
}

impl Directory {
    /// A directory over a graph, with the default TTL.
    pub fn new(graph: Arc<dyn Graph>) -> Directory {
        Directory {
            graph,
            ttl: Duration::ZERO,
            cache: parking_lot::Mutex::new(None),
        }
    }

    /// The directory with another TTL.
    pub fn with_ttl(mut self, ttl: Duration) -> Directory {
        self.ttl = ttl;
        self
    }

    /// Snapshot returns the current snapshot. A graph without any baseline yields an empty one; when the graph cannot
    /// be read the error carries the last snapshot (none if none was read).
    pub async fn snapshot(&self, ctx: &Ctx) -> Result<Arc<Snapshot>, SnapshotError> {
        let c = self.snapshots(ctx).await?;
        c.get(ctx).await.map(|(s, _)| s)
    }

    /// The current snapshot, else the last one read when the graph cannot be read (Go's `s, _ := d.Snapshot(ctx)`).
    pub async fn snapshot_or_last(&self, ctx: &Ctx) -> Option<Arc<Snapshot>> {
        match self.snapshot(ctx).await {
            Ok(s) => Some(s),
            Err(e) => e.last.map(|(s, _)| s),
        }
    }

    /// Refresh reads the head now, past the TTL: after a write the next calls must see (a user just declared at
    /// sign-in, ADR 0042).
    pub async fn refresh(&self, ctx: &Ctx) -> Result<(), SnapshotError> {
        let c = self.snapshots(ctx).await?;
        c.fresh(ctx).await.map(|_| ())
    }

    /// snapshots returns the cache of the head of the namespace of the structures, asking the graph for them first (ADR
    /// 0054; asked again until the graph answers). The structures are tagged by a frozen built-in domain: they do not
    /// change while the service runs.
    async fn snapshots(&self, ctx: &Ctx) -> Result<Arc<Cache<Arc<Snapshot>>>, SnapshotError> {
        if let Some(c) = self.cache.lock().clone() {
            return Ok(c);
        }
        let st = self
            .graph
            .structures(ctx)
            .await
            .map_err(|error| ReadError { last: None, error })?;
        let namespace = st.organisation().namespace;
        let graph: Arc<dyn goap_graphsnap::Graph> = self.graph.clone();
        let cache = Cache::new(graph, namespace, move |id, nodes, links| {
            Arc::new(build_snapshot(&st, id, nodes, links))
        })
        .with_ttl(self.ttl);
        let mut slot = self.cache.lock();
        Ok(slot.get_or_insert_with(|| Arc::new(cache)).clone())
    }

    /// peek is the snapshot last built, None when none.
    pub(crate) fn peek(&self) -> Option<Arc<Snapshot>> {
        self.cache.lock().as_ref().and_then(|c| c.peek())
    }

    /// Enrich completes a principal from the current snapshot; when the graph cannot be read the principal is
    /// returned as it is.
    pub async fn enrich(&self, ctx: &Ctx, p: &Principal) -> Principal {
        match self.snapshot_or_last(ctx).await {
            Some(s) => s.enrich(p),
            None => p.clone(),
        }
    }

    /// MayAccessProject reports whether a principal may work on a project, from the current snapshot
    /// (`Snapshot::may_access_project`).
    pub async fn may_access_project(
        &self,
        ctx: &Ctx,
        p: &Principal,
        project: &str,
    ) -> Result<bool, SnapshotError> {
        Ok(self.snapshot(ctx).await?.may_access_project(p, project))
    }

    /// SubProjects returns a project and the projects below it, from the current snapshot (`Snapshot::sub_projects`).
    pub async fn sub_projects(
        &self,
        ctx: &Ctx,
        project: &str,
    ) -> Result<Vec<String>, SnapshotError> {
        Ok(self.snapshot(ctx).await?.sub_projects(project))
    }
}

/// Authorizer decides access requests from the Policy nodes of the graph, for the principal completed with its User
/// node. The floor (administrators may do everything) is checked first and holds whatever the policies say. While the
/// graph holds no policy, or cannot be read, the compiled-in default policies apply.
pub struct Authorizer {
    pub dir: Arc<Directory>,

    stored: parking_lot::Mutex<Option<(BaselineID, Arc<Casbin>)>>,
    defaults: Arc<Casbin>,
    floor: Arc<Casbin>,
}

impl std::fmt::Debug for Authorizer {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Authorizer").finish_non_exhaustive()
    }
}

impl Authorizer {
    /// NewAuthorizer returns an authorizer over a directory.
    pub fn new(dir: Arc<Directory>) -> Result<Authorizer, goap_authz::Error> {
        Ok(Authorizer {
            dir,
            stored: parking_lot::Mutex::new(None),
            defaults: Arc::new(Casbin::new_with(&DEFAULT_POLICIES)?),
            floor: Arc::new(Casbin::new_with(&FLOOR_POLICIES)?),
        })
    }

    /// Floor is the authorizer of the compiled-in floor alone (for the principal completed with its User node); it
    /// guards the changes to policies and users.
    pub fn floor(&self) -> FloorAuthorizer {
        FloorAuthorizer {
            dir: self.dir.clone(),
            floor: self.floor.clone(),
        }
    }

    async fn enforcer(&self, ctx: &Ctx) -> (Arc<Casbin>, Option<Arc<Snapshot>>) {
        let snap = self.dir.snapshot_or_last(ctx).await;
        let Some(s) = snap.as_ref().filter(|s| !s.policies.is_empty()) else {
            return (self.defaults.clone(), snap);
        };
        let mut stored = self.stored.lock();
        if let Some((b, c)) = stored.as_ref()
            && *b == s.baseline
        {
            return (c.clone(), snap);
        }
        match Casbin::new_with(&s.policies) {
            Ok(c) => {
                let c = Arc::new(c);
                *stored = Some((s.baseline.clone(), c.clone()));
                (c, snap)
            }
            Err(_) => (self.defaults.clone(), snap),
        }
    }
}

#[async_trait]
impl goap_authz::Authorizer for Authorizer {
    async fn authorize(&self, ctx: &Ctx, req: &Request) -> Result<bool, goap_authz::Error> {
        if req.subject.anonymous() {
            return Ok(false);
        }
        let mut req = req.clone();
        let (c, snap) = self.enforcer(ctx).await;
        if let Some(snap) = snap {
            // Enrich already merges the subject's platform-wide roles (ADR 0046, 0047); only the roles held on the
            // resource's project remain to add here (ADR 0043).
            req.subject = snap.enrich(&req.subject);
            if !req.resource.org.is_empty() && req.resource.org_chain.is_empty() {
                req.resource.org_chain = snap.chain(&req.resource.org);
            }
            let mut project = req.resource.project_id.clone();
            if project.is_empty() {
                project = snap.structures.project().root;
            }
            let project_chain = snap.project_chain(&project);
            let subject_chain = snap.subject_chain(&req.subject);
            let mut roles = req.subject.roles.clone();
            for r in snap.project_roles(&subject_chain, &project_chain) {
                push_unique(&mut roles, &r);
            }
            req.subject.roles = roles;
        }
        let ok = self.floor.enforce(&req)?;
        if ok {
            return Ok(true);
        }
        c.enforce(&req)
    }
}

/// The authorizer of the compiled-in floor alone ([`Authorizer::floor`]).
#[derive(Debug, Clone)]
pub struct FloorAuthorizer {
    dir: Arc<Directory>,
    floor: Arc<Casbin>,
}

#[async_trait]
impl goap_authz::Authorizer for FloorAuthorizer {
    async fn authorize(&self, ctx: &Ctx, req: &Request) -> Result<bool, goap_authz::Error> {
        if req.subject.anonymous() {
            return Ok(false);
        }
        let mut req = req.clone();
        req.subject = self.dir.enrich(ctx, &req.subject).await;
        self.floor.enforce(&req)
    }
}
