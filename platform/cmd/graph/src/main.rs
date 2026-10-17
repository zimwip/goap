//! Command graph serves the domain/change graph and the domain registry, the definition of the graph (ADR 0125 §3).

use std::collections::BTreeMap;
use std::sync::{Arc, Weak};
use std::time::Duration;

use async_trait::async_trait;
use goap_access::{self as access, AdminFloorValidator, Authorizer, Directory};
use goap_ctx::Ctx;
use goap_domain::{Baseline, BaselineID, FACET_CRITICALITY_POLICY, Link, Node, Structures};
use goap_domainsvc as domainsvc;
use goap_gen::graph::v1::graph_service_router;
use goap_gen::registry::v1::domain_service_router;
use goap_graph::{
    BlackboardFacet, EventSink, Graph, GraphEvent, Memory, Postgres, Repo, TypeCatalog,
};
use goap_graphsvc::{self as graphsvc, BoxError, Handler, Options};
use goap_platform::{
    Events, Secrets, Server, env, fatal, logger, migrate, optional_events, optional_postgres,
};
use goap_typecat::Live;

/// The longest wait between two loads of the type catalogue.
const MAX_DELAY: Duration = Duration::from_secs(60);

/// The next delay of a retry: doubled, at most a minute.
fn next_delay(d: Duration) -> Duration {
    (2 * d).min(MAX_DELAY)
}

/// The domains `need` names that the catalogue does not hold.
fn missing(have: &BTreeMap<String, String>, need: &[String]) -> Vec<String> {
    need.iter()
        .filter(|ns| !have.contains_key(ns.as_str()))
        .cloned()
        .collect()
}

/// loadTypes loads the type catalogue until it holds the domains the seeds write to, then runs the seeds.
async fn load_types<F, Fut>(ctx: &Ctx, types: &Live, need: &[String], seed: F)
where
    F: FnOnce() -> Fut,
    Fut: Future<Output = ()>,
{
    let mut delay = Duration::from_secs(1);
    loop {
        let err = match types.reload(ctx).await {
            Ok(()) => {
                let gone = missing(&types.get().domains(), need);
                if gone.is_empty() {
                    tracing::info!(
                        domains = types.get().domains().len(),
                        "type catalogue loaded"
                    );
                    seed().await;
                    return;
                }
                format!("domains {gone:?} not published yet")
            }
            Err(e) => e.to_string(),
        };
        tracing::warn!(err = %err, retry = ?delay, "type catalogue");
        tokio::select! {
            _ = ctx.done() => return,
            _ = tokio::time::sleep(delay) => {}
        }
        delay = next_delay(delay);
    }
}

/// The domain registry as the source of the type catalogue (Go's `typecat.NewLive(domains.Domains)`).
struct DomainSource(domainsvc::Service);

#[async_trait]
impl goap_typecat::Source for DomainSource {
    async fn domains(&self, ctx: &Ctx) -> Result<Vec<goap_def::Domain>, goap_typecat::BoxError> {
        self.0.domains(ctx).await.map_err(|e| Box::new(e) as _)
    }
}

/// The graph of this process as the directory reads it (Go hands `*graph.Graph` itself to `access.Directory`). The
/// graph holds the authorizers built over the directory, so the directory holds it weakly.
struct InProcess(Weak<Graph>);

impl InProcess {
    fn graph(&self) -> Result<Arc<Graph>, goap_graphapi::Error> {
        self.0
            .upgrade()
            .ok_or_else(|| goap_graphapi::Error::msg("the graph is gone"))
    }
}

#[async_trait]
impl goap_graphsnap::Graph for InProcess {
    async fn branch_head(
        &self,
        ctx: &Ctx,
        namespace: &str,
        name: &str,
    ) -> Result<Baseline, goap_graphapi::Error> {
        self.graph()?.branch_head(ctx, namespace, name).await
    }

    async fn baseline_graph(
        &self,
        ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), goap_graphapi::Error> {
        self.graph()?.baseline_graph(ctx, id).await
    }
}

#[async_trait]
impl access::Graph for InProcess {
    async fn structures(&self, _ctx: &Ctx) -> Result<Structures, goap_graphapi::Error> {
        Ok(self.graph()?.structures())
    }
}

