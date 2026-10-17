use std::sync::Arc;

use chrono::{DateTime, TimeZone, Utc};
use goap_ctx::Ctx;
use goap_llm as llm;
use goap_llmcfg as llmcfg;
use parking_lot::Mutex;
use serde_json::{Value, json};

use super::helpers::{
    Reply, alias, fake_provider, memory, model, new_service, obj, stores, upstream, user_msg,
    with_roles,
};
use crate::protocols::Gemini;
use crate::router::{Provider, default_config};
use crate::store::{PERIOD_DAY, ProviderRecord, period_key};

fn hi() -> llm::Request {
    llm::Request {
        messages: user_msg("hi"),
        ..Default::default()
    }
}

#[tokio::test]
async fn test_service_policy() {
    let st = stores().await;
    for (name, store) in &st.all {
        let ctx = Ctx::background();
        let start = Utc.with_ymd_and_hms(2026, 9, 23, 10, 0, 0).unwrap();
        let now: Arc<Mutex<DateTime<Utc>>> = Arc::new(Mutex::new(start));
        let (mut svc, g) = new_service(store.clone());
        let clock = now.clone();
        svc.now = Some(Arc::new(move || *clock.lock()));
        let svc = svc.shared();
        let echo = llmcfg::Model {
            quota_tokens: 10,
            quota_period: PERIOD_DAY.into(),
            roles: vec!["methodologist".into()],
            ..model("fake", "echo")
        };
        g.seed(
            &[fake_provider()],
            std::slice::from_ref(&echo),
            &[alias("default", "fake/echo")],
        );
        let user = with_roles("u", &["contributor"]);
        let err = svc.complete(&user, hi()).await.unwrap_err();
        assert!(err.is_forbidden(), "{name}: role required: {err}");
        let meth = with_roles("m", &["methodologist"]);
        let r = svc.complete(&meth, hi()).await.unwrap();
        assert_eq!(r.text, "echo: hi", "{name}: allowed");
        svc.complete(&ctx, hi()).await.unwrap(); // internal caller
        // fake reports no usage: record some by hand, under the key of the model node, to reach the quota
        store
            .add_usage(&ctx, &echo.key(), &period_key(PERIOD_DAY, start), 10)
            .await
            .unwrap();
        let err = svc.complete(&meth, hi()).await.unwrap_err();
        assert!(err.is_quota_exceeded(), "{name}: quota: {err}");
        *now.lock() = start + chrono::Duration::hours(24); // new period
        svc.complete(&meth, hi()).await.unwrap();
        let (ms, as_) = svc.available(&user).await.unwrap();
        assert!(
            ms.is_empty() && as_.is_empty(),
            "{name}: contributor must see nothing: {ms:?} {as_:?}"
        );
        let (ms, as_) = svc.available(&meth).await.unwrap();
        assert!(
            ms.len() == 1 && as_.len() == 1,
            "{name}: methodologist sees the model and its alias"
        );
        let (cat, aliases) = svc.catalog(&ctx).await.unwrap();
        assert!(
            cat.len() == 1 && aliases.len() == 1 && cat[0].model.roles[0] == "methodologist",
            "{name}: catalog: {cat:?} {aliases:?}"
        );
        // the configuration is changed on the graph: the service follows
        g.update(
            &echo.key(),
            obj(json!({"provider": "fake", "model": "echo", "enabled": false})),
        );
        let err = svc.complete(&meth, hi()).await.unwrap_err();
        assert!(err.is_model_disabled(), "{name}: disabled: {err}");
        g.retire(&llmcfg::provider_key("fake"));
        let err = svc.complete(&meth, hi()).await.unwrap_err();
        assert!(
            err.is_invalid(),
            "{name}: the alias must go with its provider: {err}"
        );
        let (cat, aliases) = svc.catalog(&ctx).await.unwrap();
        assert!(
            cat.is_empty() && aliases.is_empty(),
            "{name}: models and aliases must go with the provider: {cat:?} {aliases:?}"
        );
    }
}

