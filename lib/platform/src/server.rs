//! The HTTP server of a service: axum, HTTP/1.1 (browsers) and HTTP/2 cleartext (Connect calls between services).
use std::{future::Future, io, pin::Pin, sync::Arc, time::Duration};

use axum::{
    Router,
    extract::Request,
    http::StatusCode,
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{MethodRouter, get},
};
use goap_ctx::Ctx;
use parking_lot::RwLock;
use tokio::net::TcpListener;
use tower_http::{
    catch_panic::CatchPanicLayer,
    request_id::{MakeRequestUuid, PropagateRequestIdLayer, SetRequestIdLayer},
};

/// A readiness check: `Err(reason)` while the service cannot serve.
pub type ReadyCheck =
    Arc<dyn Fn(Ctx) -> Pin<Box<dyn Future<Output = Result<(), String>> + Send>> + Send + Sync>;

/// The server of a service: its routes, `/healthz` and `/readyz`, request logging (debug), request ids and panic
/// recovery.
pub struct Server {
    pub addr: String,
    router: Router,
    ready: Arc<RwLock<Vec<ReadyCheck>>>,
}

impl Server {
    /// A server listening on `addr` (`:8080` is read as `0.0.0.0:8080`).
    pub fn new(addr: &str) -> Self {
        Server {
            addr: addr.to_string(),
            router: Router::new(),
            ready: Arc::default(),
        }
    }

    /// Registers a readiness check.
    pub fn readiness<F, Fut, E>(&self, f: F)
    where
        F: Fn(Ctx) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = Result<(), E>> + Send + 'static,
        E: std::fmt::Display,
    {
        let f = Arc::new(f);
        self.ready.write().push(Arc::new(move |ctx| {
            let f = f.clone();
            Box::pin(async move { f(ctx).await.map_err(|e| e.to_string()) })
        }));
    }

    /// Adds a route.
    pub fn route(&mut self, path: &str, method_router: MethodRouter) -> &mut Self {
        self.router = std::mem::take(&mut self.router).route(path, method_router);
        self
    }

    /// Mounts a router, such as the one a generated `<service>_router` returns (`Mount`).
    pub fn mount(&mut self, router: Router) -> &mut Self {
        self.router = std::mem::take(&mut self.router).merge(router);
        self
    }

    /// Mounts a router under a path prefix.
    pub fn nest(&mut self, path: &str, router: Router) -> &mut Self {
        self.router = std::mem::take(&mut self.router).nest(path, router);
        self
    }

    /// The whole application: the routes, the health endpoints and the middlewares.
    pub fn app(&self) -> Router {
        let ready = self.ready.clone();
        self.router
            .clone()
            .route("/healthz", get(|| async { "ok" }))
            .route(
                "/readyz",
                get(move || {
                    let checks: Vec<ReadyCheck> = ready.read().clone();
                    async move {
                        for f in checks {
                            if let Err(e) = f(Ctx::background()).await {
                                return (StatusCode::SERVICE_UNAVAILABLE, e).into_response();
                            }
                        }
                        (StatusCode::OK, "ready").into_response()
                    }
                }),
            )
            .layer(middleware::from_fn(log_request))
            .layer(PropagateRequestIdLayer::x_request_id())
            .layer(SetRequestIdLayer::x_request_id(MakeRequestUuid))
            .layer(CatchPanicLayer::new())
    }

    fn bind_addr(&self) -> String {
        if self.addr.starts_with(':') {
            format!("0.0.0.0{}", self.addr)
        } else {
            self.addr.clone()
        }
    }

    /// Serves until SIGINT / SIGTERM, then shuts down gracefully.
    pub async fn run(self) -> io::Result<()> {
        self.run_ctx(&Ctx::background()).await
    }

    /// Serves until `ctx` ends or SIGINT / SIGTERM, then shuts down gracefully (in-flight requests get 15 s).
    pub async fn run_ctx(self, ctx: &Ctx) -> io::Result<()> {
        let ln = TcpListener::bind(self.bind_addr()).await?;
        self.serve(ln, ctx).await
    }

