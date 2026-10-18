//! Go: form_test.go.

use std::sync::Arc;

use goap_domain::{self as domain, ChangeImpact, ChangeItem};
use goap_engine::{
    Engine, InprocSandboxes, Process, STATUS_WAITING, ScriptExecutor, StartRequest,
    StaticMethodologies,
};
use goap_graph::{Graph, OpenDecisionRequest};
use goap_methodology as methodology;
use serde_json::{Value, json};

use crate::common::*;

const FORM_METHODOLOGY: &str = r#"
name: linking
version: 1.0.0
namespace: alm
conditions:
  - name: linked
    expr: artifacts.exists(a, a.target == "link_request") || changeImpacts.exists(n, n.key == "REQ-1")
goals:
  - name: link
    examples: [link the requirement]
    pre: {linked: true}
actions:
  - name: link_requirement
    kind: human
    effects: {linked: true}
    form:
      title: Link the requirement
      artifact: link_request
      fields:
        - {name: requirement, widget: node, required: true, constraints: {types: [alm@Requirement]}}
        - {name: tests, widget: link, constraints: {from: requirement, linkTypes: [alm@verifies], toTypes: [alm@TestCase], max: 3}}
        - {name: why, widget: textarea, required: true}
"#;

/// start_form_src starts a linking methodology on the graph of setup.
async fn start_form_src(src: String) -> (Arc<Engine>, Arc<Graph>, Process, Errs) {
    let ctx = ctx();
    let mut s = setup().await;
    let cm = compile_yaml(&src);
    s.e.methodologies = Arc::new(StaticMethodologies::new().with(cm));
    s.e.executors.insert(
        methodology::KIND_SCRIPT.into(),
        Arc::new(ScriptExecutor {
            sandboxes: Some(Arc::new(InprocSandboxes)),
        }),
    );
    let (e, g, base, errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "linking".into(),
                goal: "link".into(),
                baseline_id: base,
                project_id: TEST_PROJECT.into(),
                intent: "link the requirement".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .and_then(|t| t.form.as_ref())
                .is_some_and(|f| f.title == "Link the requirement"),
        "the task carries its form: {} {:?}",
        p.status,
        p.pending
    );
    (e, g, p, errs)
}

/// start_form starts the linking methodology on the graph of setup, with the code given to its human action.
async fn start_form(code: &str) -> (Arc<Engine>, Arc<Graph>, Process, Errs) {
    let mut src = FORM_METHODOLOGY.to_string();
    if !code.is_empty() {
        src += &format!(
            "    language: javascript\n    code: |\n      {}\n",
            code.replace('\n', "\n      ")
        );
    }
    start_form_src(src).await
}

fn impact<'a>(c: &'a domain::Change, key: &str) -> Option<&'a ChangeImpact> {
    c.nodes.iter().find(|n| n.key == key)
}

