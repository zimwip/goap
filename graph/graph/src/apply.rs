//! Part of `apply.go` (G1): moving the head of a branch and numbering versions. The commit and the merge (`Apply`,
//! `CommitChange`, `IntegrateChange`, the applier, `writeVersion`) belong to a later slice (see PORTING.md).

use goap_ctx::Ctx;
use goap_domain::{BRANCH_OPEN, Baseline, Branch, MAIN_BRANCH, NodeID, Version, namespace_of};

use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// advanceBranch moves the head of a branch to a baseline built on it (main is created on first use). The head
    /// moves only from the baseline the new one follows (ADR 0127): read under the lock of the branch, a head another
    /// transaction moved meanwhile is a conflict, never a head silently replaced.
    pub(crate) async fn advance_branch(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        namespace: &str,
        name: &str,
        head: &Baseline,
    ) -> Result<()> {
        let namespace = namespace_of(namespace);
        tx.lock_branch(ctx, namespace, name).await?;
        let mut b = match tx.branch(ctx, namespace, name).await {
            Err(e) if e.is_not_found() && name == MAIN_BRANCH => Branch {
                name: name.to_string(),
                namespace: namespace.to_string(),
                status: BRANCH_OPEN.to_string(),
                created_at: self.now(),
                ..Default::default()
            },
            r => r?,
        };
        if b.head != head.parent_id {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "branch {name} moved to {} while baseline {} was built on {}: merge again",
                    b.head, head.id, head.parent_id
                ),
            ));
        }
        b.head = head.id.clone();
        tx.put_branch(ctx, b).await
    }
}

/// nextVersion returns the next version number of a node (numbered across branches).
pub(crate) async fn next_version(ctx: &Ctx, tx: &mut dyn Tx, id: &NodeID) -> Result<Version> {
    let vs = tx.versions(ctx, id).await?;
    Ok(vs.len() as Version + 1)
}

/// cpost is a version an accepted change impact produced (ADR 0024, Go's `changeimpact_apply.go`): the impact, its
/// post version and the version it was checked out from (None for a creation), what the node validators are given
/// (validator.go).
#[derive(Debug, Clone, Default)]
pub(crate) struct Cpost {
    pub(crate) cn: goap_domain::ChangeImpact,
    pub(crate) post: goap_domain::Node,
    pub(crate) pre: Option<goap_domain::Node>,
}

/// errCollected (Go): a pass that only reads what an authorization or a hook needs before the transaction that writes
/// is rolled back explicitly (`GraphTx::rollback`), the stores being not reentrant.
///
/// pendingMove is a lifecycle transition, from the state of node.
#[derive(Debug, Clone)]
pub(crate) struct PendingMove {
    pub(crate) node: goap_domain::Node,
    pub(crate) t: goap_domain::Transition,
}

impl PendingMove {
    pub(crate) fn key(&self) -> String {
        format!(
            "{}@{}:{}>{}",
            self.node.id, self.node.version, self.t.from, self.t.to
        )
    }
}

/// applier is what applying a change (or checking a move of one of its drafts) works with (Go's `applier`): the
/// transaction and the context are passed to each method rather than held.
pub(crate) struct Applier<'g> {
    pub(crate) g: &'g Graph,
    pub(crate) change: goap_domain::Change,
    pub(crate) ix: crate::lifecycle::TypeIndex,
    pub(crate) branch: String,
    pub(crate) target: std::collections::BTreeMap<NodeID, Version>,
    /// cposts are the versions produced by the accepted change impacts (ADR 0024).
    pub(crate) cposts: Vec<Cpost>,
    /// impact is the change impact of the node a transition moves (ImpactNodeTransition): its guard sees it (ADR
    /// 0076), draft the draft of the node in the state it goes to and drafts the drafts the flow sees (ADR 0079).
    pub(crate) impact: Option<goap_domain::ChangeImpact>,
    pub(crate) draft: Option<goap_domain::Draft>,
    pub(crate) drafts: Option<crate::draft::DraftReader>,
    /// events caches the impact log of the change for the walk of the transitions
    pub(crate) events: Option<Vec<goap_domain::ImpactEvent>>,
}

impl<'g> Applier<'g> {
    pub(crate) fn new(g: &'g Graph) -> Applier<'g> {
        Applier {
            g,
            change: Default::default(),
            ix: Default::default(),
            branch: String::new(),
            target: Default::default(),
            cposts: Vec::new(),
            impact: None,
            draft: None,
            drafts: None,
            events: None,
        }
    }

    pub(crate) fn env(&self) -> crate::algorithms::TransitionEnv<'_> {
        crate::algorithms::TransitionEnv {
            change: &self.change,
            impact: self.impact.as_ref(),
            ix: &self.ix,
        }
    }

    /// runGuards runs the guard algorithms of a transition: all must accept.
    pub(crate) async fn run_guards(
        &self,
        ctx: &Ctx,
        n: &goap_domain::Node,
        t: &goap_domain::Transition,
        children: &[goap_domain::Node],
    ) -> Result<()> {
        crate::algorithms::run_guards(ctx, &self.env(), n, t, children).await
    }

    /// runActions runs the action algorithms of a transition (see algorithms::run_actions).
    pub(crate) async fn run_actions(
        &self,
        ctx: &Ctx,
        n: &goap_domain::Node,
        t: &goap_domain::Transition,
        children: &[goap_domain::Node],
    ) -> Result<(serde_json::Map<String, serde_json::Value>, Vec<String>)> {
        crate::algorithms::run_actions(ctx, self.g, &self.env(), n, t, children).await
    }
}
