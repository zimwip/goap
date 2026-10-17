//! Go: sdlc_test.go.

use std::collections::{BTreeMap, BTreeSet};
use std::sync::Arc;

use goap_assess as assess;
use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_domain::{self as domain, ChangeID, ChangeItem};
use goap_dsl::NodeOp;
use goap_engine::{
    AuthzScope, Engine, HumanExecutor, InprocSandboxes, ItemInput, LLMExecutor, MemoryStore,
    Process, STATUS_COMPLETED, STATUS_WAITING, STEP_ACTIVE, STEP_DONE, STEP_SKIPPED, STEP_TODO,
    ScriptExecutor, StartRequest, StaticMethodologies, StepProgress, TASK_AGENT, default_builtins,
};
use goap_graph::{Graph, Memory, TypeCatalog};
use goap_graphtest as graphtest;
use goap_llm as llm;
use goap_methodology as methodology;
use parking_lot::Mutex;
use serde_json::{Value, json};

use crate::common::*;

/// between is the text of s between the first from and the next to.
fn between<'a>(s: &'a str, from: &str, to: &str) -> &'a str {
    let rest = s.split_once(from).map_or("", |(_, r)| r);
    rest.split_once(to).map_or(rest, |(o, _)| o)
}

/// sdlc_model_of answers the LLM actions of methodologies/sdlc.yaml, recording the impacts it qualified. The matched
/// substrings come from the prompt templates in methodologies/sdlc.yaml.
pub fn sdlc_model_of(errs: &Errs, record: Arc<Mutex<Vec<String>>>) -> Arc<dyn llm::Client> {
    let errs = errs.clone();
    llm::ClientFunc::shared(move |_ctx: Ctx, req: llm::Request| {
        let (errs, record) = (errs.clone(), record.clone());
        async move {
            let p = req.messages[0].content.clone();
            let out = if p.contains(r#""layer":"requirements""#) {
                // the first pass of the impact analysis names what the intent directly concerns
                r#"{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"NEED-1","rationale":"new payment method"}},
			{"type":"changeImpact","changeImpact":{"op":"declare","intent":"modified","key":"REQ-1","rationale":"the PSP must handle split payments"}},
			{"type":"artifact","target":"impact_pass","data":{"layer":"requirements","summary":"the payment need and its PSP requirement"}}]}"#.to_string()
            } else if p.contains(r#""target":"impact_pass","data":{"layer":""#) {
                // the next passes name nothing directly: the propagation finds their elements
                let layer = between(&p, r#""target":"impact_pass","data":{"layer":""#, "\"");
                json!({"items": [{"type": "artifact", "target": "impact_pass",
                    "data": {"layer": layer, "summary": "nothing beyond what propagates"}}]})
                .to_string()
            } else if p.contains("You qualify ONE suspected impact") {
                // one suspected impact at a time: the settlement batch, its artifact and its settlement file are not
                // concerned
                let (key, fp) = (
                    between(&p, "Suspected impact: ", " ("),
                    between(&p, r#""rated":""#, "\""),
                );
                let mut items = vec![];
                let verdict = if ["CMP-3", "ART-3", "ITF-3"].contains(&key) {
                    "refuted"
                } else {
                    "confirmed"
                };
                items.push(json!({"type": "assessment", "data": {"key": format!("impact:{key}"), "score": 4,
                    "verdict": verdict, "findings": [], "rated": fp}}));
                if verdict == "refuted" {
                    items.push(
                        json!({"type": "changeImpact", "changeImpact": {"op": "review", "node": key,
                        "accept": false, "comment": "not impacted: settlement is untouched"}}),
                    );
                    items.push(json!({"type": "changeImpact", "changeImpact": {"op": "remove", "node": key}}));
                }
                record.lock().push(key.to_string());
                json!({ "items": items }).to_string()
            } else if p.contains("Revise the impacted requirements") {
                // REQ-1 starts proposed: a transition moves it to draft, then it is written, the way the system
                // prompt's lifecycle instructions ask of any compliant agent (ADR 0076). REQ-10 is created. The review
                // accepts both, approve_requirements approves them.
                r##"{"items":[{"type":"changeImpact","changeImpact":{"op":"transition","node":"REQ-1","state":"draft"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"REQ-1","props":{"title":"Card payment (in full or in 3 installments) goes through the Acme PSP (API v2)"}}},
			{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#r1","intent":"created","type":"FunctionalRequirement","key":"REQ-10","rationale":"pay in installments"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#r1","props":{"title":"Pay in 3 installments with no fees","priority":"high"},"links":[{"type":"satisfies","to":"NEED-1"}]}}]}"##.to_string()
            } else if p.contains("Design the evolution") {
                r##"{"items":[{"type":"changeImpact","changeImpact":{"op":"declare","ref":"#c1","intent":"created","type":"Component","key":"CMP-10","rationale":"a dedicated engine"}},
			{"type":"changeImpact","changeImpact":{"op":"write","node":"#c1","props":{"title":"installments-engine","technology":"java","version":"0.0.0"},"links":[{"type":"implements","to":"FCT-1"}]}},
			{"type":"artifact","target":"design","data":{"summary":"dedicated installment scheduling engine","decisions":["new Java component"]}}]}"##.to_string()
            } else if p.contains("Write the release note") {
                r##"{"items":[{"type":"artifact","target":"release_note","data":{"markdown":"# Payment in 3 installments"}}]}"##.to_string()
            } else {
                errs.push(format!("unexpected prompt:\n{p}"));
                String::new()
            };
            Ok(llm::Response {
                text: out,
                provider: "test".into(),
                model: req.model.clone(),
                ..Default::default()
            })
        }
    })
}

