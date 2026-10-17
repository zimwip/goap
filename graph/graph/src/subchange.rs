//! Part of `subchange.go` (G1): the rules of a new sub-change and the listing and abandonment of sub-changes (ADR
//! 0103). The checks of a version (`checkVersion`, `checkDraft`, `checkRequiredLinks`) and SplitByOwner belong to
//! later slices (see PORTING.md).

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
