//! The smoke test of the composition (Go's app_test.go): the whole platform built in one process on a temporary SQLite
//! database, with no sign-in, driven through its router (no port to pick, nothing exits the process).

use std::future::Future;
use std::path::PathBuf;
use std::time::{Duration, Instant};

use axum::body::Body;
use axum::http::{Request, StatusCode};
use goap_ctx::Ctx;
use http_body_util::BodyExt;
use serde_json::Value;
use tower::ServiceExt;

use crate::app::{App, new_app};
use crate::{Config, load_config, run};

/// Runs a test future on a runtime whose threads have the stack of a main thread (the bootstrap is deep).
fn block_on<F: Future<Output = ()> + Send + 'static>(f: F) {
    std::thread::Builder::new()
        .stack_size(16 << 20)
        .spawn(move || {
            tokio::runtime::Builder::new_multi_thread()
                .worker_threads(4)
                .thread_stack_size(8 << 20)
                .enable_all()
                .build()
                .unwrap()
                .block_on(f)
        })
        .unwrap()
        .join()
        .unwrap();
}

/// A temporary directory of its own.
fn temp_dir(name: &str) -> PathBuf {
    let d = std::env::temp_dir().join(format!(
        "goap-dev-{name}-{}-{}",
        std::process::id(),
        uuid_like()
    ));
    std::fs::create_dir_all(&d).unwrap();
    d
}

fn uuid_like() -> String {
    let mut b = [0u8; 8];
    getrandom::fill(&mut b).unwrap();
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// The configuration of a test: SQLite in dir, no sign-in, the domains and methodologies of the repository, no IDE
/// (Go sets the environment: GOAP_STORE, GOAP_SQLITE_PATH, GOAP_AUTH_MODE, GOAP_SANDBOX=inproc, the default here).
fn test_config(dir: &std::path::Path) -> Config {
    let repo = PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../..");
    let mut cfg = load_config();
    cfg.store = "sqlite".into();
    cfg.sqlite_path = dir.join("goap.db").display().to_string();
    cfg.auth_mode = "none".into();
    cfg.connector_token = "smoke-token".into();
    cfg.domains_dir = repo.join("domains").display().to_string();
    cfg.methodologies_dir = repo.join("methodologies").display().to_string();
    cfg.web_dir = dir.join("no-web").display().to_string();
    cfg
}

async fn smoke_app(dir: &std::path::Path) -> App {
    Box::pin(new_app(&Ctx::background(), test_config(dir)))
        .await
        .unwrap_or_else(|e| panic!("new_app: {e}"))
}

async fn call(app: &axum::Router, req: Request<Body>) -> (StatusCode, Vec<u8>) {
    let resp = app.clone().oneshot(req).await.unwrap();
    let code = resp.status();
    let body = resp
        .into_body()
        .collect()
        .await
        .unwrap()
        .to_bytes()
        .to_vec();
    (code, body)
}

async fn get_json(app: &axum::Router, uri: &str) -> (StatusCode, Value) {
    let (code, body) = call(app, Request::get(uri).body(Body::empty()).unwrap()).await;
    (code, serde_json::from_slice(&body).unwrap_or(Value::Null))
}

/// The composition starts, answers its health, status, identity and graph calls, and stops.
#[test]
fn smoke() {
    block_on(async {
        let dir = temp_dir("smoke");
        let mut a = smoke_app(&dir).await;
        let app = a.server.as_ref().unwrap().app();

        let (code, body) = call(&app, Request::get("/readyz").body(Body::empty()).unwrap()).await;
        assert!(
            code == StatusCode::OK && body == b"ready",
            "/readyz = {code} {:?}",
            String::from_utf8_lossy(&body)
        );

        let (code, status) = get_json(&app, "/api/status").await;
        let services = status["services"].as_array().cloned().unwrap_or_default();
        assert!(
            code == StatusCode::OK && status["status"] == "ok" && services.len() == 1,
            "/api/status = {code} {status}"
        );
        assert!(
            services[0]["name"] == "goap-dev" && services[0]["status"] == "up",
            "/api/status services = {services:?}"
        );

        let (code, who) = get_json(&app, "/api/whoami").await;
        assert!(
            code == StatusCode::OK && who["subject"] == "dev",
            "/api/whoami = {code} {who}"
        );

        // a graph call through the Connect handler: the bootstrap and the domains left their namespaces
        let (code, body) = call(
            &app,
            Request::post("/goap.graph.v1.GraphService/ListNamespaces")
                .header("content-type", "application/json")
                .body(Body::from("{}"))
                .unwrap(),
        )
        .await;
        let ns: Value = serde_json::from_slice(&body).unwrap_or(Value::Null);
        assert_eq!(
            code,
            StatusCode::OK,
            "ListNamespaces: {}",
            String::from_utf8_lossy(&body)
        );
        let found: Vec<&str> = ns["namespaces"]
            .as_array()
            .map(|l| l.iter().filter_map(Value::as_str).collect())
            .unwrap_or_default();
        assert!(
            found.contains(&"organisation") && found.contains(&"platform"),
            "namespaces = {found:?}"
        );
        a.close().await;
        let _ = std::fs::remove_dir_all(dir);
    });
}

/// close stops the background loops: a second app on the same process starts and stops cleanly, and a context
/// ended by its parent ends the server.
#[test]
fn run_stops_with_its_context() {
    block_on(async {
        let dir = temp_dir("run");
        let mut cfg = test_config(&dir);
        let l = match std::net::TcpListener::bind("127.0.0.1:0") {
            Ok(l) => l,
            Err(e) => {
                eprintln!("no local port to listen on: {e}");
                return;
            }
        };
        cfg.addr = l.local_addr().unwrap().to_string();
        drop(l);
        let addr = cfg.addr.clone();
        let (ctx, cancel) = Ctx::background().with_cancel();
        let done = tokio::spawn(async move { Box::pin(run(&ctx, cfg)).await });
        let http = reqwest::Client::new();
        // Go waits 60 s; the seeds of the methodologies take most of a minute in a debug build
        let deadline = Instant::now() + Duration::from_secs(240);
        let mut ready = false;
        while Instant::now() < deadline && !ready {
            if let Ok(resp) = http.get(format!("http://{addr}/readyz")).send().await {
                ready = resp.status() == 200;
            }
            if !ready {
                tokio::time::sleep(Duration::from_millis(50)).await;
            }
        }
        assert!(ready, "run never became ready");
        cancel.cancel();
        match tokio::time::timeout(Duration::from_secs(20), done).await {
            Ok(res) => {
                if let Err(e) = res.unwrap() {
                    panic!("run: {e}");
                }
            }
            Err(_) => panic!("run did not stop with its context"),
        }
        let _ = std::fs::remove_dir_all(dir);
    });
}
