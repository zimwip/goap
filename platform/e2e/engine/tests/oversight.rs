//! Go: oversight_test.go.

use goap_domain::{self as domain, LogEntry};
use goap_dsl::NodeOp;
use goap_engine::{Engine, Internals};
use goap_methodology::{self as methodology, Action};
use serde_json::json;

use crate::common::*;
use crate::nodeops::{on_change, root_change};

fn action(name: &str, kind: &str) -> Action {
    let mut a = Action::default();
    a.name = name.into();
    a.kind = kind.into();
    a
}

fn op(o: &str) -> NodeOp {
    NodeOp {
        op: o.into(),
        ..Default::default()
    }
}

/// The oversight floor (EU AI Act Art. 14): with person_reviews_model_output, a model accepts nothing and an automated
/// action accepts no impact a model produced; a person's verdict and an automated one on what no model produced stand.
#[tokio::test]
async fn person_reviews_model_output() {
    let ctx = ctx();
    let mut s = setup().await;
    s.e.person_reviews_model_output = true;
    let (e, g, base, _errs) = s.share();
    let x = Engine::internals_of(&e);
    let c = root_change(&g, "alm", "PSP v2", &base).await;
    let p = on_change(&c.id);
    let llm = Internals::with_automated(&ctx, &action("draft", methodology::KIND_LLM));
    let script = Internals::with_automated(&ctx, &action("check", methodology::KIND_SCRIPT));

    // a model writes REQ-1 (its run called a model) and may not accept it
    g.append_log(
        &ctx,
        vec![LogEntry {
            change: c.id.clone(),
            r#type: "model.call".into(),
            execution: "exec-llm".into(),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    let write = vec![
        NodeOp {
            r#ref: "#n1".into(),
            intent: domain::INTENT_MODIFIED.to_string(),
            key: "REQ-1".into(),
            rationale: "PSP v2 changes the payment API".into(),
            ..op("declare")
        },
        NodeOp {
            node: "#n1".into(),
            props: obj(json!({"title": "Use PSP v2"})),
            ..op("write")
        },
    ];
    x.apply_node_ops(&llm, &p, write, "draft", "exec-llm")
        .await
        .unwrap();
    let accept = || {
        vec![NodeOp {
            node: "REQ-1".into(),
            accept: true,
            comment: "fine".into(),
            ..op("review")
        }]
    };
    let err = x
        .apply_node_ops(&llm, &p, accept(), "draft", "exec-llm")
        .await
        .expect_err("a model accepting");
    assert!(err.is_forbidden(), "a model accepting: {err}");
    // nor may a script accept what the model produced
    let err = x
        .apply_node_ops(&script, &p, accept(), "check", "exec-script")
        .await
        .expect_err("a script accepting a model's output");
    assert!(
        err.is_forbidden(),
        "a script accepting a model's output: {err}"
    );
    // a script accepts what it produced itself
    let own = vec![
        NodeOp {
            r#ref: "#n1".into(),
            intent: domain::INTENT_CREATED.to_string(),
            key: "TST-2".into(),
            r#type: "TestCase".into(),
            rationale: "cover the new API".into(),
            ..op("declare")
        },
        NodeOp {
            node: "#n1".into(),
            props: obj(json!({"title": "PSP v2 test"})),
            ..op("write")
        },
        NodeOp {
            node: "#n1".into(),
            accept: true,
            comment: "recorded".into(),
            ..op("review")
        },
    ];
    x.apply_node_ops(&script, &p, own, "check", "exec-script")
        .await
        .expect("a script accepting its own output");
    // a person accepts the model's output
    x.apply_node_ops(&ctx, &p, accept(), "review_by_hand", "exec-human")
        .await
        .expect("a person accepting");
    let ch = g.change(&ctx, &c.id).await.unwrap();
    for n in &ch.nodes {
        assert_eq!(
            n.review,
            domain::REVIEW_ACCEPTED,
            "{} is {}",
            n.key,
            n.review
        );
    }
}
