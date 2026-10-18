use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::time::Duration;

use chrono::{DateTime, TimeZone, Utc};
use goap_authz as authz;
use goap_connect::{Code, Request};
use goap_ctx::Ctx;
use goap_gen::model::v1 as pb;
use goap_gen::model::v1::ModelService;
use goap_llm as llm;
use goap_llmcfg as llmcfg;
use parking_lot::Mutex;
use tokio::sync::watch;

use super::helpers::{
    AdminOnly, FakeGraph, alias, as_principal, behavior, calls, code_of, fake_provider, memory,
    model, new_service, rpc_client, stores, user_calls, user_msg, who,
};
use crate::behaviorcost::{COST_ESTIMATED, COST_MEASURED, cost_of};
use crate::ledger::{KIND_COMPLETE, UsageFilter};
use crate::service::Service;
use crate::store::{ModelEntry, PERIOD_TOTAL, Store};

type UsageFn = Arc<dyn Fn(&llm::Request) -> i64 + Send + Sync>;
type FailFn = Arc<dyn Fn() -> Option<llm::Error> + Send + Sync>;

#[derive(Default)]
struct HState {
    /// the requests of the callers
    sent: Vec<llm::Request>,
    /// the calibration requests
    cal: Vec<llm::Request>,
    now: DateTime<Utc>,
    /// usage, when set, replaces the reported input tokens of a calibration.
    usage: Option<UsageFn>,
    /// fail, when set, fails the calibration calls.
    fail: Option<FailFn>,
    /// gate, when set, blocks the calibration calls until it holds true.
    gate: Option<watch::Receiver<bool>>,
}

/// costHarness is a service on two fake models whose "provider" reports 10 input tokens plus one a byte of system
/// text, and records the calibration requests it receives.
struct CostHarness {
    svc: Arc<Service>,
    g: Arc<FakeGraph>,
    st: Arc<Mutex<HState>>,
}

fn new_cost_harness(
    store: Arc<dyn Store>,
    quota: i64,
    configure: impl FnOnce(&mut Service),
) -> CostHarness {
    let (mut svc, g) = new_service(store);
    let st = Arc::new(Mutex::new(HState {
        now: Utc.with_ymd_and_hms(2026, 10, 7, 10, 0, 0).unwrap(),
        ..Default::default()
    }));
    let clock = st.clone();
    svc.now = Some(Arc::new(move || clock.lock().now));
    g.seed(
        &[fake_provider()],
        &[
            ModelEntry {
                quota_tokens: quota,
                quota_period: PERIOD_TOTAL.into(),
                ..model("fake", "echo")
            },
            model("fake", "other"),
        ],
        &[alias("default", "fake/echo")],
    );
    let h = st.clone();
    svc.router.instrument = Some(Arc::new(move |ctx, _, _, req, call| {
        let h = h.clone();
        Box::pin(async move {
            let calibration = llm::meta_from(&ctx).source == llm::SOURCE_CALIBRATION;
            let r = call(ctx).await;
            if !calibration {
                h.lock().sent.push(req.clone());
                let mut r = r?;
                r.usage = llm::Usage {
                    input_tokens: 10 + req.system.len() as i64,
                    output_tokens: 1,
                    ..Default::default()
                };
                return Ok(r);
            }
            let (fail, usage, gate) = {
                let mut s = h.lock();
                s.cal.push(req.clone());
                (s.fail.clone(), s.usage.clone(), s.gate.clone())
            };
            if let Some(mut gate) = gate {
                while !*gate.borrow() {
                    if gate.changed().await.is_err() {
                        break;
                    }
                }
            }
            if let Some(fail) = fail
                && let Some(e) = fail()
            {
                return Err(e);
            }
            let mut r = r?;
            let input = match usage {
                Some(u) => u(&req),
                None => 10 + req.system.len() as i64,
            };
            r.usage = llm::Usage {
                input_tokens: input,
                output_tokens: 1,
                ..Default::default()
            };
            Ok(r)
        })
    }));
    configure(&mut svc);
    CostHarness {
        svc: svc.shared(),
        g,
        st,
    }
}

impl CostHarness {
    fn calibrations(&self) -> Vec<llm::Request> {
        self.st.lock().cal.clone()
    }

    fn advance(&self, d: chrono::Duration) {
        let mut s = self.st.lock();
        s.now += d;
    }

