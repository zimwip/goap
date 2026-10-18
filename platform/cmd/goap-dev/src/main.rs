//! Command goap-dev runs graph, registry, model gateway and engine in a single process, for local development without
//! containers. Storage is in-memory (GOAP_STORE=memory, default) or a local SQLite file (GOAP_STORE=sqlite,
//! GOAP_SQLITE_PATH) that keeps the graph, methodologies, access policies (graph nodes) and processes across restarts.
//! When GOAP_WEB_DIR (default web/dist) holds a built IDE, it is served too. By default (ADR 0040, 0042) users sign in
//! (register/login/logout, goap-credsvc), the same auth code cmd/gateway uses; with GOAP_AUTH_MODE=none, callers act
//! as the principal GOAP_DEV_SUBJECT / GOAP_DEV_ROLES unless the request carries X-Goap-* identity headers.
//!
//! The wiring is one builder per concern (ADR 0073), in the order the composition needs: stores (local.rs), graph
//! (graph.rs), registry and the platform bootstrap (registry.rs), model gateway, node index and MCP hub
//! (platform.rs), engine and triggers (engine.rs), HTTP server (server.rs). new_app (app.rs) strings them; the smoke
//! test (app_test.rs) builds it without a process.

mod app;
mod engine;
mod graph;
mod local;
mod platform;
mod registry;
mod server;

#[cfg(test)]
mod app_test;

use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_platform::{env, fatal, logger};

use crate::app::{BoxError, DEFAULT_AUTH_MODE, new_app};

/// Config is what the environment says about this composition; the other variables are read where they apply
/// (sandboxes in goap_sandbox::from_env, token lifetimes in build_auth).
#[derive(Debug, Clone, Default)]
pub(crate) struct Config {
    /// GOAP_HTTP_ADDR
    pub addr: String,
    /// GOAP_AUTH_MODE
    pub auth_mode: String,
    /// GOAP_WEB_DIR
    pub web_dir: String,
    /// GOAP_DOMAINS_DIR
    pub domains_dir: String,
    /// GOAP_METHODOLOGIES_DIR
    pub methodologies_dir: String,
    /// GOAP_MODELS_CONFIG
    pub models_config: String,
    /// GOAP_DEV_FS_ROOT
    pub dev_fs_root: String,
    /// GOAP_DEV_DEMO=1: load the demo data at start (else on demand, from the settings)
    pub demo: bool,
    /// GOAP_DEV_WORKSPACE_ROOT
    pub workspace_root: String,
    /// GOAP_DEV_WORKSPACE_IMAGE: the commands of the workspaces in containers of this image
    pub workspace_image: String,
    /// GOAP_DOCKER_HOST
    pub docker_host: String,
    /// GOAP_CLAUDE_AGENT_DIR: the runner of the claude-coding-agent connector (ADR 0123)
    pub claude_agent_dir: String,
    /// GOAP_CONNECTOR_TOKEN (generated when empty)
    pub connector_token: String,
    /// GOAP_MODEL_PROXY_SECRET: the tokens of the gateway's Anthropic endpoint (ADR 0123 §6; generated when empty)
    pub model_proxy_secret: String,
    /// GOAP_MODEL_PROXY_URL: that endpoint as the connectors reach it (default this process, /anthropic)
    pub model_proxy_url: String,
    /// GOAP_STORE: memory (default) or sqlite. Go reads it (and the next one) in openStores; here it is configuration,
    /// so that a test can choose its storage without changing the environment of the process.
    pub store: String,
    /// GOAP_SQLITE_PATH (default .goap/goap.db)
    pub sqlite_path: String,
    pub dev: Principal,
}

pub(crate) fn load_config() -> Config {
    Config {
        addr: env("GOAP_HTTP_ADDR", ":8080"),
        auth_mode: env("GOAP_AUTH_MODE", DEFAULT_AUTH_MODE),
        web_dir: env("GOAP_WEB_DIR", "web/dist"),
        domains_dir: env("GOAP_DOMAINS_DIR", "domains"),
        methodologies_dir: env("GOAP_METHODOLOGIES_DIR", "methodologies"),
        models_config: env("GOAP_MODELS_CONFIG", ""),
        dev_fs_root: env("GOAP_DEV_FS_ROOT", ""),
        demo: env("GOAP_DEV_DEMO", "") == "1",
        workspace_root: env("GOAP_DEV_WORKSPACE_ROOT", ""),
        workspace_image: env("GOAP_DEV_WORKSPACE_IMAGE", ""),
        docker_host: env("GOAP_DOCKER_HOST", "unix:///var/run/docker.sock"),
        claude_agent_dir: env("GOAP_CLAUDE_AGENT_DIR", ".goap/claude-agent"),
        connector_token: env("GOAP_CONNECTOR_TOKEN", ""),
        model_proxy_secret: env("GOAP_MODEL_PROXY_SECRET", ""),
        model_proxy_url: env("GOAP_MODEL_PROXY_URL", ""),
        store: env("GOAP_STORE", "memory"),
        sqlite_path: env("GOAP_SQLITE_PATH", ".goap/goap.db"),
        dev: Principal {
            subject: env("GOAP_DEV_SUBJECT", "dev"),
            org: env("GOAP_DEV_ORG", "dev"),
            project: env("GOAP_DEV_PROJECT", ""),
            roles: env("GOAP_DEV_ROLES", "admin")
                .split(',')
                .map(str::to_string)
                .collect(),
            ..Default::default()
        },
    }
}

fn main() {
    // the futures of the bootstrap and of the graph operations are deep: the threads get the stack of a main thread
    let rt = match tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .thread_stack_size(8 << 20)
        .build()
    {
        Ok(rt) => rt,
        Err(e) => fatal("goap-dev", e),
    };
    rt.block_on(async {
        let ctx = Ctx::background();
        logger("goap-dev");
        let tel = goap_telemetry::setup(&ctx, "goap-dev");
        let res = Box::pin(run(&ctx, load_config())).await;
        let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
        if let Err(e) = res {
            fatal("goap-dev", e);
        }
    });
}

/// run builds the platform and serves it until ctx ends or the process is signalled.
pub(crate) async fn run(ctx: &Ctx, cfg: Config) -> Result<(), BoxError> {
    let mut a = Box::pin(new_app(ctx, cfg)).await?;
    let srv = a.server.take().expect("the server of a new app");
    let res = srv.run_ctx(&a.ctx).await;
    a.close().await;
    Ok(res?)
}
