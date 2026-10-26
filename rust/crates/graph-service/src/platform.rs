//! What a graph reads from the other services (ADR 0154 §9, ADR 0167): the domain registry ([`Registry`], a service of
//! its own at `GOAP_DOMAINS_URL`) gives the domains of every graph; in a content graph (`GOAP_GRAPH_SPACE=content` of
//! `platform/cmd/graph`) the platform graph ([`Remote`]) gives the organisation and the projects its changes and
//! versions are placed in, the roles of the methodologies of its changes. The domains are read again after
//! [`DOMAINS_TTL`] or when a domain event says one moved; the namespaces of the platform at each request, read again
//! only when their head moved.
use std::{
    sync::Arc,
    time::{Duration, Instant},
};

use goap_connect::{ConnectError, Ctx, Request};
use goap_proto::graph::v1::{self as pb, GraphService as _};
use goap_proto::registry::v1::{self as registrypb, DomainService as _};
use graph_catalog::{
    Catalog,
    catalog::{BUILTIN_NAMES, STRUCTURE_ORGANISATION},
    def::Domain,
    imported::Definition,
    record::{Record, latest},
};
use graph_core::{
    GraphError,
    lnk::Url,
    model::{ChangeId, Link, LinkId, MAIN_BRANCH, Node, NodeId, NodeRef, Target},
    platform::{Held, Platform},
};
use tokio::sync::Mutex;

/// How long the domains read are kept when no event says they moved (`domainclient.DefaultTTL`).
pub const DOMAINS_TTL: Duration = Duration::from_secs(30);

/// The subject a graph calls the other services as (`graphclient.ClientPrincipal`).
const CLIENT_SUBJECT: &str = "system:client";

const STATUS_PUBLISHED: &str = "published";

pub(crate) fn client(url: &str) -> goap_connect::Client {
    goap_connect::Client::builder(url)
        .header("X-Goap-Subject", CLIENT_SUBJECT)
        .build()
}

/// The domain registry at `GOAP_DOMAINS_URL` (ADR 0167): the domains in force, the built-in ones included.
pub struct Registry {
    domains: registrypb::DomainServiceClient,
    /// what was read last: when, the versions in force, their catalogue and their definitions
    catalog: Mutex<Option<Read>>,
}

/// What a registry answered.
struct Read {
    at: Instant,
    versions: String,
    catalog: Arc<Catalog>,
    domains: Arc<Vec<Domain>>,
}

impl Registry {
    pub fn new(url: &str) -> Registry {
        Registry {
            domains: registrypb::DomainServiceClient::new(client(url)),
            catalog: Mutex::new(None),
        }
    }

    /// Forgets the domains read: the next request reads the registry again (a domain event).
    pub async fn invalidate(&self) {
        *self.catalog.lock().await = None;
    }

    /// Waits until the registry holds the built-in domains (Go's `loadTypes` at start): a graph with no domains types
    /// nothing. Tried every second, `attempts` times.
    pub async fn wait_for_builtins(&self, attempts: usize) -> Result<Arc<Catalog>, GraphError> {
        let mut last = GraphError::internal("domain registry: no built-in domain");
        for _ in 0..attempts.max(1) {
            match self
                .catalog(Arc::new(Catalog::of(&[]).map_err(GraphError::internal)?))
                .await
            {
                Ok(c) if BUILTIN_NAMES.iter().all(|n| c.domains().contains_key(*n)) => return Ok(c),
                Ok(_) => {
                    self.invalidate().await;
                    last = GraphError::internal(format!(
                        "domain registry: the built-in domains ({}) are not published",
                        BUILTIN_NAMES.join(", ")
                    ));
                }
                Err(e) => last = e,
            }
            tracing::info!(err = %last.message, "waiting for the domain registry");
            tokio::time::sleep(Duration::from_secs(1)).await;
        }
        Err(last)
    }

    /// The type catalogue of the domains in force in the registry (`typecat.Live` over a `domainclient`): built again
    /// when the versions in force changed; domains that make no catalogue leave the previous one (else `current`).
    pub async fn catalog(&self, current: Arc<Catalog>) -> Result<Arc<Catalog>, GraphError> {
        Ok(self.read().await?.map(|r| r.0).unwrap_or(current))
    }

