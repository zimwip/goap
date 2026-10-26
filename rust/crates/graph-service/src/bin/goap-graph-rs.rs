//! goap-graph-rs: the Rust graph service (ADR 0163). The environment of the Go service: `GOAP_HTTP_ADDR`,
//! `GOAP_DB_DSN` (PostgreSQL), else `GOAP_SQLITE_PATH` (SQLite, `GOAP_SQLITE_READERS` read-only connections), else a
//! database in memory. `GOAP_NATS_URL` set, the events of the graph and of the domain registry are written in their
//! outboxes and delivered to the bus (ADR 0136; `GOAP_OUTBOX_POLL`, `GOAP_OUTBOX_RETENTION`).
//!
//! The domains are the domain registry's (ADR 0167, `goap-domains-rs`, `GOAP_DOMAINS_URL`, default
//! `http://localhost:8096`): read at the start (the service waits for the built-in ones), then again on the registry's
//! events (`goap.registry.domain.published|deleted`) and after `platform::DOMAINS_TTL`. `goap-graph-rs bootstrap` runs the platform bootstrap once against the database, then exits (its events stay in the outbox: the
//! service delivers them); the service bootstraps itself only with `GOAP_GRAPH_BOOTSTRAP=1` or a database in memory.
//! The bootstrap seeds the models of `GOAP_MODELS_CONFIG` (else the default, Anthropic when its key is in Vault or
//! `ANTHROPIC_API_KEY`), declares the sources of `GOAP_GRAPH_SOURCES`, and with `GOAP_GRAPH_SEED=dev` the development data, `=demo`
//! the demo too. The methodologies of the changes are asked of the guardian service of the methodology registry at
//! `GOAP_REGISTRY_URL` (ADR 0168, default `http://localhost:8082`): what a change follows, the guards and the gates of
//! its moves, the guardians of its commits, the roles of its operations.
//!
//! `GOAP_GRAPH_SPACE=content` makes it a content graph (ADR 0154 §9): it holds the domains' content and reads the
//! organisation and the projects from the platform graph at `GOAP_PLATFORM_GRAPH_URL`, serves the
//! graph service alone, and has nothing to bootstrap.
use std::sync::Arc;

