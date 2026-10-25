//! The platform graph a content graph reads (ADR 0154 §9, `GOAP_GRAPH_SPACE=content` of `platform/cmd/graph`): the
//! domains of its registry, the organisation and the projects its changes and versions are placed in, the roles of the
//! methodologies of its changes. The domains are read again after [`DOMAINS_TTL`] or when a domain event says one
//! moved; the namespaces of the platform at each request, read again only when their head moved.
use std::{
    sync::Arc,
    time::{Duration, Instant},
};

use goap_connect::{ConnectError, Ctx, Request};
use goap_proto::graph::v1::{self as pb, GraphService as _};
use goap_proto::registry::v1::{self as registrypb, DomainService as _};
use graph_catalog::{
    Catalog,
    catalog::{STRUCTURE_ORGANISATION, is_builtin},
    def::Domain,
    imported::Definition,
    record::{Record, latest},
};
use graph_core::{
    GraphError,
    lnk::Url,
    model::{ChangeId, Link, LinkId, MAIN_BRANCH, NAMESPACE_METHODOLOGY, Node, NodeId, NodeRef, Target},
    platform::{Held, Platform},
};
use tokio::sync::Mutex;

/// How long the domains read are kept when no event says they moved (`domainclient.DefaultTTL`).
pub const DOMAINS_TTL: Duration = Duration::from_secs(30);

/// The subject the content graph calls the platform graph as (`graphclient.ClientPrincipal`).
const CLIENT_SUBJECT: &str = "system:client";

const TYPE_VERSION: &str = "methodology@MethodologyVersion";
const TYPE_ROLE: &str = "methodology@Role";
const STATUS_PUBLISHED: &str = "published";

/// The platform graph at `GOAP_PLATFORM_GRAPH_URL`.
pub struct Remote {
    graph: pb::GraphServiceClient,
    domains: registrypb::DomainServiceClient,
    /// the catalogue of the domains in force, the versions it was built from and when they were read
    catalog: Mutex<Option<(Instant, String, Arc<Catalog>)>>,
    /// what was read of the platform's namespaces
    heads: Mutex<Arc<Platform>>,
}

fn unavailable(what: &str, e: ConnectError) -> GraphError {
    GraphError::internal(format!("platform graph: {what}: {}", e.message()))
}

impl Remote {
    pub fn new(url: &str) -> Remote {
        let client = goap_connect::Client::builder(url)
            .header("X-Goap-Subject", CLIENT_SUBJECT)
            .build();
        Remote {
            graph: pb::GraphServiceClient::new(client.clone()),
            domains: registrypb::DomainServiceClient::new(client),
            catalog: Mutex::new(None),
            heads: Mutex::new(Arc::new(Platform::default())),
        }
    }

    /// Forgets the domains read: the next request reads the registry again (a domain event).
    pub async fn invalidate(&self) {
        *self.catalog.lock().await = None;
    }

    /// The type catalogue of the domains in force in the platform's registry (`typecat.Live` over a `domainclient`):
    /// built again when the versions in force changed; domains that make no catalogue leave the previous one.
    pub async fn catalog(&self, current: Arc<Catalog>) -> Result<Arc<Catalog>, GraphError> {
        let mut cur = self.catalog.lock().await;
        if let Some((at, _, c)) = cur.as_ref()
            && at.elapsed() < DOMAINS_TTL
        {
            return Ok(c.clone());
        }
        let ctx = Ctx::background();
        let list = self
            .domains
            .list_domains(
                &ctx,
                Request::new(registrypb::ListDomainsRequest { all_versions: true }),
            )
            .await
            .map_err(|e| unavailable("list domains", e))?
            .into_inner();
        let records: Vec<Record> = list
            .domains
            .iter()
            .filter(|s| !s.builtin && !is_builtin(&s.name))
            .map(|s| Record {
                domain: Domain {
                    name: s.name.clone(),
                    version: s.version.clone(),
                    ..Domain::default()
                },
                status: s.status.clone(),
                created_at: None,
                updated_at: s.updated_at.and_then(|t| t.try_into().ok()),
                published_at: s.published_at.and_then(|t| t.try_into().ok()),
                updated_by: String::new(),
            })
            .collect();
        let force: Vec<(String, String)> = latest(&records)
            .into_iter()
            .filter(|r| r.status == STATUS_PUBLISHED)
            .map(|r| (r.domain.name.clone(), r.domain.version.clone()))
            .collect();
        let fp = force
            .iter()
            .map(|(n, v)| format!("{n}@{v}"))
            .collect::<Vec<_>>()
            .join(",");
        if let Some((_, was, c)) = cur.as_ref()
            && *was == fp
        {
            *cur = Some((Instant::now(), fp, c.clone()));
            return Ok(cur.as_ref().map(|c| c.2.clone()).unwrap_or(current));
        }
        let mut ds = Vec::new();
        for (name, version) in &force {
            let yaml = self
                .domains
                .export_domain(
                    &ctx,
                    Request::new(registrypb::ExportDomainRequest {
                        name: name.clone(),
                        version: version.clone(),
                        format: String::new(),
                    }),
                )
                .await
                .map_err(|e| unavailable("export domain", e))?
                .into_inner()
                .yaml;
            let d = Definition::read(&yaml)
                .and_then(|d| d.json)
                .and_then(|j| Domain::from_json(&j))
                .map_err(|e| GraphError::internal(format!("platform graph: domain {name}@{version}: {e}")))?;
            ds.push(d);
        }
        let c = match Catalog::new(&ds) {
            Ok(c) => Arc::new(c),
            Err(e) => {
                tracing::error!(err = %e, "type catalogue");
                cur.as_ref().map(|c| c.2.clone()).unwrap_or(current)
            }
        };
        *cur = Some((Instant::now(), fp, c.clone()));
        Ok(c)
    }

