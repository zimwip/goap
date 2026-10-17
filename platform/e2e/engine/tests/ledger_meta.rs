//! Go: ledger_meta_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::LogFilter;
use goap_engine::{STATUS_COMPLETED, StartRequest};
use goap_journal as journal;
use goap_llm as llm;
use parking_lot::Mutex;

use crate::common::*;

/// Every model call of the engine declares who asked and which call of which step it is, for the ledger of the
/// gateway (ADR 0089): the same numbering as the journal, so that a ledger row finds its exchange in the log of the
/// change.
#[tokio::test]
async fn engine_stamps_its_calls_for_the_ledger() {
    let ctx = ctx();
    let mut s = setup().await;
    let inner = scripted(&s.errs);
    let metas: Arc<Mutex<Vec<llm::CallMeta>>> = Arc::default();
    let m2 = metas.clone();
    s.llm(llm::ClientFunc::shared(
        move |ctx: Ctx, req: llm::Request| {
            let (inner, metas) = (inner.clone(), m2.clone());
            async move {
                metas.lock().push(llm::meta_from(&ctx));
                inner.complete(&ctx, req).await
            }
        },
    ));
    let (e, g, base, _errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: base,
                intent: "The PSP changes its API, what does this break?".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "run: {p:?}");
    let n: usize = p.steps.iter().map(|st| st.llm_calls.len()).sum();
    let metas = metas.lock().clone();
    assert!(
        n != 0 && metas.len() == n,
        "{n} calls recorded in the steps, {} stamped",
        metas.len()
    );
    let mut seen: BTreeMap<(i64, i64), llm::CallMeta> = BTreeMap::new();
    for m in metas {
        assert!(
            m.source == llm::SOURCE_ENGINE
                && m.process_id == p.id
                && p.change_id == m.change_id
                && m.agent == p.agent
                && !m.action.is_empty(),
            "meta {m:?}"
        );
        seen.insert((m.step, m.call), m);
    }
    let (entries, _) = g
        .change_log(
            &ctx,
            &LogFilter {
                change: p.change_id.clone(),
                types: vec![format!("{}.call", journal::STREAM_MODEL)],
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(!entries.is_empty(), "exchanges");
    for st in &p.steps {
        for i in 0..st.llm_calls.len() {
            let m = seen.get(&(st.index, i as i64));
            assert!(
                m.is_some_and(|m| m.action == st.action),
                "step {} call {i} ({}): no matching stamp ({m:?})",
                st.index,
                st.action
            );
        }
    }
    // the exchange a ledger row names is found by process, step and call
    for k in seen.keys() {
        let found = entries.iter().any(|en| {
            serde_json::from_value::<journal::ModelExchange>(en.payload.clone())
                .is_ok_and(|ex| en.process == p.id && ex.step == k.0 && ex.call == k.1)
        });
        assert!(found, "no exchange for step {} call {}", k.0, k.1);
    }
}
