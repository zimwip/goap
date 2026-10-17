use std::sync::Arc;

use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_llm as llm;
use goap_llmcfg as llmcfg;

use super::helpers::{
    AdminOnly, alias, behavior, behavior_service, code_of, memory, rpc_client, stores, user_calls,
    user_msg, who,
};

fn with_source(source: &str) -> Ctx {
    llm::with_meta(
        &Ctx::background(),
        llm::CallMeta {
            source: source.into(),
            ..Default::default()
        },
    )
}

fn sys(model: &str, system: &str) -> llm::Request {
    llm::Request {
        model: model.into(),
        system: system.into(),
        messages: user_msg("x"),
        ..Default::default()
    }
}

#[tokio::test]
async fn test_behaviors_apply_to_completions() {
    let st = stores().await;
    for (name, store) in &st.all {
        let (svc, g, sent) = behavior_service(store.clone());
        let svc = svc.shared();
        g.add_behaviors(&[
            llmcfg::Behavior {
                order: 2,
                ..behavior("b-tail", "TAIL", true)
            },
            llmcfg::Behavior {
                position: llmcfg::POSITION_PREPEND.into(),
                order: 1,
                ..behavior("a-head", "HEAD", true)
            },
            behavior("off", "OFF", false),
            llmcfg::Behavior {
                sources: vec![llm::SOURCE_HELPER.into()],
                ..behavior("only-helper", "HELPER", true)
            },
            llmcfg::Behavior {
                applies_to_json: true,
                order: 5,
                ..behavior("json-ok", "JSONSTYLE", true)
            },
        ]);
        let ctx = with_source(llm::SOURCE_ASSISTANT);
        let req = llm::Request {
            system: "SYS".into(),
            messages: user_msg("hi"),
            ..Default::default()
        };
        let resp = svc.complete(&ctx, req.clone()).await.unwrap();
        assert_eq!(
            sent.lock()[0].system,
            "HEAD\n\nSYS\n\nTAIL\n\nJSONSTYLE",
            "{name}"
        );
        assert_eq!(
            req.system, "SYS",
            "{name}: the caller's request was changed"
        );
        assert!(
            resp.behaviors.join(",") == "a-head,b-tail,json-ok" && resp.behavior_tokens != 0,
            "{name}: response: {resp:?}"
        );
        // a call of the helper also gets the one scoped to it
        svc.complete(&with_source(llm::SOURCE_HELPER), req.clone())
            .await
            .unwrap();
        assert!(sent.lock()[1].system.contains("HELPER"), "{name}: helper");
        // a JSON call gets only the behaviours that ask for it, wrapped
        let mut jreq = req.clone();
        jreq.json = true;
        svc.complete(&ctx, jreq).await.unwrap();
        let want = format!("SYS\n\n{}JSONSTYLE", llmcfg::JSON_NOTICE);
        assert_eq!(sent.lock()[2].system, want, "{name}: json system");
        // the ledger: names, an estimate of the tokens, and the system as sent in the stored exchange
        let cs = user_calls(&svc).await;
        assert!(
            cs.len() == 3
                && cs[0].behaviors.join(",") == "a-head,b-tail,json-ok"
                && cs[0].behavior_tokens > 0,
            "{name}: ledger: {cs:?}"
        );
        let (_, x) = svc
            .call_exchange(&who("root"), cs[0].seq, true)
            .await
            .unwrap();
        assert_eq!(
            x.system, "HEAD\n\nSYS\n\nTAIL\n\nJSONSTYLE",
            "{name}: exchange"
        );
    }
}

#[tokio::test]
async fn test_behavior_scopes() {
    let (svc, g, sent) = behavior_service(memory());
    let svc = svc.shared();
    g.add_behaviors(&[
        llmcfg::Behavior {
            aliases: vec!["fast".into()],
            ..behavior("by-alias", "ALIAS", true)
        },
        llmcfg::Behavior {
            models: vec!["fake/echo".into()],
            ..behavior("by-model", "MODEL", true)
        },
        llmcfg::Behavior {
            models: vec!["fake/other".into()],
            ..behavior("other-model", "NOPE", true)
        },
    ]);
    g.put(
        &llmcfg::alias_key("fast"),
        llmcfg::NODE_TYPE_ALIAS,
        alias("fast", "fake/echo").props(),
    );
    let send = |m: &'static str| {
        let svc = svc.clone();
        let sent = sent.clone();
        async move {
            svc.complete(&Ctx::background(), sys(m, "S")).await.unwrap();
            sent.lock().last().unwrap().system.clone()
        }
    };
    assert_eq!(send("default").await, "S\n\nMODEL", "default");
    assert_eq!(send("fast").await, "S\n\nALIAS\n\nMODEL", "fast");
    assert_eq!(
        send("fake/echo").await,
        "S\n\nMODEL",
        "a literal model matches no alias"
    );
}

