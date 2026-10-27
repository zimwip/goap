//! The lifecycle of a change (ADR 0058): the domain defines the lifecycle, the methodology of the change names it and
//! its guardian declares it (ADR 0168) — the states and the transitions of the domain, the criteria and the freezes of
//! the gates of the methodology merged in. A move is checked by the guardian: the CEL guard of the transition in the
//! environment of the conditions of the methodology, the vetos and objectives of its criteria (ADR 0075 §3), and the
//! guardians of the chain of the change (ADR 0106), all asked in the transaction of the move (ADR 0140); it is
//! journaled as a `transition` item and moves the state. Going back to a state the change
//! already occupied asks for a new review of what was written after it left it.
use std::collections::HashMap;

use serde_json::{Map, Value};

use graph_catalog::def::{Lifecycle, Transition};

use super::{
    change::{STATUS_ACTIVE, STATUS_DRAFT},
    decision,
    events::CHANGE_TRANSITIONED,
    items::ItemWrite,
    tx::Tx,
    update::unresolved_lifecycle,
};
use crate::{
    GraphError, Result,
    change::Change,
    guardian::{self, Board, GateResult, Guardian},
    impact::{ImpactEvent, ImpactOp, LOG_FACT, LOG_IMPACT, Review, events_of},
    model::ChangeId,
    store::{ReadTx, WriteTx},
};

/// The item type that records a move of the state of a change (`domain.TypeTransition`).
const TYPE_TRANSITION: &str = "transition";
/// The review a change impact goes back to when the change leaves the state it was written in.
const REVIEW_PROPOSED: &str = "proposed";
const REVIEW_ACCEPTED: &str = "accepted";

/// A move of the state of a change (`domain.StateMove`), with the position of the log it was written at.
#[derive(Debug, Clone, Default)]
pub struct Move {
    pub item: String,
    pub transition: String,
    pub from: String,
    pub to: String,
    /// the decision point that gated the move, when there was one
    pub decision: String,
    pub by: String,
    /// what the move froze beyond its phase (ADR 0132 §4), as it was recorded
    pub freezes: Option<Value>,
    pub seq: i64,
}

/// The state a change impact started its draft in (`graph.phase`).
#[derive(Debug, Clone, Default)]
pub struct Phase {
    pub state: String,
    pub seq: i64,
    /// started before the first move of the change, so in the state that move left
    pub before: bool,
}

/// The moves of the state of a change and the phase of each of its impacts, folded from its log (`draftState`).
#[derive(Debug, Clone, Default)]
pub struct History {
    pub moves: Vec<Move>,
    phases: HashMap<String, Phase>,
    /// the state the first move left: the state the change was in before it moved
    first: String,
    moved: bool,
    /// the state the last move reached
    state: String,
}

impl History {
    /// Folds a fact of the change: a move of the main flow moves the phase (`draftState.fact`).
    pub fn apply_item(&mut self, it: &Value, seq: i64) {
        let str = |v: &Value, k: &str| v.get(k).and_then(Value::as_str).unwrap_or_default().to_string();
        if str(it, "type") != TYPE_TRANSITION || !str(it, "flow").is_empty() {
            return;
        }
        let data = it.get("data").cloned().unwrap_or(Value::Null);
        let d = |k: &str| str(&data, k);
        if !self.moved {
            self.moved = true;
            self.first = d("from");
        }
        self.state = d("to");
        self.moves.push(Move {
            item: str(it, "id"),
            transition: d("transition"),
            from: d("from"),
            to: self.state.clone(),
            decision: d("decision"),
            by: str(it, "producedBy"),
            freezes: data.get("freezes").filter(|f| !f.is_null()).cloned(),
            seq,
        });
    }

    /// Folds an impact event: an event that starts a draft on the main flow sets the phase of its impact
    /// (`draftState.apply`).
    pub fn apply_event(&mut self, e: &ImpactEvent) {
        if !e.starts_draft() || !e.flow.is_empty() {
            return;
        }
        self.phases.insert(
            e.impact.clone(),
            Phase {
                state: self.state.clone(),
                seq: e.seq,
                before: !self.moved,
            },
        );
    }

