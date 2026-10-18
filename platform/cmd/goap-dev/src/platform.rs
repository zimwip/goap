//! The model gateway, the node index and the MCP hub (Go's platform.go).

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_connectorkit::Connector;
use goap_ctx::Ctx;
use goap_eventsvc::Hub;
use goap_gen::connector::v1::{InvokeRequest, InvokeResponse};
use goap_graph::{EventSink, GraphEvent};
use goap_indexersvc as indexersvc;
use goap_mcpsvc as mcpsvc;
use goap_modelgw::{self as modelgw, AnthropicProxy, Instrument, InstrumentEmbed};
use goap_platform::{Secrets, env_bool, env_duration, env_int};
use parking_lot::RwLock;

use crate::app::{BoxError, Env, wrap};
use crate::graph::GraphPart;
use crate::local::{Stores, local_secret, local_url};
use crate::registry::{RegistryPart, publication_of};

const DAY: Duration = Duration::from_secs(24 * 60 * 60);

/// PlatformPart is what the engine and the handlers reach out with: the model gateway, the node index and the MCP
/// hub.
pub(crate) struct PlatformPart {
    pub gw: Arc<modelgw::Service>,
    pub indexer: Arc<indexersvc::Service>,
    pub index_sink: indexersvc::Sink,
    pub hub: Arc<mcpsvc::Service>,
    /// The connectors the hub runs in process: the built-in ones join them once the engine exists (build_engine).
    pub connectors: Arc<Connectors>,
    pub conn_token: String,
    /// The gateway's Anthropic endpoint, where the Claude coding agent's sessions call the model (ADR 0123 §6).
    pub models: Arc<AnthropicProxy>,
}

