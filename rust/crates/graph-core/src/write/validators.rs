//! The validators the platform plugs on the commits of the graph (Go's `Graph.Validators`, ADR 0048, as
//! `platform/cmd/graph` wires them): the floor of administrators (`access.AdminFloorValidator`), the platform
//! Assignments carrying no project flag (`access.ProjectScopeValidator`), the owners of the projects
//! (`access.ProjectOwnerValidator`), the protected aliases (`llmcfg.ProtectedAliasValidator`) and the platform's own
//! source (`graphsvc.PlatformSourceValidator`). Each judges the nodes of its types a commit writes, on the state the
//! commit leaves; the rights on the projects are asked of a person only.
use std::collections::HashMap;

use graph_catalog::{
    Catalog,
    catalog::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT},
};
use serde_json::Value;

use super::tx::Tx;
use crate::{
    GraphError, Result,
    access::{
        LINK_ASSIGNS_PROJECT, LINK_PROJECT_PART_OF, PLATFORM_SCOPE, ROLE_ADMIN, STATE_RETIRED, Snapshot,
        TYPE_ASSIGNMENT, TYPE_PROJECT_UNIT, TYPE_USER,
    },
    model::{Link, Node, NodeId, NodeRef, Props, Version, namespace_of},
    store::{ReadTx, WriteTx},
};

/// The type of an alias of the models (`llmcfg.NodeTypeAlias`).
const TYPE_ALIAS: &str = "platform@LlmAlias";
/// The aliases the platform uses by name (`llmcfg.ProtectedAliases`).
const PROTECTED_ALIASES: [&str; 2] = ["assistant", "helper"];
/// The type of a declared source (`domain.TypeSource`).
const TYPE_SOURCE: &str = "platform@Source";
/// The key of the platform graph's own source (`domain.SourceKey(graphsvc.PlatformSource)`).
const PLATFORM_SOURCE_KEY: &str = "SRC:platform";

/// A node a commit writes and the version it started from (`graphapi.ValidatedNode`).
pub(super) struct Validated<'a> {
    pub post: &'a Node,
    pub pre: Option<Node>,
}

/// The state a commit leaves, read the way the validators ask it (Go's `txQuery`): the nodes of the namespace of the
/// organisation it holds and their outgoing links.
struct Query {
    cat: std::sync::Arc<Catalog>,
    nodes: Vec<Node>,
    links: HashMap<NodeRef, Vec<Link>>,
}

impl Query {
    async fn read<T: ReadTx>(tx: &T, state: &HashMap<NodeId, Version>) -> Result<Query> {
        let refs: Vec<NodeRef> = state.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
        let cat = crate::access::catalog_of(tx);
        let ns = cat
            .structure(STRUCTURE_ORGANISATION)
            .map(|s| s.namespace.clone())
            .unwrap_or_default();
        let nodes: Vec<Node> = tx
            .nodes_at(&refs, "")
            .await?
            .into_iter()
            .filter(|n| !n.deleted && namespace_of(&n.namespace) == ns)
            .collect();
        let held: Vec<NodeRef> = nodes.iter().map(Node::node_ref).collect();
        let mut links: HashMap<NodeRef, Vec<Link>> = HashMap::new();
        for l in tx.links_from(&held, None).await? {
            links.entry(l.from.clone()).or_default().push(l);
        }
        Ok(Query { cat, nodes, links })
    }

    fn of_types(&self, types: &[&str]) -> Vec<&Node> {
        self.nodes.iter().filter(|n| types.contains(&n.typ.as_str())).collect()
    }

    fn links_of(&self, r: &NodeRef) -> &[Link] {
        self.links.get(r).map_or(&[], Vec::as_slice)
    }
}

/// The links a version holds (`OutLinksOf`): the query's for a version of the state, else read.
async fn out_links<T: ReadTx>(tx: &T, q: &Query, r: &NodeRef) -> Result<Vec<Link>> {
    if let Some(ls) = q.links.get(r) {
        return Ok(ls.clone());
    }
    tx.links_from(std::slice::from_ref(r), None).await
}

