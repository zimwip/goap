//! `goap.graph.v1.GraphService`: the methods written so far; the others answer `unimplemented` (the generated
//! default), and so does a request a written method does not serve yet (a change view, a person's read: see
//! [`Caller::serves`]), so that `goap-proxy` in mode split replays it on the Go service.
use std::{collections::HashMap, sync::Arc};

use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::graph::v1 as pb;
use graph_catalog::{Catalog, catalog::STRUCTURE_PROJECT};
use graph_core::{
    GraphError, Kind,
    change::{self, ChangesFilter},
    items,
    lnk::Url,
    model::{BaselineId, NodeId, NodeQuery},
    page::link_page,
    read::{self, World},
    store::ReadTx,
};
use graph_core::{
    model::{ChangeId, MAIN_BRANCH},
    write::{
        change::NewChange,
        commit::NOTHING_TO_COMMIT,
        edits::{Commit, LinkEdit, NodeEdit},
        review::{ReviewBatch, Verdict},
        tx::{Env, Tx},
        whole,
        work::{NodeCheckout, NodeCreate, fold_change},
    },
};
use graph_store::read::Session;

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

    /// The tags matching the filter, oldest first.
    async fn list_tags(&self, _ctx: &Ctx, req: Request<pb::ListTagsRequest>) -> Answer<pb::ListTagsResponse> {
        self.read_any(&req, async |tx| {
            let m = &req.msg;
            let tags = tx.tags(&m.namespace, &m.name, &m.node_id).await?;
            Ok(pb::ListTagsResponse {
                tags: tags
                    .into_iter()
                    .map(|t| pb::Tag {
                        id: t.id,
                        name: t.name,
                        namespace: t.namespace,
                        node_id: t.node,
                        version: t.version,
                        by: t.by,
                        created_at: convert::time(&t.created_at),
                    })
                    .collect(),
            })
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
            Ok(pb::GetChangeResponse { change: Some(c) })
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

    async fn impact_node_review_batch(
        &self,
        _ctx: &Ctx,
        req: Request<pb::ImpactNodeReviewBatchRequest>,
    ) -> Answer<pb::ImpactNodeReviewBatchResponse> {
        let caller = Caller::of(&req)?;
        let m = req.into_inner();
        if m.item.is_some() {
            return Err(connect_error(GraphError::unimplemented(
                "the record of a review built up by the use case (ADR 0080)",
            )));
        }
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
            };
            let out = tx.impact_node_review_batch(&id, b).await?;
            Ok(pb::ImpactNodeReviewBatchResponse {
                nodes: out.iter().map(convert::change_impact).collect(),
            })
        })
        .await
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
