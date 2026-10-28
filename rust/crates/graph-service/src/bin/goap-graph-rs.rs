//! goap-graph-rs: the Rust graph service (ADR 0163). The environment of the Go service: `GOAP_HTTP_ADDR`,
//! `GOAP_DB_DSN` (PostgreSQL), else `GOAP_SQLITE_PATH` (SQLite, `GOAP_SQLITE_READERS` read-only connections), else a
//! database in memory. `GOAP_NATS_URL` set, the events of the graph and of the domain registry are written in their
//! outboxes and delivered to the bus (ADR 0136; `GOAP_OUTBOX_POLL`, `GOAP_OUTBOX_RETENTION`).
//!
//! The domains are the domain registry's (ADR 0167, `goap-domains-rs`, `GOAP_DOMAINS_URL`, default
//! `http://localhost:8096`): read at the start (the service waits for the built-in ones), then again on the registry's
//! events (`goap.registry.domain.published|deleted`, `service.ready.domains`), kept as read meanwhile. `goap-graph-rs bootstrap` runs the platform bootstrap once against the database, then exits (its events stay in the outbox: the
//! service delivers them); the service bootstraps itself only with `GOAP_GRAPH_BOOTSTRAP=1` or a database in memory.
//! The bootstrap seeds the models of `GOAP_MODELS_CONFIG` (else the default, Anthropic when its key is in Vault or
//! `ANTHROPIC_API_KEY`), declares the sources of `GOAP_GRAPH_SOURCES`, and with `GOAP_GRAPH_SEED=dev` the development data, `=demo`
//! the demo too. The methodologies of the changes are asked of the guardian service at `GOAP_GUARDIAN_URL`
//! (ADR 0168, default `http://localhost:8097`): what a change follows, the guards and the gates of
//! its moves, the guardians of its commits, the roles of its operations.
//!
//! `GOAP_GRAPH_SPACE=content` makes it a content graph (ADR 0154 §9): it holds the domains' content and reads the
//! organisation and the projects from the platform graph at `GOAP_PLATFORM_GRAPH_URL`, serves the
//! graph service alone, and has nothing to bootstrap.
use std::{sync::Arc, time::Duration};

use anyhow::Context;
use graph_service::{
    DomainSource, Graph, State, app, content_app,
    graph::boot::{BootOptions, DevSeed, ModelsConfig, parse_sources},
    graph::jobs::Jobs,
    relay::Sink,
};
use graph_store::{Store, StoreConfig};

/// mimalloc rather than the C library's allocator: the service allocates many small values per request, from many
/// threads (10 to 15 % less CPU per commit on PostgreSQL)
#[global_allocator]
static ALLOC: mimalloc::MiMalloc = mimalloc::MiMalloc;

/// The workers of the runtime when `TOKIO_WORKER_THREADS` names none. The service waits on its database: with a worker
/// per core, each statement wakes the connection's task on another thread and back, which costs more CPU than the
/// statement (20 to 30 % of a commit, four times the context switches) for no throughput.
const WORKERS: usize = 2;

/// The stack of a worker: the futures of a commit, unoptimized (the `dev-local` profile of `make dev`), overflow tokio's
/// 2 MiB, which aborts the process.
const STACK: usize = 16 << 20;

fn main() -> anyhow::Result<()> {
    let mut rt = tokio::runtime::Builder::new_multi_thread();
    rt.thread_stack_size(STACK);
    if std::env::var_os("TOKIO_WORKER_THREADS").is_none() {
        rt.worker_threads(WORKERS);
    }
    rt.enable_all().build()?.block_on(serve())
}

async fn serve() -> anyhow::Result<()> {
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
    // the bus, when there is one: the announcements of the services this one waits for, and its own (ADR 0169)
    let bus = if nats.is_empty() {
        None
    } else {
        Some(
            async_nats::connect(&nats)
                .await
                .with_context(|| format!("NATS at {nats}"))?,
        )
    };
    // a graph types nothing without the built-in domains: read from the registry at once; when it is not there, read
    // again as soon as it announces it serves or publishes a domain (ADR 0169), for as long as it takes
    let mut ready = match &bus {
        Some(c) => Some(futures::stream::select(
            c.subscribe(READY_DOMAINS).await.context("subscribe")?,
            c.subscribe(DOMAIN_PUBLISHED).await.context("subscribe")?,
        )),
        None => None,
    };
    registry
        .wait_for_builtins(ready.as_mut())
        .await
        .context("domain registry")?;
    drop(ready);
    // the graph knows no methodology (ADR 0168): the guardian service answers for the methodologies of the changes
    let guardian: Arc<dyn graph_core::guardian::Guardian> = Arc::new(graph_service::guardian::Client::new(
        &std::env::var("GOAP_GUARDIAN_URL").unwrap_or_else(|_| "http://localhost:8097".into()),
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
    // the domains are the registry's: kept as read, read again when it publishes or deletes one, or announces it
    // serves again (a registry restarted); with no bus, every minute
    if let Some(client) = &bus {
        let mut moved = futures::stream::select_all([
            client.subscribe(DOMAIN_PUBLISHED).await.context("subscribe")?,
            client.subscribe(DOMAIN_DELETED).await.context("subscribe")?,
            client.subscribe(READY_DOMAINS).await.context("subscribe")?,
        ]);
        let registry = registry.clone();
        tokio::spawn(async move {
            while futures::StreamExt::next(&mut moved).await.is_some() {
                if let Err(e) = registry.reload().await {
                    tracing::error!(err = %e.message, "type catalogue");
                }
            }
        });
    } else {
        let registry = registry.clone();
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(Duration::from_secs(60));
            tick.tick().await;
            loop {
                tick.tick().await;
                if let Err(e) = registry.reload().await {
                    tracing::error!(err = %e.message, "type catalogue");
                }
            }
        });
    }
    if content {
        state.remote = Some(Arc::new(graph_service::platform::Remote::new(&platform_url)));
        let state = Arc::new(state);
        follow_moves(&bus, &state).await?;
        tokio::spawn(Arc::new(Graph { state: state.clone() }).run_worker());
        for r in relays {
            tokio::spawn(r.run(state.clone()));
        }
        announce(&bus, READY_GRAPH_CONTENT).await;
        axum::serve(listener, content_app(state))
            .with_graceful_shutdown(shutdown())
            .await?;
        return Ok(());
    }
    let state = Arc::new(state);
    follow_moves(&bus, &state).await?;
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
    announce(&bus, READY_GRAPH).await;
    axum::serve(listener, app(state))
        .with_graceful_shutdown(shutdown())
        .await?;
    Ok(())
}

