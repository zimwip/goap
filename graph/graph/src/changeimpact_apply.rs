//! Port of `changeimpact_apply.go`: how the change impacts land (ADR 0024, 0079, 0102). The commit (`CommitChange`)
//! and the merge that call it are in apply.rs.

use std::collections::HashMap;

use goap_ctx::Ctx;
use goap_domain::{
    BaselineID, Branch, CHANGE_ABANDONED, CHANGE_APPLIED, Change, ChangeID, ChangeImpact, Draft,
    IMPACT_LANDED, IMPACT_REBASED, IMPACT_TRANSITIONED, ImpactEvent, Lifecycle, Link, Node, NodeID,
    REASON_CREATE, REASON_DERIVE, REASON_REVISE, REVIEW_ACCEPTED, Version, branch_of, namespace_of,
};

use crate::apply::{Applier, Cpost};
use crate::changelog::impact_events;
use crate::draft::own_draft;
use crate::lifecycle::invalidf;
use crate::merge::on_branch;
use crate::mergecheck::main_impacts_of;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Applier<'_> {
    /// prepareChangeImpacts writes, from the drafts of the change impacts, the versions the commit produces (ADR
    /// 0079): a node has no version until here. One rule for every impact of the main flow (ADR 0102): the accepted
    /// modifications are written; everything else stays in the change as it is. The versions are written on the branch
    /// of the change with their outgoing links, the links to a node of the change resolved to the version this commit
    /// writes for it; they are checked when written (walk_change_impacts), the transaction being rolled back when one
    /// fails.
    pub(crate) async fn prepare_change_impacts(
        &mut self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
    ) -> Result<()> {
        let rows = self.g.drafts(ctx, tx, &self.change.id).await?;
        let mut todo: Vec<(ChangeImpact, Draft)> = Vec::new();
        let impacts = self.main_impacts();
        for cn in &impacts {
            if cn.review != REVIEW_ACCEPTED {
                continue;
            }
            let d = own_draft(&rows, &cn.id, "");
            if !self.g.draft_modifies(ctx, tx, d.as_ref()).await? {
                continue;
            }
            self.g.check_origins(&impacts, cn, d.as_ref())?;
            if let Some(d) = d {
                todo.push((cn.clone(), d));
            }
        }
        // 1. the versions: the next version of each node, whatever the branch it was written on
        let mut written: HashMap<NodeID, Version> = HashMap::new();
        let mut from: HashMap<NodeID, Version> = HashMap::new(); // the version each draft was checked out from
        let mut nodes = Vec::with_capacity(todo.len());
        for (cn, d) in &todo {
            let n = self.write_version(ctx, tx, cn, d).await?;
            written.insert(n.id.clone(), n.version);
            if let Some(b) = &d.base {
                from.insert(n.id.clone(), b.version); // after an earlier commit of the change: the version it wrote
            } else if let Some(p) = &cn.pre {
                from.insert(n.id.clone(), p.version);
            }
            nodes.push(n);
        }
        // 2. their links, to the versions of the change
        for (i, (_, d)) in todo.iter().enumerate() {
            for dl in &d.links {
                let mut to = dl.to.clone();
                if let Some(v) = written.get(&to.id)
                    && (to.is_draft()
                        || Some(&to.version) == from.get(&to.id)
                        || (d.base.as_ref().is_some_and(|b| to.version == b.version)
                            && to.id == d.node))
                {
                    to.version = *v;
                }
                if to.is_draft() {
                    return Err(invalidf(format!(
                        "{} links to the draft of node {}, which the change does not land",
                        nodes[i].key, to.id
                    )));
                }
                tx.put_link(
                    ctx,
                    Link {
                        id: dl.id.clone(),
                        r#type: dl.r#type.clone(),
                        from: nodes[i].r#ref(),
                        to,
                        properties: dl.properties.clone(),
                        change_id: self.change.id.clone(),
                    },
                )
                .await?;
            }
        }
        // 3. what the change leaves
        for (i, (cn, _)) in todo.into_iter().enumerate() {
            let pre = match &cn.pre {
                Some(p) => Some(tx.node(ctx, p).await?),
                None => None,
            };
            let n = nodes[i].clone();
            self.target.insert(n.id.clone(), n.version);
            self.cposts.push(Cpost { cn, post: n, pre });
        }
        Ok(())
    }

    /// writeVersion writes the version of a draft on the branch of the change: the next version number of the node,
    /// the version the draft was checked out from as its parent, the origin of the change impact (its last review
    /// comment, else its rationale). The node changing on the change branch since the draft was checked out is a
    /// conflict (the branch moved by other means, such as the merge of a sub-change, ADR 0103).
    pub(crate) async fn write_version(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        cn: &ChangeImpact,
        d: &Draft,
    ) -> Result<Node> {
        let comment = cn
            .reviews
            .last()
            .map(|r| r.comment.clone())
            .unwrap_or_else(|| cn.rationale.clone());
        let mut n = Node {
            id: d.node.clone(),
            branch: self.branch.clone(),
            namespace: namespace_of(&self.change.namespace).to_string(),
            key: d.key.clone(),
            r#type: d.r#type.clone(),
            properties: d.properties.clone(),
            state: d.state.clone(),
            change_id: self.change.id.clone(),
            owner: d.owner.clone(),
            change_impact: cn.id.clone(),
            comment,
            execution: d.execution.clone(),
            origins: d.origins.clone(),
            created_at: self.g.now(),
            ..Default::default()
        };
        let Some(base) = &d.base else {
            // a key is one node's: another change may have created it meanwhile
            match tx.node_id_by_key(ctx, &n.namespace, &n.key).await {
                Ok(id) if id != n.id => {
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "{} ({}): the key is taken by a node another change created meanwhile: rename it or withdraw it",
                            d.key, d.r#type
                        ),
                    ));
                }
                Ok(_) => {}
                Err(e) if e.is_not_found() => {}
                Err(e) => return Err(e),
            }
            n.version = 1;
            n.reason = REASON_CREATE.into();
            tx.put_node(ctx, n.clone()).await?;
            return Ok(n);
        };
        let vs = tx.versions(ctx, &d.node).await?;
        n.version = vs.len() as Version + 1;
        n.parents = vec![base.version];
        n.reason = REASON_DERIVE.into();
        if let Some(m) = &d.merged_with
            && m.version != base.version
        {
            n.parents.push(m.version); // a rebase merged it with the target's version (ADR 0102)
        }
        if on_branch(ctx, tx, base, &self.branch).await? {
            n.reason = REASON_REVISE.into();
        }
        match tx.latest_on(ctx, &d.node, &self.branch).await {
            Ok(latest) if latest.version != base.version => {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "{} changed on the branch of change {} since it was checked out (v{}, checked out from v{})",
                        d.key, self.change.id, latest.version, base.version
                    ),
                ));
            }
            Ok(_) => {}
            Err(e) if e.is_not_found() => {}
            Err(e) => return Err(e),
        }
        tx.put_node(ctx, n.clone()).await?;
        Ok(n)
    }

    /// mainImpacts are the impacts of the main flow that count at landing.
    pub(crate) fn main_impacts(&self) -> Vec<ChangeImpact> {
        main_impacts_of(&self.change)
    }

    /// walkChangeImpacts validates the properties of each produced version and walks its transitions: it returns the
    /// nodes left in a state that cannot land (ADR 0078).
    pub(crate) async fn walk_change_impacts(
        &mut self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
    ) -> Result<Vec<String>> {
        let mut stuck = Vec::new();
        for cp in self.cposts.clone() {
            let n = &cp.post;
            if n.deleted {
                continue;
            }
            self.g.check_written(ctx, tx, &self.ix, n).await?;
            let Some(lc) = self.ix.lifecycle_of(&n.r#type) else {
                continue;
            };
            if n.state.is_empty() {
                continue;
            }
            if !lc.landable(&n.state) {
                stuck.push(format!("{} ({}) in {}", n.key, n.r#type, n.state));
            }
            self.walk_transitions(ctx, tx, &cp, &lc).await?;
        }
        Ok(stuck)
    }

    /// walkTransitions replays the moves recorded in the log of the change for the draft a version was written from
    /// (ADR 0079, the moves of ADR 0058 are all in the log): from the state the draft started in, each transitioned
    /// event of the main flow must leave the state the draft was in and be a move of the lifecycle, and the chain must
    /// end in the state of the version. Each move was authorized, guarded and acted when it was taken.
    pub(crate) async fn walk_transitions(
        &mut self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        cp: &Cpost,
        lc: &Lifecycle,
    ) -> Result<()> {
        if self.events.is_none() {
            self.events = Some(impact_events(ctx, tx, &self.change.id).await?);
        }
        let norm = |s: &str| {
            if s.is_empty() {
                lc.initial.clone()
            } else {
                s.to_string()
            }
        };
        let mut at = String::new();
        let mut tracked = false;
        for e in self.events.as_deref().unwrap_or_default() {
            if e.impact != cp.cn.id || !e.flow.is_empty() {
                continue;
            }
            if e.starts_draft() {
                at = norm(
                    e.draft
                        .as_ref()
                        .map(|d| d.state.as_str())
                        .unwrap_or_default(),
                );
                tracked = true;
            } else if e.op == IMPACT_TRANSITIONED
                && let Some(st) = e.patch.get("state").and_then(|v| v.as_object())
            {
                let from = st.get("from").and_then(|v| v.as_str()).unwrap_or_default();
                let to = st.get("to").and_then(|v| v.as_str()).unwrap_or_default();
                if tracked && norm(from) != at {
                    return Err(invalidf(format!(
                        "{} ({}) was moved from {from} while it was in {at}",
                        cp.post.key, cp.post.r#type
                    )));
                }
                if lc.move_to(&norm(from), to).is_none() {
                    return Err(invalidf(format!(
                        "{} ({}) cannot go from {from} to {to}",
                        cp.post.key, cp.post.r#type
                    )));
                }
                at = to.to_string();
                tracked = true;
            }
        }
        if tracked && norm(&cp.post.state) != at {
            return Err(invalidf(format!(
                "{} ({}) is in {}, the moves recorded for it end in {at}",
                cp.post.key, cp.post.r#type, cp.post.state
            )));
        }
        Ok(())
    }
}

/// joinSorted joins the strings sorted, with ", ".
pub(crate) fn join_sorted(s: &[String]) -> String {
    let mut s = s.to_vec();
    s.sort();
    s.join(", ")
}

impl Graph {
    /// land records, once the branch of a change is merged into its parent (the baseline result), the version each
    /// change impact landed as: the version it wrote, joined to the parent, or the merge version of merge_change; and
    /// moves the planned change impacts of the other open changes on the same nodes to the new head, to be re-checked.
    /// (Go's `land`; the name `land` is the test fixture's.)
    pub(crate) async fn land_impacts(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        own: &Branch,
        merge_change: &ChangeID,
        result: &BaselineID,
    ) -> Result<()> {
        let target = branch_of(&own.parent).to_string();
        for cn in &c.nodes {
            let Some(post) = &cn.post else {
                continue;
            };
            if (cn.review != REVIEW_ACCEPTED && cn.landed.is_none())
                || !cn.flow.is_empty()
                || cn.superseded
            {
                continue;
            }
            let mut post = post.clone();
            if post.is_draft()
                && let Some(l) = &cn.landed
            {
                post = l.clone(); // checked out again after its commit, left as it was (ADR 0102)
            }
            let head = tx.latest_on(ctx, &post.id, &target).await?;
            if &head.change_id != merge_change && head.r#ref() != post {
                continue; // left as is on the target: nothing of this change landed
            }
            let r = head.r#ref();
            let comment = cn
                .reviews
                .last()
                .map(|r| r.comment.clone())
                .unwrap_or_else(|| cn.rationale.clone());
            tx.set_node_origin(ctx, &r, &c.id, &cn.id, &comment).await?;
            self.emit(
                ctx,
                tx,
                vec![ImpactEvent {
                    change: c.id.clone(),
                    impact: cn.id.clone(),
                    op: IMPACT_LANDED,
                    landed: Some(r.clone()),
                    baseline: result.clone(),
                    branch: target.clone(),
                    ..Default::default()
                }],
            )
            .await?;
            let others = tx.node_change_impacts(ctx, &r.id).await?;
            for oid in others {
                if oid == c.id {
                    continue;
                }
                let oc = tx.change(ctx, &oid).await?;
                if oc.status == CHANGE_APPLIED
                    || oc.status == CHANGE_ABANDONED
                    || branch_of(&oc.branch) == branch_of(&c.branch)
                {
                    continue;
                }
                for o in &oc.nodes {
                    let stale = o.post.is_none()
                        && o.pre
                            .as_ref()
                            .is_some_and(|p| p.id == r.id && p.version < r.version);
                    if !stale {
                        continue;
                    }
                    // moved to the new head, to be re-checked
                    self.emit(
                        ctx,
                        tx,
                        vec![ImpactEvent {
                            change: oid.clone(),
                            impact: o.id.clone(),
                            op: IMPACT_REBASED,
                            pre: Some(r.clone()),
                            ..Default::default()
                        }],
                    )
                    .await?;
                }
            }
        }
        Ok(())
    }
}
