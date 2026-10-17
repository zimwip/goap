//! A versioned graph in memory, standing in for goap-graph (not a dependency of this crate yet): node versions,
//! links between exact versions (a link to a version a node has moved on from is suspect), one baseline per commit
//! per namespace, changes and their created impacts. With a type catalogue it checks the attributes of the nodes and
//! the link types as the typed graph does (strict attributes, ADR 0055).

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, CHANGE_ACTIVE, CHANGE_APPLIED, Change, ChangeID, ChangeImpact,
    ChangeImpactID, INTENT_CREATED, Link, LinkID, MAIN_BRANCH, Node, NodeID, NodeRef,
    REVIEW_PROPOSED, Structures, Version, json_equal,
};
use goap_graphapi::{Commit, CommitResult, Error, ErrorKind, NewChange, NodeCreate};
use goap_typecat::Catalog;
use serde_json::{Map, Value};

use crate::graphstore::StoreGraph;

type R<T> = std::result::Result<T, Error>;

#[derive(Default)]
struct State {
    /// every version of every node
    versions: HashMap<(NodeID, Version), Node>,
    /// every link, from the version it belongs to
    links: Vec<Link>,
    /// the versions a baseline holds
    baselines: HashMap<BaselineID, (String, BTreeMap<NodeID, Version>)>,
    /// the head of main of each namespace
    heads: HashMap<String, BaselineID>,
    changes: Vec<Change>,
    next: u64,
}

impl State {
    fn id(&mut self, prefix: &str) -> String {
        self.next += 1;
        format!("{prefix}{}", self.next)
    }

    fn snapshot(&self, ns: &str) -> BTreeMap<NodeID, Version> {
        self.heads
            .get(ns)
            .and_then(|b| self.baselines.get(b))
            .map(|(_, m)| m.clone())
            .unwrap_or_default()
    }
}

/// A graph in memory.
#[derive(Default)]
pub struct FakeGraph {
    st: parking_lot::Mutex<State>,
    /// The catalogue the graph checks the nodes and links with (None: untyped, permissive).
    pub types: Option<Arc<Catalog>>,
}

impl FakeGraph {
    pub fn new() -> Arc<FakeGraph> {
        Arc::new(FakeGraph::default())
    }

    /// A graph checking the built-in meta-domain (strict attributes and link types).
    pub fn typed() -> Arc<FakeGraph> {
        Arc::new(FakeGraph {
            types: Some(goap_typecat::builtin()),
            ..FakeGraph::default()
        })
    }

    /// NodeByKey: the version of a node at the head of main of its namespace.
    pub fn node_by_key(&self, ns: &str, key: &str) -> Option<Node> {
        let st = self.st.lock();
        st.snapshot(ns)
            .iter()
            .filter_map(|(id, v)| st.versions.get(&(id.clone(), *v)))
            .find(|n| n.key == key)
            .cloned()
    }

    /// The baseline graph of the head of main of a namespace, suspect links included.
    pub fn head_graph(&self, ns: &str) -> (Vec<Node>, Vec<Link>) {
        let st = self.st.lock();
        let snap = st.snapshot(ns);
        let nodes = graph_nodes(&st, &snap);
        let links = st
            .links
            .iter()
            .filter(|l| {
                snap.get(&l.from.id) == Some(&l.from.version) && snap.contains_key(&l.to.id)
            })
            .cloned()
            .collect();
        (nodes, links)
    }

    fn check_props(&self, typ: &str, props: &Map<String, Value>) -> R<()> {
        let Some(cat) = &self.types else {
            return Ok(());
        };
        let (names, open) = cat.attribute_names(typ);
        if open {
            return Ok(());
        }
        for k in props.keys() {
            if !names.contains(k) {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("{typ}: property {k:?} is no attribute of the type"),
                ));
            }
        }
        Ok(())
    }

    fn check_link(&self, typ: &str) -> R<()> {
        match &self.types {
            Some(cat) if !cat.has_link_type(typ) => Err(Error::wrap(
                ErrorKind::Invalid,
                format!("unknown link type {typ}"),
            )),
            _ => Ok(()),
        }
    }
}

fn graph_nodes(st: &State, snap: &BTreeMap<NodeID, Version>) -> Vec<Node> {
    let mut nodes: Vec<Node> = snap
        .iter()
        .filter_map(|(id, v)| st.versions.get(&(id.clone(), *v)).cloned())
        .collect();
    nodes.sort_by(|a, b| a.key.cmp(&b.key));
    nodes
}

