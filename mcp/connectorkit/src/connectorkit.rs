//! The connector service and its registration with the hub (`connectorkit.go`).

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use goap_connect::axum::Router;
use goap_connect::axum::http::{HeaderName, HeaderValue};
use goap_connect::{Client, Code, ConnectError, Ctx, Interceptors, Request, Response, async_trait};
use goap_gen::connector::v1::{
    self as connectorv1, ConnectorInfo, ConnectorService, connector_service_router,
};
use goap_gen::mcp::v1::{self as mcpv1, McpService, McpServiceClient};
use goap_mcp::{CallContext, with_call};
use serde_json::{Map, Value};

/// TokenHeader carries the shared token between the hub and the connectors.
pub const TOKEN_HEADER: &str = "X-Goap-Connector-Token";

/// The error of a connector operation: reported to the caller as a failure of the operation.
pub type InvokeError = Box<dyn std::error::Error + Send + Sync>;

/// ErrUnknownOperation is returned by Invoke for an operation the connector does not offer.
#[derive(Debug, Clone, Copy, PartialEq, Eq, thiserror::Error)]
#[error("unknown operation")]
pub struct ErrUnknownOperation;

/// Connector is a driver for one real service.
#[async_trait]
pub trait Connector: Send + Sync + 'static {
    /// Info describes the connector: identity, configuration schema, secrets and operations.
    fn info(&self) -> ConnectorInfo;
    /// Invoke runs an operation. config is the configuration of the organization that binds the connector, secrets
    /// the resolved secrets it declared. A returned error is reported to the caller as a failure of the operation;
    /// unknown operations are answered by the kit before Invoke is called.
    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        config: Map<String, Value>,
        secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError>;
}

struct Service {
    c: Arc<dyn Connector>,
    token: String,
}

/// Handler returns the Connect routes of a connector, for services that mount them themselves (a single-process
/// platform, tests). token, when not empty, is required on Invoke. (Go returned the path and the handler; the
/// routes carry their paths, `connector_service_methods::SERVICE`.)
pub fn handler(c: Arc<dyn Connector>, token: &str, interceptors: Interceptors) -> Router {
    connector_service_router(
        Arc::new(Service {
            c,
            token: token.to_string(),
        }),
        interceptors,
    )
}

/// `subtle.ConstantTimeCompare(a, b) == 1`.
fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

#[async_trait]
impl ConnectorService for Service {
    async fn describe(
        &self,
        _ctx: &Ctx,
        _req: Request<connectorv1::DescribeRequest>,
    ) -> Result<Response<connectorv1::DescribeResponse>, ConnectError> {
        Ok(Response::new(connectorv1::DescribeResponse {
            info: Some(self.c.info()),
        }))
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        r: Request<connectorv1::InvokeRequest>,
    ) -> Result<Response<connectorv1::InvokeResponse>, ConnectError> {
        if !self.token.is_empty()
            && !constant_time_eq(r.header(TOKEN_HEADER).as_bytes(), self.token.as_bytes())
        {
            return Err(ConnectError::new(
                Code::PermissionDenied,
                "invalid connector token",
            ));
        }
        let msg = r.msg;
        let known = self
            .c
            .info()
            .operations
            .iter()
            .any(|op| op.name == msg.operation);
        if !known {
            return Err(ConnectError::new(
                Code::InvalidArgument,
                format!("{}: {}", msg.operation, ErrUnknownOperation),
            ));
        }
        // what the call runs for, as an in-process connector reads it from its context (ADR 0118)
        let ctx = with_call(
            ctx,
            CallContext {
                unit: msg.org_id,
                change: msg.change_id,
                process: msg.process_id,
                model_url: msg.model_url,
                model_token: msg.model_token,
            },
        );
        let res = self
            .c
            .invoke(
                &ctx,
                &msg.operation,
                goap_pbconv::to_map(msg.arguments.as_ref()),
                goap_pbconv::to_map(msg.config.as_ref()),
                msg.secrets,
            )
            .await;
        Ok(Response::new(match res {
            Err(err) => connectorv1::InvokeResponse {
                is_error: true,
                error: err.to_string(),
                ..Default::default()
            },
            Ok(res) => connectorv1::InvokeResponse {
                result: goap_pbconv::to_struct(&res),
                ..Default::default()
            },
        }))
    }
}

