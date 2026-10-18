//! Go: decisions_test.go.

use std::sync::Arc;

use goap_decision as decision;
use goap_domain::{self as domain};
use goap_engine::decisions::DecisionOp;
use goap_engine::{
    Engine, InprocSandboxes, MemoryStore, Process, STATUS_COMPLETED, ScriptExecutor, StartRequest,
    StaticMethodologies, default_builtins,
};
use goap_graph::{Graph, Memory, NewChange, OpenOptionRequest};
use goap_graphtest as graphtest;
use goap_methodology as methodology;
use serde_json::json;

use crate::common::*;

/// decision_graph is a graph with the confidence policy and REQ-1, and the head of alm.
async fn decision_graph(title: Option<&str>) -> (Arc<Graph>, domain::BaselineID) {
    let mut g = Graph::new(Arc::new(Memory::new()));
    g.decision_policy = Some(Arc::new(decision::Policy));
    let g = Arc::new(g);
    let mut props = serde_json::Map::new();
    if let Some(t) = title {
        props.insert("title".into(), json!(t));
    }
    graphtest::import(
        &ctx(),
        &g,
        graphtest::Node {
            namespace: "alm".into(),
            key: "REQ-1".into(),
            r#type: "alm@Requirement".into(),
            properties: props,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let b = g
        .branch_head(&ctx(), "alm", domain::MAIN_BRANCH)
        .await
        .unwrap();
    (g, b.id)
}

/// The decision loop end to end (ADR 0009 §4, methodologies/examples/option-decision.yaml): the decider opens a
/// decision point on the options, rules it undecidable with a question, the question is investigated by a sub-agent
/// that identification picks from the question, and the decider comes back to decide: the option is selected.
#[tokio::test]
async fn decision_loop_end_to_end() {
    init();
    let ctx = ctx();
    let cm = load_methodology("option-decision.yaml");
    let (g, base) = decision_graph(Some("Pay online")).await;
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "PSP".into(),
                intent: "choose a payment provider".into(),
                namespace: "alm".into(),
                baseline_id: base,
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let stripe = g
        .open_option(
            &ctx,
            &c.id,
            OpenOptionRequest {
                name: "stripe".into(),
                hypothesis: "Stripe covers every market".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    g.open_option(
        &ctx,
        &c.id,
        OpenOptionRequest {
            name: "adyen".into(),
            hypothesis: "Adyen is cheaper".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let mut e = Engine::new(
        g.clone(),
        Arc::new(StaticMethodologies::new().with(cm.clone())),
        Arc::new(MemoryStore::new()),
    );
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
    let e = Arc::new(e);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: cm.name().into(),
                agent: "decider".into(),
                goal: "decide_option".into(),
                change_id: c.id.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    let steps: Vec<_> = p.steps.iter().map(|s| s.action.clone()).collect();
    assert!(
        p.status == STATUS_COMPLETED,
        "status {} ({}) world={:?} unknown={:?} steps={steps:?}",
        p.status,
        p.error,
        p.world,
        p.unknown
    );
    assert_eq!(steps.join(","), "open_decision,decide,investigate,decide");
    let points = g.decision_points(&ctx, &c.id).await.unwrap();
    assert_eq!(points.len(), 1, "points: {points:?}");
    let d = &points[0];
    assert!(
        d.status == domain::POINT_DECIDED
            && d.option == stripe.id
            && decision::rounds(d) == 1
            && d.questions.len() == 1,
        "decision: {d:?}"
    );
    let q = &d.questions[0];
    assert!(
        q.status == domain::QUESTION_ANSWERED && !q.process.is_empty(),
        "the question is answered by the investigation: {q:?}"
    );
    let child = e.store.get(&ctx, &q.process).await.unwrap();
    assert!(
        child.agent == "analyst" && child.status == STATUS_COMPLETED && child.parent_id == p.id,
        "investigation: {child:?}"
    );
    let opts = g.options(&ctx, &c.id).await.unwrap();
    assert!(
        opts[0].option_status() == domain::OPTION_SELECTED
            && opts[1].option_status() == domain::OPTION_REJECTED,
        "options: {opts:?}"
    );
}

/// A ruling below the threshold waits for a person; an agent may not ratify it, a human task may.
#[tokio::test]
async fn decision_ratification_is_human() {
    init();
    let ctx = ctx();
    let (g, base) = decision_graph(None).await;
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "x".into(),
                namespace: "alm".into(),
                baseline_id: base,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let e = Arc::new(Engine::new(
        g.clone(),
        Arc::new(StaticMethodologies::new()),
        Arc::new(MemoryStore::new()),
    ));
    let x = Engine::internals_of(&e);
    let p = Process {
        id: "p".into(),
        change_id: c.id.clone(),
        ..Default::default()
    };
    let pts = x
        .apply_decision_ops(
            &ctx,
            &p,
            vec![
                DecisionOp {
                    op: "open".into(),
                    r#ref: "#d".into(),
                    question: "Go?".into(),
                    policy: obj(json!({"threshold": 0.8})),
                    ..Default::default()
                },
                DecisionOp {
                    op: "rule".into(),
                    point: "#d".into(),
                    outcome: domain::OUTCOME_DECIDED.into(),
                    confidence: 0.5,
                    justification: "probably".into(),
                    ..Default::default()
                },
            ],
            false,
            "decider",
        )
        .await
        .unwrap();
    assert_eq!(pts.len(), 1, "open and rule: {pts:?}");
    let ratify = || {
        vec![DecisionOp {
            op: "ratify".into(),
            point: pts[0].clone(),
            accept: true,
            ..Default::default()
        }]
    };
    assert!(
        x.apply_decision_ops(&ctx, &p, ratify(), false, "decider")
            .await
            .is_err(),
        "an agent ratified a ruling"
    );
    x.apply_decision_ops(&ctx, &p, ratify(), true, "ann")
        .await
        .unwrap();
    let d = g.decision_points(&ctx, &c.id).await.unwrap();
    assert!(
        d[0].status == domain::POINT_DECIDED && d[0].decided_by == "ann",
        "ratified: {:?}",
        d[0]
    );
}
