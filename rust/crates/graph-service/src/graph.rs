//! `goap.graph.v1.GraphService`: the methods written so far; the others answer `unimplemented` (the generated
//! default), and so does a request a written method does not serve yet (a change view, a person's read: see
//! [`Caller::serves`]), so that `goap-proxy` in mode split replays it on the Go service.
use std::{collections::HashMap, sync::Arc};

use goap_connect::{ConnectError, Ctx, Request, Response, async_trait};
use goap_proto::graph::v1 as pb;
use graph_core::{
    GraphError, Kind,
    change::{self, ChangesFilter},
    lnk::Url,
    model::{NodeId, NodeQuery},
    page::link_page,
    read::{self, World},
    store::ReadTx,
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

/// The caller of a request.
struct Caller {
    subject: String,
    on_behalf_of: String,
    at_least: bool,
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
}

/// The Connect error of a graph error (`lib/rpcerr.ToConnect`).
fn connect_error(e: GraphError) -> ConnectError {
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
    World::open(tx, &convert::view(view)).await?.node_view(&r.id, key).await
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
}
