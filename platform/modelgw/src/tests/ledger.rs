use std::sync::Arc;

use chrono::{Duration, TimeZone, Utc};
use goap_authz as authz;
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_llm as llm;

use super::helpers::{
    AdminOnly, as_principal, calls, code_of, ledger_service, memory, rpc_client, stores, user_msg,
    who,
};
use crate::ledger::{
    Call, GROUP_ACTION, GROUP_AGENT, GROUP_ALIAS, GROUP_DAY, GROUP_HOUR, GROUP_MODEL,
    GROUP_PROCESS, GROUP_SOURCE, KIND_COMPLETE, KIND_EMBED, SummaryRow, UsageFilter,
};
use crate::store::PERIOD_TOTAL;

fn req(model: &str, text: &str) -> llm::Request {
    llm::Request {
        model: model.into(),
        messages: if text.is_empty() {
            Vec::new()
        } else {
            user_msg(text)
        },
        ..Default::default()
    }
}

fn texts(v: &[&str]) -> llm::EmbedRequest {
    llm::EmbedRequest {
        texts: v.iter().map(|s| s.to_string()).collect(),
        ..Default::default()
    }
}

#[tokio::test]
async fn test_ledger_records_ok_error_and_refused() {
    let st = stores().await;
    for (name, store) in &st.all {
        let (svc, _, echo) = ledger_service(store.clone(), &["methodologist"]);
        let svc = svc.shared();
        let meth = as_principal(authz::Principal {
            subject: "m".into(),
            org: "ORG-A".into(),
            project: "P1".into(),
            roles: vec!["methodologist".into()],
            ..Default::default()
        });
        let meta = llm::CallMeta {
            source: llm::SOURCE_ENGINE.into(),
            process_id: "proc-1".into(),
            change_id: "CHG-1".into(),
            step: 2,
            call: 1,
            action: "draft".into(),
            agent: "writer".into(),
            conversation_id: "c-1".into(),
        };
        let ok = llm::with_meta(&meth, meta);
        svc.complete(&ok, req("", "hi")).await.unwrap();
        let assistant = llm::with_meta(
            &meth,
            llm::CallMeta {
                source: llm::SOURCE_ASSISTANT.into(),
                ..Default::default()
            },
        );
        assert!(
            svc.complete(&assistant, req("fake/echo", "boom"))
                .await
                .is_err(),
            "{name}: the provider failure must be returned"
        );
        // refused: forbidden, unknown alias, quota
        let forb = as_principal(authz::Principal {
            subject: "u".into(),
            roles: vec!["contributor".into()],
            ..Default::default()
        });
        let err = svc.complete(&forb, req("", "")).await.unwrap_err();
        assert!(err.is_forbidden(), "{name}: forbidden: {err}");
        let err = svc.complete(&meth, req("nope", "")).await.unwrap_err();
        assert!(err.is_invalid(), "{name}: unknown alias: {err}");
        store
            .add_usage(&Ctx::background(), &echo.key(), PERIOD_TOTAL, 100)
            .await
            .unwrap();
        let err = svc.complete(&meth, req("", "")).await.unwrap_err();
        assert!(
            err.is_quota_exceeded(),
            "{name}: quota must still be enforced: {err}"
        );
        let err = svc.embed(&meth, texts(&["x"])).await.unwrap_err();
        assert!(err.is_quota_exceeded(), "{name}: embed quota: {err}");

        let got = calls(&svc, UsageFilter::default()).await;
        assert_eq!(
            got.len(),
            6,
            "{name}: every call is a row, refusals included: {got:?}"
        );
        let a = &got[0];
        assert!(
            a.subject == "m"
                && a.project == "P1"
                && a.org == "ORG-A"
                && a.alias == "default"
                && a.provider == "fake"
                && a.model == "echo"
                && a.kind == KIND_COMPLETE
                && a.input_tokens == 3
                && a.output_tokens == 4
                && a.error.is_empty()
                && a.source == "engine"
                && a.process_id == "proc-1"
                && a.change_id == "CHG-1"
                && a.step == 2
                && a.call_index == 1
                && a.action == "draft"
                && a.agent == "writer"
                && a.conversation_id == "c-1"
                && a.seq != 0
                && a.at.timestamp() != 0,
            "{name}: ok row: {a:?}"
        );
        let b = &got[1];
        assert!(
            !b.error.is_empty()
                && b.alias.is_empty()
                && b.source == "assistant"
                && b.step == llm::NO_STEP
                && b.call_index == llm::NO_STEP,
            "{name}: a literal model, a failure, no process: {b:?}"
        );
        let c = &got[2];
        assert!(
            c.error.contains("forbidden")
                && c.subject == "u"
                && c.source == llm::SOURCE_OTHER
                && c.input_tokens == 0,
            "{name}: forbidden row: {c:?}"
        );
        let d = &got[3];
        assert!(
            d.error.contains("nope") && d.alias == "nope" && d.provider.is_empty(),
            "{name}: unresolved row: {d:?}"
        );
        let e = &got[4];
        assert!(
            e.error.contains("quota") && e.provider == "fake",
            "{name}: quota row: {e:?}"
        );
        let f = &got[5];
        assert!(
            f.kind == KIND_EMBED && f.alias == "embed" && f.error.contains("quota"),
            "{name}: embed row: {f:?}"
        );
        assert!(got[5].seq > got[0].seq, "{name}: seq must grow");
    }
}

