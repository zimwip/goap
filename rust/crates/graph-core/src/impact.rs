//! The change impacts of a change and the drafts of its nodes, event-sourced (ADR 0029, 0079): every operation is an
//! [`ImpactEvent`] appended to the log of the change, and the impacts and drafts are the fold of those events. The
//! payloads are the ones `contracts/domain` writes (ADR 0142, 0144): the envelope in the columns of the log entry, the
//! rest in a JSON written as Go writes it, so that a log either service wrote reads the same.
use std::collections::{BTreeMap, HashMap, HashSet};

use chrono::{DateTime, Utc};
use serde::{Deserialize, Deserializer, Serialize};
use serde_json::{Map, Value, value::RawValue};

use crate::{
    gojson::{self, Raw},
    lnk::Url,
    model::{BaselineId, ChangeId, LinkId, NodeId, NodeRef, Props, Version},
};

/// The stream of the impact events in the log.
pub const LOG_IMPACT: &str = "impact";
/// The stream of the facts.
pub const LOG_FACT: &str = "fact";

pub const INTENT_CREATED: &str = "created";
pub const INTENT_MODIFIED: &str = "modified";

pub const REVIEW_PROPOSED: &str = "proposed";
pub const REVIEW_ACCEPTED: &str = "accepted";
pub const REVIEW_REJECTED: &str = "rejected";

/// The operation an impact event records.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub enum ImpactOp {
    #[default]
    Proposed,
    Created,
    CheckedOut,
    Transitioned,
    Updated,
    Cancelled,
    Withdrawn,
    Reviewed,
    Discarded,
    Adopted,
    Committed,
    Rebased,
}

impl ImpactOp {
    pub fn as_str(self) -> &'static str {
        match self {
            ImpactOp::Proposed => "proposed",
            ImpactOp::Created => "created",
            ImpactOp::CheckedOut => "checkedOut",
            ImpactOp::Transitioned => "transitioned",
            ImpactOp::Updated => "updated",
            ImpactOp::Cancelled => "cancelled",
            ImpactOp::Withdrawn => "withdrawn",
            ImpactOp::Reviewed => "reviewed",
            ImpactOp::Discarded => "discarded",
            ImpactOp::Adopted => "adopted",
            ImpactOp::Committed => "committed",
            ImpactOp::Rebased => "rebased",
        }
    }

    pub fn of(s: &str) -> Option<ImpactOp> {
        serde_json::from_value(Value::String(s.to_string())).ok()
    }

    /// Whether the operation sets the post of an impact: the draft of its node (or, in the merge change, a version).
    pub fn writes_post(self) -> bool {
        matches!(self, ImpactOp::Transitioned | ImpactOp::CheckedOut | ImpactOp::Created)
    }

    /// The operations whose events of one operation share a log entry (ADR 0144).
    pub fn batched(self) -> bool {
        matches!(self, ImpactOp::Reviewed | ImpactOp::Committed)
    }
}

fn is_false(b: &bool) -> bool {
    !*b
}

fn no_props(p: &Props) -> bool {
    p.as_ref().is_none_or(Map::is_empty)
}

/// Properties read as Go decodes them: every number a float64.
fn go_props<'de, D: Deserializer<'de>>(d: D) -> Result<Props, D::Error> {
    Ok(match Option::<Value>::deserialize(d)?.map(gojson::floats) {
        Some(Value::Object(m)) => Some(m),
        _ => None,
    })
}

fn zero_time() -> DateTime<Utc> {
    gojson::zero_time()
}

/// One entry of the review history of an impact.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Review {
    #[serde(default)]
    pub status: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub by: String,
    #[serde(default)]
    pub comment: String,
    #[serde(with = "gojson::time", default = "zero_time")]
    pub at: DateTime<Utc>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub superseded: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub review_id: String,
}

impl Default for Review {
    fn default() -> Review {
        Review {
            status: String::new(),
            by: String::new(),
            comment: String::new(),
            at: zero_time(),
            flow: String::new(),
            execution: String::new(),
            superseded: false,
            review_id: String::new(),
        }
    }
}

/// The link from a change to a node: the version it reads (pre), its draft or the version it wrote (post), the version
/// on the target branch (committed), and why.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ChangeImpact {
    /// Left out of a payload, whose event names it.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub id: String,
    #[serde(default)]
    pub key: String,
    #[serde(default, rename = "type")]
    pub typ: String,
    #[serde(default)]
    pub intent: String,
    #[serde(default)]
    pub rationale: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub pre: Option<NodeRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub post: Option<NodeRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub committed: Option<NodeRef>,
    #[serde(default)]
    pub review: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub reviews: Vec<Review>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub via: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub recheck: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub produced_by: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub derived_from: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub items: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub superseded: bool,
    #[serde(with = "gojson::time", default = "zero_time")]
    pub created_at: DateTime<Utc>,
}

impl Default for ChangeImpact {
    fn default() -> Self {
        ChangeImpact {
            id: String::new(),
            key: String::new(),
            typ: String::new(),
            intent: String::new(),
            rationale: String::new(),
            pre: None,
            post: None,
            committed: None,
            review: String::new(),
            reviews: Vec::new(),
            via: String::new(),
            recheck: false,
            produced_by: String::new(),
            derived_from: Vec::new(),
            execution: String::new(),
            items: Vec::new(),
            flow: String::new(),
            superseded: false,
            created_at: zero_time(),
        }
    }
}

impl ChangeImpact {
    /// A creation planned by `ProposeImpact` and not drafted yet: no draft, no version (ADR 0121).
    pub fn planned_creation(&self) -> bool {
        self.intent == INTENT_CREATED && self.post.is_none() && self.committed.is_none()
    }