    /// Serves on `ln` until `ctx` ends or SIGINT / SIGTERM, then shuts down gracefully.
    pub async fn serve(self, ln: TcpListener, ctx: &Ctx) -> io::Result<()> {
        let app = self.app();
        tracing::info!(addr = %ln.local_addr().map(|a| a.to_string()).unwrap_or_default(), "listening");
        let (stop_tx, stop_rx) = tokio::sync::oneshot::channel::<()>();
        let mut srv = tokio::spawn(async move {
            axum::serve(ln, app)
                .with_graceful_shutdown(async move {
                    let _ = stop_rx.await;
                })
                .await
        });
        tokio::select! {
            r = &mut srv => return r.map_err(io::Error::other)?,
            _ = shutdown_signal() => {},
            _ = ctx.done() => {},
        }
        tracing::info!("shutting down");
        let _ = stop_tx.send(());
        match tokio::time::timeout(Duration::from_secs(15), &mut srv).await {
            Ok(r) => r.map_err(io::Error::other)?,
            Err(_) => {
                srv.abort();
                Err(io::Error::new(
                    io::ErrorKind::TimedOut,
                    "shutdown: context deadline exceeded",
                ))
            }
        }
    }
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let term = async {
            match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
                Ok(mut s) => {
                    s.recv().await;
                }
                Err(_) => std::future::pending::<()>().await,
            }
        };
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {},
            _ = term => {},
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

/// Logs every request but the health checks, at debug.
async fn log_request(req: Request, next: Next) -> Response {
    let uri = req.uri().to_string();
    if uri == "/healthz" || uri == "/readyz" {
        return next.run(req).await;
    }
    let method = req.method().clone();
    let start = std::time::Instant::now();
    let resp = next.run(req).await;
    tracing::debug!(method = %method, uri = %uri, status = resp.status().as_u16(), latency = ?start.elapsed(), "request");
    resp
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::h2c_client;

    /// The server answers HTTP/2 cleartext (the Connect calls between services) and HTTP/1.1 (browsers).
    #[tokio::test]
    async fn server_speaks_h2c_and_http1() {
        let mut s = Server::new("127.0.0.1:0");
        s.route(
            "/proto",
            get(|req: Request| async move { format!("{:?}", req.version()) }),
        );
        let ln = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}", ln.local_addr().unwrap());
        let (ctx, stop) = Ctx::background().with_cancel();
        let srv = tokio::spawn(async move { s.serve(ln, &ctx).await });
        for (name, client, want) in [
            ("h2c", h2c_client(), "HTTP/2.0"),
            ("http1", reqwest::Client::new(), "HTTP/1.1"),
        ] {
            let resp = client
                .get(format!("{url}/proto"))
                .send()
                .await
                .unwrap_or_else(|e| panic!("{name}: {e}"));
            assert_eq!(resp.status(), 200, "{name}");
            assert_eq!(resp.text().await.unwrap(), want, "{name}");
        }
        let resp = h2c_client()
            .get(format!("{url}/healthz"))
            .send()
            .await
            .unwrap();
        assert_eq!(resp.status(), 200);
        stop.cancel();
        srv.await.unwrap().unwrap();
    }

    #[tokio::test]
    async fn readiness_checks() {
        let s = Server::new("127.0.0.1:0");
        let flag = Arc::new(parking_lot::Mutex::new(false));
        let f = flag.clone();
        s.readiness(move |_ctx| {
            let up = *f.lock();
            async move { if up { Ok(()) } else { Err("database down") } }
        });
        let ln = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let url = format!("http://{}/readyz", ln.local_addr().unwrap());
        let (ctx, stop) = Ctx::background().with_cancel();
        let srv = tokio::spawn(async move { s.serve(ln, &ctx).await });
        let resp = reqwest::get(&url).await.unwrap();
        assert_eq!(resp.status(), 503);
        assert_eq!(resp.text().await.unwrap(), "database down");
        *flag.lock() = true;
        let resp = reqwest::get(&url).await.unwrap();
        assert_eq!(resp.status(), 200);
        assert!(resp.headers().contains_key("x-request-id"));
        stop.cancel();
        srv.await.unwrap().unwrap();
    }
}
