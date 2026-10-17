//! Port of `handler.go`: the graph exposed over Connect (`graph.v1.GraphService`).

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use goap_access as access;
use goap_authz::{Authorizer, Request as AuthzRequest, Resource, check, principal_from};
use goap_connect::{ConnectError, Ctx, Request, Response, ServerStream, async_trait};
use goap_domain::{
    BaselineID, CHANGE_APPLIED, CHANGE_COMMITTED, Change, ChangeEvent, ChangeID, ChangeImpactID,
    ChangeStatus, DATA_CRITICALITY, ImpactVerdict, ItemID, LinkID, LogFilter, NodeID, NodeRef,
    REVIEW_ACCEPTED, REVIEW_REJECTED, ReviewBatch, STRUCTURE_PROJECT, TagFilter, TagID,
};
use goap_events as events;
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{
    ChangePatch, ChangesFilter, Commit, Error, ErrorKind, EventSink, Graph, GraphEvent, LinkQuery,
    LinkWrite, MergeInput, MergeRequest, NewBranch, NewChange, NodeCheckout, NodeCreate, NodeName,
    NodeQuery, NodeTransition, NodeUpdate, OpenDecisionRequest, OpenFlowRequest, OpenOptionRequest,
    Resolution, RuleRequest, SnapshotImport, SplitInput, TransitionRequest,
};
use goap_identity::Extractor;
use goap_journal as journal;
use goap_pbconv as pbconv;
use goap_rpcerr::to_connect;
use serde::Serialize;
use serde_json::{Map, Value};

use crate::boot::BoxError;
use crate::demo::DemoFn;
use crate::personal::visible_to;

/// Publisher publishes the events of the changes (NATS in services, the event hub in process); the engine's
/// publisher has the same shape, so one publisher serves both.
#[async_trait]
pub trait Publisher: Send + Sync {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError>;
}

#[async_trait]
impl Publisher for goap_platform::Events {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError> {
        goap_platform::Events::publish(self, ctx, subject, v)
            .await
            .map_err(|e| Box::new(e) as BoxError)
    }
}

/// The graph events published through a [`Publisher`] (Republish).
struct PublisherSink<'a>(&'a dyn Publisher);

#[async_trait]
impl EventSink for PublisherSink<'_> {
    async fn publish(&self, ctx: &Ctx, subject: &str, ev: &GraphEvent) -> goap_graph::Result<()> {
        let v = serde_json::to_value(ev).map_err(Error::other)?;
        self.0
            .publish(ctx, subject, &v)
            .await
            .map_err(crate::boot::boxed_error)
    }
}

/// Handler implements `goap_gen::graph::v1::GraphService`.
#[derive(Clone)]
pub struct Handler {
    pub graph: Arc<Graph>,
    pub events: Option<Arc<dyn Publisher>>,
    /// Authz gates the creation of a node in a change (resource "object": ImpactNodeCreate, the successors of a merge
    /// or split, a graph import), the tags and the reading of prompts. Unset grants everything. The other impact
    /// operations are not gated by it.
    pub authz: Option<Arc<dyn Authorizer>>,
    /// Floor gates changes to User and Policy nodes (resource "policy", action "write"). It must not depend on the
    /// policies themselves, so that no policy can lock the administrators out; unset falls back to authz.
    pub floor: Option<Arc<dyn Authorizer>>,
    /// Identity extracts the caller from request headers (set by the gateway).
    pub identity: Extractor,
    /// Demo loads the demo data of the composition (platform/devseed), reporting whether it wrote anything and the key
    /// of the demo project; unset: the deployment ships no demo (LoadDemoData is unimplemented).
    pub demo: Option<DemoFn>,
    /// Types gives the type catalogue the OKF export names and lays out its nodes with (Go: a type assertion of
    /// `Graph.Types()` to the typecat catalogue, which the graph's catalogue trait does not offer in Rust); unset: the
    /// defaults of the OKF writer.
    pub types: Option<OkfTypes>,
}

/// OkfTypes returns the type catalogue in force, if any (see [`Handler::types`]).
pub type OkfTypes = Arc<dyn Fn() -> Option<Arc<goap_typecat::Catalog>> + Send + Sync>;

impl Handler {
    /// A handler of the graph with no event bus, no authorizer and no demo.
    pub fn new(graph: Arc<Graph>) -> Handler {
        Handler {
            graph,
            events: None,
            authz: None,
            floor: None,
            identity: Extractor::default(),
            demo: None,
            types: None,
        }
    }
}

type Rpc<T> = Result<Response<T>, ConnectError>;

/// res answers a message, or the Connect form of the error.
pub(crate) fn res<T, E>(r: Result<T, E>) -> Rpc<T>
where
    E: Into<Box<dyn std::error::Error + Send + Sync + 'static>>,
{
    r.map(Response::new).map_err(to_connect)
}

fn ge(e: Error) -> ConnectError {
    to_connect(e)
}

fn cid(s: &str) -> ChangeID {
    ChangeID::from(s)
}

/// Caller names the principal of a request, recorded on the events of the change impacts (Graph::caller, ADR 0029).
pub fn caller(ctx: &Ctx) -> String {
    principal_from(ctx).subject
}

/// The change without its items and impacts, as the events carry it.
fn bare(mut c: Change) -> Change {
    c.items.clear();
    c.nodes.clear();
    c
}

fn status_of(s: &str) -> ChangeStatus {
    ChangeStatus::from(s)
}

/// The resolutions of a merge.
fn resolutions_of(rs: &HashMap<String, pb::Resolution>) -> BTreeMap<NodeID, Resolution> {
    rs.iter()
        .map(|(id, r)| {
            (
                NodeID::from(id.as_str()),
                Resolution {
                    props: pbconv::to_map(r.props.as_ref()),
                    skip: r.skip,
                },
            )
        })
        .collect()
}

fn link_writes(ls: &[pb::NodeLinkWrite]) -> Vec<LinkWrite> {
    ls.iter()
        .map(|l| LinkWrite {
            r#type: l.r#type.clone(),
            to: pbconv::ref_from_pb(l.to.as_ref()),
            properties: pbconv::to_map(l.props.as_ref()),
        })
        .collect()
}

fn name_of(n: Option<&pb::NodeName>) -> NodeName {
    let n = n.cloned().unwrap_or_default();
    NodeName {
        impact: ChangeImpactID::from(n.change_impact_id.as_str()),
        node: NodeID::from(n.node_id.as_str()),
        key: n.key,
    }
}

fn create_spec(
    s: Option<&pb::NodeCreateSpec>,
    flow: &str,
    execution: &str,
    by: &str,
) -> NodeCreate {
    let s = s.cloned().unwrap_or_default();
    NodeCreate {
        key: s.key,
        r#type: s.r#type,
        properties: pbconv::to_map(s.props.as_ref()),
        owner: s.owner,
        rationale: s.rationale,
        links: link_writes(&s.links),
        flow: flow.to_string(),
        execution: execution.to_string(),
        produced_by: by.to_string(),
        ..Default::default()
    }
}

fn item_ids(ss: &[String]) -> Vec<ItemID> {
    ss.iter().map(|s| ItemID::from(s.as_str())).collect()
}

/// The time of an export, as Go formats "20060102-150405".
fn stamp(t: &chrono::DateTime<chrono::Utc>) -> String {
    t.format("%Y%m%d-%H%M%S").to_string()
}

/// validCriticality refuses a criticality of the data of a change that is not C1, C2 or C3 (ADR 0075 §3).
fn valid_criticality(data: &Map<String, Value>) -> Result<(), String> {
    let Some(v) = data.get(DATA_CRITICALITY) else {
        return Ok(());
    };
    let s = v.as_str().unwrap_or_default();
    if !goap_criticality::valid(&goap_criticality::Level::new(s)) {
        let shown = match v {
            Value::String(s) => s.clone(),
            other => other.to_string(),
        };
        return Err(format!("criticality {shown}: C1, C2 or C3"));
    }
    Ok(())
}

impl Handler {
    /// publish publishes a value on the event bus, best effort.
    pub(crate) async fn publish(&self, ctx: &Ctx, subject: &str, v: &impl Serialize) {
        if let Some(p) = &self.events
            && let Ok(v) = serde_json::to_value(v)
        {
            let _ = p.publish(ctx, subject, &v).await;
        }
    }

    /// publishChange publishes an event of a change: what it sets off is attributed to its end user (ADR 0114).
    pub(crate) async fn publish_change(&self, ctx: &Ctx, subject: &str, mut ev: ChangeEvent) {
        if ev.end_user.is_empty() {
            ev.end_user = principal_from(ctx).end_user().to_string();
        }
        self.publish(ctx, subject, &ev).await;
    }

