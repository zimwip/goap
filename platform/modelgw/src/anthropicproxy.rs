//! The gateway's Anthropic Messages endpoint (ADR 0123 §6): a tool that calls the model by itself (Claude Code
//! through the Claude Agent SDK) takes <gateway>/anthropic as its ANTHROPIC_BASE_URL and the token of its call as its
//! API key, so that each of its calls is a row of the ledger (ADR 0089).

use std::collections::BTreeMap;
use std::fmt::Write as _;
use std::sync::Arc;

use axum::body::Body;
use axum::response::Response;
use futures::StreamExt;
use futures::future::BoxFuture;
use goap_authz as authz;
use goap_ctx::Ctx;
use goap_llm as llm;
use goap_modeltoken as modeltoken;
use http::{HeaderMap, HeaderValue, StatusCode};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::error::Error;
use crate::ledger::{KIND_COMPLETE, MAX_CALL_ERROR, Pending};
use crate::protocols::{cut, lookup_kind, trim_base};
use crate::router::{Target, quote};
use crate::service::Service;
use crate::store::{ModelEntry, ProviderRecord};
use crate::tools::go_json;

/// The prefix of the gateway's Anthropic Messages endpoint.
pub const ANTHROPIC_PATH: &str = "/anthropic/";

/// Caps a request to the Anthropic endpoint (a long Claude Code conversation with its tools).
const MAX_PROXY_BODY: usize = 32 << 20;

/// AnthropicProxy serves the Anthropic Messages API to a tool that calls the model by itself, so that each of its
/// calls is a row of the ledger (ADR 0089) attributed to the person, the change and the process the tool ran for
/// (ADR 0114), under the catalog policy of the gateway (enabled model, roles, quota). The request is passed to the
/// provider as it is (streaming, tools, caching): only the model is resolved and the provider's key put in; no global
/// behaviour is added. A model is a model of the platform catalog served by a provider of the anthropic protocol,
/// named by an alias, provider/model, or its name.
pub struct AnthropicProxy {
    pub service: Arc<Service>,
    /// Checks the tokens (goap_modeltoken), shared with the MCP hub that mints them (GOAP_MODEL_PROXY_SECRET).
    pub secret: Vec<u8>,
    /// Calls the provider; None: a client with no timeout (a stream lasts as long as the answer).
    pub http: Option<reqwest::Client>,
}

type RawRequest = BTreeMap<String, Box<RawValue>>;

/// What is done once the answer has passed: the row of the call written, from what was read of it.
type Finish = Box<dyn FnOnce(Option<UsageReader>, Option<String>) -> BoxFuture<'static, ()> + Send>;

impl AnthropicProxy {
    /// The router to mount: every method under ANTHROPIC_PATH.
    pub fn router(self: Arc<Self>) -> axum::Router {
        let p = self;
        axum::Router::new().route(
            "/anthropic/{*rest}",
            axum::routing::any(move |req: http::Request<Body>| {
                let p = p.clone();
                async move { p.serve(req).await }
            }),
        )
    }

