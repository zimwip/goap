use std::sync::Arc;

use chrono::{TimeZone, Utc};
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_journal::MAX_EXCHANGE_TEXT;
use goap_llm as llm;

use super::helpers::{
    AdminOnly, calls, code_of, ledger_service, memory, rpc_client, stores, user_msg, who,
};
use crate::ledger::{Call, Exchange, MAX_PREVIEW_TEXT, UsageFilter};

fn meta(ctx: &Ctx, m: llm::CallMeta) -> Ctx {
    llm::with_meta(ctx, m)
}

fn src(source: &str) -> llm::CallMeta {
    llm::CallMeta {
        source: source.into(),
        ..Default::default()
    }
}

fn msg(role: &str, c: &str) -> llm::Message {
    llm::Message {
        role: role.into(),
        content: c.into(),
        ..Default::default()
    }
}

/// The exchange of a call is stored by the gateway unless a change log keeps it (ADR 0089): an engine call of a
/// change.
#[tokio::test]
async fn test_exchange_stored_where_no_change_log_keeps_it() {
    let st = stores().await;
    for (name, store) in &st.all {
        let (svc, _, _) = ledger_service(store.clone(), &[]);
        let svc = svc.shared();
        let alice = who("alice");
        let msgs = vec![
            msg("user", "hello"),
            msg("assistant", "hi"),
            msg("user", "again"),
        ];
        let r = |model: &str, system: &str, messages: Vec<llm::Message>| llm::Request {
            model: model.into(),
            system: system.into(),
            messages,
            ..Default::default()
        };
        let engine = |change: &str| llm::CallMeta {
            source: llm::SOURCE_ENGINE.into(),
            process_id: "p".into(),
            change_id: change.into(),
            step: 0,
            call: 0,
            ..Default::default()
        };
        let long = "é".repeat(MAX_EXCHANGE_TEXT); // 2 bytes each
        let cases = vec![
            (
                llm::CallMeta {
                    conversation_id: "c1".into(),
                    ..src(llm::SOURCE_ASSISTANT)
                },
                r("", "be brief", msgs.clone()),
            ),
            (engine("CHG-1"), r("", "", msgs[..1].to_vec())),
            (engine(""), r("", "", msgs[..1].to_vec())), // no change
            (src(llm::SOURCE_HELPER), r("", "", user_msg("boom"))), // provider failure
            (src(llm::SOURCE_HELPER), r("nope", "", msgs[..1].to_vec())), // refused
            (
                src(llm::SOURCE_ASSISTANT),
                r("", &long, vec![msg("user", &long)]),
            ),
        ];
        for (m, req) in cases {
            let _ = svc.complete(&meta(&alice, m), req).await;
        }
        let texts: Vec<String> = ["alpha", &"b".repeat(1000), "c", "d", "e", "f", "g"]
            .iter()
            .map(|s| s.to_string())
            .collect();
        svc.embed(
            &meta(&alice, src(llm::SOURCE_INDEXER)),
            llm::EmbedRequest {
                texts,
                ..Default::default()
            },
        )
        .await
        .unwrap();
        let got = calls(&svc, UsageFilter::default()).await;
        assert_eq!(got.len(), 7, "{name}: {got:?}");
        let ctx = Ctx::background();
        let want = [true, false, true, true, true, true, true];
        for (i, c) in got.iter().enumerate() {
            assert_eq!(
                c.has_exchange, want[i],
                "{name}: row {i} ({}): has_exchange",
                c.source
            );
            let stored = store.exchange(&ctx, c.seq).await;
            assert_eq!(
                stored.is_ok(),
                want[i],
                "{name}: row {i}: stored {stored:?}"
            );
        }
        let x = store.exchange(&ctx, got[0].seq).await.unwrap();
        assert!(
            x.system == "be brief"
                && x.messages.len() == 3
                && x.messages[2].content == "again"
                && x.messages[1].role == "assistant"
                && !x.response.is_empty()
                && !x.truncated,
            "{name}: round trip: {x:?}"
        );
        let x = store.exchange(&ctx, got[3].seq).await.unwrap();
        assert!(
            x.messages.len() == 1 && !got[3].error.is_empty(),
            "{name}: a failed call keeps its request: {x:?} {:?}",
            got[3].error
        );
        let x = store.exchange(&ctx, got[4].seq).await.unwrap();
        assert!(
            x.messages.len() == 1 && got[4].error.contains("nope"),
            "{name}: a refused call keeps its request: {x:?}"
        );
        let x = store.exchange(&ctx, got[5].seq).await.unwrap();
        assert!(
            x.truncated
                && x.system.len() <= MAX_EXCHANGE_TEXT
                && x.messages[0].content.len() <= MAX_EXCHANGE_TEXT
                && x.system.len() >= MAX_EXCHANGE_TEXT - 4,
            "{name}: caps: truncated {}, {} {}",
            x.truncated,
            x.system.len(),
            x.messages[0].content.len()
        );
        let x = store.exchange(&ctx, got[6].seq).await.unwrap();
        let c = &x.messages[0].content;
        assert!(
            x.messages.len() == 1
                && x.messages[0].role == "texts"
                && c.starts_with("7 texts\n- alpha\n- bbb")
                && c.contains("... 2 more")
                && c.len() <= 5 * MAX_PREVIEW_TEXT + 200,
            "{name}: embed preview: {c:?}"
        );
        assert!(
            x.response.contains("vectors") || x.response.is_empty(),
            "{name}: embed answer: {:?}",
            x.response
        );
    }
}

