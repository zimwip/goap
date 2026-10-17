//! Port of `commit.go`: the producer-facing way of making a change of node edits (ADR 0024): the producer says which
//! nodes it creates or modifies and why; the change, its change impacts, the drafts, the reviews and the apply follow.
//! Services that project their own definitions into the graph (registry, seeds) use it.

use std::collections::{HashMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    CHANGE_ABANDONED, CHANGE_COMMITTED, Change, ChangeImpact, ChangeImpactID, INTENT_MODIFIED,
    ImpactVerdict, LinkID, NodeID, NodeRef, REVIEW_ACCEPTED, ReviewBatch,
};
use serde_json::{Map, Value};

use crate::api::{
    ChangePatch, Commit, CommitResult, LinkEdit, LinkWrite, NewChange, NodeCheckout, NodeCreate,
    NodeEdit, NodeTransition, NodeUpdate,
};
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// Commit runs the edits as one change, through the operations every change uses (ADR 0076): it opens the change
    /// on a branch of its own; a created node is an ImpactNodeCreate, a modified one an ImpactNodeCheckout with its
    /// ImpactNodeUpdate and link edits; the state edits are transitions (ImpactNodeTransition, on the draft), each
    /// impact that modifies its node is accepted by the producer and one that modifies nothing withdrawn (ADR 0102),
    /// and the change is applied. A commit that lands nothing (NothingToCommit) or fails leaves no change behind
    /// (purged); when the branch cannot be merged without conflict (another change moved a node meanwhile) the change
    /// is abandoned and Conflict returned: the producer reads again and rebuilds its edits. A created node of a
    /// structure or a User must name its required parent (check_required_parent, ADR 0040).
    pub async fn commit(&self, ctx: &Ctx, input: Commit) -> Result<CommitResult> {
        if input.edits.is_empty() {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                "a commit needs at least one edit",
            ));
        }
        for e in &input.edits {
            self.check_required_parent(e)?;
        }
        let c = self
            .create_change(
                ctx,
                NewChange {
                    namespace: input.namespace.clone(),
                    title: input.title.clone(),
                    intent: input.intent.clone(),
                    methodology: input.methodology.clone(),
                    baseline_id: input.baseline.clone(),
                    branch: input.branch.clone(),
                    data: input.data.clone(),
                    own_branch: true,
                    owner_org: input.owner_org.clone(),
                    project_id: input.project_id.clone(),
                    ..Default::default()
                },
            )
            .await?;
        match self.commit_edits(ctx, &c, &input).await {
            Ok(baseline) => Ok(CommitResult {
                change: c.id,
                baseline,
            }),
            Err(err) => {
                // a commit is all or nothing: a change that landed nothing leaves no trace (purged with its drafts and
                // its log, no key is held, ADR 0079), else it is abandoned
                if self.purge_change(ctx, &c.id).await.is_ok() {
                    return Err(err);
                }
                let patch = ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                };
                if let Err(aerr) = self.update_change(ctx, &c.id, &patch).await {
                    return Err(Error::new(err.kind(), format!("{err}\n{aerr}")));
                }
                Err(err)
            }
        }
    }

    /// commitEdits is the body of Commit, on the change it opened.
    async fn commit_edits(
        &self,
        ctx: &Ctx,
        c: &Change,
        input: &Commit,
    ) -> Result<goap_domain::Baseline> {
        let edits = &input.edits;
        let order = self.commit_order(ctx, edits).await?;
        let mut impacts: Vec<ChangeImpactID> = vec![ChangeImpactID::default(); edits.len()];
        let mut posts: Vec<Option<NodeRef>> = vec![None; edits.len()]; // the drafts
        let mut written: HashMap<String, usize> = HashMap::new(); // key of a created node → its edit
        // links to a node of the commit not written yet (a cycle): added once every node is
        let mut later: Vec<(usize, LinkEdit)> = Vec::new();
        let created: HashSet<&str> = edits
            .iter()
            .filter(|e| e.pre.is_none())
            .map(|e| e.key.as_str())
            .collect();
        let why = |e: &NodeEdit| {
            if e.rationale.is_empty() {
                input.title.clone()
            } else {
                e.rationale.clone()
            }
        };
        for &i in &order {
            let e = &edits[i];
            let mut links = Vec::new();
            for l in &e.links {
                let mut to = l.to.clone();
                if !l.to_key.is_empty() {
                    let r = if let Some(&j) = written.get(&l.to_key) {
                        posts[j].clone().unwrap_or_default()
                    } else if created.contains(l.to_key.as_str()) {
                        later.push((i, l.clone()));
                        continue;
                    } else {
                        // not a node of this commit: a node the graph already holds, whose version stays
                        match self
                            .node_by_key_on(ctx, &input.namespace, &input.branch, &l.to_key)
                            .await
                        {
                            Ok(n) => n.r#ref(),
                            Err(err) if err.is_not_found() => {
                                return Err(Error::wrap(
                                    ErrorKind::Invalid,
                                    format!(
                                        "link of {} to {:?}: no such node, neither stored nor created by this commit",
                                        node_name(e),
                                        l.to_key
                                    ),
                                ));
                            }
                            Err(err) => return Err(err),
                        }
                    };
                    to = Some(r);
                }
                let Some(to) = to else {
                    return Err(Error::wrap(
                        ErrorKind::Invalid,
                        format!("link of {} needs a target", node_name(e)),
                    ));
                };
                // a link is part of its source version: the one the version already carries (same attributes, not
                // removed by the edit) is not added again
                if let Some(pre) = &e.pre
                    && self
                        .links_to(ctx, pre, &l.r#type, &to.id, &l.props, &e.remove_links)
                        .await?
                {
                    continue;
                }
                links.push(LinkWrite {
                    r#type: l.r#type.clone(),
                    to,
                    properties: l.props.clone(),
                });
            }
            if let Some(pre) = &e.pre {
                // the impact names the version the edit starts from: a stale one conflicts (it is not in the reference
                // baseline)
                let added = self
                    .propose_impact(
                        ctx,
                        &c.id,
                        vec![ChangeImpact {
                            intent: INTENT_MODIFIED,
                            pre: Some(pre.clone()),
                            rationale: why(e),
                            produced_by: input.by.clone(),
                            ..Default::default()
                        }],
                    )
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                impacts[i] = added.into_iter().next().map(|cn| cn.id).unwrap_or_default();
            }
            if e.pre.is_none() {
                let cn = self
                    .impact_node_create(
                        ctx,
                        &c.id,
                        NodeCreate {
                            key: e.key.clone(),
                            r#type: e.r#type.clone(),
                            properties: e.props.clone(),
                            owner: e.owner.clone(),
                            rationale: why(e),
                            links,
                            produced_by: input.by.clone(),
                            ..Default::default()
                        },
                    )
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                impacts[i] = cn.id;
                posts[i] = cn.post;
                written.insert(e.key.clone(), i);
            } else if !e.props.is_empty()
                || !e.owner.is_empty()
                || !links.is_empty()
                || !e.remove_links.is_empty()
            {
                let cn = self
                    .impact_node_checkout(
                        ctx,
                        &c.id,
                        NodeCheckout {
                            impact: impacts[i].clone(),
                            ..Default::default()
                        },
                    )
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                posts[i] = cn.post.clone();
                if !e.props.is_empty() || !e.owner.is_empty() {
                    self.impact_node_update(
                        ctx,
                        &c.id,
                        &cn.id,
                        NodeUpdate {
                            properties: e.props.clone(),
                            owner: e.owner.clone(),
                            ..Default::default()
                        },
                    )
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                }
                for id in &e.remove_links {
                    // the draft holds a copy of the link of the version it was checked out from: ImpactLinkDelete
                    // finds it
                    self.impact_link_delete(ctx, &c.id, id, "", "")
                        .await
                        .map_err(|err| err.context(node_name(e)))?;
                }
                for l in links {
                    self.impact_link_create(ctx, &c.id, &cn.id, l, "", "")
                        .await
                        .map_err(|err| err.context(node_name(e)))?;
                }
            }
        }
        for (from, l) in later {
            let to = posts[written[&l.to_key]].clone().unwrap_or_default();
            self.impact_link_create(
                ctx,
                &c.id,
                &impacts[from],
                LinkWrite {
                    r#type: l.r#type.clone(),
                    to,
                    properties: l.props.clone(),
                },
                "",
                "",
            )
            .await
            .map_err(|err| {
                err.context(format!(
                    "link {} from {} to {}",
                    l.r#type,
                    node_name(&edits[from]),
                    l.to_key
                ))
            })?;
        }
        // The producer's edits go through the rules of every change (ADR 0102): a state edit is a move of the node's
        // draft (a node with none is checked out by the move); then each impact that modifies its node is accepted by
        // the producer (the rationale is the comment) and one that modifies nothing (an edit restating what the node
        // holds) is withdrawn. A commit left with nothing to land is NothingToCommit.
        for (i, e) in edits.iter().enumerate() {
            if e.state.is_empty() {
                continue;
            }
            let r = posts[i]
                .clone()
                .or_else(|| e.pre.clone())
                .unwrap_or_default();
            let n = self.change_node(ctx, &c.id, "", &r).await?;
            let mut cur = n.state.clone();
            if cur.is_empty()
                && e.pre.is_none()
                && let Some(lc) = self
                    .catalog()
                    .and_then(|cat| cat.lifecycle(&e.r#type).cloned())
            {
                cur = lc.initial.clone();
            }
            if cur == e.state {
                continue;
            }
            self.impact_node_transition(
                ctx,
                &c.id,
                NodeTransition {
                    checkout: NodeCheckout {
                        impact: impacts[i].clone(),
                        node: n.id.clone(),
                        rationale: why(e),
                        produced_by: input.by.clone(),
                        ..Default::default()
                    },
                    to: e.state.clone(),
                },
            )
            .await
            .map_err(|err| err.context(node_name(e)))?;
        }
        // the producer's acceptances are one review, submitted at once (ADR 0105)
        let mut accept = ReviewBatch {
            by: input.by.clone(),
            ..Default::default()
        };
        for (i, e) in edits.iter().enumerate() {
            if impacts[i].is_empty() {
                continue;
            }
            if !self.impact_modifies(ctx, &c.id, &impacts[i]).await? {
                self.withdraw_impact(ctx, &c.id, &impacts[i], "", "")
                    .await
                    .map_err(|err| err.context(node_name(e)))?;
                continue;
            }
            accept.verdicts.push(ImpactVerdict {
                impact: impacts[i].clone(),
                status: REVIEW_ACCEPTED,
                comment: why(e),
            });
        }
        if accept.verdicts.is_empty() {
            return Err(Error::wrap(
                ErrorKind::NothingToCommit,
                format!(
                    "commit {:?}: every edit restates what the graph holds",
                    input.title
                ),
            ));
        }
        self.impact_node_review_batch(ctx, &c.id, accept)
            .await
            .map_err(|err| err.context(format!("commit {:?}", input.title)))?;
        let baseline = self.apply(ctx, &c.id, &input.baseline_name).await?;
        let done = self.change(ctx, &c.id).await?;
        if done.status == CHANGE_COMMITTED {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "commit {:?} conflicts with a concurrent change",
                    input.title
                ),
            ));
        }
        Ok(baseline)
    }

    /// linksTo reports whether a version already has an outgoing link of the type to the node with these attributes,
    /// other than the ones the edit removes.
    async fn links_to(
        &self,
        ctx: &Ctx,
        from: &NodeRef,
        typ: &str,
        to: &NodeID,
        props: &Map<String, Value>,
        removed: &[LinkID],
    ) -> Result<bool> {
        let mut tx = self.begin(ctx).await?;
        let out = tx.out_links(ctx, from).await?;
        tx.commit(ctx).await?;
        Ok(out.iter().any(|l| {
            l.r#type == typ
                && l.to.id == *to
                && !removed.contains(&l.id)
                && same_link_props(&l.properties, props)
        }))
    }

    /// commitOrder orders the edits so that what a link points to is written before the node that links to it: an
    /// edit comes after the created node it links to by key, after the modified node it links to, and (for a modified
    /// node) after the modified nodes its current version links to. Cycles among modified nodes keep the given order;
    /// a cycle among created nodes is written in the given order, the links that close it added after the nodes.
    async fn commit_order(&self, ctx: &Ctx, edits: &[NodeEdit]) -> Result<Vec<usize>> {
        let mut by_key: HashMap<&str, usize> = HashMap::new();
        let mut by_node: HashMap<NodeID, usize> = HashMap::new();
        for (i, e) in edits.iter().enumerate() {
            match &e.pre {
                None => {
                    if by_key.insert(&e.key, i).is_some() {
                        return Err(Error::wrap(
                            ErrorKind::Invalid,
                            format!("node {:?} is created twice", e.key),
                        ));
                    }
                }
                Some(pre) => {
                    by_node.insert(pre.id.clone(), i);
                }
            }
        }
        let mut deps: Vec<Vec<usize>> = vec![Vec::new(); edits.len()];
        let mut tx = self.begin(ctx).await?;
        for (i, e) in edits.iter().enumerate() {
            for l in &e.links {
                if !l.to_key.is_empty() {
                    if let Some(&j) = by_key.get(l.to_key.as_str()) {
                        deps[i].push(j);
                    }
                } else if let Some(to) = &l.to
                    && let Some(&j) = by_node.get(&to.id)
                    && j != i
                {
                    deps[i].push(j);
                }
            }
            let Some(pre) = &e.pre else { continue };
            for l in tx.out_links(ctx, pre).await? {
                if let Some(&j) = by_node.get(&l.to.id)
                    && j != i
                {
                    deps[i].push(j);
                }
            }
        }
        tx.commit(ctx).await?;
        // 0 new, 1 visiting, 2 done; a cycle keeps the given order, the link that closes it is added after the nodes
        fn visit(i: usize, deps: &[Vec<usize>], state: &mut [u8], order: &mut Vec<usize>) {
            if state[i] != 0 {
                return;
            }
            state[i] = 1;
            for &d in &deps[i] {
                visit(d, deps, state, order);
            }
            state[i] = 2;
            order.push(i);
        }
        let mut order = Vec::with_capacity(edits.len());
        let mut state = vec![0u8; edits.len()];
        for i in 0..edits.len() {
            visit(i, &deps, &mut state, &mut order);
        }
        Ok(order)
    }

    /// checkRequiredParent enforces that a created node names the links its type requires: exactly one parent link
    /// for a node of a structure (ADR 0054: a unit of the organisation, a project), the links a node type declares
    /// through `requires` (ADR 0065: a User needs one member_of, ADR 0040), except the roots of the structures, created
    /// by the bootstrap. Only creation is checked here: an edit that modifies an existing node's membership (the move
    /// action) is responsible for its own atomicity (removing the old link and adding the new one in the same edit).
    pub(crate) fn check_required_parent(&self, e: &NodeEdit) -> Result<()> {
        if e.pre.is_some() {
            return Ok(());
        }
        for r in self.requirements_of(&e.r#type, &e.key) {
            let n = e.links.iter().filter(|l| l.r#type == r.link_type).count() as i64;
            if n != r.count {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("{} needs {}, got {n}", node_name(e), r.of),
                ));
            }
        }
        Ok(())
    }
}

/// sameLinkProps compares the attributes of two links, no attribute and an empty set alike.
fn same_link_props(a: &Map<String, Value>, b: &Map<String, Value>) -> bool {
    if a.is_empty() || b.is_empty() {
        return a.len() == b.len();
    }
    serde_json::to_string(a).ok() == serde_json::to_string(b).ok()
}

fn node_name(e: &NodeEdit) -> String {
    match &e.pre {
        Some(p) => p.to_string(),
        None => e.key.clone(),
    }
}
