//! The wire protocols of the providers (the plug-in point of the gateway), the provider presets, the HTTP helper,
//! the listing of the models and the Google Gemini provider.

use std::collections::HashMap;
use std::sync::{Arc, LazyLock};
use std::time::Duration;

use async_trait::async_trait;
use futures::future::BoxFuture;
use goap_ctx::Ctx;
use goap_llm as llm;
use parking_lot::RwLock;
use serde::Deserialize;
use serde::de::DeserializeOwned;
use serde_json::{Map, Value, json};

use crate::embed::{Embedder, path_escape};
use crate::providers::{
    Anthropic, Fake, OpenAICompatible, args_of, json_instruction_of, stop_reason,
};
use crate::retry::status_error_of;
use crate::router::Provider;

/// ProviderSpec is the runtime configuration of one provider.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProviderSpec {
    pub name: String,
    pub kind: String,
    pub protocol: String,
    pub base_url: String,
    pub api_key: String,
}

/// ModelInfo is a model offered by a provider.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ModelInfo {
    pub id: String,
    pub display_name: String,
}

/// Builds the completion client of a provider.
pub type NewProvider = Arc<dyn Fn(ProviderSpec) -> Result<Arc<dyn Provider>, String> + Send + Sync>;

/// Asks a provider for its models.
pub type ListModels = Arc<
    dyn Fn(
            Ctx,
            reqwest::Client,
            ProviderSpec,
        ) -> BoxFuture<'static, Result<Vec<ModelInfo>, llm::Error>>
        + Send
        + Sync,
>;

/// Protocol is a wire protocol to talk to a provider. Protocols are the plug-in point of the gateway: register one
/// with register_protocol and every provider kind (or custom provider) can use it.
#[derive(Clone)]
pub struct Protocol {
    pub id: String,
    pub label: String,
    /// New builds the completion client of a provider.
    pub new: NewProvider,
    /// List asks the provider for its models.
    pub list: ListModels,
}

/// Kind is a provider preset: a protocol with its usual endpoint.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Kind {
    pub id: String,
    pub label: String,
    pub protocol: String,
    pub default_base_url: String,
    pub key_required: bool,
    pub description: String,
}

struct Registry {
    protocols: HashMap<String, Protocol>,
    kinds: HashMap<String, Kind>,
}

static REGISTRY: LazyLock<RwLock<Registry>> = LazyLock::new(|| RwLock::new(builtins()));

/// RegisterProtocol adds (or replaces) a protocol.
pub fn register_protocol(p: Protocol) {
    REGISTRY.write().protocols.insert(p.id.clone(), p);
}

/// RegisterKind adds (or replaces) a provider preset.
pub fn register_kind(k: Kind) {
    REGISTRY.write().kinds.insert(k.id.clone(), k);
}

/// LookupProtocol returns a registered protocol.
pub fn lookup_protocol(id: &str) -> Option<Protocol> {
    REGISTRY.read().protocols.get(id).cloned()
}

/// LookupKind returns a registered provider preset.
pub fn lookup_kind(id: &str) -> Option<Kind> {
    REGISTRY.read().kinds.get(id).cloned()
}

/// Protocols lists the registered protocols, sorted by id.
pub fn protocols() -> Vec<Protocol> {
    let mut out: Vec<Protocol> = REGISTRY.read().protocols.values().cloned().collect();
    out.sort_by(|a, b| a.id.cmp(&b.id));
    out
}

/// Kinds lists the registered provider presets, sorted by label.
pub fn kinds() -> Vec<Kind> {
    let mut out: Vec<Kind> = REGISTRY.read().kinds.values().cloned().collect();
    out.sort_by(|a, b| a.label.cmp(&b.label));
    out
}

fn list_fn<F, Fut>(f: F) -> ListModels
where
    F: Fn(Ctx, reqwest::Client, ProviderSpec) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<Vec<ModelInfo>, llm::Error>> + Send + 'static,
{
    Arc::new(move |ctx, hc, s| Box::pin(f(ctx, hc, s)))
}

