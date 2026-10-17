//! Port of `apply.go`: a change lands in two acts (ADR 0056, 0102), the same for every change and every caller:
//!
//!   - CommitChange writes the modifications the change accepted on its own branch, as versions (ADR 0079), and
//!     reverts there the nodes it no longer changes (an impact withdrawn after its commit). It checks the drafts as
//!     versions (their attributes, validators, required links, the moves of their lifecycle) and nothing else: neither
//!     the methodology nor the target branch. The change stays open; it is edited and committed again as often as
//!     needed. A commit that writes and reverts nothing is refused (NothingToCommit). A sub-change is a change like any
//!     other, whose branch is forked from the branch of its parent change (ADR 0103).
//!   - IntegrateChange (the merge) takes the work into what the change was forked from, once everything in it is
//!     settled: every impact committed, no open flow, decision point or sub-change. It first rebases its branch onto
//!     the head of its target (rebase.rs), asks the guardian of the change (mergecheck.rs), then fast-forwards; the
//!     change is applied, closed, and a sub-change's parent holds its work as its own (record_in_parent). Only a root
//!     change's target moving in between falls back to the three-way merge, which leaves the change committed until
//!     it is resolved.
//!
//! Apply is both: it commits what is pending, then merges.

use std::collections::BTreeMap;

use goap_ctx::Ctx;
use goap_domain::{
    BASELINE_COMMIT, BASELINE_REVERT, BRANCH_OPEN, Baseline, BaselineID, Branch, CHANGE_ABANDONED,
    CHANGE_ACTIVE, CHANGE_APPLIED, CHANGE_COMMITTED, CHANGE_DRAFT, ChangeID, IMPACT_LANDED,
    ImpactEvent, MAIN_BRANCH, NodeID, Version, branch_of, namespace_of,
};
use goap_operations as operations;

