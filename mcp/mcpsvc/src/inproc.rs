//! Connectors running inside the process of the hub (`inproc.go`).

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_connectorkit::Connector;
use goap_ctx::Ctx;
use goap_gen::connector::v1::{InvokeRequest, InvokeResponse};
use goap_mcp as mcp;
use serde_json::{Map, Value};

use crate::{Error, Invoker, Service};

/// InprocScheme prefixes the endpoint of connectors running inside the process of the hub (single-process
/// platform, tests).
pub const INPROC_SCHEME: &str = "inproc://";

/// InprocInvoker calls connectors registered in-process (endpoint "inproc://<id>") directly and delegates the other
/// endpoints to Remote.
#[derive(Clone, Default)]
pub struct InprocInvoker {
    pub connectors: HashMap<String, Arc<dyn Connector>>,
    pub remote: Option<Arc<dyn Invoker>>,
}

#[async_trait]
impl Invoker for InprocInvoker {
    async fn invoke(
        &self,
        ctx: &Ctx,
        endpoint: &str,
        req: InvokeRequest,
    ) -> Result<InvokeResponse, Error> {
        let Some(id) = endpoint.strip_prefix(INPROC_SCHEME) else {
            return match &self.remote {
                Some(r) => r.invoke(ctx, endpoint, req).await,
                None => Err(Error::Message(format!(
                    "no invoker for the endpoint {endpoint}"
                ))),
            };
        };
        let Some(c) = self.connectors.get(id) else {
            return Ok(InvokeResponse {
                is_error: true,
                error: format!("no in-process connector {id}"),
                ..Default::default()
            });
        };
        let res = c
            .invoke(
                ctx,
                &req.operation,
                goap_pbconv::to_map(req.arguments.as_ref()),
                goap_pbconv::to_map(req.config.as_ref()),
                req.secrets,
            )
            .await;
        Ok(match res {
            Err(err) => InvokeResponse {
                is_error: true,
                error: err.to_string(),
                ..Default::default()
            },
            Ok(res) => InvokeResponse {
                result: goap_pbconv::to_struct(&res),
                ..Default::default()
            },
        })
    }
}

impl Service {
    /// KeepRegistered registers in-process connectors with the hub and renews them until ctx is done: the first
    /// registration is done when it returns, the renewals run in a task of their own.
    pub async fn keep_registered(
        self: &Arc<Self>,
        ctx: &Ctx,
        connectors: HashMap<String, Arc<dyn Connector>>,
    ) {
        let register = |s: Arc<Service>,
                        ctx: Ctx,
                        connectors: HashMap<String, Arc<dyn Connector>>| async move {
            for (id, c) in &connectors {
                let _ = s
                    .register_connector(&ctx, Some(c.info()), &format!("{INPROC_SCHEME}{id}"))
                    .await;
            }
        };
        register(self.clone(), ctx.clone(), connectors.clone()).await;
        let (s, ctx) = (self.clone(), ctx.clone());
        let period = s.lease() / 3;
        tokio::spawn(async move {
            let mut t = tokio::time::interval_at(tokio::time::Instant::now() + period, period);
            loop {
                tokio::select! {
                    _ = ctx.done() => return,
                    _ = t.tick() => register(s.clone(), ctx.clone(), connectors.clone()).await,
                }
            }
        });
    }
}

/// HubPort adapts the service to the engine when both run in the same process: no network hop, the caller has
/// already been authorized by the engine.
#[derive(Clone)]
pub struct HubPort {
    pub service: Arc<Service>,
}

impl HubPort {
    /// CallTool calls a tool of an organization.
    pub async fn call_tool(
        &self,
        ctx: &Ctx,
        org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        self.service.call(ctx, org, name, args).await
    }

    /// Tools lists the tools of an organization and the MCPs it binds.
    pub async fn tools(
        &self,
        ctx: &Ctx,
        org: &str,
    ) -> Result<(Vec<mcp::ToolInfo>, Vec<String>), Error> {
        self.service.tools(ctx, org).await
    }
}