/// an ordinary contributor delivers; a release manager approves the production deployment and an approver applies
/// the change (four-eyes)
pub fn dev_ctx() -> Ctx {
    as_who(&principal("dev", "acme", &["contributor"]))
}
fn rm_ctx() -> Ctx {
    as_who(&principal("rm", "acme", &["release_manager"]))
}
fn approver_ctx() -> Ctx {
    as_who(&principal("ap", "acme", &["approver"]))
}

/// SdlcSetup is what sdlc_setup builds: the engine not shared yet, the graph, the head of alm and the recording of
/// the qualified impacts.
pub struct SdlcSetup {
    pub e: Engine,
    pub g: Arc<Graph>,
    pub base: domain::BaselineID,
    pub errs: Errs,
}

/// sdlc_setup runs methodologies/sdlc.yaml on the demo ALM repository, with the types of the repository's domains.
pub async fn sdlc_setup() -> SdlcSetup {
    init();
    let ctx = dev_ctx();
    let m = methodology::load_file(path("methodologies/sdlc.yaml")).unwrap();
    let cm = Arc::new(m.compile().unwrap());
    // the graph and the engine judge by the types of the repository's domains (ADR 0012)
    let ds = goap_def::load_domains(path("domains")).unwrap();
    let cat = Arc::new(goap_typecat::new(&ds).unwrap());
    let want = m.supertypes();
    assert_eq!(
        cat.supertypes().get("alm@SecurityRequirement"),
        want.get("alm@SecurityRequirement"),
        "catalogue supertypes"
    );
    let mut g = Graph::new(Arc::new(Memory::new()));
    {
        let cat: Arc<dyn TypeCatalog> = cat.clone();
        g.types = Some(Arc::new(move || Some(cat.clone())));
    }
    g.caller = Some(Arc::new(goap_graphsvc::caller));
    let g = Arc::new(g);
    goap_devseed::demo(&ctx, &g).await.unwrap();
    graphtest::project(&ctx, &g, TEST_PROJECT, "Test")
        .await
        .unwrap();
    // the latest baseline (SeedDemo imports the organisation and the alm data as a change each, so the head is the
    // one the last of them left)
    let head = g
        .branch_head(&ctx, "alm", domain::MAIN_BRANCH)
        .await
        .unwrap();
    let errs = Errs::default();
    let mut e = Engine::new(
        g.clone(),
        Arc::new(StaticMethodologies::new().with(cm)),
        Arc::new(MemoryStore::new()),
    );
    e.executors.insert(
        methodology::KIND_LLM.into(),
        Arc::new(LLMExecutor {
            client: Some(sdlc_model_of(&errs, Arc::default())),
        }),
    );
    e.executors
        .insert(methodology::KIND_HUMAN.into(), Arc::new(HumanExecutor));
    e.executors.insert(
        methodology::KIND_SCRIPT.into(),
        Arc::new(ScriptExecutor {
            sandboxes: Some(Arc::new(InprocSandboxes)),
        }),
    );
    e.executors.insert(
        methodology::KIND_BUILTIN.into(),
        Arc::new(default_builtins()),
    );
    e.intent = Some(goap_intent::Resolver::new(Arc::new(goap_intent::Lexical)));
    e.scope = Some(Arc::new(AuthzScope {
        authz: Some(must_casbin()),
        hub: None,
    }));
    {
        let cat: Arc<dyn goap_def::TypeSet> = cat;
        e.types = Some(Arc::new(move || Some(cat.clone())));
    }
    SdlcSetup {
        e,
        g,
        base: head.id,
        errs,
    }
}

