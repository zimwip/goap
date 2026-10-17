//! Port of `checkout.go`: how a change edits nodes (ADR 0076, 0079). Every write names the change; a node has no
//! version during the change: ImpactNodeCreate or ImpactNodeCheckout gives it a draft on the flow (once per change and
//! flow), and the draft stays until the change lands, which writes the version from it:
//!
//!   - ImpactNodeUpdate and the link operations edit the draft (an `updated` event with the patch), writing no
//!     version;
//!   - an accepted review runs the checks of a version on the draft and records the acceptance; editing it again sends
//!     the review back to proposed;
//!   - a lifecycle transition (ImpactNodeTransition) sets the state of the draft, which a node with none is checked out
//!     for first; authorized and guarded when it is taken (the guard sees the change, the impact and the draft);
//!   - ImpactNodeCancel drops the draft; a creation cancelled leaves no node.
//!
//! A creation may be planned before it is written (ProposeImpact with intent created: a key, a type, a rationale, no
//! draft): the impact analysis names what the change will create, the specification writes it. Creating or checking
//! out the key of a planned creation gives it its draft (a checkedOut event, so a flow fulfils it as it edits any
//! impact); it lands like any creation.

use std::collections::{BTreeMap, HashSet};

use goap_ctx::Ctx;
use goap_domain::{
    Change, ChangeID, ChangeImpact, ChangeImpactID, Draft, DraftLink, FLOW_OPEN, IMPACT_CANCELLED,
    IMPACT_CHECKED_OUT, IMPACT_CREATED, IMPACT_REVIEWED, IMPACT_TRANSITIONED, IMPACT_UPDATED,
    IMPACT_WITHDRAWN, INTENT_CREATED, INTENT_MODIFIED, ImpactEvent, ItemID, Link, LinkID, Node,
    NodeID, NodeRef, REVIEW_PROPOSED, Review, STRUCTURE_ORGANISATION, Transition, Version,
    branch_of, namespace_of,
};
use goap_operations as operations;
use serde_json::{Map, Value, json};

use crate::api::{
    LinkWrite, NodeCheckout, NodeCreate, NodeTransition, NodeUpdate, flow_branch_name,
};
use crate::apply::{Applier, PendingMove};
use crate::changebranch::own_branch;
use crate::changeimpact::{change_open, find_change_impact};
use crate::draft::{draft_node, draft_of, flow_ids, own_draft};
use crate::flownodes::{FlowNodes, flow_chain, node_of};
use crate::lifecycle::{TypeIndex, invalidf};
use crate::merge::branch_head;
use crate::repo::Tx;
use crate::restructure::origins_patch;
use crate::{Error, ErrorKind, Graph, Result};

/// work is what an operation on a change impact needs: the change, the flow it works on and the branch versions are
/// written on, and the change impact as the flow sees it.
#[derive(Clone, Default)]
pub(crate) struct Work {
    pub(crate) c: Change,
    pub(crate) flow: String,
    pub(crate) branch: String,
    pub(crate) ns: String,
    pub(crate) ix: TypeIndex,
    pub(crate) seen: Vec<ChangeImpact>,
    /// the stored change impact
    pub(crate) cn: ChangeImpact,
    /// as the flow sees it
    pub(crate) vcn: ChangeImpact,
    /// planned is set when a creation names the key of a planned creation (resolve): its draft fulfils that impact
    pub(crate) planned: bool,
}

/// target names what an operation works on: a change impact, or a node (by id, or by key and type), which the
/// resolver turns into a change impact (see resolve).
#[derive(Clone, Default)]
pub(crate) struct Target {
    pub(crate) impact: ChangeImpactID,
    pub(crate) node: NodeID,
    pub(crate) key: String,
    pub(crate) r#type: String,
    /// create: the operation creates the node, so the key must be new.
    pub(crate) create: bool,
    pub(crate) rationale: String,
    pub(crate) flow: String,
    pub(crate) execution: String,
    pub(crate) produced_by: String,
    pub(crate) derived_from: Vec<ItemID>,
}

/// checkoutTarget is the node a checkout or a transition names.
pub(crate) fn checkout_target(i: &NodeCheckout) -> Target {
    Target {
        impact: i.impact.clone(),
        node: i.node.clone(),
        key: i.key.clone(),
        rationale: i.rationale.clone(),
        flow: i.flow.clone(),
        execution: i.execution.clone(),
        produced_by: i.produced_by.clone(),
        ..Default::default()
    }
}

/// plannedCreation reports whether the change impact is a creation planned by ProposeImpact and not drafted yet: no
/// draft, no version.
pub(crate) fn planned_creation(cn: &ChangeImpact) -> bool {
    cn.intent == INTENT_CREATED && cn.post.is_none() && cn.landed.is_none()
}