use anyhow::Context;
use graph_service::{
    DomainSource, Graph, State, app, content_app,
    graph::boot::{BootOptions, DevSeed, ModelsConfig, parse_sources},
    graph::jobs::Jobs,
    relay::Sink,
};
use graph_store::{Store, StoreConfig};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info,tokio_postgres=warn".into()),
        )
        .init();
    let cfg = StoreConfig::from_env();
    if matches!(cfg, StoreConfig::Memory) {
        tracing::warn!("GOAP_DB_DSN and GOAP_SQLITE_PATH not set: a database in memory");
    }
    let memory = matches!(cfg, StoreConfig::Memory);
    let bootstrap_only = std::env::args().nth(1).as_deref() == Some("bootstrap");
    let content = env("GOAP_GRAPH_SPACE") == "content";
    let platform_url = env("GOAP_PLATFORM_GRAPH_URL");
    if content && platform_url.is_empty() {
        anyhow::bail!("graph space: a content graph needs GOAP_PLATFORM_GRAPH_URL");
    }
    if bootstrap_only && content {
        tracing::info!("a content graph holds no roots nor platform data: nothing to bootstrap");
        return Ok(());
    }
    let sources = parse_sources(&env("GOAP_GRAPH_SOURCES")).map_err(anyhow::Error::msg)?;
    let store = Store::open(&cfg).await.context("database")?;
    let nats = env("GOAP_NATS_URL");
    let registry = Arc::new(graph_service::platform::Registry::new(
        &std::env::var("GOAP_DOMAINS_URL").unwrap_or_else(|_| "http://localhost:8096".into()),
    ));
    // a graph types nothing without the built-in domains: the registry, started first, seeds them
    registry.wait_for_builtins(60).await.context("domain registry")?;
    // the methodologies of the changes are the methodology registry's (ADR 0168): their guardian is asked there
    let guardian: Arc<dyn graph_core::guardian::Guardian> = Arc::new(graph_service::guardian::Client::new(
        &std::env::var("GOAP_REGISTRY_URL").unwrap_or_else(|_| "http://localhost:8082".into()),
    ));
    if bootstrap_only {
        let mut state = State::new(store);
        state.domains = Some(DomainSource::Remote(registry));
        state.guardian = Some(guardian);
        // the events of the bootstrap stay in the outbox: the service delivers them
        state.outbox = !nats.is_empty();
        let state = Arc::new(state);
        let r = boot(&state, sources).await?;
        tracing::info!(?r, "platform bootstrapped");
        return Ok(());
    }
    let mut addr = std::env::var("GOAP_HTTP_ADDR").unwrap_or_else(|_| ":8080".into());
    if addr.starts_with(':') {
        addr = format!("0.0.0.0{addr}");
    }
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .with_context(|| format!("listening on {addr}"))?;
    tracing::info!(%addr, dialect = ?store.dialect(), "goap-graph-rs serving");
    let mut state = State::new(store);
    state.domains = Some(DomainSource::Remote(registry.clone()));
    state.guardian = Some(guardian);
    if content {
        state.space = "content";
    }
    // the job service (ADR 0149): the exports and the snapshot imports are its jobs, created here with the services'
    // token and run by the worker of this process
    state.jobs = Some(Arc::new(Jobs::from_env()));
    let relays = if nats.is_empty() {
        tracing::warn!("GOAP_NATS_URL not set: events disabled");
        Vec::new()
    } else {
        let sink = Sink::connect(&nats).await.with_context(|| format!("NATS at {nats}"))?;
        vec![state.relay(sink)]
    };
    // the domains are the registry's: read again when it publishes or deletes one (and after `DOMAINS_TTL` anyway)
    if !nats.is_empty() {
        let client = async_nats::connect(&nats)
            .await
            .with_context(|| format!("NATS at {nats}"))?;
        for subject in ["goap.registry.domain.published", "goap.registry.domain.deleted"] {
            let mut sub = client.subscribe(subject).await.context("subscribe")?;
            let registry = registry.clone();
            tokio::spawn(async move {
                while futures::StreamExt::next(&mut sub).await.is_some() {
                    registry.invalidate().await;
                }
            });
        }
    }
    if content {
        state.remote = Some(Arc::new(graph_service::platform::Remote::new(&platform_url)));
        let state = Arc::new(state);
        tokio::spawn(Arc::new(Graph { state: state.clone() }).run_worker());
        for r in relays {
            tokio::spawn(r.run(state.clone()));
        }
        axum::serve(listener, content_app(state))
            .with_graceful_shutdown(shutdown())
            .await?;
        return Ok(());
    }
    let state = Arc::new(state);
    if memory || env("GOAP_GRAPH_BOOTSTRAP") == "1" {
        match boot(&state, sources).await {
            Ok(r) => tracing::info!(?r, "platform bootstrapped"),
            Err(e) => tracing::error!(err = %e, "boot"),
        }
    }
    tokio::spawn(Arc::new(Graph { state: state.clone() }).run_worker());
    for r in relays {
        tokio::spawn(r.run(state.clone()));
    }
    axum::serve(listener, app(state))
        .with_graceful_shutdown(shutdown())
        .await?;
    Ok(())
}

fn env(name: &str) -> String {
    std::env::var(name).unwrap_or_default()
}

/// The platform bootstrap with the options of the environment.
async fn boot(
    state: &Arc<State>,
    sources: Vec<graph_core::sources::Source>,
) -> anyhow::Result<graph_service::graph::boot::Report> {
    let models = ModelsConfig::initial(&env("GOAP_MODELS_CONFIG"))
        .await
        .unwrap_or_else(|e| {
            tracing::error!(err = %e, "models config");
            ModelsConfig::default()
        });
    let dev = match env("GOAP_GRAPH_SEED").as_str() {
        "demo" => DevSeed::Demo,
        "dev" => DevSeed::Dev,
        _ => DevSeed::None,
    };
    Graph { state: state.clone() }
        .boot(&BootOptions { models, sources, dev })
        .await
        .context("boot")
}

async fn shutdown() {
    let ctrl_c = tokio::signal::ctrl_c();
    #[cfg(unix)]
    {
        let mut term = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()).expect("SIGTERM");
        tokio::select! { _ = ctrl_c => {}, _ = term.recv() => {} }
    }
    #[cfg(not(unix))]
    let _ = ctrl_c.await;
}
