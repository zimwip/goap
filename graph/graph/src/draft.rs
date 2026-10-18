//! Reading the drafts of a change (ADR 0079). A draft is the working state of a node in a change,
//! per change impact and flow, the fold of the impact log ([`Graph::drafts`]). What a flow sees of a node of the
//! change is the draft of the innermost flow of its chain that holds one (the flow, its ancestors, the main flow); a
//! draft is its flow's to edit, a child flow checks the node out to write its own. Outside the change a node is still
//! its last landed version.

use std::collections::HashMap;

use goap_ctx::Ctx;
use goap_domain::{
    Change, ChangeID, ChangeImpact, ChangeImpactID, Draft, DraftLink, Flow, Link, LinkID, Node,
    NodeRef, NodeView, draft_seen_by, namespace_of,
};

use crate::api::{LinkWrite, draft_branch};
use crate::changebranch::own_branch;
use crate::changelog::impact_events;
use crate::checkout::Work;
use crate::flownodes::{FlowNodes, flow_chain, stale_of};
use crate::lifecycle::invalidf;
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::{Graph, Result};

/// draftNode is the draft seen as a node: no version (version 0), the namespace of the change, the branch of its flow.
pub(crate) fn draft_node(c: &Change, d: &Draft) -> Node {
    let mut n = d.as_node();
    n.namespace = namespace_of(&c.namespace).to_string();
    n.branch = draft_branch(c, &d.flow);
    n
}

/// ownDraft is the draft of a flow itself (None: none) among the rows of a change.
pub(crate) fn own_draft(rows: &[Draft], impact: &ChangeImpactID, flow: &str) -> Option<Draft> {
    rows.iter()
        .find(|d| &d.impact == impact && d.flow == flow)
        .cloned()
}

pub(crate) fn flow_ids(chain: &[Flow]) -> Vec<String> {
    chain.iter().map(|f| f.id.clone()).collect()
}

impl Graph {
    /// seenDrafts are the drafts a process on a flow sees, by change impact: the rows of the innermost flow of the
    /// chain holding one, or, when runs are stale on the chain, what the events of the others left without them.
    pub(crate) async fn seen_drafts(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
    ) -> Result<HashMap<ChangeImpactID, Draft>> {
        let rows = self.drafts(ctx, tx, &c.id).await?;
        let chain = flow_chain(c, flow);
        let stale = stale_of(&chain);
        let ids = flow_ids(&chain);
        let mut out: HashMap<ChangeImpactID, Draft> = HashMap::new();
        if stale.is_empty() {
            let mut order = ids.clone();
            order.push(String::new());
            for f in &order {
                for d in &rows {
                    if !out.contains_key(&d.impact) && &d.flow == f {
                        out.insert(d.impact.clone(), d.clone());
                    }
                }
            }
            return Ok(out);
        }
        let events = impact_events(ctx, tx, &c.id).await?;
        let is_stale = |e: &str| !e.is_empty() && stale.get(e).copied().unwrap_or(false);
        for cn in &c.nodes {
            if let Some(d) = draft_seen_by(&events, &cn.id, &ids, &is_stale) {
                out.insert(cn.id.clone(), d);
            }
        }
        Ok(out)
    }

    /// seenDraft is the draft a flow sees for a change impact (None: none).
    pub(crate) async fn seen_draft(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        impact: &ChangeImpactID,
    ) -> Result<Option<Draft>> {
        let mut m = self.seen_drafts(ctx, tx, c, flow).await?;
        Ok(m.remove(impact))
    }

    /// newDraftReader reads the drafts a flow sees, for the change impacts it sees (visible: stale or foreign impacts
    /// leave their drafts out).
    pub(crate) async fn new_draft_reader(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        visible: &[ChangeImpact],
    ) -> Result<DraftReader> {
        let seen = self.seen_drafts(ctx, tx, c, flow).await?;
        let mut r = DraftReader {
            c: c.clone(),
            by_node: HashMap::new(),
        };
        for cn in visible {
            if let Some(d) = seen.get(&cn.id) {
                r.by_node.insert(d.node.clone(), d.clone());
            }
        }
        Ok(r)
    }
}

