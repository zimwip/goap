use std::collections::{BTreeMap, HashMap};
use std::error::Error as StdError;
use std::sync::Arc;

use async_trait::async_trait;
use chrono::Utc;
use goap_ctx::Ctx;
use goap_def::Issues;
use goap_domain::{
    BaselineID, Blackboard, ChangeImpact, ChangeImpactID, ChangeItem, INTENT_CREATED,
    INTENT_MODIFIED, ItemID, Link, MAIN_BRANCH, NAMESPACE_METHODOLOGY, Node, NodeID, NodeView,
    REVIEW_ACCEPTED, TYPE_ARTIFACT,
};
use goap_dsl::{NodeOp, NodeOpLink};
use goap_engine::{
    ActionContext, ActionResult, BuiltinExecutor, Engine, Error, GraphPort, ItemInput, LogLine,
    MethodologyPort, Store, builtin,
};
use goap_methodology::{
    LINK_ATTR_AS, Methodology, RefIndex, RefKind, RefTarget, decode_refs, element_of_type,
    encode_refs, ref_index_of, ref_kinds_of,
};
use goap_observe::{Edit, Proposal, Report, Span, Thresholds};
use serde::Serialize;
use serde_json::{Map, Value, json};

/// The errors of the ports of the self-observation (the trace backend, the registry).
pub type BoxError = Box<dyn StdError + Send + Sync>;

/// TraceSource reads the spans of a trace from the OpenTelemetry backend.
#[async_trait]
pub trait TraceSource: Send + Sync {
    async fn spans(&self, ctx: &Ctx, trace_id: &str) -> Result<Vec<Span>, BoxError>;
}