/// Bus publishes the events of the graph on NATS: each event as its own JSON (Go's `g.Observe(events)`).
struct Bus(Events);

#[async_trait]
impl EventSink for Bus {
    async fn publish(&self, ctx: &Ctx, subject: &str, ev: &GraphEvent) -> goap_graph::Result<()> {
        self.0
            .publish(ctx, subject, ev)
            .await
            .map_err(|e| goap_graph::Error::msg(e.to_string()))
    }
}

/// The criticality policy of the items as the graph asks it (Go's `criticality.ItemPolicy` satisfies the graph's
/// interface structurally); its refusal keeps the text of the policy.
struct Criticality(goap_criticality::ItemPolicy);

#[async_trait]
impl goap_graph::ItemPolicy for Criticality {
    async fn check(
        &self,
        ctx: &Ctx,
        c: &goap_domain::Change,
        it: &goap_domain::ChangeItem,
    ) -> goap_graph::Result<()> {
        self.0
            .check(ctx, c, it)
            .await
            .map_err(|e| goap_graph::Error::msg(e.to_string()))
    }
}

/// The seed of the graph asked by GOAP_GRAPH_SEED: the domains it needs beyond the built-in ones, and the
/// development or demo data (`dev`: the document-repository MCP, `demo`: the demo data too).
fn dev_seed(seed: &str) -> (Vec<String>, Option<graphsvc::DevFn>) {
    let demo = seed == "demo";
    let need = if demo {
        vec!["alm".to_string()]
    } else {
        Vec::new()
    };
    if !demo && seed != "dev" {
        return (need, None);
    }
    let dev = graphsvc::dev_fn(move |ctx, g| {
        Box::pin(async move {
            if demo {
                let seeded = goap_devseed::demo(ctx, g).await;
                tracing::info!(loaded = matches!(seeded, Ok(true)), "demo seed");
                seeded.map_err(|e| Box::new(e) as BoxError)?;
            }
            goap_devseed::document_repository(ctx, g)
                .await
                .map_err(|e| Box::new(e) as BoxError)?;
            Ok(())
        })
    });
    (need, Some(dev))
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    // the facts of the risk register are items of a change (ADR 0065)
    goap_risk::register();
    goap_assess::register();
    goap_verify::register();
    logger("graph");
    let tel = goap_telemetry::setup(&ctx, "graph");
    let mut repo: Arc<dyn Repo> = Arc::new(Memory::new());
    let mut domain_store: Arc<dyn domainsvc::Store> = Arc::new(domainsvc::MemoryStore::new());
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    let pool = optional_postgres(&ctx, Some(&goap_graph::MIGRATIONS)).await;
    if let Some(pool) = &pool {
        // the domain registry keeps its versions next to the graph they define
        if let Err(e) = migrate(&ctx, pool, &domainsvc::MIGRATIONS, ".").await {
            fatal("migrate domains", e);
        }
        repo = Arc::new(Postgres::new(pool.clone()));
        domain_store = Arc::new(domainsvc::SqlStore::Postgres(pool.clone()));
        let p = pool.clone();
        srv.readiness(move |_ctx| {
            let p = p.clone();
            async move { sqlx::query("SELECT 1").execute(&p).await.map(|_| ()) }
        });
    }
    let events = optional_events(&ctx).await;
    if let Some(bus) = &events {
        let bus = bus.clone();
        srv.readiness(move |ctx| {
            let bus = bus.clone();
            async move { bus.ready(&ctx).await }
        });
    }

    // the graph judges nodes by the types of the published domains (ADR 0012): the domain registry served here is the
    // reference (ADR 0125 §3), the graph holds a copy reloaded when a version is published or archived; the methodology
    // registry follows the same publications through the events
    let mut domains = domainsvc::Service::new(domain_store);
    domains.events = events
        .clone()
        .map(|e| Arc::new(e) as Arc<dyn domainsvc::Publisher>);
    let types = Arc::new(Live::new(Arc::new(DomainSource(domains.clone()))));
    {
        let types = types.clone();
        domains.changed = Some(domainsvc::changed_fn(
            move |ctx: Ctx, r: goap_def::Record| {
                let types = types.clone();
                async move {
                    if let Err(e) = types.reload(&ctx).await {
                        tracing::error!(domain = %r.domain.name, err = %e, "type catalogue");
                    }
                }
            },
        ));
    }

    // the hooks of the graph are set before the seeds start (they run in the background, and write through them)
    let mut built: Option<(Arc<Directory>, Arc<Authorizer>)> = None;
    let g = Arc::new_cyclic(|me: &Weak<Graph>| {
        let mut g = Graph::new(repo);
        // the principal behind each event of the impact logs (ADR 0029)
        g.caller = Some(Arc::new(graphsvc::caller));
        // confidence, rounds and deadline settle the decision points (ADR 0067)
        g.decision_policy = Some(Arc::new(goap_decision::Policy));
        // a verifier is not the producer (ADR 0075)
        g.review_policy = Some(Arc::new(goap_verify::Policy));
        // node and baseline events feed the node index (ADR 0026)
        if let Some(bus) = &events {
            g.observe(Arc::new(Bus(bus.clone())));
        }
        {
            let types = types.clone();
            g.set_types(move || Some(types.get() as Arc<dyn TypeCatalog>));
        }
        let directory = Arc::new(Directory::new(Arc::new(InProcess(me.clone()))));
        let authorizer = match Authorizer::new(directory.clone()) {
            Ok(a) => Arc::new(a),
            Err(e) => fatal("authorizer", e),
        };
        let authz: Arc<dyn goap_authz::Authorizer> = authorizer.clone();
        g.authorizer = Some(graphsvc::transition_authorizer(Some(authz.clone())));
        g.change_authorizer = Some(graphsvc::change_transition_authorizer(Some(authz.clone())));
        g.project_move_gate = Some(graphsvc::project_move_gate(
            Some(authz.clone()),
            Some(directory.clone()),
        ));
        g.item_authorizer = Some(graphsvc::item_authorizer(
            Some(authz),
            Some(directory.criticality_resolver()),
        ));
        // the oracle and the lifetime a level accepts (ADR 0075 §3)
        g.item_policy = Some(Arc::new(Criticality(goap_criticality::item_policy(Some(
            directory.criticality_resolver(),
        )))));
        let facet: BlackboardFacet = directory.criticality_facet();
        g.facets.insert(FACET_CRITICALITY_POLICY.into(), facet);
        // the history of a change (ADR 0109): no registry here, so no step relies on another for its standing
        g.log_facets = graphsvc::log_facets(None);
        g.validators = vec![
            Arc::new(AdminFloorValidator),
            Arc::new(goap_llmcfg::ProtectedAliasValidator),
        ];
        // SubChangeValidator and Lifecycles, the guardian of the changes (ADR 0058, 0102), need the registry service
        // itself (its methodology store): only goap-dev, which holds it in process, wires them; the registry client has
        // no RPC for them, so this graph merges by the landable states of the domains only
        built = Some((directory, authorizer));
        g
    });
    let (directory, authorizer) = built.expect("the hooks are built with the graph");

    // baselines written whole before they were stored as deltas are compacted, once, in the background (ADR 0032)
    {
        let (g, ctx) = (g.clone(), ctx.clone());
        tokio::spawn(async move {
            match g.compact_baselines(&ctx).await {
                Err(e) => tracing::error!(err = %e, "compact baselines"),
                Ok(n) if n > 0 => tracing::info!(rewritten = n, "baselines compacted"),
                Ok(_) => {}
            }
        });
    }
    // every start publishes the domain versions of GOAP_DOMAINS_DIR not stored yet (ADR 0113)
    let dir = env("GOAP_DOMAINS_DIR", "domains");
    if !dir.is_empty() {
        let system =
            goap_authz::with_principal(&ctx, goap_authz::system("graph", &[access::ROLE_ADMIN]));
        match domains.seed_domains(&system, &dir).await {
            Err(e) => tracing::error!(dir = %dir, err = %e, "import domains"),
            Ok(doms) => tracing::info!(dir = %dir, domains = ?doms, "domains imported"),
        }
    }
    // the roots of the organisation and of the projects, before serving (ADR 0054): the structures are tagged by the
    // built-in organisation domain, known before the catalogue loads the others (graphsvc::boot repeats it, a no-op)
    if let Err(e) = Box::pin(g.bootstrap(&graphsvc::system(&ctx))).await {
        fatal("bootstrap", e);
    }
    // the built-in domains (organisation, platform) are always there; the demo seed needs alm. GOAP_GRAPH_SEED=dev
    // seeds the development data (the document-repository MCP), =demo the demo data too; otherwise an administrator
    // loads the demo from the settings (LoadDemoData)
    let (need, dev) = dev_seed(&env("GOAP_GRAPH_SEED", ""));
    {
        let (g, ctx, types) = (g.clone(), ctx.clone(), types.clone());
        tokio::spawn(async move {
            load_types(&ctx, &types, &need, || async {
                // the model gateway configuration (providers, models, aliases) is graph data: seeded when the graph
                // has none
                let mut opts = Options {
                    require_hooks: true,
                    dev,
                    ..Default::default()
                };
                match goap_modelgw::boot_config(
                    &ctx,
                    &env("GOAP_MODELS_CONFIG", ""),
                    &Secrets::new(),
                )
                .await
                {
                    Ok(m) => opts.models = m,
                    Err(e) => tracing::error!(err = %e, "models config"),
                }
                // the same bootstrap as every composition (ADR 0071), once the type catalogue holds the domains the
                // seeds write to
                match Box::pin(graphsvc::boot(&ctx, &g, &opts)).await {
                    Err(e) => tracing::error!(err = %e, "boot"),
                    Ok(r) => tracing::info!(
                        access = r.access,
                        builtins = r.builtins,
                        models = r.models,
                        "platform bootstrapped"
                    ),
                }
            })
            .await;
        });
    }
    let authz: Arc<dyn goap_authz::Authorizer> = authorizer.clone();
    domains.authz = Some(authz.clone());
    let mut handler = Handler::new(g.clone());
    handler.events = events
        .clone()
        .map(|e| Arc::new(e) as Arc<dyn graphsvc::Publisher>);
    handler.authz = Some(authz);
    handler.floor = Some(Arc::new(authorizer.floor()));
    // the "Load demo data" setting; the alm domain must be published
    handler.demo = Some(goap_devseed::demo_loader(Some(directory)));
    {
        let types = types.clone();
        handler.types = Some(Arc::new(move || Some(types.get())));
    }
    let handler = Arc::new(handler);
    srv.mount(graph_service_router(
        handler.clone(),
        goap_telemetry::handler_options()
            .with(handler.identify())
            .with(goap_identity::command_interceptor())
            .with(handler.personal_scope())
            .with(handler.ensure_caller()),
    ));
    srv.mount(domain_service_router(
        Arc::new(domainsvc::Handler::new(Arc::new(domains))),
        goap_telemetry::handler_options().with(goap_identity::command_interceptor()),
    ));
    let res = srv.run().await;
    if let Some(bus) = events {
        bus.close().await;
    }
    if let Some(pool) = pool {
        pool.close().await;
    }
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn delays_double_up_to_a_minute() {
        let mut d = Duration::from_secs(1);
        let mut seen = vec![];
        for _ in 0..8 {
            seen.push(d.as_secs());
            d = next_delay(d);
        }
        assert_eq!(seen, vec![1, 2, 4, 8, 16, 32, 60, 60]);
    }

    #[test]
    fn the_seed_names_what_it_needs() {
        let (need, dev) = dev_seed("");
        assert!(need.is_empty() && dev.is_none());
        let (need, dev) = dev_seed("dev");
        assert!(need.is_empty() && dev.is_some());
        let (need, dev) = dev_seed("demo");
        assert_eq!(need, vec!["alm".to_string()]);
        assert!(dev.is_some());
    }

    #[test]
    fn missing_domains() {
        let have: BTreeMap<String, String> = [("alm".to_string(), "1.0.0".to_string())].into();
        assert!(missing(&have, &["alm".into()]).is_empty());
        assert_eq!(missing(&have, &["x".into()]), vec!["x".to_string()]);
    }
}
