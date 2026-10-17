//! The HTTP server of the process (Go's server.go): sign-in, the Connect handlers of every service, the event stream,
//! the status and the built IDE.

use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use axum::Json;
use axum::Router;
use axum::routing::get;
use chrono::Utc;
use goap_connect::ConnectError;
use goap_ctx::Ctx;
use goap_gateway::{self as gateway, Authenticator, Credentials};
use goap_gen::assistant::v1::assistant_service_router;
use goap_gen::conversations::v1::conversation_service_router;
use goap_gen::engine::v1::engine_service_router;
use goap_gen::events::v1::event_service_router;
use goap_gen::graph::v1::graph_service_router;
use goap_gen::index::v1::index_service_router;
use goap_gen::mcp::v1::mcp_service_router;
use goap_gen::model::v1::model_service_router;
use goap_gen::preferences::v1::preferences_service_router;
use goap_gen::registry::v1::{domain_service_router, registry_service_router};
use goap_gen::runtime::v1::runtime_service_router;
use goap_graphsvc as graphsvc;
use goap_identity::Extractor;
use goap_methodology::Compiled;
use goap_platform::{Server, env, env_duration};
use serde_json::{Value, json};

use crate::app::{BoxError, ChangePublisher, Env, wrap};
use crate::engine::EnginePart;
use crate::graph::GraphPart;
use crate::local::{Stores, local_jwt_secret, serve_web};
use crate::platform::PlatformPart;
use crate::registry::{HubValues, RegistryPart};