    /// Serves one request.
    pub async fn serve(&self, r: http::Request<Body>) -> Response {
        let (parts, body) = r.into_parts();
        let path = parts.uri.path();
        let op = path
            .strip_prefix(ANTHROPIC_PATH.trim_end_matches('/'))
            .unwrap_or(path)
            .to_string();
        if parts.method != http::Method::POST
            || (op != "/v1/messages" && op != "/v1/messages/count_tokens")
        {
            return anthropic_error(
                StatusCode::NOT_FOUND,
                "not_found_error",
                "the gateway serves POST /v1/messages and /v1/messages/count_tokens only",
            );
        }
        let header = |n: &str| {
            parts
                .headers
                .get(n)
                .and_then(|v| v.to_str().ok())
                .unwrap_or("")
                .to_string()
        };
        let mut token = header("x-api-key");
        if token.is_empty() {
            let a = header("authorization");
            token = a.strip_prefix("Bearer ").unwrap_or(&a).to_string();
        }
        let claims = match modeltoken::verify(&self.secret, &token, self.service.now()) {
            Ok(c) => c,
            Err(e) => {
                return anthropic_error(
                    StatusCode::UNAUTHORIZED,
                    "authentication_error",
                    &e.to_string(),
                );
            }
        };
        let body = match axum::body::to_bytes(body, MAX_PROXY_BODY + 1).await {
            Ok(b) if b.len() <= MAX_PROXY_BODY => b,
            _ => {
                return anthropic_error(
                    StatusCode::PAYLOAD_TOO_LARGE,
                    "request_too_large",
                    "request too large or unreadable",
                );
            }
        };
        let req: RawRequest = match serde_json::from_slice(&body) {
            Ok(r) => r,
            Err(_) => {
                return anthropic_error(
                    StatusCode::BAD_REQUEST,
                    "invalid_request_error",
                    "the body is not a JSON object",
                );
            }
        };
        let model: String = req
            .get("model")
            .and_then(|m| serde_json::from_str(m.get()).ok())
            .unwrap_or_default();
        let ctx = claims_context(&Ctx::background(), &claims);
        if op == "/v1/messages/count_tokens" {
            // no generation: no row, the model resolved all the same
            let (t, prov) = self.service.anthropic_target(&ctx, &model).await;
            let prov = match prov {
                Ok(p) => p,
                Err(e) => {
                    return anthropic_error(status_of(&e), "invalid_request_error", &e.to_string());
                }
            };
            return match self
                .forward(&ctx, &parts.headers, &prov, &op, retarget(&req, &t.model))
                .await
            {
                Ok(resp) => pump(resp, &prov.name, None, None),
                Err((resp, _)) => resp,
            };
        }
        self.messages(&ctx, &parts.headers, &req, &model).await
    }

    /// One generation: admitted, recorded, passed on, its usage read from the answer.
    async fn messages(
        &self,
        ctx: &Ctx,
        headers: &HeaderMap,
        req: &RawRequest,
        model: &str,
    ) -> Response {
        let s = &self.service;
        let mut rec = s.begin(ctx, KIND_COMPLETE, model);
        rec.request(
            &raw_system(req.get("system").map(|v| v.get())),
            &raw_messages(req.get("messages").map(|v| v.get())),
        );
        let (t, prov) = s.anthropic_target(ctx, model).await;
        let admitted = match prov {
            Ok(p) => s
                .admit_model(ctx, &t, &end_user_of(ctx), false)
                .await
                .map(|(m, period)| (p, m, period)),
            Err(e) => Err(e),
        };
        let (prov, m, period) = match admitted {
            Ok(v) => v,
            Err(e) => {
                s.finish(ctx, rec, &t, 0, 0, Some(e.to_string())).await;
                return anthropic_error(status_of(&e), "permission_error", &e.to_string());
            }
        };
        let resp = match self
            .forward(ctx, headers, &prov, "/v1/messages", retarget(req, &t.model))
            .await
        {
            Ok(r) => r,
            Err((resp, err)) => {
                rec.answer(String::new());
                s.finish(ctx, rec, &t, 0, 0, Some(err)).await;
                return resp;
            }
        };
        let status = resp.status().as_u16();
        let stream = resp
            .headers()
            .get("content-type")
            .and_then(|v| v.to_str().ok())
            .is_some_and(|v| v.starts_with("text/event-stream"));
        let u = UsageReader {
            stream,
            ..Default::default()
        };
        let svc = s.clone();
        let ctx2 = ctx.clone();
        let provider = t.provider.clone();
        let finish: Finish = Box::new(move |u: Option<UsageReader>, ferr: Option<String>| {
            Box::pin(async move {
                let u = u.unwrap_or_default();
                let mut ferr = ferr;
                if ferr.is_none() && status >= 400 {
                    ferr = Some(format!(
                        "provider {provider} answered {status}: {}",
                        u.error_text().trim()
                    ));
                }
                let mut rec: Pending = rec;
                rec.answer(u.text.clone());
                record(&svc, &ctx2, &t, &m, &period, &u, rec, ferr).await;
            })
        });
        pump(resp, &prov.name, Some(u), Some(finish))
    }

