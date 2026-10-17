//! The providers of the gateway: Anthropic (the Messages API, retrying on its own like the official SDK),
//! OpenAI-compatible endpoints and the fake provider.

use std::time::Duration;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_llm as llm;
use serde::Deserialize;
use serde_json::{Map, Value, json};

use crate::embed::Embedder;
use crate::protocols::{default_http, error_body, read_body, send};
use crate::retry::{DEFAULT_RETRIES, SdkError, random_upto, status_error_of, status_text};
use crate::router::Provider;

pub(crate) const JSON_INSTRUCTION: &str = "\n\nRespond with a single JSON object and nothing else.";

const MAX_BODY: usize = 8 << 20;

// ---- Anthropic -------------------------------------------------------------

/// The base URL of the Messages API.
pub const ANTHROPIC_BASE: &str = "https://api.anthropic.com";

/// The retries of the provider on a transient failure (it honours Retry-After), DEFAULT_RETRIES + 1 like the
/// router's own (retry.rs), which never retries an SdkError.
pub(crate) const ANTHROPIC_RETRIES: i64 = DEFAULT_RETRIES + 1;

/// The beta of the server-side fallback.
pub const ANTHROPIC_BETA_FALLBACK: &str = "server-side-fallback-2026-07-01";

/// Anthropic calls the Claude Messages API.
#[derive(Debug, Clone)]
pub struct Anthropic {
    pub api_key: String,
    pub base_url: String,
    pub http: reqwest::Client,
    pub retries: i64,
}

impl Anthropic {
    /// NewAnthropic returns a Claude provider.
    pub fn new(api_key: &str, base_url: &str) -> Anthropic {
        Anthropic {
            api_key: api_key.to_string(),
            base_url: if base_url.is_empty() {
                ANTHROPIC_BASE.to_string()
            } else {
                base_url.to_string()
            },
            http: default_http(),
            retries: ANTHROPIC_RETRIES,
        }
    }

    /// The request body of a completion.
    pub(crate) fn body(model: &str, req: &llm::Request) -> Value {
        let max_tokens = if req.max_tokens <= 0 {
            16000
        } else {
            req.max_tokens
        };
        let system = format!("{}{}", req.system, json_instruction_of(req));
        let mut body = Map::new();
        body.insert("model".into(), json!(model));
        body.insert("max_tokens".into(), json!(max_tokens));
        body.insert("fallbacks".into(), json!("default"));
        if !system.is_empty() {
            let mut block = json!({"type": "text", "text": system});
            if req.cache {
                block["cache_control"] = json!({"type": "ephemeral"});
            }
            body.insert("system".into(), json!([block]));
        }
        if !req.tools.is_empty() {
            body.insert(
                "tools".into(),
                Value::Array(req.tools.iter().map(anthropic_tool).collect()),
            );
        }
        let n = req.messages.len();
        let mut msgs = Vec::with_capacity(n);
        for (i, m) in req.messages.iter().enumerate() {
            let mut blocks = anthropic_blocks(m);
            if req.cache && i == n - 1 {
                // two breakpoints: the system text (shared by every round and turn) and the last block of the
                // conversation (the prefix the next round repeats)
                if let Some(last) = blocks.last_mut() {
                    last["cache_control"] = json!({"type": "ephemeral"});
                }
            }
            let role = if m.role == "assistant" {
                "assistant"
            } else {
                "user"
            };
            msgs.push(json!({"role": role, "content": blocks}));
        }
        body.insert("messages".into(), Value::Array(msgs));
        Value::Object(body)
    }

