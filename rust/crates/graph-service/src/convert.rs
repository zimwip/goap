//! The model to and from the protocol, as `lib/pbconv` converts it for the Go service.
use std::collections::HashMap;

use chrono::{DateTime, Utc};
use goap_proto::graph::v1 as pb;
use graph_core::{
    change::{BaselineDiff, Change, StackChange},
    impact::ChangeImpact,
    lnk::Url,
    model::{Baseline, Branch, Link, Node, NodeRef, Props, Target, TypeCount, branch_of, namespace_of},
    view::View,
    write::work::LinkWrite,
};
use pbjson_types::value::Kind;
use serde_json::Value as Json;

pub fn time(t: &Option<DateTime<Utc>>) -> Option<pbjson_types::Timestamp> {
    t.map(pbjson_types::Timestamp::from)
}

fn value(v: &Json) -> pbjson_types::Value {
    let kind = match v {
        Json::Null => Kind::NullValue(0),
        Json::Bool(b) => Kind::BoolValue(*b),
        Json::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or_default()),
        Json::String(s) => Kind::StringValue(s.clone()),
        Json::Array(xs) => Kind::ListValue(pbjson_types::ListValue {
            values: xs.iter().map(value).collect(),
        }),
        Json::Object(m) => Kind::StructValue(object(m)),
    };
    pbjson_types::Value { kind: Some(kind) }
}

fn object(m: &serde_json::Map<String, Json>) -> pbjson_types::Struct {
    pbjson_types::Struct {
        fields: m.iter().map(|(k, v)| (k.clone(), value(v))).collect(),
    }
}

/// A JSON value of a protocol value, as Go's `structpb` reads it (numbers are float64).
fn json(v: &pbjson_types::Value) -> Json {
    match &v.kind {
        None | Some(Kind::NullValue(_)) => Json::Null,
        Some(Kind::BoolValue(b)) => Json::Bool(*b),
        Some(Kind::NumberValue(n)) => serde_json::Number::from_f64(*n).map_or(Json::Null, Json::Number),
        Some(Kind::StringValue(s)) => Json::String(s.clone()),
        Some(Kind::ListValue(l)) => Json::Array(l.values.iter().map(json).collect()),
        Some(Kind::StructValue(s)) => Json::Object(map(s)),
    }
}

/// The map of a Struct (`pbconv.Map`).
pub fn map(s: &pbjson_types::Struct) -> serde_json::Map<String, Json> {
    s.fields.iter().map(|(k, v)| (k.clone(), json(v))).collect()
}

/// Properties as a Struct: none when empty.
pub fn props(p: &Props) -> Option<pbjson_types::Struct> {
    p.as_ref().filter(|m| !m.is_empty()).map(object)
}

pub fn node_ref(r: &NodeRef) -> pb::NodeRef {
    pb::NodeRef {
        id: r.id.0.clone(),
        version: r.version,
    }
}

pub fn ref_from(r: Option<&pb::NodeRef>) -> NodeRef {
    r.map(|r| NodeRef::new(r.id.as_str().into(), r.version))
        .unwrap_or_default()
}

fn url(u: &Url) -> String {
    u.to_string()
}

pub fn node(n: &Node) -> pb::Node {
    pb::Node {
        id: n.id.0.clone(),
        version: n.version,
        namespace: n.namespace.clone(),
        key: n.key.clone(),
        r#type: n.typ.clone(),
        props: props(&n.props),
        deleted: n.deleted,
        change_id: n.change_id.0.clone(),
        created_at: time(&n.created_at),
        branch: branch_of(&n.branch).to_string(),
        parents: n.parents.clone(),
        reason: n.reason.clone(),
        state: n.state.clone(),
        change_impact: n.change_impact.clone(),
        comment: n.comment.clone(),
        execution: n.execution.clone(),
        joined: n.joined.clone(),
        owner: url(&n.owner),
        project: url(&n.project),
        draft: n.version == 0,
        origins: n.origins.iter().map(node_ref).collect(),
    }
}

pub fn nodes(ns: &[Node]) -> Vec<pb::Node> {
    ns.iter().map(node).collect()
}

pub fn link(l: &Link) -> pb::Link {
    let (to, target) = match &l.to {
        Target::Node(r) => (Some(node_ref(r)), String::new()),
        Target::External(u) => (None, u.to_string()),
    };
    pb::Link {
        id: l.id.0.clone(),
        r#type: l.typ.clone(),
        from: Some(node_ref(&l.from)),
        to,
        props: props(&l.props),
        change_id: l.change_id.0.clone(),
        target,
    }
}

pub fn links(ls: &[Link]) -> Vec<pb::Link> {
    ls.iter().map(link).collect()
}

/// A baseline: its header, and its state when read whole.
pub fn baseline(b: &Baseline, state: Option<HashMap<String, i32>>) -> pb::Baseline {
    pb::Baseline {
        id: b.id.0.clone(),
        name: b.name.clone(),
        parent_id: b.parent_id.0.clone(),
        change_id: b.change_id.0.clone(),
        nodes: state.unwrap_or_default(),
        created_at: time(&b.created_at),
        branch: branch_of(&b.branch).to_string(),
        namespace: namespace_of(&b.namespace).to_string(),
        merged_from: b.merged_from.0.clone(),
        kind: b.kind.clone(),
        rebased_from: b.rebased_from.0.clone(),
    }
}