fn merged(have: &Map<String, Value>, patch: &Map<String, Value>) -> Map<String, Value> {
    let mut out = have.clone();
    for (k, v) in patch {
        if v.is_null() {
            out.remove(k);
        } else {
            out.insert(k.clone(), v.clone());
        }
    }
    out
}

#[async_trait]
impl StoreGraph for FakeGraph {
    async fn branch_head(&self, _: &Ctx, namespace: &str, _name: &str) -> R<Baseline> {
        let st = self.st.lock();
        Ok(Baseline {
            id: st.heads.get(namespace).cloned().unwrap_or_default(),
            namespace: namespace.to_string(),
            branch: MAIN_BRANCH.to_string(),
            ..Baseline::default()
        })
    }

    async fn baseline_graph(&self, _: &Ctx, id: &BaselineID) -> R<(Vec<Node>, Vec<Link>)> {
        let st = self.st.lock();
        let Some((_, snap)) = st
            .baselines
            .get(id)
            .cloned()
            .or_else(|| id.is_empty().then(|| (String::new(), BTreeMap::new())))
        else {
            return Err(Error::not_found(format!("baseline {id}")));
        };
        let links = st
            .links
            .iter()
            .filter(|l| {
                snap.get(&l.from.id) == Some(&l.from.version)
                    && snap.get(&l.to.id) == Some(&l.to.version)
            })
            .cloned()
            .collect();
        Ok((graph_nodes(&st, &snap), links))
    }

    async fn suspect_links(&self, _: &Ctx, id: &BaselineID) -> Option<R<Vec<Link>>> {
        let st = self.st.lock();
        let snap = st
            .baselines
            .get(id)
            .map(|(_, m)| m.clone())
            .unwrap_or_default();
        Some(Ok(st
            .links
            .iter()
            .filter(|l| {
                snap.get(&l.from.id) == Some(&l.from.version)
                    && snap.get(&l.to.id).is_some_and(|v| *v != l.to.version)
            })
            .cloned()
            .collect()))
    }

