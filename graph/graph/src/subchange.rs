//! The rules of a new sub-change, the listing and abandonment of sub-changes, the split of a
//! change by owner (ADR 0103), and the checks of a version (ADR 0054, 0065, 0076, 0079). `checkRequiredParent` is in
//! commit.rs, with its only caller.

use goap_ctx::Ctx;
use goap_domain::{
    BRANCH_ABANDONED, CHANGE_ABANDONED, CHANGE_APPLIED, CHANGE_COMMITTED, Change, ChangeID, Draft,
    Link, Node, NodeRef, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT, namespace_of,
};

use crate::algorithms::NodeResolver;
use crate::api::NewChange;
use crate::changebranch::own_branch;
use crate::draft::{DraftReader, draft_node};
use crate::lifecycle::TypeIndex;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Error, ErrorKind, Graph, Result};

impl Graph {
    /// prepareSubChange applies the rules of a sub-change to c: its parent must be open and is given a branch of its
    /// own when it has none, the namespace is the parent's, and the change is forked from the head of the parent's
    /// branch, the branch it merges into (ADR 0103). Its organisational unit must be inside the unit of the parent.
    pub(crate) async fn prepare_sub_change(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &mut Change,
        input: &mut NewChange,
    ) -> Result<()> {
        if c.parent_id.is_empty() {
            return Ok(());
        }
        let mut parent = tx.change(ctx, &c.parent_id).await?;
        if parent.status == CHANGE_APPLIED
            || parent.status == CHANGE_ABANDONED
            || parent.status == CHANGE_COMMITTED
        {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("parent change {} is {}", parent.id, parent.status),
            ));
        }
        let own = match own_branch(ctx, tx, &parent).await? {
            Some(b) => b,
            None => {
                // the branch of the parent is where its sub-changes are forked from and merged into
                let (p, own) = self.ensure_own_branch(ctx, tx, parent).await?;
                parent = p;
                own
            }
        };
        if !input.namespace.is_empty()
            && namespace_of(&input.namespace) != namespace_of(&parent.namespace)
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "a sub-change acts on the namespace of its parent ({})",
                    namespace_of(&parent.namespace)
                ),
            ));
        }
        c.namespace = namespace_of(&parent.namespace).to_string();
        c.branch = own.name.clone();
        input.own_branch = true;
        if c.baseline_id.is_empty() {
            let head = branch_head(ctx, tx, &c.namespace, &own.name).await?;
            c.baseline_id = head.id;
        }
        if c.methodology.is_empty() {
            c.methodology = parent.methodology.clone();
        }
        // a sub-change works towards its parent's goal unless it names one of its own, or a methodology other than
        // the parent's (whose default goal CreateChange has set, ADR 0096)
        if input.goal.is_empty()
            && (input.methodology.is_empty() || input.methodology == parent.methodology)
        {
            c.goal = parent.goal.clone();
        }
        if c.owner_org.is_empty() {
            c.owner_org = parent.owner_org.clone();
        } else if c.owner_org != parent.owner_org
            && !self
                .within(
                    ctx,
                    tx,
                    STRUCTURE_ORGANISATION,
                    &c.owner_org,
                    &parent.owner_org,
                )
                .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "unit {} is not part of {}, the unit of the parent change",
                    c.owner_org, parent.owner_org
                ),
            ));
        }
        if c.project_id.is_empty() {
            c.project_id = parent.project_id.clone();
        } else if c.project_id != parent.project_id
            && !self
                .within(
                    ctx,
                    tx,
                    STRUCTURE_PROJECT,
                    &c.project_id,
                    &parent.project_id,
                )
                .await?
        {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                format!(
                    "project {} is not part of {}, the project of the parent change",
                    c.project_id, parent.project_id
                ),
            ));
        }
        Ok(())
    }

    /// SubChanges lists the sub-changes of a change, oldest first.
    pub async fn sub_changes(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Change>> {
        let mut tx = self.begin(ctx).await?;
        let out = sub_changes(ctx, &mut tx, id).await?;
        tx.commit(ctx).await?;
        Ok(out)
    }

    /// SplitByOwner splits a change along organisational boundaries: one sub-change per unit owning nodes the change
    /// has a candidate impact on (the owner of the pre version of a modified change impact, ADR 0024, 0054). The
    /// candidate moves to the sub-change (a copy derived from the parent's, withdrawn from the parent: ADR 0103). Units
    /// that already have an open sub-change are left as they are. Impacts on nodes the unit holding the change owns, or
    /// a unit outside it, stay with the parent.
    pub async fn split_by_owner(&self, ctx: &Ctx, id: &ChangeID) -> Result<Vec<Change>> {
        let mut tx = self.begin(ctx).await?;
        let parent = tx.change(ctx, id).await?;
        let existing = open_sub_changes(ctx, &mut tx, id).await?;
        let have: std::collections::HashSet<String> =
            existing.iter().map(|sc| sc.owner_org.clone()).collect();
        struct Group {
            org: String,
            name: String,
            nodes: Vec<goap_domain::ChangeImpact>,
        }
        let mut groups: Vec<Group> = Vec::new();
        // the change impacts of the main flow: stored and derived from items
        let seen = crate::flownodes::FlowNodes::new(parent.clone(), "")
            .nodes(ctx, &mut tx)
            .await?;
        for cn in &parent.nodes {
            if !seen.iter().any(|x| x.id == cn.id) && cn.items.is_empty() {
                continue; // a candidate of a flow, or replaced by one
            }
            let Some(pre) = &cn.pre else { continue };
            if cn.intent != goap_domain::INTENT_MODIFIED || cn.post.is_some() || cn.superseded {
                continue; // only a candidate moves: a node the change works on already stays with it
            }
            let pre = tx.node(ctx, pre).await?;
            let unit = tx
                .node(
                    ctx,
                    &NodeRef {
                        id: pre.owner.clone(),
                        version: 0,
                    },
                )
                .await?;
            if unit.key == parent.owner_org {
                continue; // the unit holding the change owns it
            }
            if !self
                .within(
                    ctx,
                    &mut tx,
                    STRUCTURE_ORGANISATION,
                    &unit.key,
                    &parent.owner_org,
                )
                .await?
            {
                continue; // owned outside the unit holding the change: nothing to delegate to
            }
            match groups.iter_mut().find(|g| g.org == unit.key) {
                Some(g) => g.nodes.push(cn.clone()),
                None => {
                    let name = unit
                        .properties
                        .get("name")
                        .and_then(|v| v.as_str())
                        .unwrap_or("");
                    groups.push(Group {
                        org: unit.key.clone(),
                        name: crate::flow::first_non_empty(&[name, &unit.key]),
                        nodes: vec![cn.clone()],
                    });
                }
            }
        }
        let mut created = Vec::new();
        for gr in groups {
            if have.contains(&gr.org) {
                continue;
            }
            let mut input = NewChange {
                title: format!("{} / {}", parent.title, gr.name),
                intent: parent.intent.clone(),
                methodology: parent.methodology.clone(),
                parent_id: id.clone(),
                owner_org: gr.org.clone(),
                ..Default::default()
            };
            let mut sub = Change {
                id: ChangeID::new(self.new_id()),
                title: input.title.clone(),
                intent: input.intent.clone(),
                methodology: input.methodology.clone(),
                status: goap_domain::CHANGE_DRAFT,
                parent_id: id.clone(),
                owner_org: gr.org.clone(),
                created_at: self.now(),
                ..Default::default()
            };
            self.prepare_sub_change(ctx, &mut tx, &mut sub, &mut input)
                .await?;
            self.scope_change(ctx, &mut tx, &mut sub).await?;
            let fork = tx.baseline(ctx, &sub.baseline_id).await?;
            let own = goap_domain::Branch {
                name: crate::changebranch::change_branch_name(&sub.id),
                namespace: sub.namespace.clone(),
                parent: sub.branch.clone(),
                fork_baseline: fork.id.clone(),
                head: fork.id,
                origin: goap_domain::change_branch_origin(&sub.id),
                status: goap_domain::BRANCH_OPEN.to_string(),
                created_at: self.now(),
                ..Default::default()
            };
            tx.put_branch(ctx, own.clone()).await?;
            sub.branch = own.name;
            tx.put_change(ctx, sub.clone()).await?;
            for cn in &gr.nodes {
                // the candidate moves to the sub-change (ADR 0103): withdrawn from the parent, it comes back to it with
                // the version the sub-change commits, when the sub-change merges
                self.emit(
                    ctx,
                    &mut tx,
                    vec![goap_domain::ImpactEvent {
                        change: id.clone(),
                        impact: cn.id.clone(),
                        op: goap_domain::IMPACT_WITHDRAWN,
                        by: "graph.split_by_owner".into(),
                        ..Default::default()
                    }],
                )
                .await?;
                // DerivedFrom holds the id of the parent's change impact it is copied from
                let cp = goap_domain::ChangeImpact {
                    id: goap_domain::ChangeImpactID::new(self.new_id()),
                    key: cn.key.clone(),
                    r#type: cn.r#type.clone(),
                    intent: goap_domain::INTENT_MODIFIED,
                    rationale: cn.rationale.clone(),
                    pre: cn.pre.clone(),
                    review: goap_domain::REVIEW_PROPOSED,
                    produced_by: "graph.split_by_owner".into(),
                    derived_from: vec![goap_domain::ItemID::new(cn.id.to_string())],
                    created_at: self.now(),
                    ..Default::default()
                };
                self.emit(
                    ctx,
                    &mut tx,
                    vec![goap_domain::ImpactEvent {
                        change: sub.id.clone(),
                        impact: cp.id.clone(),
                        op: goap_domain::IMPACT_PROPOSED,
                        by: cp.produced_by.clone(),
                        state: Some(cp),
                        ..Default::default()
                    }],
                )
                .await?;
            }
            sub.status = goap_domain::CHANGE_ACTIVE;
            tx.put_change(ctx, sub.clone()).await?;
            created.push(sub);
        }
        tx.commit(ctx).await?;
        Ok(created)
    }

    /// abandonSubChanges abandons the open sub-changes of a change (and their branches), recursively.
    pub(crate) async fn abandon_sub_changes(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
    ) -> Result<()> {
        for mut sc in open_sub_changes(ctx, tx, id).await? {
            Box::pin(self.abandon_sub_changes(ctx, tx, &sc.id)).await?;
            if let Some(mut own) = own_branch(ctx, tx, &sc).await? {
                own.status = BRANCH_ABANDONED.to_string();
                tx.put_branch(ctx, own).await?;
            }
            sc.status = CHANGE_ABANDONED;
            tx.put_change(ctx, sc).await?;
        }
        Ok(())
    }
}