pub fn branch(b: &Branch) -> pb::Branch {
    pb::Branch {
        name: b.name.clone(),
        parent: b.parent.clone(),
        fork_baseline: b.fork_baseline.0.clone(),
        head: b.head.0.clone(),
        origin: b.origin.clone(),
        status: b.status.clone(),
        created_at: time(&b.created_at),
        namespace: b.namespace.clone(),
        description: b.description.clone(),
        rebased_from: b.rebased_from.0.clone(),
    }
}

pub fn type_counts(cs: &[TypeCount]) -> Vec<pb::TypeCount> {
    cs.iter()
        .map(|c| pb::TypeCount {
            r#type: c.typ.clone(),
            count: c.count as i32,
        })
        .collect()
}

/// The view of a read; none is the zero view, which the reads refuse.
pub fn view(v: Option<&pb::View>) -> View {
    v.map(|v| View {
        namespace: v.namespace.clone(),
        branch: v.branch.clone(),
        baseline: v.baseline_id.as_str().into(),
        change: v.change_id.as_str().into(),
        flow: v.flow.clone(),
    })
    .unwrap_or_default()
}

/// A change header: no impacts, no items (`pbconv.ChangeToPB` of a head).
pub fn change(c: &Change) -> pb::Change {
    pb::Change {
        id: c.id.0.clone(),
        title: c.title.clone(),
        intent: c.intent.clone(),
        methodology: c.methodology.clone(),
        goal: c.goal.clone(),
        status: c.status.clone(),
        baseline_id: c.baseline_id.0.clone(),
        result_baseline_id: c.result_baseline_id.0.clone(),
        data: props(&c.data),
        items: Vec::new(),
        created_at: time(&c.created_at),
        branch: branch_of(&c.branch).to_string(),
        namespace: c.namespace.clone(),
        parent_id: c.parent_id.0.clone(),
        owner_org: url(&c.owner_org),
        nodes: Vec::new(),
        project_id: url(&c.project_id),
        lifecycle: c.lifecycle.clone(),
        state: c.state.clone(),
        guardians: c
            .guardians
            .iter()
            .map(|g| pb::GuardianRef {
                methodology: g.methodology.clone(),
                version: g.version.clone(),
            })
            .collect(),
        depends_on: c.depends_on.iter().map(|d| d.0.clone()).collect(),
        methodology_version: c.methodology_version.clone(),
        seq: 0,
    }
}

pub fn changes(cs: &[Change]) -> Vec<pb::Change> {
    cs.iter().map(change).collect()
}

pub fn stack_changes(cs: &[StackChange]) -> Vec<pb::StackChange> {
    cs.iter()
        .map(|c| pb::StackChange {
            id: c.id.0.clone(),
            parent: c.parent.0.clone(),
            title: c.title.clone(),
            status: c.status.clone(),
        })
        .collect()
}

pub fn baseline_diff(d: &BaselineDiff) -> pb::BaselineDiff {
    pb::BaselineDiff {
        node: d.node.0.clone(),
        key: d.key.clone(),
        r#type: d.typ.clone(),
        kind: d.kind.to_string(),
        from: d.from.as_ref().map(node),
        to: d.to.as_ref().map(node),
    }
}

/// A time as `pbconv.Time` writes it: none for the zero time.
fn go_time(t: &DateTime<Utc>) -> Option<pbjson_types::Timestamp> {
    (*t != graph_core::gojson::zero_time()).then(|| pbjson_types::Timestamp::from(*t))
}

/// A change impact (`pbconv.ChangeImpactToPB`).
pub fn change_impact(cn: &ChangeImpact) -> pb::ChangeImpact {
    pb::ChangeImpact {
        id: cn.id.clone(),
        key: cn.key.clone(),
        r#type: cn.typ.clone(),
        intent: cn.intent.clone(),
        rationale: cn.rationale.clone(),
        pre: cn.pre.as_ref().map(node_ref),
        post: cn.post.as_ref().map(node_ref),
        committed: cn.committed.as_ref().map(node_ref),
        review: cn.review.clone(),
        reviews: cn
            .reviews
            .iter()
            .map(|r| pb::Review {
                status: r.status.clone(),
                by: r.by.clone(),
                comment: r.comment.clone(),
                at: go_time(&r.at),
                flow: r.flow.clone(),
                execution: r.execution.clone(),
                superseded: r.superseded,
                review_id: r.review_id.clone(),
            })
            .collect(),
        via: cn.via.clone(),
        recheck: cn.recheck,
        produced_by: cn.produced_by.clone(),
        derived_from: cn.derived_from.clone(),
        execution: cn.execution.clone(),
        items: cn.items.clone(),
        created_at: go_time(&cn.created_at),
        flow: cn.flow.clone(),
        superseded: cn.superseded,
        validation: None,
    }
}

/// The links a write gives a draft (`linkWrites`); a target URL that does not parse is refused.
pub fn link_writes(ls: &[pb::NodeLinkWrite]) -> Result<Vec<LinkWrite>, String> {
    ls.iter()
        .map(|l| link_write(&l.r#type, l.to.as_ref(), &l.target, l.props.as_ref()))
        .collect()
}

pub fn link_write(
    typ: &str,
    to: Option<&pb::NodeRef>,
    target: &str,
    props: Option<&pbjson_types::Struct>,
) -> Result<LinkWrite, String> {
    let target = if target.is_empty() {
        Url::default()
    } else {
        Url::parse(target).map_err(|e| e.to_string())?
    };
    Ok(LinkWrite {
        typ: typ.to_string(),
        to: ref_from(to),
        target,
        props: props.map(map).filter(|m| !m.is_empty()),
    })
}
