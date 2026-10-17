//! Go: foreach_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_engine::{
    Engine, FOREACH_SUFFIX, ItemInput, Lane, Process, STATUS_COMPLETED, STATUS_WAITING,
    StartRequest, TASK_AGENT,
};
use goap_journal as journal;
use goap_methodology as methodology;
use serde_json::{Value, json};

use crate::common::*;
use crate::steps::{history_of, must_progress};

/// built is true per element: an artifact "build" naming the component, read through vars.item.key.
const FOREACH_YAML: &str = r#"
name: forked
version: 1.0.0
namespace: alm
conditions:
  - {name: built, expr: 'artifacts.exists(a, a.target == "build" && a.data.component == vars.item.key)'}
actions:
  - {name: build_java, kind: human, description: Build with maven, effects: {built: true}}
  - {name: build_go, kind: human, description: Build with go, effects: {built: true}}
  - {name: build_any, kind: human, description: Build by hand, effects: {built: true}}
methods:
  - name: java
    for: building
    when: 'vars.item.key == "java" || vars.item.lang == "java"'
    actions: [build_java]
    done: {built: true}
  - name: golang
    for: building
    when: 'vars.item.key == "go" || vars.item.lang == "go"'
    actions: [build_go]
    done: {built: true}
  - name: generic
    for: building
    actions: [build_any]
    done: {built: true}
processes:
  - name: delivery
    steps:
      - {name: build, method: building, %s}
"#;

async fn forked_engine(step: &str) -> (Arc<Engine>, Errs) {
    let mut s = setup().await;
    add_yaml(&mut s, &FOREACH_YAML.replacen("%s", step, 1));
    let (e, _, _, errs) = s.share();
    (e, errs)
}

/// streams returns the children of the step, by element.
async fn streams(e: &Engine, p: &Process) -> BTreeMap<String, Process> {
    let mut out = BTreeMap::new();
    for (key, id) in &p.children {
        if let Some((_, el)) = key.split_once(FOREACH_SUFFIX) {
            out.insert(el.to_string(), get(e, id).await);
        }
    }
    out
}

async fn finish_streams(e: &Arc<Engine>, p: &Process, components: &[&str]) {
    let ctx = ctx();
    for el in components {
        let all = streams(e, p).await;
        let c = all.get(*el);
        let c = c
            .filter(|c| c.pending.is_some())
            .unwrap_or_else(|| panic!("no stream waiting for {el}: {c:?}"));
        e.submit(
            &ctx,
            &c.id,
            vec![ItemInput {
                r#type: "artifact".into(),
                target: "build".into(),
                data: obj(json!({"component": el})),
                ..Default::default()
            }],
        )
        .await
        .unwrap();
        schedule(e, &c.id).await;
    }
}

fn start(components: Value) -> StartRequest {
    StartRequest {
        methodology: "forked".into(),
        goal: "delivery".into(),
        intent: "build".into(),
        project_id: TEST_PROJECT.into(),
        vars: obj(json!({ "components": components })),
        ..Default::default()
    }
}

