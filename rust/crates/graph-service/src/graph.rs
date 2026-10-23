//! `goap.graph.v1.GraphService`: the methods written so far; the others answer `unimplemented` (the generated
//! default), and so does a request a written method does not serve yet (a change view, a person's read: see
//! [`Caller::serves`]), so that `goap-proxy` in mode split replays it on the Go service.
use std::{collections::HashMap, sync::Arc};

use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::graph::v1 as pb;
use graph_catalog::{Catalog, catalog::STRUCTURE_PROJECT};
use graph_core::write::restructure::{NodeName, Restructured};
use graph_core::write::reviews::{Actor, EntryEdit, ReviewEdit, ReviewObject};
use graph_core::{
    GraphError, Kind,
    change::{self, ChangesFilter},
    flow::{OP_ADOPT, OP_DISCARD},
    impact::ChangeImpact,
    items,
    lnk::Url,
    model::{BaselineId, NodeId, NodeQuery, NodeRef, namespace_of},
    page::link_page,
    read::{self, ViewLevel, World},
    stats,
    store::ReadTx,
};
use graph_core::{
    model::{ChangeId, MAIN_BRANCH},
    write::{
        branches::{self, NewBranch},
        change::NewChange,
        commit::NOTHING_TO_COMMIT,
        decision::{self, OpenDecision, Rule},
        edits::{Commit, LinkEdit, NodeEdit},
        flow::{self as wflow, OpenFlow},
        items::ItemWrite,
        review::{ReviewBatch, Verdict},
        tx::{Env, Tx},
        update::ChangePatch,
        whole,
        work::{NodeCheckout, NodeCreate, fold_change},
    },
};
use graph_store::read::{LogFilter, Session, Tag};

use crate::{State, convert};

/// `goap.graph.v1.GraphService`.
pub struct Graph {
    pub state: Arc<State>,
}

/// The identity headers the gateway sets (`lib/identity`).
const HEADER_SUBJECT: &str = "x-goap-subject";
const HEADER_ON_BEHALF_OF: &str = "x-goap-on-behalf-of";
/// The positions of change logs a read must reach (ADR 0153).
const HEADER_AT_LEAST: &str = "x-goap-at-least";
/// The positions a write left the logs of its changes at (ADR 0153).
const HEADER_CHANGE_SEQ: &str = "x-goap-change-seq";
const HEADER_PROJECT: &str = "x-goap-project";
const HEADER_COMMAND: &str = "x-goap-command";
const HEADER_ROLES: &str = "x-goap-roles";
/// The platform role of the administrators (`access.RoleAdmin`).
const ROLE_ADMIN: &str = "admin";

/// The caller of a request.
struct Caller {
    subject: String,
    on_behalf_of: String,
    at_least: bool,
    project: String,
    command: String,
    roles: Vec<String>,
}

impl Caller {
    /// The caller of a request; an anonymous one is refused, as the Go service's `Identify` interceptor does.
    fn of<T>(req: &Request<T>) -> Result<Caller, ConnectError> {
        let subject = req.header(HEADER_SUBJECT).to_string();
        if subject.is_empty() {
            return Err(ConnectError::unauthenticated("the caller is not identified"));
        }
        let on_behalf_of = if subject.starts_with("system:") {
            req.header(HEADER_ON_BEHALF_OF).to_string()
        } else {
            String::new()
        };
        Ok(Caller {
            subject,
            on_behalf_of,
            at_least: !req.header(HEADER_AT_LEAST).is_empty(),
            project: req.header(HEADER_PROJECT).to_string(),
            command: req.header(HEADER_COMMAND).to_string(),
            roles: req
                .header(HEADER_ROLES)
                .split(',')
                .map(str::trim)
                .filter(|r| !r.is_empty())
                .map(str::to_string)
                .collect(),
        })
    }

    /// The reads served here: a platform service acting by itself reads the whole graph (no reach, ADR 0161 §5). A
    /// person, or a service acting for one, reads through the access snapshot of the organisation, and a read
    /// requiring the position of a change log waits on the fold of that log: the Go service serves those for now.
    fn serves(&self) -> Result<(), GraphError> {
        if !self.subject.starts_with("system:") || !self.on_behalf_of.is_empty() {
            return Err(GraphError::unimplemented(
                "a person's read goes through the reach of its caller (ADR 0161): not served here yet",
            ));
        }
        if self.at_least {
            return Err(GraphError::unimplemented(
                "a read at a position of a change log (ADR 0153): not served here yet",
            ));
        }
        Ok(())
    }

    /// The writes served here: a platform service acting by itself. A person's write goes through the organisation's
    /// access (reach, owned writes, project roles): the Go service serves those for now.
    fn writes(&self) -> Result<(), GraphError> {
        if !self.subject.starts_with("system:") || !self.on_behalf_of.is_empty() {
            return Err(GraphError::unimplemented(
                "a person's write goes through the access of the organisation: not served here yet",
            ));
        }
        if self.at_least {
            return Err(GraphError::unimplemented(
                "a request at a position of a change log (ADR 0153): not served here yet",
            ));
        }
        Ok(())
    }
}

impl Caller {
    /// The writes the access of the organisation would let through whatever the project: a platform administrator's
    /// (ADR 0047). Any other caller is judged by the policies of the organisation, which the Go service reads.
    fn administers(&self) -> Result<(), GraphError> {
        if self.roles.iter().any(|r| r == ROLE_ADMIN) {
            return Ok(());
        }
        Err(GraphError::unimplemented(
            "a write authorized by the policies of the organisation: not served here yet",
        ))
    }
}

/// The change a write on its impacts works on, when it is one served here: neither personal nor of the platform's
/// namespaces.
async fn served_change(tx: &mut Tx<'_, Session>, id: &str) -> Result<ChangeId, GraphError> {
    let id: ChangeId = id.into();
    let c = tx.change_head(&id).await?;
    if c.is_personal() || is_platform_namespace(&c.namespace) {
        return Err(GraphError::unimplemented(
            "a personal change or a change of the platform's namespaces: not served here yet",
        ));
    }
    Ok(id)
}

/// The view of a change on a flow (empty: its active option), refusing a change that does not exist; a personal
/// change is the Go service's.
async fn change_world<'a, T: ReadTx>(tx: &'a T, id: &str, flow: &str) -> Result<World<'a, T>, GraphError> {
    let id = ChangeId(id.to_string());
    known_change(tx, &id).await?;
    let view = graph_core::view::View {
        change: id,
        flow: flow.to_string(),
        ..Default::default()
    };
    World::open_node(tx, &view).await
}

/// Refuses a change that does not exist.
async fn known_change<T: ReadTx>(tx: &T, id: &ChangeId) -> Result<(), GraphError> {
    if tx.change(id).await?.is_none() {
        return Err(GraphError::not_found(format!("change {id}")));
    }
    Ok(())
}

/// The items a request writes; one carrying a flow event, a decision event or a decision is the Go service's.
fn item_writes(items: Vec<pb::ChangeItem>) -> Result<Vec<ItemWrite>, ConnectError> {
    items
        .into_iter()
        .map(|it| {
            if it.decision.is_some() || it.flow_event.is_some() || it.decision_event.is_some() {
                return Err(connect_error(GraphError::unimplemented(
                    "an item of a flow or of a decision: written by the Go service",
                )));
            }
            Ok(ItemWrite {
                id: it.id,
                typ: it.r#type,
                target: it.target,
                status: it.status,
                flow: it.flow,
                data: it.data.as_ref().map(convert::map),
                produced_by: it.produced_by,
                derived_from: it.derived_from,
                supersedes: it.supersedes,
                execution: it.execution,
            })
        })
        .collect()
}

/// The header naming, on a Connect error, the sentinel finer than its code (`lib/rpcerr`).
const REASON_HEADER: &str = "Goap-Error-Reason";

/// The Connect error of a graph error (`lib/rpcerr.ToConnect`).
pub(crate) fn connect_error(e: GraphError) -> ConnectError {
    if e.kind == Kind::Invalid && e.message.ends_with(&format!(": {NOTHING_TO_COMMIT}: invalid")) {
        return ConnectError::invalid_argument(e.message).with_meta(REASON_HEADER, "nothing-to-commit");
    }
    match e.kind {
        Kind::NotFound => ConnectError::not_found(e.message),
        Kind::Invalid => ConnectError::invalid_argument(e.message),
        Kind::Conflict => ConnectError::failed_precondition(e.message),
        Kind::Forbidden => ConnectError::permission_denied(e.message),
        Kind::Unimplemented => ConnectError::unimplemented(e.message),
        Kind::Internal => ConnectError::internal(e.message),
    }
}

type Answer<T> = Result<Response<T>, ConnectError>;

/// The size of a page of links asked: 0 the default page, at most the largest.
fn link_limit(n: i32) -> usize {
    const DEFAULT: usize = 100;
    if n <= 0 {
        DEFAULT
    } else {
        (n as usize).min(graph_core::model::MAX_BROWSE_LIMIT)
    }
}

/// A unit or a project of a filter as the changes store it: a URL, without its version. A key needs the structures
/// of the type catalogue (`Graph.StructureURL`): the Go service resolves it for now.
fn structure_ref(s: &str) -> Result<String, GraphError> {
    if s.is_empty() {
        return Ok(String::new());
    }
    if !s.starts_with("lnk:") {
        return Err(GraphError::unimplemented(
            "a unit or a project named by its key needs the structures of the catalogue: not served here yet",
        ));
    }
    Url::parse(s)
        .map(|u| u.logical().to_string())
        .map_err(GraphError::invalid)
}

impl Graph {
    /// Runs a read on a session of the store, once the caller is one served here.
    async fn read<T, F>(&self, caller: Caller, f: F) -> Answer<T>
    where
        F: AsyncFnOnce(&Session) -> Result<T, GraphError>,
    {
        caller.serves().map_err(connect_error)?;
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        f(&tx).await.map(Response::new).map_err(connect_error)
    }
}

impl Graph {
    /// Opens a call any identified caller may make: the first call of a person creates its user (Go's `EnsureCaller`
    /// interceptor, ADR 0040), which is the Go service's, so a person this graph holds no user of yet is left to it.
    async fn identified<T>(&self, req: &Request<T>) -> Result<Caller, ConnectError> {
        let caller = Caller::of(req)?;
        if caller.subject.starts_with("system:") {
            return Ok(caller);
        }
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        if !tx
            .has_node(
                graph_core::model::NAMESPACE_ORGANISATION,
                &format!("USR:{}", caller.subject),
            )
            .await
            .map_err(connect_error)?
        {
            return Err(connect_error(GraphError::unimplemented(
                "the first call of a person creates its user (EnsureUser): not served here yet",
            )));
        }
        Ok(caller)
    }

    /// Runs a read any identified caller may make, one that filters nothing by its caller.
    async fn read_any<R, T, F>(&self, req: &Request<R>, f: F) -> Answer<T>
    where
        F: AsyncFnOnce(&Session) -> Result<T, GraphError>,
    {
        self.identified(req).await?;
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        f(&tx).await.map(Response::new).map_err(connect_error)
    }

    /// The type catalogue of the domains in force.
    async fn catalog(&self) -> Result<Arc<Catalog>, ConnectError> {
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        self.state.catalog(&tx).await.map_err(connect_error)
    }

    /// Runs a write in one transaction of the store, through the guard and the outbox of the graph: committed when it
    /// succeeds, rolled back otherwise. The positions it left the change logs at go back in `X-Goap-Change-Seq`.
    async fn write<T, F>(&self, caller: Caller, f: F) -> Answer<T>
    where
        F: for<'a, 'b> AsyncFnOnce(&'a mut Tx<'b, Session>) -> Result<T, GraphError>,
    {
        caller.writes().map_err(connect_error)?;
        let session = self
            .state
            .store
            .writer()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        let catalog = self.state.catalog(&session).await.map_err(connect_error)?;
        let env = Env {
            by: caller.subject.clone(),
            end_user: String::new(),
            actor: caller.subject.clone(),
            command: caller.command.clone(),
            catalog,
            outbox: self.state.outbox,
        };
        let mut tx = Tx::new(&session, &env);
        let out = f(&mut tx).await.map_err(connect_error)?;
        let positions = tx.finish().await.map_err(connect_error)?;
        session.commit().await.map_err(|e| connect_error(GraphError::from(e)))?;
        let mut res = Response::new(out);
        if !positions.is_empty() {
            let v = positions
                .iter()
                .map(|(id, seq)| format!("{id}:{seq}"))
                .collect::<Vec<_>>()
                .join(",");
            if let Ok(v) = v.parse() {
                res.headers.insert(HEADER_CHANGE_SEQ, v);
            }
        }
        Ok(res)
    }
}