fn builtins() -> Registry {
    let mut protocols = HashMap::new();
    let mut add = |p: Protocol| {
        protocols.insert(p.id.clone(), p);
    };
    add(Protocol {
        id: "anthropic".into(),
        label: "Anthropic Messages API".into(),
        new: Arc::new(|s: ProviderSpec| {
            if s.api_key.is_empty() {
                return Err("an API key is required".to_string());
            }
            Ok(Arc::new(Anthropic::new(&s.api_key, &s.base_url)) as Arc<dyn Provider>)
        }),
        list: list_fn(|ctx, hc, s| async move { list_anthropic(&ctx, &hc, &s).await }),
    });
    add(Protocol {
        id: "openai".into(),
        label: "OpenAI-compatible (/chat/completions)".into(),
        new: Arc::new(|s: ProviderSpec| {
            if s.base_url.is_empty() {
                return Err("a base URL is required".to_string());
            }
            Ok(Arc::new(OpenAICompatible {
                base_url: s.base_url,
                api_key: s.api_key,
                http: None,
            }) as Arc<dyn Provider>)
        }),
        list: list_fn(|ctx, hc, s| async move { list_openai(&ctx, &hc, &s).await }),
    });
    add(Protocol {
        id: "gemini".into(),
        label: "Google Gemini (generateContent)".into(),
        new: Arc::new(|s: ProviderSpec| {
            if s.api_key.is_empty() {
                return Err("an API key is required".to_string());
            }
            Ok(Arc::new(Gemini {
                base_url: s.base_url,
                api_key: s.api_key,
                http: None,
            }) as Arc<dyn Provider>)
        }),
        list: list_fn(|ctx, hc, s| async move { list_gemini(&ctx, &hc, &s).await }),
    });
    add(Protocol {
        id: "fake".into(),
        label: "Fake (echo, no network)".into(),
        new: Arc::new(|_| Ok(Arc::new(Fake) as Arc<dyn Provider>)),
        list: list_fn(|_, _, _| async {
            Ok(vec![ModelInfo {
                id: "echo".into(),
                display_name: "Echo".into(),
            }])
        }),
    });
    let mut kinds = HashMap::new();
    let mut kind = |id: &str, label: &str, protocol: &str, base: &str, key: bool, desc: &str| {
        kinds.insert(
            id.to_string(),
            Kind {
                id: id.into(),
                label: label.into(),
                protocol: protocol.into(),
                default_base_url: base.into(),
                key_required: key,
                description: desc.into(),
            },
        );
    };
    kind(
        "anthropic",
        "Anthropic",
        "anthropic",
        "https://api.anthropic.com",
        true,
        "Claude models through the Messages API.",
    );
    kind(
        "mistral",
        "Mistral AI",
        "openai",
        "https://api.mistral.ai/v1",
        true,
        "Mistral models through their OpenAI-compatible API.",
    );
    kind(
        "google",
        "Google Gemini",
        "gemini",
        "https://generativelanguage.googleapis.com/v1beta",
        true,
        "Gemini models through the Generative Language API (Google AI Studio key).",
    );
    kind(
        "openai",
        "OpenAI",
        "openai",
        "https://api.openai.com/v1",
        true,
        "OpenAI models.",
    );
    kind(
        "openai-compatible",
        "OpenAI-compatible (custom)",
        "openai",
        "",
        false,
        "Ollama, vLLM, LiteLLM or any server exposing /chat/completions. The key is optional.",
    );
    kind(
        "fake",
        "Fake (echo)",
        "fake",
        "",
        false,
        "Answers without calling a model. For development.",
    );
    Registry { protocols, kinds }
}

// ---- HTTP helper ----------------------------------------------------------

const MAX_BODY: usize = 8 << 20;

/// The default client of a provider: a call may last.
pub(crate) fn default_http() -> reqwest::Client {
    reqwest::Client::builder()
        .timeout(Duration::from_secs(600))
        .build()
        .unwrap_or_default()
}