    /// Gives the phase of an impact the position its entry took, written after it was folded (`placeholder`: the
    /// position it was folded at).
    pub fn place(&mut self, impact: &str, placeholder: i64, seq: i64) {
        if let Some(p) = self.phases.get_mut(impact)
            && p.seq == placeholder
        {
            p.seq = seq;
        }
    }

    /// The state an impact started its draft in; none when the change never moved or the impact started no draft on
    /// the main flow (`draftState.phaseOf`).
    pub fn phase_of(&self, impact: &str) -> Option<Phase> {
        let p = self.phases.get(impact)?;
        if !self.moved {
            return None;
        }
        let mut p = p.clone();
        if p.before {
            p.state = self.first.clone();
        }
        Some(p)
    }

    /// The transitions already gated by a decision point (`Change.StateMoves`).
    fn consumed(&self) -> Vec<&str> {
        self.moves
            .iter()
            .filter(|m| !m.decision.is_empty())
            .map(|m| m.decision.as_str())
            .collect()
    }
}

/// Folds the moves and the phases of a change from its log.
pub async fn history<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<History> {
    let mut out = History::default();
    for l in tx.log(id, 0, &[]).await? {
        if l.stream() == LOG_FACT {
            let it: Value = serde_json::from_str(&l.payload).map_err(GraphError::internal)?;
            out.apply_item(&it, l.seq);
        } else if l.stream() == LOG_IMPACT {
            for e in events_of(&l).map_err(GraphError::internal)? {
                out.apply_event(&e);
            }
        }
    }
    Ok(out)
}

/// The freezes in force on a change (`Change.InForceFreezes`, ADR 0132 §4): those of the moves that stand, in the order
/// they were taken, one per transition.
pub fn in_force_freezes(moves: &[Move]) -> Vec<Value> {
    let passed = passed(moves);
    let mut out = Vec::new();
    let mut seen: Vec<&str> = Vec::new();
    for m in moves.iter().rev() {
        if !passed.contains(&m.transition) || seen.contains(&m.transition.as_str()) {
            continue;
        }
        seen.push(&m.transition);
        if let Some(f) = &m.freezes {
            out.insert(0, f.clone());
        }
    }
    out
}

/// Whether a freeze leaves the impacts of the state left editable (`Freezes.KeepPhase`).
pub fn keeps_phase(f: &Value) -> bool {
    f.get("keepPhase").and_then(Value::as_bool).unwrap_or_default()
}

/// Whether a freeze lets the lifecycle of a node of a frozen type still move (`Freezes.Transitions`).
pub fn freezes_transitions(f: &Value) -> bool {
    f.get("transitions").and_then(Value::as_bool).unwrap_or_default()
}

/// The node types a freeze names (`Freezes.Types`), subtypes included.
pub fn frozen_types(f: &Value) -> Vec<String> {
    f.get("types")
        .and_then(Value::as_array)
        .map(|a| a.iter().filter_map(Value::as_str).map(str::to_string).collect())
        .unwrap_or_default()
}

/// Whether a freeze names a field of the header of a change (`Freezes.FrozenField`): the description freezes the title
/// and the intent with it.
pub fn frozen_field(f: &Value, field: &str) -> bool {
    let fields: Vec<&str> = f
        .get("fields")
        .and_then(Value::as_array)
        .map(|a| a.iter().filter_map(Value::as_str).collect())
        .unwrap_or_default();
    fields.contains(&field) || (fields.contains(&"description") && (field == "title" || field == "intent"))
}