/// subChanges lists the sub-changes of a change.
pub(crate) async fn sub_changes(ctx: &Ctx, tx: &mut dyn Tx, id: &ChangeID) -> Result<Vec<Change>> {
    let all = tx.changes(ctx).await?;
    Ok(all.into_iter().filter(|c| &c.parent_id == id).collect())
}

/// openSubChanges are the sub-changes that still have to be applied or abandoned.
pub(crate) async fn open_sub_changes(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    id: &ChangeID,
) -> Result<Vec<Change>> {
    let mut subs = sub_changes(ctx, tx, id).await?;
    subs.retain(|c| c.status != CHANGE_APPLIED && c.status != CHANGE_ABANDONED);
    Ok(subs)
}

// ---- the checks of a version (ADR 0054, 0065, 0076, 0079) --------------------------------------------------------

/// requirement is a number of outgoing links of one type a node must have: what the parent of a structure (ADR 0054)
/// and the required links a node type declares (ADR 0065) come down to. of describes it in error messages.
pub(crate) struct Requirement {
    pub(crate) link_type: String,
    pub(crate) count: i64,
    pub(crate) of: String,
}

impl Graph {
    /// requirementsOf returns the links a node of type typ and key must carry: the parent of a node of a structure,
    /// except the root of it, and the links its node type requires. Empty for any other node.
    pub(crate) fn requirements_of(&self, typ: &str, key: &str) -> Vec<Requirement> {
        let mut out = Vec::new();
        for st in self.bootstrap_structures() {
            if typ == st.r#type && key != st.root {
                out.push(Requirement {
                    link_type: st.parent.clone(),
                    count: 1,
                    of: format!("a parent {} ({})", st.r#type, st.parent),
                });
            }
        }
        for r in self.requires(typ) {
            out.push(Requirement {
                link_type: r.link.clone(),
                count: r.exactly(),
                of: format!("exactly {} {}", r.exactly(), r.link),
            });
        }
        out
    }

