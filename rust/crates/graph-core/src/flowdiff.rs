//! Two flows of a change compared impact by impact (Go's `Graph.DiffFlows`, ADR 0083): the impacts one sees and not
//! the other, and those both see with another content, attribute by attribute (`domain.DiffShapes`).
use std::collections::{BTreeSet, HashMap};

use serde_json::{Map, Value};

use crate::{
    GraphError, Result,
    flow::{FLOW_OPEN, Flow, MAIN_FLOW},
    impact::{ChangeImpact, Draft, REVIEW_ACCEPTED, REVIEW_REJECTED},
    jsontext,
    lnk::Url,
    model::{ChangeId, NodeId, NodeRef, Props, Target, Version},
    read::{ViewLevel, World},
    store::ReadTx,
    view::View,
    write::work::fold_change,
};

pub const LEVEL_WRITTEN: &str = "written";
pub const LEVEL_ACCEPTED: &str = "accepted";

pub const DIFF_ADDED: &str = "added";
pub const DIFF_REMOVED: &str = "removed";
pub const DIFF_MODIFIED: &str = "modified";

const FIELD_PROPERTY: &str = "property";
const FIELD_STATE: &str = "state";
const FIELD_OWNER: &str = "owner";
const FIELD_LINK: &str = "link";

const OP_ADDED: &str = "added";
const OP_REMOVED: &str = "removed";
const OP_CHANGED: &str = "changed";

/// One attribute that differs between two shapes of a node (`domain.FieldChange`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct FieldChange {
    pub kind: String,
    pub name: String,
    pub op: String,
    pub target: String,
    pub old: Option<Value>,
    pub new: Option<Value>,
}

/// What one flow sees of an impact (`domain.ImpactSide`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ImpactSide {
    pub impact: String,
    pub flow: String,
    pub intent: String,
    pub review: String,
    /// false for a planned impact: its node is not checked out
    pub drafted: bool,
    pub state: String,
    pub owner: Url,
}

/// An impact that differs between the two flows (`domain.ImpactDiff`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct ImpactDiff {
    pub node: String,
    pub key: String,
    pub typ: String,
    pub category: String,
    pub left: Option<ImpactSide>,
    pub right: Option<ImpactSide>,
    pub changes: Vec<FieldChange>,
}

/// Two flows of a change compared (`domain.FlowDiff`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct FlowDiff {
    pub left: String,
    pub right: String,
    pub level: String,
    pub impacts: Vec<ImpactDiff>,
    /// the impacts both flows see with the same content, left out of `impacts`
    pub identical: usize,
}

/// A link as a shape compares it (`domain.LinkShape`).
#[derive(Debug, Clone, Default)]
struct LinkShape {
    typ: String,
    to: String,
    to_key: String,
    props: Props,
}

impl LinkShape {
    fn label(&self) -> &str {
        if self.to_key.is_empty() { &self.to } else { &self.to_key }
    }
}

/// What a node is, compared between two flows (`domain.NodeShape`).
#[derive(Debug, Clone, Default)]
struct NodeShape {
    state: String,
    owner: Url,
    props: Props,
    links: Vec<LinkShape>,
}

/// An impact as a flow sees it, with the shape of its node.
struct ImpactShape {
    side: ImpactSide,
    node: String,
    key: String,
    typ: String,
    shape: NodeShape,
}

/// Compares two flows of a change, each the main flow (empty or `main`) or an option, at the level `written` (the
/// default) or `accepted`.
pub async fn diff_flows<T: ReadTx>(tx: &T, id: &ChangeId, left: &str, right: &str, level: &str) -> Result<FlowDiff> {
    let level = if level.is_empty() { LEVEL_WRITTEN } else { level };
    if level != LEVEL_WRITTEN && level != LEVEL_ACCEPTED {
        return Err(GraphError::invalid(format!(
            "flows are compared at {LEVEL_WRITTEN} or {LEVEL_ACCEPTED}, not {}",
            serde_json::to_string(level).unwrap_or_default()
        )));
    }
    if tx.change(id).await?.is_none() {
        return Err(GraphError::not_found(format!("change {id}")));
    }
    let view = fold_change(tx, id).await?;
    let flow_of = |f: &str| -> Result<String> {
        if f.is_empty() || f == MAIN_FLOW {
            return Ok(String::new());
        }
        match view.flows.get(f) {
            Some(fl) if fl.option.is_some() => Ok(f.to_string()),
            _ => Err(GraphError::not_found(format!("option {f} of change {id}"))),
        }
    };
    let (lf, rf) = (flow_of(left)?, flow_of(right)?);
    let name = |f: &str| {
        if f.is_empty() {
            MAIN_FLOW.to_string()
        } else {
            f.to_string()
        }
    };
    let ls = shapes(tx, id, &view.seen(&lf), &lf, level).await?;
    let rs = shapes(tx, id, &view.seen(&rf), &rf, level).await?;
    let mut out = FlowDiff {
        left: name(&lf),
        right: name(&rf),
        level: level.to_string(),
        ..FlowDiff::default()
    };
    match_impacts(&mut out, ls, rs);
    Ok(out)
}