    /// checkPrompts is Ok when the caller may read the prompts of the model calls of the change.
    async fn check_prompts(&self, ctx: &Ctx, c: &Change) -> Result<(), goap_authz::Error> {
        check(
            ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: principal_from(ctx),
                action: "inspect".into(),
                resource: Resource {
                    r#type: "prompt".into(),
                    id: c.id.to_string(),
                    name: c.title.clone(),
                    org: c.owner_org.clone(),
                    project_id: c.project_id.clone(),
                    ..Default::default()
                },
            },
        )
        .await
    }

    /// projectOf resolves the project a change is created in (ADR 0091): the one the request names, else the caller's
    /// active project, else the root project (an empty claim means the root). The graph refuses a change with none,
    /// so the edge resolves it.
    pub(crate) fn project_of(&self, ctx: &Ctx, requested: &str) -> String {
        if !requested.is_empty() {
            return requested.to_string();
        }
        let p = principal_from(ctx).project;
        if !p.is_empty() {
            return p;
        }
        self.graph.structure(STRUCTURE_PROJECT).root
    }

    /// The gate of the access nodes: the floor, else the authorizer.
    fn gate(&self) -> Option<&dyn Authorizer> {
        self.floor.as_deref().or(self.authz.as_deref())
    }

    /// The check of the access permission ("policy":"write") against the floor (ADR 0020).
    async fn check_access(&self, ctx: &Ctx) -> Result<(), goap_authz::Error> {
        let who = principal_from(ctx);
        let org = who.org.clone();
        check(
            ctx,
            self.gate(),
            &AuthzRequest {
                subject: who,
                action: "write".into(),
                resource: Resource {
                    r#type: access::RESOURCE_POLICY.into(),
                    org,
                    ..Default::default()
                },
            },
        )
        .await
    }

    /// gateAccess applies to a change impact the gate of the access nodes (`adminOnly` node types, ADR 0068): they
    /// need the access permission ("policy":"write"), checked against the floor (ADR 0020) — administrator-only, same
    /// as every other organisation-namespace write.
    pub(crate) async fn gate_access(&self, ctx: &Ctx, typ: &str) -> Result<(), ConnectError> {
        if self.graph.admin_only_type(typ) {
            self.check_access(ctx).await.map_err(to_connect)?;
        }
        Ok(())
    }

    /// gateFunc is the gate of the access nodes as a function of a node type, for the operations that modify nodes
    /// they discover themselves (MergeInput::gate): the access permission is asked once, up front.
    async fn gate_func(&self, ctx: &Ctx) -> goap_graphapi::Gate {
        let refused = self.check_access(ctx).await.err().map(to_connect);
        let g = self.graph.clone();
        Arc::new(move |typ: &str| match &refused {
            Some(e) if g.admin_only_type(typ) => Err(Error::other(e.clone())),
            _ => Ok(()),
        })
    }

    /// checkCreate is the ABAC check of the creation of a node of a type in a change.
    pub(crate) async fn check_create(
        &self,
        ctx: &Ctx,
        c: &Change,
        typ: &str,
    ) -> Result<(), ConnectError> {
        let who = principal_from(ctx);
        let owner = who.subject.clone();
        check(
            ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: who,
                action: "create".into(),
                resource: Resource {
                    r#type: "object".into(),
                    name: typ.to_string(),
                    namespace: c.namespace.clone(),
                    org: c.owner_org.clone(),
                    owner,
                    project_id: c.project_id.clone(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)?;
        self.gate_access(ctx, typ).await
    }

    /// impactType is the node type of a change impact of a change ("" when the change holds none under the id).
    async fn impact_type(
        &self,
        ctx: &Ctx,
        change: &str,
        impact: &str,
    ) -> Result<String, ConnectError> {
        let list = self
            .graph
            .list_change_impacts(ctx, &cid(change))
            .await
            .map_err(ge)?;
        Ok(list
            .into_iter()
            .find(|cn| cn.id.as_str() == impact)
            .map(|cn| cn.r#type)
            .unwrap_or_default())
    }

    /// gateImpact applies to an operation on a change impact the gate of the access nodes (gate_access): the impact
    /// names the node by its id, or the call names the node.
    async fn gate_impact(
        &self,
        ctx: &Ctx,
        change: &str,
        impact: &str,
        node: &str,
    ) -> Result<(), ConnectError> {
        let mut typ = self.impact_type(ctx, change, impact).await?;
        if typ.is_empty()
            && !node.is_empty()
            && let Ok(n) = self
                .graph
                .node(
                    ctx,
                    &NodeRef {
                        id: NodeID::from(node),
                        ..Default::default()
                    },
                )
                .await
        {
            typ = n.r#type;
        }
        self.gate_access(ctx, &typ).await
    }

    /// gateLink applies the gate of the access nodes to an operation on a link of a draft of the change: the type of
    /// its source.
    async fn gate_link(&self, ctx: &Ctx, change: &str, link: &str) -> Result<(), ConnectError> {
        let typ = self
            .graph
            .link_source_type(ctx, &cid(change), &LinkID::from(link))
            .await
            .map_err(ge)?;
        self.gate_access(ctx, &typ).await
    }

    /// The type of the node an edit or an impact names: the type of its pre-version when it has one.
    async fn type_of(&self, ctx: &Ctx, typ: &str, pre: Option<&NodeRef>) -> String {
        if let Some(p) = pre
            && let Ok(n) = self.graph.node(ctx, p).await
        {
            return n.r#type;
        }
        typ.to_string()
    }

    /// checkCriticality guards the criticality of the header of a change (ADR 0075 §3): the requester may raise it,
    /// lowering it (below the level it holds, the default of its methodology included) asks the permission
    /// change:lower-criticality, which administrators hold. A platform service acting by itself is not asked.
    async fn check_criticality(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        data: &Map<String, Value>,
    ) -> Result<(), ConnectError> {
        if let Err(m) = valid_criticality(data) {
            return Err(ge(Error::wrap(ErrorKind::Invalid, m)));
        }
        let Some(v) = data.get(DATA_CRITICALITY) else {
            return Ok(());
        };
        let c = self.graph.change(ctx, id).await.map_err(ge)?;
        let to = goap_criticality::Level::new(v.as_str().unwrap_or_default());
        let who = principal_from(ctx);
        if !goap_criticality::lowers(&goap_criticality::of(&c.data), &to)
            || self.authz.is_none()
            || who.is_system()
        {
            return Ok(());
        }
        check(
            ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: who,
                action: "lower-criticality".into(),
                resource: Resource {
                    r#type: "change".into(),
                    id: c.id.to_string(),
                    name: c.title.clone(),
                    org: c.owner_org.clone(),
                    project_id: c.project_id.clone(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)
    }

    /// reviews is the use case of the review object (ADR 0080) over the graph.
    fn reviews(&self) -> goap_review::Service {
        goap_review::Service::new(Arc::new(ReviewPort(self.graph.clone())))
    }

    /// reviewer is the actor of a review call: the principal of the request, an administrator acting on the reviews
    /// of others.
    async fn reviewer(&self, ctx: &Ctx) -> goap_review::Actor {
        let mut a = goap_review::actor_of(ctx);
        if !a.admin {
            a.admin = self.check_access(ctx).await.is_ok();
        }
        a
    }

    fn ctx_of(&self, ctx: &Ctx, headers: &http::HeaderMap) -> Ctx {
        self.identity.context(ctx, headers)
    }
}

/// The review use case's port over the graph.
struct ReviewPort(Arc<Graph>);

#[async_trait]
impl goap_review::Port for ReviewPort {
    async fn change(&self, ctx: &Ctx, id: &ChangeID) -> Result<Change, goap_review::Error> {
        self.0
            .change(ctx, id)
            .await
            .map_err(goap_review::Error::port)
    }
    async fn add_items(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        items: Vec<goap_domain::ChangeItem>,
    ) -> Result<Vec<goap_domain::ChangeItem>, goap_review::Error> {
        self.0
            .add_items(ctx, id, items)
            .await
            .map_err(goap_review::Error::port)
    }
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        b: ReviewBatch,
    ) -> Result<Vec<goap_domain::ChangeImpact>, goap_review::Error> {
        self.0
            .impact_node_review_batch(ctx, id, b)
            .await
            .map_err(goap_review::Error::port)
    }
}

/// okfChunk is the size of the chunks of an OKF archive sent on the stream.
const OKF_CHUNK: usize = 64 << 10;

#[async_trait]
impl GraphService for Handler {
    async fn get_node(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetNodeRequest>,
    ) -> Rpc<pb::GetNodeResponse> {
        let m = r.msg;
        if !m.change_id.is_empty() {
            // as the change sees it (ADR 0079): the draft it holds of the node, else the stored version
            let change = cid(&m.change_id);
            let v = if !m.key.is_empty() {
                self.graph
                    .change_node_view_by_key(ctx, &change, &m.flow, &m.namespace, &m.key)
                    .await
            } else {
                self.graph
                    .change_node_view(
                        ctx,
                        &change,
                        &m.flow,
                        &pbconv::ref_from_pb(m.r#ref.as_ref()),
                    )
                    .await
            };
            return res(v.map(|v| pb::GetNodeResponse {
                view: Some(pbconv::view_to_pb(&v)),
            }));
        }
        let mut r = pbconv::ref_from_pb(m.r#ref.as_ref());
        if !m.key.is_empty() {
            r = self
                .graph
                .node_by_key(ctx, &m.namespace, &m.key)
                .await
                .map_err(ge)?
                .r#ref();
        }
        res(self.graph.view(ctx, &r).await.map(|v| pb::GetNodeResponse {
            view: Some(pbconv::view_to_pb(&v)),
        }))
    }

    async fn tag_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::TagChangeRequest>,
    ) -> Rpc<pb::TagChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let c = self
            .graph
            .change(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        let who = principal_from(ctx);
        check(
            ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: who.clone(),
                action: "create".into(),
                resource: Resource {
                    r#type: "tag".into(),
                    namespace: c.namespace.clone(),
                    org: who.org.clone(),
                    owner: who.subject.clone(),
                    project_id: c.project_id.clone(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)?;
        res(self
            .graph
            .tag_change(ctx, &c.id, &r.msg.name, &who.subject)
            .await
            .map(|t| pb::TagChangeResponse {
                tag: Some(pbconv::tag_to_pb(&t)),
            }))
    }

    async fn list_tags(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListTagsRequest>,
    ) -> Rpc<pb::ListTagsResponse> {
        let ts = self
            .graph
            .tags(
                ctx,
                &TagFilter {
                    namespace: r.msg.namespace.clone(),
                    name: r.msg.name.clone(),
                    change: cid(&r.msg.change_id),
                },
            )
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListTagsResponse {
            tags: ts.iter().map(pbconv::tag_to_pb).collect(),
        }))
    }

    async fn delete_tag(
        &self,
        ctx: &Ctx,
        r: Request<pb::DeleteTagRequest>,
    ) -> Rpc<pb::DeleteTagResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let who = principal_from(ctx);
        check(
            ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: who.clone(),
                action: "delete".into(),
                resource: Resource {
                    r#type: "tag".into(),
                    org: who.org.clone(),
                    owner: who.subject.clone(),
                    project_id: who.project.clone(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)?;
        res(self
            .graph
            .delete_tag(ctx, &TagID::from(r.msg.id.as_str()))
            .await
            .map(|_| pb::DeleteTagResponse {}))
    }

    async fn list_baselines(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListBaselinesRequest>,
    ) -> Rpc<pb::ListBaselinesResponse> {
        let bs = self
            .graph
            .baselines(ctx, &r.msg.namespace)
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListBaselinesResponse {
            baselines: bs.iter().map(pbconv::baseline_to_pb).collect(),
        }))
    }

    async fn get_baseline_graph(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetBaselineGraphRequest>,
    ) -> Rpc<pb::GetBaselineGraphResponse> {
        let id = BaselineID::from(r.msg.id.as_str());
        let b = self.graph.baseline(ctx, &id).await.map_err(ge)?;
        let (nodes, links) = self.graph.baseline_graph(ctx, &id).await.map_err(ge)?;
        let suspects = self.graph.suspect_links(ctx, &id).await.map_err(ge)?;
        Ok(Response::new(pb::GetBaselineGraphResponse {
            baseline: Some(pbconv::baseline_to_pb(&b)),
            nodes: pbconv::nodes_to_pb(&nodes),
            links: pbconv::links_to_pb(&links),
            suspect_links: pbconv::links_to_pb(&suspects),
        }))
    }

    async fn list_change_log(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListChangeLogRequest>,
    ) -> Rpc<pb::ListChangeLogResponse> {
        let m = &r.msg;
        let mut f = LogFilter {
            change: cid(&m.change_id),
            types: m.types.clone(),
            processes: m.process_ids.clone(),
            execution: m.execution.clone(),
            after_seq: m.after_seq,
            limit: i64::from(m.limit),
            ..Default::default()
        };
        if !m.flows.is_empty() {
            f.flows = Some(
                m.flows
                    .iter()
                    .map(|fl| {
                        if fl == "main" {
                            String::new()
                        } else {
                            fl.clone()
                        }
                    })
                    .collect(),
            );
        }
        let ctx = &self.ctx_of(ctx, &r.headers);
        let (entries, counts) = self.graph.change_log(ctx, &f).await.map_err(ge)?;
        // the prompts of the model calls (stream model) are read with their own permission, prompt:inspect on the
        // project of the change: asking for them without it is refused, a log read without types leaves them out
        let model_prefix = format!("{}.", journal::STREAM_MODEL);
        let mut hide = false;
        if f.match_type(&format!("{}.call", journal::STREAM_MODEL)) {
            if f.change.is_empty() {
                // a log read by processes only names its types
                return Err(ge(Error::wrap(
                    ErrorKind::Invalid,
                    "a log read without a change names its types",
                )));
            }
            let c = self.graph.change(ctx, &f.change).await.map_err(ge)?;
            if let Err(e) = self.check_prompts(ctx, &c).await {
                if f.types.iter().any(|t| t.starts_with(&model_prefix)) {
                    return Err(to_connect(e));
                }
                hide = true;
            }
        }
        let mut out = pb::ListChangeLogResponse::default();
        for (t, n) in counts {
            if hide && t.starts_with(&model_prefix) {
                continue;
            }
            out.counts.insert(t, n as i32);
        }
        for e in &entries {
            if hide && e.stream() == journal::STREAM_MODEL {
                continue;
            }
            out.entries.push(pbconv::log_entry_to_pb(e));
        }
        Ok(Response::new(out))
    }

    async fn export_change_provenance(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExportChangeProvenanceRequest>,
    ) -> Rpc<pb::ExportChangeProvenanceResponse> {
        let id = cid(&r.msg.change_id);
        let c = self.graph.change(ctx, &id).await.map_err(ge)?;
        let (mut entries, _) = self
            .graph
            .change_log(
                ctx,
                &LogFilter {
                    change: id.clone(),
                    ..Default::default()
                },
            )
            .await
            .map_err(ge)?;
        if self.check_prompts(ctx, &c).await.is_err() {
            // the export carries the prompts only for whoever may read them
            entries.retain(|e| e.stream() != journal::STREAM_MODEL);
        }
        let doc = goap_prov::export(&c, &entries).map_err(to_connect)?;
        Ok(Response::new(pb::ExportChangeProvenanceResponse {
            document: doc.marshal_indent("", "  "),
            filename: format!("change-{}.prov.jsonld", c.id),
            media_type: goap_prov::MEDIA_TYPE.into(),
        }))
    }

    async fn export_graph(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExportGraphRequest>,
    ) -> Rpc<pb::ExportGraphResponse> {
        let s = self
            .graph
            .export_snapshot(
                ctx,
                &r.msg.namespace,
                &r.msg.branch,
                &BaselineID::from(r.msg.baseline_id.as_str()),
            )
            .await
            .map_err(ge)?;
        let v = serde_json::to_value(&s).map_err(|e| ConnectError::internal(e.to_string()))?;
        Ok(Response::new(pb::ExportGraphResponse {
            document: goap_prov::marshal_indent(&v, "", "  "),
            filename: format!("graph-{}-{}.json", s.namespace, stamp(&s.exported_at)),
            media_type: "application/json".into(),
            nodes: s.nodes.len() as i32,
        }))
    }

    /// ExportGraphOkf streams the state of a namespace at a baseline as an OKF tree, one Markdown file per node, in a
    /// zip archive (ADR 0111): a first message with the name of the archive, then its bytes in chunks. The caller is
    /// identified here, as a streaming call passes none of the unary interceptors.
    async fn export_graph_okf(
        &self,
        ctx: &Ctx,
        r: Request<pb::ExportGraphOkfRequest>,
    ) -> Rpc<ServerStream<pb::ExportGraphOkfResponse>> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        if principal_from(ctx).anonymous() {
            return Err(ConnectError::unauthenticated(
                "the caller is not identified",
            ));
        }
        let s = self
            .graph
            .export_snapshot(
                ctx,
                &r.msg.namespace,
                &r.msg.branch,
                &BaselineID::from(r.msg.baseline_id.as_str()),
            )
            .await
            .map_err(ge)?;
        let name = format!("graph-{}-{}.okf.zip", s.namespace, stamp(&s.exported_at));
        let first = pb::ExportGraphOkfResponse {
            filename: name,
            media_type: "application/zip".into(),
            nodes: s.nodes.len() as i32,
            ..Default::default()
        };
        let archive = crate::okf_export::render(self.types.as_ref().and_then(|f| f()), &s)
            .map_err(|e| ConnectError::internal(e.to_string()))?;
        let mut msgs = vec![Ok(first)];
        for chunk in archive.chunks(OKF_CHUNK) {
            msgs.push(Ok(pb::ExportGraphOkfResponse {
                chunk: chunk.to_vec(),
                ..Default::default()
            }));
        }
        Ok(Response::new(goap_connect::server_stream(
            futures::stream::iter(msgs),
        )))
    }

    /// ImportGraph imports a JSON snapshot as one new change of the caller (ADR 0104): every node type it writes is
    /// authorized like a creation in that change (ABAC object:create on its project, the gate of the access nodes).
    async fn import_graph(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImportGraphRequest>,
    ) -> Rpc<pb::ImportGraphResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let s = goap_graph::parse_snapshot(m.document.as_bytes()).map_err(ge)?;
        let owner = self.resolve_owner(ctx, &m.owner_org).await?;
        let project_id = self.project_of(ctx, &m.project_id);
        // the gate of the graph is synchronous: every type the snapshot writes is checked up front, on the change the
        // import creates (its namespace, unit and project)
        let probe = Change {
            namespace: s.namespace.clone(),
            owner_org: owner.clone(),
            project_id: project_id.clone(),
            ..Default::default()
        };
        let mut refused: BTreeMap<String, ConnectError> = BTreeMap::new();
        let mut types: Vec<String> = s.nodes.iter().map(|n| n.r#type.clone()).collect();
        types.sort();
        types.dedup();
        for t in types {
            if let Err(e) = self.check_create(ctx, &probe, &t).await {
                refused.insert(t, e);
            }
        }
        let gate: goap_graph::SnapshotGate =
            Arc::new(move |_c: &Change, typ: &str| match refused.get(typ) {
                Some(e) => Err(Error::other(e.clone())),
                None => Ok(()),
            });
        let out = Box::pin(self.graph.import_snapshot(
            ctx,
            SnapshotImport {
                snapshot: s,
                title: m.title,
                intent: m.intent,
                methodology: m.methodology,
                owner_org: owner,
                project_id,
                by: principal_from(ctx).subject,
                gate: Some(gate),
            },
        ))
        .await
        .map_err(ge)?;
        let mut resp = pb::ImportGraphResponse {
            created: out.created as i32,
            updated: out.updated as i32,
            unchanged: out.unchanged as i32,
            warnings: out.warnings,
            ..Default::default()
        };
        if !out.change.is_empty() {
            let c = self.graph.change(ctx, &out.change).await.map_err(ge)?;
            self.publish_change(
                ctx,
                &events::change_subject(c.id.as_str(), events::CHANGE_CREATED),
                ChangeEvent {
                    r#type: events::CHANGE_CREATED.into(),
                    change: c.clone(),
                    ..Default::default()
                },
            )
            .await;
            resp.change = Some(pbconv::change_to_pb(&c));
        }
        Ok(Response::new(resp))
    }

    async fn list_change_events(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListChangeEventsRequest>,
    ) -> Rpc<pb::ListChangeEventsResponse> {
        let evs = self
            .graph
            .change_events(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListChangeEventsResponse {
            events: evs.iter().map(pbconv::impact_event_to_pb).collect(),
        }))
    }

    async fn list_baseline_nodes(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListBaselineNodesRequest>,
    ) -> Rpc<pb::ListBaselineNodesResponse> {
        let m = r.msg;
        let id = BaselineID::from(m.baseline_id.as_str());
        let b = self.graph.baseline(ctx, &id).await.map_err(ge)?;
        let page = self
            .graph
            .baseline_nodes(
                ctx,
                &id,
                NodeQuery {
                    r#type: m.r#type,
                    text: m.query,
                    include_deleted: m.include_deleted,
                    offset: m.offset as usize,
                    limit: m.limit as usize,
                },
            )
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListBaselineNodesResponse {
            baseline: Some(pbconv::baseline_to_pb(&b)),
            nodes: pbconv::nodes_to_pb(&page.nodes),
            total: page.total as i32,
            types: page
                .types
                .iter()
                .map(|t| pb::TypeCount {
                    r#type: t.r#type.clone(),
                    count: t.count as i32,
                })
                .collect(),
        }))
    }

    async fn list_baseline_links(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListBaselineLinksRequest>,
    ) -> Rpc<pb::ListBaselineLinksResponse> {
        let m = r.msg;
        let id = BaselineID::from(m.baseline_id.as_str());
        let b = self.graph.baseline(ctx, &id).await.map_err(ge)?;
        let page = self
            .graph
            .baseline_links(
                ctx,
                &id,
                LinkQuery {
                    r#type: m.r#type,
                    text: m.query,
                    offset: m.offset as usize,
                    limit: m.limit as usize,
                },
            )
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListBaselineLinksResponse {
            baseline: Some(pbconv::baseline_to_pb(&b)),
            links: pbconv::links_to_pb(&page.links),
            total: page.total as i32,
            types: page
                .types
                .iter()
                .map(|t| pb::TypeCount {
                    r#type: t.r#type.clone(),
                    count: t.count as i32,
                })
                .collect(),
        }))
    }

    async fn get_node_neighbourhood(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetNodeNeighbourhoodRequest>,
    ) -> Rpc<pb::GetNodeNeighbourhoodResponse> {
        let nb = self
            .graph
            .node_neighbourhood(
                ctx,
                &BaselineID::from(r.msg.baseline_id.as_str()),
                &NodeID::from(r.msg.node_id.as_str()),
            )
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetNodeNeighbourhoodResponse {
            node: Some(pbconv::node_to_pb(&nb.node)),
            nodes: pbconv::nodes_to_pb(&nb.nodes),
            links: pbconv::links_to_pb(&nb.links),
            suspect_link_ids: nb.suspect.iter().map(|l| l.to_string()).collect(),
        }))
    }

    /// IsAdminOnlyType tells whether a node type is written by platform administrators only (ADR 0068).
    async fn is_admin_only_type(
        &self,
        _ctx: &Ctx,
        r: Request<pb::IsAdminOnlyTypeRequest>,
    ) -> Rpc<pb::IsAdminOnlyTypeResponse> {
        Ok(Response::new(pb::IsAdminOnlyTypeResponse {
            admin_only: self.graph.admin_only_type(&r.msg.r#type),
        }))
    }

    /// ListItemTypes lists the item types a change may hold: the ones the domains in force declare (ADR 0128), open
    /// to any identified caller like the types of the catalogue.
    async fn list_item_types(
        &self,
        _ctx: &Ctx,
        _r: Request<pb::ListItemTypesRequest>,
    ) -> Rpc<pb::ListItemTypesResponse> {
        let types = self.graph.item_types();
        let refs: Vec<&goap_typecat::ItemType> = types.iter().collect();
        Ok(Response::new(pbconv::item_types_to_pb(&refs)))
    }

    async fn get_structures(
        &self,
        _ctx: &Ctx,
        _r: Request<pb::GetStructuresRequest>,
    ) -> Rpc<pb::GetStructuresResponse> {
        Ok(Response::new(pbconv::structures_to_pb(
            &self.graph.structures(),
        )))
    }

    async fn list_namespaces(
        &self,
        ctx: &Ctx,
        _r: Request<pb::ListNamespacesRequest>,
    ) -> Rpc<pb::ListNamespacesResponse> {
        res(self
            .graph
            .namespaces(ctx)
            .await
            .map(|namespaces| pb::ListNamespacesResponse { namespaces }))
    }

    async fn load_demo_data(
        &self,
        ctx: &Ctx,
        r: Request<pb::LoadDemoDataRequest>,
    ) -> Rpc<pb::LoadDemoDataResponse> {
        self.load_demo_data_rpc(ctx, r).await
    }

    async fn create_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::CreateChangeRequest>,
    ) -> Rpc<pb::CreateChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let owner = self.resolve_owner(ctx, &m.owner_org).await?;
        if !m.parent_id.is_empty() {
            // a personal change is never split, and nothing is split off a personal change
            if let Ok(parent) = self.graph.change(ctx, &cid(&m.parent_id)).await
                && (access::is_personal(&parent) || access::is_personal_unit(&owner))
            {
                return Err(ConnectError::failed_precondition(
                    "a personal change has no sub-changes",
                ));
            }
        }
        let project_id = self.project_of(ctx, &m.project_id);
        let data = pbconv::to_map(m.data.as_ref());
        if let Err(e) = valid_criticality(&data) {
            return Err(ConnectError::invalid_argument(e));
        }
        let c = self
            .graph
            .create_change(
                ctx,
                NewChange {
                    parent_id: cid(&m.parent_id),
                    owner_org: owner,
                    own_branch: m.own_branch,
                    namespace: m.namespace,
                    title: m.title,
                    intent: m.intent,
                    methodology: m.methodology,
                    methodology_version: m.methodology_version,
                    baseline_id: BaselineID::from(m.baseline_id.as_str()),
                    branch: m.branch,
                    data,
                    project_id,
                    ..Default::default()
                },
            )
            .await
            .map_err(ge)?;
        self.publish_change(
            ctx,
            &events::change_subject(c.id.as_str(), events::CHANGE_CREATED),
            ChangeEvent {
                r#type: events::CHANGE_CREATED.into(),
                change: c.clone(),
                ..Default::default()
            },
        )
        .await;
        Ok(Response::new(pb::CreateChangeResponse {
            change: Some(pbconv::change_to_pb(&c)),
        }))
    }

    async fn get_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeRequest>,
    ) -> Rpc<pb::GetChangeResponse> {
        res(self
            .graph
            .change_checked(ctx, &cid(&r.msg.id))
            .await
            .map(|c| pb::GetChangeResponse {
                change: Some(pbconv::change_to_pb(&c)),
            }))
    }

    async fn list_changes(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListChangesRequest>,
    ) -> Rpc<pb::ListChangesResponse> {
        let f = ChangesFilter {
            namespace: r.msg.namespace.clone(),
            owner_org: r.msg.owner_org.clone(),
            status: r.msg.status.iter().map(|s| status_of(s)).collect(),
        };
        let ctx = &self.ctx_of(ctx, &r.headers);
        let cs = self.graph.list_changes(ctx, &f).await.map_err(ge)?;
        Ok(Response::new(pb::ListChangesResponse {
            changes: cs
                .iter()
                .filter(|c| visible_to(ctx, c))
                .map(pbconv::change_to_pb)
                .collect(),
        }))
    }

    async fn get_change_impacts(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeImpactsRequest>,
    ) -> Rpc<pb::GetChangeImpactsResponse> {
        let refs = self
            .graph
            .change_impacts(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetChangeImpactsResponse {
            nodes: refs.iter().map(pbconv::ref_to_pb).collect(),
        }))
    }

    async fn list_node_changes(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListNodeChangesRequest>,
    ) -> Rpc<pb::ListNodeChangesResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let cs = self
            .graph
            .node_changes(ctx, &NodeID::from(r.msg.node_id.as_str()))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListNodeChangesResponse {
            changes: cs
                .iter()
                .filter(|c| visible_to(ctx, c))
                .map(pbconv::change_to_pb)
                .collect(),
        }))
    }

    async fn update_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::UpdateChangeRequest>,
    ) -> Rpc<pb::UpdateChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let id = cid(&m.id);
        let p = ChangePatch {
            title: m.title,
            intent: m.intent,
            goal: m.goal,
            data: pbconv::to_map(m.data.as_ref()),
            status: m.status.as_deref().map(status_of),
        };
        self.check_criticality(ctx, &id, &p.data).await?;
        let before = self.graph.change(ctx, &id).await.map_err(ge)?;
        let c = self.graph.update_change(ctx, &id, &p).await.map_err(ge)?;
        let fields = goap_domain::edited_fields(&before, &c);
        if !fields.is_empty() {
            // what a reactive activity of the methodology may want to look at again (ADR 0110)
            self.publish_change(
                ctx,
                &events::change_subject(c.id.as_str(), events::CHANGE_EDITED),
                ChangeEvent {
                    r#type: events::CHANGE_EDITED.into(),
                    change: bare(c.clone()),
                    fields,
                    ..Default::default()
                },
            )
            .await;
        }
        Ok(Response::new(pb::UpdateChangeResponse {
            change: Some(pbconv::change_to_pb(&c)),
        }))
    }

    /// MoveChange moves a root change, with its open sub-changes, to another project (ADR 0091). The graph's
    /// ProjectMoveGate authorizes it on both projects and checks the methodology; a personal change of someone else is
    /// refused by the PersonalScope interceptor (change_id).
    async fn move_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::MoveChangeRequest>,
    ) -> Rpc<pb::MoveChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let c = self
            .graph
            .move_change(ctx, &cid(&r.msg.change_id), &r.msg.project_id)
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::MoveChangeResponse {
            change: Some(pbconv::change_to_pb(&bare(c))),
        }))
    }

    /// AttachChange makes a root change a sub-change of a change integrating it (ADR 0106) and publishes
    /// change.attached.
    async fn attach_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::AttachChangeRequest>,
    ) -> Rpc<pb::AttachChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let c = bare(
            self.graph
                .attach_change(ctx, &cid(&r.msg.change_id), &cid(&r.msg.parent_id))
                .await
                .map_err(ge)?,
        );
        self.publish_change(
            ctx,
            &events::change_subject(c.id.as_str(), events::CHANGE_ATTACHED),
            ChangeEvent {
                r#type: events::CHANGE_ATTACHED.into(),
                change: c.clone(),
                ..Default::default()
            },
        )
        .await;
        Ok(Response::new(pb::AttachChangeResponse {
            change: Some(pbconv::change_to_pb(&c)),
        }))
    }

    /// DetachChange gives a sub-change back as a root change (ADR 0106) and publishes change.detached.
    async fn detach_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::DetachChangeRequest>,
    ) -> Rpc<pb::DetachChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let id = cid(&r.msg.change_id);
        let before = self.graph.change(ctx, &id).await.map_err(ge)?;
        let c = bare(self.graph.detach_change(ctx, &id).await.map_err(ge)?);
        // the event names the change it left, for the integrator listening to its integrated changes
        let mut ev = c.clone();
        ev.parent_id = before.parent_id;
        self.publish_change(
            ctx,
            &events::change_subject(c.id.as_str(), events::CHANGE_DETACHED),
            ChangeEvent {
                r#type: events::CHANGE_DETACHED.into(),
                change: ev,
                ..Default::default()
            },
        )
        .await;
        Ok(Response::new(pb::DetachChangeResponse {
            change: Some(pbconv::change_to_pb(&c)),
        }))
    }

    /// SetChangeDependencies replaces the changes a change depends on (ADR 0108); the graph publishes change.updated.
    async fn set_change_dependencies(
        &self,
        ctx: &Ctx,
        r: Request<pb::SetChangeDependenciesRequest>,
    ) -> Rpc<pb::SetChangeDependenciesResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let c = self
            .graph
            .set_change_dependencies(
                ctx,
                &cid(&r.msg.change_id),
                &pbconv::change_ids_from_pb(&r.msg.depends_on),
            )
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::SetChangeDependenciesResponse {
            change: Some(pbconv::change_to_pb(&bare(c))),
        }))
    }

    /// UpgradeChange moves an open change to another version of its methodology (ADR 0113).
    async fn upgrade_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::UpgradeChangeRequest>,
    ) -> Rpc<pb::UpgradeChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let c = self
            .graph
            .upgrade_change(ctx, &cid(&r.msg.change_id), &r.msg.version)
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::UpgradeChangeResponse {
            change: Some(pbconv::change_to_pb(&bare(c))),
        }))
    }

    /// GetChangeDependencies reads the dependencies of a change, its dependents and the merge order of its
    /// sub-changes (ADR 0108).
    async fn get_change_dependencies(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeDependenciesRequest>,
    ) -> Rpc<pb::GetChangeDependenciesResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let d = self
            .graph
            .change_dependencies(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetChangeDependenciesResponse {
            depends_on: pbconv::dependencies_to_pb(&d.depends_on),
            dependents: pbconv::dependencies_to_pb(&d.dependents),
            order: pbconv::dependencies_to_pb(&d.order),
        }))
    }

    async fn add_items(
        &self,
        ctx: &Ctx,
        r: Request<pb::AddItemsRequest>,
    ) -> Rpc<pb::AddItemsResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let id = cid(&r.msg.change_id);
        let proposed = pbconv::items_from_pb(&r.msg.items);
        let items = self.graph.add_items(ctx, &id, proposed).await.map_err(ge)?;
        if let Ok(mut c) = self.graph.change(ctx, &id).await {
            c.items.clear();
            self.publish_change(
                ctx,
                &events::change_subject(&r.msg.change_id, events::CHANGE_ITEM_ADDED),
                ChangeEvent {
                    r#type: events::CHANGE_ITEM_ADDED.into(),
                    change: c,
                    items: items.clone(),
                    ..Default::default()
                },
            )
            .await;
        }
        Ok(Response::new(pb::AddItemsResponse {
            items: pbconv::items_to_pb(&items),
        }))
    }

    async fn propose_impact(
        &self,
        ctx: &Ctx,
        r: Request<pb::ProposeImpactRequest>,
    ) -> Rpc<pb::ProposeImpactResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let nodes = pbconv::change_impacts_from_pb(&r.msg.nodes);
        for cn in &nodes {
            let typ = self.type_of(ctx, &cn.r#type, cn.pre.as_ref()).await;
            self.gate_access(ctx, &typ).await?;
        }
        res(self
            .graph
            .propose_impact(ctx, &cid(&r.msg.change_id), nodes)
            .await
            .map(|out| pb::ProposeImpactResponse {
                nodes: pbconv::change_impacts_to_pb(&out),
            }))
    }

    async fn commit_edits(
        &self,
        ctx: &Ctx,
        r: Request<pb::CommitEditsRequest>,
    ) -> Rpc<pb::CommitEditsResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let edits = pbconv::edits_from_pb(&m.edits);
        for e in &edits {
            let typ = self.type_of(ctx, &e.r#type, e.pre.as_ref()).await;
            self.gate_access(ctx, &typ).await?;
        }
        let mut by = principal_from(ctx).subject;
        if by.is_empty() {
            by = "graphsvc".into();
        }
        let owner = self.resolve_owner(ctx, &m.owner_org).await?;
        let project_id = self.project_of(ctx, &m.project_id);
        let out = Box::pin(self.graph.commit(
            ctx,
            Commit {
                namespace: m.namespace,
                title: m.title,
                intent: m.intent,
                methodology: m.methodology,
                data: pbconv::to_map(m.data.as_ref()),
                baseline: BaselineID::from(m.baseline_id.as_str()),
                by,
                baseline_name: m.baseline_name,
                edits,
                owner_org: owner,
                project_id,
                ..Default::default()
            },
        ))
        .await
        .map_err(ge)?;
        if let Ok(c) = self.graph.change(ctx, &out.change).await {
            self.publish_change(
                ctx,
                &events::change_subject(out.change.as_str(), events::CHANGE_APPLIED),
                ChangeEvent {
                    r#type: events::CHANGE_APPLIED.into(),
                    change: bare(c),
                    baseline: Some(out.baseline.clone()),
                    ..Default::default()
                },
            )
            .await;
        }
        Ok(Response::new(pb::CommitEditsResponse {
            change_id: out.change.to_string(),
            baseline: Some(pbconv::baseline_to_pb(&out.baseline)),
        }))
    }

    /// ImpactNodeCreate creates a node in a change: ABAC object:create on the project of the change, and the gate of
    /// the access nodes for an adminOnly type.
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeCreateRequest>,
    ) -> Rpc<pb::ImpactNodeCreateResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let c = self
            .graph
            .change(ctx, &cid(&m.change_id))
            .await
            .map_err(ge)?;
        self.check_create(ctx, &c, &m.r#type).await?;
        let who = principal_from(ctx);
        res(self
            .graph
            .impact_node_create(
                ctx,
                &c.id,
                NodeCreate {
                    key: m.key,
                    r#type: m.r#type,
                    properties: pbconv::to_map(m.props.as_ref()),
                    owner: m.owner,
                    rationale: m.rationale,
                    links: link_writes(&m.links),
                    flow: m.flow,
                    execution: m.execution,
                    produced_by: who.subject,
                    ..Default::default()
                },
            )
            .await
            .map(|cn| pb::ImpactNodeCreateResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    /// ImpactNodeMerge merges nodes into a new one (ADR 0077): the creation of the new node is authorized like
    /// ImpactNodeCreate, and the gate of the access nodes stands in front of every node the merge modifies.
    async fn impact_node_merge(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeMergeRequest>,
    ) -> Rpc<pb::ImpactNodeRestructureResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let c = self
            .graph
            .change(ctx, &cid(&m.change_id))
            .await
            .map_err(ge)?;
        let into_type = m
            .into
            .as_ref()
            .map(|s| s.r#type.clone())
            .unwrap_or_default();
        self.check_create(ctx, &c, &into_type).await?;
        let by = principal_from(ctx).subject;
        let input = MergeInput {
            into: create_spec(m.into.as_ref(), &m.flow, &m.execution, &by),
            rationale: m.rationale,
            sources: m.sources.iter().map(|s| name_of(Some(s))).collect(),
            gate: Some(self.gate_func(ctx).await),
            flow: m.flow,
            execution: m.execution,
        };
        res(self
            .graph
            .impact_node_merge(ctx, &c.id, input)
            .await
            .map(|out| pbconv::restructured_to_pb(&out)))
    }

    /// ImpactNodeSplit splits a node into new ones (ADR 0077), authorized like ImpactNodeMerge.
    async fn impact_node_split(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeSplitRequest>,
    ) -> Rpc<pb::ImpactNodeRestructureResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let c = self
            .graph
            .change(ctx, &cid(&m.change_id))
            .await
            .map_err(ge)?;
        let by = principal_from(ctx).subject;
        let mut into = Vec::new();
        for s in &m.into {
            self.check_create(ctx, &c, &s.r#type).await?;
            into.push(create_spec(Some(s), &m.flow, &m.execution, &by));
        }
        let input = SplitInput {
            source: name_of(m.source.as_ref()),
            rationale: m.rationale,
            into,
            gate: Some(self.gate_func(ctx).await),
            flow: m.flow,
            execution: m.execution,
        };
        res(self
            .graph
            .impact_node_split(ctx, &c.id, input)
            .await
            .map(|out| pbconv::restructured_to_pb(&out)))
    }

    /// DerivedNodes answers which nodes derive from a node (ADR 0077).
    async fn derived_nodes(
        &self,
        ctx: &Ctx,
        r: Request<pb::DerivedNodesRequest>,
    ) -> Rpc<pb::DerivedNodesResponse> {
        res(self
            .graph
            .derived_nodes(ctx, &pbconv::ref_from_pb(r.msg.r#ref.as_ref()))
            .await
            .map(|ns| pb::DerivedNodesResponse {
                nodes: pbconv::nodes_to_pb(&ns),
            }))
    }

    async fn impact_node_checkout(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeCheckoutRequest>,
    ) -> Rpc<pb::ImpactNodeCheckoutResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, &m.node_id)
            .await?;
        res(self
            .graph
            .impact_node_checkout(
                ctx,
                &cid(&m.change_id),
                NodeCheckout {
                    impact: ChangeImpactID::from(m.change_impact_id.as_str()),
                    node: NodeID::from(m.node_id.as_str()),
                    rationale: m.rationale,
                    flow: m.flow,
                    execution: m.execution,
                    produced_by: principal_from(ctx).subject,
                    ..Default::default()
                },
            )
            .await
            .map(|cn| pb::ImpactNodeCheckoutResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    async fn impact_node_update(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeUpdateRequest>,
    ) -> Rpc<pb::ImpactNodeUpdateResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, "")
            .await?;
        res(self
            .graph
            .impact_node_update(
                ctx,
                &cid(&m.change_id),
                &ChangeImpactID::from(m.change_impact_id.as_str()),
                NodeUpdate {
                    properties: pbconv::to_map(m.props.as_ref()),
                    owner: m.owner,
                    flow: m.flow,
                    execution: m.execution,
                    ..Default::default()
                },
            )
            .await
            .map(|cn| pb::ImpactNodeUpdateResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    async fn impact_link_create(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactLinkCreateRequest>,
    ) -> Rpc<pb::ImpactLinkCreateResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, "")
            .await?;
        res(self
            .graph
            .impact_link_create(
                ctx,
                &cid(&m.change_id),
                &ChangeImpactID::from(m.change_impact_id.as_str()),
                LinkWrite {
                    r#type: m.r#type,
                    to: pbconv::ref_from_pb(m.to.as_ref()),
                    properties: pbconv::to_map(m.props.as_ref()),
                },
                &m.flow,
                &m.execution,
            )
            .await
            .map(|l| pb::ImpactLinkCreateResponse {
                link: Some(pbconv::link_to_pb(&l)),
            }))
    }

    async fn impact_link_update(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactLinkUpdateRequest>,
    ) -> Rpc<pb::ImpactLinkUpdateResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_link(ctx, &m.change_id, &m.link_id).await?;
        res(self
            .graph
            .impact_link_update(
                ctx,
                &cid(&m.change_id),
                &LinkID::from(m.link_id.as_str()),
                pbconv::to_map(m.props.as_ref()),
                &m.flow,
                &m.execution,
            )
            .await
            .map(|l| pb::ImpactLinkUpdateResponse {
                link: Some(pbconv::link_to_pb(&l)),
            }))
    }

    async fn impact_link_delete(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactLinkDeleteRequest>,
    ) -> Rpc<pb::ImpactLinkDeleteResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_link(ctx, &m.change_id, &m.link_id).await?;
        res(self
            .graph
            .impact_link_delete(
                ctx,
                &cid(&m.change_id),
                &LinkID::from(m.link_id.as_str()),
                &m.flow,
                &m.execution,
            )
            .await
            .map(|_| pb::ImpactLinkDeleteResponse {}))
    }

    async fn impact_node_transition(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeTransitionRequest>,
    ) -> Rpc<pb::ImpactNodeTransitionResponse> {
        // the transition is authorized for the caller
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, &m.node_id)
            .await?;
        res(self
            .graph
            .impact_node_transition(
                ctx,
                &cid(&m.change_id),
                NodeTransition {
                    checkout: NodeCheckout {
                        impact: ChangeImpactID::from(m.change_impact_id.as_str()),
                        node: NodeID::from(m.node_id.as_str()),
                        rationale: m.rationale,
                        flow: m.flow,
                        execution: m.execution,
                        produced_by: principal_from(ctx).subject,
                        ..Default::default()
                    },
                    to: m.state,
                },
            )
            .await
            .map(|cn| pb::ImpactNodeTransitionResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    async fn impact_node_cancel(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeCancelRequest>,
    ) -> Rpc<pb::ImpactNodeCancelResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, "")
            .await?;
        res(self
            .graph
            .impact_node_cancel(
                ctx,
                &cid(&m.change_id),
                &ChangeImpactID::from(m.change_impact_id.as_str()),
                &m.flow,
                &m.execution,
            )
            .await
            .map(|cn| pb::ImpactNodeCancelResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    async fn withdraw_impact(
        &self,
        ctx: &Ctx,
        r: Request<pb::WithdrawImpactRequest>,
    ) -> Rpc<pb::WithdrawImpactResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, "")
            .await?;
        res(self
            .graph
            .withdraw_impact(
                ctx,
                &cid(&m.change_id),
                &ChangeImpactID::from(m.change_impact_id.as_str()),
                &m.flow,
                &m.execution,
            )
            .await
            .map(|_| pb::WithdrawImpactResponse {}))
    }

    /// WithdrawImpacts takes several impacts out of the change in one transaction; each is gated like
    /// WithdrawImpact.
    async fn withdraw_impacts(
        &self,
        ctx: &Ctx,
        r: Request<pb::WithdrawImpactsRequest>,
    ) -> Rpc<pb::WithdrawImpactsResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let mut impacts = Vec::with_capacity(m.change_impact_ids.len());
        for id in &m.change_impact_ids {
            self.gate_impact(ctx, &m.change_id, id, "").await?;
            impacts.push(ChangeImpactID::from(id.as_str()));
        }
        res(self
            .graph
            .withdraw_impacts(ctx, &cid(&m.change_id), &impacts, &m.flow, &m.execution)
            .await
            .map(|_| pb::WithdrawImpactsResponse {}))
    }

    /// RebaseChange brings a sub-change up to date with its parent (ADR 0082), a root change with the head of its
    /// target branch (ADR 0101).
    async fn rebase_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::RebaseChangeRequest>,
    ) -> Rpc<pb::RebaseChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let rb = Box::pin(self.graph.rebase_change(ctx, &cid(&r.msg.change_id)))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::RebaseChangeResponse {
            parent_id: rb.parent.to_string(),
            branch: rb.branch,
            from: rb.from.to_string(),
            onto: rb.onto.to_string(),
            impacts: rb
                .impacts
                .into_iter()
                .map(|i| pb::RebasedImpact {
                    change_impact_id: i.impact.to_string(),
                    key: i.key,
                    changed: i.changed,
                    merged: i.merged,
                    conflicts: i.conflicts,
                })
                .collect(),
        }))
    }

    /// ImpactNodeResolve keeps the sub-change's values for the conflicts a rebase left on a change impact (ADR 0082
    /// §2).
    async fn impact_node_resolve(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeResolveRequest>,
    ) -> Rpc<pb::ImpactNodeResolveResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        self.gate_impact(ctx, &m.change_id, &m.change_impact_id, "")
            .await?;
        res(self
            .graph
            .impact_node_resolve(
                ctx,
                &cid(&m.change_id),
                &ChangeImpactID::from(m.change_impact_id.as_str()),
                &m.execution,
            )
            .await
            .map(|cn| pb::ImpactNodeResolveResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    /// GetRebaseState tells where a change stands against its parent or its target branch (ADR 0082, 0101).
    async fn get_rebase_state(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetRebaseStateRequest>,
    ) -> Rpc<pb::GetRebaseStateResponse> {
        let st = self
            .graph
            .rebase_state(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetRebaseStateResponse {
            parent_id: st.parent.to_string(),
            branch: st.branch,
            onto: st.onto.to_string(),
            behind_impact_ids: st.behind.iter().map(|id| id.to_string()).collect(),
            conflicts: st
                .conflicts
                .into_iter()
                .map(|c| pb::ImpactConflicts {
                    change_impact_id: c.impact.to_string(),
                    key: c.key,
                    conflicts: c.conflicts,
                })
                .collect(),
        }))
    }

    async fn get_commit_state(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetCommitStateRequest>,
    ) -> Rpc<pb::GetCommitStateResponse> {
        let st = self
            .graph
            .commit_state(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetCommitStateResponse {
            to_commit: st.to_commit,
            reverts: st.reverts as i32,
            unsettled: st.unsettled,
            commits: st.commits,
            can_commit: st.can_commit,
            can_merge: st.can_merge,
        }))
    }

    async fn impact_node_review(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeReviewRequest>,
    ) -> Rpc<pb::ImpactNodeReviewResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let status = if m.accept {
            REVIEW_ACCEPTED
        } else {
            REVIEW_REJECTED
        };
        res(self
            .graph
            .impact_node_review_on(
                ctx,
                &cid(&m.change_id),
                &m.flow,
                &m.execution,
                &ChangeImpactID::from(m.change_impact_id.as_str()),
                status,
                &principal_from(ctx).subject,
                &m.comment,
            )
            .await
            .map(|cn| pb::ImpactNodeReviewResponse {
                node: Some(pbconv::change_impact_to_pb(&cn)),
            }))
    }

    /// ImpactNodeReviewBatch applies reviews together (ADR 0080), the reviewer being the principal of the request.
    async fn impact_node_review_batch(
        &self,
        ctx: &Ctx,
        r: Request<pb::ImpactNodeReviewBatchRequest>,
    ) -> Rpc<pb::ImpactNodeReviewBatchResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let b = ReviewBatch {
            id: m.review_id,
            flow: m.flow,
            execution: m.execution,
            by: principal_from(ctx).subject,
            verdicts: m
                .verdicts
                .iter()
                .map(|v| ImpactVerdict {
                    impact: ChangeImpactID::from(v.change_impact_id.as_str()),
                    status: if v.accept {
                        REVIEW_ACCEPTED
                    } else {
                        REVIEW_REJECTED
                    },
                    comment: v.comment.clone(),
                })
                .collect(),
            item: m.item.as_ref().map(pbconv::item_from_pb),
        };
        res(self
            .graph
            .impact_node_review_batch(ctx, &cid(&m.change_id), b)
            .await
            .map(|out| pb::ImpactNodeReviewBatchResponse {
                nodes: out.iter().map(pbconv::change_impact_to_pb).collect(),
            }))
    }

    async fn review_open(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReviewOpenRequest>,
    ) -> Rpc<pb::ReviewResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let who = self.reviewer(ctx).await;
        res(self
            .reviews()
            .open(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.flow,
                &r.msg.comment,
                &who,
            )
            .await
            .map(|rv| pb::ReviewResponse {
                review: Some(pbconv::review_to_pb(&rv)),
            }))
    }

    async fn review_update(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReviewUpdateRequest>,
    ) -> Rpc<pb::ReviewResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let who = self.reviewer(ctx).await;
        res(self
            .reviews()
            .update(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.key,
                pbconv::review_edit_from_pb(&r.msg),
                &who,
            )
            .await
            .map(|rv| pb::ReviewResponse {
                review: Some(pbconv::review_to_pb(&rv)),
            }))
    }

    async fn review_submit(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReviewSubmitRequest>,
    ) -> Rpc<pb::ReviewResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let who = self.reviewer(ctx).await;
        res(self
            .reviews()
            .submit(ctx, &cid(&r.msg.change_id), &r.msg.key, &who)
            .await
            .map(|rv| pb::ReviewResponse {
                review: Some(pbconv::review_to_pb(&rv)),
            }))
    }

    async fn review_discard(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReviewDiscardRequest>,
    ) -> Rpc<pb::ReviewResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let who = self.reviewer(ctx).await;
        res(self
            .reviews()
            .discard(ctx, &cid(&r.msg.change_id), &r.msg.key, &who)
            .await
            .map(|rv| pb::ReviewResponse {
                review: Some(pbconv::review_to_pb(&rv)),
            }))
    }

    async fn reopen_change_impacts(
        &self,
        ctx: &Ctx,
        r: Request<pb::ReopenChangeImpactsRequest>,
    ) -> Rpc<pb::ReopenChangeImpactsResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let ids: Vec<ChangeImpactID> = r
            .msg
            .change_impact_ids
            .iter()
            .map(|id| ChangeImpactID::from(id.as_str()))
            .collect();
        res(self
            .graph
            .reopen_impacts(ctx, &cid(&r.msg.change_id), &ids, &r.msg.comment)
            .await
            .map(|done| pb::ReopenChangeImpactsResponse {
                reopened: done.iter().map(|id| id.to_string()).collect(),
            }))
    }

    async fn get_blackboard(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetBlackboardRequest>,
    ) -> Rpc<pb::GetBlackboardResponse> {
        let bb = self
            .graph
            .blackboard_in(ctx, &cid(&r.msg.change_id), &r.msg.flow)
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetBlackboardResponse {
            change: Some(pbconv::change_to_pb(&bb.change)),
            active_option: goap_domain::active_option_of(&bb),
            at: bb.at.as_ref().and_then(pbconv::time),
            options: goap_domain::options_of(&bb)
                .iter()
                .map(pbconv::flow_to_pb)
                .collect(),
            decision_points: goap_domain::decision_points_of(&bb)
                .iter()
                .map(pbconv::decision_point_to_pb)
                .collect(),
            nodes: bb.nodes.values().map(pbconv::view_to_pb).collect(),
            neighbors: bb.neighbors.values().map(pbconv::node_to_pb).collect(),
        }))
    }

    async fn apply_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::ApplyChangeRequest>,
    ) -> Rpc<pb::ApplyChangeResponse> {
        // lifecycle transitions are authorized for the caller
        let ctx = &self.ctx_of(ctx, &r.headers);
        let id = cid(&r.msg.change_id);
        let b = Box::pin(self.graph.apply(ctx, &id, &r.msg.baseline_name))
            .await
            .map_err(ge)?;
        // a merge waiting for a resolution leaves the change committed, not applied
        if let Ok(mut c) = self.graph.change(ctx, &id).await
            && c.status != CHANGE_COMMITTED
        {
            c.items.clear();
            self.publish_change(
                ctx,
                &events::change_subject(&r.msg.change_id, events::CHANGE_APPLIED),
                ChangeEvent {
                    r#type: events::CHANGE_APPLIED.into(),
                    change: c,
                    baseline: Some(b.clone()),
                    ..Default::default()
                },
            )
            .await;
        }
        Ok(Response::new(pb::ApplyChangeResponse {
            baseline: Some(pbconv::baseline_to_pb(&b)),
        }))
    }

    /// CommitChange is the first step of ApplyChange: the versions are written on the change's own branch, its
    /// integration (MergeChange) is a later, separate act. The change header write publishes change.updated by
    /// itself.
    async fn commit_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::CommitChangeRequest>,
    ) -> Rpc<pb::CommitChangeResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let id = cid(&r.msg.change_id);
        let b = Box::pin(self.graph.commit_change(ctx, &id, &r.msg.baseline_name))
            .await
            .map_err(ge)?;
        if let Ok(c) = self.graph.change(ctx, &id).await {
            self.publish_change(
                ctx,
                &events::change_subject(&r.msg.change_id, events::CHANGE_COMMITTED),
                ChangeEvent {
                    r#type: events::CHANGE_COMMITTED.into(),
                    change: bare(c),
                    baseline: Some(b.clone()),
                    ..Default::default()
                },
            )
            .await;
        }
        Ok(Response::new(pb::CommitChangeResponse {
            baseline: Some(pbconv::baseline_to_pb(&b)),
        }))
    }

    async fn transition_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::TransitionChangeRequest>,
    ) -> Rpc<pb::TransitionChangeResponse> {
        // the transition is authorized for the caller
        let ctx = &self.ctx_of(ctx, &r.headers);
        let mut c = Box::pin(self.graph.transition_change(
            ctx,
            &cid(&r.msg.change_id),
            TransitionRequest {
                transition: r.msg.transition.clone(),
                decision: r.msg.decision.clone(),
                by: principal_from(ctx).subject,
            },
        ))
        .await
        .map_err(ge)?;
        c.items.clear();
        self.publish_change(
            ctx,
            &events::change_subject(c.id.as_str(), events::CHANGE_TRANSITIONED),
            ChangeEvent {
                r#type: events::CHANGE_TRANSITIONED.into(),
                change: bare(c.clone()),
                ..Default::default()
            },
        )
        .await;
        Ok(Response::new(pb::TransitionChangeResponse {
            change: Some(pbconv::change_to_pb(&c)),
        }))
    }

    async fn delete_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::DeleteChangeRequest>,
    ) -> Rpc<pb::DeleteChangeResponse> {
        self.delete_change_rpc(ctx, r).await
    }

    async fn get_pending_settings(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetPendingSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        self.get_pending_settings_rpc(ctx, r).await
    }

    async fn stage_settings(
        &self,
        ctx: &Ctx,
        r: Request<pb::StageSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        self.stage_settings_rpc(ctx, r).await
    }

    async fn save_settings(
        &self,
        ctx: &Ctx,
        r: Request<pb::SaveSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        self.save_settings_rpc(ctx, r).await
    }

    async fn discard_settings(
        &self,
        ctx: &Ctx,
        r: Request<pb::DiscardSettingsRequest>,
    ) -> Rpc<pb::PendingSettingsResponse> {
        self.discard_settings_rpc(ctx, r).await
    }

    async fn create_branch(
        &self,
        ctx: &Ctx,
        r: Request<pb::CreateBranchRequest>,
    ) -> Rpc<pb::CreateBranchResponse> {
        let m = r.msg;
        res(self
            .graph
            .create_branch(
                ctx,
                NewBranch {
                    name: m.name,
                    namespace: m.namespace,
                    from: BaselineID::from(m.from_baseline.as_str()),
                    origin: m.origin,
                    description: m.description,
                },
            )
            .await
            .map(|b| pb::CreateBranchResponse {
                branch: Some(pbconv::branch_to_pb(&b)),
            }))
    }

    async fn list_branches(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListBranchesRequest>,
    ) -> Rpc<pb::ListBranchesResponse> {
        res(self
            .graph
            .branches(ctx, &r.msg.namespace)
            .await
            .map(|bs| pb::ListBranchesResponse {
                branches: bs.iter().map(pbconv::branch_to_pb).collect(),
            }))
    }

    async fn get_branch(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetBranchRequest>,
    ) -> Rpc<pb::GetBranchResponse> {
        let b = self
            .graph
            .branch(ctx, &r.msg.namespace, &r.msg.name)
            .await
            .map_err(ge)?;
        let head = self
            .graph
            .branch_head(ctx, &r.msg.namespace, &b.name)
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetBranchResponse {
            branch: Some(pbconv::branch_to_pb(&b)),
            head: Some(pbconv::baseline_to_pb(&head)),
        }))
    }

    async fn set_branch_status(
        &self,
        ctx: &Ctx,
        r: Request<pb::SetBranchStatusRequest>,
    ) -> Rpc<pb::SetBranchStatusResponse> {
        res(self
            .graph
            .set_branch_status(ctx, &r.msg.namespace, &r.msg.name, &r.msg.status)
            .await
            .map(|_| pb::SetBranchStatusResponse {}))
    }

    async fn set_branch_description(
        &self,
        ctx: &Ctx,
        r: Request<pb::SetBranchDescriptionRequest>,
    ) -> Rpc<pb::SetBranchDescriptionResponse> {
        res(self
            .graph
            .set_branch_description(ctx, &r.msg.namespace, &r.msg.name, &r.msg.description)
            .await
            .map(|_| pb::SetBranchDescriptionResponse {}))
    }

    async fn list_node_versions(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListNodeVersionsRequest>,
    ) -> Rpc<pb::ListNodeVersionsResponse> {
        res(self
            .graph
            .versions(ctx, &NodeID::from(r.msg.id.as_str()))
            .await
            .map(|vs| pb::ListNodeVersionsResponse {
                versions: pbconv::nodes_to_pb(&vs),
            }))
    }

    async fn plan_merge(
        &self,
        ctx: &Ctx,
        r: Request<pb::PlanMergeRequest>,
    ) -> Rpc<pb::PlanMergeResponse> {
        res(self
            .graph
            .plan_merge(ctx, &r.msg.namespace, &r.msg.from, &r.msg.into)
            .await
            .map(|p| pb::PlanMergeResponse {
                plan: Some(pbconv::merge_plan_to_pb(&p)),
            }))
    }

    async fn merge_branch(
        &self,
        ctx: &Ctx,
        r: Request<pb::MergeBranchRequest>,
    ) -> Rpc<pb::MergeBranchResponse> {
        let m = r.msg;
        let req = MergeRequest {
            from: m.from,
            into: m.into,
            title: m.title,
            namespace: m.namespace,
            project_id: self.project_of(ctx, ""),
            resolutions: resolutions_of(&m.resolutions),
            ..Default::default()
        };
        let out = Box::pin(self.graph.merge_branch(ctx, req))
            .await
            .map_err(ge)?;
        let mut c = out.change.clone();
        c.items.clear();
        self.publish_change(
            ctx,
            &events::change_subject(c.id.as_str(), events::CHANGE_APPLIED),
            ChangeEvent {
                r#type: events::CHANGE_APPLIED.into(),
                change: c,
                baseline: Some(out.baseline.clone()),
                ..Default::default()
            },
        )
        .await;
        Ok(Response::new(pb::MergeBranchResponse {
            change: Some(pbconv::change_to_pb(&out.change)),
            baseline: Some(pbconv::baseline_to_pb(&out.baseline)),
            plan: Some(pbconv::merge_plan_to_pb(&out.plan)),
        }))
    }

    async fn diff_baselines(
        &self,
        ctx: &Ctx,
        r: Request<pb::DiffBaselinesRequest>,
    ) -> Rpc<pb::DiffBaselinesResponse> {
        let ds = self
            .graph
            .diff_baselines(
                ctx,
                &BaselineID::from(r.msg.from.as_str()),
                &BaselineID::from(r.msg.to.as_str()),
            )
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::DiffBaselinesResponse {
            nodes: ds
                .iter()
                .map(|d| pb::BaselineDiff {
                    node: d.node.to_string(),
                    key: d.key.clone(),
                    r#type: d.r#type.clone(),
                    kind: d.kind.clone(),
                    from: d.from.as_ref().map(pbconv::node_to_pb),
                    to: d.to.as_ref().map(pbconv::node_to_pb),
                })
                .collect(),
        }))
    }

    async fn merge_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::MergeChangeRequest>,
    ) -> Rpc<pb::MergeChangeResponse> {
        let resolutions = resolutions_of(&r.msg.resolutions);
        // the merge asks the change's methodology for the caller (ADR 0102)
        let ctx = &self.ctx_of(ctx, &r.headers);
        let c = Box::pin(
            self.graph
                .integrate_change(ctx, &cid(&r.msg.change_id), &resolutions),
        )
        .await
        .map_err(ge)?;
        if c.status == CHANGE_APPLIED {
            // a merge waiting for a resolution leaves the change committed
            let mut ev = c.clone();
            ev.items.clear();
            self.publish_change(
                ctx,
                &events::change_subject(c.id.as_str(), events::CHANGE_APPLIED),
                ChangeEvent {
                    r#type: events::CHANGE_APPLIED.into(),
                    change: ev,
                    ..Default::default()
                },
            )
            .await;
        }
        let mut shown = c;
        if shown.status == CHANGE_APPLIED {
            shown.items.clear();
        }
        Ok(Response::new(pb::MergeChangeResponse {
            change: Some(pbconv::change_to_pb(&shown)),
        }))
    }

    async fn get_shared_nodes(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetSharedNodesRequest>,
    ) -> Rpc<pb::GetSharedNodesResponse> {
        let ns = self
            .graph
            .shared_nodes(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::GetSharedNodesResponse {
            nodes: ns
                .iter()
                .map(|n| pb::SharedNode {
                    node: Some(pbconv::ref_to_pb(&n.node)),
                    key: n.key.clone(),
                    changes: n.changes.iter().map(|c| c.to_string()).collect(),
                })
                .collect(),
        }))
    }

    async fn split_change(
        &self,
        ctx: &Ctx,
        r: Request<pb::SplitChangeRequest>,
    ) -> Rpc<pb::SplitChangeResponse> {
        let cs = self
            .graph
            .split_by_owner(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        let mut out = pb::SplitChangeResponse::default();
        for c in cs {
            self.publish_change(
                ctx,
                &events::change_subject(c.id.as_str(), events::CHANGE_CREATED),
                ChangeEvent {
                    r#type: events::CHANGE_CREATED.into(),
                    change: c.clone(),
                    ..Default::default()
                },
            )
            .await;
            out.changes.push(pbconv::change_to_pb(&c));
        }
        Ok(Response::new(out))
    }

    async fn list_sub_changes(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListSubChangesRequest>,
    ) -> Rpc<pb::ListSubChangesResponse> {
        let cs = self
            .graph
            .sub_changes(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        Ok(Response::new(pb::ListSubChangesResponse {
            changes: cs
                .into_iter()
                .map(|mut c| {
                    c.items.clear();
                    pbconv::change_to_pb(&c)
                })
                .collect(),
        }))
    }

    async fn open_flow(
        &self,
        ctx: &Ctx,
        r: Request<pb::OpenFlowRequest>,
    ) -> Rpc<pb::OpenFlowResponse> {
        let m = r.msg;
        let f = self
            .graph
            .open_flow(
                ctx,
                &cid(&m.change_id),
                OpenFlowRequest {
                    parent: m.parent,
                    fork_after: ItemID::from(m.fork_after.as_str()),
                    seeds: item_ids(&m.seeds),
                    stale_runs: m.stale_runs,
                    origin: pbconv::to_map(m.origin.as_ref()),
                    items: pbconv::items_from_pb(&m.items),
                },
            )
            .await
            .map_err(ge)?;
        self.publish(ctx, &format!("goap.change.{}.flow_opened", m.change_id), &f)
            .await;
        Ok(Response::new(pb::OpenFlowResponse {
            flow: Some(pbconv::flow_to_pb(&f)),
        }))
    }

    async fn adopt_flow(
        &self,
        ctx: &Ctx,
        r: Request<pb::AdoptFlowRequest>,
    ) -> Rpc<pb::AdoptFlowResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let f = Box::pin(self.graph.adopt_flow(
            ctx,
            &cid(&r.msg.change_id),
            &r.msg.flow,
            &principal_from(ctx).subject,
        ))
        .await
        .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.flow_adopted", r.msg.change_id),
            &f,
        )
        .await;
        Ok(Response::new(pb::AdoptFlowResponse {
            flow: Some(pbconv::flow_to_pb(&f)),
        }))
    }

    async fn discard_flow(
        &self,
        ctx: &Ctx,
        r: Request<pb::DiscardFlowRequest>,
    ) -> Rpc<pb::DiscardFlowResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let f = self
            .graph
            .discard_flow(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.flow,
                &principal_from(ctx).subject,
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.flow_discarded", r.msg.change_id),
            &f,
        )
        .await;
        Ok(Response::new(pb::DiscardFlowResponse {
            flow: Some(pbconv::flow_to_pb(&f)),
        }))
    }

    async fn list_flows(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListFlowsRequest>,
    ) -> Rpc<pb::ListFlowsResponse> {
        res(self
            .graph
            .flows(ctx, &cid(&r.msg.change_id))
            .await
            .map(|fs| pb::ListFlowsResponse {
                flows: fs.iter().map(pbconv::flow_to_pb).collect(),
            }))
    }

    async fn validate_board(
        &self,
        ctx: &Ctx,
        r: Request<pb::ValidateBoardRequest>,
    ) -> Rpc<pb::ValidateBoardResponse> {
        res(self
            .graph
            .validate_board(ctx, &cid(&r.msg.change_id), &r.msg.flow)
            .await
            .map(|is| pb::ValidateBoardResponse {
                issues: is.iter().map(pbconv::board_issue_to_pb).collect(),
            }))
    }

    /// AppendLog appends the entries of a use case (the execution journal, ADR 0011) to the logs of their changes;
    /// the graph refuses the streams it writes itself.
    async fn append_log(
        &self,
        ctx: &Ctx,
        r: Request<pb::AppendLogRequest>,
    ) -> Rpc<pb::AppendLogResponse> {
        res(self
            .graph
            .append_log(ctx, pbconv::log_entries_from_pb(&r.msg.entries))
            .await
            .map(|_| pb::AppendLogResponse {}))
    }

    /// ListExecutions reads the execution journal of a change and / or of processes as records.
    async fn list_executions(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListExecutionsRequest>,
    ) -> Rpc<pb::ListExecutionsResponse> {
        let rs = journal::read(
            ctx,
            self.graph.as_ref(),
            &journal::Filter {
                change_id: cid(&r.msg.change_id),
                process_ids: r.msg.process_ids.clone(),
            },
        )
        .await
        .map_err(|e| match e {
            journal::Error::Invalid(m) => {
                ge(Error::invalid(format!("{}\n{m}", goap_graphapi::INVALID)))
            }
            journal::Error::Log(g) => ge(g),
            other => to_connect(other),
        })?;
        Ok(Response::new(pb::ListExecutionsResponse {
            records: pbconv::executions_to_pb(&rs),
        }))
    }

    /// RepublishIndex publishes again the node and baseline events so that an index can be rebuilt (ADR 0026).
    async fn republish_index(
        &self,
        ctx: &Ctx,
        r: Request<pb::RepublishIndexRequest>,
    ) -> Rpc<pb::RepublishIndexResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        check(
            ctx,
            self.authz.as_deref(),
            &AuthzRequest {
                subject: principal_from(ctx),
                action: "admin".into(),
                resource: Resource {
                    r#type: "platform".into(),
                    ..Default::default()
                },
            },
        )
        .await
        .map_err(to_connect)?;
        let Some(p) = &self.events else {
            return Err(ConnectError::failed_precondition("no event bus"));
        };
        res(self
            .graph
            .republish(ctx, &PublisherSink(p.as_ref()))
            .await
            .map(|n| pb::RepublishIndexResponse { versions: n as i32 }))
    }

    // ---- Options (ADR 0009 §3, ADR 0032 §6) --------------------------------------------------------------------

    async fn open_option(
        &self,
        ctx: &Ctx,
        r: Request<pb::OpenOptionRequest>,
    ) -> Rpc<pb::OpenOptionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let f = self
            .graph
            .open_option(
                ctx,
                &cid(&m.change_id),
                OpenOptionRequest {
                    name: m.name,
                    hypothesis: m.hypothesis,
                    activate: m.activate,
                    by: principal_from(ctx).subject,
                    ..Default::default()
                },
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.option_opened", m.change_id),
            &f,
        )
        .await;
        Ok(Response::new(pb::OpenOptionResponse {
            option: Some(pbconv::flow_to_pb(&f)),
        }))
    }

    async fn activate_option(
        &self,
        ctx: &Ctx,
        r: Request<pb::ActivateOptionRequest>,
    ) -> Rpc<pb::ActivateOptionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let active = self
            .graph
            .activate_option(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.option,
                &principal_from(ctx).subject,
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.option_activated", r.msg.change_id),
            &serde_json::json!({ "active": active }),
        )
        .await;
        Ok(Response::new(pb::ActivateOptionResponse { active }))
    }

    async fn evaluate_option(
        &self,
        ctx: &Ctx,
        r: Request<pb::EvaluateOptionRequest>,
    ) -> Rpc<pb::EvaluateOptionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        res(self
            .graph
            .evaluate_option(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.option,
                &principal_from(ctx).subject,
                &r.msg.comment,
            )
            .await
            .map(|f| pb::EvaluateOptionResponse {
                option: Some(pbconv::flow_to_pb(&f)),
            }))
    }

    async fn select_option(
        &self,
        ctx: &Ctx,
        r: Request<pb::SelectOptionRequest>,
    ) -> Rpc<pb::SelectOptionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let f = Box::pin(self.graph.select_option(
            ctx,
            &cid(&r.msg.change_id),
            &r.msg.option,
            &principal_from(ctx).subject,
        ))
        .await
        .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.option_selected", r.msg.change_id),
            &f,
        )
        .await;
        Ok(Response::new(pb::SelectOptionResponse {
            option: Some(pbconv::flow_to_pb(&f)),
        }))
    }

    async fn reject_option(
        &self,
        ctx: &Ctx,
        r: Request<pb::RejectOptionRequest>,
    ) -> Rpc<pb::RejectOptionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let f = self
            .graph
            .reject_option(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.option,
                &principal_from(ctx).subject,
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.option_rejected", r.msg.change_id),
            &f,
        )
        .await;
        Ok(Response::new(pb::RejectOptionResponse {
            option: Some(pbconv::flow_to_pb(&f)),
        }))
    }

    async fn list_options(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListOptionsRequest>,
    ) -> Rpc<pb::ListOptionsResponse> {
        let os = self
            .graph
            .options(ctx, &cid(&r.msg.change_id))
            .await
            .map_err(ge)?;
        let mut out = pb::ListOptionsResponse::default();
        for f in &os {
            out.options.push(pbconv::flow_to_pb(f));
            if f.active {
                out.active = f.id.clone();
            }
        }
        Ok(Response::new(out))
    }

    async fn compare_options(
        &self,
        ctx: &Ctx,
        r: Request<pb::CompareOptionsRequest>,
    ) -> Rpc<pb::CompareOptionsResponse> {
        let cmp = self
            .graph
            .compare_options(ctx, &cid(&r.msg.change_id), &r.msg.level, r.msg.all)
            .await
            .map_err(ge)?;
        let mut out = pb::CompareOptionsResponse {
            level: cmp.level.clone(),
            options: cmp.options.iter().map(pbconv::flow_to_pb).collect(),
            ..Default::default()
        };
        for n in &cmp.nodes {
            let mut on = pb::OptionNode {
                node: n.node.to_string(),
                key: n.key.clone(),
                r#type: n.r#type.clone(),
                main: pbconv::ref_ptr_to_pb(n.main.as_ref()),
                ..Default::default()
            };
            for (id, r) in &n.options {
                if let Some(r) = r {
                    on.options.insert(id.clone(), pbconv::ref_to_pb(r));
                }
            }
            for (side, p) in &n.props {
                on.props
                    .insert(side.clone(), pbconv::to_struct(p).unwrap_or_default());
            }
            out.nodes.push(on);
        }
        Ok(Response::new(out))
    }

    async fn diff_flows(
        &self,
        ctx: &Ctx,
        r: Request<pb::DiffFlowsRequest>,
    ) -> Rpc<pb::DiffFlowsResponse> {
        res(self
            .graph
            .diff_flows(
                ctx,
                &cid(&r.msg.change_id),
                &r.msg.left,
                &r.msg.right,
                &r.msg.level,
            )
            .await
            .map(|d| pbconv::flow_diff_to_pb(&d)))
    }

    async fn get_change_view(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeViewRequest>,
    ) -> Rpc<pb::GetChangeViewResponse> {
        res(self
            .graph
            .change_view(ctx, &cid(&r.msg.change_id), &r.msg.flow, &r.msg.level)
            .await
            .map(|b| pb::GetChangeViewResponse {
                baseline: Some(pbconv::baseline_to_pb(&b)),
            }))
    }

    async fn get_change_graph(
        &self,
        ctx: &Ctx,
        r: Request<pb::GetChangeGraphRequest>,
    ) -> Rpc<pb::GetChangeGraphResponse> {
        res(self
            .graph
            .change_graph(ctx, &cid(&r.msg.change_id), &r.msg.flow)
            .await
            .map(|(nodes, links)| pb::GetChangeGraphResponse {
                nodes: pbconv::nodes_to_pb(&nodes),
                links: pbconv::links_to_pb(&links),
            }))
    }

    // ---- Decision points (ADR 0009 §4) -------------------------------------------------------------------------

    async fn open_decision(
        &self,
        ctx: &Ctx,
        r: Request<pb::OpenDecisionRequest>,
    ) -> Rpc<pb::OpenDecisionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        // AllOptions: every option of the change (Go's nil); otherwise the named ones, none meaning none
        let options = if m.all_options { None } else { Some(m.options) };
        let d = self
            .graph
            .open_decision(
                ctx,
                &cid(&m.change_id),
                OpenDecisionRequest {
                    question: m.question,
                    options,
                    criteria: m.criteria,
                    policy: pbconv::to_map(m.policy.as_ref()),
                    by: principal_from(ctx).subject,
                },
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.decision_opened", m.change_id),
            &d,
        )
        .await;
        Ok(Response::new(pb::OpenDecisionResponse {
            point: Some(pbconv::decision_point_to_pb(&d)),
        }))
    }

    async fn rule_decision(
        &self,
        ctx: &Ctx,
        r: Request<pb::RuleDecisionRequest>,
    ) -> Rpc<pb::RuleDecisionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let d = self
            .graph
            .rule_decision(
                ctx,
                &cid(&m.change_id),
                RuleRequest {
                    point: m.point,
                    outcome: m.outcome,
                    option: m.option,
                    confidence: m.confidence,
                    justification: m.justification,
                    questions: m.questions,
                    human: !m.agent,
                    by: principal_from(ctx).subject,
                },
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.decision_ruled", m.change_id),
            &d,
        )
        .await;
        Ok(Response::new(pb::RuleDecisionResponse {
            point: Some(pbconv::decision_point_to_pb(&d)),
        }))
    }

    async fn answer_question(
        &self,
        ctx: &Ctx,
        r: Request<pb::AnswerQuestionRequest>,
    ) -> Rpc<pb::AnswerQuestionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let d = self
            .graph
            .answer_question(
                ctx,
                &cid(&m.change_id),
                &m.question,
                &m.answer,
                &m.process,
                &principal_from(ctx).subject,
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.question_answered", m.change_id),
            &d,
        )
        .await;
        Ok(Response::new(pb::AnswerQuestionResponse {
            point: Some(pbconv::decision_point_to_pb(&d)),
        }))
    }

    async fn ratify_decision(
        &self,
        ctx: &Ctx,
        r: Request<pb::RatifyDecisionRequest>,
    ) -> Rpc<pb::RatifyDecisionResponse> {
        let ctx = &self.ctx_of(ctx, &r.headers);
        let m = r.msg;
        let d = self
            .graph
            .ratify_decision(
                ctx,
                &cid(&m.change_id),
                &m.point,
                m.accept,
                &principal_from(ctx).subject,
                &m.comment,
            )
            .await
            .map_err(ge)?;
        self.publish(
            ctx,
            &format!("goap.change.{}.decision_ratified", m.change_id),
            &d,
        )
        .await;
        Ok(Response::new(pb::RatifyDecisionResponse {
            point: Some(pbconv::decision_point_to_pb(&d)),
        }))
    }

    async fn list_decision_points(
        &self,
        ctx: &Ctx,
        r: Request<pb::ListDecisionPointsRequest>,
    ) -> Rpc<pb::ListDecisionPointsResponse> {
        res(self
            .graph
            .decision_points(ctx, &cid(&r.msg.change_id))
            .await
            .map(|ds| pb::ListDecisionPointsResponse {
                points: ds.iter().map(pbconv::decision_point_to_pb).collect(),
            }))
    }
}
