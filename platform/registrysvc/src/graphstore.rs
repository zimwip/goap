//! Port of `graphstore.go`: the methodology versions kept as nodes of the graph (ADR 0023, 0120).

use std::collections::{BTreeMap, HashMap, HashSet};
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Change, ChangeID, ChangeImpact, Link, LinkID, MAIN_BRANCH, Node, NodeID,
    Structures, gotime, json_equal,
};
use goap_graphapi::{
    self as graphapi, Commit, CommitResult, Error as GraphError, LinkEdit, NewChange, NodeCreate,
    NodeEdit,
};
use goap_graphsnap::Cache;
use goap_methodology::{ELEMENT_VERSION, Methodology, RefLink, RefTarget, decode_refs};
use serde_json::{Map, Value};

use crate::defs::{
    DefEl, KIND_METHOD, KIND_METHOD_STEP, KIND_PROCESS, KIND_STEP, decode_into, encode_methodology,
    kind_of_type, node_type_of, position,
};
use crate::error::{Error, Result};
use crate::refs::{ElementGraph, build_element_graph, same_ref};
use crate::steps::{step_children, step_json};
use crate::store::{
    Record, STATUS_DRAFT, STATUS_PUBLISHED, Status, Store, after, key, sort_records,
};

/// TypeMethodologyVersion is the type of the header node of a methodology version (built-in meta-domain, ADR 0023):
/// its scalar fields, status and timestamps; one node per element of the definition (see defs.rs), tied by
/// "defines" links.
pub const TYPE_METHODOLOGY_VERSION: &str = "methodology@MethodologyVersion";

/// linkDefines is the link type that ties a methodology version to the elements of its definition.
pub(crate) const LINK_DEFINES: &str = "methodology@defines";

/// linkSubActivity ties an Activity (a process, method or step) to the sub-activities (steps) it is composed from.
pub const LINK_SUB_ACTIVITY: &str = "methodology@sub_activity";

/// statusDeleted marks a deleted draft: a node key is never freed on a versioned graph, so the node stays and a later
/// Save of the same version revives it. The stores treat it as absent.
pub(crate) const STATUS_DELETED: Status = Status::from_static("deleted");

/// NamespaceMethodology is the namespace of the methodology versions and their elements.
pub const NAMESPACE_METHODOLOGY: &str = goap_domain::NAMESPACE_METHODOLOGY;

/// MethodologyVersionKey is the key of the node of a methodology version.
pub fn methodology_version_key(name: &str, version: &str) -> String {
    format!("MV:{}", key(name, version))
}

type GResult<T> = std::result::Result<T, GraphError>;

/// StoreGraph is what the store needs from the graph (the graph itself and the graph service client implement it).
#[async_trait]
pub trait StoreGraph: Send + Sync {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> GResult<Baseline>;
    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> GResult<(Vec<Node>, Vec<Link>)>;
    /// Commit runs a change of node edits (ADR 0024).
    async fn commit(&self, ctx: &Ctx, input: Commit) -> GResult<CommitResult>;
    /// CreateChange and ImpactNodeCreate let a producer open a change on a namespace of its own and propose a
    /// pending (not auto-accepted) created node in it (the alias stubs, aliasstubs.rs); Changes lists every change,
    /// for the pending-stub dedup check.
    async fn create_change(&self, ctx: &Ctx, input: NewChange) -> GResult<Change>;
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeCreate,
    ) -> GResult<ChangeImpact>;
    async fn changes(&self, ctx: &Ctx) -> GResult<Vec<Change>>;
    /// Structures give the root project, where the registry's own changes act (ADR 0091).
    async fn structures(&self, ctx: &Ctx) -> GResult<Structures>;
    /// A baseline with its suspect links at once (the graph service client: one call); None: not supported.
    async fn baseline_graph_with_suspects(
        &self,
        _ctx: &Ctx,
        _id: &BaselineID,
    ) -> Option<GResult<(Vec<Node>, Vec<Link>, Vec<Link>)>> {
        None
    }
    /// The suspect links of a baseline; None: not supported.
    async fn suspect_links(&self, _ctx: &Ctx, _id: &BaselineID) -> Option<GResult<Vec<Link>>> {
        None
    }
}