use crate::changebranch::own_branch;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// Apply commits what the change has pending, then merges it (IntegrateChange). It returns the baseline the change
    /// left on the branch it was forked from, or its last commit when the merge waits for a resolution.
    pub async fn apply(&self, ctx: &Ctx, id: &ChangeID, baseline_name: &str) -> Result<Baseline> {
        match Box::pin(self.commit_change(ctx, id, baseline_name)).await {
            Err(e) if !e.is_nothing_to_commit() => return Err(e),
            _ => {}
        }
        let c = Box::pin(self.integrate_change(ctx, id, &BTreeMap::new())).await?;
        if c.result_baseline_id.is_empty() {
            return Ok(Baseline::default());
        }
        let mut tx = self.begin(ctx).await?;
        let result = tx.baseline(ctx, &c.result_baseline_id).await?;
        // a name other than the title the change already carries is a tag on the state it leaves (ADR 0056)
        if c.status == CHANGE_APPLIED && !baseline_name.is_empty() && baseline_name != c.title {
            self.tag_tx(ctx, &mut tx, &c, baseline_name, "").await?;
        }
        tx.commit(ctx).await?;
        Ok(result)
    }

    /// CommitChange writes the accepted modifications of a change on its own branch and reverts there the nodes it no
    /// longer changes; it returns the commit baseline. The change stays open (ADR 0102).
    ///
    /// goap:operation change.commit doer
    pub async fn commit_change(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        baseline_name: &str,
    ) -> Result<Baseline> {
        self.allow(ctx, id, operations::CHANGE_COMMIT).await?;
        let mut tx = self.change_tx(ctx, id).await?;
        let c = tx.change(ctx, id).await?;
        // the change is its branch: one that wrote nothing has none yet
        if own_branch(ctx, &mut tx, &c).await?.is_none() {
            self.ensure_own_branch(ctx, &mut tx, c).await?;
        }
        let result = Box::pin(self.commit_on_branch(ctx, &mut tx, id, baseline_name)).await?;
        tx.commit(ctx).await?;
        Ok(result)
    }

    /// newApplier prepares what applying a change starts from: its reference baseline, or the head of its own branch.
    pub(crate) async fn new_applier(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<(Applier<'_>, BaselineID)> {
        let c = tx.change(ctx, id).await?;
        if c.status == CHANGE_APPLIED
            || c.status == CHANGE_ABANDONED
            || c.status == CHANGE_COMMITTED
        {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change {id} is {}", c.status),
            ));
        }
        let base = tx.baseline(ctx, &c.baseline_id).await?;
        let (mut target, mut parent_baseline) = (base.nodes, base.id);
        // A change on its own branch starts from the head of that branch (moved by its own commits and by the merges
        // of its sub-changes, ADR 0102, 0103).
        if own_branch(ctx, tx, &c).await?.is_some() {
            let head = branch_head(ctx, tx, &c.namespace, &c.branch).await?;
            (target, parent_baseline) = (head.nodes, head.id);
        }
        let mut a = Applier::new(self);
        a.ix = self.types_at();
        a.branch = branch_of(&c.branch).to_string();
        a.target = target;
        a.change = c;
        Ok((a, parent_baseline))
    }

    /// commitOnBranch writes the accepted modifications of a change on its own branch and reverts the nodes it no
    /// longer changes, then records its commit baseline there.
    pub(crate) async fn commit_on_branch(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        baseline_name: &str,
    ) -> Result<Baseline> {
        let (mut a, parent_baseline) = self.new_applier(ctx, tx, id).await?;
        let mut c = a.change.clone();
        a.prepare_change_impacts(ctx, tx).await?;
        let reverted = a.revert_withdrawn(ctx, tx).await?;
        if a.cposts.is_empty() && reverted == 0 {
            return Err(Error::wrap(
                ErrorKind::NothingToCommit,
                format!(
                    "change {id}: no accepted change impact modifies a node, and nothing it committed is withdrawn"
                ),
            ));
        }
        a.walk_change_impacts(ctx, tx).await?;
        self.run_node_validators(ctx, tx, &a.target, &a.cposts)
            .await?;
        let name = if baseline_name.is_empty() {
            c.title.clone()
        } else {
            baseline_name.to_string()
        };
        let result = Baseline {
            id: BaselineID::new(self.new_id()),
            name,
            namespace: c.namespace.clone(),
            branch: a.branch.clone(),
            parent_id: parent_baseline,
            change_id: c.id.clone(),
            // a revert is no landed event: the state is stored whole
            kind: if reverted > 0 {
                BASELINE_REVERT
            } else {
                BASELINE_COMMIT
            }
            .into(),
            nodes: std::mem::take(&mut a.target),
            created_at: self.now(),
            ..Default::default()
        };
        tx.put_baseline(ctx, result.clone()).await?;
        let branch = a.branch.clone();
        self.advance_branch(ctx, tx, &c.namespace, &branch, &result)
            .await?;
        // each version the change applied has landed on its own branch; it lands again on the branch it is integrated
        // into (integrate, land_impacts)
        let cposts = std::mem::take(&mut a.cposts);
        drop(a);
        for cp in cposts {
            self.emit(
                ctx,
                tx,
                vec![ImpactEvent {
                    change: c.id.clone(),
                    impact: cp.cn.id.clone(),
                    op: IMPACT_LANDED,
                    landed: Some(cp.post.r#ref()),
                    baseline: result.id.clone(),
                    branch: branch_of(&branch).to_string(),
                    ..Default::default()
                }],
            )
            .await?;
        }
        // the change stays open (ADR 0102): it is edited and committed again until it is merged
        if c.status == CHANGE_DRAFT {
            c.status = CHANGE_ACTIVE;
        }
        c.result_baseline_id = result.id.clone();
        tx.put_change(ctx, c).await?;
        Ok(result)
    }
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

    /// revertWithdrawn puts back, in what the commit leaves, the version the branch was forked from for every node the
    /// branch changed that no change impact holds any more (withdrawn after its commit, ADR 0102); it returns how
    /// many.
    pub(crate) async fn revert_withdrawn(&mut self, ctx: &Ctx, tx: &mut dyn Tx) -> Result<usize> {
        let (ids, fork) = self.g.withdrawn_since(ctx, tx, &self.change).await?;
        for id in &ids {
            match fork.get(id) {
                Some(v) => {
                    self.target.insert(id.clone(), *v);
                }
                None => {
                    self.target.remove(id);
                }
            }
        }
        Ok(ids.len())
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
