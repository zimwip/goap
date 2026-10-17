//! Package pbconv converts between domain types and their protobuf messages.

use std::collections::{BTreeMap, HashMap};

use chrono::{DateTime, Utc};
use goap_domain::{
    self as domain, BaselineID, ChangeID, ChangeImpactID, ChangeStatus, ItemID, ItemStatus,
    ItemType, LinkID, NodeID, NodeIntent, NodeReview, TagID, Version, gotime,
};
use goap_gen::graph::v1 as pb;
use goap_graphapi as graphapi;
use goap_typecat as typecat;
use pbjson_types::value::Kind;
use serde_json::{Map, Value};

/// Struct converts a map to a protobuf Struct (None for an empty map).
pub fn to_struct(m: &Map<String, Value>) -> Option<pbjson_types::Struct> {
    if m.is_empty() {
        return None;
    }
    Some(struct_of(m))
}

fn struct_of(m: &Map<String, Value>) -> pbjson_types::Struct {
    pbjson_types::Struct {
        fields: m
            .iter()
            .map(|(k, v)| (k.clone(), json_to_value(v)))
            .collect::<HashMap<_, _>>(),
    }
}

/// Map converts a protobuf Struct to a map (empty for None).
pub fn to_map(s: Option<&pbjson_types::Struct>) -> Map<String, Value> {
    match s {
        Some(s) if !s.fields.is_empty() => struct_to_map(s),
        _ => Map::new(),
    }
}

/// The map of a Struct (`AsMap`).
pub fn struct_to_map(s: &pbjson_types::Struct) -> Map<String, Value> {
    s.fields
        .iter()
        .map(|(k, v)| (k.clone(), value_to_json(v)))
        .collect()
}

/// A JSON value as a protobuf Value (`structpb.NewValue`).
pub fn json_to_value(v: &Value) -> pbjson_types::Value {
    let kind = match v {
        Value::Null => Kind::NullValue(0),
        Value::Bool(b) => Kind::BoolValue(*b),
        Value::Number(n) => Kind::NumberValue(n.as_f64().unwrap_or_default()),
        Value::String(s) => Kind::StringValue(s.clone()),
        Value::Array(a) => Kind::ListValue(pbjson_types::ListValue {
            values: a.iter().map(json_to_value).collect(),
        }),
        Value::Object(m) => Kind::StructValue(struct_of(m)),
    };
    pbjson_types::Value { kind: Some(kind) }
}

/// A protobuf Value as a JSON value (`AsInterface`): a number is a float64 in Go, written as an integer when it is
/// one, so it reads here as an integer when it has no fraction (and fits a float64 exactly).
pub fn value_to_json(v: &pbjson_types::Value) -> Value {
    match &v.kind {
        None | Some(Kind::NullValue(_)) => Value::Null,
        Some(Kind::BoolValue(b)) => Value::Bool(*b),
        Some(Kind::NumberValue(f)) => number(*f),
        Some(Kind::StringValue(s)) => Value::String(s.clone()),
        Some(Kind::ListValue(l)) => Value::Array(l.values.iter().map(value_to_json).collect()),
        Some(Kind::StructValue(s)) => Value::Object(struct_to_map(s)),
    }
}

fn number(f: f64) -> Value {
    const EXACT: f64 = 9_007_199_254_740_992.0; // 2^53
    if f.is_finite() && f.fract() == 0.0 && f.abs() <= EXACT && !(f == 0.0 && f.is_sign_negative())
    {
        return Value::from(f as i64);
    }
    serde_json::Number::from_f64(f).map_or(Value::Null, Value::Number)
}

/// Time converts a timestamp (None for Go's zero time).
pub fn time(t: &DateTime<Utc>) -> Option<pbjson_types::Timestamp> {
    if gotime::is_zero(t) {
        None
    } else {
        Some((*t).into())
    }
}

/// Time of an optional time (None: Go's zero time).
pub fn opt_time(t: &Option<DateTime<Utc>>) -> Option<pbjson_types::Timestamp> {
    t.as_ref().and_then(time)
}

/// FromTime converts a protobuf timestamp (Go's zero time for None).
pub fn from_time(t: Option<&pbjson_types::Timestamp>) -> DateTime<Utc> {
    t.and_then(|t| DateTime::<Utc>::try_from(*t).ok())
        .unwrap_or_else(gotime::zero)
}

