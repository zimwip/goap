//! The Connect handler of `goap.mcp.v1.McpService` (`handler.go`).

use std::sync::Arc;

use goap_adapter as adapter;
use goap_authz::{Authorizer, Resource};
use goap_connect::{Code, ConnectError, Request, Response, async_trait};
use goap_ctx::Ctx;
use goap_gen::mcp::v1 as mcpv1;
use goap_identity::Extractor;
use goap_mcp as mcp;
use http::HeaderMap;

use crate::{CONNECTOR_TOKEN_HEADER, Error, Service};

/// Handler implements `goap_gen::mcp::v1::McpService` (mount it with `mcp_service_router`).
#[derive(Clone)]
pub struct Handler {
    pub service: Arc<Service>,
    pub authz: Option<Arc<dyn Authorizer>>,
    pub identity: Extractor,
    /// ConnectorToken must accompany RegisterConnector (header CONNECTOR_TOKEN_HEADER); with none configured no
    /// connector can register: an unauthenticated service could otherwise announce itself as the implementation of
    /// any MCP and receive the arguments and secrets of its calls.
    pub connector_token: String,
}

impl Handler {
    /// A handler of the service, with no authorizer, the default identity and no connector token.
    pub fn new(service: Arc<Service>) -> Self {
        Handler {
            service,
            authz: None,
            identity: Extractor::default(),
            connector_token: String::new(),
        }
    }
}

/// rpcErr maps an error of the hub to its Connect code.
pub fn rpc_err(err: Error) -> ConnectError {
    let code = match &err {
        Error::Rpc(ce) => return ce.clone(),
        Error::NotFound(_) => Code::NotFound,
        Error::NotBound(_) => Code::FailedPrecondition,
        Error::Invalid(_) => Code::InvalidArgument,
        Error::Unavailable(_) => Code::Unavailable,
        Error::Tool(_) => Code::Unknown,
        _ => Code::Internal,
    };
    ConnectError::from_error(code, err)
}

fn authz_err(err: goap_authz::Error) -> ConnectError {
    if err.is_forbidden() {
        ConnectError::from_error(Code::PermissionDenied, err)
    } else {
        ConnectError::from_error(Code::Internal, err)
    }
}

/// `subtle.ConstantTimeCompare(a, b) == 1`.
fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    if a.len() != b.len() {
        return false;
    }
    a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

fn def_to_pb(d: &mcp::Def) -> mcpv1::Mcp {
    mcpv1::Mcp {
        name: d.name.clone(),
        description: d.description.clone(),
        scope: mcp::scope_of(&d.scope).to_string(),
        tools: d
            .tools
            .iter()
            .map(|t| mcpv1::McpTool {
                name: t.name.clone(),
                description: t.description.clone(),
                input_schema: goap_pbconv::to_struct(&t.input_schema),
                read_only: t.read_only,
            })
            .collect(),
    }
}

fn adapter_to_pb(a: &adapter::Instance) -> mcpv1::Adapter {
    mcpv1::Adapter {
        unit: a.unit.clone(),
        mcp: a.mcp.clone(),
        adapter: a.adapter.clone(),
        params: goap_pbconv::to_struct(&a.params),
        disabled: a.disabled,
        tools: a.tools.clone(),
        deny: a.deny.clone(),
        read_only: a.read_only,
    }
}

fn adapter_from_pb(a: Option<&mcpv1::Adapter>) -> adapter::Instance {
    let Some(a) = a else {
        return adapter::Instance::default();
    };
    adapter::Instance {
        unit: a.unit.clone(),
        mcp: a.mcp.clone(),
        adapter: a.adapter.clone(),
        params: goap_pbconv::to_map(a.params.as_ref()),
        disabled: a.disabled,
        tools: a.tools.clone(),
        deny: a.deny.clone(),
        read_only: a.read_only,
    }
}

impl Handler {
    /// check authorizes the caller (identity of the request) for action on res.
    async fn check(
        &self,
        ctx: &Ctx,
        hdr: &HeaderMap,
        action: &str,
        res: Resource,
    ) -> Result<Ctx, ConnectError> {
        let ctx = self.identity.context(ctx, hdr);
        let req = goap_authz::Request {
            subject: goap_authz::principal_from(&ctx),
            action: action.to_string(),
            resource: res,
        };
        goap_authz::check(&ctx, self.authz.as_deref(), &req)
            .await
            .map_err(authz_err)?;
        Ok(ctx)
    }

