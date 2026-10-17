use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::{Duration, Instant};

use goap_ctx::Ctx;
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::Value;

use super::helpers::{Reply, must_json, upstream, user_msg};
use crate::providers::{Anthropic, OpenAICompatible};
use crate::retry::{RETRY_CAP, status_error, status_error_of};
use crate::router::{Provider, Router};

/// openAIServer answers with the given statuses in turn, then a completion.
async fn openai_server(statuses: &[u16]) -> (String, Arc<AtomicUsize>) {
    let n = Arc::new(AtomicUsize::new(0));
    let (c, statuses) = (n.clone(), statuses.to_vec());
    let url = upstream(move |_| {
        let i = c.fetch_add(1, Ordering::SeqCst);
        if i < statuses.len() {
            return Reply::status(statuses[i], "{\"error\":\"busy\"}\n").header("retry-after", "0");
        }
        Reply::ok(
            r#"{"model":"m1","choices":[{"message":{"content":"hi"},"finish_reason":"length"}],
			"usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":64}}}"#,
        )
    })
    .await;
    (url, n)
}

fn router_on(url: &str) -> Router {
    let mut r = Router::new();
    r.retry_base = Duration::from_millis(1);
    r.add_provider(
        "oa",
        Arc::new(OpenAICompatible {
            base_url: url.into(),
            ..Default::default()
        }),
    );
    r
}

fn m1() -> llm::Request {
    llm::Request {
        model: "oa/m1".into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn test_router_retries_transient_failures() {
    let (url, n) = openai_server(&[429, 503]).await;
    let resp = router_on(&url).complete(&Ctx::background(), m1()).await;
    let resp = resp.unwrap_or_else(|e| panic!("err {e} after {} calls", n.load(Ordering::SeqCst)));
    assert!(
        resp.text == "hi" && n.load(Ordering::SeqCst) == 3,
        "resp {resp:?}"
    );
    assert!(
        resp.stop_reason == llm::STOP_MAX_TOKENS
            && resp.truncated()
            && resp.usage.cache_read_tokens == 64
            && resp.usage.input_tokens == 100,
        "stop {:?} usage {:?}",
        resp.stop_reason,
        resp.usage
    );
}

#[tokio::test]
async fn test_router_does_not_retry_a_client_error() {
    let (url, n) = openai_server(&[400]).await;
    let err = router_on(&url)
        .complete(&Ctx::background(), m1())
        .await
        .unwrap_err();
    let se = status_error(&err);
    assert!(
        se.is_some_and(|s| s.code == 400) && n.load(Ordering::SeqCst) == 1,
        "err {err} after {} calls",
        n.load(Ordering::SeqCst)
    );
}

#[tokio::test]
async fn test_router_gives_up_after_its_retries() {
    let (url, n) = openai_server(&[500, 500, 500, 500, 500]).await;
    let mut r = router_on(&url);
    r.retries = 2;
    let res = r.complete(&Ctx::background(), m1()).await;
    assert!(
        res.is_err() && n.load(Ordering::SeqCst) == 3,
        "after {} calls",
        n.load(Ordering::SeqCst)
    );
    r.retries = -1; // none
    n.store(0, Ordering::SeqCst);
    let res = r.complete(&Ctx::background(), m1()).await;
    assert!(
        res.is_err() && n.load(Ordering::SeqCst) == 1,
        "after {} calls",
        n.load(Ordering::SeqCst)
    );
}

#[tokio::test]
async fn test_router_retry_stops_with_the_context() {
    let (url, _) = openai_server(&[503, 503, 503, 503]).await;
    let mut r = router_on(&url);
    r.retry_base = Duration::from_secs(3600);
    let (ctx, _cancel) = Ctx::background().with_timeout(Duration::from_millis(50));
    let start = Instant::now();
    let res = r.complete(&ctx, m1()).await;
    assert!(
        res.is_err() && start.elapsed() < Duration::from_secs(5),
        "after {:?}",
        start.elapsed()
    );
}

#[test]
fn test_status_error_reads_retry_after() {
    let mut h = http::HeaderMap::new();
    h.insert("retry-after", "7".parse().unwrap());
    let e = status_error_of(429, &h, "x");
    assert_eq!(e.retry_after, Duration::from_secs(7));
    let r = Router::new();
    assert_eq!(
        r.backoff(0, Duration::from_secs(7)),
        Duration::from_secs(7),
        "the provider's 7s"
    );
    let d = r.backoff(10, Duration::ZERO);
    assert!(
        d <= RETRY_CAP && d >= RETRY_CAP / 2,
        "backoff {d:?} out of range"
    );
}

#[tokio::test]
async fn test_anthropic_marks_the_cache_prefix_on_request() {
    let bodies: Arc<Mutex<Vec<Value>>> = Arc::default();
    let b = bodies.clone();
    let url = upstream(move |r| {
        b.lock().push(r.json());
        Reply::ok(
            r#"{"id":"m","type":"message","role":"assistant","model":"claude-x","stop_reason":"end_turn",
			"content":[{"type":"text","text":"ok"}],
			"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":2000,"cache_creation_input_tokens":500}}"#,
        )
        .header("content-type", "application/json")
    })
    .await;
    let a = Anthropic::new("k", &url);
    let ctx = Ctx::background();
    let mut req = llm::Request {
        system: "sys".into(),
        messages: user_msg("hi"),
        ..Default::default()
    };
    a.complete(&ctx, "claude-x", req.clone()).await.unwrap();
    req.cache = true;
    let resp = a.complete(&ctx, "claude-x", req).await.unwrap();
    let bodies = bodies.lock().clone();
    assert!(
        !must_json(&bodies[0]).contains("cache_control"),
        "no cache asked, marked anyway: {}",
        bodies[0]
    );
    let mark = r#""cache_control":{"type":"ephemeral"}"#;
    assert!(
        must_json(&bodies[1]["system"]).contains(mark)
            && must_json(&bodies[1]["messages"]).contains(mark),
        "cache asked, not marked: {}",
        bodies[1]
    );
    let u = &resp.usage;
    assert!(
        u.input_tokens == 2510
            && u.cache_read_tokens == 2000
            && u.cache_write_tokens == 500
            && resp.stop_reason == llm::STOP_END,
        "usage {u:?} stop {:?}",
        resp.stop_reason
    );
}