    async fn commit(&self, _: &Ctx, input: Commit) -> R<CommitResult> {
        if input.project_id.is_empty() {
            return Err(Error::invalid("a change names its project"));
        }
        let mut st = self.st.lock();
        let ns = input.namespace.clone();
        let head = st.heads.get(&ns).cloned().unwrap_or_default();
        if input.baseline != head {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("{ns}: main moved from {}", input.baseline),
            ));
        }
        let change_id = ChangeID::new(st.id("C"));
        let mut snap = st.snapshot(&ns);
        let mut by_key: HashMap<String, NodeID> = HashMap::new();
        for (id, v) in &snap {
            if let Some(n) = st.versions.get(&(id.clone(), *v)) {
                by_key.insert(n.key.clone(), id.clone());
            }
        }
        // the versions written: node id -> (version written, version it starts from)
        let mut written: Vec<(NodeID, Version, Option<Version>, usize)> = Vec::new();
        let mut new_links: Vec<Link> = Vec::new();
        let mut impacts = Vec::new();
        for (i, e) in input.edits.iter().enumerate() {
            match &e.pre {
                None => {
                    if by_key.contains_key(&e.key) {
                        return Err(Error::invalid(format!("{}: the key exists", e.key)));
                    }
                    let props = merged(&Map::new(), &e.props);
                    self.check_props(&e.r#type, &props)?;
                    let id = NodeID::new(st.id("N"));
                    st.versions.insert(
                        (id.clone(), 1),
                        Node {
                            id: id.clone(),
                            version: 1,
                            namespace: ns.clone(),
                            key: e.key.clone(),
                            r#type: e.r#type.clone(),
                            properties: props,
                            change_id: change_id.clone(),
                            ..Node::default()
                        },
                    );
                    by_key.insert(e.key.clone(), id.clone());
                    snap.insert(id.clone(), 1);
                    written.push((id, 1, None, i));
                    impacts.push((e.key.clone(), e.r#type.clone()));
                }
                Some(pre) => {
                    if snap.get(&pre.id) != Some(&pre.version) {
                        return Err(Error::wrap(
                            ErrorKind::Conflict,
                            format!("{}@{} is not the head", pre.id, pre.version),
                        ));
                    }
                    let old = st.versions[&(pre.id.clone(), pre.version)].clone();
                    let props = merged(&old.properties, &e.props);
                    let unchanged = json_equal(
                        &Value::Object(props.clone()),
                        &Value::Object(old.properties.clone()),
                    ) && e.links.is_empty()
                        && e.remove_links.is_empty();
                    if unchanged {
                        continue;
                    }
                    self.check_props(&old.r#type, &props)?;
                    let v = pre.version + 1;
                    let mut n = old.clone();
                    n.version = v;
                    n.parents = vec![pre.version];
                    n.properties = props;
                    n.change_id = change_id.clone();
                    st.versions.insert((pre.id.clone(), v), n);
                    // the links of the version it starts from, but those it leaves out
                    let kept: Vec<Link> = st
                        .links
                        .iter()
                        .filter(|l| l.from == *pre && !e.remove_links.contains(&l.id))
                        .cloned()
                        .collect();
                    for mut l in kept {
                        l.id = LinkID::new(st.id("L"));
                        l.from.version = v;
                        new_links.push(l);
                    }
                    snap.insert(pre.id.clone(), v);
                    written.push((pre.id.clone(), v, Some(pre.version), i));
                    impacts.push((old.key.clone(), old.r#type.clone()));
                }
            }
        }
        if written.is_empty() {
            return Err(Error::wrap(ErrorKind::NothingToCommit, "nothing to commit"));
        }
        for (id, v, _, i) in &written {
            for le in &input.edits[*i].links {
                self.check_link(&le.r#type)?;
                let to = match &le.to {
                    Some(r) => r.clone(),
                    None => {
                        let Some(tid) = by_key.get(&le.to_key) else {
                            return Err(Error::invalid(format!("no node {}", le.to_key)));
                        };
                        NodeRef::new(tid.clone(), snap[tid])
                    }
                };
                new_links.push(Link {
                    id: LinkID::new(st.id("L")),
                    r#type: le.r#type.clone(),
                    from: NodeRef::new(id.clone(), *v),
                    to,
                    properties: le.props.clone(),
                    change_id: change_id.clone(),
                });
            }
        }
        // a link to the version a node of the commit started from goes to the version the commit writes
        for l in &mut new_links {
            if let Some((_, v, Some(from), _)) = written.iter().find(|w| w.0 == l.to.id)
                && l.to.version == *from
            {
                l.to.version = *v;
            }
        }
        st.links.extend(new_links);
        let bid = BaselineID::new(st.id("B"));
        st.baselines.insert(bid.clone(), (ns.clone(), snap));
        st.heads.insert(ns.clone(), bid.clone());
        let nodes = impacts
            .into_iter()
            .map(|(key, r#type)| ChangeImpact {
                key,
                r#type,
                ..ChangeImpact::default()
            })
            .collect();
        st.changes.push(Change {
            id: change_id.clone(),
            namespace: ns.clone(),
            title: input.title.clone(),
            intent: input.intent.clone(),
            status: CHANGE_APPLIED,
            project_id: input.project_id.clone(),
            nodes,
            ..Change::default()
        });
        Ok(CommitResult {
            change: change_id,
            baseline: Baseline {
                id: bid,
                namespace: ns,
                branch: MAIN_BRANCH.to_string(),
                ..Baseline::default()
            },
        })
    }

    async fn create_change(&self, _: &Ctx, input: NewChange) -> R<Change> {
        if input.project_id.is_empty() {
            return Err(Error::invalid("a change names its project"));
        }
        let mut st = self.st.lock();
        let c = Change {
            id: ChangeID::new(st.id("C")),
            namespace: input.namespace,
            title: input.title,
            intent: input.intent,
            methodology: input.methodology,
            project_id: input.project_id,
            data: input.data,
            status: CHANGE_ACTIVE,
            ..Change::default()
        };
        st.changes.push(c.clone());
        Ok(c)
    }

    async fn impact_node_create(
        &self,
        _: &Ctx,
        id: &ChangeID,
        input: NodeCreate,
    ) -> R<ChangeImpact> {
        let mut st = self.st.lock();
        let iid = ChangeImpactID::new(st.id("I"));
        let Some(c) = st.changes.iter_mut().find(|c| c.id == *id) else {
            return Err(Error::not_found(format!("change {id}")));
        };
        let ci = ChangeImpact {
            id: iid,
            key: input.key,
            r#type: input.r#type,
            intent: INTENT_CREATED,
            rationale: input.rationale,
            review: REVIEW_PROPOSED,
            ..ChangeImpact::default()
        };
        c.nodes.push(ci.clone());
        Ok(ci)
    }

    async fn changes(&self, _: &Ctx) -> R<Vec<Change>> {
        Ok(self.st.lock().changes.clone())
    }

    async fn structures(&self, _: &Ctx) -> R<Structures> {
        Ok(goap_typecat::builtin().structures())
    }
}
