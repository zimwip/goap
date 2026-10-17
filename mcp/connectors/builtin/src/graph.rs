//! The goap-graph connector (`graph.go`).

use std::collections::HashMap;

use goap_authz::{self as authz, Authorizer, Principal};
use goap_connectorkit::{Connector, InvokeError};
use goap_ctx::Ctx;
use goap_domain::{self as domain, BaselineID, Link, Node, NodeRef};
use goap_gen::connector::v1::ConnectorInfo;
use goap_mcpbuiltin as mcpbuiltin;
use serde_json::{Map, Value};

use crate::builtin::{
    Args, Error, Op, Ports, boxed, caller, info, json, obj, op, result, schema, unknown,
};
use crate::pathmatch::path_match;

/// Graph is the goap-graph connector: reads of the versioned graph, as of the head of the main branch of a namespace
/// or of a given baseline. Nodes of a type the caller may not read are left out.
#[derive(Clone)]
pub struct Graph {
    p: Ports,
}

fn graph_ops() -> Vec<Op> {
    vec![
        op(
            "read",
            "Read a node by key: {node, out, in}",
            schema(
                &[
                    ("namespace", "string"),
                    ("key", "string"),
                    ("baseline", "string"),
                ],
                &["namespace", "key"],
            ),
        ),
        op(
            "glob",
            "List the nodes whose key matches a glob: {nodes, truncated}",
            schema(
                &[
                    ("namespace", "string"),
                    ("pattern", "string"),
                    ("type", "string"),
                    ("baseline", "string"),
                    ("limit", "integer"),
                ],
                &["namespace"],
            ),
        ),
        op(
            "grep",
            "Search the nodes whose properties match a regular expression: {nodes, truncated}",
            schema(
                &[
                    ("namespace", "string"),
                    ("pattern", "string"),
                    ("type", "string"),
                    ("property", "string"),
                    ("ignoreCase", "boolean"),
                    ("baseline", "string"),
                    ("limit", "integer"),
                ],
                &["namespace", "pattern"],
            ),
        ),
        op(
            "links",
            "List the links of a node: {links}",
            schema(
                &[
                    ("namespace", "string"),
                    ("key", "string"),
                    ("direction", "string"),
                    ("type", "string"),
                    ("baseline", "string"),
                ],
                &["namespace", "key"],
            ),
        ),
        op(
            "baselines",
            "List the baselines of a namespace: {head, baselines}",
            schema(
                &[("namespace", "string"), ("limit", "integer")],
                &["namespace"],
            ),
        ),
    ]
}

/// view is the graph of a baseline as the caller may see it.
struct View {
    baseline: BaselineID,
    nodes: Vec<Node>,
    by_ref: HashMap<NodeRef, Node>,
    links: Vec<Link>,
}

/// readable filters the nodes by the read authorization of their type (decided once per type).
struct Readable<'a> {
    ctx: &'a Ctx,
    a: Option<&'a dyn Authorizer>,
    who: &'a Principal,
    seen: HashMap<String, bool>,
}

impl Readable<'_> {
    async fn ok(&mut self, ns: &str, typ: &str) -> bool {
        let Some(a) = self.a else {
            return true;
        };
        if let Some(v) = self.seen.get(typ) {
            return *v;
        }
        let req = authz::Request {
            subject: self.who.clone(),
            action: "read".into(),
            resource: authz::Resource {
                r#type: typ.into(),
                namespace: ns.into(),
                ..Default::default()
            },
        };
        let ok = matches!(a.authorize(self.ctx, &req).await, Ok(true));
        self.seen.insert(typ.into(), ok);
        ok
    }
}