/// The criticality a change's data may name (`validCriticality`).
fn valid_criticality(data: &serde_json::Map<String, serde_json::Value>) -> Result<(), GraphError> {
    let Some(v) = data.get("criticality") else {
        return Ok(());
    };
    match v.as_str() {
        Some("C1" | "C2" | "C3") => Ok(()),
        _ => Err(GraphError::new(
            Kind::Invalid,
            format!("criticality {}: C1, C2 or C3", graph_core::write::tx::go_sprint(v)),
        )),
    }
}

/// A node as GetNode reads it: a version named is its history (no view), else the node as the view holds it.
async fn read_node(
    tx: &Session,
    r#ref: Option<&pb::NodeRef>,
    key: &str,
    view: Option<&pb::View>,
) -> Result<graph_core::model::NodeView, GraphError> {
    let r = convert::ref_from(r#ref);
    if key.is_empty() && r.version > 0 {
        return read::version_view(tx, &r).await;
    }
    World::open_node(tx, &convert::view(view))
        .await?
        .node_view(&r.id, key)
        .await
}

#[async_trait]
impl pb::GraphService for Graph {
    async fn get_node(&self, _ctx: &Ctx, req: Request<pb::GetNodeRequest>) -> Answer<pb::GetNodeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let v = read_node(tx, m.r#ref.as_ref(), &m.key, m.view.as_ref()).await?;
            // a page of each direction: a hub's thousands of links are read with ListNodeLinks
            let limit = link_limit(m.link_limit);
            let (out, out_total, out_types) = link_page(&v.out, "", 0, limit);
            let (r#in, in_total, in_types) = link_page(&v.r#in, "", 0, limit);
            Ok(pb::GetNodeResponse {
                view: Some(pb::NodeView {
                    node: Some(convert::node(&v.node)),
                    latest: v.latest,
                    out: convert::links(&out),
                    r#in: convert::links(&r#in),
                    not_committable: false,
                    out_total: out_total as i32,
                    in_total: in_total as i32,
                    out_types: convert::type_counts(&out_types),
                    in_types: convert::type_counts(&in_types),
                }),
            })
        })
        .await
    }

    async fn list_node_links(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListNodeLinksRequest>,
    ) -> Answer<pb::ListNodeLinksResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let v = read_node(tx, m.r#ref.as_ref(), &m.key, m.view.as_ref()).await?;
            let links = if m.incoming { &v.r#in } else { &v.out };
            let (page, total, _) = link_page(links, &m.r#type, m.offset.max(0) as usize, link_limit(m.limit));
            Ok(pb::ListNodeLinksResponse {
                links: convert::links(&page),
                total: total as i32,
            })
        })
        .await
    }

    async fn get_node_neighbourhood(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetNodeNeighbourhoodRequest>,
    ) -> Answer<pb::GetNodeNeighbourhoodResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let w = World::open(tx, &convert::view(m.view.as_ref())).await?;
            let types = (!m.link_types.is_empty()).then_some(m.link_types.as_slice());
            let types = w.link_types(types);
            let nb = w.neighbourhood(&NodeId(m.node_id.clone()), types.as_deref()).await?;
            Ok(pb::GetNodeNeighbourhoodResponse {
                node: Some(convert::node(&nb.node)),
                nodes: convert::nodes(&nb.nodes),
                links: convert::links(&nb.links),
                suspect_link_ids: nb.suspect.iter().map(|l| l.0.clone()).collect(),
            })
        })
        .await
    }

    async fn list_baseline_nodes(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListBaselineNodesRequest>,
    ) -> Answer<pb::ListBaselineNodesResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let w = World::open(tx, &convert::view(m.view.as_ref())).await?;
            let q = NodeQuery {
                typ: m.r#type.clone(),
                text: m.query.clone(),
                include_deleted: m.include_deleted,
                change: m.change_id.as_str().into(),
                offset: m.offset.max(0) as usize,
                limit: read::view_page_limit(m.limit.into()),
                projects: None,
            };
            let page = w.page(&q).await?;
            let header = read::header_of(tx, &page.baseline).await?; // the header: the page is what is sent
            Ok(pb::ListBaselineNodesResponse {
                nodes: convert::nodes(&page.nodes),
                total: page.total as i32,
                types: convert::type_counts(&page.types),
                baseline: Some(convert::baseline(&header, None)),
            })
        })
        .await
    }

    /// A page of the links of a baseline, between two of its nodes (`BaselineLinks`).
    async fn list_baseline_links(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListBaselineLinksRequest>,
    ) -> Answer<pb::ListBaselineLinksResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let header = read::header_of(tx, &BaselineId(m.baseline_id.clone())).await?;
            let limit = match m.limit {
                l if l <= 0 || l as usize > graph_core::model::MAX_BROWSE_LIMIT => graph_core::model::MAX_BROWSE_LIMIT,
                l => l as usize,
            };
            let state = read::select(tx, &header, None).await?;
            let mut refs: Vec<NodeRef> = state.iter().map(|(n, v)| NodeRef::new(n.clone(), *v)).collect();
            refs.sort();
            let (links, total, types) = tx
                .link_page(&refs, &m.r#type, &m.query, m.offset.max(0) as usize, limit)
                .await?;
            Ok(pb::ListBaselineLinksResponse {
                baseline: Some(convert::baseline(&header, None)),
                links: convert::links(&links),
                total: total as i32,
                types: convert::type_counts(&types),
            })
        })
        .await
    }

    /// The node versions merged or split from a version (`DerivedNodes`).
    async fn derived_nodes(
        &self,
        _ctx: &Ctx,
        req: Request<pb::DerivedNodesRequest>,
    ) -> Answer<pb::DerivedNodesResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let r = m
                .r#ref
                .map(|r| NodeRef::new(NodeId(r.id), r.version))
                .unwrap_or_default();
            Ok(pb::DerivedNodesResponse {
                nodes: convert::nodes(&tx.derived_nodes(&r).await?),
            })
        })
        .await
    }

    /// The nodes of a type a view holds, all of them (`ViewNodesOfType`): a branch view; a change view is still Go's.
    async fn list_view_nodes(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListViewNodesRequest>,
    ) -> Answer<pb::ListViewNodesResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let w = World::open(tx, &convert::view(m.view.as_ref())).await?;
            let q = NodeQuery {
                typ: m.r#type.clone(),
                limit: i32::MAX as usize,
                ..NodeQuery::default()
            };
            let mut nodes = w.page(&q).await?.nodes;
            if !m.r#type.is_empty() {
                nodes.retain(|n| n.typ == m.r#type);
            }
            Ok(pb::ListViewNodesResponse {
                nodes: convert::nodes(&nodes),
            })
        })
        .await
    }

    /// The versions some nodes have in a view, with what the view stands on (`ViewVersions`).
    async fn resolve_view(&self, _ctx: &Ctx, req: Request<pb::ResolveViewRequest>) -> Answer<pb::ResolveViewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let v = convert::view(m.view.as_ref());
            let w = World::open_node(tx, &v).await?;
            let main_head = if !v.is_change() && graph_core::model::branch_of(&v.branch) == MAIN_BRANCH {
                v.baseline.is_empty() || read::branch_head(tx, &w.namespace, MAIN_BRANCH).await?.id == v.baseline
            } else {
                false
            };
            let ids: Vec<NodeId> = m.node_ids.iter().cloned().map(NodeId).collect();
            let versions = w.at(&ids).await?;
            Ok(pb::ResolveViewResponse {
                namespace: w.namespace.clone(),
                baseline_id: w.baseline.id.0.clone(),
                main_head,
                versions: versions.into_iter().map(|(n, v)| (n.0, v)).collect(),
            })
        })
        .await
    }

    async fn list_node_versions(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListNodeVersionsRequest>,
    ) -> Answer<pb::ListNodeVersionsResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let (vs, next) = read::version_page(tx, &NodeId(m.id.clone()), m.before, m.limit.max(0) as usize).await?;
            Ok(pb::ListNodeVersionsResponse {
                versions: convert::nodes(&vs),
                next,
            })
        })
        .await
    }

    /// Whether a node type is written by platform administrators only (ADR 0068), open to any identified caller.
    async fn is_admin_only_type(
        &self,
        _ctx: &Ctx,
        req: Request<pb::IsAdminOnlyTypeRequest>,
    ) -> Answer<pb::IsAdminOnlyTypeResponse> {
        self.identified(&req).await?;
        let cat = self.catalog().await?;
        let t = req.into_inner().r#type;
        Ok(Response::new(pb::IsAdminOnlyTypeResponse {
            admin_only: cat.admin_only(&t) || Catalog::builtin().admin_only(&t),
        }))
    }

    /// The item types the domains in force declare (ADR 0128), with what this service gives them.
    async fn list_item_types(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListItemTypesRequest>,
    ) -> Answer<pb::ListItemTypesResponse> {
        self.identified(&req).await?;
        let cat = self.catalog().await?;
        let types = cat
            .item_types()
            .map(|t| {
                let (permission, subject_field) = items::permission(&t.name).unwrap_or_default();
                pb::ItemTypeInfo {
                    r#type: t.name.clone(),
                    domain: t.domain.clone(),
                    domain_version: t.version.clone(),
                    description: t.description.clone(),
                    code: items::has_code(&t.name),
                    permission: permission.into(),
                    subject_field: subject_field.into(),
                }
            })
            .collect();
        Ok(Response::new(pb::ListItemTypesResponse { types }))
    }

    /// The structures of the graph (ADR 0054), each with the types building it.
    async fn get_structures(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetStructuresRequest>,
    ) -> Answer<pb::GetStructuresResponse> {
        self.identified(&req).await?;
        let cat = self.catalog().await?;
        let all = cat.types();
        let structures = cat
            .structures()
            .map(|st| pb::Structure {
                kind: st.kind.clone(),
                r#type: st.typ.clone(),
                namespace: st.namespace.clone(),
                parent: st.parent.clone(),
                root: st.root.clone(),
                self_parent: st.self_parent,
                types: all
                    .iter()
                    .filter(|t| t.is(&st.typ))
                    .map(|t| t.reference.clone())
                    .collect(),
                bootstrap: (!st.bootstrap.is_empty()).then(|| convert::object(&st.bootstrap)),
            })
            .collect();
        Ok(Response::new(pb::GetStructuresResponse { structures }))
    }

    /// The namespaces some node lives in.
    async fn list_namespaces(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListNamespacesRequest>,
    ) -> Answer<pb::ListNamespacesResponse> {
        self.identified(&req).await?;
        let tx = self
            .state
            .store
            .reader()
            .await
            .map_err(|e| connect_error(GraphError::internal(e)))?;
        if other_graphs(&tx).await.map_err(connect_error)? {
            return Err(connect_error(GraphError::unimplemented(MERGED_LISTING)));
        }
        let namespaces = tx.namespaces().await.map_err(connect_error)?;
        Ok(Response::new(pb::ListNamespacesResponse { namespaces }))
    }

    async fn get_branch(&self, _ctx: &Ctx, req: Request<pb::GetBranchRequest>) -> Answer<pb::GetBranchResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let b = read::branch(tx, &m.namespace, &m.name).await?;
            if b.ephemeral && !m.header_only {
                return Err(GraphError::unimplemented(
                    "the head of the branch of a change (its fork with its commits): not served here yet",
                ));
            }
            let head = read::branch_head(tx, &m.namespace, &b.name).await?;
            let state = if m.header_only {
                None
            } else {
                let s = read::select(tx, &head, None).await?;
                Some(s.into_iter().map(|(n, v)| (n.0, v)).collect::<HashMap<_, _>>())
            };
            Ok(pb::GetBranchResponse {
                branch: Some(convert::branch(&b)),
                head: Some(convert::baseline(&head, state)),
            })
        })
        .await
    }

    /// The log of a change (or of processes), filtered, with the number of entries of each type.
    async fn list_change_log(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListChangeLogRequest>,
    ) -> Answer<pb::ListChangeLogResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let f = LogFilter {
                change: m.change_id.clone(),
                types: m.types.clone(),
                flows: (!m.flows.is_empty()).then(|| {
                    m.flows
                        .iter()
                        .map(|f| if f == "main" { String::new() } else { f.clone() })
                        .collect()
                }),
                processes: m.process_ids.clone(),
                execution: m.execution.clone(),
                after_seq: m.after_seq,
                limit: m.limit.into(),
            };
            if f.change.is_empty() && f.processes.is_empty() {
                return Err(GraphError::invalid("the log of which change"));
            }
            if !f.change.is_empty() && tx.change(&ChangeId(f.change.clone())).await?.is_none() {
                return Err(GraphError::not_found(format!("change {}", f.change)));
            }
            let (entries, counts) = tx.change_log(&f).await?;
            // the prompts of the model calls are the change's: a read by processes only names its types
            let model_call = f.types.is_empty()
                || f.types
                    .iter()
                    .any(|t| t == "model.call" || (t.ends_with('.') && "model.call".starts_with(t.as_str())));
            if model_call && f.change.is_empty() {
                return Err(GraphError::invalid("a log read without a change names its types"));
            }
            Ok(pb::ListChangeLogResponse {
                entries: entries.iter().map(convert::log_entry).collect(),
                counts: counts.into_iter().map(|(t, n)| (t, n as i32)).collect(),
            })
        })
        .await
    }

    /// A baseline whole: its state, the nodes it holds, the links between them and the suspect ones (a link to
    /// another version of a node the baseline holds).
    async fn get_baseline_graph(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetBaselineGraphRequest>,
    ) -> Answer<pb::GetBaselineGraphResponse> {
        let caller = Caller::of(&req)?;
        let id = BaselineId(req.into_inner().id);
        self.read(caller, async |tx| {
            let header = read::header_of(tx, &id).await?;
            let state = read::select(tx, &header, None).await?;
            let mut refs: Vec<NodeRef> = state.iter().map(|(n, v)| NodeRef::new(n.clone(), *v)).collect();
            refs.sort();
            let (nodes, out) = if refs.is_empty() {
                (Vec::new(), Vec::new())
            } else {
                (tx.nodes_at(&refs, "").await?, tx.links_from(&refs, None).await?)
            };
            let (mut links, mut suspects) = (Vec::new(), Vec::new());
            for l in out {
                match l.to_node().and_then(|t| state.get(&t.id).map(|v| *v == t.version)) {
                    Some(true) => links.push(l),
                    Some(false) => suspects.push(l),
                    None => {}
                }
            }
            let state = state.into_iter().map(|(n, v)| (n.0, v)).collect::<HashMap<_, _>>();
            Ok(pb::GetBaselineGraphResponse {
                baseline: Some(convert::baseline(&header, Some(state))),
                nodes: convert::nodes(&nodes),
                links: convert::links(&links),
                suspect_links: convert::links(&suspects),
            })
        })
        .await
    }

    /// What every namespace holds (`GraphOverview`): those holding nodes and those of the domains in force.
    async fn get_graph_overview(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetGraphOverviewRequest>,
    ) -> Answer<pb::GetGraphOverviewResponse> {
        let caller = Caller::of(&req)?;
        let cat = self.catalog().await?;
        self.read(caller, async |tx| {
            if other_graphs(tx).await? {
                return Err(GraphError::unimplemented(MERGED_LISTING));
            }
            let mut ns: Vec<String> = cat.domains().keys().cloned().collect();
            for n in tx.namespaces().await? {
                if !ns.contains(&n) {
                    ns.push(n);
                }
            }
            ns.sort();
            let mut out = Vec::with_capacity(ns.len());
            for n in ns {
                let counts = tx.store_counts(&n).await?;
                let sum = |m: &str| counts.get(m).map_or(0, |c| c.values().sum::<i64>()) as i32;
                out.push(pb::NamespaceSummary {
                    nodes: sum("nodesByType"),
                    versions: sum("versionsByType"),
                    link_rows: sum("linkRowsByType"),
                    changes: sum("changesByStatus"),
                    baselines: sum("baselinesByKind"),
                    branches: tx.branches(&n).await?.len() as i32,
                    types: counts.get("nodesByType").map_or(0, |c| c.len()) as i32,
                    log_entries: sum("logByType"),
                    namespace: n,
                    source: PLATFORM_SOURCE.into(),
                });
            }
            Ok(pb::GetGraphOverviewResponse { namespaces: out })
        })
        .await
    }

    /// The statistics of a namespace at the head of a lasting branch (`GraphStats`): what the store holds, counted by
    /// the database, and the shape of the state.
    async fn get_graph_stats(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetGraphStatsRequest>,
    ) -> Answer<pb::GetGraphStatsResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let namespace = namespace_of(&m.namespace).to_string();
            let branch = graph_core::model::branch_of(&m.branch).to_string();
            let store = tx.store_counts(&namespace).await?;
            let bs = stats::branch_stats(&tx.branches(&namespace).await?);
            let head = read::branch_head(tx, &namespace, &branch).await?;
            let state = read::select(tx, &head, None).await?;
            let (mut nodes, mut links) = (Vec::new(), Vec::new());
            if !state.is_empty() {
                let mut refs: Vec<NodeRef> = state.iter().map(|(n, v)| NodeRef::new(n.clone(), *v)).collect();
                refs.sort();
                nodes = tx.nodes_at(&refs, "").await?;
                let live: Vec<NodeRef> = nodes
                    .iter()
                    .filter(|n| !n.deleted)
                    .map(|n| NodeRef::new(n.id.clone(), n.version))
                    .collect();
                if !live.is_empty() {
                    links = tx.links_from(&live, None).await?;
                }
            }
            let st = stats::state_stats(&state, &nodes, &links);
            let counts = |m: &HashMap<String, usize>| m.iter().map(|(k, v)| (k.clone(), *v as i32)).collect();
            Ok(pb::GetGraphStatsResponse {
                stats: Some(pb::GraphStats {
                    namespace,
                    branch,
                    baseline_at: if head.id.is_empty() {
                        None
                    } else {
                        convert::time(&head.created_at)
                    },
                    baseline_id: head.id.0.clone(),
                    branches: Some(pb::BranchStats {
                        total: bs.total as i32,
                        lasting: bs.lasting as i32,
                        ephemeral: bs.ephemeral as i32,
                        by_status: counts(&bs.by_status),
                        names: bs.names,
                    }),
                    store: stats::STORE_MEASURES
                        .iter()
                        .map(|name| pb::StoreMeasure {
                            name: name.to_string(),
                            counts: store
                                .get(*name)
                                .map(|c| c.iter().map(|(k, v)| (k.clone(), *v as i32)).collect())
                                .unwrap_or_default(),
                        })
                        .collect(),
                    state: Some(state_stats_pb(st)),
                }),
            })
        })
        .await
    }

    /// What changed between two baselines of a branch: the nodes committed between them as the later one holds
    /// them, their links, and the nodes it no longer holds.
    async fn get_baseline_delta(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetBaselineDeltaRequest>,
    ) -> Answer<pb::GetBaselineDeltaResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            if m.from.is_empty() || m.to.is_empty() {
                return Err(GraphError::invalid("a delta goes from a baseline to another"));
            }
            let from = read::header_of(tx, &BaselineId(m.from.clone())).await?;
            let to = read::header_of(tx, &BaselineId(m.to.clone())).await?;
            let branch = graph_core::model::branch_of(&to.branch).to_string();
            if from.namespace != to.namespace
                || graph_core::model::branch_of(&from.branch) != branch
                || to.seq < from.seq
            {
                return Err(GraphError::invalid(format!(
                    "baseline {} does not follow {} on its branch",
                    m.to, m.from
                )));
            }
            let moved = tx
                .committed_versions(&to.namespace, &branch, from.seq, to.seq, None)
                .await?;
            if moved.len() > MAX_DELTA_NODES {
                return Err(GraphError::invalid(format!(
                    "{} nodes moved between {} and {}, read the baseline",
                    moved.len(),
                    m.from,
                    m.to
                )));
            }
            let mut ids: Vec<NodeId> = moved.into_keys().collect();
            ids.sort();
            let at = read::select(tx, &to, Some(&ids)).await?;
            let removed = ids
                .iter()
                .filter(|n| !at.contains_key(*n))
                .map(|n| n.0.clone())
                .collect();
            let mut refs: Vec<NodeRef> = at.into_iter().map(|(n, v)| NodeRef::new(n, v)).collect();
            refs.sort();
            let (nodes, links) = if refs.is_empty() {
                (Vec::new(), Vec::new())
            } else {
                (tx.nodes_at(&refs, "").await?, tx.links_from(&refs, None).await?)
            };
            Ok(pb::GetBaselineDeltaResponse {
                from: Some(convert::baseline(&from, None)),
                to: Some(convert::baseline(&to, None)),
                nodes: convert::nodes(&nodes),
                links: convert::links(&links),
                removed,
            })
        })
        .await
    }

    /// The events of the impacts of a change, in the order of its log.
    async fn list_change_events(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListChangeEventsRequest>,
    ) -> Answer<pb::ListChangeEventsResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            if tx.change(&id).await?.is_none() {
                return Err(GraphError::not_found(format!("change {id}")));
            }
            let view = fold_change(tx, &id).await?;
            Ok(pb::ListChangeEventsResponse {
                events: view.events.iter().map(convert::impact_event).collect(),
            })
        })
        .await
    }

    /// What a change has to commit and whether it may merge.
    async fn get_commit_state(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetCommitStateRequest>,
    ) -> Answer<pb::GetCommitStateResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.write(caller, async |tx| {
            let st = tx.commit_state(&id).await?;
            Ok(pb::GetCommitStateResponse {
                to_commit: st.to_commit,
                reverts: st.reverts as i32,
                unsettled: st.unsettled,
                commits: st.commits,
                can_commit: st.can_commit,
                can_merge: st.can_merge,
            })
        })
        .await
    }

    /// The tags matching the filter, oldest first.
    async fn list_tags(&self, _ctx: &Ctx, req: Request<pb::ListTagsRequest>) -> Answer<pb::ListTagsResponse> {
        self.read_any(&req, async |tx| {
            let m = &req.msg;
            let tags = tx.tags(&m.namespace, &m.name, &m.node_id).await?;
            Ok(pb::ListTagsResponse {
                tags: tags.into_iter().map(tag_pb).collect(),
            })
        })
        .await
    }

    /// Names a node version for the caller; the same name on the same version is the tag already there.
    async fn tag_version(&self, _ctx: &Ctx, req: Request<pb::TagVersionRequest>) -> Answer<pb::TagVersionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let mut r = NodeRef::new(NodeId(m.node_id.clone()), m.version);
            if r.version <= 0 {
                let Some(view) = m.view.as_ref() else {
                    return Err(GraphError::new(
                        Kind::Invalid,
                        format!("tag {}: name a version or a view: {}", r.id, graph_core::view::NO_VIEW),
                    ));
                };
                r.version = World::open_node(tx.store, &convert::view(Some(view)))
                    .await?
                    .node_view(&r.id, "")
                    .await?
                    .node
                    .version;
            }
            // a draft of the view (version 0) reads as the node's latest, then has no version to tag
            let n = read::stored_node(tx.store, &r).await?;
            if m.name.is_empty() {
                return Err(GraphError::invalid("a tag needs a name"));
            }
            if r.version <= 0 {
                return Err(GraphError::invalid(format!("a tag names a written version, not {r}")));
            }
            let have = tx.store.tags("", &m.name, &r.id.0).await?;
            let t = match have.into_iter().find(|t| t.version == r.version) {
                Some(t) => t,
                None => {
                    let t = Tag {
                        id: tx.env.new_id(),
                        name: m.name.clone(),
                        namespace: namespace_of(&n.namespace).to_string(),
                        node: r.id.0.clone(),
                        version: r.version,
                        by: by.clone(),
                        created_at: Some(tx.env.now()),
                    };
                    tx.store.put_tag(&t).await?;
                    t
                }
            };
            Ok(pb::TagVersionResponse { tag: Some(tag_pb(t)) })
        })
        .await
    }

    /// Removes a tag; the version it named is untouched.
    async fn delete_tag(&self, _ctx: &Ctx, req: Request<pb::DeleteTagRequest>) -> Answer<pb::DeleteTagResponse> {
        let caller = Caller::of(&req)?;
        let id = req.into_inner().id;
        self.write(caller, async |tx| {
            tx.store.delete_tag(&id).await?;
            Ok(pb::DeleteTagResponse {})
        })
        .await
    }

    /// Opens a branch forked from a baseline.
    async fn create_branch(
        &self,
        _ctx: &Ctx,
        req: Request<pb::CreateBranchRequest>,
    ) -> Answer<pb::CreateBranchResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let b = branches::create_branch(
                tx,
                NewBranch {
                    name: m.name,
                    namespace: m.namespace,
                    from: BaselineId(m.from_baseline),
                    origin: m.origin,
                    description: m.description,
                },
            )
            .await?;
            Ok(pb::CreateBranchResponse {
                branch: Some(convert::branch(&b)),
            })
        })
        .await
    }

    async fn set_branch_status(
        &self,
        _ctx: &Ctx,
        req: Request<pb::SetBranchStatusRequest>,
    ) -> Answer<pb::SetBranchStatusResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            branches::set_branch_status(tx, &m.namespace, &m.name, &m.status).await?;
            Ok(pb::SetBranchStatusResponse {})
        })
        .await
    }

    async fn set_branch_description(
        &self,
        _ctx: &Ctx,
        req: Request<pb::SetBranchDescriptionRequest>,
    ) -> Answer<pb::SetBranchDescriptionResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            branches::set_branch_description(tx, &m.namespace, &m.name, &m.description).await?;
            Ok(pb::SetBranchDescriptionResponse {})
        })
        .await
    }

    /// The headers of the baselines of a namespace, or of some of them by id. The history of a branch (a page with
    /// the baselines it relates to) is still Go's.
    async fn list_baselines(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListBaselinesRequest>,
    ) -> Answer<pb::ListBaselinesResponse> {
        self.read_any(&req, async |tx| {
            let m = &req.msg;
            if !m.branch.is_empty() {
                return Err(GraphError::unimplemented(
                    "the history of a branch (BaselineHistory): not served here yet",
                ));
            }
            let bs = if m.ids.is_empty() {
                tx.baseline_headers(&m.namespace).await?
            } else {
                let ids: Vec<_> = m.ids.iter().cloned().map(BaselineId).collect();
                tx.baseline_headers_of(&m.namespace, &ids).await?
            };
            Ok(pb::ListBaselinesResponse {
                baselines: bs.iter().map(|b| listed_baseline(b, &m.namespace)).collect(),
                ..Default::default()
            })
        })
        .await
    }

    /// The branches of a namespace (of the statuses asked), the header of each head and the number of baselines of
    /// each branch.
    async fn list_branches(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListBranchesRequest>,
    ) -> Answer<pb::ListBranchesResponse> {
        self.read_any(&req, async |tx| {
            let m = &req.msg;
            let mut bs = tx.branches(&m.namespace).await?;
            if !m.status.is_empty() {
                bs.retain(|b| m.status.contains(&b.status));
            }
            let ids: Vec<_> = bs
                .iter()
                .filter(|b| !b.head.0.is_empty())
                .map(|b| b.head.clone())
                .collect();
            let heads = tx.baseline_headers_of(&m.namespace, &ids).await?;
            let counts = tx.baseline_counts(&m.namespace).await?;
            Ok(pb::ListBranchesResponse {
                branches: bs.iter().map(convert::branch).collect(),
                heads: heads.iter().map(|b| listed_baseline(b, &m.namespace)).collect(),
                counts: counts.into_iter().map(|(k, n)| (k, n as i32)).collect(),
            })
        })
        .await
    }

    /// The versions a change starts from: the version before of each of its impacts, in the order of the fold.
    async fn get_change_impacts(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetChangeImpactsRequest>,
    ) -> Answer<pb::GetChangeImpactsResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            if tx.change(&id).await?.is_none() {
                return Err(GraphError::not_found(format!("change {id}")));
            }
            let view = fold_change(tx, &id).await?;
            Ok(pb::GetChangeImpactsResponse {
                nodes: view
                    .fold
                    .impacts
                    .iter()
                    .filter_map(|cn| cn.pre.as_ref())
                    .map(convert::node_ref)
                    .collect(),
            })
        })
        .await
    }

    /// Sets the changes an open change depends on (ADR 0108).
    async fn set_change_dependencies(
        &self,
        _ctx: &Ctx,
        req: Request<pb::SetChangeDependenciesRequest>,
    ) -> Answer<pb::SetChangeDependenciesResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let deps: Vec<ChangeId> = m.depends_on.iter().map(|d| ChangeId(d.clone())).collect();
            let (c, seq) = tx
                .set_change_dependencies(&ChangeId(m.change_id.clone()), &deps)
                .await?;
            let mut c = convert::change(&c);
            c.seq = seq;
            Ok(pb::SetChangeDependenciesResponse { change: Some(c) })
        })
        .await
    }

    /// Edits the header of a change: its title, intent, goal, status and data (ADR 0110: `change.edited`).
    async fn update_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::UpdateChangeRequest>,
    ) -> Answer<pb::UpdateChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let data = m.data.as_ref().map(convert::map);
        if data.as_ref().is_some_and(|d| d.contains_key("criticality")) {
            return Err(connect_error(GraphError::unimplemented(
                "the criticality of a change (ADR 0075): set by the Go service",
            )));
        }
        let patch = ChangePatch {
            title: m.title,
            intent: m.intent,
            goal: m.goal,
            status: m.status,
            data,
        };
        self.write(caller, async |tx| {
            let w = tx.update_change(&ChangeId(m.id.clone()), patch).await?;
            Ok(pb::UpdateChangeResponse {
                change: Some(whole_pb(&w)?),
            })
        })
        .await
    }

    /// Appends items to the blackboard of a change (artifacts, signals, the types no code reads; ADR 0119, 0128).
    async fn add_items(&self, _ctx: &Ctx, req: Request<pb::AddItemsRequest>) -> Answer<pb::AddItemsResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let its = item_writes(m.items)?;
        self.write(caller, async |tx| {
            let written = tx.add_items(&ChangeId(m.change_id.clone()), its).await?;
            let items = written
                .iter()
                .map(convert::item)
                .collect::<Result<Vec<_>, _>>()
                .map_err(GraphError::internal)?;
            Ok(pb::AddItemsResponse { items })
        })
        .await
    }

    /// What a change depends on, its dependents and the merge order of its open sub-changes (ADR 0108).
    async fn get_change_dependencies(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetChangeDependenciesRequest>,
    ) -> Answer<pb::GetChangeDependenciesResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            let d = graph_core::dependency::change_dependencies(tx, &id).await?;
            let pb = |ds: &[graph_core::dependency::Dependency]| {
                ds.iter()
                    .map(|d| pb::Dependency {
                        id: d.id.0.clone(),
                        title: d.title.clone(),
                        methodology: d.methodology.clone(),
                        status: d.status.clone(),
                        state: d.state.clone(),
                        parent_id: d.parent_id.0.clone(),
                        satisfied: d.satisfied,
                        reason: d.reason.clone(),
                    })
                    .collect()
            };
            Ok(pb::GetChangeDependenciesResponse {
                depends_on: pb(&d.depends_on),
                dependents: pb(&d.dependents),
                order: pb(&d.order),
            })
        })
        .await
    }

    /// The nodes a change starts from that other open changes of its namespace act on too (`SharedNodes`).
    async fn get_shared_nodes(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetSharedNodesRequest>,
    ) -> Answer<pb::GetSharedNodesResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            let Some(c) = tx.change(&id).await? else {
                return Err(GraphError::not_found(format!("change {id}")));
            };
            let open = tx
                .change_heads(&ChangesFilter {
                    status: vec!["draft".into(), "active".into(), "committed".into()],
                    ..ChangesFilter::default()
                })
                .await?;
            // the open changes acting on each node (`openChangeHolders`)
            let mut holders: HashMap<NodeId, Vec<&change::Change>> = HashMap::new();
            for o in &open {
                for (node, pre, post) in tx.impact_nodes(&o.id).await? {
                    if pre.is_some() || post {
                        let hs = holders.entry(node).or_default();
                        if !hs.iter().any(|h| h.id == o.id) {
                            hs.push(o);
                        }
                    }
                }
            }
            let mut seen = std::collections::HashSet::new();
            let mut nodes = Vec::new();
            for (node, pre, _) in tx.impact_nodes(&id).await? {
                let Some(pre) = pre.map(|v| NodeRef::new(node, v)) else {
                    continue;
                };
                if !seen.insert(pre.id.clone()) {
                    continue;
                }
                let others: Vec<String> = holders
                    .get(&pre.id)
                    .into_iter()
                    .flatten()
                    .filter(|o| o.id != id && namespace_of(&o.namespace) == namespace_of(&c.namespace))
                    .map(|o| o.id.0.clone())
                    .collect();
                if others.is_empty() {
                    continue;
                }
                let n = read::stored_node(tx, &pre).await?;
                nodes.push(pb::SharedNode {
                    node: Some(convert::node_ref(&pre)),
                    key: n.key,
                    changes: others,
                });
            }
            Ok(pb::GetSharedNodesResponse { nodes })
        })
        .await
    }

    /// The sub-changes of a change, oldest first, each with its impacts and without its items.
    async fn list_sub_changes(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListSubChangesRequest>,
    ) -> Answer<pb::ListSubChangesResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            let f = ChangesFilter {
                parent: id.clone(),
                ..ChangesFilter::default()
            };
            let mut changes = Vec::new();
            for c in tx.change_heads(&f).await? {
                let view = fold_change(tx, &c.id).await?;
                let mut p = convert::change(&c);
                p.nodes = view.fold.impacts.iter().map(convert::change_impact).collect();
                changes.push(p);
            }
            Ok(pb::ListSubChangesResponse { changes })
        })
        .await
    }

    /// A change whole (`Graph.ChangeChecked`): its impacts with the validation of their drafts, its items.
    async fn get_change(&self, _ctx: &Ctx, req: Request<pb::GetChangeRequest>) -> Answer<pb::GetChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let state = self.state.clone();
        self.read(caller, async |tx| {
            if m.at_seq > 0 {
                return Err(GraphError::unimplemented(
                    "a change as its log stood at a position (ADR 0153): not served here yet",
                ));
            }
            let cat = state.catalog(tx).await?;
            let w = whole::whole_change(tx, &cat, &ChangeId(m.id.clone())).await?;
            let c = whole_pb(&w)?;
            Ok(pb::GetChangeResponse { change: Some(c) })
        })
        .await
    }

    /// The blackboard of a change (`Graph.BlackboardIn`): the change as its main flow sees it, the nodes its impacts
    /// name with their links, their neighbours, its options and decision points; an option or a flow is Go's.
    async fn get_blackboard(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetBlackboardRequest>,
    ) -> Answer<pb::GetBlackboardResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let state = self.state.clone();
        self.read(caller, async |tx| {
            let cat = state.catalog(tx).await?;
            let id = ChangeId(m.change_id.clone());
            let items = wflow::items_of(tx, &id).await?;
            let fx = graph_core::flow::Flows::replay(&items);
            let flow = fx.resolve(&m.flow); // no flow: the active option (ADR 0032 §6)
            let w = whole::whole_change_on(tx, &cat, &id, Some(&flow)).await?;
            let at = chrono::Utc::now();
            let points = decision::points_of(tx, &id, at).await?;
            let view = graph_core::view::View {
                change: id,
                flow: if flow.is_empty() {
                    graph_core::flow::MAIN_FLOW.into()
                } else {
                    flow.clone()
                },
                ..Default::default()
            };
            let world = World::open_node(tx, &view).await?;
            let (views, neighbours) = world.blackboard().await?;
            let nodes = views
                .iter()
                .map(|v| pb::NodeView {
                    node: Some(convert::node(&v.node)),
                    latest: v.latest,
                    out: convert::links(&v.out),
                    r#in: convert::links(&v.r#in),
                    not_committable: !v.node.state.is_empty()
                        && cat
                            .lifecycle(&v.node.typ)
                            .is_some_and(|lc| !lc.committable(&v.node.state)),
                    ..Default::default()
                })
                .collect();
            let mut change = whole_pb(&w)?;
            // the items the flow sees (`Change.View`): those of its chain plain, the view carries no flow events
            let chain: Vec<&str> = fx.chain(&flow).iter().map(|f| f.id.as_str()).collect();
            change.items = fx
                .view(&items, &flow)
                .into_iter()
                .map(|i| {
                    let mut it = change.items[i].clone();
                    if chain.contains(&items[i].flow.as_str()) {
                        it.flow = String::new();
                    }
                    it
                })
                .collect();
            Ok(pb::GetBlackboardResponse {
                change: Some(change),
                options: fx.options().map(convert::flow).collect(),
                active_option: fx.active.clone(),
                nodes,
                neighbors: convert::nodes(&neighbours),
                decision_points: points.iter().map(convert::decision_point).collect(),
                at: convert::time(&Some(at)),
            })
        })
        .await
    }

    /// The state a change sees at a level (`Graph.ChangeView`).
    async fn get_change_view(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetChangeViewRequest>,
    ) -> Answer<pb::GetChangeViewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let level = ViewLevel::parse(&m.level)?;
            let id = ChangeId(m.change_id.clone());
            known_change(tx, &id).await?;
            let fx = wflow::flows_of(tx, &id).await?;
            let flow = fx.resolve(&m.flow); // no flow: the active option
            if !flow.is_empty() && fx.get(&flow).is_none() {
                return Err(GraphError::not_found(format!("flow {flow} of change {id}")));
            }
            let world = change_world(tx, &m.change_id, &m.flow).await?;
            let (h, state) = world.change_state(level).await?;
            let state = state.into_iter().map(|(n, v)| (n.0, v)).collect();
            Ok(pb::GetChangeViewResponse {
                baseline: Some(convert::baseline(&h, Some(state))),
            })
        })
        .await
    }

    /// The graph a change sees (`Graph.ChangeGraph`: the baseline it forked from with no option; `world`:
    /// `Graph.ChangeWorld`, its written view with its drafts).
    async fn get_change_graph(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetChangeGraphRequest>,
    ) -> Answer<pb::GetChangeGraphResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let world = change_world(tx, &m.change_id, &m.flow).await?;
            let fx = wflow::flows_of(tx, &ChangeId(m.change_id.clone())).await?;
            let on_option = fx.get(&fx.resolve(&m.flow)).is_some_and(|f| f.option.is_some());
            let (nodes, links) = if m.world || on_option {
                world.change_graph(m.world).await?
            } else {
                let state = if world.baseline.id.is_empty() {
                    HashMap::new()
                } else {
                    read::select(tx, &world.baseline, None).await?
                };
                let mut refs: Vec<NodeRef> = state.iter().map(|(n, v)| NodeRef::new(n.clone(), *v)).collect();
                refs.sort();
                if refs.is_empty() {
                    (Vec::new(), Vec::new())
                } else {
                    let out = tx.links_from(&refs, None).await?;
                    let within = out
                        .into_iter()
                        .filter(|l| l.to_node().is_some_and(|t| state.get(&t.id) == Some(&t.version)))
                        .collect();
                    (tx.nodes_at(&refs, "").await?, within)
                }
            };
            Ok(pb::GetChangeGraphResponse {
                nodes: convert::nodes(&nodes),
                links: convert::links(&links),
            })
        })
        .await
    }

    async fn list_changes(&self, _ctx: &Ctx, req: Request<pb::ListChangesRequest>) -> Answer<pb::ListChangesResponse> {
        let caller = Caller::of(&req)?;
        let subject = caller.subject.clone();
        let m = req.into_inner();
        self.read(caller, async |tx| {
            if !m.header_only {
                return Err(GraphError::unimplemented(
                    "a change read whole is a fold of its log (ADR 0153): not served here yet",
                ));
            }
            let f = ChangesFilter {
                namespace: m.namespace.clone(),
                owner_org: structure_ref(&m.owner_org)?,
                status: m.status.clone(),
                project: structure_ref(&m.project)?,
                query: m.query.clone(),
                ..ChangesFilter::default()
            };
            // a platform service by itself works on every project: only the personal changes of others are hidden
            let keep = |c: &change::Change| c.visible_to(&subject);
            if m.limit > 0 {
                let p = change::changes_page(tx, &f, change::page_limit(m.limit.into()), &m.cursor, keep).await?;
                return Ok(pb::ListChangesResponse {
                    changes: convert::changes(&p.changes),
                    next_cursor: p.next,
                    total: p.total as i32,
                });
            }
            let cs: Vec<_> = tx
                .change_heads(&f)
                .await?
                .into_iter()
                .filter(|c| f.matches(c) && keep(c))
                .collect();
            Ok(pb::ListChangesResponse {
                changes: convert::changes(&cs),
                ..Default::default()
            })
        })
        .await
    }

    async fn list_node_changes(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListNodeChangesRequest>,
    ) -> Answer<pb::ListNodeChangesResponse> {
        let caller = Caller::of(&req)?;
        let subject = caller.subject.clone();
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let (cs, next) = change::node_changes(
                tx,
                &NodeId(m.node_id.clone()),
                m.before,
                change::page_limit(m.limit.into()),
            )
            .await?;
            let cs: Vec<_> = cs.into_iter().filter(|c| c.visible_to(&subject)).collect();
            Ok(pb::ListNodeChangesResponse {
                changes: convert::changes(&cs),
                next,
            })
        })
        .await
    }

    /// Appends entries a use case writes to the logs of changes (the journal of the engine, ADR 0011, 0030).
    async fn append_log(&self, _ctx: &Ctx, req: Request<pb::AppendLogRequest>) -> Answer<pb::AppendLogResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        for e in &m.entries {
            if e.change_id.is_empty() || !e.r#type.contains('.') {
                return Err(connect_error(GraphError::invalid(
                    "a log entry needs a change and a type <stream>.<kind>",
                )));
            }
            let stream = e.r#type.split_once('.').map_or(e.r#type.as_str(), |(s, _)| s);
            if ["fact", "impact", "change"].contains(&stream) {
                return Err(connect_error(GraphError::invalid(format!(
                    "the {stream} stream of a log is written by the graph only"
                ))));
            }
        }
        self.write(caller, async |tx| {
            for e in &m.entries {
                let change = ChangeId(e.change_id.clone());
                if tx.store.change(&change).await?.is_none() {
                    return Err(GraphError::unimplemented(
                        "an entry of a change the store does not hold: not served here yet",
                    ));
                }
                let at =
                    e.at.as_ref()
                        .and_then(|t| chrono::DateTime::from_timestamp(t.seconds, t.nanos as u32))
                        .unwrap_or_else(|| tx.env.now()); // none: now, as Go reads a zero time
                tx.append_log(&graph_core::impact::LogEntry {
                    seq: 0,
                    id: if e.id.is_empty() { tx.env.new_id() } else { e.id.clone() },
                    change,
                    typ: e.r#type.clone(),
                    flow: e.flow.clone(),
                    process: e.process_id.clone(),
                    execution: e.execution.clone(),
                    subject: e.subject.clone(),
                    by: e.by.clone(),
                    at,
                    causation: e.causation.clone(),
                    payload: e.payload.clone(),
                })
                .await?;
            }
            Ok(pb::AppendLogResponse {})
        })
        .await
    }

    /// The execution journal of a change and / or of processes, as records.
    async fn list_executions(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListExecutionsRequest>,
    ) -> Answer<pb::ListExecutionsResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.read(caller, async |tx| {
            if m.change_id.is_empty() && m.process_ids.is_empty() {
                // Go joins the graph's invalid with the journal's
                return Err(GraphError::new(
                    Kind::Invalid,
                    "invalid\njournal filter needs a change or processes: invalid journal",
                ));
            }
            let f = LogFilter {
                change: m.change_id.clone(),
                types: vec![format!("{}.", crate::journal::STREAM)],
                flows: None,
                processes: m.process_ids.clone(),
                execution: String::new(),
                after_seq: 0,
                limit: 0,
            };
            if !f.change.is_empty() && tx.change(&ChangeId(f.change.clone())).await?.is_none() {
                return Err(GraphError::not_found(format!("change {}", f.change)));
            }
            let (entries, _) = tx.change_log(&f).await?;
            Ok(pb::ListExecutionsResponse {
                records: crate::journal::records(&entries).map_err(GraphError::internal)?,
            })
        })
        .await
    }

    /// The flows of a change, oldest first (ADR 0017, 0025).
    async fn list_flows(&self, _ctx: &Ctx, req: Request<pb::ListFlowsRequest>) -> Answer<pb::ListFlowsResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            known_change(tx, &id).await?;
            let fx = wflow::flows_of(tx, &id).await?;
            Ok(pb::ListFlowsResponse {
                flows: fx.list.iter().map(convert::flow).collect(),
            })
        })
        .await
    }

    /// The options of a change, oldest first, and the one it works on (ADR 0009 §3).
    async fn list_options(&self, _ctx: &Ctx, req: Request<pb::ListOptionsRequest>) -> Answer<pb::ListOptionsResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            known_change(tx, &id).await?;
            let fx = wflow::flows_of(tx, &id).await?;
            let options: Vec<_> = fx.options().collect();
            Ok(pb::ListOptionsResponse {
                options: options.iter().map(|f| convert::flow(f)).collect(),
                active: options
                    .iter()
                    .rfind(|f| f.active)
                    .map(|f| f.id.clone())
                    .unwrap_or_default(),
            })
        })
        .await
    }

    /// Opens a flow branch: the items it invalidates are stale until it is adopted.
    async fn open_flow(&self, _ctx: &Ctx, req: Request<pb::OpenFlowRequest>) -> Answer<pb::OpenFlowResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let r#in = OpenFlow {
            parent: m.parent,
            fork_after: m.fork_after,
            seeds: m.seeds,
            stale_runs: m.stale_runs,
            origin: m.origin.as_ref().map(convert::map).unwrap_or_default(),
            items: item_writes(m.items)?,
        };
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let f = tx.open_flow(&id, r#in).await?;
            Ok(pb::OpenFlowResponse {
                flow: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// Adopts an open flow branch.
    async fn adopt_flow(&self, _ctx: &Ctx, req: Request<pb::AdoptFlowRequest>) -> Answer<pb::AdoptFlowResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let f = tx.decide_flow_op(&id, &m.flow, OP_ADOPT, &by).await?;
            Ok(pb::AdoptFlowResponse {
                flow: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// Discards an open flow branch.
    async fn discard_flow(&self, _ctx: &Ctx, req: Request<pb::DiscardFlowRequest>) -> Answer<pb::DiscardFlowResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let f = tx.decide_flow_op(&id, &m.flow, OP_DISCARD, &by).await?;
            Ok(pb::DiscardFlowResponse {
                flow: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// Opens an option: a flow branch of its own, forked from the main flow.
    async fn open_option(&self, _ctx: &Ctx, req: Request<pb::OpenOptionRequest>) -> Answer<pb::OpenOptionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        let named = !m.name.trim().is_empty();
        self.write(caller, async |tx| {
            let id = if named {
                served_change(tx, &m.change_id).await?
            } else {
                ChangeId(m.change_id.clone())
            };
            let f = tx.open_option(&id, &m.name, &m.hypothesis, m.activate, &by).await?;
            Ok(pb::OpenOptionResponse {
                option: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// Makes an open option the one the change works on; empty or "main" goes back to the main flow.
    async fn activate_option(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ActivateOptionRequest>,
    ) -> Answer<pb::ActivateOptionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let active = tx.activate_option(&id, &m.option, &by).await?;
            Ok(pb::ActivateOptionResponse { active })
        })
        .await
    }

    /// Records the evaluation of an open option.
    async fn evaluate_option(
        &self,
        _ctx: &Ctx,
        req: Request<pb::EvaluateOptionRequest>,
    ) -> Answer<pb::EvaluateOptionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        let commented = !m.comment.trim().is_empty();
        self.write(caller, async |tx| {
            let id = if commented {
                served_change(tx, &m.change_id).await?
            } else {
                ChangeId(m.change_id.clone())
            };
            let f = tx.evaluate_option(&id, &m.option, &by, &m.comment).await?;
            Ok(pb::EvaluateOptionResponse {
                option: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// Selects an open option: its flow adopted, the other open options rejected.
    async fn select_option(
        &self,
        _ctx: &Ctx,
        req: Request<pb::SelectOptionRequest>,
    ) -> Answer<pb::SelectOptionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let f = tx.select_option(&id, &m.option, &by).await?;
            Ok(pb::SelectOptionResponse {
                option: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// Rejects an open option: its flow is discarded.
    async fn reject_option(
        &self,
        _ctx: &Ctx,
        req: Request<pb::RejectOptionRequest>,
    ) -> Answer<pb::RejectOptionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let f = tx.reject_option(&id, &m.option, &by).await?;
            Ok(pb::RejectOptionResponse {
                option: Some(convert::flow(&f)),
            })
        })
        .await
    }

    /// The decision points of a change: none in the simple world.
    async fn list_decision_points(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ListDecisionPointsRequest>,
    ) -> Answer<pb::ListDecisionPointsResponse> {
        let caller = Caller::of(&req)?;
        let id = ChangeId(req.into_inner().change_id);
        self.read(caller, async |tx| {
            known_change(tx, &id).await?;
            let points = decision::points_of(tx, &id, chrono::Utc::now()).await?;
            Ok(pb::ListDecisionPointsResponse {
                points: points.iter().map(convert::decision_point).collect(),
            })
        })
        .await
    }

    /// Opens a decision point on a change (ADR 0009 §4).
    async fn open_decision(
        &self,
        _ctx: &Ctx,
        req: Request<pb::OpenDecisionRequest>,
    ) -> Answer<pb::OpenDecisionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        let policy = m.policy.as_ref().map(convert::map).unwrap_or_default();
        // what is refused before the change is read
        let checked =
            !m.question.trim().is_empty() && graph_core::decision::open_policy(&policy, chrono::Utc::now()).is_ok();
        let r#in = OpenDecision {
            question: m.question,
            options: if m.all_options { None } else { Some(m.options) },
            criteria: m.criteria,
            policy,
            choices: m.choices,
            by,
        };
        self.write(caller, async |tx| {
            let id = if checked {
                served_change(tx, &m.change_id).await?
            } else {
                ChangeId(m.change_id.clone())
            };
            let d = tx.open_decision(&id, r#in).await?;
            Ok(pb::OpenDecisionResponse {
                point: Some(convert::decision_point(&d)),
            })
        })
        .await
    }

    /// Rules a decision point.
    async fn rule_decision(
        &self,
        _ctx: &Ctx,
        req: Request<pb::RuleDecisionRequest>,
    ) -> Answer<pb::RuleDecisionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        let r#in = Rule {
            point: m.point,
            outcome: m.outcome,
            option: m.option,
            confidence: m.confidence,
            justification: m.justification,
            questions: m.questions,
            human: !m.agent,
            by,
            role: m.role,
        };
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let d = tx.rule_decision(&id, r#in).await?;
            Ok(pb::RuleDecisionResponse {
                point: Some(convert::decision_point(&d)),
            })
        })
        .await
    }

    /// Answers an open question of a decision point.
    async fn answer_question(
        &self,
        _ctx: &Ctx,
        req: Request<pb::AnswerQuestionRequest>,
    ) -> Answer<pb::AnswerQuestionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = if m.answer.trim().is_empty() {
                ChangeId(m.change_id.clone())
            } else {
                served_change(tx, &m.change_id).await?
            };
            let d = tx.answer_question(&id, &m.question, &m.answer, &m.process, &by).await?;
            Ok(pb::AnswerQuestionResponse {
                point: Some(convert::decision_point(&d)),
            })
        })
        .await
    }

    /// Ratifies or refuses the ruling of a decision point the policy did not settle.
    async fn ratify_decision(
        &self,
        _ctx: &Ctx,
        req: Request<pb::RatifyDecisionRequest>,
    ) -> Answer<pb::RatifyDecisionResponse> {
        let caller = Caller::of(&req)?;
        let by = caller.subject.clone();
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let d = tx.ratify_decision(&id, &m.point, m.accept, &by, &m.comment).await?;
            Ok(pb::RatifyDecisionResponse {
                point: Some(convert::decision_point(&d)),
            })
        })
        .await
    }

    /// The stack of changes producing a node version or a baseline (ADR 0133).
    async fn get_change_stack(
        &self,
        _ctx: &Ctx,
        req: Request<pb::GetChangeStackRequest>,
    ) -> Answer<pb::GetChangeStackResponse> {
        let caller = Caller::of(&req)?;
        let subject = caller.subject.clone();
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let node = NodeRef::new(NodeId(m.node_id.clone()), m.version);
            let mut s =
                change::change_stack(tx, &node, &BaselineId(m.baseline_id.clone()), m.limit.max(0) as usize).await?;
            for c in &mut s.changes {
                if c.owner_org.key.starts_with(change::USER_PREFIX)
                    && c.owner_org.key[change::USER_PREFIX.len()..] != *subject
                {
                    c.title.clear(); // the title of the personal changes of others (ADR 0037)
                }
            }
            Ok(pb::GetChangeStackResponse {
                stack: Some(pb::ChangeStack {
                    changes: convert::stack_changes(&s.changes),
                    versions: s
                        .versions
                        .iter()
                        .map(|v| pb::VersionStep {
                            version: v.version,
                            branch: v.branch.clone(),
                            reason: v.reason.clone(),
                            parents: v.parents.clone(),
                            change: v.change.0.clone(),
                            joined: v
                                .joined
                                .iter()
                                .map(|j| pb::VersionJoin {
                                    branch: j.branch.clone(),
                                    change: j.change.0.clone(),
                                })
                                .collect(),
                            created_at: convert::time(&v.created_at),
                        })
                        .collect(),
                    baselines: s
                        .baselines
                        .iter()
                        .map(|b| pb::BaselineStep {
                            id: b.id.0.clone(),
                            name: b.name.clone(),
                            branch: b.branch.clone(),
                            kind: b.kind.clone(),
                            change: b.change.0.clone(),
                            parent: b.parent.0.clone(),
                            merged_from: b.merged_from.0.clone(),
                            rebased_from: b.rebased_from.0.clone(),
                            created_at: convert::time(&b.created_at),
                            merged: b.merged.iter().map(|c| c.0.clone()).collect(),
                        })
                        .collect(),
                    truncated: s.truncated,
                }),
            })
        })
        .await
    }

    async fn diff_baselines(
        &self,
        _ctx: &Ctx,
        req: Request<pb::DiffBaselinesRequest>,
    ) -> Answer<pb::DiffBaselinesResponse> {
        let caller = Caller::of(&req)?;
        let subject = caller.subject.clone();
        let m = req.into_inner();
        self.read(caller, async |tx| {
            let (from, to) = (m.from.as_str().into(), m.to.as_str().into());
            let (ds, gap) = change::diff_baselines(tx, &from, &to).await?;
            // the title of the personal changes of others is left out of a stack: their id stays (ADR 0037)
            let readable = |mut cs: Vec<change::StackChange>| {
                for c in &mut cs {
                    if c.owner_org.key.starts_with(change::USER_PREFIX)
                        && c.owner_org.key[change::USER_PREFIX.len()..] != *subject
                    {
                        c.title.clear();
                    }
                }
                cs
            };
            let added = readable(change::stack_changes(tx, &gap.added).await?);
            let removed = readable(change::stack_changes(tx, &gap.removed).await?);
            Ok(pb::DiffBaselinesResponse {
                nodes: ds.iter().map(convert::baseline_diff).collect(),
                changes_added: convert::stack_changes(&added),
                changes_removed: convert::stack_changes(&removed),
            })
        })
        .await
    }

    async fn create_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::CreateChangeRequest>,
    ) -> Answer<pb::CreateChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let token_project = caller.project.clone();
        self.write(caller, async |tx| {
            if !m.parent_id.is_empty() {
                return Err(GraphError::unimplemented(
                    "a sub-change (ADR 0103): not served here yet",
                ));
            }
            if m.owner_org == "@me" || m.owner_org.starts_with(change::USER_PREFIX) || m.owner_org.contains("/USR:") {
                return Err(GraphError::unimplemented(
                    "a personal change (ADR 0037): not served here yet",
                ));
            }
            if is_platform_namespace(&m.namespace) {
                return Err(GraphError::unimplemented(
                    "a change of the platform's namespaces: not served here yet",
                ));
            }
            let project = if !m.project_id.is_empty() {
                m.project_id.clone()
            } else if !token_project.is_empty() {
                token_project.clone()
            } else {
                tx.structure_root(STRUCTURE_PROJECT)?
            };
            if project_stopped(tx, &project).await? {
                return Err(GraphError::new(
                    Kind::Conflict,
                    format!("project {project} is stopped: it takes no new change"),
                ));
            }
            let data = m.data.as_ref().map(convert::map);
            valid_criticality(data.as_ref().unwrap_or(&serde_json::Map::new()))?;
            let c = tx
                .create_change(NewChange {
                    title: m.title.clone(),
                    intent: m.intent.clone(),
                    methodology: m.methodology.clone(),
                    methodology_version: m.methodology_version.clone(),
                    namespace: m.namespace.clone(),
                    baseline_id: m.baseline_id.as_str().into(),
                    branch: m.branch.clone(),
                    own_branch: m.own_branch,
                    owner_org: m.owner_org.clone(),
                    project_id: project,
                    data,
                })
                .await?;
            Ok(pb::CreateChangeResponse {
                change: Some(convert::change(&c)),
            })
        })
        .await
    }

    /// Moves a change along its lifecycle: with no registry wired, no change follows one (`Graph.TransitionChange`).
    async fn transition_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::TransitionChangeRequest>,
    ) -> Answer<pb::TransitionChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let c = tx.transition_change(&m.change_id.as_str().into()).await?;
            Ok(pb::TransitionChangeResponse {
                change: Some(convert::change(&c)),
            })
        })
        .await
    }

    /// Moves a change to another version of its methodology: with no registry wired, refused (`Graph.UpgradeChange`).
    async fn upgrade_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::UpgradeChangeRequest>,
    ) -> Answer<pb::UpgradeChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let c = tx.upgrade_change(&m.change_id.as_str().into()).await?;
            Ok(pb::UpgradeChangeResponse {
                change: Some(convert::change(&c)),
            })
        })
        .await
    }

    async fn delete_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::DeleteChangeRequest>,
    ) -> Answer<pb::DeleteChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id: ChangeId = m.change_id.as_str().into();
            let c = tx.change_head(&id).await?;
            if c.is_personal() || is_platform_namespace(&c.namespace) || !c.parent_id.is_empty() {
                return Err(GraphError::unimplemented(
                    "a personal change, a sub-change or a change of the platform's namespaces: not served here yet",
                ));
            }
            let c = tx.purge_change(&id).await?;
            Ok(pb::DeleteChangeResponse {
                change: Some(convert::change(&c)),
            })
        })
        .await
    }
    /// Proposes impacts on a change (`Graph.ProposeImpact`): modifications of existing nodes, planned creations.
    async fn propose_impact(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ProposeImpactRequest>,
    ) -> Answer<pb::ProposeImpactResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        caller.administers().map_err(connect_error)?;
        if m.nodes.iter().any(|cn| !cn.reviews.is_empty() || cn.superseded) {
            return Err(connect_error(GraphError::unimplemented(
                "an impact proposed with reviews: answered by the Go service",
            )));
        }
        let opt = |r: &Option<pb::NodeRef>| r.as_ref().map(|r| convert::ref_from(Some(r)));
        let batch: Vec<ChangeImpact> = m
            .nodes
            .iter()
            .map(|cn| ChangeImpact {
                id: cn.id.clone(),
                key: cn.key.clone(),
                typ: cn.r#type.clone(),
                intent: cn.intent.clone(),
                rationale: cn.rationale.clone(),
                pre: opt(&cn.pre),
                post: opt(&cn.post),
                committed: opt(&cn.committed),
                review: cn.review.clone(),
                via: cn.via.clone(),
                recheck: cn.recheck,
                produced_by: cn.produced_by.clone(),
                derived_from: cn.derived_from.clone(),
                execution: cn.execution.clone(),
                items: cn.items.clone(),
                flow: cn.flow.clone(),
                ..ChangeImpact::default()
            })
            .collect();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let out = tx.propose_impacts(&id, batch).await?;
            Ok(pb::ProposeImpactResponse {
                nodes: out.iter().map(convert::change_impact).collect(),
            })
        })
        .await
    }

    async fn impact_node_create(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeCreateRequest>,
    ) -> Answer<pb::ImpactNodeCreateResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        caller.administers().map_err(connect_error)?;
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let links = convert::link_writes(&m.links).map_err(|e| GraphError::new(Kind::Invalid, e))?;
            let cn = tx
                .impact_node_create(
                    &id,
                    NodeCreate {
                        key: m.key.clone(),
                        typ: m.r#type.clone(),
                        props: m.props.as_ref().map(convert::map).filter(|p| !p.is_empty()),
                        owner: m.owner.clone(),
                        rationale: m.rationale.clone(),
                        links,
                        execution: m.execution.clone(),
                        produced_by: by,
                    },
                    &m.flow,
                )
                .await?;
            Ok(pb::ImpactNodeCreateResponse {
                node: Some(convert::change_impact(&cn)),
            })
        })
        .await
    }

    async fn impact_node_merge(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeMergeRequest>,
    ) -> Answer<pb::ImpactNodeRestructureResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        caller.administers().map_err(connect_error)?;
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let into = create_spec(m.into.as_ref(), &m.execution, &by)?;
            let sources = m.sources.iter().map(node_name).collect();
            let out = tx
                .impact_node_merge(
                    &id,
                    sources,
                    into,
                    m.rationale.clone(),
                    m.flow.clone(),
                    m.execution.clone(),
                )
                .await?;
            Ok(restructured(&out))
        })
        .await
    }

    async fn impact_node_split(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeSplitRequest>,
    ) -> Answer<pb::ImpactNodeRestructureResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        caller.administers().map_err(connect_error)?;
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let into = m
                .into
                .iter()
                .map(|s| create_spec(Some(s), &m.execution, &by))
                .collect::<Result<Vec<_>, _>>()?;
            let source = m.source.as_ref().map(node_name).unwrap_or_default();
            let out = tx
                .impact_node_split(
                    &id,
                    source,
                    into,
                    m.rationale.clone(),
                    m.flow.clone(),
                    m.execution.clone(),
                )
                .await?;
            Ok(restructured(&out))
        })
        .await
    }

    async fn impact_node_checkout(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeCheckoutRequest>,
    ) -> Answer<pb::ImpactNodeCheckoutResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let mut out = tx
                .impact_node_checkout(
                    &id,
                    &m.flow,
                    vec![NodeCheckout {
                        impact: m.change_impact_id.clone(),
                        node: m.node_id.as_str().into(),
                        key: String::new(),
                        rationale: m.rationale.clone(),
                        execution: m.execution.clone(),
                        produced_by: by,
                    }],
                )
                .await?;
            Ok(pb::ImpactNodeCheckoutResponse {
                node: out.pop().map(|cn| convert::change_impact(&cn)),
            })
        })
        .await
    }

    async fn impact_node_checkout_batch(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeCheckoutBatchRequest>,
    ) -> Answer<pb::ImpactNodeCheckoutBatchResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let nodes = m
                .nodes
                .iter()
                .map(|n| NodeCheckout {
                    impact: n.change_impact_id.clone(),
                    node: n.node_id.as_str().into(),
                    key: n.key.clone(),
                    rationale: n.rationale.clone(),
                    execution: m.execution.clone(),
                    produced_by: by.clone(),
                })
                .collect();
            let out = tx.impact_node_checkout(&id, &m.flow, nodes).await?;
            Ok(pb::ImpactNodeCheckoutBatchResponse {
                nodes: out.iter().map(convert::change_impact).collect(),
            })
        })
        .await
    }

    async fn impact_node_update(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeUpdateRequest>,
    ) -> Answer<pb::ImpactNodeUpdateResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let props = m.props.as_ref().map(convert::map).unwrap_or_default();
            let cn = tx
                .impact_node_update(&id, &m.change_impact_id, props, &m.owner, &m.flow, &m.execution)
                .await?;
            Ok(pb::ImpactNodeUpdateResponse {
                node: Some(convert::change_impact(&cn)),
            })
        })
        .await
    }

    async fn impact_node_transition(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeTransitionRequest>,
    ) -> Answer<pb::ImpactNodeTransitionResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        self.write(caller, async |tx| {
            if m.state.is_empty() {
                return Err(GraphError::invalid("a transition names the state it goes to"));
            }
            let id = served_change(tx, &m.change_id).await?;
            let n = NodeCheckout {
                impact: m.change_impact_id.clone(),
                node: m.node_id.as_str().into(),
                key: String::new(),
                rationale: m.rationale.clone(),
                execution: m.execution.clone(),
                produced_by: by,
            };
            let cn = tx.impact_node_transition(&id, &m.flow, n, &m.state).await?;
            Ok(pb::ImpactNodeTransitionResponse {
                node: Some(convert::change_impact(&cn)),
            })
        })
        .await
    }

    async fn impact_node_cancel(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeCancelRequest>,
    ) -> Answer<pb::ImpactNodeCancelResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let cn = tx
                .impact_node_cancel(&id, &m.change_impact_id, &m.flow, &m.execution)
                .await?;
            Ok(pb::ImpactNodeCancelResponse {
                node: Some(convert::change_impact(&cn)),
            })
        })
        .await
    }

    async fn withdraw_impact(
        &self,
        _ctx: &Ctx,
        req: Request<pb::WithdrawImpactRequest>,
    ) -> Answer<pb::WithdrawImpactResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            tx.withdraw_impacts(&id, std::slice::from_ref(&m.change_impact_id), &m.flow, &m.execution)
                .await?;
            Ok(pb::WithdrawImpactResponse {})
        })
        .await
    }

    /// Takes several impacts out of a change in one transaction: all of them or none.
    async fn withdraw_impacts(
        &self,
        _ctx: &Ctx,
        req: Request<pb::WithdrawImpactsRequest>,
    ) -> Answer<pb::WithdrawImpactsResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            if m.change_impact_ids.is_empty() {
                return Err(GraphError::invalid("no change impact to withdraw"));
            }
            let id = served_change(tx, &m.change_id).await?;
            tx.withdraw_impacts(&id, &m.change_impact_ids, &m.flow, &m.execution)
                .await?;
            Ok(pb::WithdrawImpactsResponse {})
        })
        .await
    }

    async fn impact_link_create(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactLinkCreateRequest>,
    ) -> Answer<pb::ImpactLinkCreateResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let l = convert::link_write(&m.r#type, m.to.as_ref(), &m.target, m.props.as_ref())
                .map_err(|e| GraphError::new(Kind::Invalid, e))?;
            let link = tx
                .impact_link_create(&id, &m.change_impact_id, l, &m.flow, &m.execution)
                .await?;
            Ok(pb::ImpactLinkCreateResponse {
                link: Some(convert::link(&link)),
            })
        })
        .await
    }

    async fn impact_link_update(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactLinkUpdateRequest>,
    ) -> Answer<pb::ImpactLinkUpdateResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let props = m.props.as_ref().map(convert::map);
            let link = tx
                .impact_link_update(&id, &m.link_id.as_str().into(), props, &m.flow, &m.execution)
                .await?;
            Ok(pb::ImpactLinkUpdateResponse {
                link: Some(convert::link(&link)),
            })
        })
        .await
    }

    async fn impact_link_delete(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactLinkDeleteRequest>,
    ) -> Answer<pb::ImpactLinkDeleteResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            tx.impact_link_delete(&id, &m.link_id.as_str().into(), &m.flow, &m.execution)
                .await?;
            Ok(pb::ImpactLinkDeleteResponse {})
        })
        .await
    }

    async fn commit_change(
        &self,
        _ctx: &Ctx,
        req: Request<pb::CommitChangeRequest>,
    ) -> Answer<pb::CommitChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let b = tx.commit_change(&id, &m.baseline_name).await?;
            Ok(pb::CommitChangeResponse {
                baseline: Some(convert::baseline(&b, None)),
            })
        })
        .await
    }

    async fn commit_edits(&self, _ctx: &Ctx, req: Request<pb::CommitEditsRequest>) -> Answer<pb::CommitEditsResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let by = caller.subject.clone();
        let token_project = caller.project.clone();
        if !m.events.is_empty() {
            return Err(connect_error(GraphError::unimplemented(
                "a commit carrying the events of a service (ADR 0136): not served here yet",
            )));
        }
        if m.owner_org == "@me" || m.owner_org.starts_with(change::USER_PREFIX) || m.owner_org.contains("/USR:") {
            return Err(connect_error(GraphError::unimplemented(
                "a personal change (ADR 0037): not served here yet",
            )));
        }
        if is_platform_namespace(&m.namespace) {
            return Err(connect_error(GraphError::unimplemented(
                "a change of the platform's namespaces: not served here yet",
            )));
        }
        let edits: Vec<NodeEdit> = m
            .edits
            .iter()
            .map(|e| NodeEdit {
                key: e.key.clone(),
                typ: e.r#type.clone(),
                pre: e.pre.as_ref().map(|p| convert::ref_from(Some(p))),
                props: e.props.as_ref().map(convert::map).filter(|p| !p.is_empty()),
                rationale: e.rationale.clone(),
                links: e
                    .links
                    .iter()
                    .map(|l| LinkEdit {
                        typ: l.r#type.clone(),
                        to: l.to.as_ref().map(|t| convert::ref_from(Some(t))),
                        to_key: l.to_key.clone(),
                        props: l.props.as_ref().map(convert::map).filter(|p| !p.is_empty()),
                    })
                    .collect(),
                remove_links: e.remove_links.iter().map(|l| l.as_str().into()).collect(),
                owner: e.owner.clone(),
                state: e.state.clone(),
            })
            .collect();
        self.write(caller, async |tx| {
            // the access nodes are gated by the service (`gateAccess`)
            for e in &edits {
                let mut typ = e.typ.clone();
                if let Some(pre) = &e.pre
                    && let Ok(n) = read::stored_node(tx.store, pre).await
                {
                    typ = n.typ;
                }
                if tx.catalog().admin_only(&typ) {
                    return Err(GraphError::unimplemented(
                        "a commit of an access node (ADR 0068): not served here yet",
                    ));
                }
            }
            let project = if !m.project_id.is_empty() {
                m.project_id.clone()
            } else if !token_project.is_empty() {
                token_project.clone()
            } else {
                tx.structure_root(STRUCTURE_PROJECT)?
            };
            let out = tx
                .commit_edits(Commit {
                    namespace: m.namespace.clone(),
                    title: m.title.clone(),
                    intent: m.intent.clone(),
                    methodology: m.methodology.clone(),
                    data: m.data.as_ref().map(convert::map).filter(|d| !d.is_empty()),
                    baseline: m.baseline_id.as_str().into(),
                    by,
                    baseline_name: m.baseline_name.clone(),
                    edits,
                    owner_org: m.owner_org.clone(),
                    project_id: project,
                })
                .await?;
            Ok(pb::CommitEditsResponse {
                change_id: out.change.0.clone(),
                baseline: Some(convert::baseline(&out.baseline, None)),
            })
        })
        .await
    }

    async fn merge_change(&self, _ctx: &Ctx, req: Request<pb::MergeChangeRequest>) -> Answer<pb::MergeChangeResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        if !m.resolutions.is_empty() {
            return Err(connect_error(GraphError::unimplemented(
                "a merge given resolutions (a three-way merge)",
            )));
        }
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let r = tx.merge_change(&id).await?;
            let mut c = convert::change(&r.change);
            c.nodes = r.impacts.iter().map(convert::change_impact).collect();
            c.items = r
                .items
                .iter()
                .map(convert::item)
                .collect::<Result<_, _>>()
                .map_err(|e| GraphError::unimplemented(format!("{e}: not served here yet")))?;
            c.seq = r.seq;
            Ok(pb::MergeChangeResponse { change: Some(c) })
        })
        .await
    }

    /// Starts a review object on a flow (ADR 0080).
    async fn review_open(&self, _ctx: &Ctx, req: Request<pb::ReviewOpenRequest>) -> Answer<pb::ReviewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let who = reviewer(&caller);
        self.write(caller, async |tx| {
            let r = tx
                .review_open(&m.change_id.as_str().into(), &m.flow, &m.comment, &who)
                .await?;
            Ok(review_pb(&r))
        })
        .await
    }

    /// Changes an open review object: its comment, its impacts, the comments and outcomes of its entries.
    async fn review_update(&self, _ctx: &Ctx, req: Request<pb::ReviewUpdateRequest>) -> Answer<pb::ReviewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let who = reviewer(&caller);
        let edit = ReviewEdit {
            comment: m.set_comment.then(|| m.comment.clone()),
            remove: m.remove.clone(),
            add: m.add.clone(),
            entries: m
                .entries
                .iter()
                .map(|x| EntryEdit {
                    impact: x.change_impact_id.clone(),
                    comment: x.set_comment.then(|| x.comment.clone()),
                    outcome: x.set_outcome.then(|| x.outcome.clone()),
                })
                .collect(),
        };
        self.write(caller, async |tx| {
            let r = tx
                .review_update(&m.change_id.as_str().into(), &m.key, edit, &who)
                .await?;
            Ok(review_pb(&r))
        })
        .await
    }

    /// Submits a review object: its entries as the verdicts of one batch, all of them or none.
    async fn review_submit(&self, _ctx: &Ctx, req: Request<pb::ReviewSubmitRequest>) -> Answer<pb::ReviewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let who = reviewer(&caller);
        self.write(caller, async |tx| {
            let r = tx.review_submit(&m.change_id.as_str().into(), &m.key, &who).await?;
            Ok(review_pb(&r))
        })
        .await
    }

    /// Drops an open review object.
    async fn review_discard(&self, _ctx: &Ctx, req: Request<pb::ReviewDiscardRequest>) -> Answer<pb::ReviewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        let who = reviewer(&caller);
        self.write(caller, async |tx| {
            let r = tx.review_discard(&m.change_id.as_str().into(), &m.key, &who).await?;
            Ok(review_pb(&r))
        })
        .await
    }

    /// Reviews one change impact: a batch of one (`Graph.ImpactNodeReviewOn`).
    async fn impact_node_review(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeReviewRequest>,
    ) -> Answer<pb::ImpactNodeReviewResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let b = ReviewBatch {
                id: String::new(),
                flow: m.flow.clone(),
                execution: m.execution.clone(),
                verdicts: vec![Verdict {
                    impact: m.change_impact_id.clone(),
                    accept: m.accept,
                    comment: m.comment.clone(),
                }],
                ..ReviewBatch::default()
            };
            let out = tx.impact_node_review_batch(&id, b).await?;
            Ok(pb::ImpactNodeReviewResponse {
                node: out.first().map(convert::change_impact),
            })
        })
        .await
    }

    async fn impact_node_review_batch(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeReviewBatchRequest>,
    ) -> Answer<pb::ImpactNodeReviewBatchResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        self.write(caller, async |tx| {
            let id = served_change(tx, &m.change_id).await?;
            let b = ReviewBatch {
                id: m.review_id.clone(),
                flow: m.flow.clone(),
                execution: m.execution.clone(),
                verdicts: m
                    .verdicts
                    .iter()
                    .map(|v| Verdict {
                        impact: v.change_impact_id.clone(),
                        accept: v.accept,
                        comment: v.comment.clone(),
                    })
                    .collect(),
                record: m
                    .item
                    .as_ref()
                    .map(|it| it.data.as_ref().map(convert::map).unwrap_or_default()),
                record_type: m.item.as_ref().map(|it| it.r#type.clone()).unwrap_or_default(),
            };
            let out = tx.impact_node_review_batch(&id, b).await?;
            Ok(pb::ImpactNodeReviewBatchResponse {
                nodes: out.iter().map(convert::change_impact).collect(),
            })
        })
        .await
    }
}