/// The impacts a flow sees at a level, each with its draft as a shape (a planned one: the version it starts from, a
/// planned creation: nothing but its key and type; Go's `flowImpactShapes`).
async fn shapes<T: ReadTx>(
    tx: &T,
    id: &ChangeId,
    seen: &[ChangeImpact],
    flow: &str,
    level: &str,
) -> Result<Vec<ImpactShape>> {
    let w = World::open(
        tx,
        &View {
            change: id.clone(),
            flow: if flow.is_empty() {
                MAIN_FLOW.into()
            } else {
                flow.to_string()
            },
            ..View::default()
        },
    )
    .await?;
    let mut keys: HashMap<NodeRef, String> = HashMap::new();
    let mut out = Vec::new();
    for cn in seen {
        if cn.review == REVIEW_REJECTED || (level == LEVEL_ACCEPTED && cn.review != REVIEW_ACCEPTED) {
            continue;
        }
        let mut is = ImpactShape {
            side: ImpactSide {
                impact: cn.id.clone(),
                flow: cn.flow.clone(),
                intent: cn.intent.clone(),
                review: cn.review.clone(),
                drafted: cn.post.is_some(),
                ..ImpactSide::default()
            },
            node: node_of(cn),
            key: cn.key.clone(),
            typ: cn.typ.clone(),
            shape: NodeShape::default(),
        };
        if let Some(r) = cn.post.as_ref().or(cn.pre.as_ref()) {
            let (n, links) = w.node_and_links(r).await?;
            is.key = n.key.clone();
            is.typ = n.typ.clone();
            is.shape = NodeShape {
                state: n.state.clone(),
                owner: n.owner.clone(),
                props: n.props.clone(),
                links: Vec::new(),
            };
            for l in links {
                match &l.to {
                    Target::External(u) => is.shape.links.push(LinkShape {
                        typ: l.typ.clone(),
                        to: u.logical().to_string(),
                        to_key: u.to_string(),
                        props: l.props.clone(),
                    }),
                    Target::Node(to) => {
                        let to_key = match keys.get(to) {
                            Some(k) => k.clone(),
                            None => {
                                let k = match w.node_and_links(to).await {
                                    Ok((n, _)) => n.key,
                                    Err(_) => to.id.0.clone(),
                                };
                                keys.insert(to.clone(), k.clone());
                                k
                            }
                        };
                        is.shape.links.push(LinkShape {
                            typ: l.typ.clone(),
                            to: to.id.0.clone(),
                            to_key,
                            props: l.props.clone(),
                        });
                    }
                }
            }
            is.side.state = n.state;
            is.side.owner = n.owner;
        }
        out.push(is);
    }
    Ok(out)
}

/// The node an impact is about, when it has one (`nodeOf`): its post, else its pre.
fn node_of(cn: &ChangeImpact) -> String {
    cn.post
        .as_ref()
        .or(cn.pre.as_ref())
        .map(|r| r.id.0.clone())
        .unwrap_or_default()
}

