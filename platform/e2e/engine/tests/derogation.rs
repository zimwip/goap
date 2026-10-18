//! Go: derogation_test.go.

use std::sync::Arc;

use chrono::{Duration, SecondsFormat, Utc};
use goap_domain::{self as domain, ChangeItem};
use goap_engine::{
    ActionContext, AttachRequest, Engine, Internals, Process, default_builtins,
    derogation::expire_derogations,
};
use goap_methodology::{Action, Compiled, KIND_HUMAN, Methodology, ORACLE_HUMAN, Verify};
use goap_risk as risk;
use goap_verify as verify;
use serde_json::json;

use crate::common::*;
use crate::nodeops::{on_change, root_change};

/// verified_ctx is the context of the producer "write", which declares an independent human verification.
pub fn verified_ctx(c: &goap_ctx::Ctx) -> goap_ctx::Ctx {
    let mut a = Action::default();
    a.name = "write".into();
    a.kind = KIND_HUMAN.into();
    a.verify = Some(Verify {
        oracle: ORACLE_HUMAN.into(),
        ..Default::default()
    });
    Internals::with_verify(c, &a)
}

/// run_ops runs a script of an action and applies its node operations on the change of p.
pub async fn run_ops(
    e: &Arc<Engine>,
    p: &Process,
    c: &goap_ctx::Ctx,
    action: &str,
    execution: &str,
    code: &str,
) -> goap_engine::Result<()> {
    let res = run_script(c, js(action, code)).await;
    Engine::internals_of(e)
        .apply_node_ops(c, p, res.nodes, action, execution)
        .await
        .map(|_| ())
}

/// An acceptance with a reserve stands on a derogation in force that covers the effect; expiry closes the derogation
/// and sends what it covered back to review (ADR 0075 §2).
#[tokio::test]
async fn derogation_reserve_and_expiry() {
    let ctx = ctx();
    let s = setup_with(|g| g.review_policy = Some(Arc::new(verify::Policy))).await;
    let (e, g, base, _errs) = s.share();
    let c = root_change(&g, "alm", "t", &base).await;
    let p = on_change(&c.id);
    let vctx = verified_ctx(&ctx);
    run_ops(
        &e,
        &p,
        &vctx,
        "write",
        "e1",
        r#"const r = ctx.impactNode("REQ-1", "why"); ctx.writeNode(r, { props: { title: "A" } });"#,
    )
    .await
    .unwrap();
    const RESERVE: &str = r#"ctx.impactNodeReviewWithReserve("REQ-1", "DRG-1", "good enough");"#;

    // no derogation: refused, nothing written
    let err = run_ops(&e, &p, &ctx, "check", "e2", RESERVE).await;
    assert!(
        err.as_ref()
            .is_err_and(|e| e.to_string().contains("no derogation DRG-1")),
        "a reserve without a derogation: {err:?}"
    );
    let ch = g.change(&ctx, &c.id).await.unwrap();
    assert_eq!(
        ch.nodes[0].review,
        domain::REVIEW_PROPOSED,
        "refused review wrote: {:?}",
        ch.nodes[0]
    );

    // a derogation on another target does not cover it
    let sign = |key: &'static str, target: &'static str, expires: String| {
        let (g, id) = (g.clone(), c.id.clone());
        async move {
            g.add_items(
                &crate::common::ctx(),
                &id,
                vec![ChangeItem {
                    r#type: risk::TYPE_DEROGATION,
                    data: obj(json!({"key": key, "rule": "tests pass", "target": target,
                        "reason": "deadline", "signatory": "alice", "expires": expires})),
                    ..Default::default()
                }],
            )
            .await
            .unwrap();
        }
    };
    let expires = (Utc::now() + Duration::hours(1)).to_rfc3339_opts(SecondsFormat::Secs, true);
    sign("DRG-1", "REQ-9", expires.clone()).await;
    assert!(
        run_ops(&e, &p, &ctx, "check", "e2", RESERVE).await.is_err(),
        "a derogation on another target covers nothing"
    );
    sign("DRG-1", "REQ-1", expires).await;
    // once it ran out it covers nothing either
    let x = Engine::internals_of(&e);
    x.set_clock(Some(Arc::new(|| Utc::now() + Duration::hours(2))));
    assert!(
        run_ops(&e, &p, &ctx, "check", "e2", RESERVE).await.is_err(),
        "an expired derogation covers nothing"
    );
    x.set_clock(None);
    run_ops(&e, &p, &ctx, "check", "e2", RESERVE)
        .await
        .expect("with a derogation in force");
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let s = verify::subjects(&bb.change.items);
    assert!(
        s.len() == 1
            && s[0].state == verify::ACCEPTED_WITH_RESERVE
            && s[0].derogation == "DRG-1"
            && s[0].by == "check",
        "accepted with reserve: {s:?}"
    );
    assert_eq!(bb.change.nodes[0].review, domain::REVIEW_ACCEPTED);

    // before the expiry nothing happens
    let (keys, err) = expire_derogations(&ctx, &*g, &c.id, Utc::now()).await;
    assert!(err.is_ok() && keys.is_empty(), "not yet: {keys:?} {err:?}");
    let later = Utc::now() + Duration::hours(2);
    let (keys, err) = expire_derogations(&ctx, &*g, &c.id, later).await;
    assert!(err.is_ok() && keys == ["DRG-1"], "expiry: {keys:?} {err:?}");
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let ds = risk::derogations(&bb.change);
    assert!(
        ds.len() == 1 && ds[0].status == risk::DEROGATION_CLOSED && ds[0].versions == 3,
        "closed: {ds:?}"
    );
    assert_eq!(
        bb.change.nodes[0].review,
        domain::REVIEW_PROPOSED,
        "the covered effect is reviewed again"
    );
    let s = verify::subjects(&bb.change.items);
    assert!(
        s.len() == 1
            && s[0].state == verify::PRODUCED
            && s[0].producer == "write"
            && s[0].independent,
        "verification starts over: {s:?}"
    );
    let (keys, _) = expire_derogations(&ctx, &*g, &c.id, later).await;
    assert!(keys.is_empty(), "closed once: {keys:?}");
    // the producer still may not verify its own effect
    assert!(
        run_ops(
            &e,
            &p,
            &ctx,
            "write",
            "e3",
            r#"ctx.impactNodeReview("REQ-1", true, "mine");"#
        )
        .await
        .is_err(),
        "independence survives the expiry"
    );
}