/// The actor of a review call (`graphsvc.Handler.reviewer`): the writes served here are a platform service's, which
/// acts on the reviews of others as an administrator does.
fn reviewer(caller: &Caller) -> Actor {
    Actor {
        subject: caller.subject.clone(),
        admin: true,
    }
}

/// A review object as the answers carry it (`pbconv.ReviewToPB`).
fn review_pb(r: &ReviewObject) -> pb::ReviewResponse {
    pb::ReviewResponse {
        review: Some(pb::ReviewRecord {
            key: r.key.clone(),
            flow: r.flow.clone(),
            comment: r.comment.clone(),
            status: r.status.clone(),
            entries: r
                .entries
                .iter()
                .map(|e| pb::ReviewEntry {
                    change_impact_id: e.impact.clone(),
                    comment: e.comment.clone(),
                    outcome: e.outcome.clone(),
                })
                .collect(),
            by: r.by.clone(),
            submitted_at: convert::time(&r.submitted_at),
            item: r.item.clone(),
            versions: r.versions,
        }),
    }
}

/// The namespaces the platform graph holds (ADR 0154 §9): their writes are checked by the platform's validators,
/// which the Go service runs.
fn is_platform_namespace(ns: &str) -> bool {
    matches!(
        graph_core::model::namespace_of(ns),
        graph_core::model::NAMESPACE_ORGANISATION
            | graph_core::model::NAMESPACE_PLATFORM
            | graph_core::model::NAMESPACE_METHODOLOGY
    )
}

