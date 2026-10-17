//! refs_test.go, over the typed graph in memory of the tests and over the real graph typed the same way (strict attributes and link types of the built-in
//! meta-domain).

use std::collections::HashMap;
use std::sync::Arc;

use goap_methodology::Methodology;
use serde_json::Value;

use super::realgraph::{Probe, real_typed};
use super::{FakeGraph, at, bg, canon};
use crate::graphstore::{GraphStore, LINK_DEFINES, NAMESPACE_METHODOLOGY, methodology_version_key};
use crate::store::{Record, STATUS_DRAFT, Store};

const REFS_YAML: &str = r#"
name: refs
version: "1"
namespace: alm
goal: done
roles: [{name: analyst}]
conditions:
  - {name: framed, expr: "true"}
  - {name: analysed, expr: "true"}
  - {name: impact_ok, scope: impact, expr: "true"}
  - {name: all_ok, all: impact_ok}
actions:
  - {name: frame, kind: human, effects: {framed: true}, roles: [analyst]}
  - {name: analyse, kind: human, pre: {framed: true, "state:open": true}, effects: {analysed: true, all_ok: true}}
goals: [{name: done, pre: {analysed: true}}]
agents:
  - {name: a, planner: goap, actions: [analyse, other/x, frame], goals: [done]}
processes:
  - name: deliver
    steps:
      - {name: framing, action: frame, roles: {responsible: analyst}}
      - {name: analysis, pre: {"step:deliver/framing": true}, done: {analysed: true}, actions: [analyse]}
"#;

