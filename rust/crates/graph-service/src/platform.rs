//! What a graph reads from the other services (ADR 0154 §9, ADR 0167): the domain registry ([`Registry`], a service of
//! its own at `GOAP_DOMAINS_URL`) gives the domains of every graph; in a content graph (`GOAP_GRAPH_SPACE=content` of
//! `platform/cmd/graph`) the platform graph ([`Remote`]) gives the organisation and the projects its changes and
//! versions are placed in, the roles of the methodologies of its changes. The domains are kept as read, and read again
//! when a domain event says one moved; the namespaces of the platform at each request, read again only when their head
//! moved.
use std::{sync::Arc, time::Duration};

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

/// How long a read of the domain registry may take: a registry that does not answer is one that is not there.
pub const REGISTRY_TIMEOUT: Duration = Duration::from_secs(10);

/// The longest delay between two tries of [`Registry::wait_for_builtins`] (Go's `platform.AwaitMaxDelay`): the
/// fallback of an announcement lost.
pub const AWAIT_MAX_DELAY: Duration = Duration::from_secs(30);

/// The subject a graph calls the other services as (`graphclient.ClientPrincipal`).
const CLIENT_SUBJECT: &str = "system:client";

const STATUS_PUBLISHED: &str = "published";

pub(crate) fn client(url: &str) -> goap_connect::Client {
    goap_connect::Client::builder(url)
        .header("X-Goap-Subject", CLIENT_SUBJECT)
        .build()
}

/// The domain registry at `GOAP_DOMAINS_URL` (ADR 0167): the domains in force, the built-in ones included. What it
/// read is kept until [`Registry::reload`] (a domain event, ADR 0169): a request never waits for the registry once the
/// domains were read, nor fails when the registry is gone.
pub struct Registry {
    domains: registrypb::DomainServiceClient,
    /// what was read last: the versions in force, their catalogue and their definitions
    catalog: Mutex<Option<Read>>,
    /// one read of the registry at a time, so an older answer never replaces a newer one
    reading: Mutex<()>,
}

/// What a registry answered.
struct Read {
    versions: String,
    catalog: Arc<Catalog>,
    domains: Arc<Vec<Domain>>,
}

impl Registry {
    pub fn new(url: &str) -> Registry {
        Registry {
            domains: registrypb::DomainServiceClient::new(client(url)),
            catalog: Mutex::new(None),
            reading: Mutex::new(()),
        }
    }

    /// Waits until the registry holds the built-in domains (Go's `loadTypes` at start): a graph with no domains types
    /// nothing. Tried at once, then again as soon as a message reaches `wake` (the registry announcing it serves,
    /// `service.ready.domains`, or publishing a domain, ADR 0169), else after a delay doubling from a second up to
    /// [`AWAIT_MAX_DELAY`]; for as long as it takes.
    pub async fn wait_for_builtins<S>(&self, mut wake: Option<&mut S>) -> Result<Arc<Catalog>, GraphError>
    where
        S: futures::Stream + Unpin,
    {
        let mut delay = Duration::from_secs(1);
        loop {
            let err = match self.reload().await {
                Ok(()) => match self.read().await {
                    Some(r) if BUILTIN_NAMES.iter().all(|n| r.0.domains().contains_key(*n)) => return Ok(r.0),
                    _ => GraphError::internal(format!(
                        "domain registry: the built-in domains ({}) are not published",
                        BUILTIN_NAMES.join(", ")
                    )),
                },
                Err(e) => e,
            };
            tracing::info!(err = %err.message, retry = ?delay, "waiting for the domain registry");
            let sleep = tokio::time::sleep(delay);
            delay = (delay * 2).min(AWAIT_MAX_DELAY);
            match wake.as_deref_mut() {
                Some(sub) => {
                    tokio::select! {
                        _ = sleep => {}
                        // the registry is there now: a failure again is retried soon
                        _ = futures::StreamExt::next(sub) => delay = Duration::from_millis(500),
                    }
                }
                None => sleep.await,
            }
        }
    }

    /// The type catalogue of the domains in force in the registry (`typecat.Live` over a `domainclient`): built again
    /// when the versions in force changed; domains that make no catalogue leave the previous one (else `current`).
    pub async fn catalog(&self, current: Arc<Catalog>) -> Result<Arc<Catalog>, GraphError> {
        Ok(self.kept().await?.map(|r| r.0).unwrap_or(current))
    }

    /// The definitions of the domains in force (`domainclient.Client.Domains`).
    pub async fn domains(&self) -> Result<Arc<Vec<Domain>>, GraphError> {
        Ok(self.kept().await?.map(|r| r.1).unwrap_or_default())
    }

    /// What was read, else the registry read now (a graph that has read nothing yet).
    async fn kept(&self) -> Result<Option<(Arc<Catalog>, Arc<Vec<Domain>>)>, GraphError> {
        if let Some(r) = self.read().await {
            return Ok(Some(r));
        }
        self.reload().await?;
        Ok(self.read().await)
    }

    async fn read(&self) -> Option<(Arc<Catalog>, Arc<Vec<Domain>>)> {
        self.catalog
            .lock()
            .await
            .as_ref()
            .map(|r| (r.catalog.clone(), r.domains.clone()))
    }

    /// Reads the registry again (a domain event, Go's `reload`): the domains in force replace what was kept when they
    /// make a catalogue; a registry that does not answer, or domains that make no catalogue, leave it as it was. The
    /// requests keep being served from what was kept meanwhile.
    pub async fn reload(&self) -> Result<(), GraphError> {
        let _one = self.reading.lock().await;
        let known = self
            .catalog
            .lock()
            .await
            .as_ref()
            .map(|r| (r.versions.clone(), r.catalog.clone(), r.domains.clone()));
        let read = tokio::time::timeout(REGISTRY_TIMEOUT, self.fetch(known))
            .await
            .map_err(|_| {
                GraphError::internal(format!(
                    "domain registry: no answer within {}s",
                    REGISTRY_TIMEOUT.as_secs()
                ))
            })??;
        if let Some(r) = read {
            *self.catalog.lock().await = Some(r);
        }
        Ok(())
    }

    /// The domains in force in the registry: `known` again when its versions did not change (nothing exported), none
    /// when they make no catalogue.
    async fn fetch(&self, known: Option<(String, Arc<Catalog>, Arc<Vec<Domain>>)>) -> Result<Option<Read>, GraphError> {
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
        if let Some((v, catalog, domains)) = known
            && v == versions
        {
            return Ok(Some(Read {
                versions,
                catalog,
                domains,
            }));
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
            Ok(c) => Ok(Some(Read {
                versions,
                catalog: Arc::new(c),
                domains: Arc::new(ds),
            })),
            Err(e) => {
                tracing::error!(err = %e, "type catalogue");
                Ok(None)
            }
        }
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
        responsible: url(&n.responsible),
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