/// FromTime of an optional time (None for None or Go's zero time).
pub fn from_opt_time(t: Option<&pbjson_types::Timestamp>) -> Option<DateTime<Utc>> {
    let t = from_time(t);
    (!gotime::is_zero(&t)).then_some(t)
}

pub fn ref_to_pb(r: &domain::NodeRef) -> pb::NodeRef {
    pb::NodeRef {
        id: r.id.to_string(),
        version: r.version as i32,
    }
}

pub fn ref_ptr_to_pb(r: Option<&domain::NodeRef>) -> Option<pb::NodeRef> {
    r.map(ref_to_pb)
}

pub fn ref_from_pb(r: Option<&pb::NodeRef>) -> domain::NodeRef {
    match r {
        None => domain::NodeRef::default(),
        Some(r) => domain::NodeRef {
            id: NodeID::new(r.id.clone()),
            version: Version::from(r.version),
        },
    }
}

pub fn ref_ptr_from_pb(r: Option<&pb::NodeRef>) -> Option<domain::NodeRef> {
    match r {
        Some(x) if !x.id.is_empty() => Some(ref_from_pb(r)),
        _ => None,
    }
}

pub fn node_to_pb(n: &domain::Node) -> pb::Node {
    pb::Node {
        id: n.id.to_string(),
        version: n.version as i32,
        namespace: n.namespace.clone(),
        key: n.key.clone(),
        r#type: n.r#type.clone(),
        props: to_struct(&n.properties),
        deleted: n.deleted,
        change_id: n.change_id.to_string(),
        created_at: time(&n.created_at),
        branch: domain::branch_of(&n.branch).to_string(),
        parents: n.parents.iter().map(|v| *v as i32).collect(),
        reason: n.reason.clone(),
        state: n.state.clone(),
        change_impact: n.change_impact.to_string(),
        comment: n.comment.clone(),
        execution: n.execution.clone(),
        joined: n.joined.clone(),
        owner: n.owner.to_string(),
        project: n.project.to_string(),
        draft: n.is_draft(),
        origins: refs_to_pb(&n.origins),
    }
}

fn refs_to_pb(rs: &[domain::NodeRef]) -> Vec<pb::NodeRef> {
    rs.iter().map(ref_to_pb).collect()
}

fn refs_from_pb(rs: &[pb::NodeRef]) -> Vec<domain::NodeRef> {
    rs.iter().map(|r| ref_from_pb(Some(r))).collect()
}

pub fn node_from_pb(n: &pb::Node) -> domain::Node {
    domain::Node {
        id: NodeID::new(n.id.clone()),
        version: Version::from(n.version),
        namespace: n.namespace.clone(),
        key: n.key.clone(),
        r#type: n.r#type.clone(),
        properties: to_map(n.props.as_ref()),
        deleted: n.deleted,
        change_id: ChangeID::new(n.change_id.clone()),
        created_at: from_time(n.created_at.as_ref()),
        branch: n.branch.clone(),
        parents: n.parents.iter().map(|v| Version::from(*v)).collect(),
        reason: n.reason.clone(),
        state: n.state.clone(),
        change_impact: ChangeImpactID::new(n.change_impact.clone()),
        comment: n.comment.clone(),
        execution: n.execution.clone(),
        joined: n.joined.clone(),
        owner: NodeID::new(n.owner.clone()),
        project: NodeID::new(n.project.clone()),
        origins: refs_from_pb(&n.origins),
    }
}

/// NodeFromPB of an optional message (the zero node for None).
pub fn opt_node_from_pb(n: Option<&pb::Node>) -> domain::Node {
    n.map(node_from_pb).unwrap_or_default()
}

pub fn nodes_to_pb(ns: &[domain::Node]) -> Vec<pb::Node> {
    ns.iter().map(node_to_pb).collect()
}

pub fn nodes_from_pb(ns: &[pb::Node]) -> Vec<domain::Node> {
    ns.iter().map(node_from_pb).collect()
}

pub fn link_to_pb(l: &domain::Link) -> pb::Link {
    pb::Link {
        id: l.id.to_string(),
        r#type: l.r#type.clone(),
        from: Some(ref_to_pb(&l.from)),
        to: Some(ref_to_pb(&l.to)),
        props: to_struct(&l.properties),
        change_id: l.change_id.to_string(),
    }
}

