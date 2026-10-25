//! goap-proxy: the Connect proxy in front of the Go and the Rust graph services (ADR 0163).
//!
//! `goap-proxy` serves, with `GOAP_PROXY_MODE`:
//! - `go` / `rust`: every call to one service, passed as is;
//! - `compare`: every call to the Go service, which answers, and to the Rust one, each on its own database, compared
//!   call by call (`calls.jsonl` in `GOAP_PROXY_REPORT_DIR`, `/proxy/report`);
//! - `shadow`: every call to the Go service, which answers, a call that only reads to the Rust one too, both on one
//!   database, compared the same way (no id to pair: the Rust service reads what the Go one wrote).
//!
//! `goap-proxy diff-db --go <dsn> --rust <dsn>` compares the tables of the two databases of a comparison.
use std::{io::Read as _, sync::Arc};

use anyhow::Context;
use axum::{
    Json, Router,
    body::Body,
    extract::{Request, State},
    http::{HeaderMap, StatusCode, header},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use bytes::Bytes;
use http_body_util::BodyExt;
use serde_json::{Value, json};
use tokio::sync::Mutex;

mod compare;
mod config;
mod diffdb;
mod ids;
mod rpc;
mod stats;
mod upstream;

use compare::Comparer;
use config::{Config, GRAPH_SERVICES, Mode};
use rpc::Procedure;
use stats::{ServedBy, Stats};
use upstream::{Reply, Side, Upstream};

struct Proxy {
    cfg: Config,
    go: Option<Upstream>,
    rust: Option<Upstream>,
    stats: std::sync::Mutex<Stats>,
    comparer: Option<Mutex<Comparer>>,
}

#[tokio::main]
async fn main() -> anyhow::Result<()> {
    tracing_subscriber::fmt()
        .with_env_filter(
            tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info,tokio_postgres=warn".into()),
        )
        .init();
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.first().map(String::as_str) == Some("diff-db") {
        return diffdb::run(&args[1..]).await;
    }
    if let Some(a) = args.first() {
        anyhow::bail!("unknown command {a:?} (none: serve; diff-db: compare two databases)");
    }
    let cfg = Config::from_env()?;
    let comparer = match cfg.mode {
        Mode::Compare | Mode::Shadow => Some(Mutex::new(Comparer::new(
            cfg.report_dir.clone(),
            &cfg.ignore,
            cfg.mode == Mode::Shadow,
        )?)),
        _ => None,
    };
    let proxy = Arc::new(Proxy {
        go: cfg.go.clone().map(|u| Upstream::new(Side::Go, u)),
        rust: cfg.rust.clone().map(|u| Upstream::new(Side::Rust, u)),
        stats: Default::default(),
        comparer,
        cfg,
    });
    let app = Router::new()
        .route("/proxy/healthz", get(|| async { "ok" }))
        .route("/proxy/coverage", get(coverage))
        .route("/proxy/report", get(report))
        .route("/proxy/probe", post(probe))
        .route("/metrics", get(metrics))
        .fallback(forward)
        .with_state(proxy.clone());
    if matches!(proxy.cfg.mode, Mode::Compare | Mode::Shadow) {
        // both services answer before the first call: a call one of them is not ready for would be a divergence
        for up in [proxy.go.as_ref(), proxy.rust.as_ref()].into_iter().flatten() {
            wait_ready(up).await?;
        }
    }
    let listener = tokio::net::TcpListener::bind(&proxy.cfg.addr)
        .await
        .with_context(|| format!("listening on {}", proxy.cfg.addr))?;
    tracing::info!(addr = %proxy.cfg.addr, mode = proxy.cfg.mode.name(), go = ?proxy.cfg.go, rust = ?proxy.cfg.rust, "goap-proxy serving");
    axum::serve(listener, app).with_graceful_shutdown(shutdown()).await?;
    if let Some(c) = &proxy.comparer {
        let c = c.lock().await;
        match c.save_ids() {
            Ok(p) => tracing::info!(path = %p.display(), pairs = c.ids.len(), "ids saved"),
            Err(e) => tracing::warn!(error = %e, "saving the ids"),
        }
    }
    Ok(())
}

/// Waits for a service's `/readyz` (`GOAP_PROXY_WAIT`, default 3 minutes).
async fn wait_ready(up: &Upstream) -> anyhow::Result<()> {
    let wait = std::env::var("GOAP_PROXY_WAIT")
        .ok()
        .and_then(|v| v.parse::<u64>().ok())
        .unwrap_or(180);
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(wait);
    loop {
        match up
            .send(&http::Method::GET, "/readyz", &HeaderMap::new(), Bytes::new())
            .await
        {
            Ok(r) if r.status.is_success() => return Ok(()),
            res if std::time::Instant::now() > deadline => {
                anyhow::bail!(
                    "the {} graph service is not ready after {wait}s: {:?}",
                    up.side.name(),
                    res.map(|r| r.status)
                )
            }
            _ => {
                tracing::info!(side = up.side.name(), "waiting for the graph service");
                tokio::time::sleep(std::time::Duration::from_secs(1)).await;
            }
        }
    }
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

fn reply(r: Reply) -> Response {
    let mut res = Response::new(Body::from(r.body));
    *res.status_mut() = r.status;
    *res.headers_mut() = r.headers;
    res
}

fn error(status: StatusCode, code: &str, message: String) -> Response {
    (
        status,
        [(header::CONTENT_TYPE, "application/json")],
        json!({"code": code, "message": message}).to_string(),
    )
        .into_response()
}

fn unimplemented(r: &Reply) -> bool {
    rpc::error_of(r.status, &r.body).is_some_and(|e| e.code == "unimplemented")
}

/// The body of a request, its gzip undone (a comparison reads it).
fn gunzip(headers: &mut HeaderMap, body: Bytes) -> anyhow::Result<Bytes> {
    let gz = headers.get(header::CONTENT_ENCODING).and_then(|v| v.to_str().ok()) == Some("gzip");
    if !gz {
        return Ok(body);
    }
    let mut out = Vec::new();
    flate2::read::GzDecoder::new(&body[..]).read_to_end(&mut out)?;
    headers.remove(header::CONTENT_ENCODING);
    Ok(Bytes::from(out))
}

async fn forward(State(p): State<Arc<Proxy>>, req: Request) -> Response {
    let (parts, body) = req.into_parts();
    let path = parts
        .uri
        .path_and_query()
        .map(|x| x.as_str().to_string())
        .unwrap_or_else(|| "/".into());
    let body = match http_body_util::Limited::new(body, p.cfg.max_body).collect().await {
        Ok(b) => b.to_bytes(),
        Err(e) => {
            return error(
                StatusCode::PAYLOAD_TOO_LARGE,
                "resource_exhausted",
                format!("goap-proxy: request body: {e}"),
            );
        }
    };
    let proc = Procedure::of(parts.uri.path());
    let key = proc.as_ref().map(ToString::to_string).unwrap_or_else(|| "other".into());
    let mut headers = parts.headers;
    match p.cfg.mode {
        Mode::Go | Mode::Rust => {
            let (up, by) = if p.cfg.mode == Mode::Go {
                (p.go.as_ref(), ServedBy::Go)
            } else {
                (p.rust.as_ref(), ServedBy::Rust)
            };
            let up = up.expect("checked by the configuration");
            let r = up.call(&parts.method, &path, &headers, body).await;
            p.count(&key, by, up.side, &r);
            reply(r)
        }
        Mode::Compare | Mode::Shadow => {
            let go = p.go.as_ref().expect("checked");
            let proc = proc.filter(|pr| p.cfg.mode == Mode::Compare || pr.reads_only());
            let Some(proc) = proc else {
                // a path of no RPC (health, readiness), or a write in mode shadow: the Go service alone
                let r = go.call(&parts.method, &path, &headers, body).await;
                p.count(&key, ServedBy::Go, Side::Go, &r);
                return reply(r);
            };
            // both answers are read, never compressed; a compressed request is read once
            headers.remove(header::ACCEPT_ENCODING);
            headers.remove("connect-accept-encoding");
            let body = match gunzip(&mut headers, body) {
                Ok(b) => b,
                Err(e) => {
                    return error(
                        StatusCode::BAD_REQUEST,
                        "invalid_argument",
                        format!("goap-proxy: gzip request: {e}"),
                    );
                }
            };
            let rust = p.rust.as_ref().expect("checked");
            let comparer = p.comparer.as_ref().expect("compare mode");
            let (r, rec) = compare::call(comparer, go, rust, &proc, &parts.method, &path, &headers, body).await;
            {
                let mut s = p.stats.lock().unwrap();
                s.served(&key, ServedBy::Go);
                s.time(&key, "go", std::time::Duration::from_secs_f64(rec.go_ms / 1e3));
                s.time(&key, "rust", std::time::Duration::from_secs_f64(rec.rust_ms / 1e3));
                s.verdict(&key, rec.verdict);
            }
            if rec.verdict.diverges() {
                tracing::warn!(procedure = %rec.procedure, verdict = rec.verdict.name(), seq = rec.seq, diffs = rec.diffs.len(), "divergence");
            }
            reply(r)
        }
    }
}

impl Proxy {
    fn count(&self, key: &str, by: ServedBy, side: Side, r: &Reply) {
        let mut s = self.stats.lock().unwrap();
        s.served(key, by);
        s.time(key, side.name(), r.elapsed);
    }
}

async fn metrics(State(p): State<Arc<Proxy>>) -> Response {
    let text = p.stats.lock().unwrap().metrics(p.cfg.mode.name());
    ([(header::CONTENT_TYPE, "text/plain; version=0.0.4")], text).into_response()
}

/// The methods of the graph's services.
fn graph_methods() -> Vec<Procedure> {
    let mut out = Vec::new();
    for svc in GRAPH_SERVICES {
        for m in rpc::methods_of(svc) {
            out.push(Procedure {
                service: svc.to_string(),
                method: m,
            });
        }
    }
    out
}

/// What the Rust service serves, as the traffic told it: per method of the graph's services, who served its calls;
/// `fallback` the methods the Rust service answered `unimplemented` (modes compare and shadow).
async fn coverage(State(p): State<Arc<Proxy>>) -> Json<Value> {
    let s = p.stats.lock().unwrap();
    let mut methods = Vec::new();
    let (mut fallback, mut never) = (Vec::new(), Vec::new());
    for proc in graph_methods() {
        let k = proc.to_string();
        match s.calls.get(&k) {
            Some(c) => {
                if c.verdicts.contains_key(&compare::Verdict::RustUnimplemented) {
                    fallback.push(k.clone());
                }
                methods.push(json!({"procedure": k, "served": c.served, "verdicts": c.verdicts}));
            }
            None => never.push(k),
        }
    }
    Json(json!({"mode": p.cfg.mode.name(), "called": methods, "fallback": fallback, "neverCalled": never}))
}

/// The summary of a comparison; writes `summary.json` and `ids.json` in the report directory.
async fn report(State(p): State<Arc<Proxy>>) -> Response {
    let Some(comparer) = &p.comparer else {
        return error(
            StatusCode::NOT_FOUND,
            "not_found",
            "goap-proxy: no comparison in mode ".to_string() + p.cfg.mode.name(),
        );
    };
    let c = comparer.lock().await;
    let (verdicts, by) = {
        let s = p.stats.lock().unwrap();
        let by: serde_json::Map<String, Value> = s
            .calls
            .iter()
            .filter(|(_, c)| !c.verdicts.is_empty())
            .map(|(k, c)| (k.clone(), json!(c.verdicts)))
            .collect();
        (s.verdicts(), by)
    };
    let divergent: u64 = verdicts.iter().filter(|(v, _)| v.diverges()).map(|(_, n)| n).sum();
    let summary = json!({
        "mode": p.cfg.mode.name(),
        "calls": verdicts.values().sum::<u64>(),
        "divergent": divergent,
        "verdicts": verdicts,
        "byProcedure": by,
        "pairedIds": c.ids.len(),
        "clashes": c.clashes(),
        "dir": p.cfg.report_dir,
    });
    let save = c.save_ids().and_then(|_| {
        std::fs::write(
            p.cfg.report_dir.join("summary.json"),
            serde_json::to_vec_pretty(&summary)?,
        )?;
        Ok(())
    });
    if let Err(e) = save {
        tracing::warn!(error = %e, "writing the report");
    }
    Json(summary).into_response()
}

/// Asks the Rust service each method of the graph's services with an empty request: the generated default of a method
/// not written yet answers `unimplemented` before anything else; any other answer is a method it serves. Meant for a
/// Rust service of a test or perf environment (an empty request of a method it serves is refused, at worst it acts).
async fn probe(State(p): State<Arc<Proxy>>) -> Response {
    let Some(rust) = &p.rust else {
        return error(StatusCode::NOT_FOUND, "not_found", "goap-proxy: no Rust service".into());
    };
    let mut headers = HeaderMap::new();
    headers.insert(header::CONTENT_TYPE, "application/json".parse().unwrap());
    headers.insert("connect-protocol-version", "1".parse().unwrap());
    headers.insert("x-goap-subject", "system:goap-proxy".parse().unwrap());
    let (mut served, mut missing) = (Vec::new(), Vec::new());
    for proc in graph_methods() {
        let r = rust
            .call(
                &http::Method::POST,
                &proc.to_string(),
                &headers,
                Bytes::from_static(b"{}"),
            )
            .await;
        if unimplemented(&r) {
            missing.push(proc.to_string())
        } else {
            served.push(proc.to_string())
        }
    }
    Json(json!({"served": served, "missing": missing})).into_response()
}
