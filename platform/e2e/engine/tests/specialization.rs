//! Go: specialization_test.go.

use std::collections::HashMap;
use std::sync::Arc;

use goap_domain as domain;
use goap_engine::{
    ActionResult, BuiltinExecutor, BuiltinFunc, Engine, ItemInput, MemoryStore, STATUS_COMPLETED,
    STATUS_STUCK, StartRequest, StaticMethodologies, builtin,
};
use goap_graph::{Graph, Memory};
use goap_graphtest as graphtest;
use goap_journal as journal;
use goap_methodology as methodology;
use serde_json::json;

use crate::common::*;

const BUILD_METHODOLOGY: &str = r#"
name: demo
version: "1.0.0"
namespace: alm
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.target == "build")'}
actions:
  - {name: build, kind: abstract, effects: {built: true}, cost: 1}
  - {name: build_java, kind: builtin, builtin: build.java, specializes: build, when: 'vars.lang == "java"', priority: 1}
  - {name: build_any, kind: builtin, builtin: build.any, specializes: build}
goals:
  - {name: build_it, pre: {built: true}}
"#;

const GO_METHODOLOGY: &str = r#"
name: golang
version: "1.0.0"
conditions:
  - {name: x, expr: 'true'}
actions:
  - {name: noop, kind: human, effects: {x: true}}
  - {name: build_go, kind: builtin, builtin: build.go, specializes: demo/build, when: 'vars.lang == "go"', priority: 5}
goals:
  - {name: g, pre: {x: true}}
"#;

/// project_graph is an empty graph with the test project, and the head of a namespace.
async fn project_graph(ns: &str) -> (Arc<Graph>, domain::BaselineID) {
    let g = Arc::new(Graph::new(Arc::new(Memory::new())));
    graphtest::project(&ctx(), &g, TEST_PROJECT, "Test")
        .await
        .unwrap();
    let b = g
        .branch_head(&ctx(), ns, domain::MAIN_BRANCH)
        .await
        .unwrap();
    (g, b.id)
}

fn bare(g: &Arc<Graph>, ms: StaticMethodologies) -> Engine {
    let mut e = Engine::new(g.clone(), Arc::new(ms), Arc::new(MemoryStore::new()));
    e.intent = Some(goap_intent::Resolver::new(Arc::new(goap_intent::Lexical)));
    e
}

#[tokio::test]
async fn specialization_chosen_at_execution() {
    let ctx = ctx();
    let (demo, golang) = (
        compile_yaml(BUILD_METHODOLOGY),
        compile_yaml(GO_METHODOLOGY),
    );
    let acts = demo.planned_actions();
    assert!(
        acts.len() == 1 && acts[0].name == "build",
        "specializations must not be planned: {acts:?}"
    );
    let by = |tag: &'static str| -> BuiltinFunc {
        builtin(move |_ctx, _ac| async move {
            Ok(ActionResult {
                items: vec![ItemInput {
                    r#type: "artifact".into(),
                    target: "build".into(),
                    data: obj(json!({"by": tag})),
                    ..Default::default()
                }],
                ..Default::default()
            })
        })
    };
    let (g, base) = project_graph("alm").await;
    let mut e = bare(&g, StaticMethodologies::new().with(demo).with(golang));
    e.executors.insert(
        methodology::KIND_BUILTIN.into(),
        Arc::new(BuiltinExecutor(HashMap::from([
            ("build.java".to_string(), by("java")),
            ("build.any".to_string(), by("any")),
            ("build.go".to_string(), by("go")),
        ]))),
    );
    let e = Arc::new(e);
    for (lang, want) in [
        ("java", "build_java"),
        ("c", "build_any"),
        ("go", "golang/build_go"),
    ] {
        let p = e
            .start(
                &ctx,
                StartRequest {
                    methodology: "demo".into(),
                    goal: "build_it".into(),
                    baseline_id: base.clone(),
                    intent: "build".into(),
                    vars: obj(json!({"lang": lang})),
                    project_id: TEST_PROJECT.into(),
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        let p = e.run(&ctx, &p.id).await.unwrap();
        assert_eq!(p.status, STATUS_COMPLETED, "{lang}: {p:?}");
        let s = &p.steps[0];
        assert!(
            s.action == "build" && s.specialization == want,
            "{lang}: step {s:?}"
        );
        let recs = journal::read(
            &ctx,
            &*g,
            &journal::Filter {
                process_ids: vec![p.id.clone()],
                ..Default::default()
            },
        )
        .await
        .unwrap_or_default();
        let found = recs.iter().any(|r| {
            r.kind == journal::KIND_ACTION && r.specialization == want && r.action_kind == "builtin"
        });
        assert!(found, "{lang}: specialization not journaled: {recs:?}");
    }
}

#[tokio::test]
async fn abstract_action_without_specialization() {
    let src = r#"
name: abs
version: "1.0.0"
conditions: [{name: done, expr: 'artifacts.size() > 0'}]
actions: [{name: work, kind: abstract, effects: {done: true}}]
goals: [{name: g, pre: {done: true}}]
"#;
    let ctx = ctx();
    let (g, base) = project_graph("").await;
    let mut e = bare(&g, StaticMethodologies::new().with(compile_yaml(src)));
    e.max_failures = 1;
    let e = Arc::new(e);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "abs".into(),
                goal: "g".into(),
                baseline_id: base,
                intent: "x".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_STUCK && !p.steps[0].error.is_empty(),
        "abstract action must fail then disable: {p:?}"
    );
}

#[test]
fn specialization_validation() {
    let bad = r#"
name: bad
version: "1.0.0"
conditions: [{name: c, expr: 'true'}]
actions:
  - {name: a, kind: human, effects: {c: true}}
  - {name: s1, kind: human, specializes: missing}
  - {name: s2, kind: human, specializes: a, effects: {c: true}}
  - {name: s3, kind: abstract, specializes: a}
  - {name: s4, kind: human, when: 'true'}
  - {name: s5, kind: human, specializes: a, when: 'nope('}
agents: [{name: ag, actions: [s1]}]
goals: [{name: g, pre: {c: true}}]
"#;
    let m = methodology::parse(bad.as_bytes()).unwrap();
    let issues = m.validate();
    for path in [
        "actions[1].specializes",
        "actions[2].specializes",
        "actions[3].kind",
        "actions[4].when",
        "actions[5].when",
        "agents[0].actions[0]",
    ] {
        assert!(
            issues.iter().any(|is| is.path == path),
            "missing issue at {path}: {issues:?}"
        );
    }
}
