//! The registries and the platform bootstrap (Go's registry.go): the domain registry, the methodology registry, the
//! type catalogue the graph judges by, and the one event stream of the web.

use std::sync::Arc;

use async_trait::async_trait;
use goap_access::{Directory, ROLE_ADMIN};
use goap_ctx::Ctx;
use goap_domain::{Baseline, BaselineID, Change, ChangeID, ChangeImpact, Link, Node, Structures};
use goap_domainsvc as domainsvc;
use goap_engine::{Payload, Publisher, Publishers};
use goap_eventsvc::{Hub, Publication};
use goap_graph::{Graph, GraphEvent};
use goap_graphapi::{Commit, CommitResult, NewChange, NodeCreate};
use goap_graphsvc as graphsvc;
use goap_registrysvc::{self as registrysvc, ApplicableFn, GraphStore, StoreGraph};
use goap_typecat::Live;
use serde_json::Value;

use crate::app::{BoxError, Env, wrap};
use crate::graph::GraphPart;
use crate::local::{RegistryEvents, Stores};

type GResult<T> = Result<T, goap_graphapi::Error>;

/// RegistryPart is the two registries (the domains, the graph's; the methodologies, the engine's: ADR 0125 §3), the
/// type catalogue the graph judges by, and the one event stream of the web.
pub(crate) struct RegistryPart {
    pub domains: Arc<domainsvc::Service>,
    pub reg: Arc<registrysvc::Service>,
    pub types: Arc<Live>,
    pub bus: Arc<Hub>,
}

/// build_registry creates the registries, binds their hooks on the graph, then runs the seeds in the order of the
/// graphsvc boot: domains, type catalogue, the platform bootstrap (graphsvc::boot), methodologies.
pub(crate) async fn build_registry(
    e: &Env,
    gp: &GraphPart,
    st: &Stores,
) -> Result<RegistryPart, BoxError> {
    let g = &gp.g;
    let authz: Arc<dyn goap_authz::Authorizer> = gp.authorizer.clone();
    // the one event stream of the web (ADR 0053): every publication of the platform also feeds it; it shows a process
    // to who may read it (Go sets these two in buildServer, the hub being shared here once built)
    let mut bus = Hub::new();
    bus.authz = Some(authz.clone());
    {
        let processes = st.processes.clone();
        bus.lookup = Some(Arc::new(move |ctx: Ctx, id: String| {
            let processes = processes.clone();
            Box::pin(async move { processes.get(&ctx, &id).await.ok() })
        }));
    }
    let bus = Arc::new(bus);
    {
        let (bus, ctx) = (bus.clone(), e.ctx.clone());
        tokio::spawn(async move { bus.run(&ctx).await });
    }

    // the domains are kept in the local database (domainsvc); a domain published or archived reloads the type
    // catalogue and checks the methodologies again, the ones it breaks being suspended (ADR 0125 §3)
    let mut domains = domainsvc::Service::new(st.domains.clone());
    domains.authz = Some(authz.clone());
    domains.events = Some(Arc::new(HubValues(bus.clone())));
    {
        let late = e.late.clone();
        domains.changed = Some(domainsvc::changed_fn(
            move |ctx: Ctx, r: goap_def::Record| {
                let late = late.clone();
                async move {
                    let Some(types) = late.types.get() else {
                        return;
                    };
                    if let Err(e) = types.reload(&ctx).await {
                        tracing::error!(domain = %r.domain.name, err = %e, "type catalogue");
                        return;
                    }
                    tracing::info!(domain = %r.domain.name, version = %r.domain.version, status = %r.status, "domain published: type catalogue reloaded");
                    let reg = late.registry.get().and_then(std::sync::Weak::upgrade);
                    if let Some(reg) = reg
                        && let Err(e) = reg.domains_changed(&ctx).await
                    {
                        tracing::error!(domain = %r.domain.name, err = %e, "check the methodologies");
                    }
                }
            },
        ));
    }
    let domains = Arc::new(domains);
    // the graph judges nodes by the types of the published domains (ADR 0012): its catalogue follows the domain
    // registry
    let types = Arc::new(Live::new(Arc::new(DomainSource(domains.clone()))));
    let _ = e.late.types.set(types.clone());

    // methodologies are nodes of the graph; the scope of the MCPs (ADR 0028) is checked where methodologies declare
    // them
    let mut reg =
        registrysvc::Service::new(Arc::new(GraphStore::new(Arc::new(InGraph(g.clone())))));
    reg.domains = Some(Arc::new(InProcessDomains(domains.clone())));
    reg.authz = Some(authz);
    {
        let mcps = Arc::new(goap_mcpsvc::Directory::new(g.clone()));
        reg.mcp_scopes = Some(registrysvc::mcp_scopes_fn(move |ctx: Ctx| {
            let mcps = mcps.clone();
            async move { mcps.scopes(&ctx).await.map_err(|e| Box::new(e) as BoxError) }
        }));
    }
    reg.applicable = Some(applicable(gp.directory.clone()));
    // publications reload the triggers
    reg.events = Some(Arc::new(Publishers(vec![
        bus.clone() as Arc<dyn Publisher>,
        Arc::new(RegistryEvents {
            triggers: e.triggers.clone(),
        }),
    ])));
    let reg = Arc::new(reg);
    // a change follows the lifecycle its methodology names, its gates read the conditions of the methodology (ADR
    // 0058); the landing gate of the sub-changes (ADR 0066); the history of a change, its steps relying on one another
    // as their methodology says (ADR 0109); who may perform each operation on a change (ADR 0103)
    let _ = e.late.registry.set(Arc::downgrade(&reg));

    let system = goap_authz::with_principal(&e.ctx, goap_authz::system("registry", &[ROLE_ADMIN]));
    // every start publishes the versions of the files not stored yet (ADR 0113): a new version of a shipped domain or
    // methodology needs no reset of the database; the changes already open keep the version they follow
    let doms = domains
        .seed_domains(&system, &e.cfg.domains_dir)
        .await
        .map_err(|err| wrap("domains", err))?;
    if !doms.is_empty() {
        tracing::info!(domains = ?doms, "domain versions published");
    }
    types
        .reload(&e.ctx)
        .await
        .map_err(|err| wrap("type catalogue", err))?;
    // the one platform bootstrap (graphsvc::boot, ADR 0071) with the development data of this composition: the ALM
    // demo with GOAP_DEV_DEMO=1 (else loaded on demand from the settings, graphsvc Handler.demo), the
    // document-repository MCP and, with GOAP_DEV_FS_ROOT, a directory as the default organisation's repository; with
    // GOAP_DEV_WORKSPACE_ROOT, the workspaces of the changes (ADR 0118), their commands in containers of
    // GOAP_DEV_WORKSPACE_IMAGE when it is set.
    // The gateway configuration is seeded before the methodologies below: publishing a methodology stubs any alias it
    // references that the platform namespace doesn't have yet (registrysvc ensure_alias_stubs), and that stub would
    // otherwise collide with the alias Boot seeds.
    let models = goap_modelgw::boot_config(&e.ctx, &e.cfg.models_config, &e.secrets)
        .await
        .map_err(|err| wrap("models config", err))?;
    let opts = graphsvc::Options {
        models,
        require_hooks: true,
        dev: Some(dev_seed(e)),
    };
    Box::pin(graphsvc::boot(&e.ctx, g, &opts))
        .await
        .map_err(|err| wrap("boot", err))?;
    let loaded = Box::pin(reg.seed(&system, &e.cfg.methodologies_dir))
        .await
        .map_err(|err| wrap("methodologies", err))?;
    if !loaded.is_empty() {
        tracing::info!(methodologies = ?loaded, "methodology versions published");
    }
    // a new start may break a methodology too (a built-in domain changes with the platform)
    reg.recheck(&e.ctx)
        .await
        .map_err(|err| wrap("check the methodologies", err))?;
    Ok(RegistryPart {
        domains,
        reg,
        types,
        bus,
    })
}