    /// Its node has a draft.
    pub fn drafted(&self) -> bool {
        self.post.as_ref().is_some_and(NodeRef::is_draft)
    }

    /// The shape of an impact (Go's `ChangeImpact.Validate`).
    pub fn validate(&self) -> Result<(), String> {
        if self.key.is_empty() || self.typ.is_empty() {
            return Err("change impact needs a key and a type".into());
        }
        if self.rationale.trim().is_empty() {
            return Err(format!("change impact {} needs a rationale", self.key));
        }
        match self.intent.as_str() {
            INTENT_CREATED => {
                if self.pre.is_some() {
                    return Err(format!("change impact {}: a created node has no pre version", self.key));
                }
            }
            INTENT_MODIFIED => {
                let Some(pre) = self.pre.as_ref().filter(|p| p.version != 0) else {
                    return Err(format!(
                        "change impact {}: a modified node needs its pre version",
                        self.key
                    ));
                };
                if let Some(post) = &self.post
                    && (post.id != pre.id || (!post.is_draft() && post.version <= pre.version))
                {
                    return Err(format!(
                        "change impact {}: post {post} is not a successor of pre {pre}",
                        self.key
                    ));
                }
            }
            other => return Err(format!("change impact {}: unknown intent {other:?}", self.key)),
        }
        if self.committed.is_some() && self.post.is_none() {
            return Err(format!("change impact {}: committed without post", self.key));
        }
        match self.review.as_str() {
            REVIEW_PROPOSED | REVIEW_ACCEPTED | REVIEW_REJECTED => Ok(()),
            other => Err(format!("change impact {}: unknown review {other:?}", self.key)),
        }
    }
}

/// An outgoing link of a draft: to an exact version, to a draft (version 0) of a node the change writes, or to a URL of
/// another source.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct DraftLink {
    pub id: LinkId,
    #[serde(rename = "type")]
    pub typ: String,
    #[serde(default)]
    pub to: NodeRef,
    #[serde(default, skip_serializing_if = "Url::is_zero")]
    pub target: Url,
    #[serde(
        default,
        rename = "props",
        skip_serializing_if = "no_props",
        deserialize_with = "go_props"
    )]
    pub props: Props,
}

impl DraftLink {
    pub fn is_external(&self) -> bool {
        self.to == NodeRef::default() && !self.target.is_zero()
    }
}

/// The working state of a node in a change on a flow: everything its next version carries (ADR 0079).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Draft {
    /// Left out of a payload with the impact and the flow: they are the event's.
    #[serde(default, rename = "changeId", skip_serializing_if = "ChangeId::is_empty")]
    pub change: ChangeId,
    #[serde(default, rename = "impactId", skip_serializing_if = "String::is_empty")]
    pub impact: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(default)]
    pub node: NodeId,
    #[serde(default)]
    pub key: String,
    #[serde(default, rename = "type")]
    pub typ: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(default, skip_serializing_if = "Url::is_zero")]
    pub owner: Url,
    #[serde(
        default,
        rename = "props",
        skip_serializing_if = "no_props",
        deserialize_with = "go_props"
    )]
    pub props: Props,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub origins: Vec<NodeRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub base: Option<NodeRef>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub merged_with: Option<NodeRef>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub links: Vec<DraftLink>,
}

impl Draft {
    /// The draft reference of the node.
    pub fn draft_ref(&self) -> NodeRef {
        NodeRef::new(self.node.clone(), 0)
    }

    pub fn link(&self, id: &LinkId) -> Option<&DraftLink> {
        self.links.iter().find(|l| &l.id == id)
    }

    /// The reason of the version the draft commits as.
    pub fn reason(&self) -> &'static str {
        match (&self.base, &self.merged_with) {
            (None, _) => crate::model::REASON_CREATE,
            (Some(b), Some(m)) if m.version != b.version => crate::model::REASON_REBASE,
            _ => crate::model::REASON_REVISE,
        }
    }
}

/// On a checkout, a draft that copies its base version as is (ADR 0142): `drafted` are the targets of its links the
/// change holds a draft of, copied as links to the drafts.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct BaseCopy {
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub drafted: Vec<NodeRef>,
}

/// The namespace of the ids of the links of a copied draft.
const BASE_LINK_SPACE: uuid::Uuid = uuid::uuid!("6f1c0f0e-8d1b-4b6a-9a52-2d6e3c1f7a40");

/// The id a copied draft gives a stored link: derived from the event and the link, so the draft the log rebuilds holds
/// the ids the edits after it name.
pub fn base_link_id(event: &str, link: &LinkId) -> LinkId {
    let name = format!("{event}\x00{link}");
    LinkId(uuid::Uuid::new_v5(&BASE_LINK_SPACE, name.as_bytes()).to_string())
}

/// A stored outgoing link of a version, as a checkout copies it.
#[derive(Debug, Clone, PartialEq)]
pub struct BaseLink {
    pub id: LinkId,
    pub typ: String,
    pub to: NodeRef,
    pub target: Url,
    pub props: Props,
}

/// A version of a node, as a checkout copies it.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct BaseNode {
    pub node: NodeRef,
    pub key: String,
    pub typ: String,
    pub state: String,
    pub owner: Url,
    pub props: Props,
}