    /// Passes body to the provider; its answer is to be copied to the client as it comes (pump). The error is the
    /// answer to give the client and the reason of the failure.
    async fn forward(
        &self,
        ctx: &Ctx,
        inh: &HeaderMap,
        prov: &ProviderRecord,
        op: &str,
        body: Vec<u8>,
    ) -> Result<reqwest::Response, (Response, String)> {
        let key = match self.service.key(ctx, prov).await {
            Ok(k) => k,
            Err(e) => {
                let msg = format!("API key of provider {}: {e}", prov.name);
                return Err((
                    anthropic_error(StatusCode::BAD_GATEWAY, "api_error", &msg),
                    e.to_string(),
                ));
            }
        };
        let def = lookup_kind(&prov.kind)
            .map(|k| k.default_base_url)
            .filter(|u| !u.is_empty())
            .unwrap_or_else(|| "https://api.anthropic.com".to_string());
        let hc = self.http.clone().unwrap_or_default();
        let mut rb = hc.post(format!("{}{}", trim_base(&prov.base_url, &def), op));
        let mut version = false;
        for h in ["anthropic-version", "anthropic-beta", "accept"] {
            for v in inh.get_all(h) {
                rb = rb.header(h, v.clone());
                version |= h == "anthropic-version";
            }
        }
        if !version {
            rb = rb.header("anthropic-version", "2023-06-01");
        }
        rb = rb
            .header("content-type", "application/json")
            .header("x-api-key", key)
            .body(body);
        let sent = tokio::select! {
            e = ctx.done() => Err(e.to_string()),
            r = rb.send() => r.map_err(|e| e.to_string()),
        };
        sent.map_err(|e| {
            (
                anthropic_error(
                    StatusCode::BAD_GATEWAY,
                    "api_error",
                    &format!("provider {} unreachable", prov.name),
                ),
                format!("provider {}: {e}", prov.name),
            )
        })
    }
}

#[allow(clippy::too_many_arguments)]
async fn record(
    s: &Service,
    ctx: &Ctx,
    t: &Target,
    m: &ModelEntry,
    period: &str,
    u: &UsageReader,
    rec: Pending,
    ferr: Option<String>,
) {
    s.record_usage(ctx, t, m, period, u.input + u.output).await;
    s.finish(ctx, rec, t, u.input, u.output, ferr).await;
}

/// The answer of the provider copied to the client as it comes, through u when given; finish runs once it has
/// passed (before the client sees its end).
fn pump(
    resp: reqwest::Response,
    provider: &str,
    u: Option<UsageReader>,
    finish: Option<Finish>,
) -> Response {
    let status = StatusCode::from_u16(resp.status().as_u16()).unwrap_or(StatusCode::BAD_GATEWAY);
    let mut headers = HeaderMap::new();
    for h in [
        "content-type",
        "request-id",
        "retry-after",
        "anthropic-ratelimit-requests-remaining",
        "anthropic-ratelimit-tokens-remaining",
    ] {
        if let Some(v) = resp.headers().get(h)
            && !v.is_empty()
            && let Ok(v) = HeaderValue::from_bytes(v.as_bytes())
        {
            headers.insert(h, v);
        }
    }
    let (tx, rx) = tokio::sync::mpsc::channel::<Result<bytes::Bytes, std::io::Error>>(16);
    let provider = provider.to_string();
    tokio::spawn(async move {
        let mut u = u;
        let mut ferr = None;
        let mut stream = resp.bytes_stream();
        while let Some(chunk) = stream.next().await {
            match chunk {
                Ok(b) => {
                    if let Some(u) = &mut u {
                        u.write(&b);
                    }
                    if tx.send(Ok(b)).await.is_err() {
                        ferr = Some("client gone: the connection was closed".to_string());
                        break;
                    }
                }
                Err(e) => {
                    ferr = Some(format!("provider {provider}: {e}"));
                    let _ = tx.send(Err(std::io::Error::other(e.to_string()))).await;
                    break;
                }
            }
        }
        if ferr.is_none()
            && let Some(u) = &mut u
        {
            u.close();
        }
        if let Some(f) = finish {
            f(u, ferr).await;
        }
        drop(tx);
    });
    let body = Body::from_stream(futures::stream::unfold(rx, |mut rx| async move {
        rx.recv().await.map(|item| (item, rx))
    }));
    let mut out = Response::new(body);
    *out.status_mut() = status;
    *out.headers_mut() = headers;
    out
}