/// The key of the node a version links to with a type, none when it holds no such link (`linkedKey`).
async fn linked_key<T: ReadTx>(tx: &T, q: &Query, n: &Node, typ: &str) -> Result<String> {
    for l in out_links(tx, q, &n.node_ref()).await? {
        if l.typ != typ {
            continue;
        }
        let Some(to) = l.to_node() else { continue };
        let Some(to) = tx.node(to).await? else {
            return Err(GraphError::not_found(format!("node {}", to.id)));
        };
        return Ok(to.key);
    }
    Ok(String::new())
}

fn flag(props: &Props, k: &str) -> bool {
    props.as_ref().and_then(|p| p.get(k)).and_then(Value::as_bool) == Some(true)
}

/// The types a structure's nodes are, the organisation's units, users, Assignments, then the projects.
fn access_types(cat: &Catalog, with_projects: bool) -> Vec<String> {
    let mut out = vec![TYPE_USER.to_string(), TYPE_ASSIGNMENT.to_string()];
    if let Some(s) = cat.structure(STRUCTURE_ORGANISATION) {
        out.push(s.typ.clone());
    }
    if with_projects && let Some(s) = cat.structure(STRUCTURE_PROJECT) {
        out.push(s.typ.clone());
    }
    out
}

/// The snapshot of the organisation as the commit leaves it, of some types.
fn after(q: &Query, types: &[String]) -> Snapshot {
    let types: Vec<&str> = types.iter().map(String::as_str).collect();
    let nodes: Vec<Node> = q.of_types(&types).into_iter().cloned().collect();
    let links: Vec<Link> = nodes.iter().flat_map(|n| q.links_of(&n.node_ref()).to_vec()).collect();
    Snapshot::build(&q.cat, &nodes, &links)
}