/// MethodologyDrafts reads methodology definitions and saves improved versions as drafts (the registry).
#[async_trait]
pub trait MethodologyDrafts: Send + Sync {
    /// Definition returns a version (empty: latest published); None when it does not exist.
    async fn definition(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> Result<Option<Methodology>, BoxError>;
    async fn save_draft(&self, ctx: &Ctx, m: Methodology) -> Result<Issues, BoxError>;
}

/// SuspectLinks gives the suspect links of a baseline, when the graph keeps them (Go asserted the graph for
/// `SuspectLinks`; the graph's port has no such method, so the composition root hands it here).
#[async_trait]
pub trait SuspectLinks: Send + Sync {
    async fn suspect_links(
        &self,
        ctx: &Ctx,
        baseline: &BaselineID,
    ) -> Result<Vec<Link>, goap_graphapi::Error>;
}

/// Config configures the self-observation builtins.
#[derive(Clone, Default)]
pub struct Config {
    /// optional: slow spans of the observed run
    pub traces: Option<Arc<dyn TraceSource>>,
    /// required by methodology.draft
    pub drafts: Option<Arc<dyn MethodologyDrafts>>,
    pub thresholds: Thresholds,
    /// TraceURL is the trace viewer prefix (e.g. http://localhost:16686/trace/).
    pub trace_url: String,
    /// optional: the suspect links of the methodology namespace (a reference to an element edited since stays one).
    pub suspects: Option<Arc<dyn SuspectLinks>>,
}

/// What the builtins read of the engine.
#[derive(Clone)]
struct Parts {
    graph: Arc<dyn GraphPort>,
    store: Arc<dyn Store>,
    methodologies: Arc<dyn MethodologyPort>,
}

/// Register adds the self-observation builtins to b: they analyze a finished run of e from its journal and traces,
/// propose improvements of its methodology and turn the accepted ones into a draft version. It panics on a name
/// already registered.
///
/// The builtins keep the graph, the process store and the methodologies of e (taken now: register before sharing
/// the engine, then add b to its executors).
pub fn register(b: &mut BuiltinExecutor, e: &Engine, cfg: Config) {
    let parts = Parts {
        graph: e.graph.clone(),
        store: e.store.clone(),
        methodologies: e.methodologies.clone(),
    };
    let (p, c) = (parts.clone(), cfg.clone());
    b.register(
        goap_builtins::OBSERVE_ANALYZE,
        builtin(move |ctx, ac| {
            let (p, c) = (p.clone(), c.clone());
            async move { observe_analyze(&ctx, &p, ac, &c).await.map_err(Into::into) }
        }),
    );
    let (p, c) = (parts.clone(), cfg.clone());
    b.register(
        goap_builtins::OBSERVE_PROPOSE,
        builtin(move |ctx, ac| {
            let (p, c) = (p.clone(), c.clone());
            async move { observe_propose(&ctx, &p, ac, &c).await.map_err(Into::into) }
        }),
    );
    let c = cfg;
    b.register(
        goap_builtins::METHODOLOGY_DRAFT,
        builtin(move |ctx, ac| {
            let c = c.clone();
            async move { methodology_draft(&ctx, ac, &c).await.map_err(Into::into) }
        }),
    );
}

/// observedProcess is the process to analyze: vars.process, or the process of the trigger event
/// (vars.event.process.id).
fn observed_process(vars: &Map<String, Value>) -> String {
    if let Some(id) = vars.get("process").and_then(Value::as_str)
        && !id.is_empty()
    {
        return id.to_string();
    }
    vars.get("event")
        .and_then(|e| e.get("process"))
        .and_then(|p| p.get("id"))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

fn box_err(e: BoxError) -> Error {
    Error::msg(e.to_string())
}

async fn observe_analyze(
    ctx: &Ctx,
    e: &Parts,
    ac: ActionContext,
    cfg: &Config,
) -> Result<ActionResult, Error> {
    let id = observed_process(&ac.process.vars);
    if id.is_empty() {
        return Err(Error::msg(
            "no process to observe (vars.process or a process event)",
        ));
    }
    let obs = e.store.get(ctx, &id).await?;
    let mut ids = vec![obs.id.clone()];
    if let Ok(all) = e.store.list(ctx).await {
        let mut grew = true;
        while grew {
            grew = false;
            for p in &all {
                if !p.parent_id.is_empty() && ids.contains(&p.parent_id) && !ids.contains(&p.id) {
                    ids.push(p.id.clone());
                    grew = true;
                }
            }
        }
    }
    let recs = goap_journal::read(
        ctx,
        e.graph.as_ref(),
        &goap_journal::Filter {
            process_ids: ids,
            ..Default::default()
        },
    )
    .await?;
    let mut items: HashMap<ItemID, ChangeItem> = HashMap::new();
    let mut nodes: HashMap<ChangeImpactID, ChangeImpact> = HashMap::new();
    if !obs.change_id.is_empty()
        && let Ok(bb) = e.graph.blackboard(ctx, &obs.change_id).await
    {
        for it in bb.change.items {
            items.insert(it.id.clone(), it);
        }
        for n in bb.change.nodes {
            nodes.insert(n.id.clone(), n);
        }
    }
    let mut logs = Vec::new();
    let mut spans = Vec::new();
    if let Some(t) = &cfg.traces
        && !obs.trace_id.is_empty()
    {
        match t.spans(ctx, &obs.trace_id).await {
            Ok(s) => spans = s,
            Err(err) => logs.push(LogLine {
                time: Utc::now(),
                level: "warn".into(),
                message: format!("traces unavailable: {err}"),
                ..Default::default()
            }),
        }
    }
    let mut r = goap_observe::analyze(&recs, &items, &nodes, &spans, cfg.thresholds);
    let disabled: Vec<String> = obs.disabled.keys().cloned().collect();
    r.with_disabled(&obs.agent, &disabled);
    if r.process.is_empty() {
        r.process = obs.id.clone();
        r.methodology = obs.methodology.clone();
        r.version = obs.methodology_version.clone();
        r.agent = obs.agent.clone();
        r.status = obs.status.as_str().to_string();
    }
    let mut data = to_data(&r)?;
    if !cfg.trace_url.is_empty() && !r.trace_id.is_empty() {
        data.insert(
            "traceUrl".into(),
            Value::String(format!("{}{}", cfg.trace_url, r.trace_id)),
        );
    }
    let out = format!(
        "{}/{}: {} findings ({} steps, {} tokens, {} spans)",
        r.methodology,
        r.agent,
        r.findings.len(),
        r.steps,
        r.tokens,
        spans.len()
    );
    Ok(ActionResult {
        items: vec![ItemInput {
            r#type: "artifact".into(),
            target: "cost_report".into(),
            data,
            ..Default::default()
        }],
        output: out,
        logs,
        ..Default::default()
    })
}

/// toData converts a value into JSON-compatible data (lists never null).
fn to_data<T: Serialize>(v: &T) -> Result<Map<String, Value>, Error> {
    let mut m = match serde_json::to_value(v)? {
        Value::Object(m) => m,
        _ => Map::new(),
    };
    for (k, x) in m.iter_mut() {
        if x.is_null() && k.ends_with('s') {
            *x = Value::Array(Vec::new());
        }
    }
    Ok(m)
}

/// costReport returns the latest cost report of the blackboard.
fn cost_report(bb: &Blackboard) -> Result<Report, Error> {
    for it in bb.change.items.iter().rev() {
        if it.r#type == TYPE_ARTIFACT && it.target == "cost_report" && bb.change.active(&it.id) {
            return Ok(serde_json::from_value(Value::Object(it.data.clone()))?);
        }
    }
    Err(Error::msg("no cost report on the blackboard"))
}

async fn observe_propose(
    ctx: &Ctx,
    e: &Parts,
    ac: ActionContext,
    cfg: &Config,
) -> Result<ActionResult, Error> {
    let r = cost_report(&ac.blackboard)?;
    let cm = e.methodologies.methodology(ctx, &r.methodology).await?;
    let def = cm.definition();
    let (props, notes) = goap_observe::propose(&r, def);
    let stored = definition_links(ctx, e.graph.as_ref(), cfg.suspects.as_deref()).await?;
    let ix = ref_index_of(def);
    let hkey = goap_observe::element_key(&def.name, &def.version, "methodology", "");
    // an improvement is a change impact on an element of the methodology (ADR 0024): the rationale says what and
    // why, the version written on the change branch holds the proposed properties and links
    let mut ops = Vec::new();
    for p in &props {
        let mut why = p.title.clone();
        if !p.rationale.is_empty() {
            why = format!("{why}: {}", p.rationale);
        }
        if p.op == "update_node" {
            ops.push(NodeOp {
                op: "declare".into(),
                intent: INTENT_MODIFIED.as_str().into(),
                key: p.key.clone(),
                rationale: why,
                ..Default::default()
            });
        } else {
            ops.push(NodeOp {
                op: "declare".into(),
                intent: INTENT_CREATED.as_str().into(),
                key: p.key.clone(),
                r#type: p.r#type.clone(),
                rationale: why,
                ..Default::default()
            });
        }
        let none = Vec::new();
        ops.push(graph_write(
            p,
            &ix,
            &hkey,
            stored.get(&p.key).unwrap_or(&none),
        ));
    }
    let data = json!({
        "methodology": r.methodology, "process": r.process, "observedVersion": r.version,
        "currentVersion": def.version, "proposals": props.len(), "notes": notes,
    });
    let Value::Object(data) = data else {
        unreachable!("an object")
    };
    Ok(ActionResult {
        items: vec![ItemInput {
            r#type: "artifact".into(),
            target: "improvement_plan".into(),
            data,
            ..Default::default()
        }],
        nodes: ops,
        output: format!("{} proposals, {} notes", props.len(), notes.len()),
        ..Default::default()
    })
}

async fn methodology_draft(
    ctx: &Ctx,
    ac: ActionContext,
    cfg: &Config,
) -> Result<ActionResult, Error> {
    let Some(drafts) = &cfg.drafts else {
        return Err(Error::msg("no methodology registry to save drafts"));
    };
    let r = cost_report(&ac.blackboard)?;
    let bb = &ac.blackboard;
    let mut edits = Vec::new();
    for cn in &bb.change.nodes {
        if cn.review != REVIEW_ACCEPTED || !cn.items.is_empty() {
            continue;
        }
        let Some(post) = cn.post.as_ref().and_then(|p| bb.nodes.get(p)) else {
            continue;
        };
        let mut ed = Edit {
            op: "create_node".into(),
            key: cn.key.clone(),
            r#type: cn.r#type.clone(),
            props: definition_props(bb, post),
        };
        if let Some(pre) = &cn.pre {
            let Some(pre) = bb.nodes.get(pre) else {
                continue;
            };
            ed.op = "update_node".into();
            ed.props = changed_props(&definition_props(bb, pre), &ed.props);
        }
        edits.push(ed);
    }
    if edits.is_empty() {
        let Value::Object(data) = json!({"methodology": r.methodology, "skipped": true, "reason": "no proposal accepted"})
        else {
            unreachable!("an object")
        };
        return Ok(ActionResult {
            items: vec![ItemInput {
                r#type: "artifact".into(),
                target: "methodology_draft".into(),
                data,
                ..Default::default()
            }],
            output: "nothing to merge".into(),
            ..Default::default()
        });
    }
    // the draft is saved with the identity of the process initiator
    let ctx = goap_authz::with_principal(ctx, ac.process.initiator.clone());
    let cur = match drafts.definition(&ctx, &r.methodology, "").await {
        Ok(Some(m)) => m,
        Ok(None) => {
            return Err(Error::msg(format!(
                "methodology {}: not published",
                r.methodology
            )));
        }
        Err(err) => return Err(Error::msg(format!("methodology {}: {err}", r.methodology))),
    };
    let mut d = goap_observe::apply(&cur, &edits).map_err(|e| Error::msg(e.to_string()))?;
    d.methodology.version = next_free_version(&ctx, drafts.as_ref(), &cur).await;
    let issues = drafts
        .save_draft(&ctx, d.methodology.clone())
        .await
        .map_err(box_err)?;
    let mut data = to_data(&d)?;
    data.insert("methodology".into(), Value::String(cur.name.clone()));
    data.insert(
        "version".into(),
        Value::String(d.methodology.version.clone()),
    );
    data.insert("from".into(), Value::String(cur.version.clone()));
    let is: Vec<Value> = issues
        .0
        .iter()
        .map(|i| Value::String(i.to_string()))
        .collect();
    data.insert("issues".into(), Value::Array(is));
    Ok(ActionResult {
        items: vec![ItemInput {
            r#type: "artifact".into(),
            target: "methodology_draft".into(),
            data,
            ..Default::default()
        }],
        output: format!(
            "draft {}@{}: {} changes, {} issues",
            cur.name,
            d.methodology.version,
            d.applied.len(),
            issues.0.len()
        ),
        ..Default::default()
    })
}

/// The next version of cur no stored version takes (observe::next_version, whose test of a version is a call of the
/// registry here).
async fn next_free_version(ctx: &Ctx, drafts: &dyn MethodologyDrafts, cur: &Methodology) -> String {
    let mut out = goap_observe::next_version(&cur.version, |_| false);
    for _ in 0..1000 {
        let taken = matches!(drafts.definition(ctx, &cur.name, &out).await, Ok(Some(_)));
        if !taken {
            break;
        }
        out = goap_observe::next_version(&out, |_| false);
    }
    out
}

fn sprint(v: &Value) -> String {
    goap_dsl::sprint(std::slice::from_ref(v))
}

/// changedProps are the properties a version changes compared to the one it starts from (a property it drops: nil).
fn changed_props(pre: &Map<String, Value>, post: &Map<String, Value>) -> Map<String, Value> {
    let mut out = Map::new();
    for (k, v) in post {
        match pre.get(k) {
            Some(old) if sprint(old) == sprint(v) => {}
            _ => {
                out.insert(k.clone(), v.clone());
            }
        }
    }
    for k in pre.keys() {
        if !post.contains_key(k) {
            out.insert(k.clone(), Value::Null);
        }
    }
    out
}

/// storedLink is a link of a definition node, with the key of its target.
#[derive(Debug, Clone)]
struct StoredLink {
    link: Link,
    to_key: String,
}

/// definitionLinks are the outgoing links of the definition nodes at the head of the methodology namespace, by key of
/// their source; suspect ones too when the graph gives them (a reference to an element edited since stays one).
async fn definition_links(
    ctx: &Ctx,
    g: &dyn GraphPort,
    suspects: Option<&dyn SuspectLinks>,
) -> Result<BTreeMap<String, Vec<StoredLink>>, Error> {
    let head = g
        .branch_head(ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await?;
    if head.id.is_empty() {
        return Ok(BTreeMap::new());
    }
    let (nodes, mut links) = g.baseline_graph(ctx, &head.id).await?;
    if let Some(s) = suspects {
        links.extend(s.suspect_links(ctx, &head.id).await?);
    }
    let keys: HashMap<NodeID, String> = nodes.into_iter().map(|n| (n.id, n.key)).collect();
    let mut out: BTreeMap<String, Vec<StoredLink>> = BTreeMap::new();
    for l in links {
        let from = keys.get(&l.from.id).cloned().unwrap_or_default();
        let to_key = keys.get(&l.to.id).cloned().unwrap_or_default();
        out.entry(from)
            .or_default()
            .push(StoredLink { link: l, to_key });
    }
    Ok(out)
}

/// graphWrite is the write of a proposal (in the terms of the definition) on its node, in the terms of the graph
/// (ADR 0120): the names of elements of the methodology are links, the links of the fields it sets replace those the
/// node has (stored, its outgoing links).
fn graph_write(p: &Proposal, ix: &RefIndex, hkey: &str, stored: &[StoredLink]) -> NodeOp {
    let mut kind = "";
    if let Some(rest) = p.key.strip_prefix(&format!("{hkey}/")) {
        kind = rest.split('/').next().unwrap_or_default();
    }
    let enc = encode_refs(kind, &p.props, ix);
    let mut w = NodeOp {
        op: "write".into(),
        node: p.key.clone(),
        props: enc.props.clone(),
        ..Default::default()
    };
    if p.op == "update_node" {
        for k in p.props.keys() {
            if !enc.props.contains_key(k) {
                w.props.insert(k.clone(), Value::Null); // every name of the field is a link now
            }
        }
    }
    let want = &enc.links;
    let mut kept = vec![false; want.len()];
    if p.op == "update_node" {
        let replaced = ref_kinds_of(kind, &p.props);
        for l in stored {
            let r#as = l
                .link
                .properties
                .get(LINK_ATTR_AS)
                .and_then(Value::as_str)
                .unwrap_or_default();
            let rk = RefKind {
                r#type: l.link.r#type.clone(),
                r#as: r#as.to_string(),
            };
            if !replaced.contains(&rk) {
                continue;
            }
            let lp = sprint(&Value::Object(l.link.properties.clone()));
            let same = want.iter().position(|x| {
                x.r#type == l.link.r#type
                    && format!("{hkey}/{}", x.to) == l.to_key
                    && sprint(&Value::Object(x.props.clone())) == lp
            });
            if let Some(i) = same
                && !kept[i]
            {
                kept[i] = true;
                continue;
            }
            w.remove_links.push(l.link.id.to_string());
        }
    }
    for (i, l) in want.iter().enumerate() {
        if !kept[i] {
            w.links.push(NodeOpLink {
                r#type: l.r#type.clone(),
                to: format!("{hkey}/{}", l.to),
                props: l.props.clone(),
            });
        }
    }
    w
}

/// definitionProps are the properties of a definition node in the terms of the definition: its links to the elements
/// of the methodology named again (ADR 0120), the nodes they reach read from the blackboard.
fn definition_props(bb: &Blackboard, v: &NodeView) -> Map<String, Value> {
    let node_of = |id: &NodeID| -> Option<&Node> {
        bb.nodes
            .iter()
            .find(|(r, _)| &r.id == id)
            .map(|(_, n)| &n.node)
            .or_else(|| {
                bb.neighbors
                    .iter()
                    .find(|(r, _)| &r.id == id)
                    .map(|(_, n)| n)
            })
    };
    let mut targets = Vec::new();
    for l in &v.out {
        if let Some(t) = node_of(&l.to.id) {
            let name = t
                .properties
                .get("name")
                .and_then(Value::as_str)
                .unwrap_or_default();
            targets.push(RefTarget {
                r#type: l.r#type.clone(),
                props: l.properties.clone(),
                kind: element_of_type(&t.r#type).to_string(),
                name: name.to_string(),
            });
        }
    }
    decode_refs(
        element_of_type(&v.node.r#type),
        &v.node.properties,
        &targets,
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_domain::LinkID;
    use goap_methodology::{Action, Agent, LINK_PURSUES, LINK_USES};

    // A proposal names the elements of the methodology; written on the graph, the names are links and the links of
    // the fields it sets replace those of the node (ADR 0120).
    #[test]
    fn graph_write_turns_names_into_links() {
        let action = |name: &str| {
            let mut a = Action::default();
            a.name = name.into();
            a
        };
        let m = Methodology {
            name: "m".into(),
            version: "1".into(),
            actions: vec![action("a"), action("b"), action("c")],
            agents: vec![{
                let mut a = Agent::default();
                a.name = "ag".into();
                a
            }],
            ..Default::default()
        };
        let hkey = goap_observe::element_key("m", "1", "methodology", "");
        let link = |id: &str, typ: &str, pos: f64, to: String| StoredLink {
            link: Link {
                id: LinkID::from(id),
                r#type: typ.into(),
                properties: json!({"position": pos}).as_object().cloned().unwrap(),
                ..Default::default()
            },
            to_key: to,
        };
        let uses =
            |id: &str, to: &str, pos: f64| link(id, LINK_USES, pos, format!("{hkey}/action/{to}"));
        let stored = vec![
            uses("L1", "a", 0.0),
            uses("L2", "b", 1.0),
            uses("L3", "c", 2.0),
            link("L4", LINK_PURSUES, 0.0, format!("{hkey}/goal/g")),
        ];
        let p = Proposal {
            op: "update_node".into(),
            key: format!("{hkey}/agent/ag"),
            props: json!({"actions": ["a", "c"]}).as_object().cloned().unwrap(),
            ..Default::default()
        };
        let w = graph_write(&p, &ref_index_of(&m), &hkey, &stored);
        assert!(
            w.remove_links == ["L2", "L3"]
                && w.links.len() == 1
                && w.links[0].to == format!("{hkey}/action/c")
                && w.links[0].props.get("position").and_then(Value::as_f64) == Some(1.0),
            "links of the write: remove {:?}, add {:?}",
            w.remove_links,
            w.links
        );
        assert!(
            w.props.get("actions") == Some(&Value::Null),
            "the names are links, the property is cleared: {:?}",
            w.props
        );
    }

    #[test]
    fn changed_props_and_observed_process() {
        // not in the Go suite: the edit of a version keeps what changed, a dropped property is nil
        let pre = json!({"a": 1, "b": "x", "c": true});
        let post = json!({"a": 1.0, "b": "y", "d": [1]});
        let got = changed_props(pre.as_object().unwrap(), post.as_object().unwrap());
        assert_eq!(Value::Object(got), json!({"b": "y", "c": null, "d": [1]}));
        let vars = json!({"event": {"process": {"id": "p9"}}});
        assert_eq!(observed_process(vars.as_object().unwrap()), "p9");
        let vars = json!({"process": "p1", "event": {"process": {"id": "p9"}}});
        assert_eq!(observed_process(vars.as_object().unwrap()), "p1");
        let data = to_data(&Report::default()).unwrap();
        assert_eq!(data["findings"], json!([]));
        assert_eq!(data["actions"], json!([]));
    }
}