    /// callerResource is the resource of a request made on behalf of the caller's own tenant.
    fn caller_resource(&self, ctx: &Ctx, hdr: &HeaderMap, typ: &str, name: &str) -> Resource {
        let who = goap_authz::principal_from(&self.identity.context(ctx, hdr));
        Resource {
            r#type: typ.to_string(),
            name: name.to_string(),
            org: who.org,
            project_id: who.project,
            ..Default::default()
        }
    }

    /// unitOf resolves the unit a request acts for against its caller: the caller's own unit when the request names
    /// none, else the requested one must be the caller's unit or below it (the caller's unit is in its chain), so
    /// that a member of one unit cannot use the adapters, and the secrets they hold, of another. Administrators and
    /// platform services (system: subjects) may name any unit.
    pub async fn unit_of(
        &self,
        ctx: &Ctx,
        hdr: &HeaderMap,
        requested: &str,
    ) -> Result<String, ConnectError> {
        let who = goap_authz::principal_from(&self.identity.context(ctx, hdr));
        if who.is_system() || who.roles.iter().any(|r| r == "admin") {
            return Ok(requested.to_string());
        }
        if requested.is_empty() {
            return Ok(who.org);
        }
        if !who.org.is_empty() {
            let snap = self
                .service
                .dir()
                .map_err(rpc_err)?
                .snapshot(ctx)
                .await
                .map_err(rpc_err)?;
            if snap.chain(requested).contains(&who.org) {
                return Ok(requested.to_string());
            }
        }
        Err(ConnectError::new(
            Code::PermissionDenied,
            format!(
                "unit {requested} is not within the unit of the caller: {}",
                goap_authz::ERR_FORBIDDEN
            ),
        ))
    }
}