/// The snapshots as the commit leaves the organisation and as it found it (`snapshots`): each node written taken back
/// to the version it started from, left out when the commit creates it.
async fn snapshots<T: ReadTx>(tx: &T, q: &Query, impacted: &[&Validated<'_>]) -> Result<(Snapshot, Snapshot)> {
    let written: HashMap<&NodeId, Option<&Node>> = impacted.iter().map(|v| (&v.post.id, v.pre.as_ref())).collect();
    let types = access_types(&q.cat, true);
    let types: Vec<&str> = types.iter().map(String::as_str).collect();
    let (mut nodes, mut links, mut was, mut was_links) = (Vec::new(), Vec::new(), Vec::new(), Vec::new());
    for n in q.of_types(&types) {
        let ls = q.links_of(&n.node_ref());
        nodes.push(n.clone());
        links.extend_from_slice(ls);
        match written.get(&n.id) {
            None => {
                was.push(n.clone());
                was_links.extend_from_slice(ls);
            }
            Some(Some(pre)) => {
                was_links.extend(out_links(tx, q, &pre.node_ref()).await?);
                was.push((*pre).clone());
            }
            Some(None) => {}
        }
    }
    Ok((
        Snapshot::build(&q.cat, &nodes, &links),
        Snapshot::build(&q.cat, &was, &was_links),
    ))
}

impl<S: WriteTx> Tx<'_, S> {
    /// Runs the validators of the platform on the nodes a commit writes, each given those of its types (Go's
    /// `runNodeValidators`), on the state the commit leaves.
    pub(super) async fn run_node_validators(
        &self,
        state: impl AsyncFnOnce() -> Result<HashMap<NodeId, Version>>,
        written: &[Validated<'_>],
    ) -> Result<()> {
        let of = |types: &[&str]| -> Vec<&Validated<'_>> {
            written
                .iter()
                .filter(|v| types.contains(&v.post.typ.as_str()))
                .collect()
        };
        let floor = of(&[TYPE_USER, TYPE_ASSIGNMENT]);
        let scope = of(&[TYPE_ASSIGNMENT, TYPE_PROJECT_UNIT]);
        let owner = of(&[TYPE_ASSIGNMENT]);
        if floor.is_empty() && scope.is_empty() && owner.is_empty() {
            protected_aliases(&of(&[TYPE_ALIAS]))?;
            return platform_source(&of(&[TYPE_SOURCE]));
        }
        let q = Query::read(self.store, &state().await?).await?;
        if !floor.is_empty() {
            admin_floor(&q)?;
        }
        if !scope.is_empty() {
            self.project_scope(&q, &scope).await?;
        }
        if !owner.is_empty() {
            let (after, before) = snapshots(self.store, &q, &owner).await?;
            project_owner(&after, &before, &owner)?;
        }
        protected_aliases(&of(&[TYPE_ALIAS]))?;
        platform_source(&of(&[TYPE_SOURCE]))
    }

    /// The writes of Assignments and projects within the rights of the person committing them (`ProjectScopeValidator`,
    /// ADR 0138), judged on the state before the commit: a platform Assignment carries no owner or admin flag, whoever
    /// writes it; a platform service and a platform administrator are not asked more.
    async fn project_scope(&self, q: &Query, impacted: &[&Validated<'_>]) -> Result<()> {
        for v in impacted {
            let n = v.post;
            if n.typ == TYPE_ASSIGNMENT
                && n.state != STATE_RETIRED
                && (flag(&n.props, "owner") || flag(&n.props, "admin"))
                && linked_key(self.store, q, n, LINK_ASSIGNS_PROJECT).await?.is_empty()
            {
                return Err(GraphError::invalid(format!(
                    "assignment {}: a platform assignment carries no owner or admin flag",
                    n.key
                )));
            }
        }
        let Some(who) = &self.env.person else {
            return Ok(());
        };
        let before = match snapshots(self.store, q, impacted).await {
            Ok((_, before)) => before,
            Err(e) => {
                return Err(GraphError::forbidden(format!(
                    "the rights on the projects cannot be checked: {}",
                    e.message
                )));
            }
        };
        let subject = before.principal_chain(who);
        if who.roles.iter().any(|r| r == ROLE_ADMIN) || before.platform_roles(&subject).iter().any(|r| r == ROLE_ADMIN)
        {
            return Ok(());
        }
        let scope = Scope {
            tx: self.store,
            q,
            before: &before,
            subject: &subject,
        };
        for v in impacted {
            match v.post.typ.as_str() {
                TYPE_ASSIGNMENT => scope.assignment(v).await?,
                TYPE_PROJECT_UNIT => scope.project(v).await?,
                _ => {}
            }
        }
        Ok(())
    }
}

/// What `ProjectScopeValidator` judges a person's writes against: the state before the commit and the units of the
/// person.
struct Scope<'a, T> {
    tx: &'a T,
    q: &'a Query,
    before: &'a Snapshot,
    subject: &'a [String],
}

