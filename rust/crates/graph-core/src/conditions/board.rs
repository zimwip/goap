//! The blackboard of a change as the conditions read it (Go's `Graph.Blackboard`, answered by `GetBlackboard`): the
//! change with its impacts and items, the node views of the versions its impacts name, the neighbours of those, its
//! options, its decision points and the instant it is read at. The guard of a transition, its criteria and the
//! guardians of its chain all judge the change on this, in the transaction of the move (ADR 0140), so it is built
//! here rather than from the protocol.
use chrono::{DateTime, Utc};
use serde_json::{Map, Value, json};

use graph_catalog::Catalog;

use super::activation::Facets;
use crate::{
    Result, access,
    change::Change,
    criticality,
    decision::DecisionPoint,
    dependency,
    flow::{Flow, Flows, MAIN_FLOW},
    model::{ChangeId, Link, Node, NodeView, Target},
    read::World,
    store::ReadTx,
    view::View,
    write::{decision, flow as wflow, whole},
};

fn props(p: &Option<Map<String, Value>>) -> Value {
    p.clone().map_or_else(|| Value::Object(Map::new()), Value::Object)
}

/// A node as a view of the blackboard carries it.
fn node(n: &Node) -> Value {
    json!({"id": n.id.as_str(), "version": n.version, "key": n.key, "type": n.typ, "props": props(&n.props),
        "deleted": n.deleted, "state": n.state})
}

/// A link of a node view: its target is a version, or nothing for a link to another source (ADR 0154).
fn link(l: &Link, incoming: bool) -> Value {
    let end = match &l.to {
        Target::Node(r) => json!({"id": r.id.as_str(), "version": r.version}),
        Target::External(_) => json!({"id": "", "version": 0}),
    };
    if incoming {
        json!({"id": l.id.as_str(), "type": l.typ, "from": {"id": l.from.id.as_str(), "version": l.from.version}})
    } else {
        json!({"id": l.id.as_str(), "type": l.typ, "to": end})
    }
}

fn view(v: &NodeView, cat: &Catalog) -> Value {
    let not_committable = !v.node.state.is_empty()
        && cat
            .lifecycle(&v.node.typ)
            .is_some_and(|lc| !lc.committable(&v.node.state));
    json!({"node": node(&v.node), "latest": v.latest, "notCommittable": not_committable,
        "out": v.out.iter().map(|l| link(l, false)).collect::<Vec<_>>(),
        "in": v.r#in.iter().map(|l| link(l, true)).collect::<Vec<_>>()})
}

fn option(f: &Flow) -> Value {
    let spec = f.option.as_ref();
    json!({"id": f.id, "status": f.status, "optionStatus": f.option_status(), "active": f.active,
        "evaluation": f.evaluation,
        "option": {"name": spec.map_or("", |o| o.name.as_str()), "hypothesis": spec.map_or("", |o| o.hypothesis.as_str())}})
}

fn point(d: &DecisionPoint) -> Value {
    let questions: Vec<Value> = d
        .questions
        .iter()
        .map(|q| {
            json!({"id": q.id, "point": q.point, "text": q.text, "status": q.status, "answer": q.answer,
                "answeredBy": q.answered_by})
        })
        .collect();
    let ruling = d.ruling.as_ref().map(|r| {
        json!({"outcome": r.outcome, "option": r.option, "confidence": r.confidence,
            "justification": r.justification, "by": r.by, "human": r.human})
    });
    json!({"id": d.id, "question": d.question, "status": d.status, "options": d.options, "criteria": d.criteria,
        "policy": Value::Object(d.policy.clone()), "humanOnly": d.human_only, "escalation": d.escalation,
        "questions": questions, "ruling": ruling, "option": d.option, "decidedBy": d.decided_by})
}

/// The change as the blackboard carries it: its header, its impacts (`nodes`) and the items the flow sees.
fn change_value(w: &whole::WholeChange, fx: &Flows, items: &[crate::flow::Item], flow: &str) -> Value {
    let c = &w.change;
    let impacts: Vec<Value> = w
        .impacts
        .iter()
        .map(|(cn, _)| serde_json::to_value(cn).unwrap_or(Value::Null))
        .collect();
    // the items the flow sees (`Change.View`): those of its chain plain, the view carries no flow events
    let chain: Vec<&str> = fx.chain(flow).iter().map(|f| f.id.as_str()).collect();
    let seen: Vec<Value> = fx
        .view(items, flow)
        .into_iter()
        .map(|i| {
            let mut it = w.items[i].clone();
            if chain.contains(&items[i].flow.as_str())
                && let Some(m) = it.as_object_mut()
            {
                m.remove("flow");
            }
            it
        })
        .collect();
    json!({"id": c.id.as_str(), "title": c.title, "intent": c.intent, "status": c.status,
        "lifecycle": c.lifecycle, "state": c.state, "goal": c.goal, "methodology": c.methodology,
        "branch": c.branch, "baselineId": c.baseline_id.as_str(), "resultBaselineId": c.result_baseline_id.as_str(),
        "data": props(&c.data),
        "dependsOn": c.depends_on.iter().map(|d| d.as_str()).collect::<Vec<_>>(),
        "nodes": impacts, "items": seen})
}

/// The blackboard of a change on a flow (none: its active option), at an instant.
pub async fn wire<T: ReadTx>(
    tx: &T,
    cat: &Catalog,
    id: &ChangeId,
    flow: Option<&str>,
    at: DateTime<Utc>,
) -> Result<Value> {
    let items = wflow::items_of(tx, id).await?;
    let fx = Flows::replay(&items);
    let flow = fx.resolve(flow.unwrap_or_default());
    let w = whole::whole_change_on(tx, cat, id, Some(&flow)).await?;
    let v = View {
        change: id.clone(),
        flow: if flow.is_empty() {
            MAIN_FLOW.into()
        } else {
            flow.clone()
        },
        ..Default::default()
    };
    let world = World::open(tx, &v).await?;
    let (views, neighbours) = world.blackboard().await?;
    let points = decision::points_of(tx, id, at).await?;
    Ok(json!({
        "change": change_value(&w, &fx, &items, &flow),
        "nodes": views.iter().map(|v| view(v, cat)).collect::<Vec<_>>(),
        "neighbors": neighbours.iter().map(node).collect::<Vec<_>>(),
        "options": fx.options().map(option).collect::<Vec<_>>(),
        "activeOption": fx.active,
        "decisionPoints": points.iter().map(point).collect::<Vec<_>>(),
        "at": at.to_rfc3339(),
    }))
}

/// What the graph adds to the blackboard of a change: the changes it depends on and what its organisation requires of
/// its criticality (Go's `FacetDependencies` and `Graph.CriticalityOf`). The history of the change (ADR 0109) is not
/// folded yet, and the graph keeps no supertypes nor variables on a blackboard.
pub async fn facets<T: ReadTx>(tx: &T, c: &Change) -> Result<Facets> {
    let deps = dependency::dependencies_of(tx, c).await?;
    let dependencies = deps
        .iter()
        .map(|d| {
            json!({"id": d.id.as_str(), "title": d.title, "methodology": d.methodology, "status": d.status,
                "state": d.state, "parent": d.parent_id.as_str(), "satisfied": d.satisfied, "reason": d.reason})
        })
        .collect();
    let level = criticality::of(c.data.as_ref());
    let policy = access::Snapshot::read(tx)
        .await?
        .criticality_policy(&c.owner_org.key, level);
    Ok(Facets {
        dependencies,
        criticality_policy: Some(policy),
        ..Facets::default()
    })
}