/// The longest prefix of s of at most n bytes on a character boundary (Go's `strings.ToValidUTF8(s[:n], "")`).
pub(crate) fn cut(s: &str, n: usize) -> &str {
    if s.len() <= n {
        return s;
    }
    let mut i = n;
    while !s.is_char_boundary(i) {
        i -= 1;
    }
    &s[..i]
}

/// The error text of a provider answer: trimmed, cut at 400 bytes.
pub(crate) fn error_body(raw: &[u8]) -> String {
    let msg = String::from_utf8_lossy(raw);
    let msg = msg.trim();
    if msg.len() > 400 {
        format!("{}…", cut(msg, 400))
    } else {
        msg.to_string()
    }
}

/// Sends a request, failing with the context; an error of the transport is the reqwest error.
pub(crate) async fn send(
    ctx: &Ctx,
    rb: reqwest::RequestBuilder,
) -> Result<reqwest::Response, llm::Error> {
    tokio::select! {
        e = ctx.done() => Err(llm::Error::Ctx(e)),
        r = rb.send() => r.map_err(|e| llm::Error::Other(Box::new(e))),
    }
}

/// Reads a body (at most `limit` bytes), failing with the context.
pub(crate) async fn read_body(
    ctx: &Ctx,
    resp: reqwest::Response,
    limit: usize,
) -> Result<Vec<u8>, llm::Error> {
    use futures::StreamExt;
    let mut stream = resp.bytes_stream();
    let mut out = Vec::new();
    loop {
        let next = tokio::select! {
            e = ctx.done() => return Err(llm::Error::Ctx(e)),
            n = stream.next() => n,
        };
        match next {
            None => break,
            Some(Ok(b)) => {
                let room = limit.saturating_sub(out.len());
                out.extend_from_slice(&b[..b.len().min(room)]);
            }
            Some(Err(e)) => return Err(llm::Error::Other(Box::new(e))),
        }
    }
    Ok(out)
}

/// A JSON call: a status other than 200 is a StatusError, the answer decoded into T.
pub(crate) async fn http_json<T: DeserializeOwned>(
    ctx: &Ctx,
    hc: Option<&reqwest::Client>,
    method: reqwest::Method,
    u: &str,
    headers: &[(&str, String)],
    body: Option<Value>,
) -> Result<T, llm::Error> {
    let client = hc.cloned().unwrap_or_else(default_http);
    let mut rb = client.request(method, u);
    if let Some(b) = &body {
        rb = rb
            .header("Content-Type", "application/json")
            .body(serde_json::to_vec(b).map_err(|e| llm::Error::Other(Box::new(e)))?);
    }
    for (k, v) in headers {
        if !v.is_empty() {
            rb = rb.header(*k, v);
        }
    }
    let resp = send(ctx, rb).await?;
    let status = resp.status().as_u16();
    let headers = resp.headers().clone();
    let raw = read_body(ctx, resp, MAX_BODY).await?;
    if status != 200 {
        return Err(status_error_of(status, &headers, &error_body(&raw)).into_llm());
    }
    serde_json::from_slice(&raw).map_err(|e| llm::Error::Other(Box::new(e)))
}

pub(crate) fn trim_base(base: &str, def: &str) -> String {
    let base = if base.is_empty() { def } else { base };
    base.trim_end_matches('/').to_string()
}

// ---- model listing --------------------------------------------------------