#[async_trait]
impl StoreGraph for goap_graphclient::Client {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> GResult<Baseline> {
        graphapi::Port::branch_head(self, ctx, namespace, name).await
    }
    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> GResult<(Vec<Node>, Vec<Link>)> {
        graphapi::Port::baseline_graph(self, ctx, id).await
    }
    async fn commit(&self, ctx: &Ctx, input: Commit) -> GResult<CommitResult> {
        goap_graphclient::Client::commit(self, ctx, input).await
    }
    async fn create_change(&self, ctx: &Ctx, input: NewChange) -> GResult<Change> {
        graphapi::Port::create_change(self, ctx, input).await
    }
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeCreate,
    ) -> GResult<ChangeImpact> {
        graphapi::Port::impact_node_create(self, ctx, id, input).await
    }
    async fn changes(&self, ctx: &Ctx) -> GResult<Vec<Change>> {
        goap_graphclient::Client::changes(self, ctx).await
    }
    async fn structures(&self, ctx: &Ctx) -> GResult<Structures> {
        graphapi::Port::structures(self, ctx).await
    }
    async fn baseline_graph_with_suspects(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Option<GResult<(Vec<Node>, Vec<Link>, Vec<Link>)>> {
        Some(goap_graphclient::Client::baseline_graph_with_suspects(self, ctx, id).await)
    }
    async fn suspect_links(&self, ctx: &Ctx, id: &BaselineID) -> Option<GResult<Vec<Link>>> {
        Some(goap_graphclient::Client::suspect_links(self, ctx, id).await)
    }
}

/// A baseline read with its suspect links too (withSuspects): a link to an element is a link to the version the
/// element had when the reference was written (ADR 0003), and editing the element (a new version of its node) leaves
/// the references to it in place, suspect. They are references all the same (ADR 0120).
pub(crate) async fn baseline_with_suspects(
    g: &dyn StoreGraph,
    ctx: &Ctx,
    id: &BaselineID,
) -> GResult<(Vec<Node>, Vec<Link>)> {
    if let Some(r) = g.baseline_graph_with_suspects(ctx, id).await {
        let (nodes, mut links, sus) = r?;
        links.extend(sus);
        return Ok((nodes, links));
    }
    let (nodes, mut links) = g.baseline_graph(ctx, id).await?;
    if let Some(sus) = g.suspect_links(ctx, id).await {
        links.extend(sus?);
    }
    Ok((nodes, links))
}

/// rootProject is the project the registry's own changes (methodology and domain seeds, alias stubs) act in.
pub(crate) async fn root_project(ctx: &Ctx, g: &dyn StoreGraph) -> GResult<String> {
    Ok(g.structures(ctx).await?.project().root)
}

/// The store graph as graphsnap reads it: with the suspect links.
struct WithSuspects(Arc<dyn StoreGraph>);

#[async_trait]
impl goap_graphsnap::Graph for WithSuspects {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> GResult<Baseline> {
        self.0.branch_head(ctx, namespace, name).await
    }
    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> GResult<(Vec<Node>, Vec<Link>)> {
        baseline_with_suspects(self.0.as_ref(), ctx, id).await
    }
}

/// A link with the key of its target.
#[derive(Debug, Clone)]
pub(crate) struct KeyedLink {
    pub link: Link,
    pub to_key: String,
}

/// A decoded version with the nodes it comes from: the header and every element node (removed ones too, so that an
/// element deleted from a draft and added again is revived, not created twice), and the outgoing links of each, with
/// the key of their target.
pub(crate) struct Stored {
    pub node: Node,
    pub rec: Record,
    /// by key
    pub children: BTreeMap<String, Node>,
    pub out: HashMap<NodeID, Vec<KeyedLink>>,
}

/// The versions of a baseline, decoded once per head of main.
#[derive(Default)]
pub(crate) struct Defs {
    pub methodologies: HashMap<String, Stored>,
    pub problems: Vec<String>,
}

const META_KEYS: &[&str] = &[
    "status",
    "createdAt",
    "updatedAt",
    "publishedAt",
    "updatedBy",
];

fn removed(n: &Node) -> bool {
    n.properties
        .get("removed")
        .and_then(Value::as_bool)
        .unwrap_or(false)
}