/// The draft a checkout event copying `base` describes (Go's `CopyDraft`).
pub fn copy_draft(e: &ImpactEvent, base: &BaseNode, links: &[BaseLink]) -> Draft {
    let mut d = Draft {
        change: e.change.clone(),
        impact: e.impact.clone(),
        flow: e.flow.clone(),
        execution: e.execution.clone(),
        node: base.node.id.clone(),
        key: base.key.clone(),
        typ: base.typ.clone(),
        state: base.state.clone(),
        owner: base.owner.clone(),
        props: base.props.clone(),
        base: Some(base.node.clone()),
        ..Draft::default()
    };
    if let Some(ed) = &e.draft {
        d.execution = ed.execution.clone();
        d.merged_with = ed.merged_with.clone();
    }
    let drafted = e.from_base.as_ref().map(|b| b.drafted.as_slice()).unwrap_or_default();
    for l in links {
        let to = if drafted.contains(&l.to) {
            NodeRef::new(l.to.id.clone(), 0)
        } else {
            l.to.clone()
        };
        d.links.push(DraftLink {
            id: base_link_id(&e.id, &l.id),
            typ: l.typ.clone(),
            to,
            target: l.target.clone(),
            props: l.props.clone(),
        });
    }
    d
}

/// One operation on the impacts of a change.
#[derive(Debug, Clone, PartialEq)]
pub struct ImpactEvent {
    pub id: String,
    pub change: ChangeId,
    pub seq: i64,
    pub impact: String,
    pub op: ImpactOp,
    pub flow: String,
    pub execution: String,
    pub by: String,
    pub at: DateTime<Utc>,
    pub causation: String,
    pub state: Option<ChangeImpact>,
    pub post: Option<NodeRef>,
    pub draft: Option<Draft>,
    pub from_base: Option<BaseCopy>,
    pub pre: Option<NodeRef>,
    pub committed: Option<NodeRef>,
    pub baseline: BaselineId,
    pub branch: String,
    pub review: Option<Review>,
    pub stale: Vec<String>,
    pub patch: Option<Map<String, Value>>,
}

impl Default for ImpactEvent {
    fn default() -> Self {
        ImpactEvent {
            id: String::new(),
            change: ChangeId::default(),
            seq: 0,
            impact: String::new(),
            op: ImpactOp::default(),
            flow: String::new(),
            execution: String::new(),
            by: String::new(),
            at: zero_time(),
            causation: String::new(),
            state: None,
            post: None,
            draft: None,
            from_base: None,
            pre: None,
            committed: None,
            baseline: BaselineId::default(),
            branch: String::new(),
            review: None,
            stale: Vec::new(),
            patch: None,
        }
    }
}

impl ImpactEvent {
    /// Whether the event installs a draft for its impact on its flow.
    pub fn starts_draft(&self) -> bool {
        self.draft.is_some() && self.op.writes_post()
    }

    /// Whether the draft of a copy event has been rebuilt from its version.
    pub fn hydrated(&self) -> bool {
        self.from_base.is_none() || self.draft.as_ref().is_some_and(|d| !d.key.is_empty())
    }

    /// Whether the event carries what its operation needs (Go's `ImpactEvent.Validate`).
    pub fn validate(&self) -> Result<(), String> {
        let need = |ok: bool, what: &str| {
            if ok {
                Ok(())
            } else {
                Err(format!("event {} needs {what}", self.op.as_str()))
            }
        };
        let own_state = self.state.as_ref().is_some_and(|s| s.id == self.impact);
        match self.op {
            ImpactOp::Proposed => need(own_state, "the change impact"),
            ImpactOp::Created => {
                need(own_state && self.post.is_some(), "the change impact and its draft")?;
                need(
                    !self.post.as_ref().is_some_and(NodeRef::is_draft) || self.draft.is_some(),
                    "the initial state of the draft",
                )
            }
            ImpactOp::Transitioned | ImpactOp::CheckedOut => {
                need(
                    !self.impact.is_empty() && self.post.is_some(),
                    "a change impact and a post",
                )?;
                need(
                    self.op == ImpactOp::Transitioned
                        || !self.post.as_ref().is_some_and(NodeRef::is_draft)
                        || self.draft.is_some(),
                    "the initial state of the draft",
                )
            }
            ImpactOp::Reviewed | ImpactOp::Discarded => need(
                !self.impact.is_empty() && self.review.is_some(),
                "a change impact and a review",
            ),
            ImpactOp::Adopted => need(
                self.impact.is_empty() && !self.flow.is_empty(),
                "a flow and no change impact",
            ),
            ImpactOp::Committed => need(
                !self.impact.is_empty() && self.committed.is_some(),
                "a change impact and a committed version",
            ),
            ImpactOp::Rebased => need(
                !self.impact.is_empty() && self.pre.is_some(),
                "a change impact and a pre version",
            ),
            ImpactOp::Updated => need(
                !self.impact.is_empty() && self.post.is_some() && self.patch.as_ref().is_some_and(|p| !p.is_empty()),
                "a change impact, its draft and a patch",
            ),
            ImpactOp::Cancelled | ImpactOp::Withdrawn => need(!self.impact.is_empty(), "a change impact"),
        }
    }

    /// Whether one log entry may carry both events: of one change and one batched operation, with one envelope.
    pub fn same_entry(&self, b: &ImpactEvent) -> bool {
        self.op.batched()
            && self.change == b.change
            && self.op == b.op
            && self.flow == b.flow
            && self.execution == b.execution
            && self.by == b.by
            && self.causation == b.causation
    }
}

/// An entry of the log of a change.
#[derive(Debug, Clone, PartialEq)]
pub struct LogEntry {
    pub seq: i64,
    pub id: String,
    pub change: ChangeId,
    pub typ: String,
    pub flow: String,
    pub process: String,
    pub execution: String,
    pub subject: String,
    pub by: String,
    pub at: DateTime<Utc>,
    pub causation: String,
    /// The payload as stored: a JSON text.
    pub payload: String,
}

impl LogEntry {
    /// The stream of the entry: `fact`, `impact`, or one a use case writes.
    pub fn stream(&self) -> &str {
        self.typ.split_once('.').map_or(self.typ.as_str(), |(s, _)| s)
    }
}