/// Whether a project or one above it is stopped (`access.Snapshot.ProjectStopped`, ADR 0138): the projects at the
/// head of main, followed up through their parent links. A project the structure does not hold is not stopped.
/// The name of this graph's source in the listings (`graphsvc.PlatformSource`).
const PLATFORM_SOURCE: &str = "platform";

const MERGED_LISTING: &str = "a listing merged with the other graphs' answers: not served here yet";

/// Whether the platform declares other graphs holding namespaces (`Router.graphs`): their listings are merged into
/// this graph's, which Rust does not do yet.
async fn other_graphs(tx: &Session) -> Result<bool, GraphError> {
    let world = World::open(tx, &graph_core::view::View::branch("platform", MAIN_BRANCH)).await?;
    let q = NodeQuery {
        typ: "platform@Source".into(),
        limit: i32::MAX as usize,
        ..NodeQuery::default()
    };
    let str = |n: &graph_core::model::Node, k: &str| {
        n.props
            .as_ref()
            .and_then(|p| p.get(k))
            .and_then(serde_json::Value::as_str)
            .unwrap_or_default()
            .to_string()
    };
    Ok(world.page(&q).await?.nodes.iter().any(|n| {
        !n.deleted
            && n.state != "retired"
            && n.key.starts_with("SRC:")
            && str(n, "kind") == "graph"
            && !str(n, "location").is_empty()
            && n.props
                .as_ref()
                .and_then(|p| p.get("namespaces"))
                .and_then(serde_json::Value::as_array)
                .is_some_and(|a| a.iter().any(|v| v.as_str().is_some_and(|s| !s.is_empty())))
    }))
}

