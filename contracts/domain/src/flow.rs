//! Flow branches of the blackboard of a change.
//!
//! The blackboard of a change is an append-only log of items (event sourcing). Relaunching a step of the action flow
//! does not rewrite it: it opens a flow branch. The items the relaunched step (and what followed) produced, and
//! everything derived from them, are marked stale; the replanned run appends its items to the new branch, where they
//! are candidates. A human then adopts the branch (the stale items become superseded, the candidates count) or
//! discards it (the candidates are rejected, the stale items count again). Every transition is a KindFlow item of the
//! same log, the state is a replay.

use std::{
    collections::{HashMap, HashSet},
    fmt,
    sync::Arc,
};

use chrono::{DateTime, Utc};
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    Change, ChangeItem, Error, ItemID, TYPE_FLOW, gotime,
    ids::string_type,
    serde_util::{is_false, null_default},
};

string_type!(
    /// FlowStatus is the state of a flow branch.
    FlowStatus
);

pub const FLOW_OPEN: FlowStatus = FlowStatus::from_static("open");
pub const FLOW_ADOPTED: FlowStatus = FlowStatus::from_static("adopted");
pub const FLOW_DISCARDED: FlowStatus = FlowStatus::from_static("discarded");

/// Flow event operations. activate / deactivate / evaluate apply to options (ADR 0032 §6).
pub const FLOW_OPEN_OP: &str = "open";
pub const FLOW_ADOPT_OP: &str = "adopt";
pub const FLOW_DISCARD_OP: &str = "discard";
pub const FLOW_ACTIVATE_OP: &str = "activate";
pub const FLOW_DEACTIVATE_OP: &str = "deactivate";
pub const FLOW_EVALUATE_OP: &str = "evaluate";

/// MainFlow names the main flow explicitly. An empty flow is the active option of the change when it has one, else
/// the main flow (ADR 0032 §6).
pub const MAIN_FLOW: &str = "main";

string_type!(
    /// OptionIntent names why an option exists relative to its parent flow (derive: a new alternative, forked from
    /// main; revise: a correction/adjustment to an already-active option or landed state; refine: a sub-branch of
    /// another option, isolating narrower work within it).
    OptionIntent
);

pub const INTENT_DERIVE: OptionIntent = OptionIntent::from_static("derive");
pub const INTENT_REVISE: OptionIntent = OptionIntent::from_static("revise");
pub const INTENT_REFINE: OptionIntent = OptionIntent::from_static("refine");

/// ValidOptionIntent reports whether s is a known OptionIntent ("" included: unspecified).
pub fn valid_option_intent(s: &OptionIntent) -> bool {
    s.is_empty() || *s == INTENT_DERIVE || *s == INTENT_REVISE || *s == INTENT_REFINE
}

/// OptionSpec describes an option of a change (ADR 0009 §3): a hypothesis explored on a flow branch of its own.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct OptionSpec {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hypothesis: String,
    #[serde(skip_serializing_if = "OptionIntent::is_empty")]
    pub intent: OptionIntent,
}

/// Option statuses (ADR 0009 §3), derived from the flow of the option.
pub const OPTION_EXPLORING: &str = "exploring";
pub const OPTION_EVALUATED: &str = "evaluated";
pub const OPTION_SELECTED: &str = "selected";
pub const OPTION_REJECTED: &str = "rejected";

/// FlowEvent is the payload of a KindFlow item.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct FlowEvent {
    /// open | adopt | discard | activate | deactivate | evaluate
    pub op: String,
    pub flow: String,
    /// open: the branch this one is forked from ("" = the main flow), the last item of that line before the fork, and
    /// the items it invalidates (the seeds and their dependents).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent: String,
    #[serde(skip_serializing_if = "ItemID::is_empty")]
    pub fork_after: ItemID,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub stale: Vec<ItemID>,
    /// StaleRuns are opaque producer ids (the Execution of the change impacts and node versions): what these produced
    /// is stale until the branch is adopted (ADR 0025). The graph only compares them, it never interprets them.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub stale_runs: Vec<String>,
    /// Origin is an opaque record of why and from where the flow was opened (for a relaunch: the step, the run, the
    /// process, the reason). Its owner is whoever opened the flow: the graph stores and returns it and never reads it
    /// (ADR 0067).
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub origin: Map<String, Value>,
    /// By is the principal that adopted or discarded the branch.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    /// Option makes the opened flow an option of the change (open only).
    #[serde(skip_serializing_if = "Option::is_none")]
    pub option: Option<OptionSpec>,
    /// Comment is the evaluation of an option (evaluate).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
}