/// The payload of an impact event as the log stores it (Go's `impactPayload`): the event without its envelope, in
/// the order Go writes it (the fields it shadows, `state` and `draft`, last).
#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct PayloadOut<'a> {
    op: ImpactOp,
    #[serde(skip_serializing_if = "Option::is_none")]
    post: Option<&'a NodeRef>,
    #[serde(skip_serializing_if = "Option::is_none")]
    from_base: Option<&'a BaseCopy>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pre: Option<&'a NodeRef>,
    #[serde(skip_serializing_if = "Option::is_none")]
    committed: Option<&'a NodeRef>,
    #[serde(skip_serializing_if = "BaselineId::is_empty")]
    baseline: &'a BaselineId,
    #[serde(skip_serializing_if = "str::is_empty")]
    branch: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    review: Option<&'a Review>,
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    stale: &'a [String],
    #[serde(skip_serializing_if = "Option::is_none")]
    patch: Option<&'a Map<String, Value>>,
    #[serde(skip_serializing_if = "Option::is_none")]
    state: Option<ChangeImpact>,
    #[serde(skip_serializing_if = "Option::is_none")]
    draft: Option<Draft>,
}

/// The payload of one event, as Go's `marshalImpact` writes it.
pub fn payload(e: &ImpactEvent) -> String {
    let state = e.state.as_ref().map(|s| {
        let mut s = s.clone();
        if s.id == e.impact {
            s.id.clear();
        }
        s
    });
    let draft = e.draft.as_ref().map(|d| {
        if e.from_base.is_some() {
            // the copy of a version: the version says the rest
            Draft {
                base: d.base.clone(),
                execution: d.execution.clone(),
                merged_with: d.merged_with.clone(),
                ..Draft::default()
            }
        } else {
            Draft {
                change: ChangeId::default(),
                impact: String::new(),
                flow: String::new(),
                ..d.clone()
            }
        }
    });
    gojson::to_string(&PayloadOut {
        op: e.op,
        post: e.post.as_ref(),
        from_base: e.from_base.as_ref(),
        pre: e.pre.as_ref(),
        committed: e.committed.as_ref(),
        baseline: &e.baseline,
        branch: &e.branch,
        review: e.review.as_ref(),
        stale: &e.stale,
        patch: e.patch.as_ref().filter(|p| !p.is_empty()),
        state,
        draft,
    })
}

/// The log entry of one impact event: its envelope in the columns, the rest in the payload.
pub fn entry(e: &ImpactEvent) -> LogEntry {
    LogEntry {
        seq: 0,
        id: e.id.clone(),
        change: e.change.clone(),
        typ: format!("{LOG_IMPACT}.{}", e.op.as_str()),
        flow: e.flow.clone(),
        process: String::new(),
        execution: e.execution.clone(),
        subject: e.impact.clone(),
        by: e.by.clone(),
        at: e.at,
        causation: e.causation.clone(),
        payload: payload(e),
    }
}

/// The key of the parts of an entry carrying several events.
const BATCH_KEY: &str = "impacts";

/// The id of the n-th event an entry carries.
pub fn part_id(entry: &str, n: usize) -> String {
    format!("{entry}#{n}")
}

type Parts = BTreeMap<String, Raw>;

fn parse_object(raw: &str) -> Option<Parts> {
    let m: BTreeMap<String, Box<RawValue>> = serde_json::from_str(raw).ok()?;
    Some(m.into_iter().map(|(k, v)| (k, Raw(v.get().to_string()))).collect())
}

/// The entry carrying several events of one operation (ADR 0144), Go's `ImpactBatchEntry`: `{"impacts": [part...]}`
/// with what every part holds alike hoisted next to it.
pub fn batch_entry(id: &str, events: &[ImpactEvent]) -> Result<LogEntry, String> {
    let first = events.first().ok_or("an entry carries at least one event")?;
    let mut parts = Vec::with_capacity(events.len());
    for (i, e) in events.iter().enumerate() {
        if !first.same_entry(e) {
            return Err(format!(
                "event {i} ({} of {}) is not carried with the {} of {}",
                e.op.as_str(),
                e.impact,
                first.op.as_str(),
                first.impact
            ));
        }
        let mut p = parse_object(&payload(e)).ok_or("payload of an event")?;
        p.insert("impactId".into(), Raw(gojson::to_string(&e.impact)));
        parts.push(p);
    }
    let mut top = hoist(&mut parts);
    top.insert(BATCH_KEY.into(), Raw(gojson::to_string(&parts)));
    Ok(LogEntry {
        seq: 0,
        id: id.to_string(),
        change: first.change.clone(),
        typ: format!("{LOG_IMPACT}.{}", first.op.as_str()),
        flow: first.flow.clone(),
        process: String::new(),
        execution: first.execution.clone(),
        subject: String::new(),
        by: first.by.clone(),
        at: first.at,
        causation: first.causation.clone(),
        payload: gojson::to_string(&top),
    })
}

/// Takes out of the parts what every one of them holds alike, a key or, in an object every part holds, a key of it.
fn hoist(parts: &mut [Parts]) -> Parts {
    let mut top = Parts::new();
    let keys: Vec<String> = parts[0].keys().filter(|k| *k != "impactId").cloned().collect();
    for k in keys {
        let v = parts[0][&k].clone();
        if parts[1..].iter().all(|p| p.get(&k) == Some(&v)) {
            for p in parts.iter_mut() {
                p.remove(&k);
            }
            top.insert(k, v);
            continue;
        }
        // an object in every part: what its keys hold alike
        let Some(mut subs) = parts
            .iter()
            .map(|p| p.get(&k).and_then(|r| parse_object(&r.0)))
            .collect::<Option<Vec<Parts>>>()
        else {
            continue;
        };
        if subs.len() < 2 {
            continue;
        }
        let inner = hoist(&mut subs);
        if inner.is_empty() {
            continue;
        }
        top.insert(k.clone(), Raw(gojson::to_string(&inner)));
        for (p, s) in parts.iter_mut().zip(subs) {
            p.insert(k.clone(), Raw(gojson::to_string(&s)));
        }
    }
    top
}