async fn project_stopped(tx: &Tx<'_, Session>, project: &str) -> Result<bool, GraphError> {
    let Some(st) = tx.catalog().structure(STRUCTURE_PROJECT).cloned() else {
        return Ok(false);
    };
    let world = World::open(tx.store, &graph_core::view::View::branch(&st.namespace, MAIN_BRANCH)).await?;
    let mut seen = std::collections::HashSet::new();
    let mut key = project.to_string();
    while !key.is_empty() && seen.insert(key.clone()) {
        let v = match world.node_view(&NodeId::default(), &key).await {
            Ok(v) => v,
            Err(e) if e.is_not_found() => return Ok(false),
            Err(e) => return Err(e),
        };
        if v.node
            .props
            .as_ref()
            .and_then(|p| p.get("stopped"))
            .and_then(serde_json::Value::as_bool)
            == Some(true)
        {
            return Ok(true);
        }
        key = String::new();
        for l in &v.out {
            if l.typ == st.parent || l.typ.rsplit('@').next() == Some(st.parent.rsplit('@').next().unwrap_or_default())
            {
                if let Some(to) = l.to_node() {
                    key = tx.store.node(to).await?.map(|n| n.key).unwrap_or_default();
                }
                break;
            }
        }
    }
    Ok(false)
}