pub fn link_from_pb(l: &pb::Link) -> domain::Link {
    domain::Link {
        id: LinkID::new(l.id.clone()),
        r#type: l.r#type.clone(),
        from: ref_from_pb(l.from.as_ref()),
        to: ref_from_pb(l.to.as_ref()),
        properties: to_map(l.props.as_ref()),
        change_id: ChangeID::new(l.change_id.clone()),
    }
}

pub fn links_to_pb(ls: &[domain::Link]) -> Vec<pb::Link> {
    ls.iter().map(link_to_pb).collect()
}

pub fn links_from_pb(ls: &[pb::Link]) -> Vec<domain::Link> {
    ls.iter().map(link_from_pb).collect()
}

pub fn view_to_pb(v: &domain::NodeView) -> pb::NodeView {
    pb::NodeView {
        node: Some(node_to_pb(&v.node)),
        latest: v.latest as i32,
        out: links_to_pb(&v.out),
        r#in: links_to_pb(&v.r#in),
        not_landable: v.not_landable,
    }
}

pub fn view_from_pb(v: &pb::NodeView) -> domain::NodeView {
    domain::NodeView {
        node: opt_node_from_pb(v.node.as_ref()),
        latest: Version::from(v.latest),
        out: links_from_pb(&v.out),
        r#in: links_from_pb(&v.r#in),
        not_landable: v.not_landable,
    }
}

pub fn baseline_to_pb(b: &domain::Baseline) -> pb::Baseline {
    pb::Baseline {
        id: b.id.to_string(),
        name: b.name.clone(),
        parent_id: b.parent_id.to_string(),
        change_id: b.change_id.to_string(),
        nodes: b
            .nodes
            .iter()
            .map(|(id, v)| (id.to_string(), *v as i32))
            .collect(),
        created_at: time(&b.created_at),
        branch: domain::branch_of(&b.branch).to_string(),
        namespace: domain::namespace_of(&b.namespace).to_string(),
        merged_from: b.merged_from.to_string(),
        kind: b.kind.clone(),
        rebased_from: b.rebased_from.to_string(),
    }
}

pub fn tag_to_pb(t: &domain::Tag) -> pb::Tag {
    pb::Tag {
        id: t.id.to_string(),
        name: t.name.clone(),
        namespace: t.namespace.clone(),
        change_id: t.change_id.to_string(),
        baseline_id: t.baseline_id.to_string(),
        by: t.by.clone(),
        created_at: time(&t.created_at),
    }
}

pub fn tag_from_pb(t: &pb::Tag) -> domain::Tag {
    domain::Tag {
        id: TagID::new(t.id.clone()),
        name: t.name.clone(),
        namespace: t.namespace.clone(),
        change_id: ChangeID::new(t.change_id.clone()),
        baseline_id: BaselineID::new(t.baseline_id.clone()),
        by: t.by.clone(),
        created_at: from_time(t.created_at.as_ref()),
    }
}

pub fn baseline_from_pb(b: &pb::Baseline) -> domain::Baseline {
    domain::Baseline {
        id: BaselineID::new(b.id.clone()),
        name: b.name.clone(),
        parent_id: BaselineID::new(b.parent_id.clone()),
        change_id: ChangeID::new(b.change_id.clone()),
        nodes: b
            .nodes
            .iter()
            .map(|(id, v)| (NodeID::new(id.clone()), Version::from(*v)))
            .collect::<BTreeMap<_, _>>(),
        created_at: from_time(b.created_at.as_ref()),
        branch: b.branch.clone(),
        namespace: b.namespace.clone(),
        merged_from: BaselineID::new(b.merged_from.clone()),
        kind: b.kind.clone(),
        rebased_from: BaselineID::new(b.rebased_from.clone()),
        ..Default::default()
    }
}

pub(crate) fn ids_to_pb<T: AsRef<str>>(ids: &[T]) -> Vec<String> {
    ids.iter().map(|id| id.as_ref().to_string()).collect()
}

pub(crate) fn ids_from_pb<T: From<String>>(ids: &[String]) -> Vec<T> {
    ids.iter().map(|id| T::from(id.clone())).collect()
}

