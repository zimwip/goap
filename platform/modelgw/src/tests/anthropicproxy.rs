use std::sync::Arc;
use std::time::Duration;

use axum::body::Body;
use goap_authz as authz;
use goap_modeltoken as modeltoken;

use super::helpers::{Reply, calls, memory, new_service, upstream, who};
use crate::anthropicproxy::AnthropicProxy;
use crate::ledger::UsageFilter;
use crate::service::Service;
use crate::store::{AliasEntry, ModelEntry, PERIOD_TOTAL, ProviderRecord};

const SSE_ANSWER: &str = concat!(
    "event: message_start\n",
    r#"data: {"type":"message_start","message":{"usage":{"input_tokens":4,"cache_read_input_tokens":8,"output_tokens":1}}}"#,
    "\n\n",
    "event: content_block_start\n",
    r#"data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}"#,
    "\n\n",
    r#"data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}"#,
    "\n\n",
    r#"data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}"#,
    "\n\n",
    r#"data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"Edit"}}"#,
    "\n\n",
    r#"data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file\":1}"}}"#,
    "\n\n",
    r#"data: {"type":"content_block_stop","index":1}"#,
    "\n\n",
    r#"data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}"#,
    "\n\n",
    r#"data: {"type":"message_stop"}"#,
    "\n\n",
);