/// A baseline header as a listing gives it: no state, the namespace asked.
fn listed_baseline(b: &graph_core::model::Baseline, namespace: &str) -> pb::Baseline {
    let mut p = convert::baseline(b, None);
    p.namespace = graph_core::model::namespace_of(namespace).to_string();
    p
}

fn tag_pb(t: Tag) -> pb::Tag {
    pb::Tag {
        id: t.id,
        name: t.name,
        namespace: t.namespace,
        node_id: t.node,
        version: t.version,
        by: t.by,
        created_at: convert::time(&t.created_at),
    }
}

/// The most nodes a delta carries (`graph.MaxDeltaNodes`): beyond, the baseline is read.
const MAX_DELTA_NODES: usize = 5000;

fn distribution_pb(d: &stats::Distribution) -> Option<pb::Distribution> {
    Some(pb::Distribution {
        min: d.min as i32,
        max: d.max as i32,
        mean: d.mean,
        median: d.median,
        p90: d.p90 as i32,
        sum: d.sum as i32,
    })
}

/// A state's shape as its message (`stateStatsToPB`).
fn state_stats_pb(s: stats::StateStats) -> pb::StateStats {
    let counts = |m: &HashMap<String, usize>| m.iter().map(|(k, v)| (k.clone(), *v as i32)).collect();
    pb::StateStats {
        nodes: s.nodes as i32,
        deleted: s.deleted as i32,
        links: s.links as i32,
        suspect: s.suspect as i32,
        external: s.external as i32,
        isolated: s.isolated as i32,
        self_links: s.self_links as i32,
        reciprocal: s.reciprocal as i32,
        density: s.density,
        component_count: s.component_count as i32,
        components: s.components.iter().map(|c| *c as i32).collect(),
        out: distribution_pb(&s.out),
        r#in: distribution_pb(&s.r#in),
        degree: distribution_pb(&s.degree),
        histogram: s
            .histogram
            .iter()
            .map(|b| pb::HistogramBucket {
                label: b.label.clone(),
                from: b.from as i32,
                to: b.to as i32,
                count: b.count as i32,
            })
            .collect(),
        versions: distribution_pb(&s.versions),
        properties: distribution_pb(&s.properties),
        types: s
            .types
            .iter()
            .map(|t| pb::NodeTypeStats {
                r#type: t.typ.clone(),
                nodes: t.nodes as i32,
                states: counts(&t.states),
                out: distribution_pb(&t.out),
                r#in: distribution_pb(&t.r#in),
                versions: distribution_pb(&t.versions),
                properties: distribution_pb(&t.properties),
                isolated: t.isolated as i32,
            })
            .collect(),
        link_types: s
            .link_types
            .iter()
            .map(|lt| pb::LinkTypeStats {
                r#type: lt.typ.clone(),
                links: lt.links as i32,
                suspect: lt.suspect as i32,
                sources: lt.sources as i32,
                targets: lt.targets as i32,
                pairs: lt
                    .pairs
                    .iter()
                    .map(|p| pb::TypePair {
                        from: p.from.clone(),
                        to: p.to.clone(),
                        count: p.count as i32,
                    })
                    .collect(),
                max_out: lt.max_out as i32,
            })
            .collect(),
        hubs: s
            .hubs
            .iter()
            .map(|h| pb::HubNode {
                id: h.id.0.clone(),
                key: h.key.clone(),
                r#type: h.typ.clone(),
                out: h.out as i32,
                r#in: h.r#in as i32,
            })
            .collect(),
        owners: s.owners as i32,
        projects: s.projects as i32,
    }
}

