use std::collections::HashMap;
use std::sync::Arc;

use goap_authz as authz;
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_llm as llm;

use super::helpers::{
    AdminOnly, as_principal, calls, code_of, ledger_service, memory, rpc_client, user_msg, who,
};
use crate::ledger::{GROUP_END_USER, UsageFilter};

/// A call a platform service makes on behalf of a person is attributed to that person (ADR 0114): it is in their
/// consumption next to their own calls, with its exchange, and in nobody else's; a person never acts on behalf of
/// another, whatever their request says.
#[tokio::test]
async fn test_ledger_attributes_to_the_end_user() {
    let (svc, _, _) = ledger_service(memory(), &[]);
    let svc = svc.shared();
    let (c, rpc) = rpc_client(svc.clone(), Some(Arc::new(AdminOnly))).await;
    let as_ = |p: authz::Principal| {
        llm::with_meta(
            &as_principal(p),
            llm::CallMeta {
                source: llm::SOURCE_HELPER.into(),
                ..Default::default()
            },
        )
    };
    let trigger = authz::system("trigger:nightly", &[]).for_user("alice");
    let principals = [
        authz::Principal {
            subject: "alice".into(),
            ..Default::default()
        },
        trigger,
        authz::Principal {
            subject: "bob".into(),
            on_behalf_of: "alice".into(),
            ..Default::default()
        },
        authz::system("indexer", &[]),
    ];
    for p in principals {
        c.complete(
            &as_(p),
            llm::Request {
                messages: user_msg("hi"),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    }
    let all = calls(&svc, UsageFilter::default()).await;
    let want = [
        ("alice", "alice"),
        ("system:trigger:nightly", "alice"),
        ("bob", "bob"),
        ("system:indexer", ""),
    ];
    assert_eq!(all.len(), want.len(), "{all:?}");
    for (i, (s, e)) in want.iter().enumerate() {
        assert!(
            all[i].subject == *s && all[i].end_user == *e,
            "row {i}: subject {:?} end user {:?}, want {s} {e}",
            all[i].subject,
            all[i].end_user
        );
    }

    let list = |ctx: Ctx, f: Option<pb::UsageFilter>| {
        let rpc = rpc.clone();
        async move {
            rpc.list_usage(&ctx, Request::new(pb::ListUsageRequest { filter: f }))
                .await
                .map(|r| r.msg.calls)
        }
    };
    let filter = |subject: &str, end_user: &str| {
        Some(pb::UsageFilter {
            subject: subject.into(),
            end_user: end_user.into(),
            ..Default::default()
        })
    };
    let got = list(who("alice"), None).await.unwrap();
    assert!(
        got.len() == 2 && got[1].subject == "system:trigger:nightly" && got[1].end_user == "alice",
        "alice reads her calls and the ones made on her behalf: {got:?}"
    );
    let got = list(who("alice"), filter("alice", "")).await.unwrap();
    assert_eq!(got.len(), 1, "her own calls only: {got:?}");
    let got = list(who("alice"), filter("system:trigger:nightly", ""))
        .await
        .unwrap();
    assert_eq!(
        got.len(),
        1,
        "the calls of a service on her behalf: {got:?}"
    );
    let r = list(who("alice"), filter("", "bob")).await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "another person's consumption is refused"
    );
    let got = list(who("bob"), None).await.unwrap();
    assert_eq!(got.len(), 1, "bob reads his own call only: {got:?}");
    let r = rpc
        .usage_summary(
            &who("root"),
            Request::new(pb::UsageSummaryRequest {
                group_by: GROUP_END_USER.into(),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
    let by: HashMap<String, i64> = r
        .msg
        .rows
        .iter()
        .map(|row| (row.key.clone(), row.calls))
        .collect();
    assert!(
        by.get("alice") == Some(&2) && by.get("bob") == Some(&1) && by.get("") == Some(&1),
        "summary by end user: {by:?}"
    );
    // the exchange of a call made on her behalf is hers to read
    svc.call_exchange(&who("alice"), all[1].seq, false)
        .await
        .unwrap_or_else(|e| panic!("alice reads the exchange of a call made on her behalf: {e}"));
    assert!(
        svc.call_exchange(&who("bob"), all[1].seq, false)
            .await
            .is_err(),
        "bob does not read it"
    );
}