/// proxyFixture is a gateway whose catalog holds an Anthropic provider played by a fake API.
async fn proxy_fixture() -> (Arc<AnthropicProxy>, Arc<Service>) {
    let url = upstream(|r| {
        if r.header("x-api-key") != "sk-provider" || r.header("anthropic-beta") != "tools-1" {
            return Reply::status(
                401,
                "{\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"bad key\"}}\n",
            );
        }
        let req = r.json();
        if req["model"] != "claude-test" {
            return Reply::status(404, r#"{"type":"error","error":{"type":"not_found_error","message":"model"}}"#);
        }
        if r.path == "/v1/messages/count_tokens" {
            return Reply::ok(r#"{"input_tokens":42}"#);
        }
        if req["stream"] == true {
            // the events come in pieces
            let mut chunks = Vec::new();
            for chunk in SSE_ANSWER.split_inclusive("\n\n") {
                let (a, b) = chunk.split_at(chunk.len() / 2);
                chunks.push(a.to_string());
                chunks.push(b.to_string());
            }
            return Reply {
                status: 200,
                chunks,
                ..Default::default()
            }
            .header("content-type", "text/event-stream");
        }
        Reply::ok(r#"{"type":"message","content":[{"type":"text","text":"done"}],"usage":{"input_tokens":10,"cache_creation_input_tokens":5,"output_tokens":7}}"#)
            .header("content-type", "application/json")
    })
    .await;
    let (svc, g) = new_service(memory());
    g.set_env("TEST_PROXY_ANTHROPIC_KEY", "sk-provider");
    g.seed(
        &[ProviderRecord {
            name: "claude".into(),
            kind: "anthropic".into(),
            protocol: "anthropic".into(),
            base_url: url,
            enabled: true,
            api_key_ref: "env:TEST_PROXY_ANTHROPIC_KEY".into(),
        }],
        &[ModelEntry {
            provider: "claude".into(),
            model: "claude-test".into(),
            enabled: true,
            quota_tokens: 60,
            quota_period: PERIOD_TOTAL.into(),
            ..Default::default()
        }],
        &[AliasEntry {
            alias: "coder".into(),
            target: "claude/claude-test".into(),
            ..Default::default()
        }],
    );
    let svc = svc.shared();
    let p = Arc::new(AnthropicProxy {
        service: svc.clone(),
        secret: b"s3cret".to_vec(),
        http: None,
    });
    (p, svc)
}

/// A token of the coding agent for alice; expired: signed two minutes ago for one.
fn proxy_token(expired: bool) -> String {
    let now = chrono::Utc::now();
    let (at, ttl) = if expired {
        (now - chrono::Duration::minutes(2), Duration::from_secs(60))
    } else {
        (now, Duration::from_secs(3600))
    };
    modeltoken::sign(
        b"s3cret",
        modeltoken::Claims {
            subject: authz::system("connector:claude-coding-agent", &[]).subject,
            end_user: "alice".into(),
            org: "ORG-A".into(),
            project: "P1".into(),
            change: "CH-1".into(),
            process: "proc-1".into(),
            source: "connector:claude-coding-agent".into(),
            ..Default::default()
        },
        at,
        ttl,
    )
    .unwrap()
}

struct Answer {
    code: u16,
    content_type: String,
    body: String,
}

async fn post(p: &AnthropicProxy, path: &str, token: &str, body: &str) -> Answer {
    let r = http::Request::builder()
        .method("POST")
        .uri(path)
        .header("x-api-key", token)
        .header("anthropic-version", "2023-06-01")
        .header("anthropic-beta", "tools-1")
        .body(Body::from(body.to_string()))
        .unwrap();
    let resp = p.serve(r).await;
    let code = resp.status().as_u16();
    let content_type = resp
        .headers()
        .get("content-type")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    let bytes = axum::body::to_bytes(resp.into_body(), usize::MAX)
        .await
        .unwrap();
    Answer {
        code,
        content_type,
        body: String::from_utf8_lossy(&bytes).into_owned(),
    }
}

/// The calls of a tool calling the model by itself are rows of the ledger, attributed to the person, the change and
/// the process of the token, with their tokens and their exchange; the request and the answer pass as they are.
#[tokio::test]
async fn test_anthropic_proxy_records_every_call() {
    let (p, svc) = proxy_fixture().await;
    let tok = proxy_token(false);
    let w = post(
        &p,
        "/anthropic/v1/messages",
        &tok,
        r#"{"model":"claude-test","max_tokens":100,"system":[{"type":"text","text":"be brief"}],"messages":[{"role":"user","content":"fix the build"}]}"#,
    )
    .await;
    assert!(
        w.code == 200 && w.body.contains(r#""text":"done""#),
        "answer {} {}",
        w.code,
        w.body
    );
    let w = post(
        &p,
        "/anthropic/v1/messages",
        &tok,
        r#"{"model":"coder","stream":true,"messages":[{"role":"user","content":[{"type":"text","text":"go on"}]}]}"#,
    )
    .await;
    assert!(
        w.code == 200 && w.body == SSE_ANSWER && w.content_type.starts_with("text/event-stream"),
        "the stream must pass as it is: {} {:?}",
        w.code,
        w.body
    );
    let rows = calls(&svc, UsageFilter::default()).await;
    assert_eq!(rows.len(), 2, "rows {rows:?}");
    for c in &rows {
        assert!(
            c.subject == "system:connector:claude-coding-agent"
                && c.end_user == "alice"
                && c.change_id == "CH-1"
                && c.process_id == "proc-1"
                && c.source == "connector:claude-coding-agent"
                && c.project == "P1"
                && c.org == "ORG-A"
                && c.provider == "claude"
                && c.model == "claude-test"
                && c.error.is_empty(),
            "row {c:?}"
        );
    }
    assert!(
        rows[0].input_tokens == 15
            && rows[0].output_tokens == 7
            && rows[1].input_tokens == 12
            && rows[1].output_tokens == 9
            && rows[1].alias == "coder",
        "tokens {rows:?}"
    );
    let (_, x) = svc
        .call_exchange(&who("alice"), rows[1].seq, false)
        .await
        .expect("alice reads the exchange of the session's call");
    assert!(
        x.response == "Hello\n[tool_use Edit {\"file\":1}]" && x.messages[0].content == "go on",
        "exchange {x:?}"
    );
    let (_, x) = svc
        .call_exchange(&who("alice"), rows[0].seq, false)
        .await
        .unwrap();
    assert!(
        x.system == "be brief" && x.response == "done",
        "exchange {x:?}"
    );
    // the quota counts them: 22 + 21 of 60, the next one is refused before the provider
    let w = post(
        &p,
        "/anthropic/v1/messages",
        &tok,
        r#"{"model":"claude-test","messages":[]}"#,
    )
    .await;
    assert_eq!(w.code, 200, "third call {}", w.body);
    let w = post(
        &p,
        "/anthropic/v1/messages",
        &tok,
        r#"{"model":"claude-test","messages":[]}"#,
    )
    .await;
    assert_eq!(w.code, 429, "quota: {}", w.body);
    let rows = calls(&svc, UsageFilter::default()).await;
    assert!(
        rows.len() == 4 && rows[3].error.contains("quota"),
        "the refusal is a row too: {rows:?}"
    );
}

/// Only a valid token is served; a model outside the catalog or not served over the Anthropic protocol is refused,
/// with a row; counting tokens makes no row.
#[tokio::test]
async fn test_anthropic_proxy_refuses() {
    let (p, svc) = proxy_fixture().await;
    let body = r#"{"model":"claude-test","messages":[]}"#;
    for tok in [String::new(), "nope".to_string(), proxy_token(true)] {
        let w = post(&p, "/anthropic/v1/messages", &tok, body).await;
        assert_eq!(w.code, 401, "token {tok:?}");
    }
    let w = post(&p, "/anthropic/v1/complete", &proxy_token(false), body).await;
    assert_eq!(w.code, 404, "other paths");
    assert!(
        calls(&svc, UsageFilter::default()).await.is_empty(),
        "no row without a valid token"
    );
    let w = post(
        &p,
        "/anthropic/v1/messages",
        &proxy_token(false),
        r#"{"model":"claude-other","messages":[]}"#,
    )
    .await;
    assert_eq!(w.code, 400, "unknown model: {}", w.body);
    let w = post(
        &p,
        "/anthropic/v1/messages/count_tokens",
        &proxy_token(false),
        body,
    )
    .await;
    assert!(
        w.code == 200 && w.body.contains("42"),
        "count_tokens: {} {}",
        w.code,
        w.body
    );
    let rows = calls(&svc, UsageFilter::default()).await;
    assert!(
        rows.len() == 1 && !rows[0].error.is_empty(),
        "the refused model is one row, count_tokens none: {rows:?}"
    );
}

/// The proxy mounted as a router serves the same paths (axum's catch-all under /anthropic/).
#[tokio::test]
async fn test_anthropic_proxy_router() {
    let (p, _) = proxy_fixture().await;
    let url = super::helpers::serve(p.router()).await;
    let resp = reqwest::Client::new()
        .post(format!("{url}/anthropic/v1/messages"))
        .header("x-api-key", proxy_token(false))
        .header("anthropic-beta", "tools-1")
        .body(r#"{"model":"claude-test","messages":[]}"#)
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status().as_u16(), 200);
    assert!(resp.text().await.unwrap().contains("done"));
}
