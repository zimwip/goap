//! Port of `transition.go`: the lifecycle of a change (ADR 0058) and its guardian chain (ADR 0106); the
//! [`crate::ChangeLifecycles`] seam is in graph.rs.

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    Blackboard, CHANGE_ABANDONED, CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT,
    Change, ChangeID, ChangeImpactID, ChangeItem, DecisionPoint, FACET_DECISION_POINTS, GateResult,
    GuardianRef, IMPACT_REVIEWED, ITEM_ACCEPTED, ImpactEvent, ItemID, LOG_FACT, LOG_IMPACT,
    LogFilter, REVIEW_ACCEPTED, REVIEW_PROPOSED, Review, TYPE_TRANSITION, Transition,
    decision_points_of, methodology_ref,
};
use serde_json::{Map, Value, json};

use crate::api::{NewChange, TransitionRequest};
use crate::changeimpact::change_open;
use crate::changelog::put_item;
use crate::flow::first_non_empty;
use crate::lifecycle::invalidf;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// guardianChain is the guardian chain of a new change (ADR 0106): a sub-change takes its parent's chain and adds
    /// its own methodology; a root change gets the chain the registry computes for its methodology in its project.
    pub(crate) async fn guardian_chain(
        &self,
        ctx: &Ctx,
        input: &NewChange,
        version: &str,
    ) -> Result<Vec<GuardianRef>> {
        let Some(lc) = &self.lifecycles else {
            return Ok(Vec::new());
        };
        if input.methodology.is_empty() {
            return Ok(Vec::new());
        }
        if input.parent_id.is_empty() {
            return lc
                .guardians(
                    ctx,
                    &methodology_ref(&input.methodology, version),
                    &input.project_id,
                )
                .await;
        }
        let parent = self.change(ctx, &input.parent_id).await?;
        Ok(chain_under(
            &parent,
            &input.methodology,
            Some(&GuardianRef {
                methodology: input.methodology.clone(),
                version: version.to_string(),
            }),
        ))
    }
}

/// chainUnder is the chain of a change of methodology under a parent: the parent's chain (its own methodology when it
/// has none) then the methodology, unless it is already the last one. own, when set, is the version-pinned reference
/// of the methodology.
pub(crate) fn chain_under(
    parent: &Change,
    methodology: &str,
    own: Option<&GuardianRef>,
) -> Vec<GuardianRef> {
    let mut chain = parent.guardians.clone();
    if chain.is_empty() && !parent.methodology.is_empty() {
        chain = vec![GuardianRef {
            methodology: parent.methodology.clone(),
            ..Default::default()
        }];
    }
    if !methodology.is_empty() && chain.last().is_none_or(|l| l.methodology != methodology) {
        chain.push(match own {
            Some(r) => r.clone(),
            None => GuardianRef {
                methodology: methodology.to_string(),
                ..Default::default()
            },
        });
    }
    chain
}

// ---- the lifecycle of a change (ADR 0058) ------------------------------------------------------------------------
//
// The domain defines the lifecycle, the methodology of the change names it, the change holds its state; a transition
// is checked by the CEL guard of the lifecycle in the environment of the conditions, journaled as a transition item,
// and moves the state. Leaving a state freezes the change impacts written in it; going back to a state it already
// occupied unfreezes it and asks for a new review of what was written after.

/// phase is the state a change impact was last written in, and the position of the log it was written at.
#[derive(Debug, Clone, Default)]
pub(crate) struct Phase {
    pub(crate) state: String,
    pub(crate) seq: i64,
}