async fn list_anthropic(
    ctx: &Ctx,
    hc: &reqwest::Client,
    s: &ProviderSpec,
) -> Result<Vec<ModelInfo>, llm::Error> {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct M {
        id: String,
        display_name: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Out {
        data: Vec<M>,
    }
    let out: Out = http_json(
        ctx,
        Some(hc),
        reqwest::Method::GET,
        &format!(
            "{}/v1/models?limit=1000",
            trim_base(&s.base_url, "https://api.anthropic.com")
        ),
        &[
            ("x-api-key", s.api_key.clone()),
            ("anthropic-version", "2023-06-01".into()),
        ],
        None,
    )
    .await?;
    Ok(out
        .data
        .into_iter()
        .map(|m| ModelInfo {
            id: m.id,
            display_name: m.display_name,
        })
        .collect())
}

async fn list_openai(
    ctx: &Ctx,
    hc: &reqwest::Client,
    s: &ProviderSpec,
) -> Result<Vec<ModelInfo>, llm::Error> {
    if s.base_url.is_empty() {
        return Err(llm::Error::Message("a base URL is required".into()));
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct M {
        id: String,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Out {
        data: Vec<M>,
    }
    let mut hdr = Vec::new();
    if !s.api_key.is_empty() {
        hdr.push(("Authorization", format!("Bearer {}", s.api_key)));
    }
    let out: Out = http_json(
        ctx,
        Some(hc),
        reqwest::Method::GET,
        &format!("{}/models", trim_base(&s.base_url, "")),
        &hdr,
        None,
    )
    .await?;
    Ok(out
        .data
        .into_iter()
        .map(|m| ModelInfo {
            id: m.id,
            display_name: String::new(),
        })
        .collect())
}

/// Go's `url.QueryEscape`.
fn query_escape(s: &str) -> String {
    let mut out = String::with_capacity(s.len());
    for b in s.bytes() {
        match b {
            b'A'..=b'Z' | b'a'..=b'z' | b'0'..=b'9' | b'-' | b'_' | b'.' | b'~' => {
                out.push(b as char)
            }
            b' ' => out.push('+'),
            _ => out.push_str(&format!("%{b:02X}")),
        }
    }
    out
}

async fn list_gemini(
    ctx: &Ctx,
    hc: &reqwest::Client,
    s: &ProviderSpec,
) -> Result<Vec<ModelInfo>, llm::Error> {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct M {
        name: String,
        #[serde(rename = "displayName")]
        display_name: String,
        #[serde(rename = "supportedGenerationMethods")]
        methods: Vec<String>,
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct Out {
        models: Vec<M>,
        #[serde(rename = "nextPageToken")]
        next_page_token: String,
    }
    let mut models = Vec::new();
    let mut token = String::new();
    for _ in 0..10 {
        let mut u = format!(
            "{}/models?pageSize=200",
            trim_base(&s.base_url, GEMINI_BASE)
        );
        if !token.is_empty() {
            u.push_str(&format!("&pageToken={}", query_escape(&token)));
        }
        let out: Out = http_json(
            ctx,
            Some(hc),
            reqwest::Method::GET,
            &u,
            &[("x-goog-api-key", s.api_key.clone())],
            None,
        )
        .await?;
        for m in out.models {
            if m.methods.iter().any(|x| x == "generateContent") {
                models.push(ModelInfo {
                    id: m
                        .name
                        .strip_prefix("models/")
                        .unwrap_or(&m.name)
                        .to_string(),
                    display_name: m.display_name,
                });
            }
        }
        token = out.next_page_token;
        if token.is_empty() {
            break;
        }
    }
    Ok(models)
}

// ---- Google Gemini ---------------------------------------------------------

pub(crate) const GEMINI_BASE: &str = "https://generativelanguage.googleapis.com/v1beta";

/// Gemini calls the Generative Language API (generateContent).
#[derive(Debug, Clone, Default)]
pub struct Gemini {
    pub base_url: String,
    pub api_key: String,
    pub http: Option<reqwest::Client>,
}

#[async_trait]
impl Provider for Gemini {
    async fn complete(
        &self,
        ctx: &Ctx,
        model: &str,
        req: llm::Request,
    ) -> Result<llm::Response, llm::Error> {
        let mut body = Map::new();
        let system = format!("{}{}", req.system, json_instruction_of(&req));
        if !system.is_empty() {
            body.insert(
                "systemInstruction".into(),
                json!({"parts": [{"text": system}]}),
            );
        }
        let mut contents = Vec::new();
        for m in &req.messages {
            let role = if m.role == "assistant" {
                "model"
            } else {
                "user"
            };
            let mut parts = Vec::new();
            for r in &m.tool_results {
                let key = if r.is_error { "error" } else { "content" };
                parts.push(
                    json!({"functionResponse": {"name": r.name, "response": {key: r.content}}}),
                );
            }
            if !m.content.is_empty() || (m.tool_calls.is_empty() && m.tool_results.is_empty()) {
                parts.push(json!({"text": m.content}));
            }
            for c in &m.tool_calls {
                parts.push(json!({"functionCall": {"name": c.name, "args": c.arguments}}));
            }
            contents.push(json!({"role": role, "parts": parts}));
        }
        body.insert("contents".into(), Value::Array(contents));
        if !req.tools.is_empty() {
            let decls: Vec<Value> = req
                .tools
                .iter()
                .map(|t| {
                    let mut d = json!({"name": t.name, "description": t.description});
                    if !t.input_schema.is_empty() {
                        d["parametersJsonSchema"] = Value::Object(t.input_schema.clone());
                    }
                    d
                })
                .collect();
            body.insert("tools".into(), json!([{"functionDeclarations": decls}]));
        }
        let mut cfg = Map::new();
        if req.max_tokens > 0 {
            cfg.insert("maxOutputTokens".into(), json!(req.max_tokens));
        }
        if req.json && req.tools.is_empty() {
            // a JSON mime type is refused with function calling: the instruction asks for JSON then
            cfg.insert("responseMimeType".into(), json!("application/json"));
        }
        if !cfg.is_empty() {
            body.insert("generationConfig".into(), Value::Object(cfg));
        }

        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Call {
            name: String,
            args: Value,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Part {
            text: String,
            #[serde(rename = "functionCall")]
            function_call: Option<Call>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Content {
            parts: Vec<Part>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Candidate {
            content: Content,
            #[serde(rename = "finishReason")]
            finish_reason: String,
        }
        #[derive(Deserialize, Default)]
        #[serde(default, rename_all = "camelCase")]
        struct UsageMetadata {
            prompt_token_count: i64,
            candidates_token_count: i64,
            cached_content_token_count: i64,
        }
        #[derive(Deserialize, Default)]
        #[serde(default, rename_all = "camelCase")]
        struct Out {
            candidates: Vec<Candidate>,
            usage_metadata: UsageMetadata,
            model_version: String,
        }
        let u = format!(
            "{}/models/{}:generateContent",
            trim_base(&self.base_url, GEMINI_BASE),
            path_escape(model)
        );
        let out: Out = http_json(
            ctx,
            self.http.as_ref(),
            reqwest::Method::POST,
            &u,
            &[("x-goog-api-key", self.api_key.clone())],
            Some(Value::Object(body)),
        )
        .await
        .map_err(|e| e.wrap("generateContent"))?;
        let Some(first) = out.candidates.into_iter().next() else {
            return Err(llm::Error::Message("generateContent: no candidate".into()));
        };
        let mut text = String::new();
        let mut calls = Vec::new();
        for p in first.content.parts {
            text.push_str(&p.text);
            if let Some(fc) = p.function_call {
                // Gemini gives no call ids: the position in the answer is one
                calls.push(llm::ToolCall {
                    id: format!("call_{}", calls.len() + 1),
                    name: fc.name,
                    arguments: args_of(&fc.args),
                });
            }
        }
        let um = out.usage_metadata;
        let mut r = llm::Response {
            text,
            model: out.model_version,
            stop_reason: stop_reason(&first.finish_reason, "STOP", "MAX_TOKENS"),
            usage: llm::Usage {
                input_tokens: um.prompt_token_count,
                output_tokens: um.candidates_token_count,
                cache_read_tokens: um.cached_content_token_count,
                ..Default::default()
            },
            ..Default::default()
        };
        if !calls.is_empty() {
            r.stop_reason = llm::STOP_TOOL_USE.to_string();
        }
        r.tool_calls = calls;
        Ok(r)
    }

    /// generateContent takes function declarations.
    fn native_tools(&self) -> bool {
        true
    }

    fn embedder(&self) -> Option<&dyn Embedder> {
        Some(self)
    }
}