    /// checkRequiredLinks checks that a version or a draft carries the links its type requires (the parent of a node
    /// of a structure, the membership of a User): read back from the node itself, so a client that built its edits
    /// from a stale read fails instead of leaving the node with zero or two of them. Checked when a draft is accepted
    /// (ADR 0079) and when the version lands, never while the draft is edited.
    pub(crate) fn check_required_links(&self, n: &Node, links: &[Link]) -> Result<()> {
        let reqs = self.requirements_of(&n.r#type, &n.key);
        if reqs.is_empty() || n.deleted {
            return Ok(());
        }
        for r in reqs {
            let count = links.iter().filter(|l| l.r#type == r.link_type).count() as i64;
            if count != r.count {
                return Err(Error::wrap(
                    ErrorKind::Invalid,
                    format!("{} needs {}, got {count}", n.key, r.of),
                ));
            }
        }
        Ok(())
    }

    /// checkVersion is what a draft must satisfy to be accepted and the version written from it to land (ADR 0076,
    /// 0079): its properties, the links its type requires, and for each outgoing link its attributes, their validators
    /// and the link validators of its type.
    pub(crate) async fn check_version(
        &self,
        ctx: &Ctx,
        ix: &TypeIndex,
        n: &Node,
        links: &[Link],
        resolve: &mut dyn NodeResolver,
    ) -> Result<()> {
        self.validate_props(ctx, ix, n, &n.properties).await?;
        self.check_required_links(n, links)?;
        for l in links {
            ix.check_link_attributes(&l.r#type, &l.properties)
                .map_err(|e| e.context(&n.key))?;
            self.run_link_validators(ctx, ix, n, l, Some(&mut *resolve))
                .await?;
        }
        Ok(())
    }

    /// checkWritten checks a version a landing wrote, with its links.
    pub(crate) async fn check_written(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        ix: &TypeIndex,
        n: &Node,
    ) -> Result<()> {
        let links = tx.out_links(ctx, &n.r#ref()).await?;
        let mut r = StoredResolver { tx };
        self.check_version(ctx, ix, n, &links, &mut r).await
    }

    /// checkDraft checks a draft as the version written from it will be.
    pub(crate) async fn check_draft(
        &self,
        ctx: &Ctx,
        ix: &TypeIndex,
        c: &Change,
        d: &Draft,
        resolve: &mut dyn NodeResolver,
    ) -> Result<()> {
        self.check_version(ctx, ix, &draft_node(c, d), &d.out_links(), resolve)
            .await
    }
}

/// storedResolver resolves exact versions from the store: the links of a version written by a landing.
pub(crate) struct StoredResolver<'a> {
    pub(crate) tx: &'a mut dyn Tx,
}

#[async_trait::async_trait]
impl NodeResolver for StoredResolver<'_> {
    async fn resolve(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Option<Node>> {
        match self.tx.node(ctx, r).await {
            Ok(n) => Ok(Some(n)),
            Err(e) if e.is_not_found() => Ok(None),
            Err(e) => Err(e),
        }
    }
}

/// draftResolver resolves the references of a draft through the drafts the reader holds, else the store.
pub(crate) struct DraftResolver<'a> {
    pub(crate) tx: &'a mut dyn Tx,
    pub(crate) reader: &'a DraftReader,
}

#[async_trait::async_trait]
impl NodeResolver for DraftResolver<'_> {
    async fn resolve(&mut self, ctx: &Ctx, r: &NodeRef) -> Result<Option<Node>> {
        let r = self.reader.normalize(r);
        if r.is_draft() {
            return Ok(self
                .reader
                .by_node
                .get(&r.id)
                .map(|d| self.reader.draft_node(d)));
        }
        match self.tx.node(ctx, &r).await {
            Ok(n) => Ok(Some(n)),
            Err(e) if e.is_not_found() => Ok(None),
            Err(e) => Err(e),
        }
    }
}