#[tokio::test]
async fn submit_form_without_code() {
    let ctx = ctx();
    let (e, _g, p, _errs) = start_form("").await;
    // the constraints are checked by the engine
    let err = e
        .submit_form(
            &ctx,
            &p.id,
            obj(json!({"requirement": "CMP-1", "tests": [{"to": "REQ-1"}]})),
        )
        .await
        .expect_err("refused");
    assert!(err.is_invalid(), "refused: {err}");
    for want in [
        "requirement: CMP-1 is a alm@Component",
        "tests: link 1: REQ-1 is a alm@Requirement",
        "why: required",
    ] {
        assert!(err.to_string().contains(want), "missing {want:?} in {err}");
    }
    e.submit(&ctx, &p.id, vec![])
        .await
        .expect("items still accepted");
    let (e, g, p, _errs) = start_form("").await;
    let p = e
        .submit_form(
            &ctx,
            &p.id,
            obj(json!({"requirement": "REQ-1", "tests": [{"to": "TST-1"}], "why": "covered"})),
        )
        .await
        .unwrap();
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    let art: Option<&ChangeItem> = c.items.iter().rev().find(|it| it.target == "link_request");
    assert!(
        art.is_some_and(|a| a.data.get("why") == Some(&Value::from("covered"))),
        "the values are an artifact: {:?}",
        c.items
    );
    // the link field wrote the link on the draft of its source
    let req = impact(&c, "REQ-1");
    let post = req
        .and_then(|r| r.post.clone())
        .unwrap_or_else(|| panic!("REQ-1 is written by the change: {:?}", c.nodes));
    let v = g
        .change_node_view(&ctx, &p.change_id, "", &post)
        .await
        .unwrap();
    let (nodes, _) = g.baseline_graph(&ctx, &c.baseline_id).await.unwrap();
    let tst = nodes
        .iter()
        .find(|n| n.key == "TST-1")
        .map(|n| n.id.clone())
        .unwrap_or_default();
    assert!(
        v.out
            .iter()
            .any(|l| l.r#type == "alm@verifies" && l.to.id == tst),
        "link written: {:?}",
        v.out
    );
}

#[tokio::test]
async fn submit_form_runs_its_code() {
    let ctx = ctx();
    let (e, g, p, _errs) = start_form(
        r#"const f = ctx.var("form");
ctx.addArtifact("link_request", {about: f.requirement, reason: f.why.toUpperCase()});"#,
    )
    .await;
    let p = e
        .submit_form(
            &ctx,
            &p.id,
            obj(json!({"requirement": "REQ-1", "why": "covered"})),
        )
        .await
        .unwrap();
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert!(
        c.items.iter().any(|it| it.target == "link_request"
            && it.data.get("reason") == Some(&Value::from("COVERED"))
            && it.data.get("about") == Some(&Value::from("REQ-1"))),
        "the code made the items: {:?}",
        c.items
    );
}

#[test]
fn form_compile_issues() {
    let src = FORM_METHODOLOGY.replacen("widget: textarea", "widget: slider", 1)
        + "    language: javascript\n    code: x\n  - name: other\n    kind: llm\n    prompt: p\n    effects: {linked: true}\n    form: {fields: [{name: a, widget: text}]}\n";
    let m = methodology::parse(src.as_bytes()).unwrap();
    let err = m.compile().expect_err("compiled").to_string();
    for want in [
        r#"actions[0].form.fields[2].widget: unknown widget "slider""#,
        "actions[1].form: a form belongs to a human action",
    ] {
        assert!(err.contains(want), "missing {want:?} in {err}");
    }
}

/// Go changed the fields of the compiled action after the start; the Rust compiled form is immutable, so the
/// methodology declares these fields from the start.
#[tokio::test]
async fn submit_form_creates_and_rules() {
    let ctx = ctx();
    let fields = r#"      fields:
        - {name: req, widget: newNode, required: true, constraints: {types: [alm@Requirement]}}
        - {name: tests, widget: link, constraints: {from: req, linkTypes: [alm@verifies]}}
        - {name: choice, widget: decision}
"#;
    let start = FORM_METHODOLOGY.find("      fields:").unwrap();
    let src = FORM_METHODOLOGY[..start].to_string() + fields;
    let (e, g, p, _errs) = start_form_src(src).await;
    let d = g
        .open_decision(
            &ctx,
            &p.change_id,
            OpenDecisionRequest {
                question: "ship?".into(),
                options: Some(vec![]),
                by: "t".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e
        .submit_form(
            &ctx,
            &p.id,
            obj(json!({
                "req": {"key": "REQ-NEW", "type": "alm@Requirement", "props": {"title": "A new need"}},
                "tests": [{"to": "TST-1"}],
                "choice": {"point": d.id, "option": "go", "justification": "ready"},
            })),
        )
        .await
        .unwrap();
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    let created = impact(&c, "REQ-NEW")
        .filter(|n| n.intent == domain::INTENT_CREATED && n.post.is_some())
        .unwrap_or_else(|| panic!("REQ-NEW is created by the change: {:?}", c.nodes));
    let v = g
        .change_node_view(&ctx, &p.change_id, "", created.post.as_ref().unwrap())
        .await
        .unwrap();
    assert!(
        v.node.properties.get("title") == Some(&Value::from("A new need"))
            && v.out.len() == 1
            && v.out[0].r#type == "alm@verifies",
        "created with its properties and link: {:?} {:?}",
        v.node.properties,
        v.out
    );
    let bb = g.blackboard_in(&ctx, &p.change_id, "").await.unwrap();
    let pts = domain::decision_points_of(&bb);
    assert!(
        pts.iter()
            .any(|pt| pt.id == d.id && pt.ruling.as_ref().is_some_and(|r| r.option == "go")),
        "the point is ruled: {pts:?}"
    );
}
