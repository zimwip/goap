//! Go: agentcheck_test.go.

use std::sync::Arc;

use goap_ctx::Ctx;
use goap_engine::{StartRequest, StaticMethodologies, VAR_CONVERSATION};
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::json;

use crate::common::*;

/// MayRunAgent is the rule Start applies to its initiator, answered without starting anything (ADR 0090).
#[tokio::test]
async fn may_run_agent_is_the_rule_of_start() {
    let c = compile_yaml(&read_roles());
    let mut ms = StaticMethodologies::new();
    ms.insert("roled", c);
    let e = scope_engine(must_casbin(), ms);
    let dev = as_who(&principal("bob", "", &["developer"]));
    for (agent, want, roles) in [("shipper", true, 2), ("auditing", false, 1)] {
        let (ok, rs) = e
            .may_run_agent(&dev, "roled", agent, "PROJ-X")
            .await
            .unwrap();
        assert!(ok == want && rs.len() == roles, "{agent}: {ok} {rs:?}");
    }
    let err = e
        .may_run_agent(&dev, "roled", "nobody", "PROJ-X")
        .await
        .unwrap_err();
    assert!(err.is_invalid_state(), "unknown agent: {err}");
    assert!(
        e.may_run_agent(&dev, "nope", "shipper", "PROJ-X")
            .await
            .is_err(),
        "unknown methodology"
    );
}

/// A process started for a conversation of the assistant carries it to the ledger of the gateway (ADR 0090).
#[tokio::test]
async fn conversation_of_a_process_reaches_the_ledger_meta() {
    let ctx = ctx();
    let mut s = setup().await;
    let inner = scripted(&s.errs);
    let metas: Arc<Mutex<Vec<llm::CallMeta>>> = Arc::default();
    let m2 = metas.clone();
    s.llm(llm::ClientFunc::shared(
        move |ctx: Ctx, req: llm::Request| {
            let inner = inner.clone();
            let metas = m2.clone();
            async move {
                metas.lock().push(llm::meta_from(&ctx));
                inner.complete(&ctx, req).await
            }
        },
    ));
    let base = s.base.clone();
    let (e, _, _, _errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: base,
                intent: "The PSP changes its API, what does this break?".into(),
                project_id: TEST_PROJECT.into(),
                vars: obj(json!({(VAR_CONVERSATION): "CONV-7"})),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    e.run(&ctx, &p.id).await.unwrap();
    let metas = metas.lock();
    assert!(!metas.is_empty(), "no call");
    for m in metas.iter() {
        assert!(
            m.conversation_id == "CONV-7" && m.process_id == p.id,
            "meta {m:?}"
        );
    }
}