    /// The wait before the attempt-th retry (0-based), as the SDK computes it: the server's Retry-After when it
    /// asks for less than a minute, else 0.5s doubled per attempt up to 8s, with jitter.
    fn backoff(attempt: u32, headers: &http::HeaderMap) -> Duration {
        if let Some(ms) = headers
            .get("retry-after-ms")
            .and_then(|v| v.to_str().ok())
            .and_then(|s| s.trim().parse::<f64>().ok())
            && (0.0..60_000.0).contains(&ms)
        {
            return Duration::from_secs_f64(ms / 1000.0);
        }
        if let Some(s) = headers.get("retry-after").and_then(|v| v.to_str().ok()) {
            if let Ok(n) = s.trim().parse::<f64>()
                && (0.0..60.0).contains(&n)
            {
                return Duration::from_secs_f64(n);
            }
            if let Ok(t) = chrono::DateTime::parse_from_rfc2822(s) {
                let left = t.with_timezone(&chrono::Utc) - chrono::Utc::now();
                if let Ok(d) = left.to_std()
                    && d < Duration::from_secs(60)
                {
                    return d;
                }
            }
        }
        let d = Duration::from_millis(500)
            .saturating_mul(1u32.checked_shl(attempt).unwrap_or(u32::MAX))
            .min(Duration::from_secs(8));
        let jitter = Duration::from_nanos(random_upto((d / 4).as_nanos() as u64));
        d - jitter
    }
}