pub(crate) fn validate_flow_event(e: Option<&FlowEvent>) -> Result<(), Error> {
    let Some(e) = e.filter(|e| !e.flow.is_empty()) else {
        return Err(Error::invalid("flow item requires flowEvent.flow"));
    };
    match e.op.as_str() {
        FLOW_OPEN_OP | FLOW_ADOPT_OP | FLOW_DISCARD_OP | FLOW_ACTIVATE_OP | FLOW_DEACTIVATE_OP
        | FLOW_EVALUATE_OP => Ok(()),
        op => Err(Error::invalid(format!("unknown flow op {op:?}"))),
    }
}

/// Flow is a flow branch, replayed from its events.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Flow {
    pub id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent: String,
    #[serde(skip_serializing_if = "ItemID::is_empty")]
    pub fork_after: ItemID,
    pub status: FlowStatus,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub stale: Vec<ItemID>,
    /// StaleRuns and Origin: see FlowEvent.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub stale_runs: Vec<String>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub origin: Map<String, Value>,
    #[serde(with = "gotime", default = "gotime::zero")]
    pub opened_at: DateTime<Utc>,
    /// DecidedAt is when the flow was adopted or discarded (None: Go's zero time).
    #[serde(with = "gotime::option")]
    pub decided_at: Option<DateTime<Utc>>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub decided_by: String,
    /// CompetesWith lists the flows adopted after this one was opened that replace the same items, or whose replaced
    /// items its candidates build on: an open flow that competes cannot be adopted any more, it is relaunched or
    /// discarded.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub competes_with: Vec<String>,
    /// Option is set when the flow is an option of the change (ADR 0009 §3); Evaluation is its last evaluation.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub option: Option<OptionSpec>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub evaluation: String,
    #[serde(skip_serializing_if = "is_false")]
    pub evaluated: bool,
    /// Active tells the option is the one the change works on (ADR 0032 §6).
    #[serde(skip_serializing_if = "is_false")]
    pub active: bool,
}

/// Default is Go's zero value: its times are Go's zero time, not the Unix epoch.
impl Default for Flow {
    fn default() -> Self {
        Flow {
            id: Default::default(),
            parent: Default::default(),
            fork_after: Default::default(),
            status: Default::default(),
            stale: Default::default(),
            stale_runs: Default::default(),
            origin: Default::default(),
            opened_at: gotime::zero(),
            decided_at: Default::default(),
            decided_by: Default::default(),
            competes_with: Default::default(),
            option: Default::default(),
            evaluation: Default::default(),
            evaluated: Default::default(),
            active: Default::default(),
        }
    }
}

impl Flow {
    /// OptionStatus is the status of an option: exploring, evaluated, selected (adopted) or rejected (discarded).
    pub fn option_status(&self) -> &'static str {
        if self.status == FLOW_ADOPTED {
            OPTION_SELECTED
        } else if self.status == FLOW_DISCARDED {
            OPTION_REJECTED
        } else if self.evaluated {
            OPTION_EVALUATED
        } else {
            OPTION_EXPLORING
        }
    }
}

#[derive(Debug, Default)]
pub(crate) struct FlowIndex {
    pub(crate) list: Vec<Flow>,
    by_id: HashMap<String, usize>,
    /// active is the open option the change works on ("" = the main flow)
    active: String,
}

impl FlowIndex {
    fn get(&self, id: &str) -> Option<&Flow> {
        self.by_id.get(id).map(|&i| &self.list[i])
    }