impl Work {
    /// selectImpact selects the change impact the operation works on, as the flow sees it.
    pub(crate) fn select_impact(&mut self, id: &ChangeImpactID) -> Result<()> {
        let i = find_change_impact(&self.c, id)?;
        self.cn = self.c.nodes[i].clone();
        let mut found = false;
        for cn in &self.seen {
            if &cn.id == id {
                self.vcn = cn.clone();
                found = true;
            }
        }
        if !found {
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!(
                    "change impact {id} is not on the flow {:?} of change {}",
                    self.flow, self.c.id
                ),
            ));
        }
        Ok(())
    }

    /// base is the stored version a draft of the impact starts from: its pre version (on the main flow, the latest of
    /// the node on the change branch); None for a creation.
    pub(crate) async fn base(&self, ctx: &Ctx, tx: &mut dyn Tx) -> Result<Option<Node>> {
        let Some(pre) = &self.cn.pre else {
            if self.cn.landed.is_none() {
                return Ok(None);
            }
            // a node the change created and committed: checked out again from the version it wrote (ADR 0102)
            return match tx.latest_on(ctx, &node_of(&self.cn), &self.branch).await {
                Err(e) if e.is_not_found() => Ok(None),
                r => r.map(Some),
            };
        };
        let mut n = tx.node(ctx, pre).await?;
        if self.flow.is_empty() {
            match tx.latest_on(ctx, &n.id, &self.branch).await {
                Ok(latest) => n = latest,
                Err(e) if e.is_not_found() => {}
                Err(e) => return Err(e),
            }
        }
        Ok(Some(n))
    }

    /// seenAs returns the change impact as the operation leaves it, with its post.
    pub(crate) fn seen_as(&self, post: Option<&NodeRef>) -> ChangeImpact {
        let mut cn = self.vcn.clone();
        if let Some(p) = post {
            cn.post = Some(p.clone());
        }
        cn
    }

    /// checkDraftAttributes checks the type and enum of the values of a draft: what every edit of a draft is held to.
    pub(crate) fn check_draft_attributes(
        &self,
        d: &Draft,
        props: &Map<String, Value>,
    ) -> Result<()> {
        self.ix.check_attributes(&draft_node(&self.c, d), props)
    }
}

/// linkPatch describes a link added to a draft, for the updated event: the audit trail reads to, the draft fold the
/// rest.
pub(crate) fn link_patch(l: &DraftLink) -> Value {
    json!({"id": l.id.to_string(), "type": l.r#type, "to": l.to.to_string(), "toId": l.to.id.to_string(),
        "toVersion": l.to.version, "props": Value::Object(l.properties.clone())})
}

fn one(k: &str, v: Value) -> Map<String, Value> {
    let mut m = Map::new();
    m.insert(k.to_string(), v);
    m
}

