//! The validation of a change impact (`ImpactValidation`) is derived, never stored and
//! no event: what the review gate of an acceptance (check_accepted) would say of the draft as it stands. The views of a
//! change carry it (ChangeChecked, BlackboardIn) so a person sees the failure on the impact and not only after
//! accepting it. An acceptance still runs the check itself: this is a preview, not a gate.
//!
//! A change's validations are computed per flow and kept by the position of the impact log they were computed at
//! (the same position that validates the draft cache): a view of an unchanged change runs no validator.

use std::collections::HashMap;

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_APPLIED, CHANGE_COMMITTED, Change, ChangeID, ChangeImpact,
    ChangeImpactID, FLOW_OPEN, ImpactValidation,
};

use crate::flownodes::FlowNodes;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// validationCacheSize bounds the (change, flow) entries kept.
pub(crate) const VALIDATION_CACHE_SIZE: usize = 256;

type Validations = HashMap<ChangeImpactID, ImpactValidation>;

#[derive(Default)]
pub(crate) struct ValidationCache {
    m: parking_lot::Mutex<HashMap<(ChangeID, String), (i64, Validations)>>,
}

impl ValidationCache {
    fn get(&self, k: &(ChangeID, String), seq: i64) -> Option<Validations> {
        let m = self.m.lock();
        match m.get(k) {
            Some((s, out)) if *s == seq => Some(out.clone()),
            _ => None,
        }
    }

    fn put(&self, k: (ChangeID, String), seq: i64, out: Validations) {
        let mut m = self.m.lock();
        if !m.contains_key(&k) && m.len() >= VALIDATION_CACHE_SIZE {
            // arbitrary eviction: an entry is cheap to recompute
            if let Some(old) = m.keys().next().cloned() {
                m.remove(&old);
            }
        }
        m.insert(k, (seq, out));
    }
}

/// validationMessage is the readable text of a refusal: the error without the sentinel the graph wraps it with.
pub(crate) fn validation_message(err: &Error) -> String {
    let mut msg = err.to_string();
    for k in [ErrorKind::Invalid, ErrorKind::Conflict] {
        if let Some(s) = msg.strip_suffix(&format!(": {}", k.sentinel())) {
            msg = s.to_string();
        }
    }
    msg
}

impl Graph {
    /// impactValidations checks the drafts the flow sees, by change impact. A refusal of the gate (invalid, and the
    /// origins gate, conflict) is a result; any other error is no verdict: the impact is left out and nothing is
    /// cached.
    pub(crate) async fn impact_validations(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
    ) -> Result<Validations> {
        if c.status == CHANGE_APPLIED
            || c.status == CHANGE_ABANDONED
            || c.status == CHANGE_COMMITTED
        {
            return Ok(HashMap::new());
        }
        if !flow.is_empty() && (c.flow(flow).is_none() || c.flow_status_of(flow) != FLOW_OPEN) {
            return Ok(HashMap::new());
        }
        let st = self.draft_state(ctx, tx, &c.id).await?;
        let key = (c.id.clone(), flow.to_string());
        if let Some(out) = self.validations.get(&key, st.seq) {
            return Ok(out);
        }
        let seen = FlowNodes::new(c.clone(), flow).nodes(ctx, tx).await?;
        let drafts = self.seen_drafts(ctx, tx, c, flow).await?;
        let ix = self.types_at();
        let mut out = HashMap::new();
        let mut complete = true;
        for cn in &seen {
            let Some(d) = drafts.get(&cn.id) else {
                continue;
            };
            if cn.superseded || !cn.drafted() {
                continue;
            }
            let modified = match self.draft_modifies(ctx, tx, Some(d)).await {
                Ok(m) => m,
                Err(_) => {
                    complete = false;
                    continue;
                }
            };
            match self
                .check_draft_of(ctx, tx, c, flow, &ix, &seen, cn, d)
                .await
            {
                Ok(()) => {
                    out.insert(
                        cn.id.clone(),
                        ImpactValidation {
                            valid: true,
                            modified,
                            ..Default::default()
                        },
                    );
                }
                Err(e) if e.is_invalid() || e.is_conflict() => {
                    out.insert(
                        cn.id.clone(),
                        ImpactValidation {
                            message: validation_message(&e),
                            modified,
                            ..Default::default()
                        },
                    );
                }
                Err(_) => complete = false,
            }
        }
        if complete {
            self.validations.put(key, st.seq, out.clone());
        }
        Ok(out)
    }

    /// attachValidation sets the validation on the impacts of the list that the flow validates; pick, when set,
    /// narrows the impacts to look at. It never fails: a view without a validation is still a view.
    pub(crate) async fn attach_validation(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        impacts: &mut [ChangeImpact],
        pick: Option<&(dyn Fn(&ChangeImpact) -> bool + Send + Sync)>,
    ) {
        let Ok(vs) = self.impact_validations(ctx, tx, c, flow).await else {
            return;
        };
        for cn in impacts.iter_mut() {
            if let Some(p) = pick
                && !p(cn)
            {
                continue;
            }
            cn.validation = vs.get(&cn.id).cloned();
        }
    }
}