/// A change read whole, for an answer: its header, its impacts (with their validation) and items, its position.
fn whole_pb(w: &whole::WholeChange) -> Result<pb::Change, GraphError> {
    let mut c = convert::change(&w.change);
    c.nodes = w
        .impacts
        .iter()
        .map(|(cn, v)| {
            let mut p = convert::change_impact(cn);
            p.validation = v.as_ref().map(|v| pb::ImpactValidation {
                valid: v.valid,
                message: v.message.clone(),
                modified: v.modified,
            });
            p
        })
        .collect();
    c.items = w
        .items
        .iter()
        .map(convert::item)
        .collect::<Result<_, _>>()
        .map_err(GraphError::internal)?;
    c.seq = w.seq;
    Ok(c)
}

/// A node a merge or a split names (Go's `nameOf`).
fn node_name(n: &pb::NodeName) -> NodeName {
    NodeName {
        impact: n.change_impact_id.clone(),
        node: n.node_id.as_str().into(),
        key: n.key.clone(),
    }
}

/// A node a merge or a split creates (Go's `createSpec`).
fn create_spec(s: Option<&pb::NodeCreateSpec>, execution: &str, by: &str) -> Result<NodeCreate, GraphError> {
    let Some(s) = s else { return Ok(NodeCreate::default()) };
    Ok(NodeCreate {
        key: s.key.clone(),
        typ: s.r#type.clone(),
        props: s.props.as_ref().map(convert::map).filter(|p| !p.is_empty()),
        owner: s.owner.clone(),
        rationale: s.rationale.clone(),
        links: convert::link_writes(&s.links).map_err(|e| GraphError::new(Kind::Invalid, e))?,
        execution: execution.to_string(),
        produced_by: by.to_string(),
    })
}

fn restructured(r: &Restructured) -> pb::ImpactNodeRestructureResponse {
    pb::ImpactNodeRestructureResponse {
        successors: r.successors.iter().map(convert::change_impact).collect(),
        sources: r.sources.iter().map(convert::change_impact).collect(),
        parents: r.parents.iter().map(convert::change_impact).collect(),
        suspect: r
            .suspect
            .iter()
            .map(|l| pb::SuspectLink {
                from: Some(convert::node_ref(&l.from)),
                from_key: l.from_key.clone(),
                r#type: l.typ.clone(),
                to: Some(convert::node_ref(&l.to)),
                to_key: l.to_key.clone(),
            })
            .collect(),
    }
}