/// build_platform creates the model gateway, the node index following the graph and the MCP hub. The built-in
/// connectors join the hub's connectors once the engine exists (build_engine).
pub(crate) async fn build_platform(
    e: &Env,
    st: &Stores,
    gp: &GraphPart,
    rp: &RegistryPart,
) -> Result<PlatformPart, BoxError> {
    let g = &gp.g;
    let authz: Arc<dyn goap_authz::Authorizer> = gp.authorizer.clone();
    let mut gw = modelgw::Service::new(
        Arc::new(goap_llmcfg::Directory::new(g.clone())),
        st.models.clone(),
        Some(resolve(e.secrets.clone())),
    );
    gw.vault = Some(vault(e.secrets.clone()));
    let (complete, embed) = instrumentation();
    gw.router.instrument = Some(complete);
    gw.router.instrument_embed = Some(embed);
    // the roles a model requires are held on the caller's project (ADR 0043)
    gw.authz = Some(authz.clone());
    // a measured behaviour cost is measured again after this (ADR 0093)
    gw.cost_ttl = DAY
        * u32::try_from(
            env_int(
                "GOAP_LLM_BEHAVIOR_COST_TTL_DAYS",
                (modelgw::DEFAULT_COST_TTL.as_secs() / DAY.as_secs()) as i64,
            )
            .max(0),
        )
        .unwrap_or(u32::MAX);
    // the exchange of the calls no change log keeps (ADR 0089)
    gw.call_prompts = env_bool("GOAP_LLM_CALL_PROMPTS", true);
    let gw = gw.shared();
    gw.reload(&e.ctx).await.map_err(|err| wrap("models", err))?;
    // the ledger of calls (ADR 0089)
    {
        let (gw, ctx) = (gw.clone(), e.ctx.clone());
        let days = env_int(
            "GOAP_LLM_CALL_RETENTION_DAYS",
            modelgw::DEFAULT_CALL_RETENTION_DAYS,
        );
        tokio::spawn(async move { gw.keep_calls(&ctx, days).await });
    }
    // the node index follows the graph in-process (ADR 0026); embeddings go through the gateway, semantic search
    // needs an "embed" alias. The graph is published again at start: an index kept in SQLite catches up, a new one
    // fills.
    let indexer = Arc::new(indexersvc::Service::with_store(
        st.index.clone(),
        st.index_name,
        None,
        Some(gw.clone() as Arc<dyn goap_llm::Embedder>),
        Some(authz),
    ));
    // the projects a caller may see, and a project with its sub-projects (ADR 0095)
    indexer.set_access(Some(gp.directory.clone()));
    let index_sink = indexersvc::Sink::new(&e.ctx, indexer.clone());
    let republish = {
        let (g, sink) = (g.clone(), index_sink.clone());
        indexersvc::republish_fn(move |ctx: Ctx| {
            let (g, sink) = (g.clone(), sink.clone());
            async move {
                g.republish(&ctx, &IndexSink(sink))
                    .await
                    .map_err(|e| Box::new(e) as BoxError)
            }
        })
    };
    indexer.set_republish(Some(republish.clone()));
    let _ = e.late.sink.set(Arc::new(GraphSinks {
        index: index_sink.clone(),
        bus: rp.bus.clone(),
    }));
    {
        let ctx = e.ctx.clone();
        tokio::spawn(async move {
            match republish(ctx).await {
                Err(e) => tracing::error!(err = %e, "index: initial publication"),
                Ok(n) => tracing::info!(versions = n, "node index: graph published"),
            }
        });
    }
    // MCP hub in-process; the local file system, workspace (ADR 0118) and Claude coding agent (ADR 0123) connectors
    // and the built-in connectors (ADR 0028, added once the engine exists) run inside too, other connectors register
    // over HTTP like in the distributed platform
    let initial: HashMap<String, Arc<dyn Connector>> = HashMap::from([
        (
            "localfs".to_string(),
            Arc::new(goap_connector_localfs::Connector::default()) as Arc<dyn Connector>,
        ),
        (
            goap_connector_workspace::ID.to_string(),
            Arc::new(goap_connector_workspace::Connector::new(Vec::new())),
        ),
        (
            goap_connector_workspace::DOCKER_ID.to_string(),
            Arc::new(goap_connector_workspace::Connector::new_docker(
                &e.cfg.docker_host,
                Vec::new(),
            )),
        ),
        (
            goap_connector_claudeagent::ID.to_string(),
            Arc::new(goap_connector_claudeagent::Connector::new(
                e.cfg.claude_agent_dir.clone(),
                Vec::new(),
            )),
        ),
    ]);
    // connectors register with a shared token (a service without one cannot register): generated like the JWT
    // secret when none is configured, and shown so that a connector started by hand can use it
    let mut conn_token = e.cfg.connector_token.clone();
    if conn_token.is_empty() {
        conn_token =
            local_secret(&st.dir, "connector_token").map_err(|err| wrap("connector token", err))?;
        let file = std::path::Path::new(&st.dir).join("connector_token");
        tracing::info!(file = %file.display(), "connector token generated: start connectors with GOAP_CONNECTOR_TOKEN set to it");
    }
    let connectors = Arc::new(Connectors(RwLock::new(mcpsvc::InprocInvoker {
        connectors: initial,
        remote: Some(Arc::new(mcpsvc::ConnectInvoker::new(conn_token.clone()))),
    })));
    let mut hub = mcpsvc::Service::new(st.mcp.clone());
    hub.directory = Some(Arc::new(mcpsvc::Directory::new(g.clone())));
    hub.invoker = Some(connectors.clone());
    hub.secrets = Some(mcpsvc::resolve_secret(e.secrets.clone()));
    hub.lease = env_duration("GOAP_CONNECTOR_LEASE", mcpsvc::DEFAULT_LEASE);
    // the sessions of the Claude coding agent call the model through the gateway, each call a row of the ledger (ADR
    // 0123 §6): the hub gives each call a token the gateway checks, signed with a secret generated like the others
    let mut proxy_secret = e.cfg.model_proxy_secret.clone();
    if proxy_secret.is_empty() {
        proxy_secret = local_secret(&st.dir, "model_proxy_secret")
            .map_err(|err| wrap("model proxy secret", err))?;
    }
    let mut proxy_url = e.cfg.model_proxy_url.clone();
    if proxy_url.is_empty() {
        proxy_url = local_url(&e.cfg.addr) + modelgw::ANTHROPIC_PATH.trim_end_matches('/');
    }
    hub.models = Some(mcpsvc::ModelEndpoint {
        url: proxy_url,
        secret: proxy_secret.clone().into_bytes(),
        ..Default::default()
    });
    let hub = Arc::new(hub);
    let models = Arc::new(AnthropicProxy {
        service: gw.clone(),
        secret: proxy_secret.into_bytes(),
        http: None,
    });
    Ok(PlatformPart {
        gw,
        indexer,
        index_sink,
        hub,
        connectors,
        conn_token,
        models,
    })
}

