//! The platform in one process (Go's app.go): the builders strung in the order the composition needs.

use std::sync::{Arc, OnceLock};

use goap_authz::Principal;
use goap_ctx::{CancelHandle, Ctx};
use goap_domain::ChangeEvent;
use goap_engine::{TriggerEvent, TriggerManager, trigger_event_of};
use goap_platform::{Secrets, Server};
use serde_json::Value;

use crate::Config;
use crate::engine::build_engine;
use crate::graph::{Late, build_graph};
use crate::local::{Stores, open_stores};
use crate::platform::build_platform;
use crate::registry::build_registry;
use crate::server::build_server;

/// Any error of the composition.
pub(crate) type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// DefaultAuthMode is the sign-in mode when GOAP_AUTH_MODE is unset (goap_gateway::DEFAULT_AUTH_MODE).
pub(crate) const DEFAULT_AUTH_MODE: &str = "local";

/// App is the platform in one process: its HTTP server and what to release when it stops.
pub(crate) struct App {
    /// Taken by run (it serves until the context ends).
    pub server: Option<Server>,
    /// Ends with close: the background loops of every part follow it.
    pub ctx: Ctx,
    cancel: CancelHandle,
    st: Stores,
}

/// Env is what every builder shares: the context of the background loops, the configuration, the secrets.
pub(crate) struct Env {
    pub ctx: Ctx,
    pub cfg: Config,
    pub secrets: Arc<Secrets>,
    pub dev: Principal,
    /// The late binding of the trigger manager, which needs the engine, while the change and registry hooks it
    /// serves are wired before.
    pub triggers: Arc<TriggerRef>,
    /// The hooks of the graph whose implementation comes after the graph (the registry, the event sinks, the type
    /// catalogue): Go sets the fields of the graph afterwards, the graph here is shared once built.
    pub late: Arc<Late>,
}

/// new_app wires the whole platform in the order the composition needs (goap-graphsvc boot doc, docs/architecture.md
/// "Startup sequence"): stores, graph, registry (domains, type catalogue, Boot, methodologies), model gateway and node
/// index, MCP hub, engine and triggers, then the HTTP server. Nothing listens until the server runs.
pub(crate) async fn new_app(parent: &Ctx, cfg: Config) -> Result<App, BoxError> {
    // the facts of the risk register are items of a change (ADR 0065)
    goap_risk::register();
    goap_assess::register();
    goap_verify::register();
    let (ctx, cancel) = parent.with_cancel();
    let st = match open_stores(&ctx, &cfg).await {
        Ok(st) => st,
        Err(e) => {
            cancel.cancel();
            return Err(wrap("store", e));
        }
    };
    // devlocal (ADR 0010): no Vault running, so a raw key an admin asks to be vaulted falls back to a local file next
    // to the SQLite database (Secrets.dir), the same convenience as the generated JWT/connector secrets
    // (local_secret); ignored once a real Vault is configured (VAULT_ADDR). Go sets it in buildPlatform, the secrets
    // being shared here once built.
    let mut secrets = Secrets::new();
    secrets.dir = st.dir.clone();
    let e = Env {
        ctx: ctx.clone(),
        dev: cfg.dev.clone(),
        cfg,
        secrets: Arc::new(secrets),
        triggers: Arc::new(TriggerRef::default()),
        late: Arc::new(Late::default()),
    };
    let built = Box::pin(build(&e, &st)).await;
    match built {
        Ok(srv) => Ok(App {
            server: Some(srv),
            ctx,
            cancel,
            st,
        }),
        Err(err) => {
            cancel.cancel();
            st.close().await;
            Err(err)
        }
    }
}

async fn build(e: &Env, st: &Stores) -> Result<Server, BoxError> {
    let gp = build_graph(e, st).map_err(|err| wrap("authorizer", err))?;
    let rp = Box::pin(build_registry(e, &gp, st)).await?;
    let pp = Box::pin(build_platform(e, st, &gp, &rp)).await?;
    let ep = Box::pin(build_engine(e, st, &gp, &rp, &pp)).await?;
    build_server(e, st, &gp, &rp, &pp, &ep).await
}