    /// effective is the state of a flow taking its ancestors into account: a branch is only adopted when the whole
    /// chain is; a discarded ancestor discards it.
    pub(crate) fn effective(&self, id: &str) -> FlowStatus {
        let mut st = FLOW_ADOPTED;
        let mut id = id;
        let mut hops = 0;
        while !id.is_empty() && hops < 64 {
            let Some(f) = self.get(id) else {
                return FLOW_DISCARDED;
            };
            if f.status == FLOW_DISCARDED {
                return FLOW_DISCARDED;
            }
            if f.status == FLOW_OPEN {
                st = FLOW_OPEN;
            }
            id = &f.parent;
            hops += 1;
        }
        st
    }
}

/// The replay of the flow events of a change, cached per number of items (Go's unexported `flx` / `flxN`). A clone
/// starts empty and two caches always compare equal.
#[derive(Default)]
pub struct FlowCache(Mutex<Option<(usize, Arc<FlowIndex>)>>);

impl Clone for FlowCache {
    fn clone(&self) -> Self {
        FlowCache::default()
    }
}

impl PartialEq for FlowCache {
    fn eq(&self, _: &Self) -> bool {
        true
    }
}

impl fmt::Debug for FlowCache {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("FlowCache")
    }
}

impl Change {
    /// flows replays the flow events of the log (cached per log length).
    pub(crate) fn flow_index(&self) -> Arc<FlowIndex> {
        if let Some((n, fx)) = &*self.flx.0.lock()
            && *n == self.items.len()
        {
            return fx.clone();
        }
        let fx = Arc::new(replay_flows(&self.items));
        *self.flx.0.lock() = Some((self.items.len(), fx.clone()));
        fx
    }

    /// ActiveOption is the open option the change works on, "" when it works on its main flow (ADR 0032 §6). It is
    /// read from the flow events: on the whole change, not on the view of a flow (which carries none).
    pub fn active_option(&self) -> String {
        self.flow_index().active.clone()
    }

    /// Options lists the options of the change (the flows opened as options), oldest first.
    pub fn options(&self) -> Vec<Flow> {
        self.flow_index()
            .list
            .iter()
            .filter(|f| f.option.is_some())
            .cloned()
            .collect()
    }

    /// ResolveFlow is the flow a call works on: "main" names the main flow, an empty flow is the active option (else
    /// the main flow), any other is itself.
    pub fn resolve_flow(&self, flow: &str) -> String {
        match flow {
            MAIN_FLOW => String::new(),
            "" => self.active_option(),
            _ => flow.to_string(),
        }
    }

    /// Flows lists the flow branches of the change, oldest first.
    pub fn flows(&self) -> Vec<Flow> {
        self.flow_index().list.clone()
    }

    /// Flow returns a flow branch.
    pub fn flow(&self, id: &str) -> Option<Flow> {
        self.flow_index().get(id).cloned()
    }

    /// FlowStatusOf is the effective state of a flow, ancestors included.
    pub fn flow_status_of(&self, id: &str) -> FlowStatus {
        self.flow_index().effective(id)
    }