/// The transitions of a change that stand: taken, not gone back from since (`Change.Passed`).
pub fn passed(moves: &[Move]) -> Vec<String> {
    let from = |t: &str| {
        moves
            .iter()
            .rfind(|m| m.transition == t)
            .map_or("", |m| m.from.as_str())
            .to_string()
    };
    let mut out: Vec<String> = Vec::new();
    for m in moves {
        out.retain(|t| from(t) != m.to && *t != m.transition);
        out.push(m.transition.clone());
    }
    out
}

/// Whether a freeze freezes nothing beyond the phase of the state left (`Freezes.Empty`).
fn freezes_empty(f: &Value) -> bool {
    let none = |k: &str| {
        f.get(k)
            .and_then(Value::as_array)
            .is_none_or(|a| !a.iter().any(Value::is_string))
    };
    none("fields") && none("types") && !keeps_phase(f)
}

/// The transition of a lifecycle out of a state, by name (`domain.Lifecycle.Transition`).
fn transition_of<'l>(lc: &'l Lifecycle, from: &str, name: &str) -> Option<&'l Transition> {
    lc.transitions.iter().find(|t| t.from == from && t.name == name)
}

impl<S: WriteTx> Tx<'_, S> {
    /// Moves the state of a change along a transition of its lifecycle (Go's `TransitionChange`, ADR 0058). The
    /// caller says whether the person takes it (Go's `Graph.ChangeAuthorizer`, `change:transition` by default, the
    /// permission the transition declares otherwise).
    pub async fn transition_change(
        &mut self,
        id: &ChangeId,
        transition: &str,
        decision: &str,
        authorize: &(dyn Fn(&Change, &Transition) -> Result<()> + Sync),
    ) -> Result<Change> {
        self.lock_change(id).await?;
        let c = self.change_head(id).await?;
        if matches!(c.status.as_str(), "applied" | "abandoned" | "committed") {
            return Err(GraphError::conflict(format!("change {id} is {}", c.status)));
        }
        if c.lifecycle.is_empty() {
            return Err(GraphError::invalid(format!("change {id} follows no lifecycle")));
        }
        let (lc, raw) = self.change_lifecycle(&c).await?;
        let t = transition_of(&lc, &c.state, transition)
            .ok_or_else(|| {
                GraphError::invalid(format!(
                    "change {id} in state {} has no transition {transition:?}",
                    c.state
                ))
            })?
            .clone();
        authorize(&c, &t)?;
        // the transition as the registry declares it, gates merged in: what its gate is asked on
        let raw = raw
            .get("transitions")
            .and_then(Value::as_array)
            .and_then(|ts| {
                ts.iter().find(|r| {
                    r.get("from").and_then(Value::as_str) == Some(t.from.as_str())
                        && r.get("name").and_then(Value::as_str) == Some(t.name.as_str())
                })
            })
            .cloned()
            .unwrap_or(Value::Null);
        let hist = history(self.store, id).await?;
        let consumed = hist.consumed();
        if !decision.is_empty() {
            if consumed.contains(&decision) {
                return Err(GraphError::conflict(format!(
                    "decision point {decision} already gated a transition"
                )));
            }
            let points = decision::points_of(self.store, id, self.env.now()).await?;
            if !points.iter().any(|d| d.id == decision) {
                return Err(GraphError::not_found(format!(
                    "decision point {decision} of change {id}"
                )));
            }
        }
        let mut board = guardian::judged(self.store, self.catalog(), &c, self.env.now()).await?;
        let gate = self.check_gate(&c, &t, &raw, decision, &consumed, &mut board).await?;
        self.check_guardians(&c, &board, &t.name).await?;
        let mut cur = self.change_head(id).await?;
        if cur.state != c.state {
            return Err(GraphError::conflict(format!(
                "change {id} moved to {} meanwhile",
                cur.state
            )));
        }
        let reopened = hist.moves.iter().any(|m| m.to == t.to) || t.to == lc.initial && !hist.moves.is_empty();
        let mut data = Map::from_iter([
            ("transition".to_string(), Value::from(t.name.clone())),
            ("from".to_string(), Value::from(t.from.clone())),
            ("to".to_string(), Value::from(t.to.clone())),
        ]);
        if !decision.is_empty() {
            data.insert("decision".into(), decision.into());
        }
        if let Some(f) = t.freezes.as_ref().filter(|f| !freezes_empty(f)) {
            // what the move freezes beyond its phase (ADR 0132 §4), read back from the move by the guards of the edits
            data.insert("freezes".into(), f.clone());
        }
        if !gate.reserve.is_empty() {
            // go with reserve (ADR 0075 §3): which objectives were not met and the derogation that covered each
            data.insert("unmet".into(), gate.unmet.clone().into());
            data.insert(
                "reserve".into(),
                Value::Object(
                    gate.reserve
                        .iter()
                        .map(|(k, v)| (k.clone(), Value::from(v.clone())))
                        .collect(),
                ),
            );
        }
        let by = if self.env.by.is_empty() {
            "graph.transition".to_string()
        } else {
            self.env.by.clone()
        };
        let it = ItemWrite {
            id: self.env.new_id(),
            typ: TYPE_TRANSITION.into(),
            target: format!("transition.{}", t.name),
            status: "accepted".into(),
            produced_by: by.clone(),
            data: Some(data),
            ..ItemWrite::default()
        };
        self.put_item(id, &it, "accepted").await?;
        cur.state = t.to.clone();
        if cur.status == STATUS_DRAFT {
            cur.status = STATUS_ACTIVE.into();
        }
        self.put_change(&cur).await?;
        self.raise(CHANGE_TRANSITIONED, &cur, None, "", &[]);
        if reopened {
            self.reopen_phase(&cur, &t.to, &it.id, &by).await?;
        }
        Ok(cur)
    }

    /// The lifecycle a change follows, as its guardian declares it for the version of its methodology, with the JSON
    /// it was read from (`changeLifecycle`).
    pub(super) async fn change_lifecycle(&self, c: &Change) -> Result<(Lifecycle, Value)> {
        let Some(g) = self.store.guardian() else {
            return Err(unresolved_lifecycle(c));
        };
        let raw = g.lifecycle(&c.methodology_ref()).await?;
        let lc = match &raw {
            Some(v) => Some(serde_json::from_value::<Lifecycle>(v.clone()).map_err(GraphError::internal)?),
            None => None,
        };
        match (lc, raw) {
            (Some(lc), Some(raw)) if lc.name == c.lifecycle => Ok((lc, raw)),
            _ => Err(GraphError::invalid(format!(
                "lifecycle {} of change {} is not the one of its methodology {:?}",
                c.lifecycle, c.id, c.methodology
            ))),
        }
    }

    /// The guard of a transition, then its criteria (`checkGate`), asked of the guardian on the blackboard of the
    /// change without the decision points that already gated a transition.
    async fn check_gate(
        &self,
        c: &Change,
        t: &Transition,
        raw: &Value,
        decision: &str,
        consumed: &[&str],
        board: &mut Board,
    ) -> Result<GateResult> {
        let res = GateResult::default();
        if t.guard.is_empty() && t.vetos.is_empty() && t.objectives.is_empty() {
            return Ok(res);
        }
        let Some(g) = self.store.guardian() else {
            return Err(unresolved_lifecycle(c));
        };
        let points = board.decision_points.clone();
        board.decision_points.retain(|d| !consumed.contains(&d.id.as_str()));
        let res = self.gate_of(&*g, c, t, raw, decision, board).await;
        board.decision_points = points;
        res
    }

    async fn gate_of(
        &self,
        g: &dyn Guardian,
        c: &Change,
        t: &Transition,
        raw: &Value,
        decision: &str,
        board: &Board,
    ) -> Result<GateResult> {
        if !t.guard.is_empty() {
            let ok = g
                .guard(board, &t.guard, &t.name, decision)
                .await
                .map_err(|e| GraphError::invalid(format!("guard of {} on change {}: {}", t.name, c.id, e.message)))?;
            if !ok {
                return Err(GraphError::conflict(format!(
                    "change {} cannot take {}: gate not satisfied ({})",
                    c.id, t.name, t.guard
                )));
            }
        }
        if t.vetos.is_empty() && t.objectives.is_empty() {
            return Ok(GateResult::default());
        }
        let res = g
            .gate(board, raw, decision)
            .await
            .map_err(|e| GraphError::invalid(format!("criteria of {} on change {}: {}", t.name, c.id, e.message)))?;
        if !res.vetoed.is_empty() {
            return Err(GraphError::conflict(format!(
                "change {} cannot take {}: vetoed by {}",
                c.id,
                t.name,
                res.vetoed.join(", ")
            )));
        }
        let uncovered = res.uncovered();
        if !uncovered.is_empty() {
            return Err(GraphError::conflict(format!(
                "change {} cannot take {}: objectives not met and not covered by a derogation: {}",
                c.id,
                t.name,
                uncovered.join(", ")
            )));
        }
        Ok(res)
    }

    /// The outer guardians of the chain of a change (ADR 0106), asked of the guardian about a transition or, with
    /// none, a commit (`checkGuardians`).
    pub(super) async fn check_guardians(&self, c: &Change, board: &Board, transition: &str) -> Result<()> {
        if !c.guardians.iter().any(|g| g.methodology != c.methodology) {
            return Ok(());
        }
        let Some(g) = self.store.guardian() else {
            return Ok(());
        };
        let ancestors = self.ancestors(c).await?;
        let refusal = g
            .check_guardians(board, transition, &ancestors)
            .await
            .map_err(|e| GraphError::invalid(format!("guardians of change {}: {}", c.id, e.message)))?;
        if refusal.is_empty() {
            return Ok(());
        }
        let what = if transition.is_empty() {
            "commit".to_string()
        } else {
            format!("take {transition}")
        };
        Err(GraphError::conflict(format!(
            "change {} cannot {what}: its guardian {refusal}",
            c.id
        )))
    }

    /// The parent changes of a change, nearest first.
    async fn ancestors(&self, c: &Change) -> Result<Vec<Change>> {
        let mut out = Vec::new();
        let mut parent = c.parent_id.clone();
        let mut seen = Vec::new();
        while !parent.as_str().is_empty() && !seen.contains(&parent) {
            seen.push(parent.clone());
            let Some(pc) = self.store.change(&parent).await? else {
                break;
            };
            parent = pc.parent_id.clone();
            out.push(pc);
        }
        Ok(out)
    }

    /// What was written after the change entered the state it goes back to must be reviewed again (`reopenPhase`).
    async fn reopen_phase(&mut self, c: &Change, to: &str, item: &str, by: &str) -> Result<()> {
        let hist = history(self.store, &c.id).await?;
        // the last entry into the state, the move just journaled left out
        let since = hist
            .moves
            .iter()
            .rfind(|m| m.to == to && m.item != item)
            .map_or(0, |m| m.seq);
        let view = super::work::fold_change(self.store, &c.id).await?;
        let mut events = Vec::new();
        for cn in &view.fold.impacts {
            let Some(p) = hist.phase_of(&cn.id) else { continue };
            if p.state == to || p.seq < since || cn.review != REVIEW_ACCEPTED {
                continue;
            }
            events.push(ImpactEvent {
                change: c.id.clone(),
                impact: cn.id.clone(),
                op: ImpactOp::Reviewed,
                review: Some(Review {
                    status: REVIEW_PROPOSED.into(),
                    by: by.to_string(),
                    at: self.env.now(),
                    comment: format!(
                        "written in {}, to be reviewed again: the change went back to {to}",
                        p.state
                    ),
                    ..Review::default()
                }),
                ..ImpactEvent::default()
            });
        }
        if events.is_empty() {
            return Ok(());
        }
        self.emit(events).await?;
        Ok(())
    }
}
