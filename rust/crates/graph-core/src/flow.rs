//! The flows of a change (ADR 0017, 0025; Go's `contracts/domain/flow.go`): a relaunched step opens a flow branch whose
//! items are candidates, the items it invalidated stale, until the branch is adopted or discarded; an option (ADR 0009
//! §3, ADR 0032 §6) is a flow opened as a hypothesis, which the change may work on (its active option). Every move is
//! an item of type `flow` of the log; the flows are its replay.
use std::collections::{HashMap, HashSet};

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

/// The type of the items of the flows (`domain.TypeFlow`).
pub const TYPE_FLOW: &str = "flow";

pub const FLOW_OPEN: &str = "open";
pub const FLOW_ADOPTED: &str = "adopted";
pub const FLOW_DISCARDED: &str = "discarded";

pub const OP_OPEN: &str = "open";
pub const OP_ADOPT: &str = "adopt";
pub const OP_DISCARD: &str = "discard";
pub const OP_ACTIVATE: &str = "activate";
pub const OP_DEACTIVATE: &str = "deactivate";
pub const OP_EVALUATE: &str = "evaluate";

/// Names the main flow explicitly (`domain.MainFlow`).
pub const MAIN_FLOW: &str = "main";

pub const OPTION_EXPLORING: &str = "exploring";
pub const OPTION_EVALUATED: &str = "evaluated";
pub const OPTION_SELECTED: &str = "selected";
pub const OPTION_REJECTED: &str = "rejected";

/// What an option is (`domain.OptionSpec`).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct OptionSpec {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hypothesis: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub intent: String,
}

/// The payload of a flow item, as Go marshals a `domain.FlowEvent`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct FlowEvent {
    pub op: String,
    pub flow: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub fork_after: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub stale: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub stale_runs: Vec<String>,
    #[serde(skip_serializing_if = "Map::is_empty")]
    pub origin: Map<String, Value>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub option: Option<OptionSpec>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
}

/// What the replay of the flows reads of an item of a change.
#[derive(Debug, Clone, Default)]
pub struct Item {
    pub id: String,
    pub typ: String,
    pub flow: String,
    pub derived_from: Vec<String>,
    /// the item a `decision` item decides on
    pub decides: String,
    pub created_at: Option<DateTime<Utc>>,
    pub flow_event: Option<FlowEvent>,
}

/// The fields of an item as its payload holds them.
#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ItemJson {
    #[serde(default)]
    id: String,
    #[serde(default, rename = "type")]
    typ: String,
    #[serde(default)]
    flow: String,
    #[serde(default)]
    derived_from: Vec<String>,
    #[serde(default)]
    decision: Option<DecisionJson>,
    #[serde(default)]
    flow_event: Option<FlowEvent>,
    #[serde(with = "crate::jsontext::time")]
    created_at: DateTime<Utc>,
}

#[derive(Deserialize)]
struct DecisionJson {
    #[serde(default)]
    item: String,
}

impl Item {
    /// The item a fact payload holds.
    pub fn of(payload: &str) -> Result<Item, serde_json::Error> {
        let j: ItemJson = serde_json::from_str(payload)?;
        Ok(Item {
            id: j.id,
            typ: j.typ,
            flow: j.flow,
            derived_from: j.derived_from,
            decides: j.decision.map(|d| d.item).unwrap_or_default(),
            created_at: Some(j.created_at),
            flow_event: j.flow_event,
        })
    }
}

/// A flow branch, replayed from its events (`domain.Flow`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Flow {
    pub id: String,
    pub parent: String,
    pub fork_after: String,
    pub status: String,
    pub stale: Vec<String>,
    pub stale_runs: Vec<String>,
    pub origin: Map<String, Value>,
    pub opened_at: Option<DateTime<Utc>>,
    pub decided_at: Option<DateTime<Utc>>,
    pub decided_by: String,
    /// the flows adopted after this one opened that replace the same items: it cannot be adopted any more
    pub competes_with: Vec<String>,
    pub option: Option<OptionSpec>,
    pub evaluation: String,
    pub evaluated: bool,
    pub active: bool,
}

impl Flow {
    /// The status of an option: exploring, evaluated, selected (adopted) or rejected (discarded).
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