    /// The definitions of the domains in force (`domainclient.Client.Domains`).
    pub async fn domains(&self) -> Result<Arc<Vec<Domain>>, GraphError> {
        Ok(self.read().await?.map(|r| r.1).unwrap_or_default())
    }

    /// The catalogue and the domains in force, read again after [`DOMAINS_TTL`] or an invalidation; none while no
    /// read made a catalogue.
    async fn read(&self) -> Result<Option<(Arc<Catalog>, Arc<Vec<Domain>>)>, GraphError> {
        let mut cur = self.catalog.lock().await;
        if let Some(r) = cur.as_ref()
            && r.at.elapsed() < DOMAINS_TTL
        {
            return Ok(Some((r.catalog.clone(), r.domains.clone())));
        }
        let ctx = Ctx::background();
        let list = self
            .domains
            .list_domains(
                &ctx,
                Request::new(registrypb::ListDomainsRequest { all_versions: true }),
            )
            .await
            .map_err(|e| unavailable("domain registry: list domains", e))?
            .into_inner();
        let records: Vec<Record> = list
            .domains
            .iter()
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
        let versions = force
            .iter()
            .map(|(n, v)| format!("{n}@{v}"))
            .collect::<Vec<_>>()
            .join(",");
        if let Some(r) = cur.as_mut()
            && r.versions == versions
        {
            r.at = Instant::now();
            return Ok(Some((r.catalog.clone(), r.domains.clone())));
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
                .map_err(|e| unavailable("domain registry: export domain", e))?
                .into_inner()
                .yaml;
            let d = Definition::read(&yaml)
                .and_then(|d| d.json)
                .and_then(|j| Domain::from_json(&j))
                .map_err(|e| GraphError::internal(format!("domain registry: domain {name}@{version}: {e}")))?;
            ds.push(d);
        }
        match Catalog::of(&ds) {
            Ok(c) => {
                *cur = Some(Read {
                    at: Instant::now(),
                    versions,
                    catalog: Arc::new(c),
                    domains: Arc::new(ds),
                });
            }
            Err(e) => {
                tracing::error!(err = %e, "type catalogue");
                if let Some(r) = cur.as_mut() {
                    r.at = Instant::now();
                }
            }
        }
        Ok(cur.as_ref().map(|r| (r.catalog.clone(), r.domains.clone())))
    }
}

/// The platform graph at `GOAP_PLATFORM_GRAPH_URL`, read by a content graph, and by the domain registry for the
/// organisation's rules.
pub struct Remote {
    graph: pb::GraphServiceClient,
    /// what was read of the platform's namespaces
    heads: Mutex<Arc<Platform>>,
}

fn unavailable(what: &str, e: ConnectError) -> GraphError {
    GraphError::internal(format!("{what}: {}", e.message()))
}

impl Remote {
    pub fn new(url: &str) -> Remote {
        Remote {
            graph: pb::GraphServiceClient::new(client(url)),
            heads: Mutex::new(Arc::new(Platform::default())),
        }
    }

    /// What the platform graph holds of the organisation now: read again when its head moved.
    pub async fn platform(&self, cat: &Catalog) -> Result<Arc<Platform>, GraphError> {
        let org = cat
            .structure(STRUCTURE_ORGANISATION)
            .map(|s| s.namespace.clone())
            .unwrap_or_default();
        let org_head = self.head(&org).await?;
        let mut cur = self.heads.lock().await;
        if cur.organisation.baseline == org_head {
            return Ok(cur.clone());
        }
        *cur = Arc::new(Platform {
            organisation: self.organisation(&org_head).await?,
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
            Err(e) => Err(unavailable("platform graph: branch head", e)),
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
            .map_err(|e| unavailable("platform graph: organisation", e))?
            .into_inner();
        Ok(Held {
            baseline: baseline.into(),
            nodes: g.nodes.iter().map(node).filter(|n| !n.deleted).collect(),
            links: g.links.iter().map(link).collect(),
        })
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
        creator: graph_core::model::Creator {
            subject: n.creator.clone(),
            unit: url(&n.creator_unit),
        },
        editor: n.editor.clone(),
        visibility: n.visibility.clone(),
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
