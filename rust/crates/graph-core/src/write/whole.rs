//! A change read whole (Go's `Graph.ChangeChecked`): its header, the impacts and items of the fold of its log, and the
//! validation of each draft the main flow sees (`impactValidations`): what the review gate of an acceptance would say
//! of it as it stands, derived, never stored. The simple world only, as the fold ([`super::work::fold_change`]).
use graph_catalog::Catalog;
use serde_json::Value;

use super::{
    check,
    review::{draft_changed, stored_draft},
    work::fold_change,
};
use crate::{
    GraphError, Kind, Result,
    change::Change,
    impact::{ChangeImpact, LOG_FACT},
    model::{ChangeId, namespace_of},
    store::ReadTx,
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
    let change = tx
        .change(id)
        .await?
        .ok_or_else(|| GraphError::not_found(format!("change {id}")))?;
    let view = fold_change(tx, id).await?;
    let mut items = Vec::new();
    for l in tx.log(id, 0, &[&format!("{LOG_FACT}.")]).await? {
        items.push(serde_json::from_str(&l.payload).map_err(GraphError::internal)?);
    }
    let validates = !matches!(change.status.as_str(), "applied" | "abandoned" | "committed");
    let ns = namespace_of(&change.namespace);
    let mut impacts = Vec::with_capacity(view.fold.impacts.len());
    for cn in &view.fold.impacts {
        let d = view.fold.draft(&cn.id, "");
        let validation = match d {
            Some(d) if validates && !cn.superseded && cn.drafted() => {
                if !d.origins.is_empty() {
                    return Err(GraphError::unimplemented(
                        "the validation of the successor of a merge or a split (ADR 0077): not served here yet",
                    ));
                }
                let modified = match &d.base {
                    Some(b) => draft_changed(&stored_draft(tx, b).await?, d),
                    None => true,
                };
                let links: Vec<_> = d.links.iter().map(|l| (l.typ.as_str(), l.props.as_ref())).collect();
                match check::version(cat, ns, &d.key, &d.typ, d.props.as_ref(), &links) {
                    Ok(()) => Some(Validation {
                        valid: true,
                        modified,
                        ..Validation::default()
                    }),
                    Err(e) if matches!(e.kind, Kind::Invalid | Kind::Conflict) => Some(Validation {
                        message: message(&e),
                        modified,
                        ..Validation::default()
                    }),
                    Err(e) => return Err(e),
                }
            }
            _ => None,
        };
        impacts.push((cn.clone(), validation));
    }
    Ok(WholeChange {
        change,
        impacts,
        items,
        seq: view.seq,
    })
}