#[tokio::test]
async fn test_ledger_embed_tokens() {
    let (svc, _, _) = ledger_service(memory(), &[]);
    svc.embed(&Ctx::background(), texts(&["a b c"]))
        .await
        .unwrap();
    let cs = calls(&svc, UsageFilter::default()).await;
    assert!(
        cs.len() == 1 && cs[0].kind == KIND_EMBED && cs[0].subject.is_empty(),
        "{cs:?}"
    );
}

#[tokio::test]
async fn test_ledger_over_rpc() {
    let (svc, _, _) = ledger_service(memory(), &[]);
    let svc = svc.shared();
    let (c, _) = rpc_client(svc.clone(), None).await;
    let ctx = as_principal(authz::Principal {
        subject: "alice".into(),
        project: "P9".into(),
        ..Default::default()
    });
    let ctx = llm::with_meta(
        &ctx,
        llm::CallMeta {
            source: "Evil Source!".into(),
            process_id: "p-7".into(),
            change_id: "CHG-7".into(),
            step: 4,
            call: 2,
            action: "act".into(),
            agent: "ag".into(),
            conversation_id: "cv".into(),
        },
    );
    c.complete(&ctx, req("", "hi")).await.unwrap();
    let ctx = llm::with_meta(
        &who("alice"),
        llm::CallMeta {
            source: llm::SOURCE_INDEXER.into(),
            ..Default::default()
        },
    );
    c.embed(&ctx, texts(&["x"])).await.unwrap();
    let cs = calls(&svc, UsageFilter::default()).await;
    assert_eq!(cs.len(), 2, "{cs:?}");
    let a = &cs[0];
    assert!(
        a.subject == "alice" && a.project == "P9",
        "the subject and the project are the principal's: {a:?}"
    );
    assert_eq!(
        a.source,
        llm::SOURCE_OTHER,
        "an invalid source is sanitised"
    );
    assert!(
        a.process_id == "p-7"
            && a.change_id == "CHG-7"
            && a.step == 4
            && a.call_index == 2
            && a.action == "act"
            && a.agent == "ag"
            && a.conversation_id == "cv",
        "correlation over RPC: {a:?}"
    );
    assert!(
        cs[1].source == llm::SOURCE_INDEXER
            && cs[1].kind == KIND_EMBED
            && cs[1].step == llm::NO_STEP,
        "embed over RPC: {:?}",
        cs[1]
    );
}