/// The builtin derogation.expire sweeps the live changes (ADR 0075 §2).
#[tokio::test]
async fn derogation_expire_builtin() {
    let ctx = ctx();
    let (_e, g, base, _errs) = setup().await.share();
    let c = root_change(&g, "alm", "t", &base).await;
    let expires =
        (Utc::now() + Duration::milliseconds(150)).to_rfc3339_opts(SecondsFormat::Nanos, true);
    g.add_items(
        &ctx,
        &c.id,
        vec![ChangeItem {
            r#type: risk::TYPE_DEROGATION,
            data: obj(json!({"key": "DRG-1", "rule": "r", "target": "REQ-1",
                "reason": "x", "signatory": "alice", "expires": expires})),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    let b = default_builtins();
    assert!(
        b.has_builtin(goap_builtins::DEROGATION_EXPIRE),
        "derogation.expire is not registered"
    );
    tokio::time::sleep(std::time::Duration::from_millis(200)).await;
    let ac = ActionContext {
        process: Default::default(),
        action: Default::default(),
        blackboard: Default::default(),
        graph: g.clone(),
        host: None,
        step: None,
        form: None,
    };
    let res = Internals::expire_all_derogations(&ctx, &ac).await;
    let out = res.as_ref().map(|r| r.output.clone()).unwrap_or_default();
    assert!(
        res.is_ok() && out.starts_with("1 derogation"),
        "sweep: {out:?} {:?}",
        res.err().map(|e| e.error)
    );
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let ds = risk::derogations(&bb.change);
    assert!(ds.len() == 1 && !ds[0].open(), "closed: {ds:?}");
}

/// The change of a methodology that names a default criticality starts with it in Change.Data (ADR 0075 §3); one that
/// names none carries none, the platform default applying.
#[tokio::test]
async fn change_starts_with_the_criticality_of_its_methodology() {
    let ctx = ctx();
    let (e, g, _, _errs) = setup().await.share();
    for (crit, want) in [("C3", "C3"), ("", "")] {
        let m = Compiled::default().with_definition(Methodology {
            name: "m".into(),
            namespace: "alm".into(),
            criticality: crit.into(),
            ..Default::default()
        });
        let mut p = Process {
            goal: "g".into(),
            ..Default::default()
        };
        let id = Engine::internals_of(&e)
            .resolve_change(&ctx, &mut p, &m, &AttachRequest::default())
            .await
            .unwrap();
        let c = g.change(&ctx, &id).await.unwrap();
        let got = c
            .data
            .get(domain::DATA_CRITICALITY)
            .and_then(|v| v.as_str())
            .unwrap_or_default();
        assert_eq!(got, want, "methodology criticality {crit:?}");
    }
}