pub(crate) fn build_defs(nodes: &[Node], links: &[Link]) -> Defs {
    let mut d = Defs::default();
    // header key -> element nodes
    let mut children: HashMap<String, BTreeMap<String, Node>> = HashMap::new();
    let mut by_id: HashMap<NodeID, &Node> = HashMap::new();
    for n in nodes {
        if n.namespace != NAMESPACE_METHODOLOGY {
            continue;
        }
        by_id.insert(n.id.clone(), n);
        if kind_of_type(&n.r#type).is_empty() {
            continue;
        }
        let parent = n.key.split('/').next().unwrap_or_default().to_string();
        children
            .entry(parent)
            .or_default()
            .insert(n.key.clone(), n.clone());
    }
    let mut out: HashMap<NodeID, Vec<KeyedLink>> = HashMap::new();
    for l in links {
        let (Some(from), Some(to)) = (by_id.get(&l.from.id), by_id.get(&l.to.id)) else {
            continue;
        };
        if from.version != l.from.version {
            continue;
        }
        out.entry(l.from.id.clone()).or_default().push(KeyedLink {
            link: l.clone(),
            to_key: to.key.clone(),
        });
    }
    for ls in out.values_mut() {
        ls.sort_by(|a, b| a.link.id.cmp(&b.link.id));
    }
    for n in nodes {
        if n.namespace != NAMESPACE_METHODOLOGY || n.r#type != TYPE_METHODOLOGY_VERSION {
            continue;
        }
        let els = children.get(&n.key).cloned().unwrap_or_default();
        let rec = match decode_version(n, &els, &out, &by_id) {
            Ok(r) => r,
            Err(e) => {
                d.problems.push(format!("{}: {e}", n.key));
                continue;
            }
        };
        let mut mine: HashMap<NodeID, Vec<KeyedLink>> = HashMap::new();
        mine.insert(n.id.clone(), out.get(&n.id).cloned().unwrap_or_default());
        for e in els.values() {
            mine.insert(e.id.clone(), out.get(&e.id).cloned().unwrap_or_default());
        }
        d.methodologies.insert(
            key(&rec.methodology.name, &rec.methodology.version),
            Stored {
                node: n.clone(),
                rec,
                children: els,
                out: mine,
            },
        );
    }
    d
}

fn str_prop<'a>(p: &'a Map<String, Value>, k: &str) -> &'a str {
    p.get(k).and_then(Value::as_str).unwrap_or_default()
}