#[test]
fn test_meta_is_sanitised() {
    let long = "x".repeat(500);
    let m = llm::CallMeta {
        source: "a".repeat(41),
        process_id: "p\u{0}\n1".into(),
        change_id: long.clone(),
        action: long,
        step: 1,
        call: 1,
        ..Default::default()
    }
    .clean();
    assert!(
        m.source == llm::SOURCE_OTHER
            && m.process_id == "p1"
            && m.change_id.len() == llm::MAX_ID_LEN
            && m.action.len() == llm::MAX_NAME_LEN,
        "{m:?}"
    );
    let m = llm::CallMeta {
        source: "assistant".into(),
        step: 3,
        call: 3,
        ..Default::default()
    }
    .clean();
    assert!(
        m.step == llm::NO_STEP && m.call == llm::NO_STEP,
        "step and call mean something with a process only: {m:?}"
    );
    let m = llm::CallMeta {
        source: " Engine ".into(),
        process_id: "p".into(),
        ..Default::default()
    }
    .clean();
    assert!(m.source == "engine" && m.step == 0, "{m:?}");
}

async fn list(
    rpc: &pb::ModelServiceClient,
    ctx: &Ctx,
    f: Option<pb::UsageFilter>,
) -> Result<pb::ListUsageResponse, goap_connect::ConnectError> {
    rpc.list_usage(ctx, Request::new(pb::ListUsageRequest { filter: f }))
        .await
        .map(|r| r.msg)
}

async fn sum(
    rpc: &pb::ModelServiceClient,
    ctx: &Ctx,
    group: &str,
    f: Option<pb::UsageFilter>,
) -> Result<Vec<pb::UsageSummaryRow>, goap_connect::ConnectError> {
    rpc.usage_summary(
        ctx,
        Request::new(pb::UsageSummaryRequest {
            filter: f,
            group_by: group.into(),
        }),
    )
    .await
    .map(|r| r.msg.rows)
}

fn subject(s: &str) -> Option<pb::UsageFilter> {
    Some(pb::UsageFilter {
        subject: s.into(),
        ..Default::default()
    })
}

#[tokio::test]
async fn test_ledger_visibility() {
    let (svc, _, _) = ledger_service(memory(), &[]);
    let svc = svc.shared();
    let (c, rpc) = rpc_client(svc, Some(Arc::new(AdminOnly))).await;
    for sub in ["alice", "alice", "bob"] {
        c.complete(&who(sub), req("", "")).await.unwrap();
    }
    let r = list(&rpc, &who("alice"), None).await.unwrap();
    assert_eq!(r.calls.len(), 2, "a caller reads its own calls only");
    let r = list(&rpc, &who("alice"), subject("alice")).await.unwrap();
    assert_eq!(r.calls.len(), 2, "naming oneself");
    let r = list(&rpc, &who("alice"), subject("bob")).await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "another subject is refused"
    );
    let r = list(&rpc, &who("root"), None).await.unwrap();
    assert_eq!(r.calls.len(), 3, "an administrator reads the platform");
    let r = list(&rpc, &who("root"), subject("bob")).await.unwrap();
    assert!(
        r.calls.len() == 1 && r.calls[0].subject == "bob",
        "an administrator reads any subject: {r:?}"
    );
    // the summary has the same visibility
    let rows = sum(&rpc, &who("alice"), "subject", None).await.unwrap();
    assert!(
        rows.len() == 1
            && rows[0].key == "alice"
            && rows[0].calls == 2
            && rows[0].input_tokens == 6
            && rows[0].output_tokens == 8,
        "own summary: {rows:?}"
    );
    let rows = sum(&rpc, &who("root"), "subject", None).await.unwrap();
    assert!(
        rows.len() == 2 && rows[0].key == "alice",
        "platform summary: {rows:?}"
    );
    let r = sum(&rpc, &who("alice"), "bogus", None).await;
    assert_eq!(
        code_of(&r),
        Some(Code::InvalidArgument),
        "group_by is validated"
    );
    // without identity nothing is readable
    let r = list(&rpc, &Ctx::background(), None).await;
    assert_eq!(code_of(&r), Some(Code::PermissionDenied), "anonymous");
}