pub fn item_to_pb(it: &domain::ChangeItem) -> pb::ChangeItem {
    pb::ChangeItem {
        id: it.id.to_string(),
        r#type: it.r#type.to_string(),
        target: it.target.clone(),
        status: it.status.to_string(),
        data: to_struct(&it.data),
        produced_by: it.produced_by.clone(),
        created_at: time(&it.created_at),
        derived_from: ids_to_pb(&it.derived_from),
        supersedes: ids_to_pb(&it.supersedes),
        execution: it.execution.clone(),
        flow: it.flow.clone(),
        flow_event: it.flow_event.as_ref().map(crate::flow_event_to_pb),
        decision_event: it.decision_event.as_ref().map(crate::decision_event_to_pb),
        decision: it.decision.as_ref().map(|d| pb::Decision {
            item: d.item.to_string(),
            accept: d.accept,
            comment: d.comment.clone(),
        }),
    }
}

pub fn item_from_pb(it: &pb::ChangeItem) -> domain::ChangeItem {
    domain::ChangeItem {
        id: ItemID::new(it.id.clone()),
        r#type: ItemType::new(it.r#type.clone()),
        target: it.target.clone(),
        status: ItemStatus::new(it.status.clone()),
        data: to_map(it.data.as_ref()),
        produced_by: it.produced_by.clone(),
        created_at: from_time(it.created_at.as_ref()),
        derived_from: ids_from_pb(&it.derived_from),
        supersedes: ids_from_pb(&it.supersedes),
        execution: it.execution.clone(),
        flow: it.flow.clone(),
        flow_event: it.flow_event.as_ref().map(crate::flow_event_from_pb),
        decision_event: it
            .decision_event
            .as_ref()
            .map(crate::decision_event_from_pb),
        decision: it.decision.as_ref().map(|d| domain::Decision {
            item: ItemID::new(d.item.clone()),
            accept: d.accept,
            comment: d.comment.clone(),
        }),
        ..Default::default()
    }
}

pub fn items_to_pb(its: &[domain::ChangeItem]) -> Vec<pb::ChangeItem> {
    its.iter().map(item_to_pb).collect()
}

pub fn items_from_pb(its: &[pb::ChangeItem]) -> Vec<domain::ChangeItem> {
    its.iter().map(item_from_pb).collect()
}

pub fn change_to_pb(c: &domain::Change) -> pb::Change {
    pb::Change {
        id: c.id.to_string(),
        title: c.title.clone(),
        intent: c.intent.clone(),
        methodology: c.methodology.clone(),
        goal: c.goal.clone(),
        namespace: c.namespace.clone(),
        parent_id: c.parent_id.to_string(),
        owner_org: c.owner_org.clone(),
        status: c.status.to_string(),
        baseline_id: c.baseline_id.to_string(),
        result_baseline_id: c.result_baseline_id.to_string(),
        data: to_struct(&c.data),
        items: items_to_pb(&c.items),
        created_at: time(&c.created_at),
        branch: domain::branch_of(&c.branch).to_string(),
        nodes: change_impacts_to_pb(&c.nodes),
        project_id: c.project_id.clone(),
        lifecycle: c.lifecycle.clone(),
        state: c.state.clone(),
        guardians: guardians_to_pb(&c.guardians),
        depends_on: ids_to_pb(&c.depends_on),
        methodology_version: c.methodology_version.clone(),
    }
}

pub fn change_from_pb(c: &pb::Change) -> domain::Change {
    domain::Change {
        id: ChangeID::new(c.id.clone()),
        title: c.title.clone(),
        intent: c.intent.clone(),
        methodology: c.methodology.clone(),
        goal: c.goal.clone(),
        namespace: c.namespace.clone(),
        parent_id: ChangeID::new(c.parent_id.clone()),
        owner_org: c.owner_org.clone(),
        status: ChangeStatus::new(c.status.clone()),
        baseline_id: BaselineID::new(c.baseline_id.clone()),
        result_baseline_id: BaselineID::new(c.result_baseline_id.clone()),
        data: to_map(c.data.as_ref()),
        items: items_from_pb(&c.items),
        created_at: from_time(c.created_at.as_ref()),
        branch: c.branch.clone(),
        nodes: change_impacts_from_pb(&c.nodes),
        project_id: c.project_id.clone(),
        lifecycle: c.lifecycle.clone(),
        state: c.state.clone(),
        guardians: guardians_from_pb(&c.guardians),
        depends_on: change_ids_from_pb(&c.depends_on),
        methodology_version: c.methodology_version.clone(),
        ..Default::default()
    }
}