/// build_server creates the HTTP server: sign-in (the auth middleware of the gateway's mode), the Connect handlers of
/// every service of the process, the event stream, the status and the built IDE. Nothing listens until it runs.
pub(crate) async fn build_server(
    e: &Env,
    st: &Stores,
    gp: &GraphPart,
    rp: &RegistryPart,
    pp: &PlatformPart,
    ep: &EnginePart,
) -> Result<Server, BoxError> {
    let mut srv = Server::new(&e.cfg.addr);
    let ident = Extractor {
        default: Some(e.dev.clone()),
    };
    let authz: Arc<dyn goap_authz::Authorizer> = gp.authorizer.clone();

    // Sign-in (ADR 0040, 0042): local by default (GOAP_AUTH_MODE unset) — this single process is its own identity
    // provider (register/login/logout, no external IdP), the same way cmd/gateway's auth mode does for the
    // distributed platform, reusing its authenticator and auth endpoints. GOAP_AUTH_MODE=none turns it off for
    // development: every request then acts as the fixed GOAP_DEV_SUBJECT/ORG/ROLES principal. Applied per router (not
    // over the whole server), so the built IDE's static assets and the health endpoints stay reachable with no token.
    let auth = build_auth(e, st, gp, &mut srv).await?;
    let mut mount = |r: Router| {
        let r = match &auth {
            Some(a) => a.wrap(r),
            None => r,
        };
        srv.mount(r);
    };

    let mut graph_handler = graphsvc::Handler::new(gp.g.clone());
    graph_handler.events = Some(Arc::new(GraphHandlerEvents {
        triggers: ChangePublisher(e.triggers.clone()),
        index: pp.index_sink.clone(),
        bus: HubValues(rp.bus.clone()),
    }));
    graph_handler.authz = Some(authz.clone());
    graph_handler.floor = Some(Arc::new(gp.authorizer.floor()));
    graph_handler.identity = ident.clone();
    graph_handler.demo = Some(goap_devseed::demo_loader(Some(gp.directory.clone())));
    {
        // the OKF export reads the type catalogue of the graph (Go asks the graph's Types)
        let types = rp.types.clone();
        graph_handler.types = Some(Arc::new(move || Some(types.get())));
    }
    let graph_handler = Arc::new(graph_handler);
    mount(graph_service_router(
        graph_handler.clone(),
        goap_telemetry::handler_options()
            .with(graph_handler.identify())
            .with(goap_identity::command_interceptor())
            .with(graph_handler.personal_scope())
            .with(graph_handler.ensure_caller()),
    ));
    let mut registry_handler = goap_registrysvc::Handler::new(rp.reg.clone());
    registry_handler.identity = ident.clone();
    mount(registry_service_router(
        Arc::new(registry_handler),
        goap_telemetry::handler_options().with(goap_identity::command_interceptor()),
    ));
    let mut domain_handler = goap_domainsvc::Handler::new(rp.domains.clone());
    domain_handler.identity = ident.clone();
    mount(domain_service_router(
        Arc::new(domain_handler),
        goap_telemetry::handler_options().with(goap_identity::command_interceptor()),
    ));
    let whoami = {
        let a = gp.authorizer.clone();
        goap_identity::session_fn(move |ctx, p| {
            let a = a.clone();
            async move {
                let s = a
                    .session(&ctx, &p)
                    .await
                    .map_err(|e| Box::new(e) as BoxError)?;
                serde_json::to_value(s).map_err(|e| Box::new(e) as BoxError)
            }
        })
    };
    let whoami_route = match &auth {
        Some(a) => a.route_layer(goap_identity::who_am_i(Extractor::default(), Some(whoami))),
        None => goap_identity::who_am_i(ident.clone(), Some(whoami)),
    };
    let mut mcp_handler = goap_mcpsvc::Handler::new(pp.hub.clone());
    mcp_handler.authz = Some(authz.clone());
    mcp_handler.identity = ident.clone();
    mcp_handler.connector_token = pp.conn_token.clone();
    mount(mcp_service_router(
        Arc::new(mcp_handler),
        goap_telemetry::handler_options(),
    ));
    let mut model_handler = goap_modelgw::Handler::new(pp.gw.clone());
    model_handler.identity = ident.clone();
    model_handler.authz = Some(authz.clone());
    mount(model_service_router(
        Arc::new(model_handler),
        goap_telemetry::handler_options(),
    ));
    let mut prefs_handler =
        goap_prefssvc::Handler::new(Arc::new(goap_prefssvc::Service::new(st.prefs.clone())));
    prefs_handler.identity = ident.clone();
    mount(preferences_service_router(
        Arc::new(prefs_handler),
        goap_telemetry::handler_options(),
    ));
    let convs = Arc::new(goap_convsvc::Service::new(st.convs.clone()));
    let mut convs_handler = goap_convsvc::Handler::new(convs.clone());
    convs_handler.identity = ident.clone();
    mount(conversation_service_router(
        Arc::new(convs_handler),
        goap_telemetry::handler_options(),
    ));
    // the assistant (ADR 0087) in process: the real graph (a change it creates starts its triggers like any other),
    // the gateway, the registry, the access directory and the conversation service, all acting for the caller
    let mut assistant = goap_assistantsvc::Service::new(
        convs,
        pp.gw.clone(),
        Arc::new(goap_assistantsvc::PortGraph(Arc::new(
            goap_engine::EventingGraph::new(gp.g.clone(), Some(e.triggers.on_event())),
        ))),
        Arc::new(AssistantMethodologies(rp.reg.clone())),
        Arc::new(goap_assistantsvc::Directory(gp.directory.clone())),
    );
    assistant.engine = Some(Arc::new(goap_assistantsvc::EngineClient {
        api: ep.handler.clone(),
        views: Some(Arc::new(goap_registrysvc::Handler::new(rp.reg.clone()))),
    }));
    mount(assistant_service_router(
        Arc::new(goap_assistantsvc::Handler {
            service: Arc::new(assistant),
            identity: ident.clone(),
        }),
        goap_telemetry::handler_options(),
    ));
    let mut index_handler = goap_indexersvc::Handler::new(pp.indexer.clone());
    index_handler.identity = ident.clone();
    index_handler.authz = Some(authz);
    mount(index_service_router(
        Arc::new(index_handler),
        goap_telemetry::handler_options(),
    ));
    mount(engine_service_router(
        ep.handler.clone(),
        goap_telemetry::handler_options(),
    ));
    let mut events_handler = goap_eventsvc::Handler::new(rp.bus.clone());
    events_handler.identity = ident;
    mount(event_service_router(
        Arc::new(events_handler),
        goap_telemetry::handler_options(),
    ));
    if let Some(runtime) = ep.runtime.clone() {
        mount(runtime_service_router(
            runtime,
            goap_telemetry::handler_options(),
        ));
    }
    srv.route("/api/whoami", whoami_route);
    // its own tokens, minted by the hub (ADR 0123 §6), not the sign-in's
    srv.mount(pp.models.clone().router());
    // single process: the platform is up when this answers (the gateway serves it otherwise)
    srv.route("/api/status", get(|| async { Json(status()) }));
    if let Some(web) = serve_web(&e.cfg.web_dir) {
        srv.mount(web);
    }
    Ok(srv)
}

/// The status of the platform: this process.
fn status() -> Value {
    json!({
        "status": "ok",
        "time": goap_domain::gotime::format(&Utc::now()),
        "services": [{"name": "goap-dev", "status": "up", "latencyMs": 0}],
    })
}