#[tokio::test]
async fn test_ledger_cursor_and_summary() {
    let st = stores().await;
    for (name, store) in &st.all {
        let (svc, _, _) = ledger_service(store.clone(), &[]);
        let ctx = Ctx::background();
        let day = Utc.with_ymd_and_hms(2026, 9, 1, 10, 30, 0).unwrap();
        let add = |at, mut c: Call| {
            let store = store.clone();
            let ctx = ctx.clone();
            async move {
                c.at = at;
                if c.source.is_empty() {
                    c.source = "engine".into();
                }
                c.step = -1;
                c.call_index = -1;
                store.append_call(&ctx, c, None).await.unwrap();
            }
        };
        let call = |sub: &str, model: &str, alias: &str, input: i64, output: i64, ms: i64| Call {
            subject: sub.into(),
            provider: "p".into(),
            model: model.into(),
            alias: alias.into(),
            input_tokens: input,
            output_tokens: output,
            duration_ms: ms,
            ..Default::default()
        };
        add(
            day,
            Call {
                process_id: "x".into(),
                action: "A1".into(),
                agent: "G".into(),
                ..call("a", "m1", "default", 10, 5, 100)
            },
        )
        .await;
        add(
            day + Duration::minutes(20),
            Call {
                error: "e".into(),
                source: "assistant".into(),
                ..call("a", "m1", "fast", 1, 1, 50)
            },
        )
        .await;
        add(
            day + Duration::hours(2),
            Call {
                process_id: "x".into(),
                action: "A2".into(),
                ..call("b", "m2", "default", 100, 0, 10)
            },
        )
        .await;
        add(
            day + Duration::hours(26),
            call("b", "m2", "default", 7, 3, 1),
        )
        .await;

        let group = |g: &'static str, f: UsageFilter| {
            let svc = &svc;
            let ctx = ctx.clone();
            async move { svc.summary(&ctx, f, g, true).await.unwrap() }
        };
        let by_model = group(GROUP_MODEL, UsageFilter::default()).await;
        let row = |key: &str, calls, input, output, errors, ms| SummaryRow {
            key: key.into(),
            calls,
            input,
            output,
            errors,
            duration_ms: ms,
        };
        assert!(
            by_model.len() == 2
                && by_model[0] == row("m2", 2, 107, 3, 0, 11)
                && by_model[1] == row("m1", 2, 11, 6, 1, 150),
            "{name}: model: {by_model:?}"
        );
        let d = group(GROUP_DAY, UsageFilter::default()).await;
        assert!(
            d.len() == 2 && d[0].key == "2026-09-01" && d[0].calls == 3 && d[1].key == "2026-09-02",
            "{name}: day: {d:?}"
        );
        let h = group(GROUP_HOUR, UsageFilter::default()).await;
        assert!(
            h.len() == 3
                && h[0].key == "2026-09-01T10"
                && h[0].calls == 2
                && h[1].key == "2026-09-01T12",
            "{name}: hour: {h:?}"
        );
        let s = group(GROUP_SOURCE, UsageFilter::default()).await;
        assert!(
            s.len() == 2 && s[0].key == "engine" && s[1].key == "assistant",
            "{name}: source: {s:?}"
        );
        let f = |sub: &str, process: &str| UsageFilter {
            subject: sub.into(),
            process_id: process.into(),
            ..Default::default()
        };
        let s = group(GROUP_ALIAS, f("a", "")).await;
        assert!(
            s.len() == 2 && s[0].key == "default",
            "{name}: alias: {s:?}"
        );
        let s = group(GROUP_PROCESS, f("", "x")).await;
        assert!(s.len() == 1 && s[0].calls == 2, "{name}: process: {s:?}");
        let s = group(GROUP_ACTION, f("", "x")).await;
        assert!(s.len() == 2 && s[0].key == "A2", "{name}: action: {s:?}");
        let s = group(GROUP_AGENT, f("", "x")).await;
        assert_eq!(s.len(), 2, "{name}: agent: {s:?}");
        // filters: time window, model as provider/model, source
        let s = group(
            GROUP_MODEL,
            UsageFilter {
                from: Some(day + Duration::hours(1)),
                to: Some(day + Duration::hours(24)),
                ..Default::default()
            },
        )
        .await;
        assert!(
            s.len() == 1 && s[0].key == "m2" && s[0].calls == 1,
            "{name}: window: {s:?}"
        );
        let s = group(
            GROUP_MODEL,
            UsageFilter {
                model: "p/m1".into(),
                source: "engine".into(),
                ..Default::default()
            },
        )
        .await;
        assert!(
            s.len() == 1 && s[0].calls == 1,
            "{name}: model filter: {s:?}"
        );

        // the cursor: latest N without after_seq, the next ones after it
        let all = calls(&svc, UsageFilter::default()).await;
        assert_eq!(all.len(), 4, "{name}: {all:?}");
        let page = |after: i64, limit: i64| {
            let svc = &svc;
            let ctx = ctx.clone();
            async move {
                svc.list_calls(
                    &ctx,
                    UsageFilter {
                        after_seq: after,
                        limit,
                        ..Default::default()
                    },
                    true,
                )
                .await
                .unwrap()
            }
        };
        let last2 = page(0, 2).await;
        assert!(
            last2.more
                && last2.calls.len() == 2
                && last2.calls[0].seq == all[2].seq
                && last2.calls[1].seq == all[3].seq,
            "{name}: latest two, ascending: {last2:?}"
        );
        let first2 = page(all[0].seq, 2).await;
        assert!(
            first2.more
                && first2.calls.len() == 2
                && first2.calls[0].seq == all[1].seq
                && first2.next == all[2].seq,
            "{name}: after the first, two: {first2:?}"
        );
        let rest = page(first2.next, 2).await;
        assert!(
            !rest.more && rest.calls.len() == 1 && rest.next == all[3].seq,
            "{name}: the rest: {rest:?}"
        );
        let none = page(rest.next, 0).await;
        assert!(
            none.calls.is_empty() && !none.more && none.next == all[3].seq,
            "{name}: nothing new keeps the cursor: {none:?}"
        );
        let got = &all[0];
        assert!(
            got.at == day && got.model == "m1" && got.provider == "p",
            "{name}: round trip: {got:?}"
        );
    }
}

