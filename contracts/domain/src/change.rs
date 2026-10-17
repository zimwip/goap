//! Changes and the facts of their blackboard (items).

use std::{
    collections::{BTreeMap, HashSet},
    sync::Arc,
};

use chrono::{DateTime, Utc};
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    BaselineID, ChangeImpact, DecisionEvent, Error, FLOW_ADOPTED, FLOW_DISCARDED, FLOW_OPEN,
    FlowEvent, HeaderValue, TYPE_DECISION_POINT, TYPE_REVIEW,
    domain::Baseline,
    flow::{FlowCache, validate_flow_event},
    gotime,
    ids::string_type,
    reviewrecord::validate_review,
    serde_util::null_default,
};

string_type!(
    /// ChangeID identifies a Change.
    ChangeID
);

string_type!(
    /// ItemID identifies a ChangeItem.
    ItemID
);

string_type!(
    /// ChangeStatus is the lifecycle state of a Change.
    ChangeStatus
);

/// A change being prepared.
pub const CHANGE_DRAFT: ChangeStatus = ChangeStatus::from_static("draft");
/// A change at work.
pub const CHANGE_ACTIVE: ChangeStatus = ChangeStatus::from_static("active");
/// ChangeCommitted: its integration into the branch it was forked from waits for the resolution of a three-way merge
/// (ADR 0056); no impact can be added to it any more. A root change that committed on its own branch stays active
/// (ADR 0102: ResultBaselineID names its last commit) until it is merged; a sub-change is committed, then integrated.
pub const CHANGE_COMMITTED: ChangeStatus = ChangeStatus::from_static("committed");
/// ChangeApplied: committed and integrated into the branch it was forked from.
pub const CHANGE_APPLIED: ChangeStatus = ChangeStatus::from_static("applied");
/// A change given up.
pub const CHANGE_ABANDONED: ChangeStatus = ChangeStatus::from_static("abandoned");

/// DataAdministrative is the key of Change.Data (value true) the engine sets on a change of a methodology that
/// manages organisation/project/policy/adapter data, the admin surface itself (ADR 0039): such work is exempt from the
/// project selector gate of the web. Only a name: the graph never reads it, and a sub-change does not inherit it.
pub const DATA_ADMINISTRATIVE: &str = "administrative";

/// DataCriticality is the key of Change.Data holding the criticality of the change (C1, C2 or C3, ADR 0075 §3): the
/// weight of its effect, from which the policy of the organisation says what verification and derogations it asks.
/// Only a name: the graph and the domain model never read it (pkg/criticality interprets it).
pub const DATA_CRITICALITY: &str = "criticality";