impl App {
    /// close stops the background loops and closes the stores.
    pub async fn close(&mut self) {
        self.cancel.cancel();
        self.st.close().await;
    }
}

/// Wrapped is an error of a part of the composition: "<what>: <error>".
#[derive(Debug)]
pub(crate) struct Wrapped {
    what: String,
    err: BoxError,
}

impl std::fmt::Display for Wrapped {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(f, "{}: {}", self.what, self.err)
    }
}

impl std::error::Error for Wrapped {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(&*self.err)
    }
}

pub(crate) fn wrap(what: &str, err: impl Into<BoxError>) -> BoxError {
    Box::new(Wrapped {
        what: what.to_string(),
        err: err.into(),
    })
}

/// TriggerRef holds the trigger manager once the engine exists; the hooks that fire before it ignore events.
#[derive(Default)]
pub(crate) struct TriggerRef {
    m: OnceLock<Arc<TriggerManager>>,
}

impl TriggerRef {
    pub fn set(&self, m: Arc<TriggerManager>) {
        let _ = self.m.set(m);
    }

    pub async fn handle(&self, ctx: &Ctx, ev: TriggerEvent) {
        if let Some(m) = self.m.get() {
            m.handle(ctx, ev).await;
        }
    }

    /// on_change forwards a change event of the graph to the triggers. The hooks calling it are synchronous (the
    /// engine's EventingGraph): the event is handled in the background, in the order of the calls of each hook.
    pub fn on_change(self: &Arc<Self>, ctx: &Ctx, ev: ChangeEvent) {
        if self.m.get().is_none() {
            return;
        }
        let (t, ctx) = (self.clone(), ctx.clone());
        tokio::spawn(async move { t.handle(&ctx, trigger_event_of(ev)).await });
    }

    /// The hook of the engine's EventingGraph.
    pub fn on_event(self: &Arc<Self>) -> goap_engine::OnEvent {
        let t = self.clone();
        Arc::new(move |ctx: &Ctx, ev: ChangeEvent| t.on_change(ctx, ev))
    }
}

/// ChangePublisher forwards the change events of the graph handler (calls from the IDE) to the triggers (Go's
/// changePublisher, which keeps the values that are a domain.ChangeEvent).
pub(crate) struct ChangePublisher(pub Arc<TriggerRef>);

#[async_trait::async_trait]
impl goap_graphsvc::Publisher for ChangePublisher {
    async fn publish(&self, ctx: &Ctx, _subject: &str, v: &Value) -> Result<(), BoxError> {
        if let Some(ev) = change_event(v) {
            self.0.handle(ctx, trigger_event_of(ev)).await;
        }
        Ok(())
    }
}

/// The change event a published value is, None when it is none (a flow, a node, a document of the index).
pub(crate) fn change_event(v: &Value) -> Option<ChangeEvent> {
    let Value::Object(m) = v else { return None };
    if !matches!(m.get("type"), Some(Value::String(t)) if !t.is_empty())
        || !m.contains_key("change")
    {
        return None;
    }
    serde_json::from_value::<ChangeEvent>(v.clone()).ok()
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    /// Only the change events of the graph handler reach the triggers (Go keeps the values that are a
    /// domain.ChangeEvent): a flow, a node or a document of the index is left out.
    #[test]
    fn change_events_are_told_apart() {
        let ev = change_event(&json!({"type": "change.created", "change": {"id": "c1"}})).unwrap();
        assert_eq!(ev.r#type, "change.created");
        assert_eq!(ev.change.id.as_str(), "c1");
        assert!(change_event(&json!({"id": "f1", "parent": "main"})).is_none());
        assert!(change_event(&json!({"id": "c1", "deleted": true})).is_none());
        assert!(change_event(&json!({"type": "", "change": {"id": "c1"}})).is_none());
        assert!(change_event(&json!("x")).is_none());
    }

    #[test]
    fn wrapped_errors_name_their_part() {
        let e = wrap("boot", "dev seed: refused");
        assert_eq!(e.to_string(), "boot: dev seed: refused");
        assert!(std::error::Error::source(&*e).is_some());
    }
}
