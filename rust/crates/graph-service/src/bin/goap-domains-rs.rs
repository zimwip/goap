//! goap-domains-rs: the domain registry (ADR 0125 §3, ADR 0167), a service of its own: the domains, the definition of
//! the graphs, on a database of its own (`GOAP_DB_DSN`, else `GOAP_SQLITE_PATH`, else in memory), served over Connect
//! (`registry.v1.DomainService`) at `GOAP_HTTP_ADDR`. `GOAP_NATS_URL` set, its events are written in its outbox and
//! delivered to the bus (`goap.registry.domain.*`), which the graphs follow.
//!
//! Every start publishes the built-in domains (`GOAP_BUILTIN_DOMAINS_DIR`, else `GOAP_DOMAINS_DIR/builtin`, else
//! `lib/domains/builtin`), protected, then the shipped domains of `GOAP_DOMAINS_DIR` (default `domains`), the versions
//! not stored yet. Who may write a domain is the organisation's rules, read from the platform graph at
//! `GOAP_GRAPH_URL` (default `http://localhost:8081`) when a person asks. A protected domain is refused every write over
//! RPC; the command line, run against the same database, writes it:
//!
//! ```text
//! goap-domains-rs publish <file.yaml>...   publish domain versions (a protected domain included)
//! goap-domains-rs protect <name>...        protect domains
//! goap-domains-rs unprotect <name>...      lift their protection
//! goap-domains-rs list                     the versions stored, with their status and protection
//! ```
//!
//! The events of a command stay in the outbox: the service delivers them.
use std::{path::Path, sync::Arc};

use anyhow::Context;
use graph_service::{Domains, SYSTEM_DOMAINS, State, builtin_dir, domains_app, relay::Sink};
use graph_store::{Store, StoreConfig};

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info,tokio_postgres=warn".into()),
        )
        .init();
    let cfg = StoreConfig::from_env();
    let args: Vec<String> = std::env::args().skip(1).collect();
    if !args.is_empty() {
        if matches!(cfg, StoreConfig::Memory) {
            anyhow::bail!("the command line writes the registry's database: set GOAP_DB_DSN or GOAP_SQLITE_PATH");
        }
        let mut state = State::new(Store::open_domains(&cfg).await.context("database")?);
        // the events stay in the outbox: the service delivers them
        state.outbox = true;
        let domains = Domains { state: Arc::new(state) };
        return command(&domains, &args[0], &args[1..]).await;
    }
    if matches!(cfg, StoreConfig::Memory) {
        tracing::warn!("GOAP_DB_DSN and GOAP_SQLITE_PATH not set: a database in memory");
    }
    let store = Store::open_domains(&cfg).await.context("database")?;
    let mut addr = std::env::var("GOAP_HTTP_ADDR").unwrap_or_else(|_| ":8080".into());
    if addr.starts_with(':') {
        addr = format!("0.0.0.0{addr}");
    }
    let mut state = State::new(store);
    let nats = env("GOAP_NATS_URL");
    let relay = if nats.is_empty() {
        tracing::warn!("GOAP_NATS_URL not set: events disabled");
        None
    } else {
        let sink = Sink::connect(&nats).await.with_context(|| format!("NATS at {nats}"))?;
        Some(state.relay_domains(sink))
    };
    let graph = std::env::var("GOAP_GRAPH_URL").unwrap_or_else(|_| "http://localhost:8081".into());
    state.remote = Some(Arc::new(graph_service::platform::Remote::new(&graph)));
    let state = Arc::new(state);
    let domains = Domains { state: state.clone() };
    let dir = std::env::var("GOAP_DOMAINS_DIR").unwrap_or_else(|_| "domains".into());
    let builtins = builtin_dir(Path::new(&dir));
    let doms = domains
        .seed_builtin_domains(&builtins)
        .await
        .map_err(anyhow::Error::msg)
        .context("built-in domains")?;
    tracing::info!(dir = %builtins.display(), ?doms, "built-in domains");
    if !dir.is_empty() {
        match domains.seed_domains(Path::new(&dir)).await {
            Ok(doms) => tracing::info!(%dir, ?doms, "domains imported"),
            Err(err) => tracing::error!(%dir, %err, "import domains"),
        }
    }
    if let Some(r) = relay {
        tokio::spawn(r.run(state.clone()));
    }
    let listener = tokio::net::TcpListener::bind(&addr)
        .await
        .with_context(|| format!("listening on {addr}"))?;
    tracing::info!(%addr, dialect = ?state.store.dialect(), "goap-domains-rs serving");
    axum::serve(listener, domains_app(state))
        .with_graceful_shutdown(shutdown())
        .await?;
    Ok(())
}

/// A command of the command line (ADR 0167).
async fn command(domains: &Domains, name: &str, args: &[String]) -> anyhow::Result<()> {
    match name {
        "publish" => {
            anyhow::ensure!(!args.is_empty(), "usage: goap-domains-rs publish <file.yaml>...");
            for f in args {
                let src = std::fs::read_to_string(f).with_context(|| f.clone())?;
                let d = domains
                    .publish_local(&src, false, SYSTEM_DOMAINS)
                    .await
                    .map_err(|e| anyhow::anyhow!("{f}: {e}"))?;
                println!("published {}", d.unwrap_or_default());
            }
        }
        "protect" | "unprotect" => {
            anyhow::ensure!(!args.is_empty(), "usage: goap-domains-rs {name} <name>...");
            for n in args {
                domains
                    .protect(n, name == "protect", SYSTEM_DOMAINS)
                    .await
                    .map_err(anyhow::Error::msg)?;
                println!("{name}ed {n}");
            }
        }
        "list" => {
            println!("{:<16}{:<12}{:<12}PROTECTED", "NAME", "VERSION", "STATUS");
            for (n, v, s, p) in domains.versions().await.map_err(anyhow::Error::msg)? {
                println!("{n:<16}{v:<12}{s:<12}{p}");
            }
        }
        _ => anyhow::bail!("unknown command {name:?} (publish, protect, unprotect, list)"),
    }
    Ok(())
}

fn env(name: &str) -> String {
    std::env::var(name).unwrap_or_default()
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