/// ctx acting as the token says: a system subject acting for the end user, the source and the correlation ids of
/// the ledger.
pub(crate) fn claims_context(ctx: &Ctx, c: &modeltoken::Claims) -> Ctx {
    let p = authz::Principal {
        subject: c.subject.clone(),
        org: c.org.clone(),
        project: c.project.clone(),
        ..Default::default()
    }
    .for_user(&c.end_user);
    let ctx = authz::with_principal(ctx, p);
    llm::with_meta(
        &ctx,
        llm::CallMeta {
            source: c.source.clone(),
            process_id: c.process.clone(),
            change_id: c.change.clone(),
            step: llm::NO_STEP,
            call: llm::NO_STEP,
            ..Default::default()
        },
    )
}

impl Service {
    /// Resolves the model a tool asks for to a model of the catalog served by a loaded provider of the anthropic
    /// protocol: an alias or provider/model first, else a catalog model of that name (the first provider by name).
    /// The target is what the name resolved to, for the row of a refusal.
    pub(crate) async fn anthropic_target(
        &self,
        ctx: &Ctx,
        model: &str,
    ) -> (Target, Result<ProviderRecord, Error>) {
        let snap = match self.sync(ctx).await {
            Ok(s) => s,
            Err(e) => return (Target::default(), Err(e)),
        };
        let provider = |name: &str| {
            snap.providers
                .iter()
                .find(|p| p.name == name && p.protocol == "anthropic" && p.enabled)
                .cloned()
        };
        if !model.is_empty()
            && let Ok((t, _)) = self.router.resolve(model)
        {
            return match provider(&t.provider) {
                Some(p) => (t, Ok(p)),
                None => {
                    let e = Error::Invalid(format!(
                        "{model} resolves to {}/{}, not a provider of the Anthropic protocol",
                        t.provider, t.model
                    ));
                    (t, Err(e))
                }
            };
        }
        let mut names: Vec<&str> = snap
            .models
            .iter()
            .filter(|m| m.model == model && provider(&m.provider).is_some())
            .map(|m| m.provider.as_str())
            .collect();
        names.sort();
        for n in names {
            if self.router.resolve(&format!("{n}/{model}")).is_ok()
                && let Some(p) = provider(n)
            {
                // the provider is loaded
                return (
                    Target {
                        provider: n.to_string(),
                        model: model.to_string(),
                    },
                    Ok(p),
                );
            }
        }
        (
            Target::default(),
            Err(Error::ModelDisabled(format!(
                "{} is not a model of the platform catalog on an Anthropic provider",
                quote(model)
            ))),
        )
    }
}

/// Who the roles of a model are checked for: the person a service acts for, else the caller.
fn end_user_of(ctx: &Ctx) -> authz::Principal {
    let p = authz::principal_from(ctx);
    if p.is_system() && !p.on_behalf_of.is_empty() {
        return authz::Principal {
            subject: p.on_behalf_of.clone(),
            org: p.org.clone(),
            project: p.project.clone(),
            ..Default::default()
        };
    }
    p
}

/// The HTTP status of the Anthropic API for an error of the policy.
fn status_of(err: &Error) -> StatusCode {
    if err.is_forbidden() {
        StatusCode::FORBIDDEN
    } else if err.is_quota_exceeded() {
        StatusCode::TOO_MANY_REQUESTS
    } else if err.is_model_disabled() || err.is_invalid() {
        StatusCode::BAD_REQUEST
    } else {
        StatusCode::BAD_GATEWAY
    }
}