/// The development data of this composition (Options.dev of the boot).
fn dev_seed(e: &Env) -> graphsvc::DevFn {
    let (demo, fs_root, ws_root, ws_image) = (
        e.cfg.demo,
        e.cfg.dev_fs_root.clone(),
        e.cfg.workspace_root.clone(),
        e.cfg.workspace_image.clone(),
    );
    graphsvc::dev_fn(move |ctx, g| {
        let (fs_root, ws_root, ws_image) = (fs_root.clone(), ws_root.clone(), ws_image.clone());
        Box::pin(async move {
            let boxed = |e: goap_graphapi::Error| Box::new(e) as BoxError;
            if demo {
                Box::pin(goap_devseed::demo(ctx, g)).await.map_err(boxed)?;
            }
            Box::pin(goap_devseed::document_repository(ctx, g))
                .await
                .map_err(boxed)?;
            if !fs_root.is_empty() {
                Box::pin(goap_devseed::local_fs(ctx, g, &fs_root))
                    .await
                    .map_err(boxed)?;
            }
            if !ws_root.is_empty() {
                Box::pin(goap_devseed::workspace(ctx, g, &ws_root, &ws_image))
                    .await
                    .map_err(boxed)?;
            }
            Ok(())
        })
    })
}

/// applicable lists the methodologies applicable to a project from the snapshot of the organisation (ADR 0106: where
/// the guardian chain of a change finds the methodologies integrating its own).
fn applicable(d: Arc<Directory>) -> ApplicableFn {
    registrysvc::applicable_fn(move |ctx: Ctx, project: String| {
        let d = d.clone();
        async move {
            let s = d
                .snapshot(&ctx)
                .await
                .map_err(|e| Box::new(e) as BoxError)?;
            Ok(s.applicable_methodologies(&project))
        }
    })
}