fn anthropic_retryable(code: u16, headers: &http::HeaderMap) -> bool {
    match headers.get("x-should-retry").and_then(|v| v.to_str().ok()) {
        Some("true") => return true,
        Some("false") => return false,
        _ => {}
    }
    matches!(code, 408 | 409 | 429) || code >= 500
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct AnthropicUsage {
    input_tokens: i64,
    output_tokens: i64,
    cache_read_input_tokens: i64,
    cache_creation_input_tokens: i64,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct AnthropicBlock {
    #[serde(rename = "type")]
    kind: String,
    text: String,
    id: String,
    name: String,
    input: Value,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct AnthropicMessage {
    model: String,
    stop_reason: String,
    content: Vec<AnthropicBlock>,
    usage: AnthropicUsage,
}

#[async_trait]
impl Provider for Anthropic {
    /// Complete implements Provider. Refusals are re-served by the server-side fallback model when the primary
    /// model declines.
    async fn complete(
        &self,
        ctx: &Ctx,
        model: &str,
        req: llm::Request,
    ) -> Result<llm::Response, llm::Error> {
        let body = serde_json::to_vec(&Anthropic::body(model, &req))
            .map_err(|e| llm::Error::Other(Box::new(e)))?;
        let u = format!(
            "{}/v1/messages?beta=true",
            self.base_url.trim_end_matches('/')
        );
        let mut attempt: u32 = 0;
        let (status, raw) = loop {
            let rb = self
                .http
                .post(&u)
                .header("Content-Type", "application/json")
                .header("x-api-key", &self.api_key)
                .header("anthropic-version", "2023-06-01")
                .header("anthropic-beta", ANTHROPIC_BETA_FALLBACK)
                .header("X-Stainless-Retry-Count", attempt.to_string())
                .body(body.clone());
            let retries_left = i64::from(attempt) < self.retries;
            let resp = match send(ctx, rb).await {
                Ok(r) => r,
                Err(e) => {
                    let transient = matches!(&e, llm::Error::Other(b)
                        if b.downcast_ref::<reqwest::Error>().is_some_and(|r| r.is_connect() || r.is_timeout() || r.is_request()));
                    if transient && retries_left {
                        let wait = Anthropic::backoff(attempt, &http::HeaderMap::new());
                        tokio::select! {
                            e = ctx.done() => return Err(llm::Error::Ctx(e)),
                            _ = tokio::time::sleep(wait) => {}
                        }
                        attempt += 1;
                        continue;
                    }
                    return Err(e.wrap(format!("POST \"{u}\"")));
                }
            };
            let status = resp.status().as_u16();
            let headers = resp.headers().clone();
            if status >= 400 && anthropic_retryable(status, &headers) && retries_left {
                let _ = read_body(ctx, resp, MAX_BODY).await;
                let wait = Anthropic::backoff(attempt, &headers);
                tokio::select! {
                    e = ctx.done() => return Err(llm::Error::Ctx(e)),
                    _ = tokio::time::sleep(wait) => {}
                }
                attempt += 1;
                continue;
            }
            let raw = read_body(ctx, resp, MAX_BODY).await?;
            break (status, raw);
        };
        if status >= 400 {
            let msg = format!(
                "POST \"{u}\": {status} {} {}",
                status_text(status),
                error_body(&raw)
            );
            return Err(llm::Error::Other(Box::new(SdkError::new(status, msg))));
        }
        let msg: AnthropicMessage =
            serde_json::from_slice(&raw).map_err(|e| llm::Error::Other(Box::new(e)))?;
        if msg.stop_reason == "refusal" {
            return Err(llm::Error::Refusal);
        }
        let mut text = String::new();
        let mut calls = Vec::new();
        for b in &msg.content {
            match b.kind.as_str() {
                "text" => text.push_str(&b.text),
                "tool_use" => calls.push(llm::ToolCall {
                    id: b.id.clone(),
                    name: b.name.clone(),
                    arguments: args_of(&b.input),
                }),
                _ => {}
            }
        }
        let u = &msg.usage;
        let mut stop = stop_reason(&msg.stop_reason, "end_turn", "max_tokens");
        if stop == "tool_use" {
            stop = llm::STOP_TOOL_USE.to_string();
        }
        Ok(llm::Response {
            text,
            tool_calls: calls,
            provider: "anthropic".into(),
            model: msg.model.clone(),
            stop_reason: stop,
            usage: llm::Usage {
                input_tokens: u.input_tokens
                    + u.cache_read_input_tokens
                    + u.cache_creation_input_tokens,
                output_tokens: u.output_tokens,
                cache_read_tokens: u.cache_read_input_tokens,
                cache_write_tokens: u.cache_creation_input_tokens,
            },
            ..Default::default()
        })
    }

    /// NativeTools: Claude calls tools through its API.
    fn native_tools(&self) -> bool {
        true
    }
}

/// A tool of the Messages API.
pub(crate) fn anthropic_tool(t: &llm::ToolSpec) -> Value {
    let mut schema = Map::new();
    schema.insert("type".into(), json!("object"));
    let mut props = None;
    for (k, v) in &t.input_schema {
        match k.as_str() {
            "type" => {}
            "properties" => props = Some(v.clone()),
            "required" => {
                let req: Vec<Value> = v
                    .as_array()
                    .map(|a| a.iter().filter(|r| r.is_string()).cloned().collect())
                    .unwrap_or_default();
                if !req.is_empty() {
                    schema.insert("required".into(), Value::Array(req));
                }
            }
            _ => {
                schema.insert(k.clone(), v.clone());
            }
        }
    }
    schema.insert(
        "properties".into(),
        match props {
            Some(p) if !p.is_null() => p,
            _ => json!({}),
        },
    );
    let mut tool = Map::new();
    tool.insert("name".into(), json!(t.name));
    tool.insert("input_schema".into(), Value::Object(schema));
    if !t.description.is_empty() {
        tool.insert("description".into(), json!(t.description));
    }
    Value::Object(tool)
}

/// The content blocks of a turn: the tool results first (the API wants them at the start of the user turn that
/// follows the calls), the text, then the tool calls.
pub(crate) fn anthropic_blocks(m: &llm::Message) -> Vec<Value> {
    let mut blocks = Vec::new();
    for r in &m.tool_results {
        blocks.push(json!({
            "type": "tool_result",
            "tool_use_id": r.call_id,
            "content": [{"type": "text", "text": r.content}],
            "is_error": r.is_error,
        }));
    }
    if !m.content.is_empty() || (m.tool_calls.is_empty() && m.tool_results.is_empty()) {
        blocks.push(json!({"type": "text", "text": m.content}));
    }
    for c in &m.tool_calls {
        blocks.push(json!({"type": "tool_use", "id": c.id, "name": c.name, "input": c.arguments}));
    }
    blocks
}

/// argsOf reads the arguments of a call (a decoded JSON object, or its raw text).
pub fn args_of(v: &Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m.clone(),
        Value::String(s) => match serde_json::from_str::<Value>(s) {
            Ok(Value::Object(m)) => m,
            _ => Map::new(),
        },
        _ => Map::new(),
    }
}

/// jsonInstructionOf is the instruction asking for a JSON answer, worded for a call that may also call tools.
pub(crate) fn json_instruction_of(req: &llm::Request) -> &'static str {
    if !req.json {
        ""
    } else if !req.tools.is_empty() {
        "\n\nWhen you answer without calling a tool, respond with a single JSON object and nothing else."
    } else {
        JSON_INSTRUCTION
    }
}

/// stopReason normalises a provider's stop reason: its words for a natural end and for the output token limit
/// become STOP_END and STOP_MAX_TOKENS, any other is kept as is.
pub(crate) fn stop_reason(got: &str, end: &str, max_tokens: &str) -> String {
    if got == end {
        llm::STOP_END.to_string()
    } else if got == max_tokens {
        llm::STOP_MAX_TOKENS.to_string()
    } else {
        got.to_string()
    }
}

// ---- OpenAI-compatible (Ollama, vLLM, Mistral, OpenAI…) -------------------

/// OpenAICompatible calls a /chat/completions endpoint.
#[derive(Debug, Clone, Default)]
pub struct OpenAICompatible {
    pub base_url: String,
    pub api_key: String,
    pub http: Option<reqwest::Client>,
}

#[async_trait]
impl Provider for OpenAICompatible {
    async fn complete(
        &self,
        ctx: &Ctx,
        model: &str,
        req: llm::Request,
    ) -> Result<llm::Response, llm::Error> {
        let mut body = Map::new();
        body.insert("model".into(), json!(model));
        let mut msgs = Vec::new();
        if !req.system.is_empty() {
            msgs.push(json!({"role": "system", "content": req.system}));
        }
        for m in &req.messages {
            msgs.extend(openai_messages(m));
        }
        body.insert("messages".into(), Value::Array(msgs));
        if !req.tools.is_empty() {
            let tools: Vec<Value> = req
                .tools
                .iter()
                .map(|t| {
                    let params = if t.input_schema.is_empty() {
                        json!({"type": "object", "properties": {}})
                    } else {
                        Value::Object(t.input_schema.clone())
                    };
                    json!({"type": "function", "function": {"name": t.name, "description": t.description, "parameters": params}})
                })
                .collect();
            body.insert("tools".into(), Value::Array(tools));
        }
        if req.max_tokens > 0 {
            body.insert("max_tokens".into(), json!(req.max_tokens));
        }
        if req.json {
            body.insert("response_format".into(), json!({"type": "json_object"}));
        }
        let data =
            serde_json::to_vec(&Value::Object(body)).map_err(|e| llm::Error::Other(Box::new(e)))?;
        let hc = self.http.clone().unwrap_or_else(default_http);
        let mut rb = hc
            .post(format!(
                "{}/chat/completions",
                self.base_url.trim_end_matches('/')
            ))
            .header("Content-Type", "application/json")
            .body(data);
        if !self.api_key.is_empty() {
            rb = rb.header("Authorization", format!("Bearer {}", self.api_key));
        }
        let resp = send(ctx, rb).await?;
        let status = resp.status().as_u16();
        let headers = resp.headers().clone();
        let raw = read_body(ctx, resp, MAX_BODY).await?;
        if status != 200 {
            return Err(status_error_of(status, &headers, &error_body(&raw))
                .into_llm()
                .wrap("chat/completions"));
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Function {
            name: String,
            arguments: Option<String>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Call {
            id: String,
            function: Function,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Msg {
            content: Option<String>,
            tool_calls: Option<Vec<Call>>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Choice {
            message: Msg,
            finish_reason: Option<String>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Details {
            cached_tokens: i64,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Usage {
            prompt_tokens: i64,
            completion_tokens: i64,
            prompt_tokens_details: Option<Details>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Out {
            model: String,
            choices: Vec<Choice>,
            usage: Option<Usage>,
        }
        let out: Out = serde_json::from_slice(&raw).map_err(|e| llm::Error::Other(Box::new(e)))?;
        let Some(choice) = out.choices.into_iter().next() else {
            return Err(llm::Error::Message("chat/completions: no choice".into()));
        };
        let usage = out.usage.unwrap_or_default();
        let mut r = llm::Response {
            text: choice.message.content.unwrap_or_default(),
            model: out.model,
            stop_reason: stop_reason(&choice.finish_reason.unwrap_or_default(), "stop", "length"),
            usage: llm::Usage {
                input_tokens: usage.prompt_tokens,
                output_tokens: usage.completion_tokens,
                cache_read_tokens: usage
                    .prompt_tokens_details
                    .map(|d| d.cached_tokens)
                    .unwrap_or(0),
                cache_write_tokens: 0,
            },
            ..Default::default()
        };
        for (i, c) in choice
            .message
            .tool_calls
            .unwrap_or_default()
            .into_iter()
            .enumerate()
        {
            let id = if c.id.is_empty() {
                format!("call_{}", i + 1)
            } else {
                c.id
            };
            r.tool_calls.push(llm::ToolCall {
                id,
                name: c.function.name,
                arguments: args_of(&Value::String(c.function.arguments.unwrap_or_default())),
            });
        }
        if r.stop_reason == "tool_calls" || !r.tool_calls.is_empty() {
            r.stop_reason = llm::STOP_TOOL_USE.to_string();
        }
        Ok(r)
    }

    /// NativeTools: /chat/completions takes "tools". A server whose model has none refuses them, and the gateway
    /// then falls back on its text protocol (tools.rs).
    fn native_tools(&self) -> bool {
        true
    }

    fn embedder(&self) -> Option<&dyn Embedder> {
        Some(self)
    }
}

/// The chat messages of a turn: an assistant turn with its tool_calls, the results of a user turn as "tool"
/// messages, then its text.
pub(crate) fn openai_messages(m: &llm::Message) -> Vec<Value> {
    let mut out = Vec::new();
    if m.role == "assistant" && !m.tool_calls.is_empty() {
        let calls: Vec<Value> = m
            .tool_calls
            .iter()
            .map(|c| {
                let args = serde_json::to_string(&c.arguments).unwrap_or_else(|_| "{}".into());
                json!({"id": c.id, "type": "function", "function": {"name": c.name, "arguments": args}})
            })
            .collect();
        let content = if m.content.is_empty() {
            Value::Null
        } else {
            json!(m.content)
        };
        out.push(json!({"role": "assistant", "tool_calls": calls, "content": content}));
        return out;
    }
    for r in &m.tool_results {
        let content = if r.is_error {
            format!("error: {}", r.content)
        } else {
            r.content.clone()
        };
        out.push(json!({"role": "tool", "tool_call_id": r.call_id, "content": content}));
    }
    if !m.content.is_empty() || m.tool_results.is_empty() {
        out.push(json!({"role": m.role, "content": m.content}));
    }
    out
}

// ---- Fake -------------------------------------------------------------------

/// Fake answers without calling any model: an empty item list for JSON requests (actions then fail their effects
/// and the planner falls back on human actions), an echo otherwise. For dev without API keys.
#[derive(Debug, Clone, Copy, Default)]
pub struct Fake;

#[async_trait]
impl Provider for Fake {
    async fn complete(
        &self,
        _ctx: &Ctx,
        model: &str,
        req: llm::Request,
    ) -> Result<llm::Response, llm::Error> {
        let mut input_tokens = estimate_tokens(&req.system);
        for m in &req.messages {
            input_tokens += estimate_tokens(&m.content);
        }
        let text = if req.json {
            r#"{"items":[],"candidates":[]}"#.to_string()
        } else {
            format!(
                "echo: {}",
                req.messages
                    .last()
                    .map(|m| m.content.as_str())
                    .unwrap_or("")
            )
        };
        Ok(llm::Response {
            usage: llm::Usage {
                input_tokens,
                output_tokens: estimate_tokens(&text),
                ..Default::default()
            },
            text,
            provider: "fake".into(),
            model: model.to_string(),
            ..Default::default()
        })
    }

    fn embedder(&self) -> Option<&dyn Embedder> {
        Some(self)
    }
}

/// A rough (~4 chars/token) size for the Fake provider, whose whole point is to run without a real model: it lets a
/// dev running on goap-dev see the actual context size pushed to the agent without a real completion.
pub(crate) fn estimate_tokens(s: &str) -> i64 {
    s.len().div_ceil(4) as i64
}