/// validation is what a person submits to validate the impact analysis.
pub fn validation() -> ItemInput {
    ItemInput {
        r#type: "artifact".into(),
        target: "impact_validation".into(),
        data: obj(json!({"accepted": true, "comment": "the four layers are right"})),
        ..Default::default()
    }
}

/// qualified are the subjects the qualify step of the impact analysis ran for, in order.
fn qualified(p: &Process) -> Vec<String> {
    p.steps
        .iter()
        .filter(|s| !s.subject.is_empty())
        .map(|s| s.subject.clone())
        .collect()
}

/// check_impact_analysis checks what the four passes of the impact analysis left: the propagated impacts qualified
/// once each, the ones the change does not concern rejected with their reason and withdrawn.
async fn check_impact_analysis(g: &Graph, id: &ChangeID, qualified: &[String]) {
    let mut seen = BTreeSet::new();
    for k in qualified {
        assert!(seen.insert(k.clone()), "{k} qualified twice: {qualified:?}");
    }
    for k in ["CMP-3", "ART-3", "ITF-3"] {
        assert!(seen.contains(k), "{k} not qualified: {qualified:?}");
    }
    let c = g.change(&ctx(), id).await.unwrap();
    let keys: BTreeSet<_> = c.nodes.iter().map(|n| n.key.clone()).collect();
    for k in ["NEED-1", "REQ-1", "FCT-1", "APP-1", "CMP-1"] {
        assert!(keys.contains(k), "{k} is not an impact: {keys:?}");
    }
    for k in ["CMP-3", "ART-3", "ITF-3"] {
        assert!(
            !keys.contains(k),
            "{k} is not concerned and must be withdrawn"
        );
    }
}

/// review_proposed reviews every proposed change impact, taking out a candidate the run did not modify (ADR 0102).
fn review_proposed(c: &domain::Change) -> Vec<ItemInput> {
    c.nodes
        .iter()
        .filter(|n| n.review == domain::REVIEW_PROPOSED)
        .map(|n| {
            let op = if n.post.is_none() {
                NodeOp {
                    op: "remove".into(),
                    node: n.key.clone(),
                    ..Default::default()
                }
            } else {
                NodeOp {
                    op: "review".into(),
                    node: n.key.clone(),
                    accept: true,
                    comment: "reviewed".into(),
                    ..Default::default()
                }
            };
            ItemInput {
                r#type: "changeImpact".into(),
                change_impact: Some(op),
                ..Default::default()
            }
        })
        .collect()
}

