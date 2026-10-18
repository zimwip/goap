//! Drafts and versions at landing (ADR 0079). During a change a node has no version: its working state in the change
//! is a Draft, one per change impact and flow, the fold (ADR 0029, 0030) of the impact events that carry it (not
//! stored): `created` and `checkedOut` carry the initial state, `updated` and `transitioned` patches, `cancelled`,
//! `withdrawn` and `landed` drop it. Landing (CommitChange) writes the node version from the draft and the draft goes.

use std::collections::HashMap;

use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Map, Value};

use crate::{
    ChangeID, ChangeImpact, ChangeImpactID, IMPACT_ADOPTED, IMPACT_CANCELLED, IMPACT_CHECKED_OUT,
    IMPACT_CREATED, IMPACT_LANDED, IMPACT_TRANSITIONED, IMPACT_UPDATED, IMPACT_WITHDRAWN,
    ImpactEvent, Link, LinkID, Node, NodeID, NodeRef, REASON_CREATE, REASON_REVISE, Version,
    apply_impact_event, serde_util::null_default,
};

impl NodeRef {
    /// IsDraft reports a reference to the draft of a node in a change: the node, no version (Version 0). Outside a
    /// change a reference with Version 0 means the latest version of the node on main.
    pub fn is_draft(&self) -> bool {
        !self.id.is_empty() && self.version == 0
    }
}

/// DraftRef is the reference to the draft of a node.
pub fn draft_ref(id: NodeID) -> NodeRef {
    NodeRef { id, version: 0 }
}

/// DraftLink is an outgoing link of a draft: To is an exact node version, or a draft reference (Version 0) to a node
/// the change writes, resolved to the version landing writes for it.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DraftLink {
    pub id: LinkID,
    pub r#type: String,
    pub to: NodeRef,
    #[serde(
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub properties: Map<String, Value>,
}

/// Draft is the working state of a node in a change on a flow: everything its next version will carry.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Draft {
    #[serde(rename = "changeId")]
    pub change: ChangeID,
    #[serde(rename = "impactId")]
    pub impact: ChangeImpactID,
    /// Flow is the flow the draft belongs to ("" = the main flow): a flow checks the node out to write its own draft.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    /// Node is the id of the node, allocated when the draft is created and stable until landing.
    pub node: NodeID,
    pub key: String,
    pub r#type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    /// Owner is the unit owning the next version; empty: carried over from its base, else the unit holding the
    /// change.
    #[serde(skip_serializing_if = "NodeID::is_empty")]
    pub owner: NodeID,
    #[serde(
        rename = "props",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub properties: Map<String, Value>,
    /// Origins are the nodes the node derives from (merge and split, ADR 0077), set on a creation.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub origins: Vec<NodeRef>,
    /// Base is the version the draft was checked out from (None for a creation).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub base: Option<NodeRef>,
    /// MergedWith is, for the draft a rebase of a committed change merged (ADR 0102), the version on the head of the
    /// target branch it was merged with: the second parent of the version it lands as.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub merged_with: Option<NodeRef>,
    /// Execution is the action run that checked the node out (what a relaunch of a step marks stale, ADR 0025).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub links: Vec<DraftLink>,
}