    /// View returns the change as seen by the process running on a flow. The main flow ("") sees the log without the
    /// items of branches that are not adopted (the flow events stay, so statuses keep their derivation). A branch sees
    /// its parent's view without the items it invalidated, plus its own items: the board a replanned run works on. The
    /// result carries no flow events, its items are plain.
    pub fn view(&self, flow: &str) -> Change {
        let mut out = Change {
            items: Vec::new(),
            flx: FlowCache::default(),
            ..self.clone_header()
        };
        let fx = self.flow_index();
        if flow.is_empty() {
            out.items = self
                .items
                .iter()
                .filter(|it| it.flow.is_empty() || fx.effective(&it.flow) == FLOW_ADOPTED)
                .cloned()
                .collect();
            return out;
        }
        let Some(f) = fx.get(flow) else {
            return out;
        };
        let base = self.view(&f.parent);
        for it in base.items {
            if it.r#type == TYPE_FLOW || f.stale.contains(&it.id) {
                continue;
            }
            out.items.push(it);
        }
        for it in &self.items {
            if it.flow == flow && it.r#type != TYPE_FLOW {
                let mut it = it.clone();
                it.flow = String::new(); // the view carries no flow events: its items are plain
                out.items.push(it);
            }
        }
        out
    }

    /// The change without its items (what a view copies before it filters them).
    fn clone_header(&self) -> Change {
        Change {
            id: self.id.clone(),
            title: self.title.clone(),
            intent: self.intent.clone(),
            methodology: self.methodology.clone(),
            methodology_version: self.methodology_version.clone(),
            namespace: self.namespace.clone(),
            parent_id: self.parent_id.clone(),
            owner_org: self.owner_org.clone(),
            project_id: self.project_id.clone(),
            lifecycle: self.lifecycle.clone(),
            state: self.state.clone(),
            goal: self.goal.clone(),
            guardians: self.guardians.clone(),
            depends_on: self.depends_on.clone(),
            status: self.status.clone(),
            baseline_id: self.baseline_id.clone(),
            branch: self.branch.clone(),
            result_baseline_id: self.result_baseline_id.clone(),
            data: self.data.clone(),
            items: Vec::new(),
            nodes: self.nodes.clone(),
            created_at: self.created_at,
            flx: FlowCache::default(),
        }
    }
}

fn replay_flows(items: &[ChangeItem]) -> FlowIndex {
    let mut fx = FlowIndex::default();
    let mut open_at: HashMap<String, usize> = HashMap::new();
    let mut decided_at: HashMap<String, usize> = HashMap::new();
    for (pos, it) in items.iter().enumerate() {
        let Some(e) = it.flow_event.as_ref().filter(|_| it.r#type == TYPE_FLOW) else {
            continue;
        };
        match e.op.as_str() {
            FLOW_OPEN_OP => {
                if fx.by_id.contains_key(&e.flow) {
                    continue;
                }
                fx.by_id.insert(e.flow.clone(), fx.list.len());
                open_at.insert(e.flow.clone(), pos);
                fx.list.push(Flow {
                    id: e.flow.clone(),
                    parent: e.parent.clone(),
                    fork_after: e.fork_after.clone(),
                    status: FLOW_OPEN,
                    stale: e.stale.clone(),
                    stale_runs: e.stale_runs.clone(),
                    origin: e.origin.clone(),
                    opened_at: it.created_at,
                    option: e.option.clone(),
                    ..Default::default()
                });
            }
            FLOW_ACTIVATE_OP => {
                if let Some(&i) = fx.by_id.get(&e.flow)
                    && fx.list[i].status == FLOW_OPEN
                    && fx.list[i].option.is_some()
                {
                    fx.active = e.flow.clone();
                }
            }
            FLOW_DEACTIVATE_OP => {
                if fx.active == e.flow {
                    fx.active.clear();
                }
            }
            FLOW_EVALUATE_OP => {
                if let Some(&i) = fx.by_id.get(&e.flow)
                    && fx.list[i].status == FLOW_OPEN
                {
                    fx.list[i].evaluated = true;
                    fx.list[i].evaluation = e.comment.clone();
                }
            }
            FLOW_ADOPT_OP | FLOW_DISCARD_OP => {
                if let Some(&i) = fx.by_id.get(&e.flow)
                    && fx.list[i].status == FLOW_OPEN
                {
                    let f = &mut fx.list[i];
                    f.status = if e.op == FLOW_DISCARD_OP {
                        FLOW_DISCARDED
                    } else {
                        FLOW_ADOPTED
                    };
                    f.decided_at = Some(it.created_at).filter(|t| !gotime::is_zero(t));
                    f.decided_by = e.by.clone();
                    decided_at.insert(e.flow.clone(), pos);
                    if fx.active == e.flow {
                        fx.active.clear(); // a decided option is not worked on any more
                    }
                }
            }
            _ => {}
        }
    }
    // competition: a flow adopted after another was opened that replaces the same items
    let snapshot = fx.list.clone();
    for f in fx.list.iter_mut() {
        if f.status != FLOW_OPEN {
            continue;
        }
        for a in &snapshot {
            let decided = decided_at.get(&a.id).copied().unwrap_or(0);
            let opened = open_at.get(&f.id).copied().unwrap_or(0);
            if a.status != FLOW_ADOPTED || a.id == f.id || decided < opened {
                continue;
            }
            let mut compete = f.stale.iter().any(|id| a.stale.contains(id))
                || f.stale_runs.iter().any(|e| a.stale_runs.contains(e));
            if !compete {
                compete = items.iter().any(|it| {
                    it.flow == f.id && it.derived_from.iter().any(|d| a.stale.contains(d))
                });
            }
            if compete {
                f.competes_with.push(a.id.clone());
            }
        }
    }
    if let Some(&i) = fx.by_id.get(&fx.active) {
        fx.list[i].active = true;
    }
    fx
}