#[tokio::test]
async fn test_provider_key_is_a_reference() {
    let ctx = Ctx::background();
    let (svc, g) = new_service(memory());
    let svc = svc.shared();
    g.seed(
        &[ProviderRecord {
            name: "mistral".into(),
            kind: "mistral".into(),
            protocol: "openai".into(),
            base_url: "https://api.mistral.ai/v1".into(),
            enabled: true,
            api_key_ref: "env:GOAP_TEST_MISTRAL_KEY".into(),
        }],
        &[],
        &[],
    );
    let view = || async {
        let vs = svc.providers(&ctx).await.unwrap();
        assert_eq!(vs.len(), 1, "providers: {vs:?}");
        vs[0].clone()
    };
    let v = view().await;
    assert!(
        v.has_key && !v.active && v.reason == "no API key",
        "without the key in its environment the provider is inactive: {v:?}"
    );
    g.set_env("GOAP_TEST_MISTRAL_KEY", "sk-secret-1234");
    // any change of the provider reloads it (a change modifies its node, ADR 0102)
    g.update(
        &llmcfg::provider_key("mistral"),
        obj(
            json!({"name": "mistral", "kind": "mistral", "protocol": "openai", "enabled": true,
            "apiKeyRef": "env:GOAP_TEST_MISTRAL_KEY", "baseURL": "https://api.mistral.ai/v1/"}),
        ),
    );
    let v = view().await;
    assert!(
        v.active && v.provider.api_key_ref == "env:GOAP_TEST_MISTRAL_KEY",
        "with the key the provider is loaded: {v:?}"
    );
}