#[tokio::test]
async fn test_behaviors_never_apply_to_embeddings() {
    let (svc, g, sent) = behavior_service(memory());
    let svc = svc.shared();
    g.add_behaviors(&[behavior("all", "X", true)]);
    svc.embed(
        &Ctx::background(),
        llm::EmbedRequest {
            texts: vec!["a".into()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(
        sent.lock().is_empty(),
        "completions sent: {:?}",
        sent.lock()
    );
    let cs = user_calls(&svc).await;
    assert!(
        cs.len() == 1 && cs[0].behaviors.is_empty() && cs[0].behavior_tokens == 0,
        "{cs:?}"
    );
    let b = llmcfg::Behavior {
        kinds: vec!["embed".into()],
        ..behavior("e", "x", false)
    };
    assert!(
        b.validate().is_err(),
        "a behavior scoped to embeddings must be refused"
    );
}

#[tokio::test]
async fn test_behavior_cap() {
    let (svc, g, sent) = behavior_service(memory());
    let svc = svc.shared();
    let big = "x".repeat(3000);
    g.add_behaviors(&[
        llmcfg::Behavior {
            order: 1,
            ..behavior("one", &big, true)
        },
        llmcfg::Behavior {
            order: 2,
            ..behavior("two", &big, true)
        },
        llmcfg::Behavior {
            order: 3,
            ..behavior("three", &big, true)
        },
    ]);
    svc.complete(&Ctx::background(), sys("", "S"))
        .await
        .unwrap();
    let got = sent.lock()[0].system.len();
    assert!(
        got <= "S".len() + llmcfg::MAX_BEHAVIOR_BYTES + 2,
        "the cap is exceeded: {got} bytes"
    );
    let cs = user_calls(&svc).await;
    assert_eq!(cs[0].behaviors.join(","), "one,two,!three", "ledger");
}

#[tokio::test]
async fn test_behaviors_follow_the_graph() {
    let (svc, g, sent) = behavior_service(memory());
    let svc = svc.shared();
    g.add_behaviors(&[behavior("x", "X", true)]);
    let ctx = Ctx::background();
    let _ = svc.complete(&ctx, sys("", "S")).await;
    g.retire(&llmcfg::behavior_key("x"));
    let _ = svc.complete(&ctx, sys("", "S")).await;
    let sent = sent.lock();
    assert!(
        sent[0].system == "S\n\nX" && sent[1].system == "S",
        "{:?} {:?}",
        sent[0].system,
        sent[1].system
    );
}

#[tokio::test]
async fn test_behavior_rpc() {
    let (svc, g, _) = behavior_service(memory());
    let svc = svc.shared();
    g.add_behaviors(&[
        llmcfg::Behavior {
            order: 2,
            ..behavior("on", "ON", true)
        },
        llmcfg::Behavior {
            order: 1,
            ..behavior("off", "OFF", false)
        },
    ]);
    let (_, rpc) = rpc_client(svc.clone(), Some(Arc::new(AdminOnly))).await;
    let r = rpc
        .list_behaviors(
            &who("alice"),
            Request::new(pb::ListBehaviorsRequest::default()),
        )
        .await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "list by a non-admin"
    );
    let r = rpc
        .preview_behaviors(
            &who("alice"),
            Request::new(pb::PreviewBehaviorsRequest::default()),
        )
        .await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "preview by a non-admin"
    );
    let l = rpc
        .list_behaviors(
            &who("root"),
            Request::new(pb::ListBehaviorsRequest::default()),
        )
        .await
        .unwrap()
        .msg;
    assert!(
        l.behaviors.len() == 2
            && l.behaviors[0].name == "off"
            && l.behaviors[1].name == "on"
            && l.max_total_bytes as usize == llmcfg::MAX_BEHAVIOR_BYTES,
        "list: {l:?}"
    );
    let preview = |alias: &str, source: &str, json: bool, system: &str| {
        let rpc = rpc.clone();
        let req = pb::PreviewBehaviorsRequest {
            alias: alias.into(),
            source: source.into(),
            json,
            system: system.into(),
        };
        async move {
            rpc.preview_behaviors(&who("root"), Request::new(req))
                .await
                .map(|r| r.msg)
        }
    };
    let p = preview("default", "engine", false, "S").await.unwrap();
    assert!(
        p.system == "S\n\nON" && p.applied.len() == 1 && p.added_tokens != 0,
        "preview: {p:?}"
    );
    let p = preview("default", "", true, "S").await.unwrap();
    assert_eq!(
        p.system, "S",
        "a JSON preview skips the behaviours that do not ask for it"
    );
    let r = preview("nope", "", false, "").await;
    assert_eq!(code_of(&r), Some(Code::InvalidArgument), "unknown alias");
    // the preview calls no model: nothing is in the ledger
    let cs = user_calls(&svc).await;
    assert!(cs.is_empty(), "{cs:?}");
}

/// The behaviours the gateway added travel back to the caller over RPC (the engine's change log records them).
#[tokio::test]
async fn test_behaviors_over_rpc_response() {
    let (svc, g, _) = behavior_service(memory());
    let svc = svc.shared();
    g.add_behaviors(&[behavior("x", "X", true)]);
    let (c, _) = rpc_client(svc, None).await;
    let r = c.complete(&Ctx::background(), sys("", "S")).await.unwrap();
    assert!(
        r.behaviors.len() == 1 && r.behaviors[0] == "x" && r.behavior_tokens != 0,
        "{r:?}"
    );
}