/// ChangeFromPB of an optional message (the zero change for None).
pub fn opt_change_from_pb(c: Option<&pb::Change>) -> domain::Change {
    c.map(change_from_pb).unwrap_or_default()
}

pub fn branch_to_pb(b: &domain::Branch) -> pb::Branch {
    pb::Branch {
        name: b.name.clone(),
        parent: b.parent.clone(),
        fork_baseline: b.fork_baseline.to_string(),
        head: b.head.to_string(),
        origin: b.origin.clone(),
        status: b.status.clone(),
        created_at: time(&b.created_at),
        namespace: b.namespace.clone(),
        description: b.description.clone(),
        rebased_from: b.rebased_from.to_string(),
    }
}

fn review_to_pb(r: &domain::Review) -> pb::Review {
    pb::Review {
        status: r.status.to_string(),
        by: r.by.clone(),
        comment: r.comment.clone(),
        at: time(&r.at),
        flow: r.flow.clone(),
        execution: r.execution.clone(),
        superseded: r.superseded,
        review_id: r.review_id.clone(),
    }
}

pub fn change_impact_to_pb(cn: &domain::ChangeImpact) -> pb::ChangeImpact {
    pb::ChangeImpact {
        id: cn.id.to_string(),
        key: cn.key.clone(),
        r#type: cn.r#type.clone(),
        intent: cn.intent.to_string(),
        rationale: cn.rationale.clone(),
        pre: ref_ptr_to_pb(cn.pre.as_ref()),
        post: ref_ptr_to_pb(cn.post.as_ref()),
        landed: ref_ptr_to_pb(cn.landed.as_ref()),
        review: cn.review.to_string(),
        reviews: cn.reviews.iter().map(review_to_pb).collect(),
        via: cn.via.to_string(),
        recheck: cn.recheck,
        produced_by: cn.produced_by.clone(),
        derived_from: ids_to_pb(&cn.derived_from),
        execution: cn.execution.clone(),
        items: ids_to_pb(&cn.items),
        created_at: time(&cn.created_at),
        flow: cn.flow.clone(),
        superseded: cn.superseded,
        validation: cn.validation.as_ref().map(|v| pb::ImpactValidation {
            valid: v.valid,
            message: v.message.clone(),
            modified: v.modified,
        }),
    }
}

pub fn change_impact_from_pb(cn: &pb::ChangeImpact) -> domain::ChangeImpact {
    domain::ChangeImpact {
        id: ChangeImpactID::new(cn.id.clone()),
        key: cn.key.clone(),
        r#type: cn.r#type.clone(),
        intent: NodeIntent::new(cn.intent.clone()),
        rationale: cn.rationale.clone(),
        pre: ref_ptr_from_pb(cn.pre.as_ref()),
        post: ref_ptr_from_pb(cn.post.as_ref()),
        landed: ref_ptr_from_pb(cn.landed.as_ref()),
        review: NodeReview::new(cn.review.clone()),
        reviews: cn
            .reviews
            .iter()
            .map(|r| domain::Review {
                status: NodeReview::new(r.status.clone()),
                by: r.by.clone(),
                comment: r.comment.clone(),
                at: from_time(r.at.as_ref()),
                flow: r.flow.clone(),
                execution: r.execution.clone(),
                superseded: r.superseded,
                review_id: r.review_id.clone(),
            })
            .collect(),
        via: ChangeImpactID::new(cn.via.clone()),
        recheck: cn.recheck,
        produced_by: cn.produced_by.clone(),
        derived_from: ids_from_pb(&cn.derived_from),
        execution: cn.execution.clone(),
        items: ids_from_pb(&cn.items),
        created_at: from_time(cn.created_at.as_ref()),
        flow: cn.flow.clone(),
        superseded: cn.superseded,
        validation: cn.validation.as_ref().map(|v| domain::ImpactValidation {
            valid: v.valid,
            message: v.message.clone(),
            modified: v.modified,
        }),
    }
}

pub fn change_impacts_to_pb(cns: &[domain::ChangeImpact]) -> Vec<pb::ChangeImpact> {
    cns.iter().map(change_impact_to_pb).collect()
}

pub fn change_impacts_from_pb(cns: &[pb::ChangeImpact]) -> Vec<domain::ChangeImpact> {
    cns.iter().map(change_impact_from_pb).collect()
}

