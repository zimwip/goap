use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::Value;

use super::helpers::{Reply, upstream, user_msg};
use crate::router::{Config, ProviderConfig, SecretFunc, build};

#[tokio::test]
async fn test_router_aliases_and_openai_compatible() {
    let got: Arc<Mutex<Value>> = Arc::default();
    let g = got.clone();
    let url = upstream(move |r| {
        assert!(
            r.path == "/v1/chat/completions" && r.header("authorization") == "Bearer k",
            "unexpected request {} {}",
            r.path,
            r.header("authorization")
        );
        *g.lock() = r.json();
        Reply::ok(r#"{"model":"llama","choices":[{"message":{"content":"{\"ok\":true}"}}],"usage":{"prompt_tokens":3,"completion_tokens":2}}"#)
    })
    .await;
    let cfg = Config {
        providers: BTreeMap::from([
            (
                "local".to_string(),
                ProviderConfig {
                    r#type: "openai".into(),
                    base_url: format!("{url}/v1"),
                    api_key_env: "TEST_KEY".into(),
                    ..Default::default()
                },
            ),
            (
                "fake".to_string(),
                ProviderConfig {
                    r#type: "fake".into(),
                    ..Default::default()
                },
            ),
        ]),
        aliases: BTreeMap::from([
            ("default".to_string(), "local/llama".to_string()),
            ("fast".to_string(), "fake/echo".to_string()),
        ]),
    };
    let secret: SecretFunc = Arc::new(|_, _, env| {
        Box::pin(async move {
            Ok(if env == "TEST_KEY" {
                "k".to_string()
            } else {
                String::new()
            })
        })
    });
    let ctx = Ctx::background();
    let r = build(&ctx, &cfg, secret).await.unwrap();
    let resp = r
        .complete(
            &ctx,
            llm::Request {
                system: "sys".into(),
                json: true,
                messages: user_msg("hi"),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        resp.provider == "local" && resp.usage.output_tokens == 2 && resp.text == r#"{"ok":true}"#,
        "unexpected response {resp:?}"
    );
    let got = got.lock().clone();
    assert!(
        got["model"] == "llama"
            && !got["response_format"].is_null()
            && got["messages"].as_array().map(Vec::len) == Some(2),
        "unexpected payload {got}"
    );
    let resp = r
        .complete(
            &ctx,
            llm::Request {
                model: "fast".into(),
                messages: user_msg("x"),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(resp.text, "echo: x", "fake: {resp:?}");
    assert!(
        r.complete(
            &ctx,
            llm::Request {
                model: "nope".into(),
                ..Default::default()
            }
        )
        .await
        .is_err(),
        "unknown alias must fail"
    );
    // anthropic without key is skipped, so an alias to it fails at resolution
    assert!(
        r.resolve("anthropic/claude-opus-5").is_err(),
        "unconfigured provider must fail"
    );
}