/// Change describes a modification of the domain graph. It starts from a reference baseline and accumulates items.
/// It is the blackboard of a process.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Change {
    pub id: ChangeID,
    pub title: String,
    pub intent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    /// MethodologyVersion is the version of its methodology the change follows (ADR 0113): the latest published one
    /// when it was created (a sub-change of the same methodology takes its parent's), kept while it runs, moved only
    /// by Graph.UpgradeChange. Empty: the latest published one, whatever it is when asked (a graph without a
    /// registry).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology_version: String,
    /// Namespace the change acts on: only its nodes can be linked to the change.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    /// ParentID is set on a sub-change: a part of the parent change, split along an organisational boundary.
    /// OwnerOrg is the key of the unit of the organisation structure responsible for it (ADR 0054): the unit holding
    /// the change, resolved when the change is created (a sub-change inherits its parent's, else the root unit) and
    /// never empty once stored. It decides which MCP adapters its actions resolve (ADR 0019) and owns the nodes the
    /// change creates.
    #[serde(rename = "parentId", skip_serializing_if = "ChangeID::is_empty")]
    pub parent_id: ChangeID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner_org: String,
    /// ProjectID is the key of the project (project structure, ADR 0039, 0054) the change acts in, named when the
    /// change is created (a sub-change inherits its parent's; none is defaulted, ADR 0091) and never empty once
    /// stored: the nodes the change creates are created in it. It changes only through Graph.MoveChange, before the
    /// change lands (existing nodes keep their project).
    #[serde(rename = "projectId", skip_serializing_if = "String::is_empty")]
    pub project_id: String,
    /// Lifecycle names the lifecycle the change follows (ADR 0058), set by CreateChange from its methodology, and
    /// State is the state it is in; both are empty when the methodology names no lifecycle. State is moved only by
    /// TransitionChange, which journals it as a KindTransition item.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub lifecycle: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub goal: String,
    /// Guardians is the guardian chain of the change (ADR 0106): the methodologies that frame it, outermost first,
    /// its own last. Computed when the change is created (or attached to, or detached from, a parent) and saved, so
    /// the change keeps the rules it started with; each guardian has its say on its transitions and its merge. Empty:
    /// its own methodology only.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub guardians: Vec<GuardianRef>,
    /// DependsOn are the changes whose work must have reached the branch this change merges into before it merges
    /// (ADR 0108): set by Graph.SetChangeDependencies, never a cycle, never an ancestor or a sub-change of the change.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub depends_on: Vec<ChangeID>,
    pub status: ChangeStatus,
    #[serde(rename = "baselineId")]
    pub baseline_id: BaselineID,
    /// Branch the change is applied to (default main).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub branch: String,
    #[serde(
        rename = "resultBaselineId",
        skip_serializing_if = "BaselineID::is_empty"
    )]
    pub result_baseline_id: BaselineID,
    /// Data is the free-form data of the change: the marks the services give it (the graph reads none, see
    /// DataAdministrative, "trigger").
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub data: Map<String, Value>,
    #[serde(deserialize_with = "null_default")]
    pub items: Vec<ChangeItem>,
    /// Nodes are the node versions the change reads, modifies or creates (ADR 0024).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub nodes: Vec<ChangeImpact>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
    /// The replay of the flow events, valid for as many items as it was computed for (a cache: never compared,
    /// never serialized, empty in a clone).
    #[doc(hidden)]
    #[serde(skip)]
    pub flx: FlowCache,
}

impl Change {
    /// MethodologyRef is the reference of the methodology of the change at the version it follows (methodology_ref).
    pub fn methodology_ref(&self) -> String {
        methodology_ref(&self.methodology, &self.methodology_version)
    }

    /// OuterGuardians are the guardians of the chain of a change outside its own methodology, innermost first: the
    /// order they are asked in after its own.
    pub fn outer_guardians(&self) -> Vec<GuardianRef> {
        self.guardians
            .iter()
            .rev()
            .filter(|g| g.methodology != self.methodology)
            .cloned()
            .collect()
    }
}

/// MethodologyRef names a methodology at a version, "<name>@<version>" (ADR 0113); the name alone when the version is
/// empty: the latest published one. The readers of a methodology that take a name take a reference too.
pub fn methodology_ref(name: &str, version: &str) -> String {
    if version.is_empty() || name.is_empty() {
        return name.to_string();
    }
    format!("{name}@{version}")
}

/// SplitMethodologyRef splits a reference made by methodology_ref: the version is empty for a bare name.
pub fn split_methodology_ref(r: &str) -> (&str, &str) {
    match r.rfind('@') {
        Some(i) if i > 0 => (&r[..i], &r[i + 1..]),
        _ => (r, ""),
    }
}

/// GuardianRef is a methodology of the guardian chain of a change (ADR 0106), at the version in force when the chain
/// was computed ("" when unknown: the latest).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GuardianRef {
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub version: String,
}

/// ChangeEvent is published by the graph service on change lifecycle events (change.created, change.item_added,
/// change.applied).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ChangeEvent {
    pub r#type: String,
    pub change: Change,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub baseline: Option<Baseline>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub items: Vec<ChangeItem>,
    /// Fields are the fields of the header a change.edited event changed (ADR 0110).
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub fields: BTreeMap<String, HeaderValue>,
    /// EndUser is the person whose action raised the event, directly or from one consequence to the next
    /// (authz.Principal.EndUser of the writer, ADR 0114): what the event sets off is attributed to them.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub end_user: String,
}

/// EditedFields returns the fields of the header a person reads that differ between two versions of a change: title,
/// intent, goal and the keys of data ("data.<key>"). Empty when none differs (Go's nil).
pub fn edited_fields(before: &Change, after: &Change) -> BTreeMap<String, HeaderValue> {
    let mut out = BTreeMap::new();
    for (name, from, to) in [
        ("title", &before.title, &after.title),
        ("intent", &before.intent, &after.intent),
        ("goal", &before.goal, &after.goal),
    ] {
        if from != to {
            out.insert(
                name.to_string(),
                HeaderValue {
                    from: Value::from(from.clone()),
                    to: Value::from(to.clone()),
                },
            );
        }
    }
    for (k, v) in &after.data {
        match before.data.get(k) {
            Some(old) if old == v => {}
            old => {
                out.insert(
                    format!("data.{k}"),
                    HeaderValue {
                        from: old.cloned().unwrap_or(Value::Null),
                        to: v.clone(),
                    },
                );
            }
        }
    }
    out
}