#[tokio::test]
async fn test_discover_models() {
    let url = upstream(|r| {
        let no = || Reply::status(401, "no key\n");
        match r.path.as_str() {
            // anthropic
            "/v1/models" if r.header("x-api-key") != "k" => no(),
            "/v1/models" => Reply::ok(r#"{"data":[{"id":"claude-a","display_name":"Claude A"}]}"#),
            // mistral-like
            "/openai/models" if r.header("authorization") != "Bearer k" => no(),
            "/openai/models" => Reply::ok(r#"{"data":[{"id":"mistral-large"},{"id":"mistral-small"}]}"#),
            "/gemini/models" if r.header("x-goog-api-key") != "k" => no(),
            "/gemini/models" => Reply::ok(
                r#"{"models":[{"name":"models/gemini-pro","displayName":"Gemini Pro","supportedGenerationMethods":["generateContent"]},{"name":"models/embed","supportedGenerationMethods":["embedContent"]}]}"#,
            ),
            _ => Reply::status(404, "404 page not found\n"),
        }
    })
    .await;
    let ctx = Ctx::background();
    let (svc, _) = new_service(memory());
    let rec = |kind: &str, base: String| ProviderRecord {
        name: "p".into(),
        kind: kind.into(),
        base_url: base,
        ..Default::default()
    };
    for (kind, base, want) in [
        ("anthropic", url.clone(), vec!["claude-a"]),
        (
            "mistral",
            format!("{url}/openai"),
            vec!["mistral-large", "mistral-small"],
        ),
        ("google", format!("{url}/gemini"), vec!["gemini-pro"]),
    ] {
        let got = svc
            .discover(&ctx, rec(kind, base), "k")
            .await
            .unwrap_or_else(|e| panic!("{kind}: {e}"));
        let ids: Vec<&str> = got.iter().map(|d| d.model.id.as_str()).collect();
        assert_eq!(ids, want, "{kind}");
    }
    assert!(
        svc.discover(&ctx, rec("mistral", format!("{url}/openai")), "wrong")
            .await
            .is_err(),
        "bad key must fail"
    );
}

#[tokio::test]
async fn test_gemini_complete() {
    let got: Arc<Mutex<Value>> = Arc::default();
    let gg = got.clone();
    let url = upstream(move |r| {
        assert!(
            r.path == "/models/gemini-pro:generateContent" && r.header("x-goog-api-key") == "k",
            "unexpected request {}",
            r.path
        );
        *gg.lock() = r.json();
        Reply::ok(
            r#"{"candidates":[{"content":{"parts":[{"text":"hello "},{"text":"world"}]}}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2}}"#,
        )
    })
    .await;
    let g = Gemini {
        base_url: url,
        api_key: "k".into(),
        ..Default::default()
    };
    let msg = |role: &str, c: &str| llm::Message {
        role: role.into(),
        content: c.into(),
        ..Default::default()
    };
    let resp = g
        .complete(
            &Ctx::background(),
            "gemini-pro",
            llm::Request {
                system: "sys".into(),
                json: true,
                max_tokens: 50,
                messages: vec![msg("user", "a"), msg("assistant", "b"), msg("user", "c")],
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        resp.text == "hello world" && resp.usage.input_tokens == 4 && resp.usage.output_tokens == 2,
        "unexpected {resp:?}"
    );
    let got = got.lock().clone();
    let contents = got["contents"].as_array().unwrap();
    assert!(
        contents.len() == 3
            && contents[1]["role"] == "model"
            && !got["systemInstruction"].is_null(),
        "unexpected payload {got}"
    );
    let cfg = &got["generationConfig"];
    assert!(
        cfg["responseMimeType"] == "application/json" && cfg["maxOutputTokens"] == 50,
        "unexpected config {cfg}"
    );
}

#[tokio::test]
async fn test_default_config_is_seeded_once() {
    // on the real graph, seeded by graphsvc::seed_models and administered through a change, as in Go
    let ctx = goap_graphsvc::system(&Ctx::background());
    let ds = goap_def::load_domains("../../domains").expect("load the domains");
    let cat = Arc::new(goap_typecat::new(&ds).expect("catalogue"));
    let mut g = goap_graph::Graph::new(Arc::new(goap_graph::Memory::new()));
    g.set_types(move || Some(cat.clone() as Arc<dyn goap_graph::TypeCatalog>));
    let g = Arc::new(g);
    g.bootstrap(&ctx).await.unwrap();
    let dir = llmcfg::Directory::new(g.clone()).with_ttl(std::time::Duration::from_nanos(1));
    let svc = crate::Service::new(Arc::new(dir), memory(), None).shared();
    let (provs, models, aliases) = default_config(false).objects().unwrap();
    assert!(
        goap_graphsvc::seed_models(&ctx, &g, &provs, &models, &aliases)
            .await
            .unwrap(),
        "seed"
    );
    assert!(
        svc.router.resolve("default").is_err(),
        "the router is built on first use"
    );
    svc.reload(&ctx).await.unwrap();
    svc.router.resolve("default").unwrap();
    let (cat, _) = svc.catalog(&ctx).await.unwrap();
    // echo and the embedding model
    assert!(
        cat.len() == 2 && cat[0].model.enabled && cat[1].model.enabled,
        "alias targets must be in the catalog: {cat:?}"
    );
    let n = g
        .node_by_key(
            &ctx,
            llmcfg::NAMESPACE_PLATFORM,
            &llmcfg::alias_key("default"),
        )
        .await
        .unwrap();
    goap_graphsvc::seed_change(
        &ctx,
        &g,
        llmcfg::NAMESPACE_PLATFORM,
        "retire",
        vec![goap_graph::NodeEdit {
            pre: Some(n.r#ref()),
            state: "retired".into(),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    assert!(
        !goap_graphsvc::seed_models(&ctx, &g, &provs, &models, &aliases)
            .await
            .unwrap(),
        "an administered graph must not be re-seeded"
    );
    svc.reload(&ctx).await.unwrap();
    assert!(
        svc.router.resolve("default").is_err(),
        "the deleted alias must be gone"
    );
}

#[tokio::test]
async fn test_embed() {
    let ctx = Ctx::background();
    let (svc, g) = new_service(memory());
    let svc = svc.shared();
    let emb = llmcfg::Model {
        roles: vec!["methodologist".into()],
        ..model("fake", "hash")
    };
    g.seed(&[fake_provider()], &[emb], &[alias("embed", "fake/hash")]);
    let r = svc
        .embed(
            &ctx,
            llm::EmbedRequest {
                texts: vec![
                    "reset a password by mail".into(),
                    "export the report as pdf".into(),
                    "password reset".into(),
                ],
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        r.vectors.len() == 3 && r.vectors[0].len() == crate::embed::FAKE_EMBED_DIM,
        "embed: {r:?}"
    );
    // texts sharing words are nearer
    let dot = |a: &[f32], b: &[f32]| a.iter().zip(b).map(|(x, y)| x * y).sum::<f32>();
    assert!(
        dot(&r.vectors[0], &r.vectors[2]) > dot(&r.vectors[0], &r.vectors[1]),
        "similar texts must be nearer"
    );
    // the catalog policy applies like for completions
    let user = with_roles("u", &["contributor"]);
    let err = svc
        .embed(
            &user,
            llm::EmbedRequest {
                texts: vec!["x".into()],
                ..Default::default()
            },
        )
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "role required: {err}");
}