/// build_auth mounts the sign-in endpoints and returns the authentication middleware of the mode (None when the mode
/// is open: every request then acts as the dev principal).
async fn build_auth(
    e: &Env,
    st: &Stores,
    gp: &GraphPart,
    srv: &mut Server,
) -> Result<Option<Authenticator>, BoxError> {
    let mode = gateway::lookup_mode(&e.cfg.auth_mode).map_err(|err| wrap("auth mode", err))?;
    if mode.open() {
        return Ok(None);
    }
    // the secret is read from Vault or the environment; locally, it is generated once next to the database
    let mut secret = e
        .secrets
        .get(&e.ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
        .await
        .map_err(|err| wrap("jwt secret", err))?;
    if secret.is_empty() && mode.sessions() {
        secret = local_jwt_secret(&st.dir).map_err(|err| wrap("jwt secret", err))?;
    }
    let mut cfg = gateway::Config {
        auth_mode: e.cfg.auth_mode.clone(),
        jwt_secret: secret.into_bytes(),
        dev_tokens: env("GOAP_DEV_TOKENS", "") == "true",
        token_ttl: env_duration("GOAP_TOKEN_TTL", gateway::DEFAULT_TOKEN_TTL),
        max_session: env_duration("GOAP_SESSION_MAX", gateway::DEFAULT_MAX_SESSION),
        credentials: Some(Arc::new(LocalCredentials(goap_credsvc::Service::new(
            st.creds.clone(),
        )))),
        ..Default::default()
    };
    {
        let d = gp.directory.clone();
        cfg.enrich = Some(gateway::enrich_fn(move |ctx, p| {
            let d = d.clone();
            async move { d.enrich(&ctx, &p).await }
        }));
    }
    {
        let d = gp.directory.clone();
        cfg.project_access = Some(gateway::project_access_fn(move |ctx, p, project| {
            let d = d.clone();
            async move {
                d.may_access_project(&ctx, &p, &project)
                    .await
                    .map_err(|e| e.to_string().into())
            }
        }));
    }
    // the user exists in the graph from their first sign-in, member of the unit new users join (ADR 0042)
    {
        let (g, d) = (gp.g.clone(), gp.directory.clone());
        cfg.on_sign_in = Some(gateway::on_sign_in_fn(move |ctx, subject| {
            let (g, d) = (g.clone(), d.clone());
            async move {
                Box::pin(graphsvc::ensure_user(&ctx, &g, &subject))
                    .await
                    .map_err(|e| Box::new(e) as BoxError)?;
                d.refresh(&ctx).await.map_err(|e| e.to_string().into())
            }
        }));
    }
    // one state of the sessions for the endpoints and the authenticator: a sign-out is refused at once (ADR 0045)
    let cfg = gateway::prepare(cfg);
    let endpoints = gateway::mount_auth_endpoints(Router::new(), cfg.clone())
        .map_err(|err| wrap("auth", err))?;
    srv.mount(endpoints);
    let a = gateway::authenticator(cfg).map_err(|err| wrap("auth", err))?;
    tracing::info!(mode = %e.cfg.auth_mode, "local sign-in enabled");
    Ok(Some(a))
}

/// The publishers of the graph handler's events (calls from the IDE): the triggers, the node index and the event
/// stream of the web (Go's `engine.Publishers{changePublisher(...), indexSink, bus}`).
struct GraphHandlerEvents {
    triggers: ChangePublisher,
    index: goap_indexersvc::Sink,
    bus: HubValues,
}

#[async_trait]
impl graphsvc::Publisher for GraphHandlerEvents {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Value) -> Result<(), BoxError> {
        let first = self.triggers.publish(ctx, subject, v).await.err();
        let index = self
            .index
            .publish(ctx, subject, v)
            .await
            .err()
            .map(|e| Box::new(e) as BoxError);
        let bus = graphsvc::Publisher::publish(&self.bus, ctx, subject, v)
            .await
            .err();
        match first.or(index).or(bus) {
            Some(e) => Err(e),
            None => Ok(()),
        }
    }
}

/// The methodology registry in process as the assistant reads it (Go's `*registrysvc.Service` satisfies
/// `assistantsvc.Methodologies`).
struct AssistantMethodologies(Arc<goap_registrysvc::Service>);

#[async_trait]
impl goap_assistantsvc::Methodologies for AssistantMethodologies {
    async fn methodology(&self, ctx: &Ctx, name: &str) -> Result<Arc<Compiled>, BoxError> {
        self.0
            .methodology(ctx, name)
            .await
            .map_err(|e| Box::new(e) as BoxError)
    }
}

