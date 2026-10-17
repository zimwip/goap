//! Command engine runs agent processes.

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_access::{Authorizer, Directory};
use goap_ctx::Ctx;
use goap_engine::{
    AuthzScope, Broker, Engine, Executor, HumanExecutor, LLMExecutor, LeaderFn, LogLine,
    MethodologyPort, Payload, Process, ProcessEvent, Publisher, ScriptExecutor, StepEvent,
    ToolExecutor, ToolPort, TriggerEvent, TriggerManager, default_builtins, trigger_event_of,
};
use goap_gen::engine::v1::engine_service_router;
use goap_gen::runtime::v1::runtime_service_router;
use goap_intent::{LLMRanker, Lexical, Ranker, Resolver};
use goap_llm as llm;
use goap_mcp::ToolInfo;
use goap_methodology::{KIND_BUILTIN, KIND_HUMAN, KIND_LLM, KIND_SCRIPT, KIND_TOOL};
use goap_platform::{
    Events, Server, env, env_bool, env_duration, env_int, fatal, h2c_client, logger, migrate,
    optional_events, optional_postgres,
};
use serde::Deserialize;
use serde_json::{Map, Value};

/// The subjects of the domain registry after which the type catalogue is read again (ADR 0125 §3).
const DOMAIN_SUBJECTS: [&str; 2] = [
    "goap.registry.domain.published",
    "goap.registry.domain.deleted",
];

/// The subject of a methodology published, an event of the triggers.
const METHODOLOGY_PUBLISHED_SUBJECT: &str = "goap.registry.methodology.published";

/// The lease of the leadership of the schedule (cron) triggers among the replicas.
const LEADER_TTL: Duration = Duration::from_secs(30);

/// Bus publishes the process events on NATS (Go's `*platform.Events` as the engine's publisher).
struct Bus(Events);

#[async_trait]
impl Publisher for Bus {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Payload<'_>) -> goap_engine::Result<()> {
        self.0
            .publish(ctx, subject, &v.json())
            .await
            .map_err(|e| goap_engine::Error::Msg(e.to_string()))
    }
}

/// Hub is the MCP hub over RPC as the engine's tool port (Go's `*mcpsvc.Client`, which satisfies `engine.ToolPort`).
struct Hub(goap_mcpsvc::Client);