/// decodeVersion assembles a definition from its header node, its live element nodes and their links: the references
/// back in the properties (methodology::decode_refs), the steps of a process or a method from its sub_activity links.
fn decode_version(
    n: &Node,
    elements: &BTreeMap<String, Node>,
    out: &HashMap<NodeID, Vec<KeyedLink>>,
    by_id: &HashMap<NodeID, &Node>,
) -> Result<Record> {
    let mut props = n.properties.clone();
    let mut rec = Record {
        status: Status::new(str_prop(&props, "status")),
        updated_by: str_prop(&props, "updatedBy").to_string(),
        ..Record::default()
    };
    for (k, dst) in [
        ("createdAt", &mut rec.created_at),
        ("updatedAt", &mut rec.updated_at),
        ("publishedAt", &mut rec.published_at),
    ] {
        let s = str_prop(&props, k);
        if !s.is_empty() {
            *dst = Some(
                gotime::parse(s)
                    .ok_or_else(|| Error::Other(format!("{k}: cannot parse {s:?} as a time")))?,
            );
        }
    }
    for k in META_KEYS {
        props.remove(*k);
    }
    let refs = |id: &NodeID| -> Vec<RefTarget> {
        out.get(id)
            .into_iter()
            .flatten()
            .map(|l| {
                let to = by_id.get(&l.link.to.id);
                RefTarget {
                    r#type: l.link.r#type.clone(),
                    props: l.link.properties.clone(),
                    kind: to
                        .map(|t| kind_of_type(&t.r#type))
                        .unwrap_or("")
                        .to_string(),
                    name: to
                        .map(|t| str_prop(&t.properties, "name").to_string())
                        .unwrap_or_default(),
                }
            })
            .collect()
    };
    let props = decode_refs(ELEMENT_VERSION, &props, &refs(&n.id));
    fn steps_of(
        id: &NodeID,
        out: &HashMap<NodeID, Vec<KeyedLink>>,
        by_id: &HashMap<NodeID, &Node>,
        refs: &dyn Fn(&NodeID) -> Vec<RefTarget>,
    ) -> Vec<Value> {
        let mut kids: Vec<&Node> = Vec::new();
        for l in out.get(id).into_iter().flatten() {
            if l.link.r#type != LINK_SUB_ACTIVITY {
                continue;
            }
            if let Some(c) = by_id.get(&l.link.to.id)
                && !removed(c)
            {
                kids.push(c);
            }
        }
        kids.sort_by(|a, b| position(&a.properties).total_cmp(&position(&b.properties)));
        let mut steps = Vec::new();
        for c in kids {
            let mut p = step_json(&decode_refs(
                kind_of_type(&c.r#type),
                &c.properties,
                &refs(&c.id),
            ));
            let sub = steps_of(&c.id, out, by_id, refs);
            if !sub.is_empty() {
                p.insert("steps".into(), Value::Array(sub));
            }
            steps.push(Value::Object(p));
        }
        steps
    }
    let mut els = Vec::new();
    let mut kinds = Vec::new();
    for e in elements.values() {
        let kind = kind_of_type(&e.r#type);
        if removed(e) || kind == KIND_STEP || kind == KIND_METHOD_STEP {
            continue; // a step is read through its process or method
        }
        let mut p = decode_refs(kind, &e.properties, &refs(&e.id));
        if kind == KIND_PROCESS || kind == KIND_METHOD {
            let steps = steps_of(&e.id, out, by_id, &refs);
            if !steps.is_empty() {
                p.insert("steps".into(), Value::Array(steps));
            }
        }
        els.push(p);
        kinds.push(kind.to_string());
    }
    rec.methodology = decode_into(&props, els, &kinds)?;
    Ok(rec)
}

fn ts(t: &Option<DateTime<Utc>>) -> Option<Value> {
    t.as_ref().map(|t| Value::String(gotime::format(t)))
}

/// headerProps are the properties of a version's header node: the fields of the definition and the record's own.
pub(crate) fn header_props(
    def: &Map<String, Value>,
    st: &Status,
    created: &Option<DateTime<Utc>>,
    updated: &Option<DateTime<Utc>>,
    published: &Option<DateTime<Utc>>,
    by: &str,
) -> Map<String, Value> {
    let mut m = def.clone();
    m.insert("status".into(), Value::String(st.to_string()));
    for (k, t) in [
        ("createdAt", created),
        ("updatedAt", updated),
        ("publishedAt", published),
    ] {
        if let Some(v) = ts(t) {
            m.insert(k.into(), v);
        }
    }
    if !by.is_empty() {
        m.insert("updatedBy".into(), Value::String(by.to_string()));
    }
    m
}

/// patch returns the update of stored properties to reach want (removed keys set to null), empty when they match.
pub(crate) fn patch(have: &Map<String, Value>, want: &Map<String, Value>) -> Map<String, Value> {
    let mut out = Map::new();
    for (k, v) in want {
        if !json_equal(have.get(k).unwrap_or(&Value::Null), v) {
            out.insert(k.clone(), v.clone());
        }
    }
    for (k, v) in have {
        if !want.contains_key(k) && !v.is_null() {
            out.insert(k.clone(), Value::Null);
        }
    }
    out
}

/// The stored version a save edits: its header, its element nodes by key and their outgoing links.
pub(crate) type OldVersion<'a> = (
    &'a Node,
    &'a BTreeMap<String, Node>,
    &'a HashMap<NodeID, Vec<KeyedLink>>,
);

/// versionEdits reconciles the nodes of a version with a definition (an ElementGraph): it creates or updates the
/// header, creates, updates and, for the elements that left the definition, marks removed the element nodes (their
/// links dropped), ties new elements to the header (defines) and brings the links of every node to the references of
/// the definition (ADR 0120): a link the definition no longer makes is removed, one it makes is added, one whose
/// attributes changed is replaced. old is the stored version, None for a new one.
pub(crate) fn version_edits(
    hkey: &str,
    h_type: &str,
    eg: &ElementGraph,
    old: Option<OldVersion<'_>>,
) -> Vec<NodeEdit> {
    let empty_children = BTreeMap::new();
    let empty_out = HashMap::new();
    let (old_node, old_children, old_out) = match old {
        Some((n, c, o)) => (Some(n), c, o),
        None => (None, &empty_children, &empty_out),
    };
    let prefix = format!("{hkey}/");
    // linkEdits diffs the links of a stored node (only the types managed count) with the wanted ones
    let link_edits = |id: Option<&NodeID>,
                      want: &[RefLink],
                      managed: &dyn Fn(&str) -> bool|
     -> (Vec<LinkEdit>, Vec<LinkID>) {
        let mut used = vec![false; want.len()];
        let mut remove = Vec::new();
        for l in id.and_then(|id| old_out.get(id)).into_iter().flatten() {
            if !managed(&l.link.r#type) {
                continue;
            }
            let to = l.to_key.strip_prefix(&prefix).unwrap_or(&l.to_key);
            let mut kept = false;
            for (i, w) in want.iter().enumerate() {
                if !used[i] && same_ref(w, &l.link.r#type, to, &l.link.properties) {
                    used[i] = true;
                    kept = true;
                    break;
                }
            }
            if !kept {
                remove.push(l.link.id.clone());
            }
        }
        let mut add = Vec::new();
        for (i, w) in want.iter().enumerate() {
            if !used[i] {
                add.push(LinkEdit {
                    r#type: w.r#type.clone(),
                    to_key: format!("{prefix}{}", w.to),
                    props: w.props.clone(),
                    ..LinkEdit::default()
                });
            }
        }
        (add, remove)
    };
    let all = |_: &str| true;
    let mut head = match old_node {
        Some(n) => {
            let (links, remove_links) =
                link_edits(Some(&n.id), &eg.header_links, &|t: &str| t != LINK_DEFINES);
            NodeEdit {
                pre: Some(n.r#ref()),
                props: patch(&n.properties, &eg.header),
                links,
                remove_links,
                ..NodeEdit::default()
            }
        }
        None => NodeEdit {
            key: hkey.to_string(),
            r#type: h_type.to_string(),
            props: eg.header.clone(),
            links: link_edits(None, &eg.header_links, &all).0,
            ..NodeEdit::default()
        },
    };
    let mut edits = Vec::new();
    let mut want: HashSet<String> = HashSet::new();
    let no_links: Vec<RefLink> = Vec::new();
    for e in &eg.els {
        let rel = format!("{}/{}", e.kind, e.name);
        let k = format!("{prefix}{rel}");
        want.insert(k.clone());
        let wanted = eg.links.get(&rel).unwrap_or(&no_links);
        if let Some(n) = old_children.get(&k) {
            let p = patch(&n.properties, &e.props);
            let (add, remove) = link_edits(Some(&n.id), wanted, &all);
            if !p.is_empty() || !add.is_empty() || !remove.is_empty() {
                edits.push(NodeEdit {
                    pre: Some(n.r#ref()),
                    props: p,
                    links: add,
                    remove_links: remove,
                    ..NodeEdit::default()
                });
            }
            continue;
        }
        let (add, _) = link_edits(None, wanted, &all);
        edits.push(NodeEdit {
            key: k.clone(),
            r#type: node_type_of(&e.kind).to_string(),
            props: e.props.clone(),
            links: add,
            ..NodeEdit::default()
        });
        head.links.push(LinkEdit {
            r#type: LINK_DEFINES.to_string(),
            to_key: k,
            ..LinkEdit::default()
        });
    }
    for (k, n) in old_children {
        if !want.contains(k) && !removed(n) {
            let mut ed = update(
                n,
                Map::from_iter([("removed".to_string(), Value::Bool(true))]),
            );
            ed.remove_links = link_edits(Some(&n.id), &[], &all).1;
            edits.push(ed);
        }
    }
    if head.pre.is_some()
        && head.props.is_empty()
        && head.links.is_empty()
        && head.remove_links.is_empty()
    {
        return edits;
    }
    let mut all_edits = vec![head];
    all_edits.extend(edits);
    all_edits
}

fn update(n: &Node, props: Map<String, Value>) -> NodeEdit {
    NodeEdit {
        pre: Some(n.r#ref()),
        props,
        ..NodeEdit::default()
    }
}

/// statusPatch is the update of a header node when a version changes status.
fn status_patch(st: &Status, at: &DateTime<Utc>) -> Map<String, Value> {
    let mut p = Map::new();
    p.insert("status".into(), Value::String(st.to_string()));
    p.insert("updatedAt".into(), Value::String(gotime::format(at)));
    if *st == STATUS_PUBLISHED {
        p.insert("publishedAt".into(), Value::String(gotime::format(at)));
    }
    p
}

fn mark_deleted(n: &Node) -> NodeEdit {
    update(
        n,
        Map::from_iter([(
            "status".to_string(),
            Value::String(STATUS_DELETED.to_string()),
        )]),
    )
}

/// encodeVersion is a definition as the graph holds it: the header, an element per declared element and per step,
/// and the links of their references (ADR 0120).
pub(crate) fn encode_version(
    m: &Methodology,
    st: &Status,
    created: &Option<DateTime<Utc>>,
    updated: &Option<DateTime<Utc>>,
    published: &Option<DateTime<Utc>>,
    by: &str,
) -> Result<ElementGraph> {
    let (header, mut els) = encode_methodology(m)?;
    let (step_els, sub_activity): (Vec<DefEl>, _) = step_children(&mut els);
    els.extend(step_els);
    Ok(build_element_graph(
        &header_props(&header, st, created, updated, published, by),
        &els,
        &sub_activity,
    ))
}

/// GraphStore keeps methodology versions as nodes of the graph, changed through changes applied on main: they are
/// versioned, journaled and reviewable like any node (each element has its own history). It implements Store; the
/// domains are in the domain registry (ADR 0125 §3).
pub struct GraphStore {
    pub graph: Arc<dyn StoreGraph>,
    cache: Cache<Arc<Defs>>,
}

impl GraphStore {
    /// NewGraphStore returns a store over a graph (the graph itself, or a client of the graph service).
    pub fn new(g: Arc<dyn StoreGraph>) -> GraphStore {
        let cache = Cache::new(
            Arc::new(WithSuspects(g.clone())),
            NAMESPACE_METHODOLOGY,
            |_: &BaselineID, nodes: &[Node], links: &[Link]| Arc::new(build_defs(nodes, links)),
        );
        GraphStore { graph: g, cache }
    }

    /// read returns the versions at the head of main, looked at now (the last view read when the graph cannot be
    /// read again).
    pub(crate) async fn read(&self, ctx: &Ctx) -> Result<Arc<Defs>> {
        match self.cache.fresh(ctx).await {
            Ok((d, _)) => Ok(d),
            Err(e) => match e.last {
                Some((d, _)) => Ok(d),
                None => Err(Error::Graph(e.error)),
            },
        }
    }

    /// The problems of the versions that could not be decoded at the head of main.
    pub async fn problems(&self, ctx: &Ctx) -> Result<Vec<String>> {
        Ok(self.read(ctx).await?.problems.clone())
    }

    /// commit applies the edits build makes from the current versions as one change on main; when main moved
    /// meanwhile, it reads the new head and builds them again (up to three retries).
    async fn commit(
        &self,
        ctx: &Ctx,
        ns: &str,
        title: &str,
        build: impl Fn(&Defs) -> Result<Vec<NodeEdit>> + Send + Sync,
    ) -> Result<()> {
        let mut attempt = 0;
        loop {
            let d = self.read(ctx).await?;
            let edits = build(&d)?;
            if edits.is_empty() {
                return Ok(());
            }
            let head = self.graph.branch_head(ctx, ns, MAIN_BRANCH).await?;
            let root = root_project(ctx, self.graph.as_ref()).await?;
            let res = self
                .graph
                .commit(
                    ctx,
                    Commit {
                        namespace: ns.to_string(),
                        title: title.to_string(),
                        intent: title.to_string(),
                        baseline: head.id,
                        by: "registrysvc".into(),
                        baseline_name: title.to_string(),
                        edits,
                        project_id: root,
                        ..Commit::default()
                    },
                )
                .await;
            match res {
                Ok(_) => return Ok(()),
                Err(e) if e.is_conflict() && attempt < 3 => {
                    attempt += 1; // main moved: read again and rebuild
                }
                Err(e) if e.is_nothing_to_commit() => return Ok(()), // the graph holds the definition already
                Err(e) => return Err(Error::Graph(e)),
            }
        }
    }
}

#[async_trait]
impl Store for GraphStore {
    async fn save(&self, ctx: &Ctx, r: Record) -> Result<()> {
        let m = &r.methodology;
        let k = key(&m.name, &m.version);
        self.commit(
            ctx,
            NAMESPACE_METHODOLOGY,
            &format!("Methodology {k}"),
            |d: &Defs| {
                let old = d.methodologies.get(&k);
                let revive = old.is_some_and(|o| o.rec.status == STATUS_DELETED);
                if let Some(o) = old
                    && !revive
                    && o.rec.status != STATUS_DRAFT
                {
                    return Err(Error::immutable(&k));
                }
                let created = match old {
                    Some(o) if !revive => o.rec.created_at,
                    _ => r.updated_at,
                };
                let eg = encode_version(
                    m,
                    &r.status,
                    &created,
                    &r.updated_at,
                    &r.published_at,
                    &r.updated_by,
                )?;
                Ok(version_edits(
                    &methodology_version_key(&m.name, &m.version),
                    TYPE_METHODOLOGY_VERSION,
                    &eg,
                    old.map(|o| (&o.node, &o.children, &o.out)),
                ))
            },
        )
        .await
    }

    async fn get(&self, ctx: &Ctx, name: &str, version: &str) -> Result<Record> {
        let d = self.read(ctx).await?;
        if !version.is_empty() {
            return match d.methodologies.get(&key(name, version)) {
                Some(r) if r.rec.status != STATUS_DELETED => Ok(r.rec.clone()),
                _ => Err(Error::not_found(key(name, version))),
            };
        }
        let mut best: Option<&Record> = None;
        for r in d.methodologies.values() {
            if r.rec.methodology.name == name
                && r.rec.status == STATUS_PUBLISHED
                && best.is_none_or(|b| after(&r.rec.published_at, &b.published_at))
            {
                best = Some(&r.rec);
            }
        }
        best.cloned()
            .ok_or_else(|| Error::not_found(format!("{name} (published)")))
    }

    async fn list(&self, ctx: &Ctx) -> Result<Vec<Record>> {
        let d = self.read(ctx).await?;
        let mut keys: Vec<&String> = d.methodologies.keys().collect();
        keys.sort();
        let mut out: Vec<Record> = keys
            .into_iter()
            .map(|k| &d.methodologies[k])
            .filter(|r| r.rec.status != STATUS_DELETED)
            .map(|r| r.rec.clone())
            .collect();
        sort_records(&mut out);
        Ok(out)
    }

    async fn set_status(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
        st: Status,
        at: DateTime<Utc>,
    ) -> Result<()> {
        let k = key(name, version);
        self.commit(
            ctx,
            NAMESPACE_METHODOLOGY,
            &format!("Methodology {k} {st}"),
            |d: &Defs| match d.methodologies.get(&k) {
                Some(o) if o.rec.status != STATUS_DELETED => {
                    Ok(vec![update(&o.node, status_patch(&st, &at))])
                }
                _ => Err(Error::not_found(&k)),
            },
        )
        .await
    }

    async fn delete(&self, ctx: &Ctx, name: &str, version: &str) -> Result<()> {
        let k = key(name, version);
        self.commit(
            ctx,
            NAMESPACE_METHODOLOGY,
            &format!("Delete methodology {k}"),
            |d: &Defs| match d.methodologies.get(&k) {
                Some(o) if o.rec.status != STATUS_DELETED => {
                    if o.rec.status != STATUS_DRAFT {
                        return Err(Error::immutable(&k));
                    }
                    Ok(vec![mark_deleted(&o.node)])
                }
                _ => Err(Error::not_found(&k)),
            },
        )
        .await
    }

    fn graph(&self) -> Option<Arc<dyn StoreGraph>> {
        Some(self.graph.clone())
    }
}