/// Pairs the impacts of the two flows (by node, then by type and key) and fills the diff (Go's `matchImpacts`).
fn match_impacts(out: &mut FlowDiff, left: Vec<ImpactShape>, right: Vec<ImpactShape>) {
    let mut by_node: HashMap<&str, usize> = HashMap::new();
    for (i, r) in right.iter().enumerate() {
        if !r.node.is_empty() {
            by_node.insert(&r.node, i);
        }
    }
    let name = |s: &ImpactShape| format!("{}\0{}", s.typ, s.key);
    let mut by_name: HashMap<String, usize> = HashMap::new();
    for (i, r) in right.iter().enumerate() {
        by_name.insert(name(r), i);
    }
    let mut paired = vec![false; right.len()];
    let mut pairs: Vec<(Option<usize>, Option<usize>)> = Vec::new();
    let mut unmatched = Vec::new();
    for (i, l) in left.iter().enumerate() {
        match by_node.get(l.node.as_str()) {
            Some(&j) if !l.node.is_empty() && !paired[j] => {
                paired[j] = true;
                pairs.push((Some(i), Some(j)));
            }
            _ => unmatched.push(i),
        }
    }
    for i in unmatched {
        match by_name.get(&name(&left[i])) {
            Some(&j) if !paired[j] => {
                paired[j] = true;
                pairs.push((Some(i), Some(j)));
            }
            _ => pairs.push((Some(i), None)),
        }
    }
    for (j, p) in paired.iter().enumerate() {
        if !p {
            pairs.push((None, Some(j)));
        }
    }
    for p in pairs {
        let d = match p {
            (Some(i), None) => {
                let l = &left[i];
                ImpactDiff {
                    node: l.node.clone(),
                    key: l.key.clone(),
                    typ: l.typ.clone(),
                    category: DIFF_REMOVED.into(),
                    left: Some(l.side.clone()),
                    ..ImpactDiff::default()
                }
            }
            (None, Some(j)) => {
                let r = &right[j];
                ImpactDiff {
                    node: r.node.clone(),
                    key: r.key.clone(),
                    typ: r.typ.clone(),
                    category: DIFF_ADDED.into(),
                    right: Some(r.side.clone()),
                    ..ImpactDiff::default()
                }
            }
            (Some(i), Some(j)) => {
                let (l, r) = (&left[i], &right[j]);
                let changes = diff_shapes(&l.shape, &r.shape);
                if changes.is_empty() {
                    out.identical += 1;
                    continue;
                }
                ImpactDiff {
                    node: r.node.clone(),
                    key: r.key.clone(),
                    typ: r.typ.clone(),
                    category: DIFF_MODIFIED.into(),
                    left: Some(l.side.clone()),
                    right: Some(r.side.clone()),
                    changes,
                }
            }
            (None, None) => continue,
        };
        out.impacts.push(d);
    }
    out.impacts
        .sort_by(|a, b| a.key.cmp(&b.key).then_with(|| a.typ.cmp(&b.typ)));
}

/// Two values the same once marshalled (Go's `sameValue`).
fn same(a: &Value, b: &Value) -> bool {
    jsontext::to_string(a) == jsontext::to_string(b)
}

/// The operation of a value that was empty or is: set from nothing is added, to nothing removed (`changeOp`).
fn change_op(was_empty: bool, is_empty: bool) -> &'static str {
    if was_empty {
        OP_ADDED
    } else if is_empty {
        OP_REMOVED
    } else {
        OP_CHANGED
    }
}

/// The attributes that differ between two shapes of a node, from the left to the right (`domain.DiffShapes`).
fn diff_shapes(left: &NodeShape, right: &NodeShape) -> Vec<FieldChange> {
    let mut out = Vec::new();
    if left.state != right.state {
        out.push(FieldChange {
            kind: FIELD_STATE.into(),
            op: change_op(left.state.is_empty(), right.state.is_empty()).into(),
            old: Some(Value::String(left.state.clone())),
            new: Some(Value::String(right.state.clone())),
            ..FieldChange::default()
        });
    }
    if left.owner != right.owner {
        out.push(FieldChange {
            kind: FIELD_OWNER.into(),
            op: change_op(left.owner.is_zero(), right.owner.is_zero()).into(),
            old: Some(Value::String(left.owner.to_string())),
            new: Some(Value::String(right.owner.to_string())),
            ..FieldChange::default()
        });
    }
    let empty = Map::new();
    let (lp, rp) = (
        left.props.as_ref().unwrap_or(&empty),
        right.props.as_ref().unwrap_or(&empty),
    );
    let names: BTreeSet<&String> = lp.keys().chain(rp.keys()).collect();
    for k in names {
        let change = |op: &str, old: Option<&Value>, new: Option<&Value>| FieldChange {
            kind: FIELD_PROPERTY.into(),
            name: k.clone(),
            op: op.into(),
            old: old.filter(|v| !v.is_null()).cloned(),
            new: new.filter(|v| !v.is_null()).cloned(),
            ..FieldChange::default()
        };
        match (lp.get(k), rp.get(k)) {
            (Some(l), None) => out.push(change(OP_REMOVED, Some(l), None)),
            (None, Some(r)) => out.push(change(OP_ADDED, None, Some(r))),
            (Some(l), Some(r)) if !same(l, r) => out.push(change(OP_CHANGED, Some(l), Some(r))),
            _ => {}
        }
    }
    out.extend(diff_links(&left.links, &right.links));
    out
}