/// Puts back into a part what hoist took out of it.
fn unhoist(top: &Parts, part: &mut Parts) -> Result<(), String> {
    for (k, v) in top {
        let Some(w) = part.get(k) else {
            part.insert(k.clone(), v.clone());
            continue;
        };
        let inner = parse_object(&v.0).ok_or("a hoisted object")?;
        let mut sub = parse_object(&w.0).ok_or("a part's object")?;
        unhoist(&inner, &mut sub)?;
        part.insert(k.clone(), Raw(gojson::to_string(&sub)));
    }
    Ok(())
}

/// The payload of an impact event as stored, older forms included (an envelope inside it).
#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct PayloadIn {
    id: String,
    change_id: ChangeId,
    impact_id: String,
    op: ImpactOp,
    state: Option<ChangeImpact>,
    post: Option<NodeRef>,
    draft: Option<Draft>,
    from_base: Option<BaseCopy>,
    pre: Option<NodeRef>,
    committed: Option<NodeRef>,
    baseline: BaselineId,
    branch: String,
    review: Option<Review>,
    stale: Vec<String>,
    patch: Option<Value>,
}

/// The impact event of an entry recording one (Go's `ImpactEventOf`). The draft of a copy event holds its base only
/// until the graph hydrates it.
pub fn event_of(l: &LogEntry) -> Result<ImpactEvent, String> {
    let p: PayloadIn = serde_json::from_str(&l.payload).map_err(|e| format!("impact event {}: {e}", l.id))?;
    let mut e = ImpactEvent {
        id: if p.id.is_empty() { l.id.clone() } else { p.id },
        change: if p.change_id.is_empty() {
            l.change.clone()
        } else {
            p.change_id
        },
        impact: if p.impact_id.is_empty() {
            l.subject.clone()
        } else {
            p.impact_id
        },
        seq: l.seq,
        op: p.op,
        flow: l.flow.clone(),
        execution: l.execution.clone(),
        by: l.by.clone(),
        at: l.at,
        causation: l.causation.clone(),
        state: p.state,
        post: p.post,
        draft: p.draft,
        from_base: p.from_base,
        pre: p.pre,
        committed: p.committed,
        baseline: p.baseline,
        branch: p.branch,
        review: p.review,
        stale: p.stale,
        patch: match p.patch.map(gojson::floats) {
            Some(Value::Object(m)) => Some(m),
            _ => None,
        },
    };
    if let Some(s) = e.state.as_mut()
        && s.id.is_empty()
    {
        s.id = e.impact.clone();
    }
    if let Some(d) = e.draft.as_mut() {
        d.change = e.change.clone();
        d.impact = e.impact.clone();
        d.flow = e.flow.clone();
        if e.from_base.is_some()
            && let Some(b) = &d.base
        {
            d.node = b.id.clone();
        }
    }
    Ok(e)
}

/// The impact events of an entry: the one it records, or the ones it carries (Go's `ImpactEventsOf`).
pub fn events_of(l: &LogEntry) -> Result<Vec<ImpactEvent>, String> {
    if !l.payload.contains(&format!("\"{BATCH_KEY}\"")) {
        return Ok(vec![event_of(l)?]);
    }
    let mut top = parse_object(&l.payload).ok_or_else(|| format!("impact entry {}: not an object", l.id))?;
    let Some(raw) = top.remove(BATCH_KEY) else {
        return Ok(vec![event_of(l)?]);
    };
    let parts: Vec<BTreeMap<String, Box<RawValue>>> =
        serde_json::from_str(&raw.0).map_err(|e| format!("impact entry {}: {e}", l.id))?;
    let mut out = Vec::with_capacity(parts.len());
    for (i, p) in parts.into_iter().enumerate() {
        let mut p: Parts = p.into_iter().map(|(k, v)| (k, Raw(v.get().to_string()))).collect();
        unhoist(&top, &mut p)?;
        let mut part = l.clone();
        part.id = part_id(&l.id, i);
        part.payload = gojson::to_string(&p);
        if part.subject.is_empty() {
            part.subject = p
                .get("impactId")
                .and_then(|r| serde_json::from_str::<String>(&r.0).ok())
                .unwrap_or_default();
        }
        out.push(event_of(&part)?);
    }
    Ok(out)
}

/// The impacts after an event, the impact of the event at `at` in the list (none: absent). Go's `ApplyImpactEventAt`.
pub fn apply_impact_event(out: &mut Vec<ChangeImpact>, at: Option<usize>, e: &ImpactEvent) {
    if matches!(e.op, ImpactOp::Proposed | ImpactOp::Created) {
        let Some(mut cn) = e.state.clone() else { return };
        if e.op == ImpactOp::Created {
            cn.post = e.post.clone();
        }
        match at {
            Some(i) => out[i] = cn,
            None => out.push(cn),
        }
        return;
    }
    if e.op == ImpactOp::Adopted {
        for cn in out.iter_mut() {
            adopt_impact(cn, &e.flow, &e.stale);
        }
        return;
    }
    let Some(i) = at else { return };
    if e.op == ImpactOp::Withdrawn {
        out.remove(i);
        return;
    }
    let cn = &mut out[i];
    match e.op {
        ImpactOp::Transitioned | ImpactOp::CheckedOut => {
            if e.flow.is_empty() || e.flow == cn.flow {
                cn.post = e.post.clone();
                if let Some(pre) = &e.pre {
                    cn.pre = Some(pre.clone());
                }
            }
        }
        ImpactOp::Reviewed => {
            if let Some(r) = &e.review {
                cn.reviews.push(r.clone());
                if r.flow.is_empty() {
                    cn.review = r.status.clone();
                }
            }
        }
        ImpactOp::Discarded => {
            if let Some(r) = &e.review {
                cn.reviews.push(r.clone());
            }
            cn.review = REVIEW_REJECTED.into();
        }
        ImpactOp::Committed => {
            cn.committed = e.committed.clone();
            if cn.post.as_ref().is_none_or(NodeRef::is_draft) {
                cn.post = e.committed.clone();
            }
        }
        ImpactOp::Rebased => {
            cn.pre = e.pre.clone();
            cn.recheck = true;
        }
        ImpactOp::Cancelled => {
            if !e.flow.is_empty() && e.flow != cn.flow {
                return;
            }
            if e.post.is_none() && cn.intent == INTENT_CREATED {
                out.remove(i);
                return;
            }
            cn.post = e.post.clone();
        }
        _ => {}
    }
}

