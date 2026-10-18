//! Conditions evaluated over the blackboard of a change of a real graph (ADR 0125: goap-condition is tested without
//! the graph).

use std::sync::Arc;

use goap_condition::{Definition, Expectation, LinkSpec, ProduceSpec, compile};
use goap_ctx::Ctx;
use goap_domain::{self as domain, Blackboard, ChangeImpact};
use goap_graph::{Graph, LinkWrite, Memory, NewChange, NodeCreate};
use goap_graphtest as graphtest;
use serde_json::{Map, Value};

async fn blackboard(with_test: bool) -> Blackboard {
    let ctx = Ctx::background();
    let g = Graph::new(Arc::new(Memory::new()));
    let need = graphtest::import(
        &ctx,
        &g,
        graphtest::Node {
            key: "NEED-1".into(),
            r#type: "Need".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let req = graphtest::import(
        &ctx,
        &g,
        graphtest::Node {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            links: vec![LinkWrite {
                r#type: "satisfies".into(),
                to: need.r#ref(),
                ..Default::default()
            }],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let b = g.branch_head(&ctx, "", domain::MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "c".into(),
                baseline_id: b.id,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let r = req.r#ref();
    g.propose_impact(
        &ctx,
        &c.id,
        vec![ChangeImpact {
            intent: domain::INTENT_MODIFIED,
            pre: Some(r.clone()),
            rationale: "impacted".into(),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    if with_test {
        let mut props = Map::new();
        props.insert("title".into(), Value::from("t"));
        g.impact_node_create(
            &ctx,
            &c.id,
            NodeCreate {
                key: "TST-9".into(),
                r#type: "TestCase".into(),
                rationale: "cover REQ-1".into(),
                properties: props,
                links: vec![LinkWrite {
                    r#type: "verifies".into(),
                    to: r,
                    ..Default::default()
                }],
                ..Default::default()
            },
        )
        .await
        .unwrap();
    }
    g.blackboard(&ctx, &c.id).await.unwrap()
}

fn check(res: &goap_condition::Result, want: &[(&str, bool)]) {
    for (k, v) in want {
        match res.state.get(*k) {
            Some(got) if got == v => {}
            got => panic!("{k}: got {got:?} want {v}; errors={:?}", res.errors),
        }
    }
}

#[tokio::test]
async fn evaluate() {
    let exp = Expectation {
        for_each: "changeImpacts".into(),
        r#where: r#"x.type == "Requirement""#.into(),
        produce: ProduceSpec {
            op: "create_node".into(),
            node_type: "TestCase".into(),
        },
        link: Some(LinkSpec {
            r#type: "verifies".into(),
            ..Default::default()
        }),
    };
    let expr = exp.expr().unwrap();
    let set = compile(&[
        Definition::new("has_impacts", r#"changeImpacts.exists(n, n.intent == "modified")"#),
        Definition::new(
            "req_impacted",
            r#"changeImpacts.exists(n, n.type == "Requirement" && n.pre.out.exists(l, l.type == "satisfies" && l.to.key == "NEED-1"))"#,
        ),
        Definition::new(
            "up_to_date",
            "changeImpacts.all(n, n.pre == null || n.pre.version == n.pre.latest)",
        ),
        Definition::new("tests_proposed", expr),
        Definition::new("broken", "changeImpacts[0].nope == 1"),
    ])
    .unwrap();
    let res = set.evaluate(&blackboard(false).await);
    check(
        &res,
        &[
            ("has_impacts", true),
            ("req_impacted", true),
            ("up_to_date", true),
            ("tests_proposed", false),
        ],
    );
    assert!(
        !res.state.contains_key("broken")
            && res.errors.get("broken").is_some_and(|e| !e.is_empty()),
        "broken condition must be unknown"
    );
    let res = set.evaluate(&blackboard(true).await);
    assert!(
        res.state.get("tests_proposed").copied().unwrap_or(false),
        "tests_proposed should hold: {:?}",
        res.errors
    );
}

#[tokio::test]
async fn change_impacts() {
    let set = compile(&[
        Definition::new(
            "one_planned",
            r#"changeImpacts.filter(n, n.intent == "modified" && n.planned && n.pre.key == "REQ-1").size() == 1"#,
        ),
        Definition::new(
            "created_test",
            r#"changeImpacts.exists(n, n.intent == "created" && n.type == "TestCase" && n.pre == null && n.hasPost)"#,
        ),
        Definition::new("all_reviewed", r#"changeImpacts.all(n, n.review != "proposed")"#),
        Definition::new(
            "linked",
            r#"changeImpacts.exists(n, n.pre != null && n.pre.out.exists(l, l.type == "satisfies"))"#,
        ),
    ])
    .unwrap();
    let res = set.evaluate(&blackboard(true).await);
    check(
        &res,
        &[
            ("one_planned", true),
            ("created_test", true),
            ("all_reviewed", false),
            ("linked", true),
        ],
    );
}
