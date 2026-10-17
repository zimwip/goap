//! The difference between two flows of a change (ADR 0083): each flow sees its change impacts through drafts (ADR
//! 0079), and an impact is added, removed or modified by one flow compared with the other. The functions here are
//! pure: they compare two shapes of a node, the state a flow sees of it.

use std::collections::{BTreeMap, BTreeSet};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    ChangeID, ChangeImpactID, NodeID, NodeIntent, NodeReview,
    serde_util::{json_equal, null_default},
};

/// Kinds of a FieldChange.
pub const FIELD_PROPERTY: &str = "property";
pub const FIELD_STATE: &str = "state";
pub const FIELD_OWNER: &str = "owner";
pub const FIELD_LINK: &str = "link";

/// Operations of a FieldChange, from the left shape to the right one.
pub const OP_ADDED: &str = "added";
pub const OP_REMOVED: &str = "removed";
pub const OP_CHANGED: &str = "changed";

/// Categories of an impact in a FlowDiff (identical impacts are counted, not listed).
pub const DIFF_ADDED: &str = "added";
pub const DIFF_REMOVED: &str = "removed";
pub const DIFF_MODIFIED: &str = "modified";

/// LinkShape is an outgoing link of a node as a flow sees it: the type and the target node (the key names it for a
/// reader), not the version, since a draft points at the draft of its target.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LinkShape {
    pub r#type: String,
    pub to: NodeID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub to_key: String,
    #[serde(
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub properties: Map<String, Value>,
}

/// NodeShape is the content of a node as a flow sees it: what its draft carries.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct NodeShape {
    pub key: String,
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(skip_serializing_if = "NodeID::is_empty")]
    pub owner: NodeID,
    #[serde(
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub properties: Map<String, Value>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub links: Vec<LinkShape>,
}

/// FieldChange is one difference between two shapes: a property (Name), the lifecycle state, the owner, or a link
/// (Name is its type, Target the key of its target). Old and New are JSON null when absent (Go's nil).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct FieldChange {
    pub kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    pub op: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub target: String,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub old: Value,
    #[serde(skip_serializing_if = "Value::is_null")]
    pub new: Value,
}

/// ImpactSide is what one flow sees of an impact: its identity and review on that flow.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ImpactSide {
    pub impact: ChangeImpactID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    pub intent: NodeIntent,
    pub review: NodeReview,
    /// Drafted is false for a planned impact: its node is not checked out, no draft exists.
    pub drafted: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(skip_serializing_if = "NodeID::is_empty")]
    pub owner: NodeID,
}

/// ImpactDiff is an impact that differs between the two flows. Changes is empty for an added or removed impact.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ImpactDiff {
    pub node: NodeID,
    pub key: String,
    pub r#type: String,
    pub category: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub left: Option<ImpactSide>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub right: Option<ImpactSide>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub changes: Vec<FieldChange>,
}

/// FlowDiff compares the impacts two flows of a change see, at a level (written or accepted). Left and Right are the
/// flows ("main" for the main flow, else an option id).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct FlowDiff {
    pub change: ChangeID,
    pub left: String,
    pub right: String,
    pub level: String,
    #[serde(deserialize_with = "null_default")]
    pub impacts: Vec<ImpactDiff>,
    /// Identical counts the impacts both flows see with the same content: they are left out of Impacts.
    pub identical: i64,
}

impl FlowDiff {
    /// DiffCounts counts the impacts of each category.
    pub fn diff_counts(&self) -> BTreeMap<String, usize> {
        let mut out: BTreeMap<String, usize> = [DIFF_ADDED, DIFF_REMOVED, DIFF_MODIFIED]
            .into_iter()
            .map(|c| (c.to_string(), 0))
            .collect();
        for i in &self.impacts {
            *out.entry(i.category.clone()).or_default() += 1;
        }
        out
    }
}