/// ADR 0025 §5.3 for one impact: the flow's impacts and reviews become the main flow's, the stale ones superseded.
fn adopt_impact(cn: &mut ChangeImpact, flow: &str, stale: &[String]) {
    let is_stale = |e: &str| !e.is_empty() && stale.iter().any(|s| s == e);
    if cn.flow == flow {
        cn.flow.clear();
    } else if cn.flow.is_empty() && !cn.superseded && is_stale(&cn.execution) {
        cn.superseded = true;
    }
    for r in cn.reviews.iter_mut() {
        if r.flow == flow {
            r.flow.clear();
        } else if r.flow.is_empty() && is_stale(&r.execution) {
            r.superseded = true;
        }
    }
    if !cn.superseded {
        cn.review = REVIEW_PROPOSED.into();
        for r in &cn.reviews {
            if !r.superseded && r.flow.is_empty() {
                cn.review = r.status.clone();
            }
        }
    }
}

/// What a process on a flow sees of the impacts (Go's `ImpactsSeenBy`, ADR 0025 §3, 0029 §3): `chain` the flow and
/// its ancestors, innermost first (empty: the main flow), `stale` the runs the chain invalidates. The impacts of
/// another flow or of a stale run are left out; on a flow, the post is the last one a run that is not stale wrote on
/// the innermost flow of the chain that wrote one, else on the main flow, and the review the last one on the chain or
/// the main flow neither superseded nor stale.
pub fn impacts_seen_by(
    impacts: &[ChangeImpact],
    events: &[ImpactEvent],
    chain: &[String],
    stale: &HashSet<String>,
) -> Vec<ChangeImpact> {
    let is_stale = |e: &str| !e.is_empty() && stale.contains(e);
    let in_chain = |f: &str| chain.iter().any(|c| c == f);
    let mut out = Vec::new();
    for cn in impacts {
        if cn.superseded
            || (!cn.flow.is_empty() && !in_chain(&cn.flow))
            || (!chain.is_empty() && is_stale(&cn.execution))
        {
            continue;
        }
        if chain.is_empty() {
            out.push(cn.clone());
            continue;
        }
        let mut cn = cn.clone();
        cn.post = flow_post(events, &cn.id, chain, &is_stale);
        cn.review = REVIEW_PROPOSED.into();
        for r in &cn.reviews {
            if r.superseded || (!r.flow.is_empty() && !in_chain(&r.flow)) || is_stale(&r.execution) {
                continue;
            }
            cn.review = r.status.clone();
        }
        out.push(cn);
    }
    out
}

/// The post a chain of flows sees for an impact (Go's `flowPost`).
fn flow_post(events: &[ImpactEvent], id: &str, chain: &[String], stale: &dyn Fn(&str) -> bool) -> Option<NodeRef> {
    let mut last: HashMap<&str, Option<NodeRef>> = HashMap::new();
    for e in events {
        if (!e.op.writes_post() && e.op != ImpactOp::Cancelled) || e.impact != id {
            continue;
        }
        if e.flow.is_empty() && !stale(&e.execution) {
            last.insert("", e.post.clone());
        } else if !e.flow.is_empty() && chain.contains(&e.flow) && !stale(&e.execution) {
            // a flow inside a flow may relaunch a step of its parent flow: that step's writes are stale too
            last.insert(&e.flow, e.post.clone());
        }
        if e.op == ImpactOp::Cancelled && e.post.is_none() {
            last.remove(e.flow.as_str()); // back to what the flow saw before its checkout
        }
    }
    chain
        .iter()
        .map(String::as_str)
        .chain([""])
        .find_map(|f| last.get(f).cloned().flatten())
}

/// The draft a process on a flow sees for an impact (Go's `DraftSeenBy`): the one of the innermost flow of the chain
/// (the flow, its ancestors, the main flow) holding one, as that flow's own events left it, the events of the runs
/// stale on the chain not counted.
pub fn draft_seen_by(
    events: &[ImpactEvent],
    impact: &str,
    chain: &[String],
    stale: &dyn Fn(&str) -> bool,
) -> Option<Draft> {
    let order: Vec<&str> = chain.iter().map(String::as_str).chain([""]).collect();
    let mut rows: HashMap<&str, Option<Draft>> = HashMap::new();
    for e in events {
        let gone = matches!(e.op, ImpactOp::Committed | ImpactOp::Withdrawn);
        if e.impact != impact || !order.contains(&e.flow.as_str()) || (stale(&e.execution) && !gone) {
            continue;
        }
        match e.op {
            ImpactOp::Withdrawn | ImpactOp::Committed => rows.clear(),
            ImpactOp::Created
            | ImpactOp::CheckedOut
            | ImpactOp::Transitioned
            | ImpactOp::Updated
            | ImpactOp::Cancelled => {
                let next = apply_draft(rows.get(e.flow.as_str()).and_then(Option::as_ref), e);
                rows.insert(&e.flow, next);
            }
            _ => {}
        }
    }
    order.iter().find_map(|f| rows.get(f).cloned().flatten())
}