#[tokio::test]
async fn test_exchange_storage_can_be_off() {
    let (mut svc, _, _) = ledger_service(memory(), &[]);
    svc.call_prompts = false;
    let svc = svc.shared();
    svc.complete(
        &meta(&who("alice"), src(llm::SOURCE_ASSISTANT)),
        llm::Request {
            messages: user_msg("secret"),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let cs = calls(&svc, UsageFilter::default()).await;
    assert!(
        cs.len() == 1 && !cs[0].has_exchange && cs[0].input_tokens == 3,
        "counters only: {cs:?}"
    );
    let err = svc
        .call_exchange(&who("alice"), cs[0].seq, false)
        .await
        .unwrap_err();
    assert!(err.is_not_found(), "{err}");
}

#[tokio::test]
async fn test_exchange_visibility() {
    let (svc, _, _) = ledger_service(memory(), &[]);
    let svc = svc.shared();
    let (_, rpc) = rpc_client(svc.clone(), Some(Arc::new(AdminOnly))).await;
    for sub in ["alice", "bob"] {
        svc.complete(
            &meta(&who(sub), src(llm::SOURCE_ASSISTANT)),
            llm::Request {
                messages: user_msg(&format!("q of {sub}")),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    }
    let get = |ctx: Ctx, seq: i64| {
        let rpc = rpc.clone();
        async move {
            rpc.get_call_exchange(&ctx, Request::new(pb::GetCallExchangeRequest { seq }))
                .await
                .map(|r| r.msg)
        }
    };
    let r = get(who("alice"), 1).await.unwrap();
    let call = r.call.clone().unwrap_or_default();
    assert!(
        call.subject == "alice"
            && call.has_exchange
            && r.messages.len() == 1
            && r.messages[0].content == "q of alice"
            && !r.response.is_empty(),
        "own call: {r:?}"
    );
    let r = get(who("alice"), 2).await;
    assert_eq!(
        code_of(&r),
        Some(Code::NotFound),
        "another user's call must not be revealed"
    );
    let r = get(who("alice"), 99).await;
    assert_eq!(
        code_of(&r),
        Some(Code::NotFound),
        "unknown seq must look the same"
    );
    let r = get(who("root"), 2).await.unwrap();
    assert!(
        r.call.as_ref().is_some_and(|c| c.subject == "bob") && r.messages[0].content == "q of bob",
        "an administrator reads any: {r:?}"
    );
    let r = get(Ctx::background(), 1).await;
    assert_eq!(code_of(&r), Some(Code::PermissionDenied), "anonymous");
}

#[tokio::test]
async fn test_exchange_is_purged_with_its_row() {
    let st = stores().await;
    for (name, store) in &st.all {
        let (mut svc, _, _) = ledger_service(store.clone(), &[]);
        let ctx = Ctx::background();
        let now = Utc.with_ymd_and_hms(2026, 9, 23, 10, 0, 0).unwrap();
        svc.now = Some(Arc::new(move || now));
        let row = |days: u64| Call {
            at: now - chrono::Days::new(days),
            source: "assistant".into(),
            step: -1,
            call_index: -1,
            ..Default::default()
        };
        let x = |response: &str| Exchange {
            response: response.into(),
            ..Default::default()
        };
        let old = store
            .append_call(&ctx, row(200), Some(x("old")))
            .await
            .unwrap();
        let recent = store
            .append_call(&ctx, row(1), Some(x("recent")))
            .await
            .unwrap();
        assert_eq!(svc.purge_calls(&ctx, 90).await.unwrap(), 1, "{name}");
        let err = store.exchange(&ctx, old).await.unwrap_err();
        assert!(
            err.is_not_found(),
            "{name}: the exchange goes with its row: {err}"
        );
        let x = store.exchange(&ctx, recent).await.unwrap();
        assert_eq!(x.response, "recent", "{name}");
    }
}