string_type!(
    /// ItemType classifies the facts of the blackboard (the nodes a change acts on are its change impacts).
    ItemType
);

/// A decision on another item.
pub const TYPE_DECISION: ItemType = ItemType::from_static("decision");
/// An artifact an action produced.
pub const TYPE_ARTIFACT: ItemType = ItemType::from_static("artifact");
/// TypeFlow is an event of the action flow: a step is relaunched on a new flow branch, and the branch is adopted or
/// discarded (see flow.rs).
pub const TYPE_FLOW: ItemType = ItemType::from_static("flow");
/// TypeSignal is a named notification other agents or a live parent may react to (Target is the signal's name,
/// Recipient addresses a process, "" = broadcast).
pub const TYPE_SIGNAL: ItemType = ItemType::from_static("signal");
/// TypeTransition records a move of the lifecycle of the change (ADR 0058): Data holds transition, from, to and, when
/// a decision point gated it, decision. Written only by TransitionChange.
pub const TYPE_TRANSITION: ItemType = ItemType::from_static("transition");

string_type!(
    /// ItemStatus is the review state of an item.
    ItemStatus
);

pub const ITEM_PROPOSED: ItemStatus = ItemStatus::from_static("proposed");
pub const ITEM_ACCEPTED: ItemStatus = ItemStatus::from_static("accepted");
pub const ITEM_REJECTED: ItemStatus = ItemStatus::from_static("rejected");
/// ItemSuperseded marks an item replaced by another one (rebase, merge).
pub const ITEM_SUPERSEDED: ItemStatus = ItemStatus::from_static("superseded");
/// ItemCandidate is an item of a flow branch that is neither adopted nor discarded yet.
pub const ITEM_CANDIDATE: ItemStatus = ItemStatus::from_static("candidate");
/// ItemStale is an item that a relaunched step may invalidate: it stays in effect until the relaunched flow is
/// adopted (then it is superseded).
pub const ITEM_STALE: ItemStatus = ItemStatus::from_static("stale");

/// ChangeItem is one fact on the blackboard: an artifact, a decision, a merge or a flow event.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct ChangeItem {
    pub id: ItemID,
    pub r#type: ItemType,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub target: String,
    pub status: ItemStatus,
    /// Flow is the flow branch that produced the item ("" = the main flow); FlowEvent is set on KindFlow items.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub flow: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub flow_event: Option<FlowEvent>,
    /// DecisionEvent is set on KindDecisionPoint items (ADR 0009 §4).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub decision_event: Option<DecisionEvent>,
    /// Decision is set on decision items only.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub decision: Option<Decision>,
    /// Recipient addresses a signal item to a process id ("" = broadcast).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub recipient: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub data: Map<String, Value>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub produced_by: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub derived_from: Vec<ItemID>,
    /// Supersedes lists the items this one replaces.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub supersedes: Vec<ItemID>,
    /// Execution is the journal record of the action execution that produced the item (provenance down to the model /
    /// tool calls).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub execution: String,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub created_at: DateTime<Utc>,
}

/// Decision accepts or rejects another item.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Decision {
    pub item: ItemID,
    pub accept: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
}