/// draftReader reads nodes as a flow of a change sees them: a draft reference (a node the flow holds a draft of) is
/// the draft, any other reference a stored version.
#[derive(Debug, Clone, Default)]
pub(crate) struct DraftReader {
    pub(crate) c: Change,
    pub(crate) by_node: HashMap<goap_domain::NodeID, Draft>,
}

impl DraftReader {
    /// draftNode is the node of a draft the reader holds.
    pub(crate) fn draft_node(&self, d: &Draft) -> Node {
        draft_node(&self.c, d)
    }

    /// draft is the draft a reference designates: a draft reference, or an exact version the draft was checked out
    /// from.
    pub(crate) fn draft(&self, r: &NodeRef) -> Option<&Draft> {
        let d = self.by_node.get(&r.id)?;
        if !r.is_draft() && d.base.as_ref() != Some(r) {
            return None;
        }
        Some(d)
    }

    /// normalize points a reference to the version a node was checked out from at the draft of the node.
    pub(crate) fn normalize(&self, r: &NodeRef) -> NodeRef {
        if let Some(d) = self.by_node.get(&r.id)
            && d.base.as_ref() == Some(r)
        {
            return d.r#ref();
        }
        r.clone()
    }

    /// node reads a node: the draft for a draft reference, else the stored version.
    pub(crate) async fn node(&self, ctx: &Ctx, tx: &mut dyn Tx, r: &NodeRef) -> Result<Node> {
        if r.is_draft()
            && let Some(d) = self.by_node.get(&r.id)
        {
            return Ok(self.draft_node(d));
        }
        tx.node(ctx, r).await
    }

    /// outLinks reads the outgoing links of a node: the ones of its draft for a draft reference, else the stored
    /// ones. The targets a draft holds as the version a node it also holds was checked out from are shown as that
    /// draft.
    pub(crate) async fn out_links(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        r: &NodeRef,
    ) -> Result<Vec<Link>> {
        if r.is_draft()
            && let Some(d) = self.by_node.get(&r.id)
        {
            let mut out = d.out_links();
            for l in &mut out {
                l.to = self.normalize(&l.to);
            }
            return Ok(out);
        }
        tx.out_links(ctx, r).await
    }

    /// inLinks reads the links entering a node: the stored ones of the version, and the ones the drafts of the flow
    /// hold to it.
    pub(crate) async fn in_links(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        r: &NodeRef,
    ) -> Result<Vec<Link>> {
        let mut out = Vec::new();
        let mut from = r.clone();
        if let Some(d) = self.by_node.get(&r.id)
            && r.is_draft()
            && let Some(b) = &d.base
        {
            from = b.clone(); // the links of the version the draft was checked out from still enter the node
        }
        if !from.is_draft() {
            let stored = tx.in_links(ctx, &from).await?;
            for l in stored {
                // a link of a version a draft of the flow replaces is the draft's now
                if self.draft(&l.from).is_some() && !l.from.is_draft() {
                    continue;
                }
                out.push(l);
            }
        }
        for d in self.sorted_drafts() {
            for l in &d.links {
                let to = self.normalize(&l.to);
                if to.id == r.id && (r.is_draft() == to.is_draft()) && (r.is_draft() || &to == r) {
                    out.push(Link {
                        id: l.id.clone(),
                        r#type: l.r#type.clone(),
                        from: d.r#ref(),
                        to,
                        properties: l.properties.clone(),
                        change_id: d.change.clone(),
                    });
                }
            }
        }
        Ok(out)
    }

    pub(crate) fn sorted_drafts(&self) -> Vec<Draft> {
        let mut out: Vec<Draft> = self.by_node.values().cloned().collect();
        out.sort_by(|a, b| a.key.cmp(&b.key));
        out
    }
}