/// The domain registry as the source of the type catalogue (Go's `typecat.NewLive(domains.Domains)`).
struct DomainSource(Arc<domainsvc::Service>);

#[async_trait]
impl goap_typecat::Source for DomainSource {
    async fn domains(&self, ctx: &Ctx) -> Result<Vec<goap_def::Domain>, goap_typecat::BoxError> {
        self.0.domains(ctx).await.map_err(|e| Box::new(e) as _)
    }
}

/// The domain registry in process as the methodology registry reads it (Go's `*domainsvc.Service` satisfies
/// `registrysvc.Domains`).
pub(crate) struct InProcessDomains(pub Arc<domainsvc::Service>);

#[async_trait]
impl registrysvc::Domains for InProcessDomains {
    async fn domain_versions(
        &self,
        ctx: &Ctx,
        all: bool,
    ) -> registrysvc::Result<Vec<goap_def::Record>> {
        self.0
            .domain_versions(ctx, all)
            .await
            .map_err(registrysvc::Error::domains)
    }
    async fn get_domain(
        &self,
        ctx: &Ctx,
        name: &str,
        version: &str,
    ) -> registrysvc::Result<goap_def::Record> {
        self.0
            .get_domain(ctx, name, version)
            .await
            .map_err(registrysvc::Error::domains)
    }
}

/// The domain registry in process as the built-in connectors list the domains (goap-admin).
#[async_trait]
impl goap_connector_builtin::Domains for InProcessDomains {
    async fn domains(&self, ctx: &Ctx) -> Result<Vec<goap_def::Domain>, BoxError> {
        Ok(self.0.domains(ctx).await?)
    }
}

/// The graph of this process under the store of the methodology registry (Go's `*graph.Graph` satisfies
/// `registrysvc.StoreGraph`).
struct InGraph(Arc<Graph>);

#[async_trait]
impl StoreGraph for InGraph {
    async fn branch_head(&self, ctx: &Ctx, namespace: &str, name: &str) -> GResult<Baseline> {
        self.0.branch_head(ctx, namespace, name).await
    }
    async fn baseline_graph(&self, ctx: &Ctx, id: &BaselineID) -> GResult<(Vec<Node>, Vec<Link>)> {
        self.0.baseline_graph(ctx, id).await
    }
    async fn commit(&self, ctx: &Ctx, input: Commit) -> GResult<CommitResult> {
        Box::pin(self.0.commit(ctx, input)).await
    }
    async fn create_change(&self, ctx: &Ctx, input: NewChange) -> GResult<Change> {
        Box::pin(self.0.create_change(ctx, input)).await
    }
    async fn impact_node_create(
        &self,
        ctx: &Ctx,
        id: &ChangeID,
        input: NodeCreate,
    ) -> GResult<ChangeImpact> {
        Box::pin(self.0.impact_node_create(ctx, id, input)).await
    }
    async fn changes(&self, ctx: &Ctx) -> GResult<Vec<Change>> {
        self.0.changes(ctx).await
    }
    async fn structures(&self, ctx: &Ctx) -> GResult<Structures> {
        goap_graphapi::Port::structures(&*self.0, ctx).await
    }
    async fn suspect_links(&self, ctx: &Ctx, id: &BaselineID) -> Option<GResult<Vec<Link>>> {
        Some(self.0.suspect_links(ctx, id).await)
    }
}

/// HubValues is the event hub as a publisher of JSON values (the graph handler, the domain registry): a value is read
/// by its subject, as the hub reads the messages of the bus (Go hands it the values themselves).
pub(crate) struct HubValues(pub Arc<Hub>);

impl HubValues {
    async fn send(&self, ctx: &Ctx, subject: &str, v: &Value) {
        let _ = Publisher::publish(&*self.0, ctx, subject, &Payload::new(v)).await;
    }
}

#[async_trait]
impl graphsvc::Publisher for HubValues {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError> {
        self.send(ctx, subject, v).await;
        Ok(())
    }
}

#[async_trait]
impl domainsvc::Publisher for HubValues {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError> {
        self.send(ctx, subject, v).await;
        Ok(())
    }
}

/// The publication of the hub a graph event is: nodes, baselines and changes; the documents of the index are not for
/// the web.
pub(crate) fn publication_of(ev: &GraphEvent) -> Option<Publication> {
    match ev {
        GraphEvent::Node(n) => Some(Publication::Node(n.clone())),
        GraphEvent::Baseline(b) => Some(Publication::Baseline(b.clone())),
        GraphEvent::Change(c) => Some(Publication::Change(c.clone())),
        GraphEvent::ChangeDoc(_) => None,
    }
}