impl Graph {
    /// workOn opens an operation on a change, on a flow: the change gets its branch (where its versions are written
    /// when it lands, ADR 0079).
    pub(crate) async fn work_on(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        flow: &str,
    ) -> Result<Work> {
        let mut c = change_open(ctx, tx, id).await?;
        if own_branch(ctx, tx, &c).await?.is_none() {
            c = self.ensure_own_branch(ctx, tx, c).await?.0;
        }
        let mut w = Work {
            flow: c.resolve_flow(flow),
            branch: branch_of(&c.branch).to_string(),
            ns: namespace_of(&c.namespace).to_string(),
            ..Default::default()
        };
        if !w.flow.is_empty() {
            if c.flow(&w.flow).is_none() {
                return Err(Error::wrap(ErrorKind::NotFound, format!("flow {}", w.flow)));
            }
            if c.flow_status_of(&w.flow) != FLOW_OPEN {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("flow {} is not open", w.flow),
                ));
            }
            w.branch = flow_branch_name(&w.flow);
        }
        w.ix = self.types_at();
        w.seen = FlowNodes::new(c.clone(), &w.flow).nodes(ctx, tx).await?;
        w.c = c;
        Ok(w)
    }

    /// impact selects the change impact the operation works on, to edit it: one written in a state of the lifecycle of
    /// the change that the change has left is frozen (ADR 0058).
    pub(crate) async fn impact(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &mut Work,
        id: &ChangeImpactID,
    ) -> Result<()> {
        w.select_impact(id)?;
        let (frozen, r#in) = self.frozen(ctx, tx, &w.c, id).await?;
        if frozen {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "change impact {id} was written in state {in}, which the change has left (now {}): go back to it first",
                    w.c.state
                ),
            ));
        }
        Ok(())
    }

    /// resolve is the one entry of every operation on a node of a change (ADR 0077, "Resolution"): it finds the change
    /// impact the operation names, declaring it in the same transaction when the change holds none, and selects it.
    ///
    ///   - an impact id is taken as it is;
    ///   - a node (id, or key) the flow already sees in the change reuses its impact, never doubling it;
    ///   - else a node of the reference baseline gets an impact of intent modified, its pre the baseline version;
    ///   - else, for a creation, the key is new: the impact is checked but not recorded, the created event of its
    ///     first version carries it (ADR 0077); for any other operation the node is in neither the baseline nor the
    ///     change: not found.
    ///
    /// A creation whose key the baseline or the change already holds is a conflict.
    pub(crate) async fn resolve(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        t: Target,
    ) -> Result<Work> {
        let mut w = self.work_on(ctx, tx, id, &t.flow).await?;
        if !t.impact.is_empty() {
            self.impact(ctx, tx, &mut w, &t.impact).await?;
            return Ok(w);
        }
        if t.node.is_empty() && t.key.is_empty() {
            return Err(invalidf("name a change impact or a node"));
        }
        if t.create && (t.key.is_empty() || t.r#type.is_empty()) {
            return Err(invalidf("a node needs a key and a type"));
        }
        let mut found: Option<ChangeImpact> = None;
        for cn in &w.seen {
            if (!t.node.is_empty() && node_of(cn) == t.node)
                || (t.node.is_empty()
                    && cn.key == t.key
                    && (t.r#type.is_empty() || cn.r#type == t.r#type))
            {
                found = Some(cn.clone());
            }
        }
        if let Some(found) = found {
            if t.create
                && planned_creation(&found)
                && (t.r#type.is_empty() || found.r#type == t.r#type)
            {
                w.planned = true;
                self.impact(ctx, tx, &mut w, &found.id).await?;
                return Ok(w);
            }
            if t.create {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!(
                        "cannot create {} ({}): the change already holds it as change impact {} ({})",
                        t.key, t.r#type, found.id, found.intent
                    ),
                ));
            }
            self.impact(ctx, tx, &mut w, &found.id).await?;
            return Ok(w);
        }
        let b = tx.baseline(ctx, &w.c.baseline_id).await?;
        let mut nid = t.node.clone();
        if nid.is_empty() {
            match tx.node_id_by_key(ctx, &w.ns, &t.key).await {
                Ok(got) => nid = got,
                Err(e) if e.is_not_found() => {}
                Err(e) => return Err(e),
            }
        }
        let v = b.nodes.get(&nid).copied();
        let in_base = v.is_some() && !nid.is_empty();
        let mut decl = if t.create && in_base {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "cannot create {} ({}): the reference baseline of change {id} already holds it",
                    t.key, t.r#type
                ),
            ));
        } else if t.create {
            ChangeImpact {
                intent: INTENT_CREATED,
                key: t.key.clone(),
                r#type: t.r#type.clone(),
                derived_from: t.derived_from.clone(),
                ..Default::default()
            }
        } else if in_base {
            ChangeImpact {
                intent: INTENT_MODIFIED,
                pre: Some(NodeRef {
                    id: nid.clone(),
                    version: v.unwrap_or_default(),
                }),
                ..Default::default()
            }
        } else {
            let name = if t.node.is_empty() {
                t.key.clone()
            } else {
                t.node.to_string()
            };
            return Err(Error::wrap(
                ErrorKind::NotFound,
                format!("node {name} is in neither the reference baseline nor the change {id}"),
            ));
        };
        decl.rationale = t.rationale.clone();
        decl.flow = t.flow.clone();
        decl.execution = t.execution.clone();
        decl.produced_by = t.produced_by.clone();
        let out = self.declare_tx(ctx, tx, id, vec![decl], !t.create).await?;
        let mut w = self.work_on(ctx, tx, id, &t.flow).await?;
        if t.create {
            // not recorded yet: the created event of the first version adds it (createTx)
            w.cn = out[0].clone();
            w.vcn = out[0].clone();
            w.seen.push(out[0].clone());
            return Ok(w);
        }
        self.impact(ctx, tx, &mut w, &out[0].id).await?;
        Ok(w)
    }

    /// working is the draft of the impact on the flow itself, editable: the flow checked the node out, and the impact
    /// is not rejected. An accepted one is edited all the same: editedAgain then sends its review back to proposed.
    pub(crate) async fn working(&self, ctx: &Ctx, tx: &mut dyn Tx, w: &Work) -> Result<Draft> {
        let rows = self.drafts(ctx, tx, &w.c.id).await?;
        match own_draft(&rows, &w.cn.id, &w.flow) {
            Some(d) => Ok(d),
            None => {
                if self
                    .seen_draft(ctx, tx, &w.c, &w.flow, &w.cn.id)
                    .await?
                    .is_some()
                {
                    // the draft of a parent flow is that flow's to edit: this flow checks the node out to write its own
                    return Err(Error::wrap(
                        ErrorKind::Conflict,
                        format!(
                            "{} holds a draft of another flow: check it out on this one first",
                            draft_of(&w.cn)
                        ),
                    ));
                }
                Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("{} is not checked out: check it out first", draft_of(&w.cn)),
                ))
            }
        }
    }

    /// ImpactNodeCreate creates a node in a change: one `created` event adds the change impact (intent created) and
    /// the draft of the node (ADR 0076, 0077, 0079). There is no proposal of a new node, and no version until the
    /// change lands.
    ///
    /// pending goap:operation impact.create doer
    pub async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeCreate,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_CREATE).await?;
        if input.key.is_empty() || input.r#type.is_empty() {
            return Err(invalidf("a node needs a key and a type"));
        }
        let mut tx = self.begin(ctx).await?;
        let cn = self.create_tx(ctx, &mut tx, id, &input, &[]).await?;
        tx.commit(ctx).await?;
        Ok(cn)
    }

    /// createTx is ImpactNodeCreate inside a transaction. origins are the nodes the new node derives from (a merge or a
    /// split, ADR 0077): recorded on its draft and in the patch of the created event.
    pub(crate) async fn create_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        input: &NodeCreate,
        origins: &[NodeRef],
    ) -> Result<ChangeImpact> {
        let w = self
            .resolve(
                ctx,
                tx,
                id,
                Target {
                    key: input.key.clone(),
                    r#type: input.r#type.clone(),
                    create: true,
                    rationale: input.rationale.clone(),
                    flow: input.flow.clone(),
                    execution: input.execution.clone(),
                    produced_by: input.produced_by.clone(),
                    derived_from: input.derived_from.clone(),
                    ..Default::default()
                },
            )
            .await?;
        let d = self.new_draft(ctx, tx, &w, input, origins).await?;
        let r = d.r#ref();
        let mut patch = Map::new();
        if !origins.is_empty() {
            patch.insert(
                "origins".into(),
                Value::Array(origins_patch(ctx, tx, origins).await),
            );
        }
        if w.planned {
            // the creation was planned: its impact is recorded, the draft fulfils it
            self.emit(
                ctx,
                tx,
                vec![ImpactEvent {
                    change: w.c.id.clone(),
                    impact: w.cn.id.clone(),
                    op: IMPACT_CHECKED_OUT,
                    flow: w.flow.clone(),
                    execution: input.execution.clone(),
                    post: Some(r.clone()),
                    draft: Some(d),
                    patch,
                    ..Default::default()
                }],
            )
            .await?;
            return Ok(w.seen_as(Some(&r)));
        }
        let st = w.cn.clone(); // the creation adds its impact (ADR 0077)
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: w.c.id.clone(),
                impact: w.cn.id.clone(),
                op: IMPACT_CREATED,
                flow: w.flow.clone(),
                execution: input.execution.clone(),
                state: Some(st),
                post: Some(r.clone()),
                draft: Some(d),
                patch,
                ..Default::default()
            }],
        )
        .await?;
        Ok(w.seen_as(Some(&r)))
    }

    /// newDraft is the draft of a new node: its id, key, type, initial state, owner, properties and links, checked.
    pub(crate) async fn new_draft(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &Work,
        input: &NodeCreate,
        origins: &[NodeRef],
    ) -> Result<Draft> {
        let mut d = Draft {
            change: w.c.id.clone(),
            impact: w.cn.id.clone(),
            flow: w.flow.clone(),
            node: NodeID::new(self.new_id()),
            key: input.key.clone(),
            r#type: input.r#type.clone(),
            properties: input.properties.clone(),
            origins: origins.to_vec(),
            execution: input.execution.clone(),
            ..Default::default()
        };
        if let Some(lc) = w.ix.lifecycle_of(&d.r#type) {
            d.state = lc.initial.clone();
        }
        w.check_draft_attributes(&d, &d.properties)?;
        if !input.owner.is_empty() {
            let unit = self
                .structure_node(ctx, tx, STRUCTURE_ORGANISATION, &input.owner)
                .await?;
            d.owner = unit.id;
        }
        for l in &input.links {
            let dl = self.new_draft_link(ctx, tx, w, &d, l).await?;
            d.links.push(dl);
        }
        Ok(d)
    }

    /// ImpactNodeCheckout puts a node in edit mode in a change: it gives the node a draft on the flow, a copy of what
    /// the flow sees of it (properties, state, owner and outgoing links), whatever its state (ADR 0078). The node is
    /// checked out once per change and flow: it is refused while the flow holds a draft of it (update it, ADR 0079). A
    /// flow whose parent flow holds a draft copies that one. A checkout modifies nothing: the review stands until the
    /// draft is edited (editedAgain, ADR 0102).
    ///
    /// pending goap:operation impact.checkout doer
    pub async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeCheckout,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_CHECKOUT).await?;
        let mut tx = self.begin(ctx).await?;
        let w = self
            .resolve(ctx, &mut tx, id, checkout_target(&input))
            .await?;
        let r = self.checkout_tx(ctx, &mut tx, &w, &input.execution).await?;
        tx.commit(ctx).await?;
        Ok(w.seen_as(Some(&r)))
    }

    /// checkoutTx is the checkout of the impact w selects, inside a transaction (see ImpactNodeCheckout).
    pub(crate) async fn checkout_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &Work,
        execution: &str,
    ) -> Result<NodeRef> {
        let id = w.c.id.clone();
        let rows = self.drafts(ctx, tx, &id).await?;
        if own_draft(&rows, &w.cn.id, &w.flow).is_some() {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("{} is already checked out", draft_of(&w.cn)),
            ));
        }
        let d = match self.seen_draft(ctx, tx, &w.c, &w.flow, &w.cn.id).await? {
            Some(parent) => self.fork_draft(w, &parent, execution),
            None => {
                let base = w.base(ctx, tx).await?;
                if base.is_none() && planned_creation(&w.cn) {
                    // a planned creation checked out: its first draft, bare (ADR 0076: the creation fulfils the plan)
                    let d = self
                        .new_draft(
                            ctx,
                            tx,
                            w,
                            &NodeCreate {
                                key: w.cn.key.clone(),
                                r#type: w.cn.r#type.clone(),
                                execution: execution.to_string(),
                                ..Default::default()
                            },
                            &[],
                        )
                        .await?;
                    let r = d.r#ref();
                    self.emit(
                        ctx,
                        tx,
                        vec![ImpactEvent {
                            change: id.clone(),
                            impact: w.cn.id.clone(),
                            op: IMPACT_CHECKED_OUT,
                            flow: w.flow.clone(),
                            execution: execution.to_string(),
                            post: Some(r.clone()),
                            draft: Some(d),
                            ..Default::default()
                        }],
                    )
                    .await?;
                    return Ok(r);
                }
                let Some(base) = base else {
                    return Err(invalidf(format!(
                        "change impact {} has no version to check out",
                        w.cn.id
                    )));
                };
                if namespace_of(&base.namespace) != w.ns {
                    return Err(invalidf(format!(
                        "node {} is in namespace {}, the change acts on {}",
                        base.key,
                        namespace_of(&base.namespace),
                        w.ns
                    )));
                }
                let links = tx.out_links(ctx, &base.r#ref()).await?;
                self.draft_from(w, &base, &links, execution)
            }
        };
        let r = d.r#ref();
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: id,
                impact: w.cn.id.clone(),
                op: IMPACT_CHECKED_OUT,
                flow: w.flow.clone(),
                execution: execution.to_string(),
                post: Some(r.clone()),
                draft: Some(d),
                ..Default::default()
            }],
        )
        .await?;
        Ok(r)
    }

    /// ImpactNodeUpdate edits the draft of a change impact: its properties (merged and validated) and its owner. No
    /// version is written; the edit is an updated event of the impact log.
    ///
    /// pending goap:operation impact.update doer
    pub async fn impact_node_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        input: NodeUpdate,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_UPDATE).await?;
        if input.properties.is_empty() && input.owner.is_empty() {
            return Err(invalidf("nothing to update"));
        }
        let mut tx = self.begin(ctx).await?;
        let mut w = self
            .resolve(
                ctx,
                &mut tx,
                id,
                Target {
                    impact: impact.clone(),
                    node: input.node.clone(),
                    key: input.key.clone(),
                    flow: input.flow.clone(),
                    execution: input.execution.clone(),
                    ..Default::default()
                },
            )
            .await?;
        let d = self.working(ctx, &mut tx, &w).await?;
        let mut patch = Map::new();
        if !input.properties.is_empty() {
            let mut props = d.properties.clone();
            for (k, v) in &input.properties {
                props.insert(k.clone(), v.clone());
            }
            w.check_draft_attributes(&d, &props)?;
            patch.insert("props".into(), Value::Object(input.properties.clone()));
        }
        if !input.owner.is_empty() {
            let unit = self
                .structure_node(ctx, &mut tx, STRUCTURE_ORGANISATION, &input.owner)
                .await?;
            patch.insert("owner".into(), Value::String(input.owner.clone()));
            patch.insert("ownerId".into(), Value::String(unit.id.to_string()));
        }
        let r = d.r#ref();
        self.emit_updated(ctx, &mut tx, &mut w, &r, &input.execution, patch)
            .await?;
        tx.commit(ctx).await?;
        Ok(w.seen_as(Some(&r)))
    }

    /// ImpactLinkCreate adds an outgoing link to the draft of a change impact.
    ///
    /// pending goap:operation impact.link.create doer
    pub async fn impact_link_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        l: LinkWrite,
        flow: &str,
        execution: &str,
    ) -> Result<Link> {
        self.allow(ctx, id, operations::IMPACT_LINK_CREATE).await?;
        let mut tx = self.begin(ctx).await?;
        let mut w = self.work_on(ctx, &mut tx, id, flow).await?;
        self.impact(ctx, &mut tx, &mut w, impact).await?;
        let d = self.working(ctx, &mut tx, &w).await?;
        let dl = self.new_draft_link(ctx, &mut tx, &w, &d, &l).await?;
        let link = Link {
            id: dl.id.clone(),
            r#type: dl.r#type.clone(),
            from: d.r#ref(),
            to: dl.to.clone(),
            properties: dl.properties.clone(),
            change_id: id.clone(),
        };
        self.emit_updated(
            ctx,
            &mut tx,
            &mut w,
            &d.r#ref(),
            execution,
            one("addLink", link_patch(&dl)),
        )
        .await?;
        tx.commit(ctx).await?;
        Ok(link)
    }

    /// emitUpdated records an edit of the draft r (the updated event with its patch), then sends a decided review back
    /// to proposed (editedAgain).
    pub(crate) async fn emit_updated(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &mut Work,
        r: &NodeRef,
        execution: &str,
        patch: Map<String, Value>,
    ) -> Result<()> {
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: w.c.id.clone(),
                impact: w.cn.id.clone(),
                op: IMPACT_UPDATED,
                flow: w.flow.clone(),
                execution: execution.to_string(),
                post: Some(r.clone()),
                patch,
                ..Default::default()
            }],
        )
        .await?;
        self.edited_again(ctx, tx, w, execution).await
    }

    /// emitAddLink and emitRemoveLink record the edit of the links of a draft.
    pub(crate) async fn emit_add_link(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &mut Work,
        d: &Draft,
        link: &DraftLink,
        execution: &str,
    ) -> Result<()> {
        self.emit_updated(
            ctx,
            tx,
            w,
            &d.r#ref(),
            execution,
            one("addLink", link_patch(link)),
        )
        .await
    }

    pub(crate) async fn emit_remove_link(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &mut Work,
        d: &Draft,
        l: &DraftLink,
        execution: &str,
    ) -> Result<()> {
        let rm = json!({"id": l.id.to_string(), "type": l.r#type, "to": l.to.to_string(), "toId": l.to.id.to_string()});
        self.emit_updated(ctx, tx, w, &d.r#ref(), execution, one("removeLink", rm))
            .await
    }

    /// linkWork finds the draft of the flow holding a link, and the link: by its id, or by the id of the stored link of
    /// the version the draft was checked out from, which the checkout copied.
    pub(crate) async fn link_work(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
    ) -> Result<(Work, Draft, DraftLink)> {
        let mut w = self.work_on(ctx, tx, id, flow).await?;
        let rows = self.drafts(ctx, tx, id).await?;
        let stored = match tx.link(ctx, link).await {
            Ok(l) => Some(l),
            Err(e) if e.is_not_found() => None,
            Err(e) => return Err(e),
        };
        let mut other = false;
        for d in &rows {
            let mut found: Option<DraftLink> = d.link(link).cloned();
            if found.is_none()
                && let Some(s) = &stored
                && d.base.as_ref() == Some(&s.from)
            {
                for c in &d.links {
                    if c.r#type == s.r#type && c.to.id == s.to.id {
                        found = Some(c.clone());
                    }
                }
            }
            let Some(found) = found else {
                continue;
            };
            if d.flow != w.flow {
                other = true;
                continue;
            }
            self.impact(ctx, tx, &mut w, &d.impact).await?;
            let d = self.working(ctx, tx, &w).await?;
            return Ok((w, d, found));
        }
        if other {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "link {link} is a link of a draft of another flow of change {id}: check the node out on this one first"
                ),
            ));
        }
        Err(Error::wrap(
            ErrorKind::Conflict,
            format!("link {link} is not a link of a draft of change {id}"),
        ))
    }

    /// ImpactLinkUpdate replaces the properties of an outgoing link of a draft.
    ///
    /// pending goap:operation impact.link.update doer
    pub async fn impact_link_update(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        props: Map<String, Value>,
        flow: &str,
        execution: &str,
    ) -> Result<Link> {
        self.allow(ctx, id, operations::IMPACT_LINK_UPDATE).await?;
        let mut tx = self.begin(ctx).await?;
        let (mut w, d, cur) = self.link_work(ctx, &mut tx, id, link, flow).await?;
        w.ix.check_link_attributes(&cur.r#type, &props)?;
        let l = Link {
            id: cur.id.clone(),
            r#type: cur.r#type.clone(),
            from: d.r#ref(),
            to: cur.to.clone(),
            properties: props.clone(),
            change_id: id.clone(),
        };
        let up = json!({"id": cur.id.to_string(), "type": cur.r#type, "toId": cur.to.id.to_string(), "props": Value::Object(props)});
        self.emit_updated(
            ctx,
            &mut tx,
            &mut w,
            &d.r#ref(),
            execution,
            one("updateLink", up),
        )
        .await?;
        tx.commit(ctx).await?;
        Ok(l)
    }

    /// ImpactLinkDelete removes an outgoing link of a draft: removing a child is a modification of its parent (ADR
    /// 0024 §4).
    ///
    /// pending goap:operation impact.link.delete doer
    pub async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        link: &LinkID,
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        self.allow(ctx, id, operations::IMPACT_LINK_DELETE).await?;
        let mut tx = self.begin(ctx).await?;
        let (mut w, d, l) = self.link_work(ctx, &mut tx, id, link, flow).await?;
        self.emit_remove_link(ctx, &mut tx, &mut w, &d, &l, execution)
            .await?;
        tx.commit(ctx).await
    }

    /// checkAccepted is the gate of an accepted review (ADR 0079): the draft of the impact the flow sees must satisfy
    /// what a version does (checkDraft: validators, required links, link attributes) and the review gate of the merge
    /// and split origins. Nothing is written: the version is written when the change lands. It does nothing when the
    /// impact has no draft.
    pub(crate) async fn check_accepted(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        flow: &str,
        impact: &ChangeImpactID,
    ) -> Result<()> {
        let mut w = self.work_on(ctx, tx, id, flow).await?;
        w.select_impact(impact)?;
        let Some(d) = self.seen_draft(ctx, tx, &w.c, &w.flow, impact).await? else {
            return Ok(());
        };
        self.check_draft_of(ctx, tx, &w.c, &w.flow, &w.ix, &w.seen, &w.vcn, &d)
            .await
    }

    /// checkDraftOf is the check of an accepted review on one draft, writing nothing: the origins gate over the change
    /// impacts the flow sees, then what a version must satisfy. It serves the review (checkAccepted) and the
    /// validation the views carry (ImpactValidation).
    #[allow(clippy::too_many_arguments)]
    pub(crate) async fn check_draft_of(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        c: &Change,
        flow: &str,
        ix: &TypeIndex,
        seen: &[ChangeImpact],
        cn: &ChangeImpact,
        d: &Draft,
    ) -> Result<()> {
        self.check_origins(seen, cn, Some(d))?;
        let r = self.new_draft_reader(ctx, tx, c, flow, seen).await?;
        let mut resolver = crate::subchange::DraftResolver { tx, reader: &r };
        self.check_draft(ctx, ix, c, d, &mut resolver).await
    }

    /// editedAgain sends a decided review (accepted or rejected) back to proposed when the content of its draft
    /// changes (ADR 0077, 0102): an edit of its properties, owner or links, a cancel. What was reviewed is no longer
    /// what is there: a rejected impact is reworked this way, or withdrawn. A move of its lifecycle is no edit: it is
    /// judged by its transition (authorization, guard) when it is taken, and leaves the review as it is (a guard may
    /// read it). It follows the event that changed the draft.
    pub(crate) async fn edited_again(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &mut Work,
        execution: &str,
    ) -> Result<()> {
        if w.vcn.review == REVIEW_PROPOSED {
            return Ok(());
        }
        // accepted or rejected, the draft it was reviewed on is gone: the modification is proposed again (ADR 0102)
        let r = Review {
            status: REVIEW_PROPOSED,
            by: self.caller_of(ctx),
            at: self.now(),
            comment: "edited again".into(),
            flow: w.flow.clone(),
            execution: execution.to_string(),
            ..Default::default()
        };
        self.emit(
            ctx,
            tx,
            vec![ImpactEvent {
                change: w.c.id.clone(),
                impact: w.cn.id.clone(),
                op: IMPACT_REVIEWED,
                flow: w.flow.clone(),
                execution: execution.to_string(),
                by: r.by.clone(),
                review: Some(r),
                ..Default::default()
            }],
        )
        .await?;
        w.vcn.review = REVIEW_PROPOSED;
        Ok(())
    }

    /// ImpactNodeCancel drops the draft of a change impact on the flow: the impact goes back to what the flow saw
    /// before (the draft of a parent flow), or to none. A creation cancelled leaves no node: the change impact is
    /// removed.
    ///
    /// pending goap:operation impact.cancel doer
    pub async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_CANCEL).await?;
        let mut tx = self.begin(ctx).await?;
        let mut w = self.work_on(ctx, &mut tx, id, flow).await?;
        self.impact(ctx, &mut tx, &mut w, impact).await?;
        let rows = self.drafts(ctx, &mut tx, id).await?;
        let Some(d) = own_draft(&rows, impact, &w.flow) else {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!("change impact {impact} ({}) is not checked out", w.cn.key),
            ));
        };
        // what the flow falls back to: the draft of its parent flows
        let mut back: Option<NodeRef> = None;
        if w.flow.is_empty() && w.cn.landed.is_some() {
            // committed already (ADR 0102): the impact falls back to the version the change wrote
            back = w.cn.landed.clone();
        } else if !w.flow.is_empty() {
            let ids = flow_ids(&flow_chain(&w.c, &w.flow));
            let mut order: Vec<String> = ids.into_iter().skip(1).collect();
            order.push(String::new());
            for f in &order {
                if own_draft(&rows, impact, f).is_some() {
                    back = Some(d.r#ref());
                    break;
                }
            }
        }
        self.emit(
            ctx,
            &mut tx,
            vec![ImpactEvent {
                change: id.clone(),
                impact: impact.clone(),
                op: IMPACT_CANCELLED,
                flow: w.flow.clone(),
                execution: execution.to_string(),
                post: back.clone(),
                ..Default::default()
            }],
        )
        .await?;
        if w.cn.intent == INTENT_MODIFIED || w.cn.landed.is_some() {
            // a creation cancelled is gone with its impact
            self.edited_again(ctx, &mut tx, &mut w, execution).await?;
        }
        let mut cn = w.vcn.clone();
        cn.post = back;
        tx.commit(ctx).await?;
        Ok(cn)
    }

    /// ImpactNodeTransition moves a node along its lifecycle in a change (ADR 0076, 0079): it sets the state of the
    /// draft of the node, which a node with none is checked out for first; the transitioned event carries the patch
    /// {"state": {"from", "to"}} and the properties its actions set. The transition is authorized before the
    /// transaction (Graph::authorizer reads the access graph), then its requirements, its guard (which sees the
    /// change, the impact of the node and its draft: a transition may require a review) and its actions run on the
    /// draft.
    ///
    /// pending goap:operation impact.transition doer
    pub async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeTransition,
    ) -> Result<ChangeImpact> {
        self.allow(ctx, id, operations::IMPACT_TRANSITION).await?;
        if input.to.is_empty() {
            return Err(invalidf("a transition names the state it goes to"));
        }
        let mut authorized: Option<PendingMove> = None;
        if let Some(a) = &self.authorizer {
            // a pass that only reads what the authorization needs, rolled back (the stores are not reentrant)
            let mut tx = self.begin(ctx).await?;
            let (_, cur, t) = self.transition_of(ctx, &mut tx, id, &input).await?;
            tx.rollback().await?;
            a.authorize(ctx, &cur, &t).await?;
            authorized = Some(PendingMove { node: cur, t });
        }
        let mut tx = self.begin(ctx).await?;
        let (mut w, cur, t) = self.transition_of(ctx, &mut tx, id, &input).await?;
        if let Some(m) = &authorized
            && (PendingMove {
                node: cur.clone(),
                t: t.clone(),
            })
            .key()
                != m.key()
        {
            return Err(Error::wrap(
                ErrorKind::Conflict,
                format!(
                    "{} moved while the transition was authorized: take it again",
                    cur.key
                ),
            ));
        }
        let rows = self.drafts(ctx, &mut tx, id).await?;
        if own_draft(&rows, &w.cn.id, &w.flow).is_none() {
            // the node is checked out first, the draft the move acts on
            self.checkout_tx(ctx, &mut tx, &w, &input.execution).await?;
            w = self
                .resolve(
                    ctx,
                    &mut tx,
                    id,
                    Target {
                        impact: w.cn.id.clone(),
                        flow: input.flow.clone(),
                        execution: input.execution.clone(),
                        ..Default::default()
                    },
                )
                .await?;
        }
        let d = self.working(ctx, &mut tx, &w).await?;
        let mut moved = d.clone();
        let from = d.state.clone();
        moved.state = t.to.clone();
        let n = draft_node(&w.c, &moved);
        let mut reader = self
            .new_draft_reader(ctx, &mut tx, &w.c, &w.flow, &w.seen)
            .await?;
        reader.by_node.insert(moved.node.clone(), moved.clone());
        let out = reader.out_links(ctx, &mut tx, &moved.r#ref()).await?;
        let view_target = self.view_target(ctx, &mut tx, &w).await?;
        let a = Applier {
            g: self,
            change: w.c.clone(),
            ix: w.ix.clone(),
            branch: w.branch.clone(),
            target: view_target,
            impact: Some(w.vcn.clone()),
            draft: Some(moved),
            drafts: Some(reader),
            ..Applier::new(self)
        };
        let children = a.check_transition(ctx, &mut tx, &n, &out, &t).await?;
        let (set, unset) = a.run_actions(ctx, &n, &t, &children).await?;
        let mut patch = one("state", json!({"from": from, "to": t.to}));
        if !set.is_empty() {
            patch.insert("props".into(), Value::Object(set));
        }
        if !unset.is_empty() {
            patch.insert(
                "unset".into(),
                Value::Array(unset.into_iter().map(Value::String).collect()),
            );
        }
        let r = d.r#ref();
        self.emit(
            ctx,
            &mut tx,
            vec![ImpactEvent {
                change: w.c.id.clone(),
                impact: w.cn.id.clone(),
                op: IMPACT_TRANSITIONED,
                flow: w.flow.clone(),
                execution: input.execution.clone(),
                post: Some(r.clone()),
                patch,
                ..Default::default()
            }],
        )
        .await?;
        tx.commit(ctx).await?;
        Ok(w.seen_as(Some(&r)))
    }

    /// transitionOf resolves what a transition starts from: the change impact (declared when the change holds none on
    /// the node), the node as the flow sees it (its draft, else the stored version it starts from) with the state it
    /// leaves, and the transition of the lifecycle.
    pub(crate) async fn transition_of(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        input: &NodeTransition,
    ) -> Result<(Work, Node, Transition)> {
        let w = self
            .resolve(ctx, tx, id, checkout_target(&input.checkout))
            .await?;
        let cur = match self.seen_draft(ctx, tx, &w.c, &w.flow, &w.cn.id).await? {
            Some(d) => Some(draft_node(&w.c, &d)),
            None => w.base(ctx, tx).await?,
        };
        let Some(cur) = cur else {
            return Err(invalidf(format!(
                "change impact {} has no version to move",
                w.cn.id
            )));
        };
        let Some(lc) = w.ix.lifecycle_of(&cur.r#type) else {
            return Err(invalidf(format!(
                "node type {} has no lifecycle: state {:?}",
                cur.r#type, input.to
            )));
        };
        if lc.state(&input.to).is_none() {
            return Err(invalidf(format!(
                "{} has no state {:?}",
                cur.r#type, input.to
            )));
        }
        let from = if cur.state.is_empty() {
            lc.initial.clone()
        } else {
            cur.state.clone()
        };
        if from == input.to {
            return Err(invalidf(format!(
                "{} ({}) is already {}",
                cur.key, cur.r#type, input.to
            )));
        }
        let Some(t) = lc.move_to(&from, &input.to).cloned() else {
            return Err(invalidf(format!(
                "{} ({}) cannot go from {from} to {}",
                cur.key, cur.r#type, input.to
            )));
        };
        let mut at = cur;
        at.state = from;
        Ok((w, at, t))
    }

    /// viewTarget is the state of the namespace as an operation on a flow sees it: the head of the change branch
    /// (else the reference baseline). The nodes the change holds a draft of are read through their drafts
    /// (draftReader).
    pub(crate) async fn view_target(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        w: &Work,
    ) -> Result<BTreeMap<NodeID, Version>> {
        let b = match branch_head(ctx, tx, &w.c.namespace, branch_of(&w.c.branch)).await {
            Err(e) if e.is_not_found() => tx.baseline(ctx, &w.c.baseline_id).await?,
            Ok(b) if b.id.is_empty() => tx.baseline(ctx, &w.c.baseline_id).await?,
            r => r?,
        };
        Ok(b.nodes)
    }

    /// WithdrawImpact takes a change impact out of the change, at any time while the change is open (ADR 0076 §5b, ADR
    /// 0102): its draft, if any, is dropped (a creation leaves no node behind) and the impact leaves the list, whatever
    /// its review; the impacts that come with it (via: the sources of a merge or a split its parent realizes) leave with
    /// it. One the change committed already is reverted on its branch by the next commit (the node goes back to the
    /// version the branch was forked from). A rejected impact is withdrawn this way, or reworked (editedAgain). Only
    /// the flow that declared an impact withdraws it.
    ///
    /// pending goap:operation impact.withdraw doer
    pub async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impact: &ChangeImpactID,
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        self.allow(ctx, id, operations::IMPACT_WITHDRAW).await?;
        let mut tx = self.begin(ctx).await?;
        self.withdraw_tx(
            ctx,
            &mut tx,
            id,
            std::slice::from_ref(impact),
            flow,
            execution,
        )
        .await?;
        tx.commit(ctx).await
    }

    /// WithdrawImpacts takes several change impacts out of the change at once, in one transaction: all of them or
    /// none. Each is withdrawn as WithdrawImpact does (the impacts via one of them leave with it); an impact named
    /// twice, or leaving already with another one of the list, is withdrawn once.
    pub async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        impacts: &[ChangeImpactID],
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        if impacts.is_empty() {
            return Err(invalidf("no change impact to withdraw"));
        }
        self.allow(ctx, id, operations::IMPACT_WITHDRAW).await?;
        let mut tx = self.begin(ctx).await?;
        self.withdraw_tx(ctx, &mut tx, id, impacts, flow, execution)
            .await?;
        tx.commit(ctx).await
    }

    /// withdrawTx emits the withdrawal of the impacts and of the impacts via them, each once.
    pub(crate) async fn withdraw_tx(
        &self,
        ctx: &Ctx,
        tx: &mut dyn Tx,
        id: &ChangeID,
        impacts: &[ChangeImpactID],
        flow: &str,
        execution: &str,
    ) -> Result<()> {
        let mut w = self.work_on(ctx, tx, id, flow).await?;
        let mut gone: HashSet<ChangeImpactID> = HashSet::new();
        let mut events = Vec::new();
        let mut withdraw = |impact: &ChangeImpactID, flow: &str| {
            if gone.insert(impact.clone()) {
                events.push(ImpactEvent {
                    change: id.clone(),
                    impact: impact.clone(),
                    op: IMPACT_WITHDRAWN,
                    flow: flow.to_string(),
                    execution: execution.to_string(),
                    ..Default::default()
                });
            }
        };
        let mut done: HashSet<ChangeImpactID> = HashSet::new();
        for impact in impacts {
            if done.contains(impact) {
                continue;
            }
            self.impact(ctx, tx, &mut w, impact).await?;
            if w.cn.flow != w.flow {
                return Err(Error::wrap(
                    ErrorKind::Conflict,
                    format!("change impact {impact} was declared on another flow"),
                ));
            }
            for o in &w.c.nodes {
                if &o.via == impact {
                    done.insert(o.id.clone());
                    withdraw(&o.id, &w.flow);
                }
            }
            done.insert(impact.clone());
            withdraw(impact, &w.flow);
        }
        self.emit(ctx, tx, events).await
    }
}