/// LocalCredentials is the credentials service in process as the gateway's sign-in needs it (Go hands
/// `*credsvc.Service` itself to the gateway). A subject registered twice is refused with an error carrying the
/// `already_exists` code, the register answering 409 as through the credentials service's client.
struct LocalCredentials(goap_credsvc::Service);

/// Exists is credsvc's refusal of a second registration, keeping its text, with the Connect code the gateway reads.
#[derive(Debug)]
struct Exists(ConnectError);

impl std::fmt::Display for Exists {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&goap_credsvc::Error::Exists.to_string())
    }
}

impl std::error::Error for Exists {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        Some(&self.0)
    }
}

fn boxed(e: goap_credsvc::Error) -> BoxError {
    if e.is_exists() {
        return Box::new(Exists(ConnectError::already_exists(e.to_string())));
    }
    Box::new(e)
}

#[async_trait]
impl Credentials for LocalCredentials {
    async fn register(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<(), BoxError> {
        self.0.register(ctx, subject, password).await.map_err(boxed)
    }
    async fn verify(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<bool, BoxError> {
        self.0.verify(ctx, subject, password).await.map_err(boxed)
    }
    async fn start_session(
        &self,
        ctx: &Ctx,
        subject: &str,
        max_age: Duration,
    ) -> Result<String, BoxError> {
        self.0
            .start_session(ctx, subject, max_age)
            .await
            .map_err(boxed)
    }
    async fn session_active(&self, ctx: &Ctx, id: &str, subject: &str) -> Result<bool, BoxError> {
        self.0.session_active(ctx, id, subject).await.map_err(boxed)
    }
    async fn end_session(&self, ctx: &Ctx, id: &str) -> Result<(), BoxError> {
        self.0.end_session(ctx, id).await.map_err(boxed)
    }
    async fn end_sessions(&self, ctx: &Ctx, subject: &str) -> Result<(), BoxError> {
        self.0.end_sessions(ctx, subject).await.map_err(boxed)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use axum::body::Body;
    use axum::http::Request;
    use goap_connect::Code;
    use tower::ServiceExt;

    fn local() -> LocalCredentials {
        LocalCredentials(goap_credsvc::Service::new(Arc::new(
            goap_credsvc::MemoryStore::new(),
        )))
    }

    /// A subject registered twice keeps credsvc's text and carries the code that makes the gateway answer 409.
    #[tokio::test]
    async fn a_second_registration_is_already_exists() {
        let (ctx, c) = (Ctx::background(), local());
        c.register(&ctx, "alice", "correct horse").await.unwrap();
        assert!(c.verify(&ctx, "alice", "correct horse").await.unwrap());
        let e = c
            .register(&ctx, "alice", "correct horse")
            .await
            .expect_err("registered twice");
        assert_eq!(e.to_string(), "credential already exists");
        assert_eq!(
            goap_connect::code_of(&*e as &(dyn std::error::Error + 'static)),
            Code::AlreadyExists
        );
        // another refusal is no conflict
        let e = c.register(&ctx, "bob", "short").await.expect_err("short");
        assert_ne!(
            goap_connect::code_of(&*e as &(dyn std::error::Error + 'static)),
            Code::AlreadyExists
        );
    }

    /// The sign-in endpoints over the credentials in process: a register, then a second one refused with 409.
    #[tokio::test]
    async fn register_twice_is_a_conflict() {
        let cfg = gateway::Config {
            auth_mode: "local".into(),
            jwt_secret: b"a-test-secret-of-at-least-32-bytes!".to_vec(),
            credentials: Some(Arc::new(local())),
            ..Default::default()
        };
        let app = gateway::mount_auth_endpoints(Router::new(), gateway::prepare(cfg)).unwrap();
        let register = || {
            Request::post("/auth/register")
                .header("content-type", "application/json")
                .body(Body::from(
                    r#"{"subject":"alice","password":"correct horse"}"#,
                ))
                .unwrap()
        };
        let first = app.clone().oneshot(register()).await.unwrap();
        assert_eq!(first.status(), 200);
        let second = app.oneshot(register()).await.unwrap();
        assert_eq!(second.status(), 409);
    }

    #[test]
    fn the_status_names_this_process() {
        let s = status();
        assert_eq!(s["status"], "ok");
        assert_eq!(s["services"][0]["name"], "goap-dev");
        assert_eq!(s["services"][0]["latencyMs"], 0);
        assert!(s["time"].as_str().is_some_and(|t| t.ends_with('Z')));
    }
}