impl<T: ReadTx> Scope<'_, T> {
    /// The chain of a project (itself first): the state's for a project it held, else the parent the commit gives a
    /// new project, then the chain of that parent (`projectChain`).
    async fn chain(&self, mut project: String) -> Result<Vec<String>> {
        let mut chain = Vec::new();
        for _ in 0..64 {
            if self.before.has_project(&project) {
                chain.extend(self.before.project_chain(&project));
                return Ok(chain);
            }
            chain.push(project.clone());
            let Some(n) = self
                .q
                .of_types(&[TYPE_PROJECT_UNIT])
                .into_iter()
                .find(|n| n.key == project)
            else {
                return Ok(chain);
            };
            let parent = linked_key(self.tx, self.q, n, LINK_PROJECT_PART_OF).await?;
            if parent.is_empty() || parent == project || chain.contains(&parent) {
                return Ok(chain);
            }
            project = parent;
        }
        Ok(chain)
    }

    /// The right a write needs on a project: to administer it, or to own it.
    async fn need(&self, project: &str, owner: bool, what: &str) -> Result<()> {
        let chain = self.chain(project.to_string()).await?;
        let (admin, own) = self.before.project_flags(self.subject, &chain);
        if (owner && !own) || !admin {
            let right = if owner { "own" } else { "administer" };
            return Err(GraphError::forbidden(format!(
                "{what} needs to {right} the project {project}"
            )));
        }
        Ok(())
    }

    async fn assignment(&self, v: &Validated<'_>) -> Result<()> {
        let n = v.post;
        let what = format!("assignment {}", n.key);
        let post = linked_key(self.tx, self.q, n, LINK_ASSIGNS_PROJECT).await?;
        let retired = n.state == STATE_RETIRED;
        let (post_owner, post_admin) = (!retired && flag(&n.props, "owner"), !retired && flag(&n.props, "admin"));
        let pre = self.before.assignment_flags(&n.key);
        let (pre_project, pre_owner, pre_admin) = pre.clone().unwrap_or_default();
        if post.is_empty() || pre_project == PLATFORM_SCOPE {
            return Err(GraphError::forbidden(format!(
                "{what}: a platform assignment is a platform administrator's"
            )));
        }
        let flags = post_owner != pre_owner || post_admin != pre_admin;
        let mut projects = vec![post.clone()];
        if pre.is_some() && pre_project != post {
            projects.push(pre_project);
        }
        for p in projects {
            self.need(&p, flags, &what).await?;
        }
        Ok(())
    }

    async fn project(&self, v: &Validated<'_>) -> Result<()> {
        let key = &v.post.key;
        let what = format!("project {key}");
        let mut parent = linked_key(self.tx, self.q, v.post, LINK_PROJECT_PART_OF).await?;
        if &parent == key {
            parent.clear();
        }
        let Some(pre) = v.pre.as_ref().filter(|_| self.before.has_project(key)) else {
            if parent.is_empty() {
                return Err(GraphError::forbidden(format!(
                    "{what}: a root project is a platform administrator's"
                )));
            }
            return self.need(&parent, false, &format!("creating {what}")).await;
        };
        let was = self.before.project_of(key);
        if was.is_empty() {
            return Err(GraphError::forbidden(format!(
                "{what}: the root project is a platform administrator's"
            )));
        }
        if parent != was {
            if parent.is_empty() {
                return Err(GraphError::forbidden(format!(
                    "{what}: a root project is a platform administrator's"
                )));
            }
            for p in [&was, &parent] {
                self.need(p, true, &format!("moving {what}")).await?;
            }
        }
        if flag(&pre.props, "stopped") != flag(&v.post.props, "stopped") {
            return self.need(key, true, &format!("stopping or restarting {what}")).await;
        }
        self.need(key, false, &format!("editing {what}")).await
    }
}

/// At least one active user administers the platform as the commit leaves it, unless there is no user at all
/// (`AdminFloorValidator`).
fn admin_floor(q: &Query) -> Result<()> {
    let snap = after(q, &access_types(&q.cat, false));
    let mut users = false;
    for n in q.of_types(&[TYPE_USER]) {
        users = true;
        if n.state != "active" {
            continue;
        }
        let subject = n
            .props
            .as_ref()
            .and_then(|p| p.get("subject"))
            .and_then(Value::as_str)
            .unwrap_or("");
        if !subject.is_empty() && snap.administers(subject) {
            return Ok(());
        }
    }
    if !users {
        return Ok(());
    }
    Err(GraphError::invalid("this change would leave no active administrator"))
}

