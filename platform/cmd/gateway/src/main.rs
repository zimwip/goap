//! Command gateway is the single entry point of the platform.
mod credentials;

use std::sync::Arc;

use goap_access::{Authorizer, Directory};
use goap_ctx::Ctx;
use goap_gateway::{
    BoxError, Config, DEFAULT_AUTH_MODE, DEFAULT_MAX_SESSION, DEFAULT_TOKEN_TTL, Route, enrich_fn,
    lookup_mode, mount, on_sign_in_fn, project_access_fn,
};
use goap_gen::credentials::v1::CredentialsServiceClient;
use goap_platform::{Secrets, Server, env, env_duration, fatal, h2c_client, logger};

use crate::credentials::CredentialsClient;

fn route(prefix: &str, var: &str, def: &str) -> Route {
    Route {
        prefix: prefix.to_string(),
        upstream: env(var, def),
    }
}

/// `errors.Join(err, errors.New("not set"))` of a secret read: the secret, else the error to report.
fn required(r: goap_platform::Result<String>) -> Result<String, String> {
    match r {
        Ok(v) if !v.is_empty() => Ok(v),
        Ok(_) => Err("not set".to_string()),
        Err(e) => Err(format!("{e}\nnot set")),
    }
}

#[tokio::main]
async fn main() {
    let ctx = Ctx::background();
    logger("gateway");
    let tel = goap_telemetry::setup(&ctx, "gateway");
    let secrets = Secrets::new();
    let graph_url = env("GOAP_GRAPH_URL", "http://localhost:8081");
    let mut cfg = Config {
        auth_mode: env("GOAP_AUTH_MODE", DEFAULT_AUTH_MODE),
        dev_tokens: env("GOAP_DEV_TOKENS", "") == "true",
        // a token lives GOAP_TOKEN_TTL and is refreshed by the web up to GOAP_SESSION_MAX after the sign-in
        token_ttl: env_duration("GOAP_TOKEN_TTL", DEFAULT_TOKEN_TTL),
        max_session: env_duration("GOAP_SESSION_MAX", DEFAULT_MAX_SESSION),
        routes: vec![
            route(
                "/goap.graph.v1.GraphService/",
                "GOAP_GRAPH_URL",
                "http://localhost:8081",
            ),
            route(
                "/goap.registry.v1.RegistryService/",
                "GOAP_REGISTRY_URL",
                "http://localhost:8082",
            ),
            // the domain registry is the graph's (ADR 0125 §3)
            route(
                "/goap.registry.v1.DomainService/",
                "GOAP_GRAPH_URL",
                "http://localhost:8081",
            ),
            route(
                "/goap.engine.v1.EngineService/",
                "GOAP_ENGINE_URL",
                "http://localhost:8083",
            ),
            route(
                "/goap.model.v1.ModelService/",
                "GOAP_MODELGW_URL",
                "http://localhost:8084",
            ),
            route(
                "/goap.preferences.v1.PreferencesService/",
                "GOAP_PREFERENCES_URL",
                "http://localhost:8087",
            ),
            route(
                "/goap.conversations.v1.ConversationService/",
                "GOAP_CONVERSATIONS_URL",
                "http://localhost:8090",
            ),
            route(
                "/goap.assistant.v1.AssistantService/",
                "GOAP_CONVERSATIONS_URL",
                "http://localhost:8090",
            ),
            route(
                "/goap.index.v1.IndexService/",
                "GOAP_INDEXER_URL",
                "http://localhost:8086",
            ),
            route(
                "/goap.events.v1.EventService/",
                "GOAP_EVENTS_URL",
                "http://localhost:8089",
            ),
            route(
                "/goap.mcp.v1.McpService/",
                "GOAP_MCP_URL",
                "http://localhost:8085",
            ),
        ],
        ..Default::default()
    };
    // who a caller is comes from the User nodes of the graph as well as from its token
    let graph_client = Arc::new(goap_graphclient::Client::from_connect(
        goap_graphclient::Client::builder(&graph_url)
            .http(h2c_client())
            .interceptors(&goap_telemetry::client_options())
            .build(),
    ));
    let directory = Arc::new(Directory::new(graph_client.clone()));
    {
        let d = directory.clone();
        cfg.enrich = Some(enrich_fn(move |ctx, p| {
            let d = d.clone();
            async move { d.enrich(&ctx, &p).await }
        }));
    }
    let authorizer = match Authorizer::new(directory.clone()) {
        Ok(a) => Arc::new(a),
        Err(e) => fatal("authorizer", e),
    };
    cfg.session = Some(goap_identity::session_fn(move |ctx, p| {
        let a = authorizer.clone();
        async move {
            let s = a
                .session(&ctx, &p)
                .await
                .map_err(|e| Box::new(e) as BoxError)?;
            serde_json::to_value(s).map_err(|e| Box::new(e) as BoxError)
        }
    }));
    {
        let d = directory.clone();
        cfg.project_access = Some(project_access_fn(move |ctx, p, project| {
            let d = d.clone();
            async move {
                d.may_access_project(&ctx, &p, &project)
                    .await
                    .map_err(|e| e.to_string().into())
            }
        }));
    }
    let origins = env("GOAP_CORS_ORIGINS", "");
    if !origins.is_empty() {
        cfg.allow_origins = origins.split(',').map(str::to_string).collect();
    }
    let mode = match lookup_mode(&cfg.auth_mode) {
        Ok(m) => m,
        Err(e) => fatal("auth mode", e),
    };
    if mode.verifies_tokens() {
        match secrets
            .get(&ctx, "goap/gateway#jwt_secret", "GOAP_JWT_SECRET")
            .await
        {
            Ok(secret) => cfg.jwt_secret = secret.into_bytes(),
            Err(e) => fatal("jwt secret", e),
        }
    }
    if mode.sessions() {
        // the credentials service answers the gateway only, which presents its shared service credential
        let cred_token = match required(
            secrets
                .get(
                    &ctx,
                    "goap/credentials#service_token",
                    "GOAP_CREDENTIALS_TOKEN",
                )
                .await,
        ) {
            Ok(t) => t,
            Err(e) => fatal("credentials service token (GOAP_CREDENTIALS_TOKEN)", e),
        };
        let rpc = CredentialsServiceClient::new(
            goap_connect::Client::builder(&env("GOAP_CREDENTIALS_URL", "http://localhost:8088"))
                .http(h2c_client())
                .interceptors(&goap_telemetry::client_options())
                .interceptor(goap_credsvc::client_token(&cred_token))
                .build(),
        );
        cfg.credentials = Some(Arc::new(CredentialsClient { rpc }));
        // the user exists in the graph from their first sign-in (ADR 0042), and their first calls see it
        let (g, d) = (graph_client.clone(), directory.clone());
        cfg.on_sign_in = Some(on_sign_in_fn(move |ctx, subject| {
            let (g, d) = (g.clone(), d.clone());
            async move {
                g.declare_user(&ctx, &subject)
                    .await
                    .map_err(|e| Box::new(e) as BoxError)?;
                d.refresh(&ctx).await.map_err(|e| e.to_string().into())
            }
        }));
    }
    let auth_mode = cfg.auth_mode.clone();
    let mut srv = Server::new(&env("GOAP_HTTP_ADDR", ":8080"));
    match mount(goap_connect::axum::Router::new(), cfg) {
        Ok(r) => {
            srv.mount(r);
        }
        Err(e) => fatal("gateway", e),
    }
    tracing::info!(mode = %auth_mode, "auth");
    let res = srv.run().await;
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(e) = res {
        fatal("server", e);
    }
}
