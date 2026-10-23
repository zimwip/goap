//! goap-graph-rs: the Rust graph service (ADR 0163). The environment of the Go service: `GOAP_HTTP_ADDR`,
//! `GOAP_DB_DSN` (PostgreSQL), else `GOAP_SQLITE_PATH` (SQLite, `GOAP_SQLITE_READERS` read-only connections), else a
//! database in memory. `GOAP_NATS_URL` set, the events of the graph are written in its outbox, which the Go service's
//! relay delivers (ADR 0136).
use std::sync::Arc;

use anyhow::Context;
use graph_service::{State, app};
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
    let store = Store::open(&cfg).await.context("database")?;
    let mut addr = std::env::var("GOAP_HTTP_ADDR").unwrap_or_else(|_| ":8080".into());
    if addr.starts_with(':') {
        addr = format!("0.0.0.0{addr}");
    }
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .with_context(|| format!("listening on {addr}"))?;
    tracing::info!(%addr, dialect = ?store.dialect(), "goap-graph-rs serving");
    let mut state = State::new(store);
    state.outbox = std::env::var("GOAP_NATS_URL").is_ok_and(|u| !u.is_empty());
    if !state.outbox {
        tracing::warn!("GOAP_NATS_URL not set: events disabled");
    }
    axum::serve(listener, app(Arc::new(state)))
        .with_graceful_shutdown(shutdown())
        .await?;
    Ok(())
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