impl Graph {
    /// changeLifecycle resolves the lifecycle a change follows.
    pub(crate) async fn change_lifecycle(
        &self,
        ctx: &Ctx,
        c: &Change,
    ) -> Result<Option<goap_domain::Lifecycle>> {
        if c.lifecycle.is_empty() {
            return Ok(None);
        }
        let Some(lcs) = &self.lifecycles else {
            return Err(invalidf(format!(
                "change {} follows lifecycle {}, which nothing resolves",
                c.id, c.lifecycle
            )));
        };
        let lc = lcs.lifecycle(ctx, &c.methodology_ref()).await?;
        match lc {
            Some(lc) if lc.name == c.lifecycle => Ok(Some(lc)),
            _ => Err(invalidf(format!(
                "lifecycle {} of change {} is not the one of its methodology {:?}",
                c.lifecycle, c.id, c.methodology
            ))),
        }
    }

    /// TransitionChange moves the state of a change along the transition named by the request (ADR 0058).
    pub async fn transition_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: TransitionRequest,
    ) -> Result<Change> {
        let bb = self.blackboard(ctx, id).await?;
        let c = bb.change.clone();
        if c.status == CHANGE_APPLIED
            || c.status == CHANGE_ABANDONED
            || c.status == CHANGE_COMMITTED
        {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is {}", c.status),
            ));
        }
        if c.lifecycle.is_empty() {
            return Err(invalidf(format!("change {id} follows no lifecycle")));
        }
        let lc = self.change_lifecycle(ctx, &c).await?.unwrap_or_default();
        let Some(t) = lc.transition(&c.state, &input.transition).cloned() else {
            return Err(invalidf(format!(
                "change {id} in state {} has no transition {:?}",
                c.state, input.transition
            )));
        };
        if let Some(a) = &self.change_authorizer {
            a.authorize(ctx, &c, &t).await?;
        }
        let mut consumed: HashSet<String> = HashSet::new();
        for m in c.state_moves() {
            if !m.decision.is_empty() {
                consumed.insert(m.decision);
            }
        }
        if !input.decision.is_empty() {
            if consumed.contains(&input.decision) {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "decision point {} already gated a transition",
                        input.decision
                    ),
                ));
            }
            let found = c
                .decision_points_at(self.now(), self.decision_policy.as_deref())
                .iter()
                .any(|d| d.id == input.decision);
            if !found {
                return Err(Error::wrap(
                    ErrorKind::NotFound,
                    format!("decision point {} of change {id}", input.decision),
                ));
            }
        }
        let gate = self
            .check_gate(ctx, &bb, &t, &input.decision, &consumed)
            .await?;
        self.check_guardians(ctx, &bb, &t.name).await?;
        let mut tx = self.change_tx(ctx, id).await?;
        let mut cur = change_open(ctx, &mut tx, id).await?;
        if cur.state != c.state {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} moved to {} meanwhile", cur.state),
            ));
        }
        let moves = cur.state_moves();
        let reopened =
            moves.iter().any(|m| m.to == t.to) || (t.to == lc.initial && !moves.is_empty());
        let mut data = Map::new();
        data.insert("transition".into(), Value::String(t.name.clone()));
        data.insert("from".into(), Value::String(t.from.clone()));
        data.insert("to".into(), Value::String(t.to.clone()));
        if !input.decision.is_empty() {
            data.insert("decision".into(), Value::String(input.decision.clone()));
        }
        if gate.with_reserve() {
            // go with reserve (ADR 0075 §3): which objectives were not met and the derogation that covered each
            data.insert("unmet".into(), json!(gate.unmet));
            data.insert("reserve".into(), json!(gate.reserve));
        }
        let caller = self.caller_of(ctx);
        let it = ChangeItem {
            id: ItemID::new(self.new_id()),
            r#type: TYPE_TRANSITION,
            target: format!("transition.{}", t.name),
            status: ITEM_ACCEPTED,
            produced_by: first_non_empty(&[&input.by, &caller, "graph.transition"]),
            data,
            created_at: self.now(),
            ..Default::default()
        };
        self.check_item(&it).map_err(invalidf)?;
        put_item(ctx, &mut tx, id, &it).await?;
        cur.state = t.to.clone();
        if cur.status == CHANGE_DRAFT {
            cur.status = CHANGE_ACTIVE;
        }
        tx.put_change(ctx, cur.clone()).await?;
        if reopened {
            self.reopen_phase(ctx, &mut tx, &cur, &t.to).await?;
        }
        tx.commit(ctx).await?;
        self.change(ctx, id).await
    }

    /// checkGate evaluates the guard of a transition: the CEL guard of the lifecycle over the change, its decision
    /// points (the ones that already gated a transition left out) and its world state, then its vetos and objectives
    /// (ADR 0075 §3). The result says which objectives the transition goes with reserve of.
    pub(crate) async fn check_gate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        t: &Transition,
        decision: &str,
        consumed: &HashSet<String>,
    ) -> Result<GateResult> {
        if t.guard.is_empty() && t.vetos.is_empty() && t.objectives.is_empty() {
            return Ok(GateResult::default());
        }
        let kept: Vec<DecisionPoint> = decision_points_of(bb)
            .into_iter()
            .filter(|d| !consumed.contains(&d.id))
            .collect();
        let bb = bb.with_facet(FACET_DECISION_POINTS, kept);
        let Some(lcs) = &self.lifecycles else {
            return Err(invalidf(format!(
                "guard of {} on change {}: no lifecycle resolver",
                t.name, bb.change.id
            )));
        };
        if !t.guard.is_empty() {
            let ok = lcs
                .guard(ctx, &bb, &t.guard, &t.name, decision)
                .await
                .map_err(|e| {
                    invalidf(format!(
                        "guard of {} on change {}: {e}",
                        t.name, bb.change.id
                    ))
                })?;
            if !ok {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "change {} cannot take {}: gate not satisfied ({})",
                        bb.change.id, t.name, t.guard
                    ),
                ));
            }
        }
        if t.vetos.is_empty() && t.objectives.is_empty() {
            return Ok(GateResult::default());
        }
        let res = lcs.gate(ctx, &bb, t, decision).await.map_err(|e| {
            invalidf(format!(
                "criteria of {} on change {}: {e}",
                t.name, bb.change.id
            ))
        })?;
        if !res.vetoed.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} cannot take {}: vetoed by {}",
                    bb.change.id,
                    t.name,
                    res.vetoed.join(", ")
                ),
            ));
        }
        let u = res.uncovered();
        if !u.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} cannot take {}: objectives not met and not covered by a derogation: {}",
                    bb.change.id,
                    t.name,
                    u.join(", ")
                ),
            ));
        }
        Ok(res)
    }

    /// phases is the state each change impact was last written in, by replaying the journal of the transitions
    /// against the impact log: the state before the first move is the one the first move leaves. A draft started
    /// (created, checked out, installed by an adoption) sets it; an edit or a transition of the draft does not (ADR
    /// 0079).
    pub(crate) async fn phases(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
    ) -> Result<HashMap<ChangeImpactID, Phase>> {
        let moves = c.state_moves();
        if moves.is_empty() {
            return Ok(HashMap::new());
        }
        let entries = tx
            .log(
                ctx,
                &LogFilter {
                    change: c.id.clone(),
                    types: vec![format!("{LOG_FACT}."), format!("{LOG_IMPACT}.")],
                    ..Default::default()
                },
            )
            .await?;
        let at: HashMap<ItemID, String> = moves
            .iter()
            .map(|m| (m.item.clone(), m.to.clone()))
            .collect();
        let mut state = moves[0].from.clone();
        let transition_type = format!("{LOG_FACT}.{TYPE_TRANSITION}");
        let mut out = HashMap::new();
        for l in &entries {
            if l.r#type == transition_type {
                let it: ChangeItem = serde_json::from_value(l.payload.clone())
                    .map_err(|e| Error::msg(e.to_string()))?;
                if let Some(to) = at.get(&it.id) {
                    state = to.clone();
                }
            } else if l.r#type.len() > LOG_IMPACT.len() && l.r#type.starts_with(LOG_IMPACT) {
                let ev: ImpactEvent = serde_json::from_value(l.payload.clone())
                    .map_err(|e| Error::msg(e.to_string()))?;
                if ev.starts_draft() && ev.flow.is_empty() {
                    out.insert(
                        ev.impact.clone(),
                        Phase {
                            state: state.clone(),
                            seq: l.seq,
                        },
                    );
                }
            }
        }
        Ok(out)
    }

    /// frozen reports whether the impact was written in a state other than the current one (and that state): leaving
    /// a state freezes its impacts (ADR 0058 §4), going back to it unfreezes them.
    pub(crate) async fn frozen(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        impact: &ChangeImpactID,
    ) -> Result<(bool, String)> {
        if c.state.is_empty() {
            return Ok((false, String::new()));
        }
        let ph = self.phases(ctx, tx, c).await?;
        Ok(match ph.get(impact) {
            Some(p) => (p.state != c.state, p.state.clone()),
            None => (false, String::new()),
        })
    }

    /// reopenPhase is made when a change goes back to a state it already occupied: what was written after it entered
    /// the state, in a later phase, must be reviewed again.
    pub(crate) async fn reopen_phase(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        to: &str,
    ) -> Result<()> {
        let ph = self.phases(ctx, tx, c).await?;
        // the last entry into the state, before the move just journaled (c does not hold it yet)
        let mut entered = String::new();
        for m in c.state_moves() {
            if m.to == to {
                entered = m.item.to_string();
            }
        }
        let mut since = 0i64;
        let entries = tx
            .log(
                ctx,
                &LogFilter {
                    change: c.id.clone(),
                    types: vec![format!("{LOG_FACT}.{TYPE_TRANSITION}")],
                    ..Default::default()
                },
            )
            .await?;
        for l in &entries {
            let it: ChangeItem =
                serde_json::from_value(l.payload.clone()).map_err(|e| Error::msg(e.to_string()))?;
            if it.id.as_str() == entered {
                since = l.seq;
            }
        }
        for cn in &c.nodes {
            let Some(p) = ph.get(&cn.id) else {
                continue;
            };
            if p.state == to || p.seq < since || cn.review != REVIEW_ACCEPTED {
                continue;
            }
            let r = Review {
                status: REVIEW_PROPOSED,
                by: self.caller_of(ctx),
                at: self.now(),
                comment: format!(
                    "written in {}, to be reviewed again: the change went back to {to}",
                    p.state
                ),
                ..Default::default()
            };
            self.emit(
                ctx,
                tx,
                vec![ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_REVIEWED,
                    review: Some(r),
                    ..Default::default()
                }],
            )
            .await?;
        }
        Ok(())
    }

    /// checkGuardians asks the outer guardians of the chain of a change (ADR 0106) about a transition, or about its
    /// merge when transition is "". A change whose chain is its own methodology only is not asked.
    pub(crate) async fn check_guardians(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        transition: &str,
    ) -> Result<()> {
        let c = &bb.change;
        let Some(lcs) = &self.lifecycles else {
            return Ok(());
        };
        if c.outer_guardians().is_empty() {
            return Ok(());
        }
        let mut ancestors = Vec::new();
        let mut seen: HashSet<ChangeID> = HashSet::new();
        let mut p = c.parent_id.clone();
        while !p.is_empty() && seen.insert(p.clone()) {
            let pc = self.change(ctx, &p).await?;
            p = pc.parent_id.clone();
            ancestors.push(pc);
        }
        let refusal = lcs
            .check_guardians(ctx, bb, transition, &ancestors)
            .await
            .map_err(|e| invalidf(format!("guardians of change {}: {e}", c.id)))?;
        if refusal.is_empty() {
            return Ok(());
        }
        let what = if transition.is_empty() {
            "merge".to_string()
        } else {
            format!("take {transition}")
        };
        Err(Error::wrap(
            ErrorKind::Conflict,
            format!("change {} cannot {what}: its guardian {refusal}", c.id),
        ))
    }
}