/// The properties of a link as a change of it holds them: none when empty (`emptyAsNil`).
fn link_props(p: &Props) -> Option<Value> {
    p.as_ref().filter(|m| !m.is_empty()).map(|m| Value::Object(m.clone()))
}

fn diff_links(left: &[LinkShape], right: &[LinkShape]) -> Vec<FieldChange> {
    let by_key = |ls: &[LinkShape]| -> HashMap<String, LinkShape> {
        ls.iter().map(|l| (format!("{}\0{}", l.typ, l.to), l.clone())).collect()
    };
    let (lm, rm) = (by_key(left), by_key(right));
    let mut keys: Vec<&String> = lm.keys().chain(rm.keys().filter(|k| !lm.contains_key(*k))).collect();
    let shape = |k: &str| lm.get(k).or_else(|| rm.get(k)).cloned().unwrap_or_default();
    keys.sort_by(|a, b| {
        let (la, lb) = (shape(a), shape(b));
        la.typ.cmp(&lb.typ).then_with(|| la.label().cmp(lb.label()))
    });
    let mut out = Vec::new();
    for k in keys {
        match (lm.get(k), rm.get(k)) {
            (Some(l), None) => out.push(FieldChange {
                kind: FIELD_LINK.into(),
                name: l.typ.clone(),
                op: OP_REMOVED.into(),
                target: l.label().to_string(),
                old: link_props(&l.props),
                ..FieldChange::default()
            }),
            (None, Some(r)) => out.push(FieldChange {
                kind: FIELD_LINK.into(),
                name: r.typ.clone(),
                op: OP_ADDED.into(),
                target: r.label().to_string(),
                new: link_props(&r.props),
                ..FieldChange::default()
            }),
            (Some(l), Some(r)) => {
                let (o, n) = (link_props(&l.props), link_props(&r.props));
                let differ = match (&o, &n) {
                    (None, None) => false,
                    (Some(a), Some(b)) => !same(a, b),
                    _ => true,
                };
                if differ {
                    out.push(FieldChange {
                        kind: FIELD_LINK.into(),
                        name: r.typ.clone(),
                        op: OP_CHANGED.into(),
                        target: r.label().to_string(),
                        old: o,
                        new: n,
                    });
                }
            }
            (None, None) => {}
        }
    }
    out
}

/// A node written by at least one option, with its version on the main flow and on each option (None: not in the graph
/// of that side) and its properties by side (`OptionNode`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct OptionNode {
    pub node: NodeId,
    pub key: String,
    pub typ: String,
    pub main: Option<NodeRef>,
    /// by option id
    pub options: HashMap<String, Option<NodeRef>>,
    /// by side: `main` or the option id
    pub props: HashMap<String, Props>,
}

/// The options of a change compared (`OptionComparison`).
#[derive(Debug, Clone, Default)]
pub struct OptionComparison {
    pub level: String,
    pub options: Vec<Flow>,
    pub nodes: Vec<OptionNode>,
}