#[async_trait]
impl mcpv1::McpService for Handler {
    async fn register_connector(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::RegisterConnectorRequest>,
    ) -> Result<Response<mcpv1::RegisterConnectorResponse>, ConnectError> {
        if self.connector_token.is_empty() {
            return Err(ConnectError::new(
                Code::PermissionDenied,
                "connector registration is closed: no connector token is configured (GOAP_CONNECTOR_TOKEN)",
            ));
        }
        if !constant_time_eq(
            r.header(CONNECTOR_TOKEN_HEADER).as_bytes(),
            self.connector_token.as_bytes(),
        ) {
            return Err(ConnectError::new(
                Code::PermissionDenied,
                "invalid connector token",
            ));
        }
        let lease = self
            .service
            .register_connector(ctx, r.msg.info, &r.msg.endpoint)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(mcpv1::RegisterConnectorResponse {
            lease_seconds: lease.as_secs() as i32,
        }))
    }

    async fn list_connectors(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::ListConnectorsRequest>,
    ) -> Result<Response<mcpv1::ListConnectorsResponse>, ConnectError> {
        self.check(
            ctx,
            &r.headers,
            "read",
            Resource {
                r#type: "connector".into(),
                ..Default::default()
            },
        )
        .await?;
        let cs = self.service.connectors(ctx).await.map_err(rpc_err)?;
        Ok(Response::new(mcpv1::ListConnectorsResponse {
            connectors: cs
                .into_iter()
                .map(|c| mcpv1::Connector {
                    last_seen: goap_pbconv::time(&c.reg.last_seen),
                    info: Some(c.reg.info),
                    endpoint: c.reg.endpoint,
                    live: c.live,
                })
                .collect(),
        }))
    }

    async fn list_mcps(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::ListMcpsRequest>,
    ) -> Result<Response<mcpv1::ListMcpsResponse>, ConnectError> {
        let res = self.caller_resource(ctx, &r.headers, "mcp", "");
        self.check(ctx, &r.headers, "read", res).await?;
        let ds = self.service.mcps(ctx).await.map_err(rpc_err)?;
        Ok(Response::new(mcpv1::ListMcpsResponse {
            mcps: ds.iter().map(def_to_pb).collect(),
        }))
    }

    async fn list_effective(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::ListEffectiveRequest>,
    ) -> Result<Response<mcpv1::ListEffectiveResponse>, ConnectError> {
        let res = self.caller_resource(ctx, &r.headers, "adapter", &r.msg.unit);
        self.check(ctx, &r.headers, "read", res).await?;
        let org = self.unit_of(ctx, &r.headers, &r.msg.unit).await?;
        let (chain, eff) = self.service.effective(ctx, &org).await.map_err(rpc_err)?;
        let mut out = mcpv1::ListEffectiveResponse {
            chain,
            mcps: Vec::new(),
        };
        for e in eff {
            out.mcps.push(mcpv1::EffectiveMcp {
                mcp: Some(def_to_pb(&e.mcp)),
                adapter: Some(adapter_to_pb(&e.adapter)),
                inherited: e.inherited,
                connector: self.service.connector_of(ctx, &e.adapter).await,
                allowed_tools: e.allowed().tools.into_iter().map(|t| t.name).collect(),
                restricted_by: e.restriction.by.clone(),
                disabled: e.restriction.disabled,
                builtin: goap_mcpbuiltin::is(&e.mcp.name),
            });
        }
        Ok(Response::new(out))
    }

    async fn check_adapter(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::CheckAdapterRequest>,
    ) -> Result<Response<mcpv1::CheckAdapterResponse>, ConnectError> {
        let a = adapter_from_pb(r.msg.adapter.as_ref());
        let res = self.caller_resource(ctx, &r.headers, "adapter", &a.mcp);
        self.check(ctx, &r.headers, "read", res).await?;
        let warnings = self.service.check_adapter(ctx, &a).await.map_err(rpc_err)?;
        Ok(Response::new(mcpv1::CheckAdapterResponse { warnings }))
    }

    async fn adapter_template(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::AdapterTemplateRequest>,
    ) -> Result<Response<mcpv1::AdapterTemplateResponse>, ConnectError> {
        let res = self.caller_resource(ctx, &r.headers, "adapter", &r.msg.mcp);
        self.check(ctx, &r.headers, "read", res).await?;
        let (code, params) = self
            .service
            .template(ctx, &r.msg.mcp, &r.msg.connector)
            .await
            .map_err(rpc_err)?;
        Ok(Response::new(mcpv1::AdapterTemplateResponse {
            code,
            params: params
                .into_iter()
                .map(|p| mcpv1::TemplateParam {
                    name: p.name,
                    r#type: p.r#type,
                    description: p.description,
                    required: p.required,
                })
                .collect(),
        }))
    }

    async fn list_tools(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::ListToolsRequest>,
    ) -> Result<Response<mcpv1::ListToolsResponse>, ConnectError> {
        let res = self.caller_resource(ctx, &r.headers, "tool", "");
        self.check(ctx, &r.headers, "read", res).await?;
        let org = self.unit_of(ctx, &r.headers, &r.msg.unit).await?;
        let (tools, mcps) = self.service.tools(ctx, &org).await.map_err(rpc_err)?;
        Ok(Response::new(mcpv1::ListToolsResponse {
            mcps,
            tools: tools
                .into_iter()
                .map(|t| mcpv1::Tool {
                    input_schema: goap_pbconv::to_struct(&t.input_schema),
                    name: t.name,
                    description: t.description,
                    read_only: t.read_only,
                    scope: t.scope,
                })
                .collect(),
        }))
    }

    async fn call_tool(
        &self,
        ctx: &Ctx,
        r: Request<mcpv1::CallToolRequest>,
    ) -> Result<Response<mcpv1::CallToolResponse>, ConnectError> {
        let res = self.caller_resource(ctx, &r.headers, "tool", &r.msg.name);
        let ctx = self.check(ctx, &r.headers, "call", res).await?;
        let org = self.unit_of(&ctx, &r.headers, &r.msg.unit).await?;
        let msg = r.msg;
        let ctx = mcp::with_call(
            &ctx,
            mcp::CallContext {
                change: msg.change_id,
                process: msg.process_id,
                ..Default::default()
            },
        );
        match self
            .service
            .call(
                &ctx,
                &org,
                &msg.name,
                goap_pbconv::to_map(msg.arguments.as_ref()),
            )
            .await
        {
            Err(Error::Tool(te)) => Ok(Response::new(mcpv1::CallToolResponse {
                is_error: true,
                error: te.msg,
                ..Default::default()
            })),
            Err(e) => Err(rpc_err(e)),
            Ok(res) => Ok(Response::new(mcpv1::CallToolResponse {
                result: goap_pbconv::to_struct(&res),
                ..Default::default()
            })),
        }
    }
}