impl Draft {
    /// Ref is the draft reference of the node.
    pub fn r#ref(&self) -> NodeRef {
        draft_ref(self.node.clone())
    }

    /// AsNode is the draft seen as a node (Version 0): what a reader of the change sees of the node. Branch and
    /// Namespace are set by the graph.
    pub fn as_node(&self) -> Node {
        Node {
            id: self.node.clone(),
            key: self.key.clone(),
            r#type: self.r#type.clone(),
            state: self.state.clone(),
            owner: self.owner.clone(),
            properties: self.properties.clone(),
            origins: self.origins.clone(),
            change_id: self.change.clone(),
            change_impact: self.impact.clone(),
            execution: self.execution.clone(),
            reason: if self.base.is_none() {
                REASON_CREATE
            } else {
                REASON_REVISE
            }
            .to_string(),
            ..Default::default()
        }
    }

    /// OutLinks are the outgoing links of the draft as links of a node view (From is the draft reference).
    pub fn out_links(&self) -> Vec<Link> {
        self.links
            .iter()
            .map(|l| Link {
                id: l.id.clone(),
                r#type: l.r#type.clone(),
                from: self.r#ref(),
                to: l.to.clone(),
                properties: l.properties.clone(),
                change_id: self.change.clone(),
            })
            .collect()
    }

    /// Link returns the outgoing link with an id.
    pub fn link(&self, id: &LinkID) -> Option<&DraftLink> {
        self.links.iter().find(|l| l.id == *id)
    }
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
pub(crate) struct StatePatch {
    #[allow(dead_code)]
    pub(crate) from: String,
    pub(crate) to: String,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub(crate) struct AddLinkPatch {
    pub(crate) id: LinkID,
    pub(crate) r#type: String,
    pub(crate) to_id: NodeID,
    pub(crate) to_version: Version,
    #[serde(deserialize_with = "null_default")]
    pub(crate) props: Map<String, Value>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
pub(crate) struct UpdateLinkPatch {
    pub(crate) id: LinkID,
    #[serde(deserialize_with = "null_default")]
    pub(crate) props: Map<String, Value>,
}

#[derive(Debug, Default, Deserialize)]
#[serde(default)]
pub(crate) struct RemoveLinkPatch {
    pub(crate) id: LinkID,
}

/// draftPatch is the typed form of ImpactEvent.patch for what changes a draft. The event keeps the free-form map (the
/// audit trail and PROV-O read it); the fold reads it through this shape. Like Go's json.Unmarshal into the struct, a
/// field of another shape is left unset and the others are read.
#[derive(Debug, Default)]
pub(crate) struct DraftPatch {
    pub(crate) props: Map<String, Value>,
    pub(crate) unset: Vec<String>,
    pub(crate) owner_id: NodeID,
    pub(crate) state: Option<StatePatch>,
    pub(crate) add_link: Option<AddLinkPatch>,
    pub(crate) update_link: Option<UpdateLinkPatch>,
    pub(crate) remove_link: Option<RemoveLinkPatch>,
}

fn field<T: DeserializeOwned>(p: &Map<String, Value>, k: &str) -> Option<T> {
    p.get(k)
        .filter(|v| !v.is_null())
        .and_then(|v| serde_json::from_value(v.clone()).ok())
}

pub(crate) fn patch_of(p: &Map<String, Value>) -> DraftPatch {
    if p.is_empty() {
        return DraftPatch::default();
    }
    DraftPatch {
        props: field(p, "props").unwrap_or_default(),
        unset: field(p, "unset").unwrap_or_default(),
        owner_id: field(p, "ownerId").unwrap_or_default(),
        state: field(p, "state"),
        add_link: field(p, "addLink"),
        update_link: field(p, "updateLink"),
        remove_link: field(p, "removeLink"),
    }
}

/// The draft an event leaves for (impact, flow): the fold of one row.
fn apply_draft(d: Option<Draft>, e: &ImpactEvent) -> Option<Draft> {
    if (e.op == IMPACT_CREATED || e.op == IMPACT_CHECKED_OUT || e.op == IMPACT_TRANSITIONED)
        && let Some(draft) = &e.draft
    {
        let mut n = draft.clone();
        n.change = e.change.clone();
        n.impact = e.impact.clone();
        n.flow = e.flow.clone();
        return Some(n);
    }
    let d = d?;
    if e.op == IMPACT_UPDATED || e.op == IMPACT_TRANSITIONED {
        let mut n = d;
        let p = patch_of(&e.patch);
        if !p.props.is_empty() || !p.unset.is_empty() {
            for (k, v) in p.props {
                n.properties.insert(k, v);
            }
            for k in &p.unset {
                n.properties.remove(k);
            }
        }
        if !p.owner_id.is_empty() {
            n.owner = p.owner_id;
        }
        if let Some(s) = p.state {
            n.state = s.to;
        }
        if let Some(a) = p.add_link {
            n.links.push(DraftLink {
                id: a.id,
                r#type: a.r#type,
                to: NodeRef {
                    id: a.to_id,
                    version: a.to_version,
                },
                properties: a.props,
            });
        }
        if let Some(u) = p.update_link {
            for l in n.links.iter_mut().filter(|l| l.id == u.id) {
                l.properties = u.props.clone();
            }
        }
        if let Some(r) = p.remove_link {
            n.links.retain(|l| l.id != r.id);
        }
        return Some(n);
    }
    if e.op == IMPACT_CANCELLED {
        return None;
    }
    Some(d)
}

/// ApplyDraftEvent returns the drafts of a change after an event; impacts are the change impacts after it (a draft
/// whose impact is gone, or superseded by an adopted flow, goes with it). The list is not modified.
pub fn apply_draft_event(
    drafts: &[Draft],
    e: &ImpactEvent,
    impacts: &[ChangeImpact],
) -> Vec<Draft> {
    let mut out = drafts.to_vec();
    let at = out
        .iter()
        .position(|d| d.impact == e.impact && d.flow == e.flow);
    let op = &e.op;
    if *op == IMPACT_CREATED
        || *op == IMPACT_CHECKED_OUT
        || *op == IMPACT_TRANSITIONED
        || *op == IMPACT_UPDATED
        || *op == IMPACT_CANCELLED
    {
        let cur = at.map(|i| out[i].clone());
        match (apply_draft(cur, e), at) {
            (None, Some(i)) => {
                out.remove(i);
            }
            (Some(next), Some(i)) => out[i] = next,
            (Some(next), None) => out.push(next),
            (None, None) => {}
        }
    } else if *op == IMPACT_WITHDRAWN || *op == IMPACT_LANDED {
        out.retain(|d| d.impact != e.impact);
    } else if *op == IMPACT_ADOPTED {
        // the adopted flow's drafts were installed on the main flow by the events that follow (see the adoption)
        out.retain(|d| d.flow != e.flow);
    }
    out.retain(|d| {
        impacts
            .iter()
            .find(|c| c.id == d.impact)
            .is_some_and(|c| !c.superseded)
    });
    out
}

/// FoldDrafts replays a log: the drafts of the change after the last event, in the order of the rows.
pub fn fold_drafts(events: &[ImpactEvent]) -> Vec<Draft> {
    let mut drafts = Vec::new();
    let mut impacts = Vec::new();
    for e in events {
        impacts = apply_impact_event(&impacts, e);
        drafts = apply_draft_event(&drafts, e, &impacts);
    }
    drafts
}

/// DraftSeenBy is the draft a process on a flow sees for a change impact (ADR 0025 §3): the one of the innermost flow
/// of the chain (the flow, then its ancestors, then the main flow) that holds one, as that flow's own events left it,
/// not counting the events of the runs that are stale on the chain. None: none.
pub fn draft_seen_by(
    events: &[ImpactEvent],
    impact: &ChangeImpactID,
    chain: &[String],
    stale: &dyn Fn(&str) -> bool,
) -> Option<Draft> {
    let mut order: Vec<&str> = chain.iter().map(String::as_str).collect();
    order.push("");
    let mut rows: HashMap<String, Option<Draft>> = HashMap::new();
    for e in events {
        let gone = e.op == IMPACT_LANDED || e.op == IMPACT_WITHDRAWN;
        if e.impact != *impact
            || !order.contains(&e.flow.as_str())
            || (stale(&e.execution) && !gone)
        {
            continue;
        }
        if gone {
            rows.clear();
        } else if e.op == IMPACT_CREATED
            || e.op == IMPACT_CHECKED_OUT
            || e.op == IMPACT_TRANSITIONED
            || e.op == IMPACT_UPDATED
            || e.op == IMPACT_CANCELLED
        {
            let cur = rows.remove(&e.flow).flatten();
            rows.insert(e.flow.clone(), apply_draft(cur, e));
        }
    }
    order.iter().find_map(|f| rows.get(*f).cloned().flatten())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{IMPACT_CREATED, IMPACT_LANDED, IMPACT_UPDATED, INTENT_CREATED, REVIEW_PROPOSED};
    use serde_json::json;

    #[test]
    fn fold_drafts_follows_the_patches() {
        let state = ChangeImpact {
            id: "I".into(),
            key: "K".into(),
            r#type: "T".into(),
            intent: INTENT_CREATED,
            review: REVIEW_PROPOSED,
            ..Default::default()
        };
        let created = ImpactEvent {
            op: IMPACT_CREATED,
            change: "C".into(),
            impact: "I".into(),
            state: Some(state),
            post: Some(draft_ref("N".into())),
            draft: Some(Draft {
                node: "N".into(),
                key: "K".into(),
                r#type: "T".into(),
                ..Default::default()
            }),
            ..Default::default()
        };
        let patch = |p: Value| ImpactEvent {
            op: IMPACT_UPDATED,
            impact: "I".into(),
            post: Some(draft_ref("N".into())),
            patch: p.as_object().unwrap().clone(),
            ..Default::default()
        };
        let log = vec![
            created,
            patch(json!({"props": {"a": 1, "b": 2}, "ownerId": "U"})),
            patch(
                json!({"unset": ["b"], "addLink": {"id": "l", "type": "uses", "toId": "M", "toVersion": 3}}),
            ),
            patch(
                json!({"updateLink": {"id": "l", "props": {"w": 1}}, "state": {"from": "", "to": "x"}, "ownerId": 7}),
            ),
        ];
        let d = fold_drafts(&log);
        assert_eq!(d.len(), 1);
        let d = &d[0];
        assert_eq!(d.change, "C");
        assert_eq!(Value::Object(d.properties.clone()), json!({"a": 1}));
        assert_eq!((d.owner.as_str(), d.state.as_str()), ("U", "x"));
        assert_eq!(d.links[0].to, NodeRef::new("M", 3));
        assert_eq!(
            Value::Object(d.links[0].properties.clone()),
            json!({"w": 1})
        );
        assert_eq!(d.as_node().reason, REASON_CREATE);
        let seen = draft_seen_by(&log, &"I".into(), &[], &|_| false).unwrap();
        assert_eq!(&seen, d);
        let mut landed = log.clone();
        landed.push(ImpactEvent {
            op: IMPACT_LANDED,
            impact: "I".into(),
            landed: Some(NodeRef::new("N", 1)),
            ..Default::default()
        });
        assert!(fold_drafts(&landed).is_empty());
        assert!(crate::draft_changed(None, d));
    }
}
