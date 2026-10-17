//! Port of `algorithms_test.go`, on the algorithm runners of `algorithms.rs` (the paths that call them, accepting a
//! draft and taking a transition, are later slices'). The stamp action, written in Go there, is JavaScript here.

use std::sync::Arc;

use goap_algo::{Bound, USAGE_PROPERTY_VALIDATOR, USAGE_TRANSITION_ACTION, USAGE_TRANSITION_GUARD};
use goap_domain::{Change, Lifecycle, LifecycleState, Node, Transition};
use serde_json::{Value, json};

use crate::algorithms::{TransitionEnv, run_actions, run_guards};
use crate::testsupport::*;
use crate::{ErrorKind, Graph};

const REGEX_VALIDATOR_JS: &str = r#"
var v = ctx.value();
if (v !== undefined && v !== null && !new RegExp(ctx.param("pattern")).test(String(v)))
  ctx.fail("must match " + ctx.param("pattern"));"#;

const NO_EMPTY_CHILD_GUARD_JS: &str = r#"
for (const c of ctx.children()) if (c.state !== ctx.param("state")) ctx.fail(c.key + " is " + c.state);"#;

const STAMP_ACTION_JS: &str = r#"
ctx.setProp("approvedAt", ctx.change().title);
ctx.setProp("state_after", ctx.transition().to);"#;

fn params(v: Value) -> serde_json::Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

/// Req: property "code" must match ^REQ-[0-9]+$ (inherited by Sub); approve stamps the node. Doc: release needs its
/// requirements approved (guard).
fn algo_graph() -> (Graph, Transition, Transition) {
    let regex = Bound {
        instance: "req-code".into(),
        algorithm: "regex".into(),
        r#type: USAGE_PROPERTY_VALIDATOR,
        language: "javascript".into(),
        code: REGEX_VALIDATOR_JS.into(),
        params: params(json!({"pattern": "^REQ-[0-9]+$"})),
        property: "code".into(),
    };
    let stamp = Bound {
        instance: "stamp".into(),
        algorithm: "stamp".into(),
        r#type: USAGE_TRANSITION_ACTION,
        language: "javascript".into(),
        code: STAMP_ACTION_JS.into(),
        ..Default::default()
    };
    let guard = Bound {
        instance: "children-approved".into(),
        algorithm: "children-in-state".into(),
        r#type: USAGE_TRANSITION_GUARD,
        language: "javascript".into(),
        code: NO_EMPTY_CHILD_GUARD_JS.into(),
        params: params(json!({"state": "approved"})),
        ..Default::default()
    };
    let state = |n: &str, not_landable: bool| LifecycleState {
        name: n.into(),
        not_landable,
        ..Default::default()
    };
    let approve = Transition {
        name: "approve".into(),
        from: "draft".into(),
        to: "approved".into(),
        action_algos: vec![stamp],
        ..Default::default()
    };
    let release = Transition {
        name: "release".into(),
        from: "draft".into(),
        to: "released".into(),
        guard_algos: vec![guard],
        ..Default::default()
    };
    let req_lc = Lifecycle {
        initial: "draft".into(),
        states: vec![state("draft", true), state("approved", false)],
        transitions: vec![approve.clone()],
        ..Default::default()
    };
    let doc_lc = Lifecycle {
        initial: "draft".into(),
        states: vec![state("draft", true), state("released", false)],
        transitions: vec![release.clone()],
        ..Default::default()
    };
    let mut g = memory_repo().graph();
    use_types(
        &mut g,
        Arc::new(TestTypes::new(&[
            (
                "Req",
                TestType {
                    lifecycle: Some(req_lc),
                    validators: vec![regex],
                    ..Default::default()
                },
            ),
            (
                "Sub",
                TestType {
                    extends: "Req".into(),
                    ..Default::default()
                },
            ),
            (
                "Doc",
                TestType {
                    lifecycle: Some(doc_lc),
                    ..Default::default()
                },
            ),
        ])),
    );
    (g, approve, release)
}

fn node(key: &str, typ: &str, state: &str, code: Option<&str>) -> Node {
    Node {
        key: key.into(),
        r#type: typ.into(),
        state: state.into(),
        properties: code
            .map(|c| props(&[("code", c.into())]))
            .unwrap_or_default(),
        ..Default::default()
    }
}

// The validators judge the properties (also through the inherited validator of a subtype).
#[tokio::test]
async fn property_validators() {
    let c = ctx();
    let (g, _, _) = algo_graph();
    let ix = g.types_at();
    for typ in ["Req", "Sub"] {
        let n = node("R4", typ, "draft", Some("nope"));
        let e = g
            .validate_props(&c, &ix, &n, &n.properties)
            .await
            .expect_err("an invalid property");
        assert!(
            e.kind() == ErrorKind::Invalid && e.to_string().contains("must match"),
            "{typ}: {e}"
        );
    }
    let n = node("R2", "Sub", "draft", Some("REQ-2"));
    must(g.validate_props(&c, &ix, &n, &n.properties).await);
    // no validator, no check
    let d = node("D1", "Doc", "draft", Some("anything"));
    must(g.validate_props(&c, &ix, &d, &d.properties).await);
    assert!(g.lifecycle_controlled("Sub"));
    assert!(!g.lifecycle_controlled("Other"));
}

// The guard refuses releasing a document whose requirement is a draft; the action stamps the requirement it
// approves, and what it sets is validated again.
#[tokio::test]
async fn transition_guard_and_action() {
    let c = ctx();
    let (g, approve, release) = algo_graph();
    let ix = g.types_at();
    let change = Change {
        title: "chg".into(),
        ..Default::default()
    };
    let env = TransitionEnv {
        change: &change,
        impact: None,
        ix: &ix,
    };
    let doc = node("D1", "Doc", "draft", None);
    let draft = node("R1", "Req", "draft", Some("REQ-1"));
    let e = run_guards(&c, &env, &doc, &release, std::slice::from_ref(&draft))
        .await
        .expect_err("the guard refuses");
    assert!(
        e.kind() == ErrorKind::Invalid && e.to_string().contains("R1 is draft"),
        "{e}"
    );
    let approved = node("R1", "Req", "approved", Some("REQ-1"));
    must(run_guards(&c, &env, &doc, &release, &[approved]).await);
    let (set, unset) = must(run_actions(&c, &g, &env, &draft, &approve, &[]).await);
    assert!(unset.is_empty());
    assert_eq!(set["approvedAt"], "chg");
    assert_eq!(set["state_after"], "approved");
    assert!(!set.contains_key("code"));
    // what the actions set is validated again
    let bad = node("R3", "Req", "draft", Some("nope"));
    let e = run_actions(&c, &g, &env, &bad, &approve, &[])
        .await
        .expect_err("validated again");
    assert!(
        e.to_string().contains("after the actions of approve"),
        "{e}"
    );
}