    pub fn is_open(&self) -> bool {
        self.status == FLOW_OPEN
    }
}

/// The flows of a change, oldest first, with the option it works on.
#[derive(Debug, Clone, Default)]
pub struct Flows {
    pub list: Vec<Flow>,
    /// the open option the change works on (empty: the main flow)
    pub active: String,
}

impl Flows {
    /// Replays the flow events of the items of a change, in the order of its log (`Change.flows`).
    pub fn replay(items: &[Item]) -> Flows {
        let mut fx = Flows::default();
        let mut by_id: HashMap<String, usize> = HashMap::new();
        let (mut open_at, mut decided_at) = (HashMap::new(), HashMap::new());
        for (pos, it) in items.iter().enumerate() {
            let Some(e) = it.flow_event.as_ref().filter(|_| it.typ == TYPE_FLOW) else {
                continue;
            };
            match e.op.as_str() {
                OP_OPEN => {
                    if by_id.contains_key(&e.flow) {
                        continue;
                    }
                    by_id.insert(e.flow.clone(), fx.list.len());
                    open_at.insert(e.flow.clone(), pos);
                    fx.list.push(Flow {
                        id: e.flow.clone(),
                        parent: e.parent.clone(),
                        fork_after: e.fork_after.clone(),
                        status: FLOW_OPEN.into(),
                        stale: e.stale.clone(),
                        stale_runs: e.stale_runs.clone(),
                        origin: e.origin.clone(),
                        opened_at: it.created_at,
                        option: e.option.clone(),
                        ..Flow::default()
                    });
                }
                OP_ACTIVATE => {
                    if let Some(&i) = by_id.get(&e.flow)
                        && fx.list[i].is_open()
                        && fx.list[i].option.is_some()
                    {
                        fx.active = e.flow.clone();
                    }
                }
                OP_DEACTIVATE => {
                    if fx.active == e.flow {
                        fx.active.clear();
                    }
                }
                OP_EVALUATE => {
                    if let Some(&i) = by_id.get(&e.flow)
                        && fx.list[i].is_open()
                    {
                        fx.list[i].evaluated = true;
                        fx.list[i].evaluation = e.comment.clone();
                    }
                }
                OP_ADOPT | OP_DISCARD => {
                    if let Some(&i) = by_id.get(&e.flow)
                        && fx.list[i].is_open()
                    {
                        let f = &mut fx.list[i];
                        f.status = if e.op == OP_DISCARD {
                            FLOW_DISCARDED
                        } else {
                            FLOW_ADOPTED
                        }
                        .into();
                        f.decided_at = it.created_at;
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
        // competition: a flow adopted after another opened that replaces the same items
        let snapshot = fx.list.clone();
        for f in fx.list.iter_mut().filter(|f| f.is_open()) {
            for a in &snapshot {
                if a.status != FLOW_ADOPTED || a.id == f.id || decided_at[&a.id] < open_at[&f.id] {
                    continue;
                }
                let compete = f.stale.iter().any(|s| a.stale.contains(s))
                    || f.stale_runs.iter().any(|s| a.stale_runs.contains(s))
                    || items
                        .iter()
                        .any(|it| it.flow == f.id && it.derived_from.iter().any(|d| a.stale.contains(d)));
                if compete {
                    f.competes_with.push(a.id.clone());
                }
            }
        }
        if let Some(&i) = by_id.get(&fx.active) {
            fx.list[i].active = true;
        }
        fx
    }

    pub fn get(&self, id: &str) -> Option<&Flow> {
        self.list.iter().find(|f| f.id == id)
    }

    /// The options of the change, oldest first.
    pub fn options(&self) -> impl Iterator<Item = &Flow> {
        self.list.iter().filter(|f| f.option.is_some())
    }

    /// The state of a flow with its ancestors: adopted only when the whole chain is, discarded with an ancestor.
    pub fn effective(&self, id: &str) -> &'static str {
        let mut st = FLOW_ADOPTED;
        let mut id = id.to_string();
        for _ in 0..64 {
            if id.is_empty() {
                break;
            }
            let Some(f) = self.get(&id) else {
                return FLOW_DISCARDED;
            };
            match f.status.as_str() {
                FLOW_DISCARDED => return FLOW_DISCARDED,
                FLOW_OPEN => st = FLOW_OPEN,
                _ => {}
            }
            id = f.parent.clone();
        }
        st
    }

    /// The flows from one up to the main flow (excluded), innermost first (Go's `flowChain`).
    pub fn chain(&self, flow: &str) -> Vec<&Flow> {
        let mut out = Vec::new();
        let mut flow = flow;
        for _ in 0..64 {
            if flow.is_empty() {
                break;
            }
            let Some(f) = self.get(flow) else { break };
            out.push(f);
            flow = &f.parent;
        }
        out
    }

    /// The ids of the chain of a flow and the runs it invalidates (Go's `flowIDs` and `staleOf`).
    pub fn chain_of(&self, flow: &str) -> (Vec<String>, HashSet<String>) {
        let chain = self.chain(flow);
        let stale = chain.iter().flat_map(|f| f.stale_runs.iter().cloned()).collect();
        (chain.into_iter().map(|f| f.id.clone()).collect(), stale)
    }

    /// The flow a call works on: "main" names the main flow, an empty one the active option (else the main flow).
    pub fn resolve(&self, flow: &str) -> String {
        match flow {
            MAIN_FLOW => String::new(),
            "" => self.active.clone(),
            f => f.to_string(),
        }
    }

    /// The items a flow sees, by position (`Change.View`): the main flow, the log without the items of the branches
    /// not adopted; a branch, its parent's view without what it invalidated nor the flow events, then its own items.
    pub fn view(&self, items: &[Item], flow: &str) -> Vec<usize> {
        if flow.is_empty() {
            return (0..items.len())
                .filter(|&i| items[i].flow.is_empty() || self.effective(&items[i].flow) == FLOW_ADOPTED)
                .collect();
        }
        let Some(f) = self.get(flow) else {
            return Vec::new();
        };
        let mut out: Vec<usize> = self
            .view(items, &f.parent)
            .into_iter()
            .filter(|&i| items[i].typ != TYPE_FLOW && !f.stale.contains(&items[i].id))
            .collect();
        out.extend((0..items.len()).filter(|&i| items[i].flow == flow && items[i].typ != TYPE_FLOW));
        out
    }
}

/// The items of a view that depend on the seeds: the seeds, then any item derived from a stale one or deciding on
/// one, until nothing else is reached (`domain.StaleClosure`).
pub fn stale_closure(items: &[&Item], seeds: &[String]) -> Vec<String> {
    let mut stale: HashSet<&str> = seeds.iter().map(String::as_str).collect();
    let mut changed = true;
    while changed {
        changed = false;
        for it in items {
            if stale.contains(it.id.as_str()) || it.typ == TYPE_FLOW {
                continue;
            }
            let dep = it.derived_from.iter().any(|d| stale.contains(d.as_str()))
                || (!it.decides.is_empty() && stale.contains(it.decides.as_str()));
            if dep {
                stale.insert(&it.id);
                changed = true;
            }
        }
    }
    items
        .iter()
        .filter(|it| stale.contains(it.id.as_str()))
        .map(|it| it.id.clone())
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn ev(op: &str, flow: &str, option: bool) -> Item {
        Item {
            id: format!("{op}-{flow}"),
            typ: TYPE_FLOW.into(),
            flow_event: Some(FlowEvent {
                op: op.into(),
                flow: flow.into(),
                option: option.then(|| OptionSpec {
                    name: flow.into(),
                    ..OptionSpec::default()
                }),
                ..FlowEvent::default()
            }),
            ..Item::default()
        }
    }

    #[test]
    fn an_adopted_option_is_not_active() {
        let items = vec![ev(OP_OPEN, "a", true), ev(OP_ACTIVATE, "a", false)];
        assert_eq!(Flows::replay(&items).active, "a");
        let items = vec![
            ev(OP_OPEN, "a", true),
            ev(OP_ACTIVATE, "a", false),
            ev(OP_ADOPT, "a", false),
        ];
        let fx = Flows::replay(&items);
        assert_eq!((fx.active.as_str(), fx.list[0].option_status()), ("", OPTION_SELECTED));
    }
}