    /// What the platform graph holds of the organisation and the methodologies now: read again where a head moved.
    pub async fn platform(&self, cat: &Catalog) -> Result<Arc<Platform>, GraphError> {
        let org = cat
            .structure(STRUCTURE_ORGANISATION)
            .map(|s| s.namespace.clone())
            .unwrap_or_default();
        let (org_head, meth_head) = (self.head(&org).await?, self.head(NAMESPACE_METHODOLOGY).await?);
        let mut cur = self.heads.lock().await;
        if cur.organisation.baseline == org_head && cur.methodology.baseline == meth_head {
            return Ok(cur.clone());
        }
        let organisation = if cur.organisation.baseline == org_head {
            cur.organisation.clone()
        } else {
            self.organisation(&org_head).await?
        };
        let methodology = if cur.methodology.baseline == meth_head {
            cur.methodology.clone()
        } else {
            self.methodologies(&meth_head).await?
        };
        *cur = Arc::new(Platform {
            organisation,
            methodology,
        });
        Ok(cur.clone())
    }

    /// The head of main of a namespace of the platform graph (empty: the empty state).
    async fn head(&self, namespace: &str) -> Result<String, GraphError> {
        let r = self
            .graph
            .get_branch(
                &Ctx::background(),
                Request::new(pb::GetBranchRequest {
                    name: MAIN_BRANCH.into(),
                    namespace: namespace.into(),
                    header_only: true,
                }),
            )
            .await;
        match r {
            Ok(r) => Ok(r.into_inner().head.map(|b| b.id).unwrap_or_default()),
            Err(e) if e.code() == goap_connect::Code::NotFound => Ok(String::new()),
            Err(e) => Err(unavailable("branch head", e)),
        }
    }

    /// The organisation at a baseline: its nodes and their links (`access.Directory` over `graphclient`).
    async fn organisation(&self, baseline: &str) -> Result<Held, GraphError> {
        if baseline.is_empty() {
            return Ok(Held::default());
        }
        let g = self
            .graph
            .get_baseline_graph(
                &Ctx::background(),
                Request::new(pb::GetBaselineGraphRequest { id: baseline.into() }),
            )
            .await
            .map_err(|e| unavailable("organisation", e))?
            .into_inner();
        Ok(Held {
            baseline: baseline.into(),
            nodes: g.nodes.iter().map(node).filter(|n| !n.deleted).collect(),
            links: g.links.iter().map(link).collect(),
        })
    }

    /// The versions of the methodologies and their roles at a baseline: what the roles of an operation are read from.
    async fn methodologies(&self, baseline: &str) -> Result<Held, GraphError> {
        let mut out = Held {
            baseline: baseline.into(),
            ..Held::default()
        };
        if baseline.is_empty() {
            return Ok(out);
        }
        for typ in [TYPE_VERSION, TYPE_ROLE] {
            let r = self
                .graph
                .list_view_nodes(
                    &Ctx::background(),
                    Request::new(pb::ListViewNodesRequest {
                        view: Some(pb::View {
                            namespace: NAMESPACE_METHODOLOGY.into(),
                            branch: MAIN_BRANCH.into(),
                            baseline_id: baseline.into(),
                            ..pb::View::default()
                        }),
                        r#type: typ.into(),
                    }),
                )
                .await
                .map_err(|e| unavailable("methodologies", e))?
                .into_inner();
            out.nodes.extend(r.nodes.iter().map(node).filter(|n| !n.deleted));
        }
        Ok(out)
    }
}

fn url(s: &str) -> Url {
    if s.is_empty() {
        return Url::default();
    }
    Url::parse(s).unwrap_or_default()
}

fn node_ref(r: Option<&pb::NodeRef>) -> NodeRef {
    r.map(|r| NodeRef::new(NodeId(r.id.clone()), r.version))
        .unwrap_or_default()
}

/// A node of the protocol (`pbconv.NodeFromPB`).
fn node(n: &pb::Node) -> Node {
    Node {
        id: NodeId(n.id.clone()),
        version: n.version,
        namespace: n.namespace.clone(),
        key: n.key.clone(),
        typ: n.r#type.clone(),
        props: n.props.as_ref().map(crate::convert::map),
        deleted: n.deleted,
        change_id: ChangeId(n.change_id.clone()),
        created_at: n.created_at.and_then(|t| t.try_into().ok()),
        branch: n.branch.clone(),
        parents: n.parents.clone(),
        reason: n.reason.clone(),
        state: n.state.clone(),
        change_impact: n.change_impact.clone(),
        comment: n.comment.clone(),
        execution: n.execution.clone(),
        joined: n.joined.clone(),
        owner: url(&n.owner),
        project: url(&n.project),
        origins: n.origins.iter().map(|r| node_ref(Some(r))).collect(),
    }
}

/// A link of the protocol (`pbconv.LinkFromPB`).
fn link(l: &pb::Link) -> Link {
    Link {
        id: LinkId(l.id.clone()),
        typ: l.r#type.clone(),
        from: node_ref(l.from.as_ref()),
        to: if l.target.is_empty() {
            Target::Node(node_ref(l.to.as_ref()))
        } else {
            Target::External(url(&l.target))
        },
        props: l.props.as_ref().map(crate::convert::map),
        change_id: ChangeId(l.change_id.clone()),
    }
}
