//! goap-graph-rs: the Rust graph service (ADR 0163). The environment of the Go service: `GOAP_HTTP_ADDR`,
//! `GOAP_DB_DSN` (PostgreSQL), else `GOAP_SQLITE_PATH` (SQLite, `GOAP_SQLITE_READERS` read-only connections), else a
//! database in memory. `GOAP_NATS_URL` set, the events of the graph and of the domain registry are written in their
//! outboxes and delivered to the bus (ADR 0136; `GOAP_OUTBOX_POLL`, `GOAP_OUTBOX_RETENTION`).
//!
//! Every start publishes the domain versions of `GOAP_DOMAINS_DIR` (default `domains`) not stored yet. `goap-graph-rs
//! bootstrap` runs the platform bootstrap once against the database, then exits (its events stay in the outbox: the
//! service delivers them); the service bootstraps itself only with `GOAP_GRAPH_BOOTSTRAP=1` or a database in memory.
//! The bootstrap seeds the models of `GOAP_MODELS_CONFIG` (else the default, Anthropic when `ANTHROPIC_API_KEY` is
//! set), declares the sources of `GOAP_GRAPH_SOURCES`, and with `GOAP_GRAPH_SEED=dev` the development data, `=demo`
//! the demo too.
use std::sync::Arc;

use anyhow::Context;
use graph_service::{
    Domains, Graph, State, app,
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
    let sources = parse_sources(&env("GOAP_GRAPH_SOURCES")).map_err(anyhow::Error::msg)?;
    let store = Store::open(&cfg).await.context("database")?;
    let nats = env("GOAP_NATS_URL");
    if bootstrap_only {
        let mut state = State::new(store);
        // the events of the bootstrap stay in the outbox: the service delivers them
        state.outbox = !nats.is_empty();
        let state = Arc::new(state);
        seed_domains(&state).await;
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
    let relays = if nats.is_empty() {
        tracing::warn!("GOAP_NATS_URL not set: events disabled");
        Vec::new()
    } else {
        let sink = Sink::connect(&nats).await.with_context(|| format!("NATS at {nats}"))?;
        state.relay(sink).to_vec()
    };
    // the job service (ADR 0149): the exports and the snapshot imports are its jobs, created here with the services'
    // token and run by the worker of this process
    state.jobs = Some(Arc::new(Jobs::from_env()));
    let state = Arc::new(state);
    seed_domains(&state).await;
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

/// Publishes the domain versions of `GOAP_DOMAINS_DIR` not stored yet (ADR 0113): a failure is logged.
async fn seed_domains(state: &Arc<State>) {
    let dir = std::env::var("GOAP_DOMAINS_DIR").unwrap_or_else(|_| "domains".into());
    if dir.is_empty() {
        return;
    }
    match (Domains { state: state.clone() })
        .seed_domains(std::path::Path::new(&dir))
        .await
    {
        Ok(doms) => tracing::info!(%dir, ?doms, "domains imported"),
        Err(err) => tracing::error!(%dir, %err, "import domains"),
    }
}

/// The platform bootstrap with the options of the environment.
async fn boot(
    state: &Arc<State>,
    sources: Vec<graph_core::sources::Source>,
) -> anyhow::Result<graph_service::graph::boot::Report> {
    let models = ModelsConfig::initial(&env("GOAP_MODELS_CONFIG")).unwrap_or_else(|e| {
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