/// linksOf lists the reference links leaving a node of the head of the methodology namespace, suspect ones
/// included, as "<type> <target key relative to the version> <attributes>".
async fn links_of<G: Probe>(g: &G, hk: &str, from: &str) -> Vec<String> {
    let (nodes, links) = g.head_graph(NAMESPACE_METHODOLOGY).await;
    let mut keys = HashMap::new();
    let mut src = None;
    for n in &nodes {
        keys.insert(n.id.clone(), n.key.clone());
        if n.key == format!("{hk}/{from}") || (from.is_empty() && n.key == hk) {
            src = Some(n.id.clone());
        }
    }
    let prefix = format!("{hk}/");
    let mut out: Vec<String> = links
        .iter()
        .filter(|l| Some(&l.from.id) == src.as_ref() && l.r#type != LINK_DEFINES)
        .map(|l| {
            let to = keys.get(&l.to.id).cloned().unwrap_or_default();
            let props = if l.properties.is_empty() {
                "null".to_string()
            } else {
                canon(&l.properties)
            };
            format!(
                "{} {} {props}",
                l.r#type.strip_prefix("methodology@").unwrap_or(&l.r#type),
                to.strip_prefix(&prefix).unwrap_or(&to)
            )
        })
        .collect();
    out.sort();
    out
}

async fn props<G: Probe>(g: &G, key: &str) -> serde_json::Map<String, Value> {
    g.find(NAMESPACE_METHODOLOGY, key)
        .await
        .unwrap_or_else(|| panic!("{key}"))
        .properties
}

// The references between the elements of a version are links (ADR 0120): a condition is the output of an action
// (produces) and the input of another (requires), the same node; the names that resolve to no element stay in the
// properties; the definition reads back as it was saved.
#[tokio::test]
async fn references_are_links() {
    references_are_links_over(FakeGraph::typed()).await;
    references_are_links_over(real_typed()).await;
}

async fn references_are_links_over<G: Probe + 'static>(g: Arc<G>) {
    let ctx = bg();
    let s = GraphStore::new(g.clone());
    let mut m = Methodology::parse(REFS_YAML.as_bytes()).expect("parse");
    let now = at(2026, 10, 15, 8, 0);
    let save = |m: Methodology| {
        let s = &s;
        let ctx = ctx.clone();
        async move {
            s.save(
                &ctx,
                Record {
                    methodology: m.clone(),
                    status: STATUS_DRAFT,
                    created_at: Some(now),
                    updated_at: Some(now),
                    ..Record::default()
                },
            )
            .await
            .expect("save");
            let got = s.get(&ctx, &m.name, &m.version).await.expect("get");
            assert_eq!(
                canon(&got.methodology),
                canon(&m),
                "the definition does not read back"
            );
        }
    };
    save(m.clone()).await;
    let hk = methodology_version_key(&m.name, &m.version);
    let expect = async |from: &str, want: &[&str]| {
        assert_eq!(links_of(&*g, &hk, from).await, want, "links of {from}");
    };
    expect(
        "action/frame",
        &[
            r#"assigns role/analyst {"as":"run","position":0}"#,
            r#"produces condition/framed {"value":true}"#,
        ],
    )
    .await;
    expect(
        "action/analyse",
        &[
            r#"produces condition/all_ok {"value":true}"#,
            r#"produces condition/analysed {"value":true}"#,
            r#"requires condition/framed {"value":true}"#,
        ],
    )
    .await;
    expect(
        "goal/done",
        &[r#"requires condition/analysed {"value":true}"#],
    )
    .await;
    expect(
        "agent/a",
        &[
            r#"pursues goal/done {"position":0}"#,
            r#"uses action/analyse {"position":0}"#,
            r#"uses action/frame {"position":2}"#,
        ],
    )
    .await;
    expect(
        "condition/all_ok",
        &[r#"aggregates condition/impact_ok {"as":"all"}"#],
    )
    .await;
    expect(
        "process/deliver",
        &[
            "sub_activity step/deliver/analysis null",
            "sub_activity step/deliver/framing null",
        ],
    )
    .await;
    expect(
        "step/deliver/framing",
        &[
            r#"assigns role/analyst {"as":"responsible"}"#,
            r#"performed_by action/frame {"as":"action"}"#,
        ],
    )
    .await;
    expect(
        "step/deliver/analysis",
        &[
            r#"performed_by action/analyse {"as":"alternative","position":0}"#,
            r#"produces condition/analysed {"value":true}"#,
        ],
    )
    .await;
    expect("", &["main_goal goal/done null"]).await;

    // what names nothing of the version stays a property
    let p = props(&*g, &format!("{hk}/action/analyse")).await;
    assert!(
        canon(&p.get("pre")) == r#"{"state:open":true}"# && p.get("effects").is_none(),
        "the action keeps only the names it cannot link: {p:?}"
    );
    let p = props(&*g, &format!("{hk}/agent/a")).await;
    assert!(
        canon(&p.get("actions")) == r#"["other/x"]"# && p.get("goals").is_none(),
        "the agent keeps only the names it cannot link: {p:?}"
    );
    let p = props(&*g, &format!("{hk}/process/deliver")).await;
    assert!(
        p.get("steps").is_none(),
        "the steps of a process are nodes: {p:?}"
    );

    // a value that changes replaces its link; an edited condition keeps the references to it (suspect)
    m.actions[1].pre = [
        ("framed".to_string(), false),
        ("state:open".to_string(), true),
    ]
    .into_iter()
    .collect();
    m.conditions[0].description = "The change is framed".into();
    save(m.clone()).await;
    expect(
        "action/analyse",
        &[
            r#"produces condition/all_ok {"value":true}"#,
            r#"produces condition/analysed {"value":true}"#,
            r#"requires condition/framed {"value":false}"#,
        ],
    )
    .await;
    expect(
        "action/frame",
        &[
            r#"assigns role/analyst {"as":"run","position":0}"#,
            r#"produces condition/framed {"value":true}"#,
        ],
    )
    .await;

    // an element that leaves the definition drops its links, and the names of it become properties again
    m.conditions.remove(0);
    save(m.clone()).await;
    expect("condition/framed", &[]).await;
    let p = props(&*g, &format!("{hk}/action/frame")).await;
    assert_eq!(
        canon(&p.get("effects")),
        r#"{"framed":true}"#,
        "a name the version no longer declares is a property again: {p:?}"
    );
}
