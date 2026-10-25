//! The consistency of the content of a blackboard (Go's `Graph.ValidateBoard`): what a flow sees of the items and
//! impacts of a change, checked for what a step relaunch can fix (an item built on one no longer in effect, a reference
//! to nothing) or should know of (an impact written against a version that is no longer the head).
use std::collections::{HashMap, HashSet};

use graph_catalog::Catalog;
use serde::Deserialize;
use serde_json::{Map, Value};

use crate::{
    GraphError, Result,
    flow::{FLOW_ADOPTED, FLOW_DISCARDED, FLOW_OPEN, Flows, Item, TYPE_FLOW},
    impact::LOG_FACT,
    model::{ChangeId, MAIN_BRANCH, NodeRef},
    read,
    store::ReadTx,
    write::{check, items::ItemWrite, items::validate_item, work::fold_change},
};

pub const ISSUE_ERROR: &str = "error";
pub const ISSUE_WARNING: &str = "warning";

const ITEM_PROPOSED: &str = "proposed";
const ITEM_ACCEPTED: &str = "accepted";
const ITEM_REJECTED: &str = "rejected";
const ITEM_SUPERSEDED: &str = "superseded";
const ITEM_CANDIDATE: &str = "candidate";
const ITEM_STALE: &str = "stale";

/// An inconsistency found in the content of a blackboard (`domain.BoardIssue`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct BoardIssue {
    /// The item where the problem shows.
    pub item: String,
    /// The item whose production is at fault: the item itself, or the one another wrongly builds on.
    pub culprit: String,
    pub code: String,
    pub message: String,
    pub severity: String,
}

/// An item of a change as its fact holds it, what the board reads of it.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(rename_all = "camelCase")]
struct BoardItem {
    #[serde(default)]
    id: String,
    #[serde(default, rename = "type")]
    typ: String,
    #[serde(default)]
    target: String,
    #[serde(default)]
    status: String,
    #[serde(default)]
    flow: String,
    #[serde(default)]
    data: Option<Map<String, Value>>,
    #[serde(default)]
    produced_by: String,
    #[serde(default)]
    derived_from: Vec<String>,
    #[serde(default)]
    supersedes: Vec<String>,
    #[serde(default)]
    execution: String,
    #[serde(default)]
    decision: Option<Decision>,
}

#[derive(Debug, Clone, Default, Deserialize)]
struct Decision {
    #[serde(default)]
    item: String,
    #[serde(default)]
    accept: bool,
}

/// The items of a change as a flow sees them (`Change.View`) with the flows they replay: on a branch, its own items
/// carry no flow.
struct ItemsView {
    items: Vec<BoardItem>,
    flows: Flows,
}

impl ItemsView {
    fn of(all: &[BoardItem], replayed: &[Item], fx: &Flows, flow: &str) -> ItemsView {
        let items: Vec<BoardItem> = fx
            .view(replayed, flow)
            .into_iter()
            .map(|i| {
                let mut it = all[i].clone();
                if !flow.is_empty() && it.flow == flow {
                    it.flow.clear();
                }
                it
            })
            .collect();
        let flows = Flows::replay(
            &items
                .iter()
                .zip(fx.view(replayed, flow))
                .map(|(it, i)| Item {
                    flow: it.flow.clone(),
                    ..replayed[i].clone()
                })
                .collect::<Vec<_>>(),
        );
        ItemsView { items, flows }
    }