/// The typed form of a patch, whatever the JSON decoding made of its numbers.
#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct DraftPatch {
    props: Option<Map<String, Value>>,
    unset: Vec<String>,
    owner: Url,
    state: Option<StatePatch>,
    add_link: Option<PatchLink>,
    update_link: Option<PatchLink>,
    remove_link: Option<PatchLink>,
    add_links: Vec<PatchLink>,
    update_links: Vec<PatchLink>,
    remove_links: Vec<PatchLink>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct StatePatch {
    to: String,
}

#[derive(Deserialize, Default, Clone)]
#[serde(default, rename_all = "camelCase")]
struct PatchLink {
    id: LinkId,
    #[serde(rename = "type")]
    typ: String,
    to_id: NodeId,
    to_version: f64,
    target: String,
    props: Option<Map<String, Value>>,
}

fn links_of(one: Option<PatchLink>, many: Vec<PatchLink>) -> Vec<PatchLink> {
    one.into_iter().chain(many).collect()
}

/// The draft an event of its impact and flow leaves of a draft (none: none, or no more). Go's `applyDraft`.
pub fn apply_draft(d: Option<&Draft>, e: &ImpactEvent) -> Option<Draft> {
    if matches!(e.op, ImpactOp::Created | ImpactOp::CheckedOut | ImpactOp::Transitioned)
        && let Some(n) = &e.draft
    {
        let mut n = n.clone();
        n.change = e.change.clone();
        n.impact = e.impact.clone();
        n.flow = e.flow.clone();
        return Some(n);
    }
    let d = d?;
    match e.op {
        ImpactOp::Updated | ImpactOp::Transitioned => {
            let mut n = d.clone();
            let p: DraftPatch = e
                .patch
                .as_ref()
                .and_then(|m| serde_json::from_value(Value::Object(m.clone())).ok())
                .unwrap_or_default();
            let props = p.props.unwrap_or_default();
            if !props.is_empty() || !p.unset.is_empty() {
                let m = n.props.get_or_insert_with(Map::new);
                m.extend(props);
                for k in &p.unset {
                    m.remove(k);
                }
            }
            if !p.owner.is_zero() {
                n.owner = p.owner;
            }
            if let Some(s) = p.state {
                n.state = s.to;
            }
            for a in links_of(p.add_link, p.add_links) {
                let mut l = DraftLink {
                    id: a.id,
                    typ: a.typ,
                    to: NodeRef::new(a.to_id.clone(), a.to_version as Version),
                    target: Url::default(),
                    props: a.props,
                };
                if a.to_id.is_empty() && !a.target.is_empty() {
                    l.target = Url::of(&a.target);
                }
                n.links.push(l);
            }
            for u in links_of(p.update_link, p.update_links) {
                for l in n.links.iter_mut().filter(|l| l.id == u.id) {
                    l.props = u.props.clone();
                }
            }
            for r in links_of(p.remove_link, p.remove_links) {
                n.links.retain(|l| l.id != r.id);
            }
            Some(n)
        }
        ImpactOp::Cancelled => None,
        _ => Some(d.clone()),
    }
}

/// The impacts and drafts of a change, folded from its events (Go's `FoldImpacts` and `FoldDrafts`, kept together
/// as the view of the change keeps them).
#[derive(Debug, Clone, Default)]
pub struct Fold {
    pub impacts: Vec<ChangeImpact>,
    pub drafts: Vec<Draft>,
    /// The position of each impact in `impacts`.
    at: HashMap<String, usize>,
}

impl Fold {
    pub fn impact(&self, id: &str) -> Option<&ChangeImpact> {
        self.at.get(id).map(|&i| &self.impacts[i])
    }

    /// The draft of an impact on a flow.
    pub fn draft(&self, impact: &str, flow: &str) -> Option<&Draft> {
        self.drafts.iter().find(|d| d.impact == impact && d.flow == flow)
    }

    /// The impact of a node on the main flow, by its key or its node id.
    pub fn impact_of_key(&self, key: &str) -> Option<&ChangeImpact> {
        self.impacts
            .iter()
            .find(|c| c.key == key && c.flow.is_empty() && !c.superseded)
    }

    fn reindex(&mut self) {
        self.at = self
            .impacts
            .iter()
            .enumerate()
            .map(|(i, c)| (c.id.clone(), i))
            .collect();
    }