    fn now(&self) -> DateTime<Utc> {
        self.st.lock().now
    }

    async fn complete(&self) -> llm::Response {
        complete_on(&self.svc).await
    }
}

async fn complete_on(svc: &Service) -> llm::Response {
    let ctx = llm::with_meta(
        &Ctx::background(),
        llm::CallMeta {
            source: llm::SOURCE_ASSISTANT.into(),
            ..Default::default()
        },
    );
    svc.complete(
        &ctx,
        llm::Request {
            system: "S".into(),
            messages: user_msg("x"),
            ..Default::default()
        },
    )
    .await
    .unwrap()
}

const TERSE_TEXT: &str = "Answer tersely, drop the filler.";

#[test]
fn test_cost_arithmetic() {
    let cases: [(i64, i64, i64, &str); 5] = [
        (35, 10, 25, COST_MEASURED),
        (10, 10, 9, COST_ESTIMATED), // nothing between the two: no measure
        (8, 10, 9, COST_ESTIMATED),  // never negative
        (0, 0, 9, COST_ESTIMATED),   // no usage numbers
        (30, 0, 9, COST_ESTIMATED),  // no baseline
    ];
    for (input, base, tokens, source) in cases {
        let (tok, src) = cost_of(input, base, TERSE_TEXT);
        assert!(
            tok == tokens && src == source,
            "cost_of({input},{base}) = {tok} {src}, want {tokens} {source}"
        );
    }
}

