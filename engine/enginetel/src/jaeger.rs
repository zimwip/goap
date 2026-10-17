use std::collections::BTreeMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_observe::Span;
use goap_selfimprove::{BoxError, Config, MethodologyDrafts, TraceSource};
use serde::Deserialize;
use serde_json::Value;

/// JaegerTraces reads traces from the Jaeger query API, so that the self-observation agent can look for slow spans
/// (GOAP_TRACE_QUERY_URL).
#[derive(Clone, Default)]
pub struct JaegerTraces {
    /// e.g. http://jaeger:16686
    pub base_url: String,
    pub client: Option<reqwest::Client>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Body {
    data: Option<Vec<Trace>>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Trace {
    spans: Option<Vec<JSpan>>,
}

#[derive(Deserialize, Default)]
#[serde(default, rename_all = "camelCase")]
struct JSpan {
    operation_name: String,
    /// µs
    duration: i64,
    tags: Option<Vec<Tag>>,
}

#[derive(Deserialize, Default)]
#[serde(default)]
struct Tag {
    key: String,
    value: Value,
}

#[async_trait]
impl TraceSource for JaegerTraces {
    async fn spans(&self, ctx: &Ctx, trace_id: &str) -> Result<Vec<Span>, BoxError> {
        let hc = match &self.client {
            Some(c) => c.clone(),
            None => reqwest::Client::builder()
                .timeout(Duration::from_secs(10))
                .build()?,
        };
        let url = format!(
            "{}/api/traces/{trace_id}",
            self.base_url.strip_suffix('/').unwrap_or(&self.base_url)
        );
        let resp = tokio_select(ctx, hc.get(url).send()).await?;
        if resp.status() != reqwest::StatusCode::OK {
            return Err(format!("jaeger: {}", resp.status()).into());
        }
        let raw = tokio_select(ctx, resp.bytes()).await?;
        let body: Body = serde_json::from_slice(&raw)?;
        let mut out = Vec::new();
        for t in body.data.unwrap_or_default() {
            for s in t.spans.unwrap_or_default() {
                let mut sp = Span {
                    name: s.operation_name,
                    duration_ms: s.duration / 1000,
                    attributes: BTreeMap::new(),
                    ..Default::default()
                };
                for tag in s.tags.unwrap_or_default() {
                    let v = goap_dsl::sprint(std::slice::from_ref(&tag.value));
                    if (tag.key == "error" && v == "true")
                        || (tag.key == "otel.status_code" && v == "ERROR")
                    {
                        sp.error = true;
                    }
                    sp.attributes.insert(tag.key, v);
                }
                out.push(sp);
            }
        }
        Ok(out)
    }
}

async fn tokio_select<T>(
    ctx: &Ctx,
    f: impl Future<Output = Result<T, reqwest::Error>>,
) -> Result<T, BoxError> {
    tokio::select! {
        r = f => r.map_err(Into::into),
        e = ctx.done() => Err(e.into()),
    }
}

/// SelfImprovementFromEnv configures the self-observation builtins: traces from GOAP_TRACE_QUERY_URL (Jaeger query
/// API, optional), links to GOAP_TRACE_UI_URL (default <query>/trace/).
pub fn self_improvement_from_env(drafts: Arc<dyn MethodologyDrafts>) -> Config {
    let mut cfg = Config {
        drafts: Some(drafts),
        ..Default::default()
    };
    let q = std::env::var("GOAP_TRACE_QUERY_URL").unwrap_or_default();
    if !q.is_empty() {
        cfg.traces = Some(Arc::new(JaegerTraces {
            base_url: q.clone(),
            client: None,
        }));
        cfg.trace_url = format!("{}/trace/", q.strip_suffix('/').unwrap_or(&q));
    }
    let ui = std::env::var("GOAP_TRACE_UI_URL").unwrap_or_default();
    if !ui.is_empty() {
        cfg.trace_url = ui;
    }
    cfg
}