    /// Applies one event: the impacts, then the drafts (Go's `ApplyImpactEventAt`, then `ApplyDraftEvent`).
    pub fn apply(&mut self, e: &ImpactEvent) {
        let at = self.at.get(&e.impact).copied();
        let before = self.impacts.len();
        apply_impact_event(&mut self.impacts, at, e);
        if self.impacts.len() != before || e.op == ImpactOp::Adopted {
            self.reindex();
        }
        let at = self
            .drafts
            .iter()
            .position(|d| d.impact == e.impact && d.flow == e.flow);
        match e.op {
            ImpactOp::Created
            | ImpactOp::CheckedOut
            | ImpactOp::Transitioned
            | ImpactOp::Updated
            | ImpactOp::Cancelled => match (apply_draft(at.map(|i| &self.drafts[i]), e), at) {
                (None, Some(i)) => {
                    self.drafts.remove(i);
                }
                (Some(n), Some(i)) => self.drafts[i] = n,
                (Some(n), None) => self.drafts.push(n),
                (None, None) => {}
            },
            ImpactOp::Withdrawn | ImpactOp::Committed => self.drafts.retain(|d| d.impact != e.impact),
            ImpactOp::Adopted => self.drafts.retain(|d| d.flow != e.flow),
            _ => {}
        }
        let live: HashMap<&str, bool> = self.impacts.iter().map(|c| (c.id.as_str(), !c.superseded)).collect();
        self.drafts
            .retain(|d| live.get(d.impact.as_str()).copied().unwrap_or(false));
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(s: &str) -> DateTime<Utc> {
        DateTime::parse_from_rfc3339(s).unwrap().with_timezone(&Utc)
    }

    fn created() -> ImpactEvent {
        let mut props = Map::new();
        props.insert("title".into(), Value::String("a<b".into()));
        props.insert("value".into(), serde_json::json!(1.0));
        ImpactEvent {
            id: "e1".into(),
            change: ChangeId::from("c1"),
            impact: "i1".into(),
            op: ImpactOp::Created,
            by: "system:bench".into(),
            at: at("2026-10-23T10:00:01.5Z"),
            state: Some(ChangeImpact {
                id: "i1".into(),
                key: "K-1".into(),
                typ: "bench@T000".into(),
                intent: INTENT_CREATED.into(),
                rationale: "bench".into(),
                pre: None,
                post: None,
                committed: None,
                review: REVIEW_PROPOSED.into(),
                reviews: vec![],
                via: String::new(),
                recheck: false,
                produced_by: String::new(),
                derived_from: vec![],
                execution: String::new(),
                items: vec![],
                flow: String::new(),
                superseded: false,
                created_at: at("2026-10-23T10:00:01.5Z"),
            }),
            post: Some(NodeRef::new(NodeId::from("n1"), 0)),
            draft: Some(Draft {
                change: ChangeId::from("c1"),
                impact: "i1".into(),
                node: NodeId::from("n1"),
                key: "K-1".into(),
                typ: "bench@T000".into(),
                owner: Url::new("organisation", "OrgUnit", "ORG-DEFAULT"),
                props: Some(props),
                links: vec![DraftLink {
                    id: LinkId::from("l1"),
                    typ: "bench@relates".into(),
                    to: NodeRef::new(NodeId::from("n0"), 3),
                    target: Url::default(),
                    props: None,
                }],
                ..Draft::default()
            }),
            ..ImpactEvent::default()
        }
    }

    #[test]
    fn writes_the_payload_go_writes() {
        let e = created();
        // the bytes contracts/domain writes for this event
        assert_eq!(
            payload(&e),
            r#"{"op":"created","post":{"id":"n1","version":0},"state":{"key":"K-1","type":"bench@T000","intent":"created","rationale":"bench","review":"proposed","createdAt":"2026-10-23T10:00:01.5Z"},"draft":{"node":"n1","key":"K-1","type":"bench@T000","owner":"lnk:organisation/OrgUnit/ORG-DEFAULT","props":{"title":"a\u003cb","value":1},"links":[{"id":"l1","type":"bench@relates","to":{"id":"n0","version":3}}]}}"#
        );
        let back = event_of(&entry(&e)).unwrap();
        assert_eq!(back.state, e.state);
        assert_eq!(back.draft.as_ref().map(|d| &d.key), Some(&"K-1".to_string()));
        let mut f = Fold::default();
        f.apply(&back);
        assert_eq!(f.impacts.len(), 1);
        assert_eq!(f.drafts[0].links.len(), 1);
    }

    #[test]
    fn hoists_what_a_batch_shares() {
        let review = |imp: &str| ImpactEvent {
            id: format!("r-{imp}"),
            change: ChangeId::from("c1"),
            impact: imp.into(),
            op: ImpactOp::Reviewed,
            by: "system:r".into(),
            at: at("2026-10-23T10:00:02Z"),
            review: Some(Review {
                status: REVIEW_ACCEPTED.into(),
                by: "system:r".into(),
                comment: "bench".into(),
                at: at("2026-10-23T10:00:02Z"),
                flow: String::new(),
                execution: String::new(),
                superseded: false,
                review_id: "rv".into(),
            }),
            ..ImpactEvent::default()
        };
        let l = batch_entry("b1", &[review("i1"), review("i2")]).unwrap();
        assert_eq!(
            l.payload,
            r#"{"impacts":[{"impactId":"i1"},{"impactId":"i2"}],"op":"reviewed","review":{"status":"accepted","by":"system:r","comment":"bench","at":"2026-10-23T10:00:02Z","reviewId":"rv"}}"#
        );
        let back = events_of(&l).unwrap();
        assert_eq!((back[0].impact.as_str(), back[1].id.as_str()), ("i1", "b1#1"));
        assert_eq!(back[1].review, review("i2").review);
        // a review of another comment: the rest of the review hoisted, re-written with its keys sorted
        let mut other = review("i2");
        other.review.as_mut().unwrap().comment = "other".into();
        let l = batch_entry("b1", &[review("i1"), other.clone()]).unwrap();
        assert_eq!(
            l.payload,
            r#"{"impacts":[{"impactId":"i1","review":{"comment":"bench"}},{"impactId":"i2","review":{"comment":"other"}}],"op":"reviewed","review":{"at":"2026-10-23T10:00:02Z","by":"system:r","reviewId":"rv","status":"accepted"}}"#
        );
        assert_eq!(events_of(&l).unwrap()[1].review, other.review);
    }

    #[test]
    fn derives_the_ids_of_copied_links() {
        // uuid.NewSHA1(baseLinkSpace, "e\x00l") in Go
        let id = base_link_id("e", &LinkId::from("l"));
        assert_eq!(id.as_str(), "41fe350d-7281-5d86-aecc-5e2cb2219099");
    }
}