/// Follows the moves of the other processes (Go's `Directory.FollowEvents` and `Graph.HeadEvent`): a commit on main of
/// a namespace, the organisation's among them, makes what the store keeps of its snapshot checked again at its next
/// read; a commit on a lasting branch moves the head the store keeps of it to the header its event carries, with the
/// versions it moved.
async fn follow_moves(bus: &Option<async_nats::Client>, state: &Arc<State>) -> anyhow::Result<()> {
    let Some(client) = bus else {
        return Ok(());
    };
    let mut sub = client
        .subscribe("goap.baseline.*.advanced")
        .await
        .context("subscribe")?;
    state.store.follow_events();
    let state = state.clone();
    tokio::spawn(async move {
        #[derive(serde::Deserialize)]
        #[serde(rename_all = "camelCase")]
        struct Moved {
            #[serde(default)]
            id: String,
            #[serde(default)]
            namespace: String,
            #[serde(default)]
            branch: String,
            #[serde(default)]
            parent: String,
            #[serde(default)]
            set: std::collections::HashMap<String, i32>,
            #[serde(default)]
            removed: Vec<String>,
            #[serde(default)]
            time: Option<chrono::DateTime<chrono::Utc>>,
            #[serde(default)]
            whole: bool,
            #[serde(default)]
            name: String,
            #[serde(default)]
            change_id: String,
            #[serde(default)]
            kind: String,
            #[serde(default)]
            merged_from: String,
            #[serde(default)]
            rebased_from: String,
            #[serde(default)]
            seq: i64,
        }
        while let Some(msg) = futures::StreamExt::next(&mut sub).await {
            let main = msg.subject.as_str() == "goap.baseline.main.advanced";
            let Ok(m) = serde_json::from_slice::<Moved>(&msg.payload) else {
                if main {
                    // unreadable: every head is checked again rather than miss a change
                    state.store.moved_all();
                }
                continue;
            };
            if main {
                if m.namespace.is_empty() {
                    state.store.moved_all();
                } else {
                    state.store.moved(&m.namespace);
                }
            }
            let mut moves: std::collections::HashMap<graph_core::model::NodeId, i32> =
                m.set.into_iter().map(|(id, v)| (id.into(), v)).collect();
            for id in m.removed {
                moves.insert(id.into(), 0);
            }
            let head = graph_core::model::Baseline {
                id: m.id.into(),
                name: m.name,
                parent_id: m.parent.into(),
                merged_from: m.merged_from.into(),
                change_id: m.change_id.into(),
                created_at: m.time,
                branch: m.branch,
                namespace: m.namespace,
                kind: m.kind,
                rebased_from: m.rebased_from.into(),
                seq: m.seq,
            };
            state.store.head_event(&head, &moves, m.whole);
        }
    });
    Ok(())
}

/// The registry announces it serves (Go's `platform.ReadyDomains`, ADR 0169).
const READY_DOMAINS: &str = "service.ready.domains";
/// The registry published or deleted a domain version (ADR 0167).
const DOMAIN_PUBLISHED: &str = "goap.registry.domain.published";
const DOMAIN_DELETED: &str = "goap.registry.domain.deleted";
/// This graph announces it serves (Go's `platform.ReadyGraph` / `ReadyGraphContent`).
const READY_GRAPH: &str = "service.ready.graph.platform";
const READY_GRAPH_CONTENT: &str = "service.ready.graph.content";

/// Publishes `subject` on core NATS once the graph listens (Go's `Server.Announce`): a service waiting for this one
/// tries again at once.
async fn announce(bus: &Option<async_nats::Client>, subject: &'static str) {
    let Some(c) = bus else { return };
    let payload = r#"{"service":"graph"}"#;
    if let Err(e) = c.publish(subject, payload.into()).await {
        tracing::warn!(subject, err = %e, "announce");
    }
    let _ = c.flush().await;
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