/// summary is what a list says of a node.
fn summary(n: &Node) -> Map<String, Value> {
    let mut out = obj([
        ("key", json(&n.key)),
        ("type", json(&n.r#type)),
        ("version", json(n.version)),
    ]);
    if !n.state.is_empty() {
        out.insert("state".into(), json(&n.state));
    }
    for p in ["title", "name"] {
        if let Some(t) = n.properties.get(p) {
            out.insert(p.into(), t.clone());
        }
    }
    out
}

impl View {
    fn node(&self, key: &str) -> Option<&Node> {
        self.nodes.iter().find(|n| n.key == key)
    }

    /// linkView is a link seen from one of its ends: the type and the key and type of the other end.
    fn link_view(&self, l: &Link, from: bool) -> Value {
        let other = if from { &l.to } else { &l.from };
        let mut out = obj([("type", json(&l.r#type))]);
        if let Some(n) = self.by_ref.get(other) {
            out.insert("key".into(), json(&n.key));
            out.insert("nodeType".into(), json(&n.r#type));
        } else {
            // another namespace, or a type the caller may not read
            out.insert("id".into(), json(&other.id));
        }
        if !l.properties.is_empty() {
            out.insert("properties".into(), Value::Object(l.properties.clone()));
        }
        Value::Object(out)
    }

    fn links_of(&self, n: &Node, direction: &str, typ: &str) -> (Vec<Value>, Vec<Value>) {
        let (mut out, mut inn) = (Vec::new(), Vec::new());
        let r = n.r#ref();
        for l in &self.links {
            if !typ.is_empty() && l.r#type != typ {
                continue;
            }
            if l.from == r && direction != "in" {
                out.push(self.link_view(l, true));
            }
            if l.to == r && direction != "out" {
                inn.push(self.link_view(l, false));
            }
        }
        (out, inn)
    }
}

/// text is the searchable text of a property value.
fn text(v: &Value) -> String {
    match v {
        Value::String(s) => s.clone(),
        v => v.to_string(),
    }
}

fn limited(v: &View, mut hits: Vec<Value>, limit: usize) -> Result<Map<String, Value>, Error> {
    let truncated = hits.len() > limit;
    if truncated {
        hits.truncate(limit);
    }
    Ok(obj([
        ("baseline", json(&v.baseline)),
        ("nodes", Value::Array(hits)),
        ("truncated", Value::Bool(truncated)),
    ]))
}

impl Graph {
    pub fn new(p: Ports) -> Self {
        Graph { p }
    }

    /// view reads the graph of the baseline the call names, else of the head of main of the namespace.
    async fn view(&self, ctx: &Ctx, a: Args<'_>) -> Result<View, Error> {
        let who = caller(ctx)?;
        let ns = a.required("namespace")?;
        let g = self.p.graph()?;
        let mut id = BaselineID::from(a.str("baseline"));
        if id.is_empty() {
            let head = g
                .branch_head(ctx, &ns, domain::MAIN_BRANCH)
                .await
                .map_err(|e| e.context(format!("head of main of {ns}")))?;
            id = head.id;
        }
        let (nodes, links) = g.baseline_graph(ctx, &id).await?;
        let mut r = Readable {
            ctx,
            a: self.p.authz(),
            who: &who,
            seen: HashMap::new(),
        };
        let mut v = View {
            baseline: id,
            nodes: Vec::new(),
            by_ref: HashMap::new(),
            links: Vec::new(),
        };
        for n in nodes {
            if n.namespace == ns && !n.deleted && r.ok(&n.namespace, &n.r#type).await {
                v.by_ref.insert(n.r#ref(), n.clone());
                v.nodes.push(n);
            }
        }
        v.nodes.sort_by(|x, y| x.key.cmp(&y.key));
        for l in links {
            if v.by_ref.contains_key(&l.from) || v.by_ref.contains_key(&l.to) {
                v.links.push(l);
            }
        }
        Ok(v)
    }

    /// Invoke without the boxing of the connector's error: the operation's result or its typed error.
    pub async fn call(
        &self,
        ctx: &Ctx,
        op: &str,
        raw: &Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        let a = Args(raw);
        if op == "baselines" {
            return self.baselines(ctx, a).await;
        }
        if !matches!(op, "read" | "glob" | "grep" | "links") {
            // the view is read only for an operation of the connector (Go read it first, then answered unknown)
            caller(ctx)?;
            a.required("namespace")?;
            return Err(unknown(op));
        }
        let v = self.view(ctx, a).await?;
        match op {
            "read" => {
                let key = a.required("key")?;
                let Some(n) = v.node(&key) else {
                    return Err(Error::msg(format!(
                        "no node {key} in {} (baseline {})",
                        a.str("namespace"),
                        v.baseline
                    )));
                };
                let (out, inn) = v.links_of(n, "", "");
                Ok(obj([
                    ("baseline", json(&v.baseline)),
                    (
                        "node",
                        Value::Object(obj([
                            ("key", json(&n.key)),
                            ("type", json(&n.r#type)),
                            ("version", json(n.version)),
                            ("state", json(&n.state)),
                            ("properties", Value::Object(n.properties.clone())),
                        ])),
                    ),
                    ("out", Value::Array(out)),
                    ("in", Value::Array(inn)),
                ]))
            }
            "glob" => {
                let mut pattern = a.str("pattern");
                if pattern.is_empty() {
                    pattern = "*".into();
                }
                if let Err(e) = path_match(&pattern, "") {
                    return Err(Error::msg(format!("pattern {pattern:?}: {e}")));
                }
                let typ = a.str("type");
                let hits = v
                    .nodes
                    .iter()
                    .filter(|n| {
                        path_match(&pattern, &n.key).unwrap_or(false)
                            && (typ.is_empty() || n.r#type == typ)
                    })
                    .map(|n| Value::Object(summary(n)))
                    .collect();
                limited(&v, hits, a.limit())
            }
            "grep" => {
                let mut expr = a.required("pattern")?;
                if a.boolean("ignoreCase") {
                    expr = format!("(?i){expr}");
                }
                let re =
                    regex::Regex::new(&expr).map_err(|e| Error::msg(format!("pattern: {e}")))?;
                let (typ, prop) = (a.str("type"), a.str("property"));
                let mut hits = Vec::new();
                for n in &v.nodes {
                    if !typ.is_empty() && n.r#type != typ {
                        continue;
                    }
                    let mut matches = Map::new();
                    for (name, val) in &n.properties {
                        if !prop.is_empty() && *name != prop {
                            continue;
                        }
                        let s = text(val);
                        if re.is_match(&s) {
                            matches.insert(name.clone(), Value::String(s));
                        }
                    }
                    if !matches.is_empty() {
                        let mut h = summary(n);
                        h.insert("matches".into(), Value::Object(matches));
                        hits.push(Value::Object(h));
                    }
                }
                limited(&v, hits, a.limit())
            }
            _ => {
                // links
                let key = a.required("key")?;
                let Some(n) = v.node(&key) else {
                    return Err(Error::msg(format!(
                        "no node {key} in {} (baseline {})",
                        a.str("namespace"),
                        v.baseline
                    )));
                };
                let (out, inn) = v.links_of(n, &a.str("direction"), &a.str("type"));
                Ok(obj([
                    ("baseline", json(&v.baseline)),
                    ("out", Value::Array(out)),
                    ("in", Value::Array(inn)),
                ]))
            }
        }
    }

    async fn baselines(&self, ctx: &Ctx, a: Args<'_>) -> Result<Map<String, Value>, Error> {
        caller(ctx)?;
        let ns = a.required("namespace")?;
        let g = self.p.graph()?;
        let mut bs = g.baselines(ctx, &ns).await?;
        bs.sort_by_key(|b| std::cmp::Reverse(b.created_at));
        let mut out = Map::new();
        if let Ok(head) = g.branch_head(ctx, &ns, domain::MAIN_BRANCH).await {
            out.insert("head".into(), json(&head.id));
        }
        let list: Vec<Value> = bs
            .iter()
            .take(a.limit())
            .map(|b| {
                Value::Object(obj([
                    ("id", json(&b.id)),
                    ("name", json(&b.name)),
                    ("createdAt", json(domain::gotime::format(&b.created_at))),
                ]))
            })
            .collect();
        out.insert("baselines".into(), Value::Array(list));
        result(out)
    }
}

#[goap_connect::async_trait]
impl Connector for Graph {
    fn info(&self) -> ConnectorInfo {
        info(
            mcpbuiltin::GRAPH,
            "The versioned graph of the platform, read for the caller (built in).",
            &graph_ops(),
        )
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        _config: Map<String, Value>,
        _secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        self.call(ctx, op, &args).await.map_err(boxed)
    }
}