/// DiffShapes lists what changes from the left shape to the right one: the state, the owner, the properties (by
/// name), then the links (a link is the type and the target node; the same one with other properties changed).
/// Empty: identical. The result is ordered: state, owner, properties by name, links by type then target.
pub fn diff_shapes(left: &NodeShape, right: &NodeShape) -> Vec<FieldChange> {
    let mut out = Vec::new();
    if left.state != right.state {
        out.push(FieldChange {
            kind: FIELD_STATE.into(),
            op: change_op(left.state.is_empty(), right.state.is_empty()).into(),
            old: Value::from(left.state.clone()),
            new: Value::from(right.state.clone()),
            ..Default::default()
        });
    }
    if left.owner != right.owner {
        out.push(FieldChange {
            kind: FIELD_OWNER.into(),
            op: change_op(left.owner.is_empty(), right.owner.is_empty()).into(),
            old: Value::from(left.owner.as_str()),
            new: Value::from(right.owner.as_str()),
            ..Default::default()
        });
    }
    let names: BTreeSet<&String> = left
        .properties
        .keys()
        .chain(right.properties.keys())
        .collect();
    for k in names {
        match (left.properties.get(k), right.properties.get(k)) {
            (Some(l), None) => out.push(FieldChange {
                kind: FIELD_PROPERTY.into(),
                name: k.clone(),
                op: OP_REMOVED.into(),
                old: l.clone(),
                ..Default::default()
            }),
            (None, Some(r)) => out.push(FieldChange {
                kind: FIELD_PROPERTY.into(),
                name: k.clone(),
                op: OP_ADDED.into(),
                new: r.clone(),
                ..Default::default()
            }),
            (Some(l), Some(r)) if !json_equal(l, r) => out.push(FieldChange {
                kind: FIELD_PROPERTY.into(),
                name: k.clone(),
                op: OP_CHANGED.into(),
                old: l.clone(),
                new: r.clone(),
                ..Default::default()
            }),
            _ => {}
        }
    }
    out.extend(diff_links(&left.links, &right.links));
    out
}

/// changeOp names the operation of a value that was empty or is: set from nothing is added, to nothing removed.
fn change_op(was_empty: bool, is_empty: bool) -> &'static str {
    if was_empty {
        OP_ADDED
    } else if is_empty {
        OP_REMOVED
    } else {
        OP_CHANGED
    }
}

