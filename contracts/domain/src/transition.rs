//! A change that follows a lifecycle (ADR 0058) records every move of it as a KindTransition item of the main flow:
//! the journal of its state, from which the phase an impact was written in is derived.

use serde_json::Value;

use crate::{Change, ItemID, TYPE_TRANSITION};

/// StateMove is one entry of the journal of the state of a change.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StateMove {
    /// Item is the item that records the move.
    pub item: ItemID,
    pub transition: String,
    pub from: String,
    pub to: String,
    /// Decision is the decision point that gated the move, when there was one.
    pub decision: String,
    pub by: String,
}

impl Change {
    /// StateMoves are the moves of the state of the change, in order.
    pub fn state_moves(&self) -> Vec<StateMove> {
        self.items
            .iter()
            .filter(|it| it.r#type == TYPE_TRANSITION && it.flow.is_empty())
            .map(|it| {
                let s = |k: &str| {
                    it.data
                        .get(k)
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string()
                };
                StateMove {
                    item: it.id.clone(),
                    transition: s("transition"),
                    from: s("from"),
                    to: s("to"),
                    decision: s("decision"),
                    by: it.produced_by.clone(),
                }
            })
            .collect()
    }
}