/// StaleClosure returns the items that depend on the seeds among the items of a view: the seeds, then any item
/// derived from a stale item or deciding on one, until nothing else is reached.
pub fn stale_closure(items: &[ChangeItem], seeds: &[ItemID]) -> Vec<ItemID> {
    let mut stale: HashSet<ItemID> = seeds.iter().cloned().collect();
    let mut changed = true;
    while changed {
        changed = false;
        for it in items {
            if stale.contains(&it.id) || it.r#type == TYPE_FLOW {
                continue;
            }
            let mut dep = it.derived_from.iter().any(|d| stale.contains(d));
            if !dep && let Some(d) = &it.decision {
                dep = stale.contains(&d.item);
            }
            if dep {
                stale.insert(it.id.clone());
                changed = true;
            }
        }
    }
    items
        .iter()
        .filter(|it| stale.contains(&it.id))
        .map(|it| it.id.clone())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{ITEM_CANDIDATE, ITEM_PROPOSED, ITEM_STALE, ITEM_SUPERSEDED, TYPE_ARTIFACT};

    fn art(id: &str, flow: &str) -> ChangeItem {
        ChangeItem {
            id: id.into(),
            r#type: TYPE_ARTIFACT,
            status: ITEM_PROPOSED,
            flow: flow.into(),
            ..Default::default()
        }
    }

    fn flow_ev(id: &str, op: &str, flow: &str, stale: &[&str]) -> ChangeItem {
        ChangeItem {
            id: id.into(),
            r#type: TYPE_FLOW,
            flow_event: Some(FlowEvent {
                op: op.into(),
                flow: flow.into(),
                stale: stale.iter().map(|s| ItemID::from(*s)).collect(),
                ..Default::default()
            }),
            created_at: Utc::now(),
            ..Default::default()
        }
    }

    #[test]
    fn relaunch_then_adopt() {
        let mut c = Change {
            items: vec![
                art("a", ""),
                flow_ev("f1", FLOW_OPEN_OP, "F", &["a"]),
                art("b", "F"),
            ],
            ..Default::default()
        };
        assert_eq!(c.effective_status(&"a".into()), ITEM_STALE);
        assert_eq!(c.effective_status(&"b".into()), ITEM_CANDIDATE);
        let v = c.view("F");
        assert_eq!(
            v.items.iter().map(|i| i.id.as_str()).collect::<Vec<_>>(),
            vec!["b"]
        );
        assert!(v.items[0].flow.is_empty());
        assert_eq!(c.view("").items.len(), 2);
        c.items.push(flow_ev("f2", FLOW_ADOPT_OP, "F", &[]));
        assert_eq!(c.effective_status(&"a".into()), ITEM_SUPERSEDED);
        assert!(c.in_effect(&"b".into()));
        assert_eq!(c.flow_status_of("F"), FLOW_ADOPTED);
        assert!(c.flow("F").unwrap().decided_at.is_some());
        assert_eq!(
            stale_closure(&c.items, &["a".into()]),
            vec![ItemID::from("a")]
        );
    }
}