#[tokio::test]
async fn sdlc_delivery() {
    let (ctx, rm, approver) = (dev_ctx(), rm_ctx(), approver_ctx());
    let SdlcSetup {
        e,
        g,
        base,
        errs: _errs,
    } = sdlc_setup().await;
    let e = Arc::new(e);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "sdlc".into(),
                agent: "delivery".into(),
                goal: "deliver".into(),
                baseline_id: base,
                title: "Payment in 3 installments".into(),
                intent: "Allow payment in 3 installments with no fees".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    // run as the background scheduler does, without a caller: the run acts for the initiator
    let p = Box::pin(e.run(&crate::common::ctx(), &p.id)).await.unwrap();
    // the impact analysis runs its four passes, then a person validates it
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.action == "validate_impact_analysis"),
        "must wait for the validation of the impact analysis: {} {} {:?}",
        p.status,
        p.error,
        p.pending
    );
    check_impact_analysis(&g, &p.change_id, &qualified(&p)).await;
    e.submit(&ctx, &p.id, vec![validation()]).await.unwrap();
    let p = Box::pin(e.run(&crate::common::ctx(), &p.id)).await.unwrap();
    assert!(
        p.status == STATUS_WAITING && p.pending.as_ref().is_some_and(|t| t.action == "review"),
        "must wait for the review: {} {} {:?}",
        p.status,
        p.error,
        p.steps
    );
    let actions: Vec<_> = p.steps.iter().map(|s| s.action.clone()).collect();
    let builds: Vec<_> = p
        .steps
        .iter()
        .filter(|s| s.action == "build")
        .map(|s| s.specialization.clone())
        .collect();
    assert_eq!(
        builds,
        ["build_java", "build_c", "build_generic"],
        "builds (steps {actions:?})"
    );
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    // every operation of the run on the change impacts is an event linked to its action run (ADR 0029)
    let evs = g.change_events(&ctx, &p.change_id).await.unwrap();
    assert!(!evs.is_empty(), "impact log: no events");
    for ev in &evs {
        assert!(
            !((ev.op == domain::IMPACT_PROPOSED || ev.op.writes_post()) && ev.execution.is_empty()),
            "event {} ({} of {}) has no action run",
            ev.seq,
            ev.op,
            ev.impact
        );
        // a review an action makes is the action's (the verifier is not the producer, ADR 0075)
        assert!(
            ev.by == "dev" || (ev.op == domain::IMPACT_REVIEWED && ev.by == "qualify_impact"),
            "event {} ({} of {}) is recorded for {:?}, not the initiator",
            ev.seq,
            ev.op,
            ev.impact,
            ev.by
        );
    }
    let mut count: BTreeMap<String, i32> = BTreeMap::new();
    for n in c
        .nodes
        .iter()
        .filter(|n| n.intent == domain::INTENT_CREATED)
    {
        *count.entry(n.r#type.clone()).or_default() += 1;
    }
    for it in c
        .items
        .iter()
        .filter(|it| it.r#type == domain::TYPE_ARTIFACT)
    {
        *count.entry(format!("artifact:{}", it.target)).or_default() += 1;
        if it.target == "design_check" {
            assert_eq!(
                it.data.get("ok"),
                Some(&Value::Bool(true)),
                "design check: {:?}",
                it.data
            );
        }
    }
    let n = |k: &str| count.get(k).copied().unwrap_or_default();
    // CMP-1, CMP-10 (java), CMP-4 (c), CMP-2 (go, generic)
    assert!(
        n("alm@BuildArtifact") == 4
            && n("artifact:build") == 4
            && n("alm@TestCase") == 2
            && n("alm@FunctionalRequirement") == 1,
        "items: {count:?}"
    );
    e.submit(&ctx, &p.id, review_proposed(&c)).await.unwrap();
    // dev, test, staging are deployed; production waits for a release manager
    let p = Box::pin(e.run(&ctx, &p.id)).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.permission == "release:deploy"),
        "production must wait for a release manager: {} {} {:?}",
        p.status,
        p.error,
        p.pending
    );
    assert!(
        e.approve(&approver, &p.id, true, "").await.is_err(),
        "an approver is not a release manager"
    );
    e.approve(&rm, &p.id, true, "Thursday window")
        .await
        .unwrap();
    // the change itself is applied by an approver (four-eyes)
    let p = Box::pin(e.run(&ctx, &p.id)).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.permission == "change:apply"),
        "apply must wait for an approver: {} {:?}",
        p.status,
        p.pending
    );
    e.approve(&approver, &p.id, true, "").await.unwrap();
    let p = Box::pin(e.run(&ctx, &p.id)).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "delivery: {}", p.error);
    let deploys: Vec<_> = p
        .steps
        .iter()
        .filter(|s| s.action == "deploy")
        .map(|s| s.specialization.clone())
        .collect();
    assert_eq!(
        deploys,
        [
            "deploy_auto",
            "deploy_auto",
            "deploy_staging",
            "deploy_production"
        ],
        "deployment waves"
    );
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    let (nodes, links) = g.baseline_graph(&ctx, &c.result_baseline_id).await.unwrap();
    let mut by_key = BTreeMap::new();
    let mut types: BTreeMap<String, i32> = BTreeMap::new();
    for n in &nodes {
        by_key.insert(n.key.clone(), n.clone());
        *types.entry(n.r#type.clone()).or_default() += 1;
    }
    // releases of APP-1 (CMP-1, CMP-4) and APP-2 (CMP-2), each on 4 environments (+1 seeded deployment)
    assert!(
        types.get("alm@Release") == Some(&3) && types.get("alm@Deployment") == Some(&9),
        "releases / deployments: {types:?}"
    );
    let art = &by_key["ART-CMP-10-0.0.1"];
    assert_eq!(
        art.properties.get("coordinates"),
        Some(&Value::from("com.acme:installments-engine:0.0.1")),
        "java artifact: {art:?}"
    );
    let id = |k: &str| by_key.get(k).map(|n| n.id.clone()).unwrap_or_default();
    let rel = id("REL-APP-1-5.3");
    let has = |t: &str, from: &domain::NodeID, to: &domain::NodeID| {
        links
            .iter()
            .any(|l| l.r#type == t && l.from.id == *from && l.to.id == *to)
    };
    let realized = has("alm@realizes", &id("FCT-1"), &id("REQ-10"));
    let deployed = has("alm@deploys", &id("APP-1"), &id("ART-CMP-1-1.4.3"));
    let contains = has("alm@contains", &rel, &id("ART-CMP-4-3.0.2"));
    let in_prod = has(
        "alm@in_environment",
        &id("DEP-REL-APP-1-5.3-ENV-PRD"),
        &id("ENV-PRD"),
    );
    assert!(
        realized && deployed && contains && in_prod,
        "traceability: realized={realized} deployed={deployed} contains={contains} inProd={in_prod}"
    );
}

fn collapse(steps: impl Iterator<Item = String>) -> Vec<String> {
    let mut out: Vec<String> = Vec::new();
    for s in steps {
        if out.last() != Some(&s) {
            out.push(s);
        }
    }
    out
}

/// The change goes through the software_delivery process of the SDLC (ADR 0034): a manual framing step, the analysis
/// sub-steps done by actions or their alternatives, the design by the architect agent, the build, then the
/// release_train process nested as a sub-agent, and the application.
#[tokio::test]
async fn sdlc_process() {
    // the roles of the process's steps, held in the default unit (the one holding the change) or unscoped
    let ctx = as_who(&principal(
        "dev",
        "acme",
        &["contributor", "business_analyst@ORG-DEFAULT", "tech_lead"],
    ));
    let bg = crate::common::ctx();
    let SdlcSetup {
        e,
        g,
        base,
        errs: _errs,
    } = sdlc_setup().await;
    let e = Arc::new(e);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "sdlc".into(),
                goal: "software_delivery".into(),
                baseline_id: base,
                title: "Payment in 3 installments".into(),
                intent: "Allow payment in 3 installments with no fees".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    // the analysis waits for the description to be accepted (ADR 0110): the automatic review of the framing rated it
    // (ADR 0130), so the run does not rate it itself
    g.add_items(
        &ctx,
        &p.change_id,
        vec![ChangeItem {
            r#type: assess::TYPE_ASSESSMENT,
            status: domain::ITEM_PROPOSED,
            produced_by: "rate_description".into(),
            data: obj(json!({"key": assess::SUBJECT_DESCRIPTION, "score": 4, "verdict": assess::VERDICT_CLEAR,
                "rated": "Payment in 3 installments\n\nAllow payment in 3 installments with no fees"})),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    let p = Box::pin(e.run(&bg, &p.id)).await.unwrap();
    assert!(
        p.agent == "software_delivery"
            && p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.action == "software_delivery/framing/confirmation"
                    && !t.instructions.is_empty()),
        "the process starts with the confirmation of the framing, its description reviewed: {} {} {:?} {}",
        p.agent,
        p.status,
        p.pending,
        p.error
    );
    let c = p.pending.as_ref().and_then(|t| t.context.clone());
    assert!(
        c.as_ref().is_some_and(|c| !c.guidance.is_empty()
            && c.checklist.len() == 3
            && c.references.len() == 1),
        "the framing task carries its guidance, checklist and references: {c:?}"
    );
    e.submit(&ctx, &p.id, vec![]).await.unwrap();
    let p = Box::pin(e.run(&bg, &p.id)).await.unwrap();
    // the impacts step names a capability: the impact_analysis method runs its four passes as a sub-agent, then waits
    // for a person to validate them
    let pending = p.pending.clone().unwrap_or_default();
    assert!(
        p.status == STATUS_WAITING
            && pending.kind == TASK_AGENT
            && pending.action == "software_delivery/analysis/impacts",
        "expected the impact analysis: {} {} {:?}",
        p.status,
        p.error,
        p.pending
    );
    let analysis = get(&e, &pending.child_process_id).await;
    assert!(
        analysis.agent == "impact_analysis"
            && analysis
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "impact_analysis/validation"),
        "impact analysis {} {} {:?}",
        analysis.status,
        analysis.error,
        analysis.pending
    );
    let passes = collapse(analysis.steps.iter().map(|s| s.action.clone()));
    let want_passes = [
        "impact_analysis/requirements/identify:identify_requirements",
        "impact_analysis/requirements/propagate",
        "impact_analysis/requirements/qualify",
        "impact_analysis/functions/identify",
        "impact_analysis/functions/propagate",
        "impact_analysis/functions/qualify",
        "impact_analysis/applications/identify",
        "impact_analysis/applications/propagate",
        "impact_analysis/applications/qualify",
        "impact_analysis/components/identify",
        "impact_analysis/components/propagate",
        "impact_analysis/components/qualify",
        "impact_analysis/validation",
    ];
    assert_eq!(passes, want_passes, "passes");
    check_impact_analysis(&g, &p.change_id, &qualified(&analysis)).await;
    e.submit(&ctx, &analysis.id, vec![validation()])
        .await
        .unwrap();
    let analysis = Box::pin(e.run(&ctx, &analysis.id)).await.unwrap();
    assert_eq!(
        analysis.status, STATUS_COMPLETED,
        "impact analysis: {}",
        analysis.error
    );
    Box::pin(e.drain()).await;
    let p = get(&e, &p.id).await;
    // the nested release process waits for the review
    let pending = p.pending.clone().unwrap_or_default();
    assert!(
        p.status == STATUS_WAITING
            && pending.kind == TASK_AGENT
            && pending.action == "software_delivery/release",
        "expected the release train: {} {} {:?}",
        p.status,
        p.error,
        p.pending
    );
    let steps = collapse(p.steps.iter().map(|s| s.action.clone()));
    let want = [
        "software_delivery/framing/confirmation",
        "software_delivery/analysis/impacts",
        // the traceability sub-step is skipped: its criteria hold (the new requirement satisfies a need)
        "software_delivery/analysis/specification/requirements:specify_requirements",
        "software_delivery/design",
        "software_delivery/analysis/specification/test_plan",
        "software_delivery/build",
        "software_delivery/release",
    ];
    assert_eq!(steps, want, "steps");
    // where the delivery stands while the release train works
    let pr = e.progress(&ctx, &p.id).await.unwrap();
    let mut states = BTreeMap::new();
    fn walk(steps: &[StepProgress], states: &mut BTreeMap<String, String>) {
        for s in steps {
            states.insert(s.path.clone(), s.state.clone());
            walk(&s.steps, states);
        }
    }
    walk(&pr.steps, &mut states);
    for (path, want) in [
        ("software_delivery/framing", STEP_DONE),
        ("software_delivery/analysis", STEP_DONE),
        (
            "software_delivery/analysis/specification/traceability",
            STEP_SKIPPED,
        ),
        ("software_delivery/design", STEP_DONE),
        ("software_delivery/build", STEP_DONE),
        ("software_delivery/release", STEP_ACTIVE),
        ("software_delivery/application", STEP_TODO),
    ] {
        assert_eq!(
            states.get(path).map(String::as_str),
            Some(want),
            "{path} ({states:?})"
        );
    }
    // the framing counts its review and its confirmation
    assert!(
        pr.total == 10 && pr.done == 8,
        "progress {}/{}",
        pr.done,
        pr.total
    );
    // the build step names a capability: no component has a repository, so the recorded_build method was chosen
    // (ADR 0118), and the specializations of its build action apply
    let mut builds = Vec::new();
    for s in p
        .steps
        .iter()
        .filter(|s| s.action == "software_delivery/build" && !s.children.is_empty())
    {
        assert_eq!(s.specialization, "recorded_build", "build method");
        let builder = get(&e, &s.children[0]).await;
        assert_eq!(builder.status, STATUS_COMPLETED, "build step: {builder:?}");
        builds.extend(builder.steps.iter().map(|bs| bs.specialization.clone()));
    }
    assert_eq!(builds, ["build_java", "build_c", "build_generic"], "builds");
    // the design step names a capability: components are impacted, so the solution_design method was chosen, and its
    // agent generated for the method ran as a sub-agent on the same change, towards its goal
    for s in p
        .steps
        .iter()
        .filter(|s| s.action == "software_delivery/design" && !s.children.is_empty())
    {
        assert_eq!(s.specialization, "solution_design", "design method");
        let architect = get(&e, &s.children[0]).await;
        assert!(
            architect.agent == "solution_design"
                && architect.goal == "solution_design"
                && architect.change_id == p.change_id
                && architect.status == STATUS_COMPLETED
                && architect
                    .step
                    .as_ref()
                    .is_some_and(|st| st.method == "solution_design"),
            "design step: {architect:?}"
        );
    }
    let train = get(&e, &pending.child_process_id).await;
    assert!(
        train.agent == "release_train"
            && train.parent_id == p.id
            && train
                .pending
                .as_ref()
                .is_some_and(|t| t.action == "release_train/review"),
        "release train {train:?}"
    );
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    e.submit(&ctx, &train.id, review_proposed(&c))
        .await
        .unwrap();
    let train = Box::pin(e.run(&ctx, &train.id)).await.unwrap();
    assert!(
        train
            .pending
            .as_ref()
            .is_some_and(|t| t.permission == "release:deploy"),
        "production must wait for a release manager: {train:?}"
    );
    e.approve(&rm_ctx(), &train.id, true, "").await.unwrap();
    let train = Box::pin(e.run(&ctx, &train.id)).await.unwrap();
    assert_eq!(
        train.status, STATUS_COMPLETED,
        "release train: {}",
        train.error
    );
    Box::pin(e.drain()).await;
    // back in the delivery: the application waits for an approver (four-eyes)
    let p = get(&e, &p.id).await;
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.permission == "change:apply"
                    && t.action == "software_delivery/application"),
        "application must wait for an approver: {} {:?}",
        p.status,
        p.pending
    );
    // the accountable role of the application step answers for it: a change_approver of the unit may approve the
    // gate, without the approver role the change:apply permission asks for; never on its own change
    let roles = p
        .pending
        .as_ref()
        .and_then(|t| t.context.as_ref())
        .and_then(|c| c.roles.clone());
    assert!(
        roles
            .as_ref()
            .is_some_and(|r| r.accountable == "change_approver"),
        "the application gate carries its roles: {roles:?}"
    );
    let own = as_who(&principal("dev", "acme", &["change_approver"]));
    let err = e
        .approve(&own, &p.id, true, "")
        .await
        .expect_err("an accountable role never approves its own change");
    assert!(
        err.is_forbidden(),
        "an accountable role never approves its own change: {err}"
    );
    let accountable = as_who(&Principal {
        subject: "ca".into(),
        org: "acme".into(),
        roles: vec!["change_approver@ORG-DEFAULT".into()],
        ..Default::default()
    });
    e.approve(&accountable, &p.id, true, "").await.unwrap();
    let p = Box::pin(e.run(&ctx, &p.id)).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "delivery: {}", p.error);
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert_eq!(c.status, domain::CHANGE_APPLIED, "change");
}
