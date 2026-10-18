//! The completion contract: messages, requests, responses, the clients and the embedders.

use std::{future::Future, sync::Arc};

use async_trait::async_trait;
use goap_ctx::Ctx;
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use serde_json::{Map, Value};

use crate::{
    observe::Error,
    serde_util::{go_marshal, is_false, is_zero, null_default},
};

/// Message is a conversation turn. An assistant turn may carry the tool calls the model made; the user turn after
/// it carries their results (ADR 0098).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Message {
    /// user | assistant
    pub role: String,
    pub content: String,
    #[serde(
        rename = "toolCalls",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tool_calls: Vec<ToolCall>,
    #[serde(
        rename = "toolResults",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tool_results: Vec<ToolResult>,
}

/// ToolSpec declares a tool the model may call: its name, what it does and the JSON schema of its arguments. A name
/// may hold any character: the gateway gives the provider a name it accepts and maps it back (ADR 0098).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolSpec {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(
        rename = "inputSchema",
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub input_schema: Map<String, Value>,
}

/// ToolCall is a call of a tool the model made. ID pairs it with its result.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub arguments: Map<String, Value>,
}

/// ToolResult is the result of a tool call given back to the model: Content is its text (a JSON document, usually),
/// IsError says the call failed and Content is why.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ToolResult {
    #[serde(rename = "callId")]
    pub call_id: String,
    pub name: String,
    pub content: String,
    #[serde(rename = "isError", skip_serializing_if = "is_false")]
    pub is_error: bool,
}

impl Message {
    /// Transcript renders the turn as text: its content, then its tool calls or results (the exchange of a call keeps
    /// it so, ADR 0059, 0089).
    pub fn transcript(&self) -> String {
        let mut out = self.content.clone();
        let mut add = |label: &str, json: String| {
            if !out.is_empty() {
                out.push('\n');
            }
            out.push_str(label);
            out.push(' ');
            out.push_str(&json);
        };
        if !self.tool_calls.is_empty() {
            add("[tool calls]", go_marshal(&self.tool_calls));
        }
        if !self.tool_results.is_empty() {
            add("[tool results]", go_marshal(&self.tool_results));
        }
        out
    }
}

/// Request is a completion request. Model is an alias resolved by the gateway
/// ("default", "fast", …) or an explicit "provider/model".
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Request {
    pub model: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub system: String,
    #[serde(deserialize_with = "null_default")]
    pub messages: Vec<Message>,
    #[serde(rename = "maxTokens", skip_serializing_if = "is_zero")]
    pub max_tokens: i64,
    /// JSON asks the provider for a single JSON object answer.
    #[serde(skip_serializing_if = "is_false")]
    pub json: bool,
    /// Tools are the tools the model may call (ADR 0098): a provider with native tool calling gets them as such, any
    /// other through a text protocol the gateway runs for it. The calls come back in Response.ToolCalls.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tools: Vec<ToolSpec>,
    /// Cache says the caller will send the same prefix again (a tool loop, a conversation): a provider that caches
    /// prompts only on request marks the system text and the messages as a cache prefix. Providers that cache on their
    /// own ignore it. Writing a cache entry costs more than a plain read, so a single-shot call leaves it unset.
    #[serde(skip_serializing_if = "is_false")]
    pub cache: bool,
}

/// Usage reports token consumption. InputTokens is the whole prompt, cached or not; CacheReadTokens and
/// CacheWriteTokens are the parts of it read from and written to the provider's prompt cache.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Usage {
    #[serde(rename = "inputTokens")]
    pub input_tokens: i64,
    #[serde(rename = "outputTokens")]
    pub output_tokens: i64,
    #[serde(rename = "cacheReadTokens", skip_serializing_if = "is_zero")]
    pub cache_read_tokens: i64,
    #[serde(rename = "cacheWriteTokens", skip_serializing_if = "is_zero")]
    pub cache_write_tokens: i64,
}

/// Response is a completion result.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Response {
    pub text: String,
    pub provider: String,
    pub model: String,
    pub usage: Usage,
    /// ToolCalls are the tools the model asks to call (Request.Tools); Text is then what it said with them, if anything.
    #[serde(
        rename = "toolCalls",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub tool_calls: Vec<ToolCall>,
    /// StopReason is why the model stopped, normalised: StopEnd, StopMaxTokens, or the provider's own word.
    #[serde(rename = "stopReason", skip_serializing_if = "String::is_empty")]
    pub stop_reason: String,
    /// Behaviors names the global behaviours the gateway added to the instructions of the call (ADR 0093; a name with a
    /// leading "!" was dropped by the cap), and BehaviorTokens estimates the tokens they added. The caller's request is
    /// never changed: the gateway reports what it added here.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub behaviors: Vec<String>,
    #[serde(rename = "behaviorTokens", skip_serializing_if = "is_zero")]
    pub behavior_tokens: i64,
    /// BehaviorsEstimated says BehaviorTokens is the byte estimate, not a cost measured on the model.
    #[serde(rename = "behaviorsEstimated", skip_serializing_if = "is_false")]
    pub behaviors_estimated: bool,
}

/// Normalised stop reason of Response.StopReason: the model ended its answer.
pub const STOP_END: &str = "end";
/// Normalised stop reason of Response.StopReason: the answer was cut by the output token limit.
pub const STOP_MAX_TOKENS: &str = "max_tokens";

/// StopToolUse is the stop reason of an answer that calls tools.
pub const STOP_TOOL_USE: &str = "tool_use";