fn shape_link_key(l: &LinkShape) -> String {
    format!("{}\x00{}", l.r#type, l.to)
}

fn link_label(l: &LinkShape) -> String {
    if l.to_key.is_empty() {
        l.to.to_string()
    } else {
        l.to_key.clone()
    }
}

fn empty_as_nil(m: &Map<String, Value>) -> Value {
    if m.is_empty() {
        Value::Null
    } else {
        Value::Object(m.clone())
    }
}

fn diff_links(left: &[LinkShape], right: &[LinkShape]) -> Vec<FieldChange> {
    let by_key = |ls: &[LinkShape]| -> BTreeMap<String, LinkShape> {
        ls.iter().map(|l| (shape_link_key(l), l.clone())).collect()
    };
    let (lm, rm) = (by_key(left), by_key(right));
    let mut sorted: Vec<&String> = lm
        .keys()
        .chain(rm.keys())
        .collect::<BTreeSet<_>>()
        .into_iter()
        .collect();
    let shape = |k: &String| lm.get(k).or_else(|| rm.get(k)).cloned().unwrap_or_default();
    sorted.sort_by(|a, b| {
        let (la, lb) = (shape(a), shape(b));
        la.r#type
            .cmp(&lb.r#type)
            .then_with(|| link_label(&la).cmp(&link_label(&lb)))
    });
    let mut out = Vec::new();
    for k in sorted {
        match (lm.get(k), rm.get(k)) {
            (Some(l), None) => out.push(FieldChange {
                kind: FIELD_LINK.into(),
                name: l.r#type.clone(),
                op: OP_REMOVED.into(),
                target: link_label(l),
                old: empty_as_nil(&l.properties),
                ..Default::default()
            }),
            (None, Some(r)) => out.push(FieldChange {
                kind: FIELD_LINK.into(),
                name: r.r#type.clone(),
                op: OP_ADDED.into(),
                target: link_label(r),
                new: empty_as_nil(&r.properties),
                ..Default::default()
            }),
            (Some(l), Some(r))
                if !json_equal(&empty_as_nil(&l.properties), &empty_as_nil(&r.properties)) =>
            {
                out.push(FieldChange {
                    kind: FIELD_LINK.into(),
                    name: r.r#type.clone(),
                    op: OP_CHANGED.into(),
                    target: link_label(r),
                    old: empty_as_nil(&l.properties),
                    new: empty_as_nil(&r.properties),
                })
            }
            _ => {}
        }
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        v.as_object().unwrap().clone()
    }

    #[test]
    fn diff_shapes_test() {
        let base = NodeShape {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            state: "draft".into(),
            owner: "ORG-A".into(),
            properties: obj(json!({"title": "Use PSP", "n": 1.0, "tags": ["a"]})),
            links: vec![LinkShape {
                r#type: "satisfies".into(),
                to: "n1".into(),
                to_key: "NEED-1".into(),
                ..Default::default()
            }],
        };
        let d = diff_shapes(&base, &base);
        assert!(d.is_empty(), "identical shapes: {d:?}");
        // a number as another type, a link order, an empty and a nil property map are not differences
        let mut same = base.clone();
        same.properties = obj(json!({"title": "Use PSP", "n": 1, "tags": ["a"]}));
        same.links = vec![LinkShape {
            r#type: "satisfies".into(),
            to: "n1".into(),
            to_key: "NEED-1".into(),
            properties: Map::new(),
        }];
        let d = diff_shapes(&base, &same);
        assert!(d.is_empty(), "equivalent shapes: {d:?}");

        let right = NodeShape {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            state: "approved".into(),
            owner: "ORG-B".into(),
            properties: obj(json!({"title": "Use Stripe", "n": 1.0, "extra": "x"})),
            links: vec![
                LinkShape {
                    r#type: "satisfies".into(),
                    to: "n1".into(),
                    to_key: "NEED-1".into(),
                    properties: obj(json!({"w": 2})),
                },
                LinkShape {
                    r#type: "refines".into(),
                    to: "n2".into(),
                    to_key: "NEED-2".into(),
                    ..Default::default()
                },
            ],
        };
        let got = diff_shapes(&base, &right);
        let fc =
            |kind: &str, name: &str, op: &str, target: &str, old: Value, new: Value| FieldChange {
                kind: kind.into(),
                name: name.into(),
                op: op.into(),
                target: target.into(),
                old,
                new,
            };
        let want = vec![
            fc(
                FIELD_STATE,
                "",
                OP_CHANGED,
                "",
                json!("draft"),
                json!("approved"),
            ),
            fc(
                FIELD_OWNER,
                "",
                OP_CHANGED,
                "",
                json!("ORG-A"),
                json!("ORG-B"),
            ),
            fc(
                FIELD_PROPERTY,
                "extra",
                OP_ADDED,
                "",
                Value::Null,
                json!("x"),
            ),
            fc(
                FIELD_PROPERTY,
                "tags",
                OP_REMOVED,
                "",
                json!(["a"]),
                Value::Null,
            ),
            fc(
                FIELD_PROPERTY,
                "title",
                OP_CHANGED,
                "",
                json!("Use PSP"),
                json!("Use Stripe"),
            ),
            fc(
                FIELD_LINK,
                "refines",
                OP_ADDED,
                "NEED-2",
                Value::Null,
                Value::Null,
            ),
            fc(
                FIELD_LINK,
                "satisfies",
                OP_CHANGED,
                "NEED-1",
                Value::Null,
                json!({"w": 2}),
            ),
        ];
        assert_eq!(got, want);

        // the other way round: what was added is removed
        let back = diff_shapes(&right, &base);
        assert!(
            back.len() == want.len()
                && back[2].op == OP_REMOVED
                && back[3].op == OP_ADDED
                && back[5].op == OP_REMOVED,
            "reverse diff: {back:?}"
        );

        // a state or an owner set from nothing, a link removed
        let got = diff_shapes(
            &NodeShape {
                links: base.links.clone(),
                ..Default::default()
            },
            &NodeShape {
                state: "draft".into(),
                owner: "ORG-A".into(),
                ..Default::default()
            },
        );
        assert!(
            got.len() == 3
                && got[0].op == OP_ADDED
                && got[1].op == OP_ADDED
                && got[2].op == OP_REMOVED
                && got[2].target == "NEED-1",
            "set from nothing: {got:?}"
        );
    }

    #[test]
    fn flow_diff_counts() {
        let cat = |c: &str| ImpactDiff {
            category: c.into(),
            ..Default::default()
        };
        let d = FlowDiff {
            impacts: vec![cat(DIFF_ADDED), cat(DIFF_ADDED), cat(DIFF_MODIFIED)],
            ..Default::default()
        };
        let c = d.diff_counts();
        assert!(
            c[DIFF_ADDED] == 2 && c[DIFF_REMOVED] == 0 && c[DIFF_MODIFIED] == 1,
            "counts: {c:?}"
        );
    }
}