/// A project owned before the commit keeps an owner (`ProjectOwnerValidator`).
fn project_owner(after: &Snapshot, before: &Snapshot, impacted: &[&Validated<'_>]) -> Result<()> {
    let mut projects: Vec<String> = Vec::new();
    for v in impacted {
        if let Some((p, true)) = before.assignment_project(&v.post.key)
            && p != PLATFORM_SCOPE
            && !projects.contains(&p)
        {
            projects.push(p);
        }
    }
    for p in projects {
        if before.project_owned(&p) && after.has_project(&p) && !after.project_owned(&p) {
            return Err(GraphError::invalid(format!(
                "this change would leave the project {p} with no owner"
            )));
        }
    }
    Ok(())
}

/// An alias as its node holds it (`aliasOfProps`): none when a property it reads has another type.
fn alias_of(props: &Props) -> Option<(String, bool)> {
    let p = props.as_ref();
    let get = |k: &str| p.and_then(|p| p.get(k)).filter(|v| !v.is_null());
    let name = match get("alias") {
        None => String::new(),
        Some(Value::String(s)) => s.clone(),
        Some(_) => return None,
    };
    if get("target").is_some_and(|v| !v.is_string()) {
        return None;
    }
    let protected = match get("protected") {
        None => false,
        Some(Value::Bool(b)) => *b,
        Some(_) => return None,
    };
    Some((name, protected))
}

/// The protected aliases are never retired, unflagged or renamed, and the ones the platform uses by name are created
/// protected (`ProtectedAliasValidator`).
fn protected_aliases(impacted: &[&Validated<'_>]) -> Result<()> {
    for v in impacted {
        let Some((name, protected)) = alias_of(&v.post.props) else {
            continue;
        };
        let Some(pre) = &v.pre else {
            if PROTECTED_ALIASES.contains(&name.as_str()) && !protected {
                return Err(GraphError::invalid(format!(
                    "alias {name} is protected: it must be created with protected set"
                )));
            }
            continue;
        };
        let Some((was, true)) = alias_of(&pre.props) else {
            continue;
        };
        if v.post.state == STATE_RETIRED {
            return Err(GraphError::invalid(format!(
                "alias {was} is protected: it cannot be retired, only retargeted"
            )));
        }
        if !protected {
            return Err(GraphError::invalid(format!(
                "alias {was} is protected: its protected flag cannot be removed"
            )));
        }
        if name != was {
            return Err(GraphError::invalid(format!(
                "alias {was} is protected: it cannot be renamed to {}",
                serde_json::to_string(&name).unwrap_or_default()
            )));
        }
    }
    Ok(())
}

/// Whether the platform source a node declares is this graph (`domain.SourceOf` then the check of
/// `PlatformSourceValidator`): kind graph, no location, no namespace held, a valid declaration.
fn declares_this_graph(n: &Node) -> bool {
    let p = n.props.as_ref();
    let text = |k: &str| p.and_then(|p| p.get(k)).and_then(Value::as_str).unwrap_or("");
    let name = n.key.strip_prefix("SRC:").unwrap_or_default();
    if !text("name").is_empty() && text("name") != name {
        return false;
    }
    let held = match p.and_then(|p| p.get("namespaces")) {
        Some(Value::Array(l)) => l.iter().any(|v| v.as_str().is_some_and(|s| !s.is_empty())),
        _ => false,
    };
    text("kind") == "graph" && text("location").is_empty() && !held
}

/// The platform graph's own source is only ever created as this graph (`PlatformSourceValidator`).
fn platform_source(impacted: &[&Validated<'_>]) -> Result<()> {
    for v in impacted {
        if v.post.key != PLATFORM_SOURCE_KEY {
            continue;
        }
        let Some(pre) = &v.pre else {
            if !declares_this_graph(v.post) {
                return Err(GraphError::invalid(
                    "source platform is this graph: it is declared of kind graph, with no location and no namespace held",
                ));
            }
            continue;
        };
        let props = |p: &Props| p.clone().unwrap_or_default();
        if v.post.state != pre.state || props(&v.post.props) != props(&pre.props) {
            return Err(GraphError::invalid(
                "source platform is the platform graph: it cannot be edited or retired",
            ));
        }
    }
    Ok(())
}