impl Response {
    /// Transcript renders the answer as text: what the model said, then the tools it calls.
    pub fn transcript(&self) -> String {
        Message {
            role: "assistant".into(),
            content: self.text.clone(),
            tool_calls: self.tool_calls.clone(),
            tool_results: Vec::new(),
        }
        .transcript()
    }

    /// Truncated says the answer was cut by the output token limit.
    pub fn truncated(&self) -> bool {
        self.stop_reason == STOP_MAX_TOKENS
    }
}

/// Client completes prompts.
#[async_trait]
pub trait Client: Send + Sync {
    async fn complete(&self, ctx: &Ctx, req: Request) -> Result<Response, Error>;
}

/// ClientFunc adapts an async function to Client. The function gets its own copy of the context.
pub struct ClientFunc<F>(pub F);

impl<F, Fut> ClientFunc<F>
where
    F: Fn(Ctx, Request) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<Response, Error>> + Send + 'static,
{
    /// The function as a shared Client.
    pub fn shared(f: F) -> Arc<dyn Client> {
        Arc::new(ClientFunc(f))
    }
}

#[async_trait]
impl<F, Fut> Client for ClientFunc<F>
where
    F: Fn(Ctx, Request) -> Fut + Send + Sync,
    Fut: Future<Output = Result<Response, Error>> + Send,
{
    async fn complete(&self, ctx: &Ctx, req: Request) -> Result<Response, Error> {
        (self.0)(ctx.clone(), req).await
    }
}

/// DecodeJSON extracts the first JSON object of a model answer (tolerating
/// markdown fences and surrounding prose) and decodes it.
pub fn decode_json<T: DeserializeOwned>(text: &str) -> Result<T, Error> {
    let start = text.find(['{', '[']).ok_or(Error::NoJson)?;
    let mut stream = serde_json::Deserializer::from_str(&text[start..]).into_iter::<T>();
    match stream.next() {
        Some(Ok(v)) => Ok(v),
        Some(Err(e)) => Err(Error::Decode(e)),
        None => serde_json::from_str(&text[start..]).map_err(Error::Decode),
    }
}

/// EmbedRequest asks for the embedding of texts. Model is an alias ("embed" when empty) or "provider/model".
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct EmbedRequest {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub model: String,
    #[serde(deserialize_with = "null_default")]
    pub texts: Vec<String>,
}

/// EmbedResponse holds one vector per text, in order.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct EmbedResponse {
    #[serde(deserialize_with = "null_default")]
    pub vectors: Vec<Vec<f32>>,
    pub provider: String,
    pub model: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub tokens: i64,
}

/// Embedder embeds texts (implemented by the model gateway).
#[async_trait]
pub trait Embedder: Send + Sync {
    async fn embed(&self, ctx: &Ctx, req: EmbedRequest) -> Result<EmbedResponse, Error>;
}

/// EmbedAlias is the alias of the embedding model of the platform.
pub const EMBED_ALIAS: &str = "embed";

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn json_forms_keep_the_go_names_and_omissions() {
        let r = Response {
            text: "hi".into(),
            usage: Usage {
                input_tokens: 3,
                ..Default::default()
            },
            ..Default::default()
        };
        assert_eq!(
            serde_json::to_value(&r).unwrap(),
            json!({"text":"hi","provider":"","model":"","usage":{"inputTokens":3,"outputTokens":0}})
        );
        let m: Message =
            serde_json::from_value(json!({"role":"user","content":"x","toolCalls":null})).unwrap();
        assert!(m.tool_calls.is_empty());
        let req = Request {
            model: "default".into(),
            ..Default::default()
        };
        assert_eq!(
            serde_json::to_value(&req).unwrap(),
            json!({"model":"default","messages":[]})
        );
    }

    #[test]
    fn transcript() {
        let mut args = Map::new();
        args.insert("q".into(), json!("<a>"));
        let r = Response {
            text: "calling".into(),
            tool_calls: vec![ToolCall {
                id: "1".into(),
                name: "s".into(),
                arguments: args,
            }],
            ..Default::default()
        };
        assert_eq!(
            r.transcript(),
            "calling\n[tool calls] [{\"id\":\"1\",\"name\":\"s\",\"arguments\":{\"q\":\"\\u003ca\\u003e\"}}]"
        );
        let m = Message {
            tool_results: vec![ToolResult {
                call_id: "1".into(),
                name: "s".into(),
                content: "ok".into(),
                is_error: false,
            }],
            ..Default::default()
        };
        assert_eq!(
            m.transcript(),
            "[tool results] [{\"callId\":\"1\",\"name\":\"s\",\"content\":\"ok\"}]"
        );
        assert!(
            Response {
                stop_reason: STOP_MAX_TOKENS.into(),
                ..Default::default()
            }
            .truncated()
        );
    }

    #[test]
    fn decode_json_tolerates_fences_and_prose() {
        let v: Value = decode_json("Here:\n```json\n{\"a\": 1}\n```\nmore").unwrap();
        assert_eq!(v, json!({"a": 1}));
        let v: Vec<i64> = decode_json("x [1,2] y").unwrap();
        assert_eq!(v, vec![1, 2]);
        assert_eq!(
            decode_json::<Value>("nothing").unwrap_err().to_string(),
            "no JSON in model answer"
        );
        assert!(
            decode_json::<Value>("{oops")
                .unwrap_err()
                .to_string()
                .starts_with("decode model answer: ")
        );
    }

    #[tokio::test]
    async fn client_func() {
        let c = ClientFunc::shared(|_ctx, req: Request| async move {
            Ok(Response {
                text: req.model,
                ..Default::default()
            })
        });
        let r = c
            .complete(
                &Ctx::background(),
                Request {
                    model: "m".into(),
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        assert_eq!(r.text, "m");
    }
}
