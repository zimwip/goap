use goap_domain::{self as domain, ChangeID, ChangeImpactID, NodeID, NodeIntent, NodeReview};
use goap_gen::graph::v1 as pb;
use serde_json::Value;

use crate::{json_to_value, value_to_json};

/// value is a JSON value as a protobuf Value (None: absent).
fn value(v: &Value) -> Option<pbjson_types::Value> {
    if v.is_null() {
        return None;
    }
    Some(json_to_value(v))
}

fn from_value(v: Option<&pbjson_types::Value>) -> Value {
    v.map_or(Value::Null, value_to_json)
}

fn impact_side_to_pb(s: Option<&domain::ImpactSide>) -> Option<pb::ImpactSide> {
    s.map(|s| pb::ImpactSide {
        impact: s.impact.to_string(),
        flow: s.flow.clone(),
        intent: s.intent.to_string(),
        review: s.review.to_string(),
        drafted: s.drafted,
        state: s.state.clone(),
        owner: s.owner.to_string(),
    })
}

fn impact_side_from_pb(s: Option<&pb::ImpactSide>) -> Option<domain::ImpactSide> {
    s.map(|s| domain::ImpactSide {
        impact: ChangeImpactID::new(s.impact.clone()),
        flow: s.flow.clone(),
        intent: NodeIntent::new(s.intent.clone()),
        review: NodeReview::new(s.review.clone()),
        drafted: s.drafted,
        state: s.state.clone(),
        owner: NodeID::new(s.owner.clone()),
    })
}

/// FlowDiffToPB converts the comparison of two flows of a change (ADR 0083).
pub fn flow_diff_to_pb(d: &domain::FlowDiff) -> pb::DiffFlowsResponse {
    pb::DiffFlowsResponse {
        left: d.left.clone(),
        right: d.right.clone(),
        level: d.level.clone(),
        identical: d.identical as i32,
        impacts: d
            .impacts
            .iter()
            .map(|i| pb::ImpactDiff {
                node: i.node.to_string(),
                key: i.key.clone(),
                r#type: i.r#type.clone(),
                category: i.category.clone(),
                left: impact_side_to_pb(i.left.as_ref()),
                right: impact_side_to_pb(i.right.as_ref()),
                changes: i
                    .changes
                    .iter()
                    .map(|c| pb::FieldChange {
                        kind: c.kind.clone(),
                        name: c.name.clone(),
                        op: c.op.clone(),
                        target: c.target.clone(),
                        old: value(&c.old),
                        new: value(&c.new),
                    })
                    .collect(),
            })
            .collect(),
    }
}

/// FlowDiffFromPB is the inverse of FlowDiffToPB.
pub fn flow_diff_from_pb(change: ChangeID, r: &pb::DiffFlowsResponse) -> domain::FlowDiff {
    domain::FlowDiff {
        change,
        left: r.left.clone(),
        right: r.right.clone(),
        level: r.level.clone(),
        identical: i64::from(r.identical),
        impacts: r
            .impacts
            .iter()
            .map(|i| domain::ImpactDiff {
                node: NodeID::new(i.node.clone()),
                key: i.key.clone(),
                r#type: i.r#type.clone(),
                category: i.category.clone(),
                left: impact_side_from_pb(i.left.as_ref()),
                right: impact_side_from_pb(i.right.as_ref()),
                changes: i
                    .changes
                    .iter()
                    .map(|c| domain::FieldChange {
                        kind: c.kind.clone(),
                        name: c.name.clone(),
                        op: c.op.clone(),
                        target: c.target.clone(),
                        old: from_value(c.old.as_ref()),
                        new: from_value(c.new.as_ref()),
                    })
                    .collect(),
            })
            .collect(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn flow_diff_round_trip() {
        let input = domain::FlowDiff {
            change: "c1".into(),
            left: "main".into(),
            right: "f1".into(),
            level: "written".into(),
            identical: 2,
            impacts: vec![domain::ImpactDiff {
                node: "n1".into(),
                key: "REQ-1".into(),
                r#type: "Requirement".into(),
                category: domain::DIFF_MODIFIED.into(),
                left: Some(domain::ImpactSide {
                    impact: "i1".into(),
                    intent: domain::INTENT_MODIFIED,
                    review: domain::REVIEW_ACCEPTED,
                    drafted: true,
                    state: "draft".into(),
                    ..Default::default()
                }),
                right: Some(domain::ImpactSide {
                    impact: "i2".into(),
                    flow: "f1".into(),
                    intent: domain::INTENT_MODIFIED,
                    review: domain::REVIEW_PROPOSED,
                    drafted: true,
                    state: "approved".into(),
                    owner: "ORG".into(),
                }),
                changes: vec![
                    domain::FieldChange {
                        kind: domain::FIELD_PROPERTY.into(),
                        name: "title".into(),
                        op: domain::OP_CHANGED.into(),
                        old: json!("a"),
                        new: json!("b"),
                        ..Default::default()
                    },
                    domain::FieldChange {
                        kind: domain::FIELD_PROPERTY.into(),
                        name: "n".into(),
                        op: domain::OP_ADDED.into(),
                        new: json!(3),
                        ..Default::default()
                    },
                    domain::FieldChange {
                        kind: domain::FIELD_PROPERTY.into(),
                        name: "tags".into(),
                        op: domain::OP_REMOVED.into(),
                        old: json!(["x"]),
                        ..Default::default()
                    },
                    domain::FieldChange {
                        kind: domain::FIELD_LINK.into(),
                        name: "satisfies".into(),
                        op: domain::OP_ADDED.into(),
                        target: "NEED-1".into(),
                        ..Default::default()
                    },
                ],
            }],
        };
        let out = flow_diff_from_pb("c1".into(), &flow_diff_to_pb(&input));
        assert_eq!(input, out);
    }
}