/// One stream per element, each with the most specific method for it, all in flight at once; the step is done when
/// every stream is.
#[tokio::test]
async fn foreach_runs_one_stream_per_element() {
    let ctx = ctx();
    let (e, _errs) = forked_engine("foreach: 'vars.components'").await;
    let p = e
        .start(
            &ctx,
            start(json!([
                {"key": "api", "lang": "go"},
                {"key": "web", "lang": "java"},
                {"key": "docs", "lang": "md"},
            ])),
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING && p.pending.as_ref().is_some_and(|t| t.kind == TASK_AGENT),
        "expected the step waiting for its streams: {} {:?} {}",
        p.status,
        p.pending,
        p.error
    );
    let got = streams(&e, &p).await;
    assert_eq!(got.len(), 3, "streams {got:?}");
    for (el, want) in [("api", "golang"), ("web", "java"), ("docs", "generic")] {
        let c = &got[el];
        assert!(
            c.agent == want
                && c.step
                    .as_ref()
                    .is_some_and(|s| s.method == want && s.item_key == el)
                && c.vars.get("item").is_some_and(|v| !v.is_null()),
            "stream {el}: agent {} step {:?} vars {:?}, want method {want}",
            c.agent,
            c.step,
            c.vars
        );
        assert!(
            c.pending.is_some(),
            "stream {el} is not at work: {}",
            c.status
        );
    }
    let lanes: Vec<Lane> = must_progress(&e, &p.id)
        .await
        .steps
        .into_iter()
        .flat_map(|st| st.lanes)
        .collect();
    assert!(
        lanes.len() == 3 && lanes[0].item == "api" && lanes[0].method == "golang",
        "progress shows one lane per stream: {lanes:?}"
    );
    // the step is not done until every stream is
    finish_streams(&e, &p, &["api", "web"]).await;
    assert_ne!(
        get(&e, &p.id).await.status,
        STATUS_COMPLETED,
        "done with a stream still at work"
    );
    finish_streams(&e, &p, &["docs"]).await;
    let p = get(&e, &p.id).await;
    assert_eq!(
        p.status, STATUS_COMPLETED,
        "not completed: {:?} {}",
        p.pending, p.error
    );
    // the step has run, and its run stands (ADR 0109)
    let last = history_of(&e, &p.change_id).await.last();
    let s = last.get("delivery/build");
    assert!(
        s.is_some_and(|s| s.outcome == journal::OUTCOME_SUCCEEDED && s.current),
        "the history of the step: {s:?}"
    );
}

/// groupBy: one stream per group, whose item is {key, items}.
#[tokio::test]
async fn foreach_groups_the_elements() {
    let ctx = ctx();
    let (e, _errs) = forked_engine("foreach: 'vars.components', groupBy: 'vars.item.lang'").await;
    let p = e
        .start(
            &ctx,
            start(json!([
                {"key": "a", "lang": "java"},
                {"key": "b", "lang": "go"},
                {"key": "c", "lang": "java"},
            ])),
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    let got = streams(&e, &p).await;
    assert!(
        got.len() == 2 && got.contains_key("java") && got.contains_key("go"),
        "one stream per language: {got:?}"
    );
    let item = got["java"].vars.get("item").cloned().unwrap_or_default();
    let members = item
        .get("items")
        .and_then(Value::as_array)
        .map_or(0, Vec::len);
    assert!(
        item.get("key") == Some(&Value::from("java"))
            && members == 2
            && got["java"].agent == "java"
            && got["go"].agent == "golang",
        "group {item} agents {}/{}",
        got["java"].agent,
        got["go"].agent
    );
    finish_streams(&e, &p, &["java", "go"]).await;
    let p = get(&e, &p.id).await;
    assert_eq!(
        p.status, STATUS_COMPLETED,
        "not completed: {:?} {}",
        p.pending, p.error
    );
}

#[test]
fn foreach_validation() {
    for (name, step, want) in [
        (
            "not a list",
            "{name: s, method: building, foreach: '1 + 1'}",
            "must return a list",
        ),
        (
            "bad expression",
            "{name: s, method: building, foreach: 'nope('}",
            "foreach",
        ),
        (
            "groupBy alone",
            "{name: s, method: building, groupBy: 'vars.item.k'}",
            "declare foreach",
        ),
        (
            "not a capability",
            "{name: s, action: build_any, foreach: 'vars.components'}",
            "naming a capability",
        ),
    ] {
        let src = FOREACH_YAML.replacen("{name: build, method: building, %s}", step, 1);
        let m = methodology::parse(src.as_bytes()).unwrap();
        match m.compile() {
            Ok(_) => panic!("{name}: want {want:?}, compiled"),
            Err(err) => assert!(
                err.to_string().contains(want),
                "{name}: want {want:?}, got {err}"
            ),
        }
    }
}
