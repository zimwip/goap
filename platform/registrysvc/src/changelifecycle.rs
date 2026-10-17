//! What the graph asks the registry about the lifecycle of a change (ADR 0058), its
//! default goal (ADR 0096), the guards and gates of its transitions and the roles of the operations (ADR 0103).

use goap_ctx::Ctx;
use goap_def::Record as DomainRecord;
use goap_domain::{Blackboard, GateResult, Lifecycle, Transition};
use goap_methodology::Methodology;

use crate::domains::builtin_records;
use crate::error::{Error, Result};
use crate::service::Service;
use crate::store::STATUS_PUBLISHED;

impl Service {
    /// Lifecycle implements graph.ChangeLifecycles (ADR 0058): the lifecycle the changes of a methodology follow, the
    /// one it names in the domain of its namespace (else in any domain, a built-in one for instance). A methodology
    /// the registry does not know, or one naming none, gives None: its changes have no state.
    pub async fn lifecycle(&self, ctx: &Ctx, methodology: &str) -> Result<Option<Lifecycle>> {
        let r = match self.record(ctx, methodology).await {
            Err(e) if e.is_not_found() => return Ok(None),
            Err(e) => return Err(e),
            Ok(r) => r,
        };
        let m = r.methodology;
        if m.lifecycle.is_empty() {
            return Ok(None);
        }
        // the lifecycle of the domain versions the methodology version is written against (ADR 0113)
        let ds = self.domains_for(ctx, &m, r.published_at).await?;
        let mut recs = builtin_records();
        recs.extend(ds.into_iter().map(|d| DomainRecord {
            domain: d,
            status: STATUS_PUBLISHED,
            ..DomainRecord::default()
        }));
        match lifecycle_in(&recs, &m) {
            Some(lc) => Ok(Some(lc)),
            None => Err(Error::invalid_lifecycle(&m)),
        }
    }

    /// DefaultGoal implements graph.ChangeLifecycles (ADR 0096): the main goal of a methodology, the goal its changes
    /// start with; "" for a methodology the registry does not know. It reads the definition, a methodology that does
    /// not compile still gives its goal.
    pub async fn default_goal(&self, ctx: &Ctx, name: &str) -> Result<String> {
        match self.record(ctx, name).await {
            Err(e) if e.is_not_found() => Ok(String::new()),
            Err(e) => Err(e),
            Ok(r) => Ok(r.methodology.main_goal()),
        }
    }

    /// Guard implements graph.ChangeLifecycles: the guard of a transition of the lifecycle of a change, in the
    /// environment of the conditions of its methodology, whose values are the world state the guard reads.
    pub async fn guard(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        expr: &str,
        transition: &str,
        decision: &str,
    ) -> Result<bool> {
        let c = self.methodology(ctx, &bb.change.methodology_ref()).await?;
        let world = c.conditions.evaluate(bb).state;
        Ok(goap_condition::check_guard(
            expr,
            bb,
            Some(&world),
            transition,
            decision,
        )?)
    }

    /// Gate implements graph.ChangeLifecycles: the vetos and objectives of a transition (ADR 0075 §3), in the same
    /// environment as its guard.
    pub async fn gate(
        &self,
        ctx: &Ctx,
        bb: &Blackboard,
        t: &Transition,
        decision: &str,
    ) -> Result<GateResult> {
        let c = self.methodology(ctx, &bb.change.methodology_ref()).await?;
        let world = c.conditions.evaluate(bb).state;
        Ok(goap_condition::check_gate(t, bb, &world, decision)?)
    }

    /// OperationRoles returns the roles of a methodology that allow an operation of the catalogue (goap-operations,
    /// ADR 0103), from its stored definition. restricted is false when the methodology lists the operations of none
    /// of its roles, or the registry does not know it: the operation is then allowed to anyone the other rules let in.
    pub async fn operation_roles(
        &self,
        ctx: &Ctx,
        methodology: &str,
        op: &str,
    ) -> Result<(Vec<String>, bool)> {
        if methodology.is_empty() {
            return Ok((Vec::new(), false));
        }
        match self.record(ctx, methodology).await {
            Err(e) if e.is_not_found() => Ok((Vec::new(), false)),
            Err(e) => Err(e),
            Ok(r) => Ok(r.methodology.operation_roles(op)),
        }
    }
}

impl Error {
    fn invalid_lifecycle(m: &Methodology) -> Error {
        Error::invalid(format!(
            "methodology {} names lifecycle {:?}, which no domain defines",
            m.name, m.lifecycle
        ))
    }
}

/// lifecycleIn is the lifecycle m names among the domains: the one of its namespace, else of any domain (None:
/// none).
pub(crate) fn lifecycle_in(recs: &[DomainRecord], m: &Methodology) -> Option<Lifecycle> {
    if m.lifecycle.is_empty() {
        return None;
    }
    let mut found: Option<Lifecycle> = None;
    for d in recs {
        if let Some(lc) = d.domain.lifecycle(&m.lifecycle)
            && (found.is_none() || d.domain.name == m.namespace)
        {
            found = Some(lc.clone());
        }
    }
    found
}
