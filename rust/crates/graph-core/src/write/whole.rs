//! A change read whole (Go's `Graph.ChangeChecked`): its header, the impacts and items of the fold of its log, and the
//! validation of each draft by the flow it is on (`impactValidations`): what the review gate of an acceptance would say
//! of it as it stands, derived, never stored.
use std::collections::HashMap;

use graph_catalog::Catalog;
use serde_json::Value;

use super::scripts;
use super::{
    check,
    review::{draft_changed, stored_draft},
    work::{ChangeView, fold_change, fold_upto},
};
use crate::{
    GraphError, Kind, Result, change::Change, flow::FLOW_OPEN, impact::ChangeImpact, model::ChangeId, store::ReadTx,
};

/// What the gate of an acceptance says of a draft (`domain.ImpactValidation`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Validation {
    pub valid: bool,
    pub message: String,
    pub modified: bool,
}

/// A change with its impacts (each with its validation, when the change validates it), its items and the position of
/// its log they were read at.
#[derive(Debug, Clone, Default)]
pub struct WholeChange {
    pub change: Change,
    pub impacts: Vec<(ChangeImpact, Option<Validation>)>,
    /// The items as the log holds them, one per version, in order.
    pub items: Vec<Value>,
    pub seq: i64,
}

/// The readable text of a refusal (`validationMessage`): the error without the sentinel it is wrapped with.
fn message(e: &GraphError) -> String {
    let m = e.message.as_str();
    m.strip_suffix(": invalid")
        .or_else(|| m.strip_suffix(": conflict"))
        .unwrap_or(m)
        .to_string()
}

/// Reads a change whole.
pub async fn whole_change<T: ReadTx>(tx: &T, cat: &Catalog, id: &ChangeId) -> Result<WholeChange> {
    whole_change_on(tx, cat, id, None).await
}

/// Reads a change whole as a flow sees it (Go's `BlackboardIn`): its impacts the ones the flow sees, validated with
/// the drafts it sees while it is open; with no flow, every impact, validated as the main flow sees them.
pub async fn whole_change_on<T: ReadTx>(
    tx: &T,
    cat: &Catalog,
    id: &ChangeId,
    flow: Option<&str>,
) -> Result<WholeChange> {
    let change = tx
        .change(id)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("change {id}")))?;
    let view = fold_change(tx, id).await?;
    let items = view.items()?;
    let validates = !matches!(change.status.as_str(), "applied" | "abandoned" | "committed");
    let list = match flow {
        Some(f) => view.seen(f),
        None => view.fold.impacts.clone(),
    };
    // each impact validated by the flow it is on (Go's `attachValidation` per flow), or by the flow read
    let mut by_flow: HashMap<String, HashMap<String, Validation>> = HashMap::new();
    let mut impacts = Vec::with_capacity(list.len());
    for cn in &list {
        let f = flow.unwrap_or(&cn.flow);
        if validates && !by_flow.contains_key(f) {
            let vs = validations(tx, cat, &view, f).await?;
            by_flow.insert(f.to_string(), vs);
        }
        let validation = by_flow.get(f).and_then(|vs| vs.get(&cn.id)).cloned();
        impacts.push((cn.clone(), validation));
    }
    Ok(WholeChange {
        change,
        impacts,
        items,
        seq: view.seq,
    })
}

/// A change of a listing read whole (Go's `guardTx.Change`): the header already read, the impacts and items of the
/// fold of its log and its position, nothing validated.
pub async fn folded<T: ReadTx>(tx: &T, change: Change) -> Result<WholeChange> {
    let id = change.id.clone();
    let view = fold_change(tx, &id).await?;
    let items = view.items()?;
    Ok(WholeChange {
        change,
        impacts: view.fold.impacts.iter().map(|cn| (cn.clone(), None)).collect(),
        items,
        seq: view.seq,
    })
}

/// What the gate of an acceptance says of each draft a flow sees (Go's `impactValidations`): none on a flow no longer
/// open.
async fn validations<T: ReadTx>(
    tx: &T,
    cat: &Catalog,
    view: &ChangeView,
    flow: &str,
) -> Result<HashMap<String, Validation>> {
    let mut out = HashMap::new();
    if !flow.is_empty() && (view.flows.get(flow).is_none() || view.flows.effective(flow) != FLOW_OPEN) {
        return Ok(out);
    }
    let drafts = view.seen_drafts(flow);
    let seen = view.seen(flow);
    for cn in &seen {
        let Some(d) = drafts.get(&cn.id) else { continue };
        if cn.superseded || !cn.drafted() {
            continue;
        }
        let modified = match &d.base {
            Some(b) => draft_changed(&stored_draft(tx, b).await?, d),
            None => true,
        };
        let to = scripts::draft_targets(tx, cat, &d.links, &drafts).await?;
        let links: Vec<_> = d
            .links
            .iter()
            .zip(to)
            .map(|(l, to)| (l.typ.as_str(), l.props.as_ref(), to))
            .collect();
        let checked = super::review::check_origins(&seen, cn, d)
            .and_then(|()| check::version(cat, &scripts::draft_node(d), &links));
        let v = match checked {
            Ok(()) => Validation {
                valid: true,
                modified,
                ..Validation::default()
            },
            Err(e) if matches!(e.kind, Kind::Invalid | Kind::Conflict) => Validation {
                message: message(&e),
                modified,
                ..Validation::default()
            },
            Err(e) => return Err(e),
        };
        out.insert(cn.id.clone(), v);
    }
    Ok(out)
}

/// A change as its log stood at a position (Go's `ChangeAt`, ADR 0153): its impacts and items folded from the entries
/// up to it, nothing validated; at or past the position the change stands at, the change as it is.
pub async fn change_at<T: ReadTx>(tx: &T, id: &ChangeId, seq: i64) -> Result<WholeChange> {
    if seq <= 0 {
        return Err(GraphError::invalid(format!(
            "change {id}: a position of its log is above 0"
        )));
    }
    let change = tx
        .change(id)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("change {id}")))?;
    let now = fold_change(tx, id).await?;
    let upto = if seq >= now.seq { 0 } else { seq };
    let (view, _) = fold_upto(tx, id, upto).await?;
    let items = view.items()?;
    Ok(WholeChange {
        change,
        impacts: view.fold.impacts.into_iter().map(|cn| (cn, None)).collect(),
        items,
        seq: view.seq,
    })
}