impl Graph {
    /// ChangeNodeView reads a node as a call of a change on a flow sees it (ADR 0079): for a draft reference (version
    /// 0) the draft the flow holds for the node, hydrated with its links, else the stored version (version 0 with no
    /// draft: the latest on main).
    pub async fn change_node_view(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        r: &NodeRef,
    ) -> Result<NodeView> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let flow = c.resolve_flow(flow);
        let seen = FlowNodes::new(c.clone(), &flow).nodes(ctx, &mut tx).await?;
        let reader = self
            .new_draft_reader(ctx, &mut tx, &c, &flow, &seen)
            .await?;
        let v = view_in(ctx, &mut tx, &reader, r).await?;
        tx.commit(ctx).await?;
        Ok(v)
    }

    /// LinkSourceType is the type of the node an outgoing link of a draft of the change leaves (or, failing that, the
    /// stored link of that id): what the gate of the access nodes asks before a link of a draft is edited (ADR 0079).
    pub async fn link_source_type(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
    ) -> Result<String> {
        let mut tx = self.begin(ctx).await?;
        let rows = self.drafts(ctx, &mut tx, id).await?;
        if let Some(d) = rows.iter().find(|d| d.link(link).is_some()) {
            let typ = d.r#type.clone();
            tx.commit(ctx).await?;
            return Ok(typ);
        }
        let l = tx.link(ctx, link).await?;
        let n = tx.node(ctx, &l.from).await?;
        tx.commit(ctx).await?;
        Ok(n.r#type)
    }

    /// ChangeNodeViewByKey is ChangeNodeView for the node a key designates: the one the change holds a draft of on the
    /// flow, else the latest version of the node on main.
    pub async fn change_node_view_by_key(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        namespace: &str,
        key: &str,
    ) -> Result<NodeView> {
        let mut tx = self.begin(ctx).await?;
        let c = tx.change(ctx, id).await?;
        let flow = c.resolve_flow(flow);
        let seen = FlowNodes::new(c.clone(), &flow).nodes(ctx, &mut tx).await?;
        let reader = self
            .new_draft_reader(ctx, &mut tx, &c, &flow, &seen)
            .await?;
        let same_ns = namespace_of(&c.namespace) == namespace_of(namespace);
        let mut r = NodeRef::default();
        for d in reader.sorted_drafts() {
            if d.key == key && same_ns {
                r = d.r#ref();
            }
        }
        if r.id.is_empty() && same_ns {
            // a node the change committed, or a sub-change merged into it (ADR 0103): the version it landed
            for cn in &seen {
                if cn.key == key
                    && let Some(l) = &cn.landed
                {
                    r = l.clone();
                } else if cn.key == key
                    && let Some(p) = &cn.post
                    && !p.is_draft()
                {
                    r = p.clone();
                }
            }
        }
        if r.id.is_empty() {
            let n = tx.node_by_key(ctx, namespace, key).await?;
            r = n.r#ref();
            // a change with a branch of its own reads the version its branch holds: what its parent committed, for a
            // sub-change (ADR 0103), not the latest version anywhere
            if let Some(own) = own_branch(ctx, &mut tx, &c).await? {
                let head = branch_head(ctx, &mut tx, &c.namespace, &own.name).await?;
                if let Some(at) = head.nodes.get(&n.id) {
                    r.version = *at;
                }
            }
        }
        let v = view_in(ctx, &mut tx, &reader, &r).await?;
        tx.commit(ctx).await?;
        Ok(v)
    }

    /// ChangeNode is ChangeNodeView without the neighbourhood.
    pub async fn change_node(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        flow: &str,
        r: &NodeRef,
    ) -> Result<Node> {
        Ok(self.change_node_view(ctx, id, flow, r).await?.node)
    }
}

/// viewIn hydrates a node with its neighbourhood as a draft reader sees it.
pub(crate) async fn view_in(
    ctx: &Ctx,
    tx: &mut dyn Tx,
    reader: &DraftReader,
    r: &NodeRef,
) -> Result<NodeView> {
    let n = reader.node(ctx, tx, r).await?;
    let latest = match tx
        .node(
            ctx,
            &NodeRef {
                id: r.id.clone(),
                version: 0,
            },
        )
        .await
    {
        Ok(l) => l.version,
        Err(e) if e.is_not_found() => n.version, // a node the change creates, not on main yet
        Err(e) => return Err(e),
    };
    let out = reader.out_links(ctx, tx, r).await?;
    let r#in = reader.in_links(ctx, tx, r).await?;
    Ok(NodeView {
        node: n,
        latest,
        out,
        r#in,
        ..Default::default()
    })
}