/// Compares the open options of a change (every option when `all` is set) with its main flow at a level, `written`
/// (the default) or `accepted` (Go's `Graph.CompareOptions`): the nodes an option holds at another version than the
/// main flow, or a draft of that differs from the main flow's, or not at all.
pub async fn compare_options<T: ReadTx>(tx: &T, id: &ChangeId, level: &str, all: bool) -> Result<OptionComparison> {
    let level = if level.is_empty() { LEVEL_WRITTEN } else { level };
    if level != LEVEL_WRITTEN && level != LEVEL_ACCEPTED {
        return Err(GraphError::invalid(format!(
            "options are compared at {LEVEL_WRITTEN} or {LEVEL_ACCEPTED}, not {}",
            serde_json::to_string(level).unwrap_or_default()
        )));
    }
    if tx.change(id).await?.is_none() {
        return Err(GraphError::not_found(format!("change {id}")));
    }
    let lv = ViewLevel::parse(level)?;
    let fx = fold_change(tx, id).await?.flows;
    let world = async |flow: &str| {
        World::open(
            tx,
            &View {
                change: id.clone(),
                flow: flow.to_string(),
                ..View::default()
            },
        )
        .await
    };
    let main_w = world(MAIN_FLOW).await?;
    let (_, main) = main_w.change_state(lv).await?;
    let mut out = OptionComparison {
        level: level.to_string(),
        ..OptionComparison::default()
    };
    let mut sides = Vec::new();
    for o in fx.options() {
        if !all && o.status != FLOW_OPEN {
            continue;
        }
        let w = world(&o.id).await?;
        let (_, state) = w.change_state(lv).await?;
        out.options.push(o.clone());
        sides.push((o.id.clone(), w, state));
    }
    // the drafts each side holds (ADR 0079): a node both sides hold a draft of differs when the drafts do
    let differs = |w: &World<'_, T>, n: &NodeId| match (w.draft_of(n), main_w.draft_of(n)) {
        (Some(a), Some(b)) => !same_draft(a, b),
        _ => false,
    };
    let mut changed: BTreeSet<NodeId> = BTreeSet::new();
    for (_, w, state) in &sides {
        for (n, &v) in state {
            if main.get(n).copied().unwrap_or_default() != v || (v == 0 && differs(w, n)) {
                changed.insert(n.clone());
            }
        }
        changed.extend(main.keys().filter(|n| !state.contains_key(*n)).cloned());
    }
    let at = |state: &HashMap<NodeId, Version>, n: &NodeId| state.get(n).map(|&v| NodeRef::new(n.clone(), v));
    for n in changed {
        let mut on = OptionNode {
            node: n.clone(),
            main: at(&main, &n),
            ..OptionNode::default()
        };
        let mut read = vec![(MAIN_FLOW.to_string(), &main_w, on.main.clone())];
        for (oid, w, state) in &sides {
            let r = at(state, &n);
            on.options.insert(oid.clone(), r.clone());
            read.push((oid.clone(), w, r));
        }
        for (side, w, r) in read {
            let Some(r) = r else { continue };
            let (node, _) = w.node_and_links(&r).await?;
            on.key = node.key;
            on.typ = node.typ;
            on.props.insert(side, node.props);
        }
        if on.key.is_empty() {
            // retired on every side it was on: named from its history
            if let Some(first) = tx.version_range(&n, 0, usize::MAX >> 1).await?.into_iter().next() {
                on.key = first.key;
                on.typ = first.typ;
            }
        }
        out.nodes.push(on);
    }
    out.nodes.sort_by(|a, b| a.key.cmp(&b.key));
    Ok(out)
}

/// Whether two drafts hold the same node (Go's `sameDraft`): their flows, executions and link ids aside.
fn same_draft(a: &Draft, b: &Draft) -> bool {
    let norm = |d: &Draft| {
        let mut d = d.clone();
        d.flow.clear();
        d.execution.clear();
        for l in &mut d.links {
            l.id = Default::default();
        }
        serde_json::to_value(&d).unwrap_or_default()
    };
    norm(a) == norm(b)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn shapes_differ_attribute_by_attribute() {
        let l = NodeShape {
            state: "draft".into(),
            props: json!({"a": 1, "b": "x"}).as_object().cloned(),
            ..NodeShape::default()
        };
        let r = NodeShape {
            state: "review".into(),
            props: json!({"a": 2, "c": true}).as_object().cloned(),
            links: vec![LinkShape {
                typ: "of".into(),
                to: "n".into(),
                to_key: "K".into(),
                props: None,
            }],
            ..NodeShape::default()
        };
        let d = diff_shapes(&l, &r);
        let ops: Vec<_> = d
            .iter()
            .map(|c| (c.kind.as_str(), c.name.as_str(), c.op.as_str()))
            .collect();
        assert_eq!(
            ops,
            [
                ("state", "", "changed"),
                ("property", "a", "changed"),
                ("property", "b", "removed"),
                ("property", "c", "added"),
                ("link", "of", "added"),
            ]
        );
        assert_eq!(d[4].target, "K");
    }
}