#[async_trait]
impl ToolPort for Hub {
    async fn call_tool(
        &self,
        ctx: &Ctx,
        org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> goap_engine::Result<Value> {
        self.0
            .call_tool(ctx, org, name, &args)
            .await
            .map(Value::Object)
            .map_err(|e| goap_engine::Error::Msg(e.to_string()))
    }

    async fn tools(
        &self,
        ctx: &Ctx,
        org: &str,
    ) -> goap_engine::Result<(Vec<ToolInfo>, Vec<String>)> {
        self.0
            .tools(ctx, org)
            .await
            .map_err(|e| goap_engine::Error::Msg(e.to_string()))
    }
}

/// The JSON of a process event read from NATS (`goap_engine::ProcessEvent` is written only).
#[derive(Deserialize)]
struct WireProcessEvent {
    #[serde(default)]
    event: String,
    #[serde(default)]
    process: Option<Process>,
    #[serde(default)]
    step: Option<StepEvent>,
    #[serde(default)]
    log: Option<LogLine>,
    #[serde(default, with = "goap_domain::gotime::option")]
    time: Option<DateTime<Utc>>,
}

/// The process event of a NATS message, None when it is not one.
fn process_event(data: &[u8]) -> Option<ProcessEvent> {
    let w: WireProcessEvent = serde_json::from_slice(data).ok()?;
    Some(ProcessEvent {
        event: w.event,
        process: w.process,
        step: w.step,
        log: w.log,
        time: w.time.unwrap_or_default(),
    })
}

/// The methodology a `goap.registry.methodology.published` event names (Go reads `struct{ Name, Version string }`,
/// its field names matched without case).
#[derive(Deserialize, Default)]
#[serde(default)]
struct Published {
    #[serde(alias = "Name")]
    name: String,
    #[serde(alias = "Version")]
    version: String,
}

/// The trigger event of a message of the trigger consumer, None when it is none.
fn trigger_event(subject: &str, data: &[u8]) -> Option<TriggerEvent> {
    if subject == METHODOLOGY_PUBLISHED_SUBJECT {
        let m: Published = serde_json::from_slice(data).unwrap_or_default();
        return Some(TriggerEvent {
            r#type: goap_events::METHODOLOGY_PUBLISHED.to_string(),
            methodology: m.name,
            version: m.version,
            ..Default::default()
        });
    }
    match serde_json::from_slice::<goap_domain::ChangeEvent>(data) {
        Ok(ev) if !ev.r#type.is_empty() => Some(trigger_event_of(ev)),
        _ => None,
    }
}

/// The name of the host (Go's `os.Hostname`).
fn hostname() -> String {
    std::fs::read_to_string("/proc/sys/kernel/hostname")
        .ok()
        .or_else(|| std::fs::read_to_string("/etc/hostname").ok())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .or_else(|| std::env::var("HOSTNAME").ok())
        .or_else(|| std::env::var("COMPUTERNAME").ok())
        .unwrap_or_default()
}

/// The next delay of a retry: doubled, at most a minute.
fn next_delay(d: Duration) -> Duration {
    (2 * d).min(Duration::from_secs(60))
}

/// A Connect client of the platform at `url`: h2c and `interceptors`.
fn platform_client(url: &str, interceptors: &goap_connect::Interceptors) -> goap_connect::Client {
    goap_connect::Client::builder(url)
        .http(h2c_client())
        .interceptors(interceptors)
        .build()
}

/// leader elects, over a NATS lease, the replica that fires the schedule (cron) triggers: several replicas may run
/// this binary, only one may fire them at a time (event triggers are instead deduplicated by binding every replica to
/// the same durable consumer name). None when the lease cannot be opened: every replica is then a leader.
async fn leader(ctx: &Ctx, events: &Events) -> Option<LeaderFn> {
    let holder = format!("{}-{}", hostname(), std::process::id());
    let lease = match events.new_lease(ctx, "goap-leases").await {
        Ok(l) => l,
        Err(e) => {
            tracing::warn!(err = %e, "trigger leadership lease");
            return None;
        }
    };
    let held = Arc::new(AtomicBool::new(false));
    let renew = {
        let (held, lease, holder) = (held.clone(), lease.clone(), holder.clone());
        move |ctx: Ctx| {
            let (held, lease, holder) = (held.clone(), lease.clone(), holder.clone());
            async move {
                match lease
                    .acquire(&ctx, "trigger-manager", &holder, LEADER_TTL)
                    .await
                {
                    Ok(h) => held.store(h, Ordering::SeqCst),
                    Err(e) => tracing::warn!(err = %e, "trigger leadership"),
                }
            }
        }
    };
    renew(ctx.clone()).await;
    let ctx = ctx.clone();
    tokio::spawn(async move {
        let mut tick = tokio::time::interval(LEADER_TTL / 3);
        tick.tick().await;
        loop {
            tokio::select! {
                _ = ctx.done() => return,
                _ = tick.tick() => renew(ctx.clone()).await,
            }
        }
    });
    Some(Arc::new(move || held.load(Ordering::SeqCst)))
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    // the facts of the risk register are items of a change (ADR 0065)
    goap_risk::register();
    goap_assess::register();
    goap_verify::register();
    logger("engine");
    let tel = goap_telemetry::setup(&ctx, "engine");
    let copts = goap_telemetry::client_options();
    let models: Arc<dyn llm::Client> = Arc::new(goap_enginetel::LLMClient {
        next: Arc::new(goap_modelgw::Client::new(platform_client(
            &env("GOAP_MODELGW_URL", "http://localhost:8084"),
            &copts.with(goap_identity::forward()),
        ))),
    });
    let events = optional_events(&ctx).await;

    // live events: fed by NATS when available (several replicas), locally otherwise
    let broker = Arc::new(Broker::new());
    let mut publisher: Arc<dyn Publisher> = broker.clone();
    if let Some(bus) = &events {
        publisher = Arc::new(Bus(bus.clone()));
        let b = broker.clone();
        let res = bus
            .subscribe("goap.process.>", move |data| {
                if let Some(ev) = process_event(&data) {
                    b.deliver(ev);
                }
            })
            .await;
        if let Err(e) = res {
            fatal("subscribe", e);
        }
    }

    let sandboxes = match goap_sandbox::from_env(h2c_client(), copts.clone()) {
        Ok(s) => s,
        Err(e) => fatal("sandbox", e),
    };
    if let Some(pool) = &sandboxes.pool {
        let pool = pool.clone();
        tokio::spawn(async move {
            let mut tick = tokio::time::interval(Duration::from_secs(60));
            tick.tick().await;
            loop {
                tick.tick().await;
                pool.reap_idle();
            }
        });
    }

    let ranker: Arc<dyn Ranker> = if env("GOAP_INTENT_RANKER", "lexical") == "llm" {
        Arc::new(LLMRanker {
            client: models.clone(),
            model: env("GOAP_INTENT_MODEL", "fast"),
        })
    } else {
        Arc::new(Lexical)
    };
    let graph_url = env("GOAP_GRAPH_URL", "http://localhost:8081");
    let directory = Arc::new(Directory::new(Arc::new(
        goap_graphclient::Client::from_connect(platform_client(&graph_url, &copts)),
    )));
    let authorizer = match Authorizer::new(directory) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    let registry = Arc::new(goap_registrysvc::Client::new(platform_client(
        &env("GOAP_REGISTRY_URL", "http://localhost:8082"),
        &copts,
    )));
    // the type catalogue (ADR 0012): the ancestors behind x.types, from the domain registry the graph serves (ADR 0125 §3)
    let domains = Arc::new(goap_domainclient::Client::new(platform_client(
        &graph_url, &copts,
    )));
    let types = Arc::new(goap_typecat::Live::new(domains.clone()));
    {
        let (types, ctx) = (types.clone(), ctx.clone());
        tokio::spawn(async move {
            let mut delay = Duration::from_secs(1);
            while types.reload(&ctx).await.is_err() {
                tokio::time::sleep(delay).await;
                delay = next_delay(delay);
            }
        });
    }
    // processes: in memory (one replica), or in PostgreSQL (GOAP_DB_DSN), shared by the replicas, which serialize
    // their work on a process through its locks and resume the runs a stopped replica left (ADR 0124)
    let mut store: Arc<dyn goap_engine::Store> = Arc::new(goap_engine::MemoryStore::new());
    let mut locks: Option<Arc<dyn goap_engine::Locker>> = None;
    let pg = optional_postgres(&ctx, None).await;
    if let Some(pool) = &pg {
        if let Err(e) = migrate(&ctx, pool, &goap_enginesvc::MIGRATIONS, ".").await {
            fatal("migrate", e);
        }
        let shared = Arc::new(goap_enginesvc::SQLStore::postgres(
            pool.clone(),
            goap_enginesvc::new_holder(),
        ));
        store = shared.clone();
        locks = Some(shared);
    }
    let mut builtins = default_builtins();
    let mut e = Engine::new(
        Arc::new(goap_graphclient::Client::from_connect(platform_client(
            &graph_url, &copts,
        ))),
        registry.clone() as Arc<dyn MethodologyPort>,
        store,
    );
    let executors: [(&str, Arc<dyn Executor>); 4] = [
        (
            KIND_LLM,
            Arc::new(LLMExecutor {
                client: Some(models.clone()),
            }),
        ),
        (
            KIND_SCRIPT,
            Arc::new(ScriptExecutor {
                sandboxes: Some(sandboxes.sandboxes.clone()),
            }),
        ),
        (KIND_HUMAN, Arc::new(HumanExecutor)),
        (KIND_TOOL, Arc::new(ToolExecutor)),
    ];
    e.executors = executors
        .into_iter()
        .map(|(k, x)| (k.to_string(), x))
        .collect::<HashMap<_, _>>();
    e.intent = Some(Resolver::new(ranker));
    e.locks = locks.clone();
    e.lock_ttl = env_duration("GOAP_ENGINE_LOCK_TTL", goap_engine::DEFAULT_LOCK_TTL);
    e.max_runs = env_int("GOAP_ENGINE_MAX_RUNS", 0);
    e.events = Some(publisher);
    e.scope = Some(Arc::new(AuthzScope {
        authz: Some(authorizer.clone()),
        hub: Some(Arc::new(Hub(goap_mcpsvc::Client::new(platform_client(
            &env("GOAP_MCP_URL", "http://localhost:8085"),
            &copts,
        ))))),
    }));
    e.llm = Some(models);
    e.sandboxes = Some(sandboxes.sandboxes.clone());
    e.tracer = Some(Arc::new(goap_enginetel::EngineTracer::new()));
    e.max_steps = env_int("GOAP_MAX_STEPS", 50);
    e.max_parallel = env_int("GOAP_MAX_PARALLEL", 4);
    // the oversight floor (EU AI Act Art. 14)
    e.person_reviews_model_output = env_bool("GOAP_PERSON_REVIEWS_MODEL_OUTPUT", true);
    {
        let types = types.clone();
        e.types = Some(Arc::new(move || {
            Some(types.get() as Arc<dyn goap_def::TypeSet>)
        }));
    }
    // wake-ups and interruptions reach the other replicas at once (ADR 0124)
    let signals = match (&locks, &events) {
        (Some(_), Some(bus)) => Some(goap_enginesvc::NATSSignals { bus: bus.clone() }),
        _ => None,
    };
    if let Some(s) = &signals {
        e.signals = Some(Arc::new(s.clone()));
    }
    // self-observation (methodology-improvement): journal, traces, drafts
    goap_selfimprove::register(
        &mut builtins,
        &e,
        goap_enginetel::self_improvement_from_env(registry.clone()),
    );
    e.executors
        .insert(KIND_BUILTIN.to_string(), Arc::new(builtins));
    let e = Arc::new(e);
    if locks.is_some() {
        if let Some(s) = &signals
            && let Err(err) = s.listen(e.clone()).await
        {
            fatal("subscribe", err);
        }
        let (e, ctx) = (e.clone(), ctx.clone());
        tokio::spawn(async move { e.recover_loop(&ctx, Duration::ZERO).await });
    }
    if let Some(bus) = &events {
        for subject in DOMAIN_SUBJECTS {
            let (types, domains) = (types.clone(), domains.clone());
            let res = bus
                .subscribe(subject, move |_data| {
                    domains.invalidate();
                    let types = types.clone();
                    tokio::spawn(async move {
                        if let Err(e) = types.reload(&Ctx::background()).await {
                            tracing::error!(err = %e, "type catalogue");
                        }
                    });
                })
                .await;
            if let Err(e) = res {
                fatal("subscribe", e);
            }
        }
    }
    // triggers: agents run automatically on events and schedules
    let mut triggers = None;
    if env("GOAP_TRIGGERS", "on") == "on" {
        let lead = match &events {
            Some(bus) => leader(&ctx, bus).await,
            None => None,
        };
        let t = TriggerManager::with(e.clone(), Duration::ZERO, lead);
        if let Some(bus) = &events {
            let (bus, t, ctx) = (bus.clone(), t.clone(), ctx.clone());
            tokio::spawn(async move {
                let handle = |ctx: Ctx, subject: String, data: bytes::Bytes| {
                    let t = t.clone();
                    async move {
                        if let Some(ev) = trigger_event(&subject, &data) {
                            t.handle(&ctx, ev).await;
                        }
                        Ok::<(), std::convert::Infallible>(())
                    }
                };
                if let Err(e) = bus
                    .consume_durable(
                        &ctx,
                        "trigger-manager",
                        &["goap.change.>", METHODOLOGY_PUBLISHED_SUBJECT],
                        handle,
                    )
                    .await
                {
                    tracing::error!(err = %e, "trigger events consumer");
                }
            });
        }
        t.start(&ctx).await;
        {
            let (t, ctx, broker) = (t.clone(), ctx.clone(), broker.clone());
            tokio::spawn(async move { t.watch_processes(&ctx, &broker).await });
        }
        triggers = Some(t);
    }
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    if let Some(bus) = &events {
        let bus = bus.clone();
        srv.readiness(move |ctx| {
            let bus = bus.clone();
            async move { bus.ready(&ctx).await }
        });
    }
    let mut handler = goap_enginesvc::Handler::new(e.clone());
    handler.authz = Some(authorizer);
    handler.broker = Some(broker);
    handler.triggers = triggers;
    srv.mount(engine_service_router(
        Arc::new(handler),
        goap_telemetry::handler_options(),
    ));
    if let Some(runtime) = sandboxes.runtime.clone() {
        // sandboxes call back the engine here (job token authentication)
        srv.mount(runtime_service_router(
            runtime,
            goap_telemetry::handler_options(),
        ));
    }
    let res = srv.run().await;
    if let Some(pool) = &sandboxes.pool {
        pool.close();
    }
    if let Some(bus) = events {
        bus.close().await;
    }
    if let Some(pool) = pg {
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
    fn a_process_event_is_read_back() {
        let ev = ProcessEvent {
            event: "step".into(),
            process: Some(Process {
                id: "p1".into(),
                ..Default::default()
            }),
            step: None,
            log: None,
            time: Utc::now(),
        };
        let data = serde_json::to_vec(&ev).unwrap();
        let back = process_event(&data).unwrap();
        assert_eq!(back.event, "step");
        assert_eq!(back.process.unwrap().id, "p1");
        assert_eq!(back.time, ev.time);
        assert!(process_event(b"not json").is_none());
    }

    #[test]
    fn trigger_events_of_the_consumer() {
        let ev = trigger_event(
            METHODOLOGY_PUBLISHED_SUBJECT,
            br#"{"name":"sdlc","version":"1.0.0"}"#,
        )
        .unwrap();
        assert_eq!(ev.r#type, goap_events::METHODOLOGY_PUBLISHED);
        assert_eq!(
            (ev.methodology.as_str(), ev.version.as_str()),
            ("sdlc", "1.0.0")
        );
        let ev = trigger_event(METHODOLOGY_PUBLISHED_SUBJECT, br#"{"Name":"x"}"#).unwrap();
        assert_eq!(ev.methodology, "x");
        let ev = trigger_event(
            "goap.change.c1",
            br#"{"type":"change.created","change":{"id":"c1"}}"#,
        )
        .unwrap();
        assert_eq!(ev.r#type, "change.created");
        assert_eq!(ev.change.unwrap().id.as_str(), "c1");
        assert!(trigger_event("goap.change.c1", br#"{"change":{"id":"c1"}}"#).is_none());
        assert!(trigger_event("goap.change.c1", b"nope").is_none());
    }
}