    /// The status of an item once the flows of the view are applied (`Change.EffectiveStatus`): an item of a branch
    /// a candidate until it is adopted, rejected once discarded; one a relaunch invalidated stale, then superseded.
    fn effective_status(&self, id: &str) -> String {
        let mut st = ITEM_PROPOSED.to_string();
        let mut own = String::new();
        for it in &self.items {
            if it.id == id {
                st = it.status.clone();
                own = it.flow.clone();
            }
            if it.supersedes.iter().any(|s| s == id) {
                return ITEM_SUPERSEDED.into();
            }
        }
        for it in &self.items {
            if it.typ == "decision"
                && let Some(d) = it.decision.as_ref().filter(|d| d.item == id)
            {
                st = if d.accept { ITEM_ACCEPTED } else { ITEM_REJECTED }.into();
            }
        }
        if !own.is_empty() {
            match self.flows.effective(&own) {
                FLOW_OPEN => return ITEM_CANDIDATE.into(),
                FLOW_DISCARDED => return ITEM_REJECTED.into(),
                _ => {}
            }
        }
        for f in &self.flows.list {
            if !f.stale.iter().any(|s| s == id) {
                continue;
            }
            match self.flows.effective(&f.id) {
                FLOW_ADOPTED => return ITEM_SUPERSEDED.into(),
                FLOW_OPEN if st == ITEM_PROPOSED || st == ITEM_ACCEPTED => st = ITEM_STALE.into(),
                _ => {}
            }
        }
        st
    }

    /// Whether an item counts for the change (`Change.InEffect`).
    fn in_effect(&self, id: &str) -> bool {
        !matches!(
            self.effective_status(id).as_str(),
            ITEM_REJECTED | ITEM_SUPERSEDED | ITEM_CANDIDATE
        )
    }
}

/// Checks what a flow of a change sees (Go's `ValidateBoard`): the items in effect (their structure, the items they
/// build on or decide on), then the impacts (their shape, their reference version, whether their node moved on), in
/// the order of the log.
pub async fn validate_board<T: ReadTx>(tx: &T, cat: &Catalog, id: &ChangeId, flow: &str) -> Result<Vec<BoardIssue>> {
    let c = tx
        .change(id)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("change {id}")))?;
    let view = fold_change(tx, id).await?;
    let mut all = Vec::new();
    let mut replayed = Vec::new();
    for l in tx.log(id, 0, &[&format!("{LOG_FACT}.")]).await? {
        all.push(serde_json::from_str::<BoardItem>(&l.payload).map_err(GraphError::internal)?);
        replayed.push(Item::of(&l.payload).map_err(GraphError::internal)?);
    }
    let flow = view.flows.resolve(flow);
    let nodes = view.seen(&flow);
    let v = ItemsView::of(&all, &replayed, &view.flows, &flow);

    // the reference baseline is read for the nodes the impacts start from only (ADR 0137)
    let pres: Vec<_> = nodes
        .iter()
        .filter_map(|cn| cn.pre.as_ref().map(|p| p.id.clone()))
        .collect();
    let header = read::header_of(tx, &c.baseline_id).await?;
    let base = read::select(tx, &header, Some(pres.as_slice())).await?;

    let by_id: HashSet<&str> = v.items.iter().map(|it| it.id.as_str()).collect();
    let mut out = Vec::new();
    let mut add = |item: &str, culprit: &str, code: &str, message: String| {
        out.push(BoardIssue {
            item: item.to_string(),
            culprit: if culprit.is_empty() { item } else { culprit }.to_string(),
            code: code.to_string(),
            message,
            // relaunching a step cannot fix an outdated impact (the reference baseline stays): it is re-checked, so
            // it is reported without blocking the run
            severity: if code == "outdated" { ISSUE_WARNING } else { ISSUE_ERROR }.to_string(),
        });
    };
    let now = chrono::Utc::now();
    for it in &v.items {
        if it.typ == TYPE_FLOW || !v.in_effect(&it.id) {
            continue;
        }
        if let Err(e) = check_item(cat, it, now)? {
            add(&it.id, "", "structure", e);
        }
        // a decision may target an item that is out of effect (it is what rejected it): it only has to exist
        if let Some(d) = &it.decision
            && !by_id.contains(d.item.as_str())
        {
            add(
                &it.id,
                "",
                "dangling",
                format!("decides on item {}, which is not on the blackboard", d.item),
            );
        }
        let mut refs = it.derived_from.clone();
        refs.dedup();
        for r in &refs {
            if !by_id.contains(r.as_str()) {
                add(
                    &it.id,
                    "",
                    "dangling",
                    format!("refers to item {r}, which is not on the blackboard"),
                );
            } else if !v.in_effect(r) {
                add(
                    &it.id,
                    r,
                    "derived_from_invalid",
                    format!("builds on item {r}, which is {}", v.effective_status(r)),
                );
            }
        }
    }
    // the change impacts: the id of a change impact stands where an item id does
    for cn in &nodes {
        if let Err(e) = cn.validate() {
            add(&cn.id, "", "structure", e);
        }
        if let Some(p) = &cn.pre
            && base.get(&p.id).copied().unwrap_or_default() != p.version
            && !cn.recheck
        {
            add(
                &cn.id,
                "",
                "reference",
                format!("refers to {p} which is not in the reference baseline"),
            );
        }
        let mut outdated = cn.recheck;
        if !outdated
            && let Some(p) = &cn.pre
            && cn.post.is_none()
            && cn.committed.is_none()
        {
            // a planned impact is read against the latest version of its node where the change commits, never moved
            // by the changes that committed meanwhile (ADR 0140): it starts from there when the change is rebased
            outdated = behind_head(tx, &c, p).await?;
        }
        if outdated {
            add(
                &cn.id,
                "",
                "outdated",
                format!(
                    "the impact on {} was written against a version that is no longer the head",
                    cn.key
                ),
            );
        }
    }
    // log order: the facts, then the change impacts
    let pos: HashMap<&str, usize> = v.items.iter().enumerate().map(|(i, it)| (it.id.as_str(), i)).collect();
    out.sort_by_key(|i| pos.get(i.item.as_str()).copied().unwrap_or_default());
    Ok(out)
}