/// The first call is priced by the estimate and asks for a calibration; once it ran, the measured cost prices the next.
#[tokio::test(flavor = "multi_thread")]
async fn test_cost_measured_on_first_use() {
    let stores = stores().await;
    for (name, store) in &stores.all {
        let h = new_cost_harness(store.clone(), 0, |_| {});
        h.g.add_behaviors(&[
            llmcfg::Behavior {
                order: 1,
                ..behavior("terse", TERSE_TEXT, true)
            },
            llmcfg::Behavior {
                order: 2,
                ..behavior("polite", "Be polite.", true)
            },
        ]);
        let r = h.complete().await;
        assert!(
            r.behaviors_estimated && r.behavior_tokens != 0,
            "{name}: first call: {r:?}"
        );
        h.svc.wait_costs().await;
        // the calibration calls bypass the behaviours: the instruction alone, then nothing (the baseline)
        let mut systems = Vec::new();
        for c in h.calibrations() {
            assert!(
                c.max_tokens == 1 && !c.json && c.messages.len() == 1,
                "{name}: not a minimal request: {c:?}"
            );
            systems.push(c.system);
        }
        for want in [TERSE_TEXT, "Be polite.", ""] {
            assert!(
                systems.iter().any(|s| s == want),
                "{name}: no calibration with system {want:?} in {systems:?}"
            );
        }
        assert_eq!(systems.len(), 3, "{name}: calibrations {systems:?}");
        let r = h.complete().await;
        assert!(
            !r.behaviors_estimated
                && r.behavior_tokens == (TERSE_TEXT.len() + "Be polite.".len()) as i64,
            "{name}: second call: {r:?}"
        );
        // the ledger: the caller's rows carry the flag, the calibration rows their own source and subject
        let (mut user, mut cal) = (0, 0);
        for c in calls(&h.svc, UsageFilter::default()).await {
            match c.source.as_str() {
                llm::SOURCE_CALIBRATION => {
                    cal += 1;
                    assert!(
                        c.subject == "system:modelgw"
                            && c.behaviors.is_empty()
                            && c.behavior_tokens == 0
                            && c.kind == KIND_COMPLETE,
                        "{name}: calibration row: {c:?}"
                    );
                }
                llm::SOURCE_ASSISTANT => user += 1,
                _ => {}
            }
        }
        let cs = user_calls(&h.svc).await;
        assert!(
            cal == 3
                && user == 2
                && cs[0].behavior_tokens_estimated
                && !cs[1].behavior_tokens_estimated,
            "{name}: ledger: {cal} calibrations, {user} callers: {cs:?}"
        );
        // stored: both costs and the baseline
        let ctx = Ctx::background();
        let costs = h.svc.store.behavior_costs(&ctx).await.unwrap();
        let bases = h.svc.store.model_baselines(&ctx).await.unwrap();
        assert!(
            costs.len() == 2 && bases.len() == 1 && bases[0].tokens == 10,
            "{name}: stored: {costs:?} {bases:?}"
        );
        for c in &costs {
            let text = if c.key.behavior == "terse" {
                TERSE_TEXT
            } else {
                "Be polite."
            };
            assert!(
                c.source == COST_MEASURED
                    && c.baseline == 10
                    && c.key.hash == llmcfg::instruction_hash(text),
                "{name}: cost: {c:?}"
            );
        }
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn test_cost_calibration_deduplicated_and_not_blocking() {
    let h = new_cost_harness(memory(), 0, |_| {});
    h.g.add_behaviors(&[behavior("terse", TERSE_TEXT, true)]);
    let (open, gate) = watch::channel(false);
    h.st.lock().gate = Some(gate); // the calibrations hang
    let mut tasks = Vec::new();
    for _ in 0..20 {
        let svc = h.svc.clone();
        tasks.push(tokio::spawn(async move { complete_on(&svc).await }));
    }
    let all = futures::future::join_all(tasks);
    // the callers did not wait for the calibration
    tokio::time::timeout(Duration::from_secs(5), all)
        .await
        .expect("the callers are blocked by the calibration");
    open.send(true).unwrap();
    h.svc.wait_costs().await;
    // the instruction and the baseline, once
    assert_eq!(h.calibrations().len(), 2, "calibrations for 20 calls");
}

#[tokio::test(flavor = "multi_thread")]
async fn test_cost_failure_backs_off() {
    let h = new_cost_harness(memory(), 0, |s| s.cost_backoff = Duration::from_secs(3600));
    h.g.add_behaviors(&[behavior("terse", TERSE_TEXT, true)]);
    let fails = Arc::new(AtomicUsize::new(0));
    let f = fails.clone();
    h.st.lock().fail = Some(Arc::new(move || {
        f.fetch_add(1, Ordering::SeqCst);
        Some(llm::Error::Message("provider down".into()))
    }));
    for _ in 0..5 {
        let r = h.complete().await;
        assert!(
            r.behaviors_estimated && r.behavior_tokens != 0,
            "the caller never sees it: {r:?}"
        );
        h.svc.wait_costs().await;
    }
    assert_eq!(
        fails.load(Ordering::SeqCst),
        1,
        "calibration attempts inside the backoff"
    );
    let costs = h
        .svc
        .store
        .behavior_costs(&Ctx::background())
        .await
        .unwrap();
    assert!(costs.is_empty(), "a failure stored {costs:?}");
    h.advance(chrono::Duration::hours(2));
    h.st.lock().fail = None;
    h.complete().await;
    h.svc.wait_costs().await;
    let r = h.complete().await;
    assert!(
        !r.behaviors_estimated,
        "not measured after the backoff: {r:?}"
    );
}

/// A provider with no usage numbers gives no measure: the estimate is stored once and not measured at every call.
#[tokio::test(flavor = "multi_thread")]
async fn test_cost_without_usage_stored_once() {
    let stores = stores().await;
    for (name, store) in &stores.all {
        let h = new_cost_harness(store.clone(), 0, |_| {});
        h.st.lock().usage = Some(Arc::new(|_| 0));
        h.g.add_behaviors(&[behavior("terse", TERSE_TEXT, true)]);
        h.complete().await;
        h.svc.wait_costs().await;
        let n = h.calibrations().len();
        for _ in 0..5 {
            let r = h.complete().await;
            assert!(
                r.behaviors_estimated && r.behavior_tokens != 0,
                "{name}: {r:?}"
            );
            h.svc.wait_costs().await;
        }
        assert_eq!(h.calibrations().len(), n, "{name}: measured again");
        let costs = h
            .svc
            .store
            .behavior_costs(&Ctx::background())
            .await
            .unwrap();
        assert!(
            costs.len() == 1
                && costs[0].source == COST_ESTIMATED
                && costs[0].tokens == llmcfg::estimate_tokens(TERSE_TEXT.len() + 2) as i64,
            "{name}: {costs:?}"
        );
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn test_cost_refresh_on_instruction_model_and_ttl() {
    let stores = stores().await;
    for (name, store) in &stores.all {
        let ctx = Ctx::background();
        let h = new_cost_harness(store.clone(), 0, |s| {
            s.cost_ttl = Duration::from_secs(24 * 3600)
        });
        h.g.add_behaviors(&[behavior("terse", TERSE_TEXT, true)]);
        h.complete().await;
        h.svc.wait_costs().await;
        // a new instruction text is another key: measured again, the old row is purged
        h.g.update(
            &llmcfg::behavior_key("terse"),
            behavior("terse", "Shorter.", true).props(),
        );
        let r = h.complete().await;
        assert!(
            r.behaviors_estimated,
            "{name}: a new instruction priced as measured: {r:?}"
        );
        h.svc.wait_costs().await;
        let r = h.complete().await;
        assert!(
            !r.behaviors_estimated && r.behavior_tokens == "Shorter.".len() as i64,
            "{name}: after the instruction changed: {r:?}"
        );
        let costs = h.svc.store.behavior_costs(&ctx).await.unwrap();
        assert!(
            costs.len() == 1 && costs[0].key.hash == llmcfg::instruction_hash("Shorter."),
            "{name}: the cost of the old text stays: {costs:?}"
        );
        // the alias retargeted to another model: measured there, the old model's rows are not read
        h.g.update(
            &llmcfg::alias_key("default"),
            alias("default", "fake/other").props(),
        );
        let r = h.complete().await;
        assert!(
            r.behaviors_estimated,
            "{name}: a new model priced as measured: {r:?}"
        );
        h.svc.wait_costs().await;
        let r = h.complete().await;
        assert!(
            !r.behaviors_estimated,
            "{name}: after the model changed: {r:?}"
        );
        let costs = h.svc.store.behavior_costs(&ctx).await.unwrap();
        assert_eq!(costs.len(), 2, "{name}: {costs:?}");
        // older than the TTL: the stored cost still prices the call, and is measured again
        let before = h.calibrations().len();
        h.advance(chrono::Duration::hours(25));
        let r = h.complete().await;
        assert!(
            !r.behaviors_estimated,
            "{name}: a stale cost is still a measure: {r:?}"
        );
        h.svc.wait_costs().await;
        assert!(
            h.calibrations().len() > before,
            "{name}: a cost older than the TTL was not measured again"
        );
        let costs = h.svc.store.behavior_costs(&ctx).await.unwrap();
        for c in &costs {
            assert!(
                c.key.model != "other" || c.measured_at > h.now() - chrono::Duration::hours(1),
                "{name}: not refreshed: {c:?}"
            );
        }
    }
}

/// An exhausted quota refuses the calibration like any call: the estimate stays and nothing is stored.
#[tokio::test]
async fn test_cost_quota_exhausted_keeps_estimate() {
    let h = new_cost_harness(memory(), 5, |_| {});
    h.g.add_behaviors(&[behavior("terse", TERSE_TEXT, true)]);
    let ctx = Ctx::background();
    h.svc
        .store
        .add_usage(&ctx, &llmcfg::model_key("fake", "echo"), PERIOD_TOTAL, 5)
        .await
        .unwrap();
    // the caller's own call is refused too, so price through the explicit trigger
    let costs = h.svc.measure_behaviors(&ctx, &[], &[]).await.unwrap();
    assert_eq!(costs.len(), 1, "{costs:?}");
    assert!(
        costs[0].error.contains("quota")
            && costs[0].source == COST_ESTIMATED
            && costs[0].tokens != 0,
        "{:?}",
        costs[0]
    );
    let stored = h.svc.store.behavior_costs(&ctx).await.unwrap();
    assert!(stored.is_empty(), "{stored:?}");
    assert!(h.calibrations().is_empty(), "the provider was called");
}

/// A calibration counts its few tokens against the quota of the model and is admitted like any call, but a model
/// whose roles exclude the gateway is still calibrated: the roles are for the callers.
#[tokio::test]
async fn test_cost_calibration_counts_quota_and_skips_roles() {
    let (svc, g) = new_service(memory());
    let svc = svc.shared();
    g.seed(
        &[fake_provider()],
        &[ModelEntry {
            quota_tokens: 1000,
            quota_period: PERIOD_TOTAL.into(),
            roles: vec!["methodologist".into()],
            ..model("fake", "echo")
        }],
        &[alias("default", "fake/echo")],
    );
    g.add_behaviors(&[behavior("terse", TERSE_TEXT, true)]);
    let root = as_principal(authz::Principal {
        subject: "root".into(),
        roles: vec!["admin".into()],
        ..Default::default()
    });
    let costs = svc.measure_behaviors(&root, &[], &[]).await.unwrap();
    assert!(
        costs.len() == 1 && costs[0].error.is_empty() && costs[0].source == COST_MEASURED,
        "{costs:?}"
    );
    let used = svc
        .store
        .usage(
            &Ctx::background(),
            &llmcfg::model_key("fake", "echo"),
            PERIOD_TOTAL,
        )
        .await
        .unwrap();
    assert_ne!(used, 0, "the calibration is not counted against the quota");
}

#[tokio::test(flavor = "multi_thread")]
async fn test_measure_behaviors_rpc_and_list_costs() {
    let h = new_cost_harness(memory(), 0, |_| {});
    h.g.add_behaviors(&[
        behavior("terse", TERSE_TEXT, true),
        behavior("off", "Off text", false),
        llmcfg::Behavior {
            models: vec!["fake/other".into()],
            ..behavior("other-only", "Other", true)
        },
    ]);
    h.g.put(
        &llmcfg::alias_key("fast"),
        llmcfg::NODE_TYPE_ALIAS,
        alias("fast", "fake/echo").props(),
    );
    let (_, rpc) = rpc_client(h.svc.clone(), Some(Arc::new(AdminOnly))).await;
    let measure = |sub: &str, names: &[&str], models: &[&str]| {
        let rpc = rpc.clone();
        let ctx = who(sub);
        let req = pb::MeasureBehaviorsRequest {
            names: names.iter().map(|s| s.to_string()).collect(),
            models: models.iter().map(|s| s.to_string()).collect(),
        };
        async move {
            rpc.measure_behaviors(&ctx, Request::new(req))
                .await
                .map(|r| r.msg)
        }
    };
    let list = || {
        let rpc = rpc.clone();
        async move {
            rpc.list_behaviors(
                &who("root"),
                Request::new(pb::ListBehaviorsRequest::default()),
            )
            .await
            .unwrap()
            .msg
        }
    };
    let preview = || {
        let rpc = rpc.clone();
        async move {
            rpc.preview_behaviors(
                &who("root"),
                Request::new(pb::PreviewBehaviorsRequest {
                    alias: "default".into(),
                    system: "S".into(),
                    ..Default::default()
                }),
            )
            .await
            .unwrap()
            .msg
        }
    };
    let r = measure("alice", &[], &[]).await;
    assert_eq!(
        code_of(&r),
        Some(Code::PermissionDenied),
        "measure by a non-admin"
    );
    assert!(
        h.calibrations().is_empty(),
        "a refused request called the model"
    );
    // before any measure: estimates, per model, with the aliases that reach it
    let l = list().await;
    let by = |n: &str| {
        l.behaviors
            .iter()
            .find(|b| b.name == n)
            .unwrap()
            .costs
            .clone()
    };
    let tc = by("terse");
    assert!(
        tc.len() == 1
            && tc[0].model == "fake/echo"
            && tc[0].aliases.join(",") == "default,fast"
            && tc[0].source == COST_ESTIMATED
            && tc[0].measured_at_ms == 0
            && tc[0].tokens != 0,
        "terse costs: {tc:?}"
    );
    let oc = by("other-only");
    assert!(
        oc.len() == 1 && oc[0].model == "fake/other" && oc[0].aliases.is_empty(),
        "other-only costs: {oc:?}"
    );
    // measure the disabled one only, on one model
    let m = measure("root", &["off"], &["fake/echo"]).await.unwrap();
    assert!(
        m.costs.len() == 1
            && m.costs[0].behavior == "off"
            && m.costs[0].source == COST_MEASURED
            && m.costs[0].tokens == "Off text".len() as i64
            && m.costs[0].measured_at_ms != 0,
        "{m:?}"
    );
    for b in list().await.behaviors {
        if b.name == "off" {
            assert!(
                b.costs.len() == 1 && b.costs[0].source == COST_MEASURED,
                "the listing does not show the measure: {:?}",
                b.costs
            );
        }
    }
    // the preview shows the cost of the previewed alias
    let p = preview().await;
    assert!(p.added_tokens_estimated && p.added_tokens != 0, "{p:?}");
    measure("root", &["terse"], &[]).await.unwrap();
    let p = preview().await;
    assert!(
        !p.added_tokens_estimated && p.added_tokens == TERSE_TEXT.len() as i32,
        "{p:?}"
    );
}