impl ChangeItem {
    /// Validate checks the structural consistency of an item and runs the validator its use case registered for its
    /// type (register_item_type). Whether a domain declares the type is the graph's question (ADR 0128): it knows the
    /// domains.
    pub fn validate(&self) -> Result<(), Error> {
        let t = &self.r#type;
        if *t == TYPE_FLOW {
            validate_flow_event(self.flow_event.as_ref())
        } else if *t == TYPE_DECISION_POINT {
            crate::decision::validate_decision_event(self.decision_event.as_ref())
        } else if *t == TYPE_DECISION {
            match &self.decision {
                Some(d) if !d.item.is_empty() => Ok(()),
                _ => Err(Error::invalid("decision item requires decision.item")),
            }
        } else if *t == TYPE_ARTIFACT {
            Ok(())
        } else if *t == TYPE_SIGNAL {
            if self.target.is_empty() {
                return Err(Error::invalid(
                    "signal item requires target, the name of the signal",
                ));
            }
            Ok(())
        } else if *t == TYPE_TRANSITION {
            match self.data.get("to").and_then(Value::as_str) {
                Some(s) if !s.is_empty() => Ok(()),
                _ => Err(Error::invalid("transition item requires data.to")),
            }
        } else if *t == TYPE_REVIEW {
            validate_review(self)
        } else {
            match item_validator(t) {
                Some(validate) => validate(self),
                None => Ok(()),
            }
        }
    }
}

/// The validation a use case gives the items of a type (register_item_type).
pub type ItemValidator = Arc<dyn Fn(&ChangeItem) -> Result<(), Error> + Send + Sync>;

/// The validators the use cases give their types of item (register_item_type) and the permissions a type asks of its
/// writer (require_item_permission).
struct Registry {
    m: BTreeMap<ItemType, ItemValidator>,
    perms: BTreeMap<ItemType, ItemPermission>,
}

static ITEM_TYPES: RwLock<Registry> = RwLock::new(Registry {
    m: BTreeMap::new(),
    perms: BTreeMap::new(),
});

pub(crate) fn item_validator(k: &ItemType) -> Option<ItemValidator> {
    ITEM_TYPES.read().m.get(k).cloned()
}

pub(crate) fn registered_types() -> Vec<ItemType> {
    ITEM_TYPES.read().m.keys().cloned().collect()
}

/// RegisterItemType gives the items of a type the validation of its use case, run by ChangeItem::validate: a use case
/// adds the meaning of the facts it writes to a change (pkg/risk: risks, actions, derogations) without the core naming
/// them. The type itself is declared by a domain (ADR 0128). Registration is explicit, done by the composition root
/// (the cmd/* mains) or by a test, never at load time; it is idempotent (the last validation of a kind wins) and safe
/// for concurrent use. A `None` validate accepts every item of the kind.
pub fn register_item_type(kind: ItemType, validate: Option<ItemValidator>) {
    let validate = validate.unwrap_or_else(|| Arc::new(|_: &ChangeItem| Ok(())));
    ITEM_TYPES.write().m.insert(kind, validate);
}

/// ItemPermission is what writing an item of a kind asks of the writer (require_item_permission).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ItemPermission {
    /// Permission is "<resource>:<action>", checked by the authorizer of the services on the project of the change.
    pub permission: String,
    /// SubjectField, when set, is the data field naming the principal who answers for the item: it must be the
    /// writer's (a platform service acting by itself excepted), as someone signs for themselves.
    pub subject_field: String,
}

/// RequireItemPermission makes the write of the items of a kind subject to a permission (ADR 0075): the graph asks
/// its ItemAuthorizer before it stores them and knows nothing of what the kind means. Registration is explicit and
/// idempotent, like register_item_type.
pub fn require_item_permission(kind: ItemType, p: ItemPermission) {
    ITEM_TYPES.write().perms.insert(kind, p);
}

/// ItemPermissionOf returns the permission the items of a kind ask of their writer, if any.
pub fn item_permission_of(kind: &ItemType) -> Option<ItemPermission> {
    ITEM_TYPES.read().perms.get(kind).cloned()
}

