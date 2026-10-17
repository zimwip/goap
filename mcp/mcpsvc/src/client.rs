//! The engine side of the hub (`client.go`).

use goap_connect::Request;
use goap_ctx::Ctx;
use goap_gen::mcp::v1::{self as mcpv1, McpService, McpServiceClient};
use goap_identity::{HEADER_ON_BEHALF_OF, HEADER_ORG, HEADER_ROLES, HEADER_SUBJECT};
use http::{HeaderMap, HeaderName, HeaderValue};
use serde_json::{Map, Value};

use crate::{Error, Tool, ToolError};

/// Client is the engine side of the hub: it lists the tools of an organization and calls them, forwarding the
/// principal of the context (the initiator of the process).
#[derive(Clone, Debug)]
pub struct Client {
    rpc: McpServiceClient,
}

fn set(h: &mut HeaderMap, name: &str, value: &str) {
    if let (Ok(n), Ok(v)) = (HeaderName::try_from(name), HeaderValue::from_str(value)) {
        h.insert(n, v);
    }
}

/// The request of msg carrying the principal of ctx.
fn forward<T>(ctx: &Ctx, msg: T) -> Request<T> {
    let mut r = Request::new(msg);
    let p = goap_authz::principal_from(ctx);
    if !p.anonymous() {
        set(&mut r.headers, HEADER_SUBJECT, &p.subject);
        set(&mut r.headers, HEADER_ORG, &p.org);
        if !p.roles.is_empty() {
            set(&mut r.headers, HEADER_ROLES, &p.roles.join(","));
        }
        if p.is_system() && !p.on_behalf_of.is_empty() {
            // the end user the work is attributed to (ADR 0114)
            set(&mut r.headers, HEADER_ON_BEHALF_OF, &p.on_behalf_of);
        }
    }
    r
}

impl Client {
    /// NewClient returns a hub client over a Connect client (its base URL the hub's).
    pub fn new(client: goap_connect::Client) -> Self {
        Client {
            rpc: McpServiceClient::new(client),
        }
    }

    /// A hub client at `base_url`, default settings.
    pub fn connect(base_url: &str) -> Self {
        Client {
            rpc: McpServiceClient::connect(base_url),
        }
    }

    /// CallTool calls "<mcp>/<tool>" for an organization. A failure reported by the connector is returned as an
    /// [`Error::Tool`].
    pub async fn call_tool(
        &self,
        ctx: &Ctx,
        org: &str,
        name: &str,
        args: &Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        let call = goap_mcp::call_from(ctx);
        let r = self
            .rpc
            .call_tool(
                ctx,
                forward(
                    ctx,
                    mcpv1::CallToolRequest {
                        unit: org.to_string(),
                        name: name.to_string(),
                        arguments: goap_pbconv::to_struct(args),
                        change_id: call.change,
                        process_id: call.process,
                    },
                ),
            )
            .await?;
        if r.msg.is_error {
            return Err(ToolError { msg: r.msg.error }.into());
        }
        Ok(goap_pbconv::to_map(r.msg.result.as_ref()))
    }

    /// Tools returns the tools available to an organization and the MCPs it binds.
    pub async fn tools(&self, ctx: &Ctx, org: &str) -> Result<(Vec<Tool>, Vec<String>), Error> {
        let r = self
            .rpc
            .list_tools(
                ctx,
                forward(
                    ctx,
                    mcpv1::ListToolsRequest {
                        unit: org.to_string(),
                    },
                ),
            )
            .await?;
        let out = r
            .msg
            .tools
            .into_iter()
            .map(|t| Tool {
                name: t.name,
                description: t.description,
                input_schema: goap_pbconv::to_map(t.input_schema.as_ref()),
                read_only: t.read_only,
                scope: t.scope,
            })
            .collect();
        Ok((out, r.msg.mcps))
    }
}