/// The structure of an item (`checkItem`): what its use case asks of it, then the data its type declares.
fn check_item(
    cat: &Catalog,
    it: &BoardItem,
    now: chrono::DateTime<chrono::Utc>,
) -> Result<std::result::Result<(), String>> {
    let w = ItemWrite {
        id: it.id.clone(),
        typ: it.typ.clone(),
        target: it.target.clone(),
        status: it.status.clone(),
        flow: it.flow.clone(),
        data: it.data.clone(),
        produced_by: it.produced_by.clone(),
        derived_from: it.derived_from.clone(),
        supersedes: it.supersedes.clone(),
        execution: it.execution.clone(),
        decision: it.decision.as_ref().map(|d| crate::write::items::ItemDecision {
            item: d.item.clone(),
            accept: d.accept,
            comment: String::new(),
        }),
        created_at: None,
        ..ItemWrite::default()
    };
    if let Err(e) = validate_item(&w, now) {
        return Ok(Err(e));
    }
    check::item_data(cat, &it.typ, it.data.as_ref().filter(|m| !m.is_empty()))
}

/// Whether a node version is no longer the latest of its node on the branch a change commits on (`behindHead`).
async fn behind_head<T: ReadTx>(tx: &T, c: &crate::change::Change, r: &NodeRef) -> Result<bool> {
    let mut target = MAIN_BRANCH.to_string();
    if !c.branch.is_empty()
        && let Some(br) = tx.branch(&c.namespace, &c.branch).await?
        && !br.parent.is_empty()
    {
        target = br.parent;
    }
    Ok(tx
        .latest_on(&r.id, &target)
        .await?
        .is_some_and(|n| n.version != r.version))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn item(id: &str, status: &str) -> BoardItem {
        BoardItem {
            id: id.into(),
            typ: "artifact".into(),
            status: status.into(),
            ..BoardItem::default()
        }
    }

    #[test]
    fn a_superseded_item_is_out_of_effect() {
        let mut b = item("b", "proposed");
        b.supersedes = vec!["a".into()];
        let v = ItemsView {
            items: vec![item("a", "accepted"), b],
            flows: Flows::default(),
        };
        assert_eq!(v.effective_status("a"), ITEM_SUPERSEDED);
        assert!(!v.in_effect("a"));
        assert!(v.in_effect("b"));
    }
}
