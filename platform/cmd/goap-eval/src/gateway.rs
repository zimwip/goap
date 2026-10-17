//! The model gateway as an `llm::Client`, over its Connect API (Go's `modelgw.NewClient`; `goap-modelgw` has no
//! client yet, so the command keeps its own: `Complete` only, the one call the evaluation makes).

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::{ModelService, ModelServiceClient};
use goap_llm as llm;
use serde_json::{Map, Value};

/// Client completes through the model gateway, as the user of its bearer token.
pub struct Client {
    rpc: ModelServiceClient,
}

impl Client {
    /// A client of the gateway at `base_url`, sending `token` (when not empty) as a bearer token.
    pub fn new(http: reqwest::Client, base_url: &str, token: &str) -> Self {
        let mut b = goap_connect::Client::builder(base_url).http(http);
        if !token.is_empty() {
            b = b.header("authorization", &format!("Bearer {token}"));
        }
        Client {
            rpc: ModelServiceClient::new(b.build()),
        }
    }
}

#[async_trait]
impl llm::Client for Client {
    async fn complete(&self, ctx: &Ctx, req: llm::Request) -> Result<llm::Response, llm::Error> {
        let input = pb::CompleteRequest {
            model: req.model,
            system: req.system,
            max_tokens: req.max_tokens as i32,
            json: req.json,
            cache: req.cache,
            meta: Some(meta_to_pb(ctx)),
            messages: messages_to_pb(&req.messages),
            tools: tools_to_pb(&req.tools),
        };
        let r = self
            .rpc
            .complete(ctx, goap_connect::Request::new(input))
            .await
            .map_err(|e| llm::Error::Other(Box::new(e)))?
            .into_inner();
        let mut out = llm::Response {
            text: r.text,
            provider: r.provider,
            model: r.model,
            stop_reason: r.stop_reason,
            tool_calls: calls_from_pb(&r.tool_calls),
            behaviors: r.behaviors,
            behavior_tokens: r.behavior_tokens as i64,
            behaviors_estimated: r.behaviors_estimated,
            ..Default::default()
        };
        if let Some(u) = r.usage {
            out.usage = llm::Usage {
                input_tokens: u.input_tokens as i64,
                output_tokens: u.output_tokens as i64,
                cache_read_tokens: u.cache_read_tokens as i64,
                cache_write_tokens: u.cache_write_tokens as i64,
            };
        }
        Ok(out)
    }
}

/// The declaration of the calls made with ctx, for the ledger of the gateway (ADR 0089).
fn meta_to_pb(ctx: &Ctx) -> pb::CallMeta {
    let m = llm::meta_from(ctx);
    pb::CallMeta {
        source: m.source,
        conversation_id: m.conversation_id,
        process_id: m.process_id,
        change_id: m.change_id,
        step: m.step as i32,
        action: m.action,
        agent: m.agent,
        call: m.call as i32,
    }
}

/// The JSON text of an object, empty for an empty one (Go's nil map).
fn json_text(m: &Map<String, Value>) -> String {
    if m.is_empty() {
        return String::new();
    }
    serde_json::to_string(m).unwrap_or_default()
}

fn json_object(s: &str) -> Map<String, Value> {
    if s.is_empty() {
        return Map::new();
    }
    serde_json::from_str(s).unwrap_or_default()
}

fn calls_to_pb(calls: &[llm::ToolCall]) -> Vec<pb::ToolCall> {
    calls
        .iter()
        .map(|c| pb::ToolCall {
            id: c.id.clone(),
            name: c.name.clone(),
            arguments_json: json_text(&c.arguments),
        })
        .collect()
}

fn calls_from_pb(calls: &[pb::ToolCall]) -> Vec<llm::ToolCall> {
    calls
        .iter()
        .map(|c| llm::ToolCall {
            id: c.id.clone(),
            name: c.name.clone(),
            arguments: json_object(&c.arguments_json),
        })
        .collect()
}

fn messages_to_pb(msgs: &[llm::Message]) -> Vec<pb::Message> {
    msgs.iter()
        .map(|m| pb::Message {
            role: m.role.clone(),
            content: m.content.clone(),
            tool_calls: calls_to_pb(&m.tool_calls),
            tool_results: m
                .tool_results
                .iter()
                .map(|r| pb::ToolResult {
                    call_id: r.call_id.clone(),
                    name: r.name.clone(),
                    content: r.content.clone(),
                    is_error: r.is_error,
                })
                .collect(),
        })
        .collect()
}

fn tools_to_pb(tools: &[llm::ToolSpec]) -> Vec<pb::ToolSpec> {
    tools
        .iter()
        .map(|t| pb::ToolSpec {
            name: t.name.clone(),
            description: t.description.clone(),
            input_schema_json: json_text(&t.input_schema),
        })
        .collect()
}