impl Graph {
    /// draftLinkTargetType is the type of the node a link of a draft targets: a draft of the flow, or a stored
    /// version.
    pub(crate) async fn draft_link_target_type(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &Work,
        to: &NodeRef,
    ) -> Result<String> {
        if to.is_draft() {
            for cn in &w.seen {
                if let Some(p) = &cn.post
                    && p.is_draft()
                    && p.id == to.id
                {
                    return Ok(cn.r#type.clone());
                }
            }
            return Err(invalidf(format!(
                "a link targets the draft of node {}, which the change holds no draft of on this flow",
                to.id
            )));
        }
        Ok(tx.node(ctx, to).await?.r#type)
    }

    /// newDraftLink builds the outgoing link a draft gains: the type and the ends are checked, the target (an exact
    /// version, or the draft of a node of the change) is pointed to the draft of the node when it was checked out from
    /// that version.
    pub(crate) async fn new_draft_link(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &Work,
        from: &Draft,
        l: &LinkWrite,
    ) -> Result<DraftLink> {
        if l.r#type.is_empty() || l.to.id.is_empty() {
            return Err(invalidf("a link needs a type and a target"));
        }
        let to_type = self.draft_link_target_type(ctx, tx, w, &l.to).await?;
        if !l.to.is_draft() && l.to.version == 0 {
            return Err(invalidf(
                "a link needs an exact target version, or the draft of a node of the change",
            ));
        }
        w.ix.check_link(&l.r#type, &from.r#type, &to_type)?;
        w.ix.check_link_attributes(&l.r#type, &l.properties)?;
        Ok(DraftLink {
            id: LinkID::new(self.new_id()),
            r#type: l.r#type.clone(),
            to: w.normalize_to(&l.to),
            properties: l.properties.clone(),
        })
    }

    /// draftFrom is the draft of a stored version: a checkout copies it (the outgoing links get ids of their own).
    pub(crate) fn draft_from(
        &self,
        w: &Work,
        base: &Node,
        links: &[Link],
        execution: &str,
    ) -> Draft {
        let mut d = Draft {
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            flow: w.flow.clone(),
            node: base.id.clone(),
            key: base.key.clone(),
            r#type: base.r#type.clone(),
            state: base.state.clone(),
            owner: base.owner.clone(),
            properties: base.properties.clone(),
            base: Some(base.r#ref()),
            execution: execution.to_string(),
            ..Default::default()
        };
        for l in links {
            d.links.push(DraftLink {
                id: LinkID::new(self.new_id()),
                r#type: l.r#type.clone(),
                to: w.normalize_to(&l.to),
                properties: l.properties.clone(),
            });
        }
        d
    }

    /// forkDraft is the draft a flow starts from when it checks a node out that a parent flow holds a draft of: a
    /// copy, the links with ids of their own.
    pub(crate) fn fork_draft(&self, w: &Work, parent: &Draft, execution: &str) -> Draft {
        let mut d = parent.clone();
        d.change = w.c.id.clone();
        d.impact = w.cn.id.clone();
        d.flow = w.flow.clone();
        d.execution = execution.to_string();
        for l in &mut d.links {
            l.id = LinkID::new(self.new_id());
        }
        d
    }
}

impl Work {
    /// normalizeTo points the version a node of the flow was checked out from at its draft.
    pub(crate) fn normalize_to(&self, to: &NodeRef) -> NodeRef {
        for cn in &self.seen {
            if let (Some(p), Some(pre)) = (&cn.post, &cn.pre)
                && p.is_draft()
                && p.id == to.id
                && pre == to
            {
                return p.clone();
            }
        }
        to.clone()
    }
}

/// draftOf names the draft of a change impact in an error message.
pub(crate) fn draft_of(cn: &ChangeImpact) -> String {
    format!("change impact {} ({})", cn.id, cn.key)
}