/// Register announces the connector to the hub and keeps the registration alive until ctx is done. It retries while
/// the hub is unreachable, so start order does not matter.
pub async fn register(
    ctx: &Ctx,
    hc: reqwest::Client,
    hub_url: &str,
    endpoint: &str,
    token: &str,
    info: ConnectorInfo,
) {
    let hub = McpServiceClient::new(Client::builder(hub_url).http(hc).build());
    let mut registered = false;
    loop {
        let mut req = Request::new(mcpv1::RegisterConnectorRequest {
            info: Some(info.clone()),
            endpoint: endpoint.to_string(),
        });
        if !token.is_empty()
            && let Ok(v) = HeaderValue::from_str(token)
        {
            req.headers
                .insert(HeaderName::from_static("x-goap-connector-token"), v);
        }
        let delay = match hub.register_connector(ctx, req).await {
            Ok(resp) => {
                if !registered {
                    tracing::info!(id = %info.id, hub = %hub_url, endpoint = %endpoint, "connector registered");
                }
                registered = true;
                let lease = Duration::from_secs(resp.msg.lease_seconds.max(0) as u64) / 3;
                lease.max(Duration::from_secs(1))
            }
            Err(err) => {
                if registered {
                    tracing::warn!(err = %err, "connector registration lost");
                } else {
                    tracing::warn!(err = %err, "connector registration failed, retrying");
                }
                registered = false;
                Duration::from_secs(2)
            }
        };
        tokio::select! {
            _ = ctx.done() => return,
            _ = tokio::time::sleep(delay) => {}
        }
    }
}

/// Run serves the connector and registers it with the hub. It returns when the server stops (Connect handler
/// options of the telemetry are not reproduced: the Rust telemetry crate offers no Connect interceptor yet).
pub async fn run(name: &str, c: Arc<dyn Connector>) {
    goap_platform::logger(name);
    let bg = Ctx::background();
    let tel = goap_telemetry::setup(&bg, name);
    let token = std::env::var("GOAP_CONNECTOR_TOKEN").unwrap_or_default();
    let addr = goap_platform::env("GOAP_HTTP_ADDR", ":8080");
    let mut srv = goap_platform::Server::new(&addr);
    srv.mount(handler(c.clone(), &token, Interceptors::none()));
    let (ctx, cancel) = bg.with_cancel();
    let hub_url = goap_platform::env("GOAP_MCP_URL", "http://localhost:8085");
    let endpoint = advertised(&addr);
    let info = c.info();
    let reg_ctx = ctx.clone();
    let reg_token = token.clone();
    let reg = tokio::spawn(async move {
        register(
            &reg_ctx,
            goap_platform::h2c_client(),
            &hub_url,
            &endpoint,
            &reg_token,
            info,
        )
        .await
    });
    let res = srv.run().await;
    cancel.cancel();
    let _ = reg.await;
    let _ = tokio::task::spawn_blocking(move || tel.shutdown()).await;
    if let Err(err) = res {
        goap_platform::fatal("server", err);
    }
}

fn hostname() -> String {
    for f in ["/proc/sys/kernel/hostname", "/etc/hostname"] {
        if let Ok(s) = std::fs::read_to_string(f) {
            let s = s.trim();
            if !s.is_empty() {
                return s.to_string();
            }
        }
    }
    std::env::var("HOSTNAME")
        .or_else(|_| std::env::var("COMPUTERNAME"))
        .unwrap_or_default()
}

/// `net.SplitHostPort`'s port.
fn port_of(addr: &str) -> Option<&str> {
    let i = addr.rfind(':')?;
    let host = &addr[..i];
    if host.contains(':') && !(host.starts_with('[') && host.ends_with(']')) {
        return None;
    }
    Some(&addr[i + 1..])
}

/// `net.JoinHostPort`.
fn join_host_port(host: &str, port: &str) -> String {
    if host.contains(':') {
        format!("[{host}]:{port}")
    } else {
        format!("{host}:{port}")
    }
}

/// The URL of this connector as the hub reaches it.
pub fn advertised(addr: &str) -> String {
    if let Ok(u) = std::env::var("GOAP_CONNECTOR_URL")
        && !u.is_empty()
    {
        return u;
    }
    let port = port_of(addr).unwrap_or("8080");
    format!("http://{}", join_host_port(&hostname(), port))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn ports_as_go_splits_them() {
        assert_eq!(port_of(":8080"), Some("8080"));
        assert_eq!(port_of("0.0.0.0:9"), Some("9"));
        assert_eq!(port_of("[::1]:7"), Some("7"));
        assert_eq!(port_of("nohost"), None);
        assert_eq!(join_host_port("::1", "7"), "[::1]:7");
        assert!(constant_time_eq(b"ab", b"ab"));
        assert!(!constant_time_eq(b"ab", b"abc"));
    }
}
