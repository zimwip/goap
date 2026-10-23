//! A graph service behind the proxy: a request sent as is, its answer read whole (every call of the graph's services
//! is unary).
use std::time::{Duration, Instant};

use anyhow::Context;
use bytes::Bytes;
use http::{HeaderMap, HeaderName, Method, Request, StatusCode, Uri, header};
use http_body_util::{BodyExt, Full};
use hyper_util::{
    client::legacy::{Client, connect::HttpConnector},
    rt::TokioExecutor,
};

/// Which service answered.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, PartialOrd, Ord, serde::Serialize)]
#[serde(rename_all = "snake_case")]
pub enum Side {
    Go,
    Rust,
}

impl Side {
    pub fn name(self) -> &'static str {
        match self {
            Side::Go => "go",
            Side::Rust => "rust",
        }
    }
}

/// An answer, read whole.
#[derive(Debug, Clone)]
pub struct Reply {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body: Bytes,
    pub elapsed: Duration,
}

impl Reply {
    /// An answer of the proxy itself, when a service cannot be reached.
    pub fn unavailable(side: Side, err: &anyhow::Error) -> Reply {
        let body = serde_json::json!({"code": "unavailable", "message": format!("goap-proxy: {} graph service: {err:#}", side.name())});
        let mut headers = HeaderMap::new();
        headers.insert(header::CONTENT_TYPE, "application/json".parse().unwrap());
        Reply {
            status: StatusCode::SERVICE_UNAVAILABLE,
            headers,
            body: Bytes::from(body.to_string()),
            elapsed: Duration::ZERO,
        }
    }
}

/// The headers that belong to one hop, never forwarded (RFC 9110 §7.6.1), and the ones the client recomputes.
fn hop_by_hop(name: &HeaderName) -> bool {
    matches!(
        name.as_str(),
        "connection"
            | "keep-alive"
            | "proxy-authenticate"
            | "proxy-authorization"
            | "proxy-connection"
            | "te"
            | "trailer"
            | "transfer-encoding"
            | "upgrade"
            | "host"
            | "content-length"
    )
}

/// The headers of a request or an answer without the ones of the hop.
pub fn end_to_end(h: &HeaderMap) -> HeaderMap {
    let mut out = HeaderMap::with_capacity(h.len());
    for (k, v) in h {
        if !hop_by_hop(k) {
            out.append(k.clone(), v.clone());
        }
    }
    out
}

#[derive(Clone)]
pub struct Upstream {
    pub side: Side,
    base: Uri,
    client: Client<HttpConnector, Full<Bytes>>,
}

impl Upstream {
    pub fn new(side: Side, base: Uri) -> Upstream {
        let mut connector = HttpConnector::new();
        connector.set_nodelay(true);
        connector.set_keepalive(Some(Duration::from_secs(60)));
        let client = Client::builder(TokioExecutor::new())
            .pool_idle_timeout(Duration::from_secs(90))
            .pool_max_idle_per_host(256)
            .build(connector);
        Upstream { side, base, client }
    }

    fn uri(&self, path_and_query: &str) -> anyhow::Result<Uri> {
        let base = self.base.to_string();
        format!("{}{}", base.trim_end_matches('/'), path_and_query)
            .parse()
            .context("upstream URI")
    }

    /// Sends a request and reads its answer whole.
    pub async fn send(
        &self,
        method: &Method,
        path_and_query: &str,
        headers: &HeaderMap,
        body: Bytes,
    ) -> anyhow::Result<Reply> {
        let mut req = Request::builder().method(method.clone()).uri(self.uri(path_and_query)?);
        *req.headers_mut().context("request")? = end_to_end(headers);
        let req = req.body(Full::new(body))?;
        let start = Instant::now();
        let res = self
            .client
            .request(req)
            .await
            .with_context(|| format!("{} {}", method, self.base))?;
        let (parts, body) = res.into_parts();
        let body = body.collect().await.context("reading the answer")?.to_bytes();
        Ok(Reply {
            status: parts.status,
            headers: end_to_end(&parts.headers),
            body,
            elapsed: start.elapsed(),
        })
    }

    /// Sends a request; an unreachable service answers `unavailable`.
    pub async fn call(&self, method: &Method, path_and_query: &str, headers: &HeaderMap, body: Bytes) -> Reply {
        match self.send(method, path_and_query, headers, body).await {
            Ok(r) => r,
            Err(e) => {
                tracing::warn!(side = self.side.name(), error = %format!("{e:#}"), "upstream unreachable");
                Reply::unavailable(self.side, &e)
            }
        }
    }
}
