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

pub fn time(t: &Option<DateTime<Utc>>) -> Option<goap_proto::wkt::Timestamp> {
    t.map(goap_proto::wkt::Timestamp::from)
}

pub fn value(v: &Json) -> pbjson_types::Value {
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

pub fn object(m: &serde_json::Map<String, Json>) -> pbjson_types::Struct {
    pbjson_types::Struct {
        fields: m.iter().map(|(k, v)| (k.clone(), value(v))).collect(),
    }
}

/// A JSON value of a protocol value, as Go's `structpb` reads it (numbers are float64).
pub fn json(v: &pbjson_types::Value) -> Json {
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
fn go_time(t: &DateTime<Utc>) -> Option<goap_proto::wkt::Timestamp> {
    (*t != graph_core::gojson::zero_time()).then(|| goap_proto::wkt::Timestamp::from(*t))
}

fn review(r: &graph_core::impact::Review) -> pb::Review {
    pb::Review {
        status: r.status.clone(),
        by: r.by.clone(),
        comment: r.comment.clone(),
        at: go_time(&r.at),
        flow: r.flow.clone(),
        execution: r.execution.clone(),
        superseded: r.superseded,
        review_id: r.review_id.clone(),
    }
}

/// An event of the impacts of a change (`pbconv.ImpactEventToPB`).
pub fn impact_event(e: &graph_core::impact::ImpactEvent) -> pb::ImpactEvent {
    pb::ImpactEvent {
        id: e.id.clone(),
        change_id: e.change.0.clone(),
        seq: e.seq as i32,
        impact_id: e.impact.clone(),
        op: e.op.as_str().to_string(),
        flow: e.flow.clone(),
        execution: e.execution.clone(),
        by: e.by.clone(),
        at: go_time(&e.at),
        post: e.post.as_ref().map(node_ref),
        pre: e.pre.as_ref().map(node_ref),
        committed: e.committed.as_ref().map(node_ref),
        stale: e.stale.clone(),
        state: e.state.as_ref().map(change_impact),
        review: e.review.as_ref().map(review),
    }
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
        reviews: cn.reviews.iter().map(review).collect(),
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

/// An item as its log entry holds it (`domain.ChangeItem` JSON), for the answer (`pbconv.ItemToPB`); a decision is
/// the Go service's.
pub fn item(v: &Json) -> Result<pb::ChangeItem, String> {
    let s = |k: &str| v.get(k).and_then(Json::as_str).unwrap_or_default().to_string();
    let list = |k: &str| {
        v.get(k)
            .and_then(Json::as_array)
            .map(|a| a.iter().filter_map(|x| x.as_str().map(str::to_string)).collect())
            .unwrap_or_default()
    };
    let created = v
        .get("createdAt")
        .and_then(Json::as_str)
        .and_then(|t| DateTime::parse_from_rfc3339(t).ok())
        .map(|t| t.with_timezone(&Utc));
    Ok(pb::ChangeItem {
        id: s("id"),
        r#type: s("type"),
        target: s("target"),
        status: s("status"),
        data: v
            .get("data")
            .and_then(Json::as_object)
            .filter(|m| !m.is_empty())
            .map(object),
        produced_by: s("producedBy"),
        derived_from: list("derivedFrom"),
        supersedes: list("supersedes"),
        created_at: created.as_ref().and_then(go_time),
        execution: s("execution"),
        flow: s("flow"),
        decision: v.get("decision").and_then(Json::as_object).map(|d| pb::Decision {
            item: d.get("item").and_then(Json::as_str).unwrap_or_default().into(),
            accept: d.get("accept").and_then(Json::as_bool).unwrap_or_default(),
            comment: d.get("comment").and_then(Json::as_str).unwrap_or_default().into(),
        }),
        decision_event: match v.get("decisionEvent") {
            Some(e) => Some(decision_event(
                &serde_json::from_value(e.clone()).map_err(|e| e.to_string())?,
            )),
            None => None,
        },
        flow_event: match v.get("flowEvent") {
            Some(e) => Some(flow_event(
                &serde_json::from_value(e.clone()).map_err(|e| e.to_string())?,
            )),
            None => None,
        },
    })
}

fn option(o: &Option<graph_core::flow::OptionSpec>) -> Option<pb::Option> {
    o.as_ref().map(|o| pb::Option {
        name: o.name.clone(),
        hypothesis: o.hypothesis.clone(),
    })
}

/// An event of a flow (`pbconv.FlowEventToPB`).
pub fn flow_event(e: &graph_core::flow::FlowEvent) -> pb::FlowEvent {
    pb::FlowEvent {
        op: e.op.clone(),
        flow: e.flow.clone(),
        parent: e.parent.clone(),
        fork_after: e.fork_after.clone(),
        stale: e.stale.clone(),
        by: e.by.clone(),
        option: option(&e.option),
        comment: e.comment.clone(),
        stale_runs: e.stale_runs.clone(),
        origin: (!e.origin.is_empty()).then(|| object(&e.origin)),
    }
}

/// A replayed flow (`pbconv.FlowToPB`).
pub fn flow(f: &graph_core::flow::Flow) -> pb::Flow {
    pb::Flow {
        id: f.id.clone(),
        parent: f.parent.clone(),
        fork_after: f.fork_after.clone(),
        status: f.status.clone(),
        stale: f.stale.clone(),
        opened_at: time(&f.opened_at),
        decided_at: time(&f.decided_at),
        decided_by: f.decided_by.clone(),
        competes_with: f.competes_with.clone(),
        option: option(&f.option),
        option_status: if f.option.is_some() {
            f.option_status().to_string()
        } else {
            String::new()
        },
        evaluation: f.evaluation.clone(),
        active: f.active,
        stale_runs: f.stale_runs.clone(),
        origin: (!f.origin.is_empty()).then(|| object(&f.origin)),
    }
}

/// An event of a decision point (`pbconv.DecisionEventToPB`).
pub fn decision_event(e: &graph_core::decision::DecisionEvent) -> pb::DecisionEvent {
    pb::DecisionEvent {
        op: e.op.clone(),
        point: e.point.clone(),
        question: e.question.clone(),
        options: e.options.clone(),
        criteria: e.criteria.clone(),
        outcome: e.outcome.clone(),
        option: e.option.clone(),
        confidence: e.confidence,
        justification: e.justification.clone(),
        questions: e.questions.clone(),
        human: e.human,
        question_id: e.question_id.clone(),
        answer: e.answer.clone(),
        process: e.process.clone(),
        accept: e.accept,
        comment: e.comment.clone(),
        by: e.by.clone(),
        policy: (!e.policy.is_empty()).then(|| object(&e.policy)),
    }
}

/// A replayed decision point (`pbconv.DecisionPointToPB`).
pub fn decision_point(d: &graph_core::decision::DecisionPoint) -> pb::DecisionPoint {
    pb::DecisionPoint {
        id: d.id.clone(),
        question: d.question.clone(),
        options: d.options.clone(),
        criteria: d.criteria.clone(),
        policy: (!d.policy.is_empty()).then(|| object(&d.policy)),
        opened_at: time(&d.opened_at),
        opened_by: d.opened_by.clone(),
        status: d.status.clone(),
        human_only: d.human_only,
        escalation: d.escalation.clone(),
        option: d.option.clone(),
        decided_at: time(&d.decided_at),
        decided_by: d.decided_by.clone(),
        questions: d
            .questions
            .iter()
            .map(|q| pb::Question {
                id: q.id.clone(),
                point: q.point.clone(),
                text: q.text.clone(),
                status: q.status.clone(),
                answer: q.answer.clone(),
                answered_by: q.answered_by.clone(),
                process: q.process.clone(),
                asked_at: time(&q.asked_at),
            })
            .collect(),
        ruling: d.ruling.as_ref().map(|r| pb::Ruling {
            outcome: r.outcome.clone(),
            option: r.option.clone(),
            confidence: r.confidence,
            justification: r.justification.clone(),
            by: r.by.clone(),
            human: r.human,
            at: time(&r.at),
        }),
    }
}

pub fn log_entry(e: &graph_core::impact::LogEntry) -> pb::LogEntry {
    pb::LogEntry {
        seq: e.seq,
        id: e.id.clone(),
        change_id: e.change.0.clone(),
        r#type: e.typ.clone(),
        flow: e.flow.clone(),
        process_id: e.process.clone(),
        execution: e.execution.clone(),
        subject: e.subject.clone(),
        by: e.by.clone(),
        at: time(&Some(e.at)),
        payload: e.payload.clone(),
        causation: e.causation.clone(),
    }
}

/// A merge plan (Go's `pbconv.MergePlanToPB`).
pub fn merge_plan(p: &graph_core::write::branchmerge::MergePlan) -> pb::MergePlan {
    pb::MergePlan {
        from: p.from.clone(),
        into: p.into.clone(),
        into_head: p.into_head.0.clone(),
        candidates: p
            .candidates
            .iter()
            .map(|c| pb::MergeCandidate {
                node: c.node.0.clone(),
                key: c.key.clone(),
                r#type: c.typ.clone(),
                kind: c.kind.clone(),
                ancestor: c.ancestor.as_ref().map(node_ref),
                ours: c.ours.as_ref().map(node_ref),
                theirs: Some(node_ref(&c.theirs)),
                deleted: c.deleted,
                base: props(&c.base),
                ours_props: props(&c.ours_props),
                theirs_props: props(&c.theirs_props),
                merged: props(&c.merged),
                conflicts: c.conflicts.clone(),
            })
            .collect(),
    }
}

/// A flow event a writer gives (`pbconv.FlowEventFromPB`).
pub fn flow_event_from(e: pb::FlowEvent) -> graph_core::flow::FlowEvent {
    graph_core::flow::FlowEvent {
        op: e.op,
        flow: e.flow,
        parent: e.parent,
        fork_after: e.fork_after,
        stale: e.stale,
        stale_runs: e.stale_runs,
        origin: e.origin.as_ref().map(map).unwrap_or_default(),
        by: e.by,
        option: e.option.map(|o| graph_core::flow::OptionSpec {
            name: o.name,
            hypothesis: o.hypothesis,
            ..Default::default()
        }),
        comment: e.comment,
    }
}

/// A decision event a writer gives (`pbconv.DecisionEventFromPB`: the seat is not read).
pub fn decision_event_from(e: pb::DecisionEvent) -> graph_core::decision::DecisionEvent {
    graph_core::decision::DecisionEvent {
        op: e.op,
        point: e.point,
        question: e.question,
        options: e.options,
        criteria: e.criteria,
        policy: e.policy.as_ref().map(map).unwrap_or_default(),
        outcome: e.outcome,
        option: e.option,
        confidence: e.confidence,
        justification: e.justification,
        questions: e.questions,
        human: e.human,
        question_id: e.question_id,
        answer: e.answer,
        process: e.process,
        accept: e.accept,
        comment: e.comment,
        by: e.by,
        role: String::new(),
    }
}