pub fn edits_to_pb(edits: &[graphapi::NodeEdit]) -> Vec<pb::NodeEdit> {
    edits
        .iter()
        .map(|e| pb::NodeEdit {
            key: e.key.clone(),
            r#type: e.r#type.clone(),
            pre: ref_ptr_to_pb(e.pre.as_ref()),
            props: to_struct(&e.props),
            rationale: e.rationale.clone(),
            owner: e.owner.clone(),
            state: e.state.clone(),
            links: e
                .links
                .iter()
                .map(|l| pb::LinkEdit {
                    r#type: l.r#type.clone(),
                    to: ref_ptr_to_pb(l.to.as_ref()),
                    to_key: l.to_key.clone(),
                    props: to_struct(&l.props),
                })
                .collect(),
            remove_links: ids_to_pb(&e.remove_links),
        })
        .collect()
}

pub fn edits_from_pb(edits: &[pb::NodeEdit]) -> Vec<graphapi::NodeEdit> {
    edits
        .iter()
        .map(|pe| graphapi::NodeEdit {
            key: pe.key.clone(),
            r#type: pe.r#type.clone(),
            pre: ref_ptr_from_pb(pe.pre.as_ref()),
            props: to_map(pe.props.as_ref()),
            rationale: pe.rationale.clone(),
            owner: pe.owner.clone(),
            state: pe.state.clone(),
            links: pe
                .links
                .iter()
                .map(|l| graphapi::LinkEdit {
                    r#type: l.r#type.clone(),
                    to: ref_ptr_from_pb(l.to.as_ref()),
                    to_key: l.to_key.clone(),
                    props: to_map(l.props.as_ref()),
                })
                .collect(),
            remove_links: ids_from_pb(&pe.remove_links),
        })
        .collect()
}

/// ImpactEventToPB converts an event of the impact log (ADR 0029).
pub fn impact_event_to_pb(e: &domain::ImpactEvent) -> pb::ImpactEvent {
    pb::ImpactEvent {
        id: e.id.clone(),
        change_id: e.change.to_string(),
        seq: e.seq as i32,
        impact_id: e.impact.to_string(),
        op: e.op.to_string(),
        flow: e.flow.clone(),
        execution: e.execution.clone(),
        by: e.by.clone(),
        at: opt_time(&e.at),
        post: ref_ptr_to_pb(e.post.as_ref()),
        pre: ref_ptr_to_pb(e.pre.as_ref()),
        landed: ref_ptr_to_pb(e.landed.as_ref()),
        stale: e.stale.clone(),
        state: e.state.as_ref().map(change_impact_to_pb),
        review: e.review.as_ref().map(review_to_pb),
    }
}

/// ItemTypesToPB converts the item types the domains in force declare (ADR 0128), with what the code of this process
/// adds to them (the permission asked of their writer, whether a meaning is coded).
pub fn item_types_to_pb(types: &[&typecat::ItemType]) -> pb::ListItemTypesResponse {
    let mut out = pb::ListItemTypesResponse::default();
    for x in types {
        let t = ItemType::new(x.name.clone());
        let mut info = pb::ItemTypeInfo {
            r#type: x.name.clone(),
            domain: x.domain.clone(),
            domain_version: x.version.clone(),
            description: x.description.clone(),
            code: domain::has_item_code(&t),
            ..Default::default()
        };
        if let Some(p) = domain::item_permission_of(&t) {
            info.permission = p.permission;
            info.subject_field = p.subject_field;
        }
        out.types.push(info);
    }
    out
}

/// StructuresToPB converts the structures of the graph (ADR 0054).
pub fn structures_to_pb(s: &domain::Structures) -> pb::GetStructuresResponse {
    pb::GetStructuresResponse {
        structures: s
            .iter()
            .map(|x| pb::Structure {
                kind: x.kind.clone(),
                r#type: x.r#type.clone(),
                namespace: x.namespace.clone(),
                parent: x.parent.clone(),
                root: x.root.clone(),
                self_parent: x.self_parent,
                types: x.types.clone(),
                bootstrap: to_struct(&x.bootstrap),
            })
            .collect(),
    }
}