fn anthropic_error(status: StatusCode, typ: &str, msg: &str) -> Response {
    #[derive(Serialize)]
    struct Detail<'a> {
        message: &'a str,
        #[serde(rename = "type")]
        kind: &'a str,
    }
    #[derive(Serialize)]
    struct Body_<'a> {
        error: Detail<'a>,
        #[serde(rename = "type")]
        kind: &'a str,
    }
    let text = go_json(&Body_ {
        error: Detail {
            message: msg,
            kind: typ,
        },
        kind: "error",
    }) + "\n";
    let mut out = Response::new(Body::from(text));
    *out.status_mut() = status;
    out.headers_mut()
        .insert("content-type", HeaderValue::from_static("application/json"));
    out
}

/// The request with the model of the provider.
fn retarget(req: &RawRequest, model: &str) -> Vec<u8> {
    let mut out: BTreeMap<&str, &RawValue> =
        req.iter().map(|(k, v)| (k.as_str(), v.as_ref())).collect();
    let m = RawValue::from_string(go_json(model)).ok();
    if let Some(m) = &m {
        out.insert("model", m);
    }
    serde_json::to_vec(&out).unwrap_or_default()
}

/// The system text of a request (a string or text blocks), for the exchange.
fn raw_system(raw: Option<&str>) -> String {
    let Some(raw) = raw else {
        return String::new();
    };
    if let Ok(s) = serde_json::from_str::<String>(raw) {
        return s;
    }
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct B {
        text: String,
    }
    let blocks: Vec<B> = serde_json::from_str(raw).unwrap_or_default();
    blocks
        .into_iter()
        .map(|b| b.text)
        .collect::<Vec<_>>()
        .join("\n\n")
}

/// The messages of a request, for the exchange: the text of each, the other blocks as JSON.
fn raw_messages(raw: Option<&str>) -> Vec<llm::Message> {
    #[derive(Deserialize, Default)]
    #[serde(default)]
    struct M {
        role: String,
        content: Option<Box<RawValue>>,
    }
    let msgs: Vec<M> = raw
        .and_then(|r| serde_json::from_str(r).ok())
        .unwrap_or_default();
    msgs.into_iter()
        .map(|m| {
            let c = m.content.as_ref().map(|c| c.get()).unwrap_or("null");
            let text = match serde_json::from_str::<String>(c) {
                Ok(t) => t,
                Err(_) => blocks_text(c),
            };
            llm::Message {
                role: m.role,
                content: text,
                ..Default::default()
            }
        })
        .collect()
}

fn blocks_text(raw: &str) -> String {
    let Ok(blocks) = serde_json::from_str::<Vec<BTreeMap<String, Box<RawValue>>>>(raw) else {
        return raw.to_string();
    };
    let parts: Vec<String> = blocks
        .iter()
        .map(|b| {
            let typ: String = b
                .get("type")
                .and_then(|v| serde_json::from_str(v.get()).ok())
                .unwrap_or_default();
            if typ == "text"
                && let Some(t) = b
                    .get("text")
                    .and_then(|v| serde_json::from_str::<String>(v.get()).ok())
            {
                return t;
            }
            go_json(b)
        })
        .collect();
    parts.join("\n")
}

#[derive(Deserialize, Default, Clone, Copy)]
#[serde(default)]
struct AnthropicUsage {
    input_tokens: i64,
    cache_creation_input_tokens: i64,
    cache_read_input_tokens: i64,
    output_tokens: i64,
}

impl AnthropicUsage {
    /// Every input token the model read, cached or not.
    fn input(&self) -> i64 {
        self.input_tokens + self.cache_creation_input_tokens + self.cache_read_input_tokens
    }
}

/// Reads the answer of the provider as it passes: the tokens it counts and the text it gives, from one JSON message
/// or from the events of a stream.
#[derive(Default)]
pub(crate) struct UsageReader {
    stream: bool,
    in_tool: bool,
    input: i64,
    output: i64,
    text: String,
    /// a JSON answer (capped), or the pending line of a stream
    body: Vec<u8>,
    err_body: String,
}