impl Change {
    /// ItemsOfType returns the items of the given kind.
    pub fn items_of_type(&self, k: &ItemType) -> Vec<&ChangeItem> {
        self.items.iter().filter(|it| it.r#type == *k).collect()
    }

    /// Item returns the item with the given id.
    pub fn item(&self, id: &ItemID) -> Option<&ChangeItem> {
        self.items.iter().find(|it| it.id == *id)
    }

    /// EffectiveStatus returns the status of an item after applying the latest decision targeting it and the flow
    /// events of the change: an item of a flow branch is a candidate until the branch is adopted (and rejected when it
    /// is discarded), an item invalidated by a relaunched step is stale until the new flow is adopted (then
    /// superseded).
    pub fn effective_status(&self, id: &ItemID) -> ItemStatus {
        let mut st = ITEM_PROPOSED;
        let mut own = "";
        for it in &self.items {
            if it.id == *id {
                st = it.status.clone();
                own = &it.flow;
            }
            if it.supersedes.contains(id) {
                return ITEM_SUPERSEDED;
            }
        }
        for it in &self.items {
            if it.r#type == TYPE_DECISION
                && let Some(d) = &it.decision
                && d.item == *id
            {
                st = if d.accept {
                    ITEM_ACCEPTED
                } else {
                    ITEM_REJECTED
                };
            }
        }
        let fx = self.flow_index();
        if !own.is_empty() {
            let e = fx.effective(own);
            if e == FLOW_OPEN {
                return ITEM_CANDIDATE;
            }
            if e == FLOW_DISCARDED {
                return ITEM_REJECTED;
            }
        }
        for f in &fx.list {
            if !f.stale.contains(id) {
                continue;
            }
            let e = fx.effective(&f.id);
            if e == FLOW_ADOPTED {
                return ITEM_SUPERSEDED;
            }
            if e == FLOW_OPEN && (st == ITEM_PROPOSED || st == ITEM_ACCEPTED) {
                st = ITEM_STALE;
            }
        }
        st
    }

    /// InEffect reports whether an item counts for the change: it is neither rejected, superseded nor the candidate
    /// of a flow branch.
    pub fn in_effect(&self, id: &ItemID) -> bool {
        let st = self.effective_status(id);
        st != ITEM_REJECTED && st != ITEM_SUPERSEDED && st != ITEM_CANDIDATE
    }

    /// Active reports whether an item is not superseded.
    pub fn active(&self, id: &ItemID) -> bool {
        let st = self.effective_status(id);
        st != ITEM_SUPERSEDED && st != ITEM_CANDIDATE
    }

    /// ReferencedNodes lists every domain reference held by the change impacts.
    pub fn referenced_nodes(&self) -> Vec<crate::NodeRef> {
        let mut seen = HashSet::new();
        let mut out = Vec::new();
        for cn in &self.nodes {
            for r in [&cn.pre, &cn.post, &cn.landed].into_iter().flatten() {
                if r.is_zero() || !seen.insert(r.clone()) {
                    continue;
                }
                out.push(r.clone());
            }
        }
        out
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn change_item_validate_signal() {
        let valid = ChangeItem {
            r#type: TYPE_SIGNAL,
            target: "review_needed".into(),
            recipient: "P1".into(),
            ..Default::default()
        };
        valid.validate().expect("valid signal");
        let invalid = ChangeItem {
            r#type: TYPE_SIGNAL,
            ..Default::default()
        };
        assert!(
            invalid.validate().is_err(),
            "signal item without type should fail validation"
        );
    }

    #[test]
    fn edited_fields_test() {
        let before = Change {
            title: "a".into(),
            intent: "x".into(),
            goal: "g".into(),
            data: serde_json::json!({"k": 1}).as_object().unwrap().clone(),
            ..Default::default()
        };
        let f = edited_fields(&before, &before);
        assert!(f.is_empty(), "nothing edited: {f:?}");
        let mut after = before.clone();
        after.intent = "y".into();
        after.data = serde_json::json!({"k": 2, "n": true})
            .as_object()
            .unwrap()
            .clone();
        let f = edited_fields(&before, &after);
        assert!(
            f.len() == 3
                && f["intent"].from == "x"
                && f["intent"].to == "y"
                && f["data.k"].to == 2
                && f["data.n"].from.is_null(),
            "edited {f:?}"
        );
    }

    #[test]
    fn change_json_shape() {
        let c = Change {
            id: "C1".into(),
            status: CHANGE_ACTIVE,
            ..Default::default()
        };
        let s = serde_json::to_string(&c).unwrap();
        assert_eq!(
            s,
            r#"{"id":"C1","title":"","intent":"","status":"active","baselineId":"","items":[],"createdAt":"1970-01-01T00:00:00Z"}"#
        );
        let back: Change =
            serde_json::from_str(r#"{"id":"C1","items":null,"createdAt":"0001-01-01T00:00:00Z"}"#)
                .unwrap();
        assert!(back.items.is_empty() && gotime::is_zero(&back.created_at));
        assert_eq!(split_methodology_ref("sdlc@0.9.0"), ("sdlc", "0.9.0"));
        assert_eq!(split_methodology_ref("@x"), ("@x", ""));
        assert_eq!(methodology_ref("sdlc", ""), "sdlc");
    }
}
