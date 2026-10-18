//! Versions of a methodology (ADR 0113). A change follows the version of its methodology it was
//! created with (`Change::methodology_version`); a new version of the methodology serves the changes created after
//! it. A change moves to another version only when someone upgrades it.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ACTIVE, CHANGE_DRAFT, Change, ChangeID, GuardianRef, HeaderValue, Lifecycle,
    methodology_ref,
};
use goap_operations as operations;
use serde_json::Value;

use crate::attach::{HEADER_GUARDIANS, guardians_value};
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

/// HeaderMethodologyVersion is the field of a change.updated log entry recording an upgrade of the version of the
/// methodology of a change.
pub const HEADER_METHODOLOGY_VERSION: &str = "methodologyVersion";

impl Graph {
    /// UpgradeChange moves an open change to another version of its methodology (ADR 0113): the version named, else
    /// the latest published one. The version must be one the registry holds published (Invalid otherwise, and without
    /// a registry). The change keeps its state: the lifecycle of the new version must be the same lifecycle and know
    /// the state the change is in (Conflict otherwise). Its guardian chain names the new version of its own
    /// methodology. The upgrade is journaled in a change.updated entry (methodologyVersion {from, to}); no entry when
    /// the version is the one it follows already. Its sub-changes keep theirs, each is upgraded on its own; a run
    /// already started keeps the version it started with until it ends.
    ///
    /// goap:operation change.upgrade doer
    pub async fn upgrade_change(&self, ctx: &Ctx, id: &ChangeID, version: &str) -> Result<Change> {
        self.allow(ctx, id, operations::CHANGE_UPGRADE).await?;
        let c = self.change(ctx, id).await?;
        if c.methodology.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("change {id} follows no methodology"),
            ));
        }
        let Some(lcs) = &self.lifecycles else {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "no registry resolves the versions of methodology {}",
                    c.methodology
                ),
            ));
        };
        let target = lcs
            .methodology_version(ctx, &methodology_ref(&c.methodology, version))
            .await?;
        if target.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!("methodology {} is unknown", c.methodology),
            ));
        }
        if target == c.methodology_version {
            return Ok(c);
        }
        let lc = lcs
            .lifecycle(ctx, &methodology_ref(&c.methodology, &target))
            .await?;
        same_lifecycle(&c, lc.as_ref(), &target)?;
        let mut tx = self.change_tx(ctx, id).await?;
        let mut c = tx.change(ctx, id).await?;
        if c.status != CHANGE_DRAFT && c.status != CHANGE_ACTIVE {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change {} is {}: it no longer changes version",
                    c.id, c.status
                ),
            ));
        }
        if c.methodology_version == target {
            tx.commit(ctx).await?;
            return Ok(c);
        }
        let mut fields = BTreeMap::new();
        fields.insert(
            HEADER_METHODOLOGY_VERSION.to_string(),
            HeaderValue {
                from: Value::String(c.methodology_version.clone()),
                to: Value::String(target.clone()),
            },
        );
        let chain = upgraded_chain(&c, &target);
        if chain != c.guardians {
            fields.insert(
                HEADER_GUARDIANS.to_string(),
                HeaderValue {
                    from: guardians_value(&c.guardians),
                    to: guardians_value(&chain),
                },
            );
            c.guardians = chain;
        }
        self.header_entry(ctx, &mut tx, &c.id, fields).await?;
        c.methodology_version = target;
        tx.put_change(ctx, c.clone()).await?;
        tx.commit(ctx).await?;
        Ok(c)
    }
}

/// sameLifecycle checks that the lifecycle of the target version of the methodology of a change keeps the change where
/// it stands: the same lifecycle (or none for both), knowing its state.
fn same_lifecycle(c: &Change, lc: Option<&Lifecycle>, target: &str) -> Result<()> {
    let r = methodology_ref(&c.methodology, target);
    match lc {
        None if c.lifecycle.is_empty() => Ok(()),
        Some(lc) if lc.name == c.lifecycle => {
            if lc.state(&c.state).is_none() && !c.state.is_empty() {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "the lifecycle {} of {r} has no state {:?}, the state of change {}",
                        lc.name, c.state, c.id
                    ),
                ));
            }
            Ok(())
        }
        _ => Err(Error::wrap(
            ErrorKind::Conflict,
            format!(
                "{r} follows lifecycle {:?}, change {} follows {:?}",
                lc.map(|l| l.name.as_str()).unwrap_or(""),
                c.id,
                c.lifecycle
            ),
        )),
    }
}

/// upgradedChain is the guardian chain of a change naming the version target of its own methodology.
fn upgraded_chain(c: &Change, target: &str) -> Vec<GuardianRef> {
    let mut chain = c.guardians.clone();
    for g in &mut chain {
        if g.methodology == c.methodology {
            g.version = target.to_string();
        }
    }
    chain
}
