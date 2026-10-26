//! The lifecycle of a change (ADR 0058): the domain defines the lifecycle, the methodology of the change names it and
//! the graph instantiates it from what the registry declares (ADR 0163 addendum) — the states and the transitions of
//! the domain, the criteria and the freezes of the gates of the methodology merged in. A move is checked by the CEL
//! guard of the transition in the environment of the conditions of the methodology, by the vetos and objectives of its
//! criteria (ADR 0075 §3), and by the guardians of the chain of the change (ADR 0106), all in the transaction of the
//! move (ADR 0140); it is journaled as a `transition` item and moves the state. Going back to a state the change
//! already occupied asks for a new review of what was written after it left it.
use std::collections::{BTreeMap, HashMap};

use serde_json::{Map, Value, json};

use graph_catalog::def::{Lifecycle, Transition};
use graph_script::guard::{self, Guard};

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
    conditions::{activation::activation, board},
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

/// A criterion of a transition (`domain.Criterion`), as the lifecycle of the domain or a gate of the methodology
/// declares it.
#[derive(Debug, Clone, Default, serde::Deserialize)]
#[serde(default)]
struct Criterion {
    name: String,
    expr: String,
}

/// What the criteria of a transition said (`domain.GateResult`): the vetos that do not hold, the objectives not met and
/// the derogation covering each of those.
#[derive(Debug, Clone, Default)]
struct GateResult {
    vetoed: Vec<String>,
    unmet: Vec<String>,
    reserve: BTreeMap<String, String>,
}

impl GateResult {
    /// The objectives not met that no derogation covers (`GateResult.Uncovered`).
    fn uncovered(&self) -> Vec<&str> {
        self.unmet
            .iter()
            .filter(|u| !self.reserve.contains_key(*u))
            .map(String::as_str)
            .collect()
    }

    fn with_reserve(&self) -> bool {
        !self.reserve.is_empty()
    }
}

/// The variables a guard of a change reads (`condition.guardInputs`): the change with the transition asked for and the
/// decision point gating it, the facts of the blackboard and the world state of the conditions.
fn guard_env(
    act: &Map<String, Value>,
    world: &BTreeMap<String, bool>,
    transition: &str,
    decision: &str,
    integrator: Option<Value>,
) -> guard::Env {
    let mut change = act
        .get("change")
        .and_then(Value::as_object)
        .cloned()
        .unwrap_or_default();
    change.insert("transition".into(), transition.into());
    change.insert("decision".into(), decision.into());
    let mut facts = Map::new();
    for k in guard::FACTS {
        if let Some(v) = act.get(k) {
            facts.insert(k.into(), v.clone());
        }
    }
    facts.insert(
        "world".into(),
        Value::Object(world.iter().map(|(k, v)| (k.clone(), Value::from(*v))).collect()),
    );
    if let Some(i) = integrator {
        facts.insert("integrator".into(), i);
    }
    guard::Env {
        change,
        facts,
        ..guard::Env::default()
    }
}

/// Evaluates a guard expression over the blackboard of a change.
fn check(env: &guard::Env, expr: &str) -> Result<bool> {
    Guard::compile(expr)
        .and_then(|g| g.check(env))
        .map_err(GraphError::invalid)
}