/// StructuresFromPB converts the structures of the graph (ADR 0054).
pub fn structures_from_pb(r: &pb::GetStructuresResponse) -> domain::Structures {
    domain::Structures(
        r.structures
            .iter()
            .map(|st| domain::StructureSet {
                structure: domain::Structure {
                    kind: st.kind.clone(),
                    r#type: st.r#type.clone(),
                    namespace: st.namespace.clone(),
                    parent: st.parent.clone(),
                    root: st.root.clone(),
                    self_parent: st.self_parent,
                    bootstrap: st.bootstrap.as_ref().map(struct_to_map).unwrap_or_default(),
                },
                types: st.types.clone(),
            })
            .collect(),
    )
}

/// RestructuredToPB and RestructuredFromPB carry the result of a merge or a split (ADR 0077).
pub fn restructured_to_pb(r: &graphapi::Restructured) -> pb::ImpactNodeRestructureResponse {
    pb::ImpactNodeRestructureResponse {
        successors: change_impacts_to_pb(&r.successors),
        sources: change_impacts_to_pb(&r.sources),
        parents: change_impacts_to_pb(&r.parents),
        suspect: r
            .suspect
            .iter()
            .map(|s| pb::SuspectLink {
                from: Some(ref_to_pb(&s.from)),
                from_key: s.from_key.clone(),
                r#type: s.r#type.clone(),
                to: Some(ref_to_pb(&s.to)),
                to_key: s.to_key.clone(),
            })
            .collect(),
    }
}

pub fn restructured_from_pb(r: &pb::ImpactNodeRestructureResponse) -> graphapi::Restructured {
    graphapi::Restructured {
        successors: change_impacts_from_pb(&r.successors),
        sources: change_impacts_from_pb(&r.sources),
        parents: change_impacts_from_pb(&r.parents),
        suspect: r
            .suspect
            .iter()
            .map(|s| graphapi::SuspectLink {
                from: ref_from_pb(s.from.as_ref()),
                from_key: s.from_key.clone(),
                r#type: s.r#type.clone(),
                to: ref_from_pb(s.to.as_ref()),
                to_key: s.to_key.clone(),
            })
            .collect(),
    }
}

fn guardians_to_pb(gs: &[domain::GuardianRef]) -> Vec<pb::GuardianRef> {
    gs.iter()
        .map(|g| pb::GuardianRef {
            methodology: g.methodology.clone(),
            version: g.version.clone(),
        })
        .collect()
}

fn guardians_from_pb(gs: &[pb::GuardianRef]) -> Vec<domain::GuardianRef> {
    gs.iter()
        .map(|g| domain::GuardianRef {
            methodology: g.methodology.clone(),
            version: g.version.clone(),
        })
        .collect()
}

/// ChangeIDsFromPB converts a list of change ids.
pub fn change_ids_from_pb(ids: &[String]) -> Vec<ChangeID> {
    ids_from_pb(ids)
}

/// DependenciesToPB converts dependencies between changes (ADR 0108).
pub fn dependencies_to_pb(ds: &[domain::Dependency]) -> Vec<pb::Dependency> {
    ds.iter()
        .map(|d| pb::Dependency {
            id: d.id.to_string(),
            title: d.title.clone(),
            methodology: d.methodology.clone(),
            status: d.status.to_string(),
            state: d.state.clone(),
            parent_id: d.parent_id.to_string(),
            satisfied: d.satisfied,
            reason: d.reason.clone(),
        })
        .collect()
}

/// DependenciesFromPB converts dependencies between changes (ADR 0108).
pub fn dependencies_from_pb(ds: &[pb::Dependency]) -> Vec<domain::Dependency> {
    ds.iter()
        .map(|d| domain::Dependency {
            id: ChangeID::new(d.id.clone()),
            title: d.title.clone(),
            methodology: d.methodology.clone(),
            status: ChangeStatus::new(d.status.clone()),
            state: d.state.clone(),
            parent_id: ChangeID::new(d.parent_id.clone()),
            satisfied: d.satisfied,
            reason: d.reason.clone(),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn baseline_round_trip() {
        let b = domain::Baseline {
            id: "B1".into(),
            name: "b".into(),
            branch: "main".into(),
            namespace: "alm".into(),
            nodes: BTreeMap::from([(NodeID::from("N1"), 2)]),
            ..Default::default()
        };
        let got = baseline_from_pb(&baseline_to_pb(&b));
        assert!(
            got.namespace == "alm" && got.branch == "main" && got.nodes.get("N1") == Some(&2),
            "round trip = {got:?}"
        );
    }
}
