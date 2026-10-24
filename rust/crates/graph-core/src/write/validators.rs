//! The validators the platform plugs on the commits of the graph (Go's `Graph.Validators`, ADR 0048, as
//! `platform/cmd/graph` wires them): the floor of administrators (`access.AdminFloorValidator`), the platform
//! Assignments carrying no project flag (`access.ProjectScopeValidator`), the owners of the projects
//! (`access.ProjectOwnerValidator`), the protected aliases (`llmcfg.ProtectedAliasValidator`) and the platform's own
//! source (`graphsvc.PlatformSourceValidator`). Each judges the nodes of its types a commit writes, on the state the
//! commit leaves; the writes served here are a platform service's, which the rights on the projects never ask.
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
        LINK_ASSIGNS_PROJECT, PLATFORM_SCOPE, STATE_RETIRED, Snapshot, TYPE_ASSIGNMENT, TYPE_PROJECT_UNIT, TYPE_USER,
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
    nodes: Vec<Node>,
    links: HashMap<NodeRef, Vec<Link>>,
}

impl Query {
    async fn read<T: ReadTx>(tx: &T, state: &HashMap<NodeId, Version>) -> Result<Query> {
        let refs: Vec<NodeRef> = state.iter().map(|(id, v)| NodeRef::new(id.clone(), *v)).collect();
        let ns = Catalog::builtin()
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
        Ok(Query { nodes, links })
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
fn access_types(with_projects: bool) -> Vec<String> {
    let cat = Catalog::builtin();
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
    Snapshot::build(&nodes, &links)
}

/// The snapshots as the commit leaves the organisation and as it found it (`snapshots`): each node written taken back
/// to the version it started from, left out when the commit creates it.
async fn snapshots<T: ReadTx>(tx: &T, q: &Query, impacted: &[&Validated<'_>]) -> Result<(Snapshot, Snapshot)> {
    let written: HashMap<&NodeId, Option<&Node>> = impacted.iter().map(|v| (&v.post.id, v.pre.as_ref())).collect();
    let types = access_types(true);
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
    Ok((Snapshot::build(&nodes, &links), Snapshot::build(&was, &was_links)))
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

    /// A platform Assignment carries no owner or admin flag, whoever writes it (the first part of
    /// `ProjectScopeValidator`); the rights on the projects are not asked of a platform service.
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
        Ok(())
    }
}

/// At least one active user administers the platform as the commit leaves it, unless there is no user at all
/// (`AdminFloorValidator`).
fn admin_floor(q: &Query) -> Result<()> {
    let snap = after(q, &access_types(false));
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
