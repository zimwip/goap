//! Part of `transition.go` (G1): the guardian chain of a new change (ADR 0106). The lifecycle of a change
//! (TransitionChange, the gates, `checkGuardians`) belongs to a later slice (see PORTING.md); the
//! [`crate::ChangeLifecycles`] seam is in graph.rs.

use goap_ctx::Ctx;
use goap_domain::{Change, GuardianRef, methodology_ref};

use crate::api::NewChange;
use crate::{Graph, Result};

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