impl UsageReader {
    fn write(&mut self, b: &[u8]) {
        if !self.stream {
            if self.body.len() < MAX_PROXY_BODY {
                self.body.extend_from_slice(b);
            }
            return;
        }
        self.body.extend_from_slice(b);
        while let Some(i) = self.body.iter().position(|c| *c == b'\n') {
            let line: Vec<u8> = self.body.drain(..=i).collect();
            let line = String::from_utf8_lossy(&line);
            self.event(line.trim_end_matches(['\r', '\n']));
        }
    }

    /// Reads what is left: the JSON answer, or the last line of a stream.
    fn close(&mut self) {
        if self.stream {
            let rest = String::from_utf8_lossy(&std::mem::take(&mut self.body)).to_string();
            for line in rest.lines() {
                self.event(line);
            }
            return;
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct C {
            #[serde(rename = "type")]
            kind: String,
            text: String,
            name: String,
            input: Option<Box<RawValue>>,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Msg {
            usage: AnthropicUsage,
            content: Vec<C>,
        }
        let Ok(msg) = serde_json::from_slice::<Msg>(&self.body) else {
            self.err_body.push_str(&String::from_utf8_lossy(&self.body));
            return;
        };
        self.input = msg.usage.input();
        self.output = msg.usage.output_tokens;
        for c in &msg.content {
            match c.kind.as_str() {
                "text" => self.text.push_str(&c.text),
                "tool_use" => {
                    let input = c.input.as_ref().map(|r| r.get()).unwrap_or("");
                    let _ = write!(self.text, "\n[tool_use {} {}]", c.name, input);
                }
                _ => {}
            }
        }
        if self.input == 0 && self.output == 0 {
            self.err_body.push_str(&String::from_utf8_lossy(&self.body));
        }
    }

    /// Reads one line of a stream (only its data lines carry something).
    fn event(&mut self, line: &str) {
        let Some(data) = line.strip_prefix("data:") else {
            return;
        };
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Message {
            usage: AnthropicUsage,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Block {
            #[serde(rename = "type")]
            kind: String,
            name: String,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Delta {
            #[serde(rename = "type")]
            kind: String,
            text: String,
            partial_json: String,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Err_ {
            message: String,
        }
        #[derive(Deserialize, Default)]
        #[serde(default)]
        struct Event {
            #[serde(rename = "type")]
            kind: String,
            message: Message,
            usage: AnthropicUsage,
            content_block: Block,
            delta: Delta,
            error: Err_,
        }
        let Ok(ev) = serde_json::from_str::<Event>(data.trim()) else {
            return;
        };
        match ev.kind.as_str() {
            "message_start" => {
                self.input = ev.message.usage.input();
                self.output = ev.message.usage.output_tokens;
            }
            "message_delta" => {
                // the counts so far; the output of the whole answer comes last
                let n = ev.usage.input();
                if n > 0 {
                    self.input = n;
                }
                if ev.usage.output_tokens > 0 {
                    self.output = ev.usage.output_tokens;
                }
            }
            "content_block_start" => {
                if ev.content_block.kind == "tool_use" {
                    let _ = write!(self.text, "\n[tool_use {} ", ev.content_block.name);
                    self.in_tool = true;
                }
            }
            "content_block_stop" => {
                if self.in_tool {
                    self.text.push(']');
                    self.in_tool = false;
                }
            }
            "content_block_delta" => match ev.delta.kind.as_str() {
                "text_delta" => self.text.push_str(&ev.delta.text),
                "input_json_delta" => self.text.push_str(&ev.delta.partial_json),
                _ => {}
            },
            "error" => self.err_body.push_str(&ev.error.message),
            _ => {}
        }
    }

    /// What the provider said when it refused.
    fn error_text(&self) -> String {
        if !self.err_body.is_empty() {
            return cut(&self.err_body, MAX_CALL_ERROR).to_string();
        }
        cut(&String::from_utf8_lossy(&self.body), MAX_CALL_ERROR).to_string()
    }
}