/// The criteria of a transition (`condition.CheckGate`): a veto or an objective that does not hold is listed; an
/// objective not met is covered when a derogation in force names it (its rule is the name of the objective).
fn check_criteria(env: &guard::Env, t: &Transition, derogations: &[Value]) -> Result<GateResult> {
    let mut res = GateResult::default();
    let read = |v: &Value| serde_json::from_value::<Criterion>(v.clone()).unwrap_or_default();
    for c in t.vetos.iter().map(read) {
        if !check(env, &c.expr)? {
            res.vetoed.push(c.name);
        }
    }
    for c in t.objectives.iter().map(read) {
        if check(env, &c.expr)? {
            continue;
        }
        fn s<'v>(v: &'v Value, k: &str) -> &'v str {
            v.get(k).and_then(Value::as_str).unwrap_or_default()
        }
        fn b(v: &Value, k: &str) -> bool {
            v.get(k).and_then(Value::as_bool).unwrap_or_default()
        }
        if let Some(d) = derogations
            .iter()
            .find(|d| s(d, "rule") == c.name && b(d, "open") && !b(d, "expired"))
        {
            res.reserve.insert(c.name.clone(), s(d, "key").to_string());
        }
        res.unmet.push(c.name);
    }
    Ok(res)
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
        let (lc, instance) = self.change_lifecycle(&c).await?;
        let t = transition_of(&lc, &c.state, transition)
            .ok_or_else(|| {
                GraphError::invalid(format!(
                    "change {id} in state {} has no transition {transition:?}",
                    c.state
                ))
            })?
            .clone();
        authorize(&c, &t)?;
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
        let at = self.env.now();
        let wire = board::wire(self.store, self.catalog(), id, None, at).await?;
        let facets = board::facets(self.store, &c).await?;
        let mut act = activation(&wire, &facets);
        let world = instance.set.evaluate(&act).state;
        // the decision points that already gated a transition are left out of the gate (`checkGate`)
        if let Some(points) = act.get_mut("decisionPoints").and_then(Value::as_array_mut) {
            points.retain(|d| {
                let id = d.get("id").and_then(Value::as_str).unwrap_or_default();
                !consumed.contains(&id)
            });
        }
        let env = guard_env(&act, &world, &t.name, decision, None);
        let gate = self.check_gate(&c, &t, &env, &act).await?;
        self.check_guardians(&c, &act, &world, &t.name).await?;
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
        if let Some(f) = t.freezes.as_ref().filter(|f| !crate::methodologies::freezes_empty(f)) {
            // what the move freezes beyond its phase (ADR 0132 §4), read back from the move by the guards of the edits
            data.insert("freezes".into(), f.clone());
        }
        if gate.with_reserve() {
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

    /// The lifecycle a change follows and the methodology version it is instantiated from (`changeLifecycle`).
    pub(super) async fn change_lifecycle(&self, c: &Change) -> Result<(Lifecycle, crate::methodologies::Instance)> {
        let declared = crate::methodologies::Declared::read(self.store).await?;
        let instance = declared.instance(self.store, &c.methodology_ref()).await?;
        let Some(instance) = instance else {
            return Err(unresolved_lifecycle(c));
        };
        match instance.lifecycle(self.catalog())? {
            Some(lc) if lc.name == c.lifecycle => Ok((lc, instance)),
            _ => Err(GraphError::invalid(format!(
                "lifecycle {} of change {} is not the one of its methodology {:?}",
                c.lifecycle, c.id, c.methodology
            ))),
        }
    }

    /// The guard of a transition, then its criteria (`checkGate`).
    async fn check_gate(
        &self,
        c: &Change,
        t: &Transition,
        env: &guard::Env,
        act: &Map<String, Value>,
    ) -> Result<GateResult> {
        let res = GateResult::default();
        if t.guard.is_empty() && t.vetos.is_empty() && t.objectives.is_empty() {
            return Ok(res);
        }
        if !t.guard.is_empty() {
            let ok = check(env, &t.guard)
                .map_err(|e| GraphError::invalid(format!("guard of {} on change {}: {e}", t.name, c.id)))?;
            if !ok {
                return Err(GraphError::conflict(format!(
                    "change {} cannot take {}: gate not satisfied ({})",
                    c.id, t.name, t.guard
                )));
            }
        }
        if t.vetos.is_empty() && t.objectives.is_empty() {
            return Ok(res);
        }
        let derogations = act
            .get("derogations")
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let res = check_criteria(env, t, &derogations)
            .map_err(|e| GraphError::invalid(format!("criteria of {} on change {}: {e}", t.name, c.id)))?;
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

    /// The outer guardians of the chain of a change (ADR 0106), innermost first, each with its guard for a transition
    /// or, with none, for a commit (`checkGuardians` over `registrysvc.Service.CheckGuardians`).
    pub(super) async fn check_guardians(
        &self,
        c: &Change,
        act: &Map<String, Value>,
        world: &BTreeMap<String, bool>,
        transition: &str,
    ) -> Result<()> {
        let outer: Vec<_> = c
            .guardians
            .iter()
            .filter(|g| g.methodology != c.methodology)
            .cloned()
            .collect();
        if outer.is_empty() {
            return Ok(());
        }
        let declared = crate::methodologies::Declared::read(self.store).await?;
        let ancestors = self.ancestors(c).await?;
        for (i, g) in c.guardians.iter().enumerate().rev() {
            if g.methodology == c.methodology {
                continue; // its own: the lifecycle and the commits of the change
            }
            let reference = crate::methodologies::reference(&g.methodology, &g.version);
            // the version saved in the chain, else the latest one (`guardianMethodology`)
            let Some(d) = declared
                .get(&reference)
                .or_else(|| declared.get(&g.methodology))
                .cloned()
            else {
                continue; // a guardian the registry no longer knows says nothing
            };
            let inner = c
                .guardians
                .get(i + 1)
                .map_or(c.methodology.as_str(), |n| n.methodology.as_str());
            let expr = if transition.is_empty() {
                d.commit_guard(inner)
            } else {
                d.transition_guard(transition)
            };
            if expr.is_empty() {
                continue;
            }
            let integrator = ancestors.iter().find(|a| a.methodology == g.methodology).map(|a| {
                json!({"attached": true, "id": a.id.as_str(), "methodology": a.methodology, "state": a.state,
                        "status": a.status})
            });
            let env = guard_env(act, world, transition, "", integrator);
            let ok = check(&env, &expr).map_err(|e| {
                GraphError::invalid(format!("guardians of change {}: guardian {}: {e}", c.id, g.methodology))
            })?;
            if !ok {
                let what = if transition.is_empty() {
                    "commit".to_string()
                } else {
                    format!("take {transition}")
                };
                return Err(GraphError::conflict(format!(
                    "change {} cannot {what}: its guardian {} requires {expr}",
                    c.id, g.methodology
                )));
            }
        }
        Ok(())
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