#[tokio::test]
async fn test_ledger_retention() {
    let st = stores().await;
    for (name, store) in &st.all {
        let (mut svc, _, _) = ledger_service(store.clone(), &[]);
        let ctx = Ctx::background();
        let now = Utc.with_ymd_and_hms(2026, 9, 23, 10, 0, 0).unwrap();
        svc.now = Some(Arc::new(move || now));
        for age in [200, 91, 89, 1] {
            store
                .append_call(
                    &ctx,
                    Call {
                        at: now - chrono::Days::new(age),
                        source: "engine".into(),
                        step: -1,
                        call_index: -1,
                        ..Default::default()
                    },
                    None,
                )
                .await
                .unwrap();
        }
        assert_eq!(
            svc.purge_calls(&ctx, 0).await.unwrap(),
            0,
            "{name}: 0 keeps everything"
        );
        assert_eq!(svc.purge_calls(&ctx, 90).await.unwrap(), 2, "{name}: purge");
        assert_eq!(calls(&svc, UsageFilter::default()).await.len(), 2, "{name}");
        // the quota counter is not the ledger: it outlives the purge
        store
            .add_usage(&ctx, "LLM:x", PERIOD_TOTAL, 5)
            .await
            .unwrap();
        svc.purge_calls(&ctx, 1).await.unwrap();
        assert_eq!(
            store.usage(&ctx, "LLM:x", PERIOD_TOTAL).await.unwrap(),
            5,
            "{name}: usage counter"
        );
    }
}
