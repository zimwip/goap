//! The call of connectors over Connect (`invoker.go`).

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_connect::Request;
use goap_ctx::Ctx;
use goap_gen::connector::v1::{
    ConnectorService, ConnectorServiceClient, InvokeRequest, InvokeResponse,
};
use http::{HeaderName, HeaderValue};
use parking_lot::Mutex;

use crate::{Error, Invoker, SecretError, SecretResolver};

/// ConnectorTokenHeader carries the shared token between the hub and the connectors.
pub const CONNECTOR_TOKEN_HEADER: &str = "X-Goap-Connector-Token";

/// ConnectInvoker calls connectors over Connect. Token, when set, is sent on every call: connectors reject calls
/// without it (GOAP_CONNECTOR_TOKEN).
#[derive(Default)]
pub struct ConnectInvoker {
    /// The HTTP client of the calls (None: `goap_platform::h2c_client()`).
    pub http: Option<reqwest::Client>,
    pub token: String,

    clients: Mutex<HashMap<String, ConnectorServiceClient>>,
}

impl ConnectInvoker {
    /// An invoker sending `token` (empty: none).
    pub fn new(token: impl Into<String>) -> Self {
        ConnectInvoker {
            token: token.into(),
            ..Default::default()
        }
    }

    fn client(&self, endpoint: &str) -> ConnectorServiceClient {
        let mut clients = self.clients.lock();
        if let Some(c) = clients.get(endpoint) {
            return c.clone();
        }
        let hc = self.http.clone().unwrap_or_else(goap_platform::h2c_client);
        let c =
            ConnectorServiceClient::new(goap_connect::Client::builder(endpoint).http(hc).build());
        clients.insert(endpoint.to_string(), c.clone());
        c
    }
}

#[async_trait]
impl Invoker for ConnectInvoker {
    async fn invoke(
        &self,
        ctx: &Ctx,
        endpoint: &str,
        req: InvokeRequest,
    ) -> Result<InvokeResponse, Error> {
        let mut r = Request::new(req);
        if !self.token.is_empty() {
            let v = HeaderValue::from_str(&self.token)
                .map_err(|e| Error::Message(format!("connector token: {e}")))?;
            r.headers
                .insert(HeaderName::from_static("x-goap-connector-token"), v);
        }
        Ok(self.client(endpoint).invoke(ctx, r).await?.msg)
    }
}

struct PlatformSecrets(Arc<goap_platform::Secrets>);

#[async_trait]
impl SecretResolver for PlatformSecrets {
    async fn resolve(&self, ctx: &Ctx, reference: &str) -> Result<String, SecretError> {
        Ok(self.0.resolve(ctx, reference).await?)
    }
}

/// ResolveSecret resolves a secret reference of an adapter (see `goap_platform::Secrets::resolve`).
pub fn resolve_secret(s: Arc<goap_platform::Secrets>) -> Arc<dyn SecretResolver> {
    Arc::new(PlatformSecrets(s))
}