/// Connectors are the connectors the hub runs in process, shared with the hub's invoker (Go shares the map, which the
/// built-in connectors join once the engine exists).
pub(crate) struct Connectors(RwLock<mcpsvc::InprocInvoker>);

impl Connectors {
    /// add makes connectors available to the hub.
    pub fn add(&self, more: HashMap<String, Arc<dyn Connector>>) {
        self.0.write().connectors.extend(more);
    }

    /// all are the connectors in process.
    pub fn all(&self) -> HashMap<String, Arc<dyn Connector>> {
        self.0.read().connectors.clone()
    }
}

#[async_trait]
impl mcpsvc::Invoker for Connectors {
    async fn invoke(
        &self,
        ctx: &Ctx,
        endpoint: &str,
        req: InvokeRequest,
    ) -> Result<InvokeResponse, mcpsvc::Error> {
        let inv = self.0.read().clone();
        inv.invoke(ctx, endpoint, req).await
    }
}

/// The sink of the indexer as the graph's event sink (Go's `indexersvc.Sink` satisfies `graph.EventSink`).
struct IndexSink(indexersvc::Sink);

#[async_trait]
impl EventSink for IndexSink {
    async fn publish(&self, ctx: &Ctx, subject: &str, ev: &GraphEvent) -> goap_graph::Result<()> {
        self.0
            .publish(ctx, subject, ev)
            .await
            .map_err(goap_graph::Error::from)
    }
}

/// GraphSinks are the observers of the graph: the node index and the event stream of the web (Go's
/// `engine.Publishers{indexSink, bus}`: every one is called, the first error is returned).
struct GraphSinks {
    index: indexersvc::Sink,
    bus: Arc<Hub>,
}

#[async_trait]
impl EventSink for GraphSinks {
    async fn publish(&self, ctx: &Ctx, subject: &str, ev: &GraphEvent) -> goap_graph::Result<()> {
        let res = self.index.publish(ctx, subject, ev).await;
        if let Some(p) = publication_of(ev) {
            self.bus.publish(ctx, subject, p);
        }
        res.map_err(goap_graph::Error::from)
    }
}

/// The provider API keys are references resolved by the secrets of the platform.
fn resolve(secrets: Arc<Secrets>) -> modelgw::SecretsFn {
    Arc::new(move |ctx: Ctx, reference: String| {
        let secrets = secrets.clone();
        Box::pin(async move {
            secrets
                .resolve(&ctx, &reference)
                .await
                .map_err(|e| Box::new(e) as modelgw::BoxError)
        })
    })
}

/// A raw API key written to the backing store of the secrets.
fn vault(secrets: Arc<Secrets>) -> modelgw::VaultFn {
    Arc::new(
        move |ctx: Ctx, path: String, field: String, value: String| {
            let secrets = secrets.clone();
            Box::pin(async move {
                secrets
                    .put(&ctx, &path, &field, &value)
                    .await
                    .map_err(|e| Box::new(e) as modelgw::BoxError)
            })
        },
    )
}

/// The OpenTelemetry GenAI instrumentation of the provider calls.
fn instrumentation() -> (Instrument, InstrumentEmbed) {
    let genai = Arc::new(goap_telemetry::GenAI::new());
    let g = genai.clone();
    let complete: Instrument = Arc::new(move |ctx, provider, model, req, call| {
        let g = g.clone();
        Box::pin(async move { g.instrument(&ctx, &provider, &model, &req, call).await })
    });
    let embed: InstrumentEmbed = Arc::new(move |ctx, provider, model, texts, call| {
        let g = genai.clone();
        Box::pin(async move {
            g.instrument_embed(&ctx, &provider, &model, texts, call)
                .await
        })
    });
    (complete, embed)
}
