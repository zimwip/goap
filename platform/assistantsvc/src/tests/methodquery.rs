//! methodquery_test.go

use std::collections::HashMap;
use std::sync::atomic::Ordering;

use goap_methodology::{Action, Methodology, Role};
use serde_json::{Map, Value};

use super::*;
use crate::agents::{TOOL_LIST_AGENTS, TOOL_START_AGENT};
use crate::methodquery::{
    DEFAULT_QUERY_LIMIT, DETAIL_FULL, DETAIL_NAMES, MAX_FULL_ITEMS, MAX_QUERY_BYTES,
    MAX_QUERY_LIMIT, Query, QueryResult, SUMMARY_TEXT, run_query,
};
use crate::methodtool::TOOL_METHODOLOGY_QUERY;
use crate::prompt::system_rules;
use crate::service::MAX_TOOL_CALLS;
use crate::turn::{
    TOOL_CREATE_CHANGE, TOOL_LIST_METHODOLOGIES, TOOL_OPEN_CHANGE, TOOL_SELECT_PROJECT, tools,
};

const QUERY_YAML: &str = r#"
name: shop
version: 1.0.0
namespace: alm
description: |
  Sell things online.
  Second line that a summary must drop.
imports: [decisions]
roles:
  - {name: developer, description: Writes code}
  - {name: release_manager}
conditions:
  - {name: built, description: The build exists, expr: 'artifacts.exists(a, a.target == "build")'}
  - {name: released, expr: 'artifacts.exists(a, a.target == "release")'}
actions:
  - {name: build, description: Compile the shop, kind: builtin, builtin: test.emit, effects: {built: true}, params: {type: artifact, target: build}, roles: [developer]}
  - {name: release, description: Publish the shop, kind: builtin, builtin: test.emit, pre: {built: true}, effects: {released: true}, params: {type: artifact, target: release}}
goals:
  - {name: built, description: The build exists, examples: ["compile the shop"], pre: {built: true}}
  - {name: ship, description: Released, pre: {released: true}}
agents:
  - name: builder
    description: Builds the software
    examples: ["build it"]
    roles: [developer]
    actions: [build]
    goals: [built]
    triggers:
      - {name: nightly, type: schedule, schedule: "0 2 * * *", goal: built, enabled: true}
  - {name: shipper, description: Ships the release, roles: [release_manager], actions: [build, release], goals: [built, ship]}
processes:
  - name: delivery
    description: From code to release
    steps:
      - name: compile
        description: Build it
        action: build
      - name: publish
        description: Release it
        steps:
          - {name: tag, action: release}
          - {name: announce, description: Tell people}
"#;

fn shop() -> Methodology {
    compiled(QUERY_YAML).definition().clone()
}

fn query(q: Query) -> QueryResult {
    let m = shop();
    run_query(q, &[&m]).expect("query")
}

fn names(r: &QueryResult) -> Vec<String> {
    r.items
        .iter()
        .map(|it| it["name"].as_str().expect("a name").to_string())
        .collect()
}

fn qk(kind: &str) -> Query {
    Query {
        kind: kind.into(),
        ..Default::default()
    }
}

fn strs(v: &Value) -> Vec<String> {
    v.as_array()
        .map(|a| {
            a.iter()
                .filter_map(|x| x.as_str().map(String::from))
                .collect()
        })
        .unwrap_or_default()
}

#[test]
fn query_overview_counts_and_names() {
    let r = query(Query::default());
    let want = [
        ("agent", 2),
        ("action", 2),
        ("goal", 2),
        ("process", 1),
        ("step", 4),
        ("role", 2),
        ("trigger", 1),
        ("condition", 2),
        ("library", 1),
    ];
    for (k, v) in want {
        assert_eq!(
            r.counts.get(k).copied().unwrap_or(0),
            v,
            "count {k} ({:?})",
            r.counts
        );
    }
    assert!(
        r.methodology == "shop"
            && r.total == 17
            && r.returned == 17
            && !r.truncated
            && r.next.is_none(),
        "{r:?}"
    );
    // the overview gives names only
    let it = &r.items[0];
    assert!(
        it.len() == 2 && it["kind"] == "agent" && it["name"] == "builder",
        "{it:?}"
    );
}

#[test]
fn query_each_kind() {
    let want: HashMap<&str, Vec<&str>> = [
        ("agent", vec!["builder", "shipper"]),
        ("action", vec!["build", "release"]),
        ("goal", vec!["built", "ship"]),
        ("process", vec!["delivery"]),
        (
            "step",
            vec![
                "delivery/compile",
                "delivery/publish",
                "delivery/publish/tag",
                "delivery/publish/announce",
            ],
        ),
        ("role", vec!["developer", "release_manager"]),
        ("trigger", vec!["nightly"]),
        ("condition", vec!["built", "released"]),
        ("library", vec!["decisions"]),
    ]
    .into_iter()
    .collect();
    for (kind, w) in want {
        let r = query(qk(kind));
        assert_eq!(names(&r), w, "{kind}");
        assert!(
            r.counts.is_empty() && r.kind == kind,
            "{kind}: counts only for the overview: {r:?}"
        );
    }
    let r = query(qk("method"));
    assert!(r.total == 0 && r.items.is_empty(), "{r:?}");
    let err = run_query(qk("tool"), &[]).unwrap_err();
    assert!(err.contains("agent, action"), "{err}");
}

#[test]
fn query_name_glob_and_text() {
    let n = |q: Query| names(&query(q));
    assert_eq!(
        n(Query {
            name: "RELEASE".into(),
            ..Default::default()
        }),
        ["release"],
        "exact is case-insensitive"
    );
    assert_eq!(
        n(Query {
            kind: "action".into(),
            name: "re*".into(),
            ..Default::default()
        }),
        ["release"]
    );
    assert_eq!(
        n(Query {
            kind: "role".into(),
            name: "*_manage?".into(),
            ..Default::default()
        }),
        ["release_manager"]
    );
    // a step matches its path or its own name
    assert_eq!(
        n(Query {
            kind: "step".into(),
            name: "tag".into(),
            ..Default::default()
        }),
        ["delivery/publish/tag"]
    );
    assert_eq!(
        n(Query {
            kind: "step".into(),
            name: "delivery/*/t?g".into(),
            ..Default::default()
        }),
        ["delivery/publish/tag"]
    );
    // q: name, description and examples
    assert_eq!(
        n(Query {
            q: "compile THE shop".into(),
            ..Default::default()
        }),
        ["build", "built"],
        "examples"
    );
    assert_eq!(
        n(Query {
            q: "publish".into(),
            ..Default::default()
        }),
        [
            "release",
            "delivery/publish",
            "delivery/publish/tag",
            "delivery/publish/announce"
        ]
    );
    assert_eq!(
        n(Query {
            kind: "agent".into(),
            q: "ships".into(),
            ..Default::default()
        }),
        ["shipper"]
    );
}

#[test]
fn query_parent() {
    let n = |kind: &str, parent: &str| {
        names(&query(Query {
            kind: kind.into(),
            parent: parent.into(),
            ..Default::default()
        }))
    };
    assert_eq!(
        n("step", "delivery"),
        ["delivery/compile", "delivery/publish"]
    );
    assert_eq!(
        n("step", "delivery/publish"),
        ["delivery/publish/tag", "delivery/publish/announce"]
    );
    assert_eq!(n("action", "builder"), ["build"]);
    assert_eq!(n("", "shipper"), ["build", "release", "built", "ship"]);
    assert_eq!(n("trigger", "builder"), ["nightly"]);
    let r = query(Query {
        parent: "nobody".into(),
        ..Default::default()
    });
    assert_eq!(r.total, 0, "{r:?}");
}

#[test]
fn query_detail_levels() {
    // summary is the default with a kind: a one-line description and the defining fields
    let r = query(Query {
        kind: "action".into(),
        name: "build".into(),
        ..Default::default()
    });
    let it = &r.items[0];
    assert!(
        it["description"] == "Compile the shop"
            && it["actionKind"] == "builtin"
            && it["kind"] == "action"
            && it.get("code").is_none_or(Value::is_null)
            && it.get("params").is_none_or(Value::is_null),
        "{it:?}"
    );
    // the description of a methodology-sized text is cut to a line in a summary
    let mut m = shop();
    m.agents[0].description = "first line\nsecond line".into();
    let builder = Query {
        kind: "agent".into(),
        name: "builder".into(),
        ..Default::default()
    };
    let rr = run_query(builder.clone(), &[&m]).unwrap();
    assert_eq!(rr.items[0]["description"], "first line");
    m.agents[0].description = "x".repeat(500);
    let rr = run_query(builder, &[&m]).unwrap();
    let d = rr.items[0]["description"].as_str().unwrap();
    assert!(d.len() <= SUMMARY_TEXT + 4, "{}", d.len());
    // full: the definition, with the parameters; the steps of a process are named, not nested
    let r = query(Query {
        kind: "action".into(),
        name: "build".into(),
        detail: DETAIL_FULL.into(),
        ..Default::default()
    });
    let it = &r.items[0];
    assert!(
        it.get("params").is_some_and(|v| !v.is_null())
            && it["builtin"] == "test.emit"
            && it.get("effects").is_some_and(|v| !v.is_null()),
        "{it:?}"
    );
    let r = query(Query {
        kind: "process".into(),
        detail: DETAIL_FULL.into(),
        ..Default::default()
    });
    let st = strs(&r.items[0]["steps"]);
    assert!(
        st.len() == 2 && st[0] == "delivery/compile",
        "{:?}",
        r.items[0]
    );
    let r = query(Query {
        kind: "step".into(),
        name: "delivery/publish".into(),
        detail: DETAIL_FULL.into(),
        ..Default::default()
    });
    let st = strs(&r.items[0]["steps"]);
    assert!(
        st.len() == 2 && r.items[0]["how"] == "steps",
        "{:?}",
        r.items[0]
    );
    // names
    let r = query(Query {
        kind: "agent".into(),
        detail: DETAIL_NAMES.into(),
        ..Default::default()
    });
    assert_eq!(r.items[0].len(), 2, "{:?}", r.items[0]);
    assert!(
        run_query(
            Query {
                detail: "huge".into(),
                ..Default::default()
            },
            &[]
        )
        .is_err(),
        "unknown detail"
    );
    // the library names its conditions
    let r = query(qk("library"));
    assert!(
        !strs(&r.items[0]["conditions"]).is_empty(),
        "{:?}",
        r.items[0]
    );
}

#[test]
fn query_fields_projection() {
    let r = query(Query {
        kind: "agent".into(),
        fields: vec![sv("roles"), sv("planner")],
        ..Default::default()
    });
    let it = &r.items[0];
    assert!(
        it.len() == 3
            && it["name"] == "builder"
            && it.get("roles").is_some_and(|v| !v.is_null())
            && it["kind"] == "agent",
        "{it:?}"
    );
    // without a kind, a field of any kind is valid and present where it exists
    let r = query(Query {
        name: "build".into(),
        fields: vec![sv("actionKind"), sv("planner")],
        ..Default::default()
    });
    assert!(
        r.items.len() == 1 && r.items[0]["actionKind"] == "builtin",
        "{:?}",
        r.items
    );
    let err = run_query(
        Query {
            kind: "goal".into(),
            fields: vec![sv("pre"), sv("colour")],
            ..Default::default()
        },
        &[],
    )
    .unwrap_err();
    assert!(err.contains("colour"), "{err}");
    assert!(
        err.contains("description, examples"),
        "lists the valid fields: {err}"
    );
}

#[test]
fn query_pagination() {
    let r = query(Query {
        kind: "step".into(),
        limit: 3,
        ..Default::default()
    });
    assert!(
        r.total == 4 && r.returned == 3 && r.next.is_some_and(|n| n.offset == 3) && !r.truncated,
        "{r:?}"
    );
    let r = query(Query {
        kind: "step".into(),
        limit: 3,
        offset: 3,
        ..Default::default()
    });
    assert!(
        r.returned == 1
            && r.next.is_none()
            && r.offset == 3
            && names(&r)[0] == "delivery/publish/announce",
        "{r:?}"
    );
    let r = query(Query {
        kind: "step".into(),
        offset: 99,
        ..Default::default()
    });
    assert!(r.returned == 0 && r.next.is_none() && r.total == 4, "{r:?}");
    // the limit is capped
    let m = Methodology {
        name: "big".into(),
        roles: (0..120)
            .map(|i| Role {
                name: format!("r{i:03}"),
                ..Default::default()
            })
            .collect(),
        ..Default::default()
    };
    let r = run_query(
        Query {
            kind: "role".into(),
            detail: DETAIL_NAMES.into(),
            limit: 1000,
            ..Default::default()
        },
        &[&m],
    )
    .unwrap();
    assert!(
        r.returned == MAX_QUERY_LIMIT
            && r.total == 120
            && r.next.is_some_and(|n| n.offset == MAX_QUERY_LIMIT),
        "{r:?}"
    );
    let r = run_query(
        Query {
            kind: "role".into(),
            detail: DETAIL_NAMES.into(),
            ..Default::default()
        },
        &[&m],
    )
    .unwrap();
    assert_eq!(r.returned, DEFAULT_QUERY_LIMIT);
    assert!(
        run_query(
            Query {
                limit: -1,
                ..Default::default()
            },
            &[]
        )
        .is_err(),
        "negative limit"
    );
}

#[test]
fn query_byte_cap() {
    let mut m = Methodology {
        name: "wide".into(),
        roles: (0..50)
            .map(|i| Role {
                name: format!("role{i:02}"),
                description: "d".repeat(400),
                ..Default::default()
            })
            .collect(),
        ..Default::default()
    };
    let r = run_query(
        Query {
            kind: "role".into(),
            detail: DETAIL_FULL.into(),
            limit: 50,
            ..Default::default()
        },
        &[&m],
    )
    .unwrap();
    // full detail is capped by count before the bytes
    assert!(
        r.returned == MAX_FULL_ITEMS
            && r.truncated
            && r.next.is_some()
            && r.hint.contains("at most 3"),
        "{r:?}"
    );
    // a projection of long texts hits the byte cap
    let r = run_query(
        Query {
            kind: "role".into(),
            fields: vec![sv("description")],
            limit: 50,
            ..Default::default()
        },
        &[&m],
    )
    .unwrap();
    let b = serde_json::to_vec(&r).unwrap();
    assert!(
        b.len() <= MAX_QUERY_BYTES
            && r.truncated
            && r.returned < 50
            && r.returned != 0
            && r.next.is_some_and(|n| n.offset == r.returned)
            && r.hint.contains("narrow"),
        "{} bytes {r:?}",
        b.len()
    );
    // a single item over the cap
    m.roles = vec![Role {
        name: "huge".into(),
        description: "x".into(),
        ..Default::default()
    }];
    let mut params = Map::new();
    params.insert(sv("k"), Value::String("y".repeat(100)));
    for i in 0..200 {
        params.insert(format!("k{i}"), Value::String("z".repeat(100)));
    }
    let mut a = Action::default();
    a.name = "a".into();
    a.params = params;
    m.actions = vec![a];
    let r = run_query(
        Query {
            kind: "action".into(),
            detail: DETAIL_FULL.into(),
            ..Default::default()
        },
        &[&m],
    )
    .unwrap();
    assert!(
        r.returned == 0 && r.truncated && r.hint.contains("fields"),
        "{r:?}"
    );
}

#[test]
fn query_many_methodologies_names_them() {
    let a = shop();
    let b = Methodology {
        name: "other".into(),
        roles: vec![Role {
            name: "tester".into(),
            ..Default::default()
        }],
        ..Default::default()
    };
    let r = run_query(qk("role"), &[&a, &b]).unwrap();
    assert!(
        r.methodology.is_empty()
            && r.counts.is_empty()
            && r.total == 3
            && r.items[2]["methodology"] == "other"
            && r.items[0]["methodology"] == "shop",
        "{r:?}"
    );
}

// ---- the tool, through the model loop --------------------------------------------------------------------------

/// recorded: the outcome of the tool call, its result as JSON.
#[derive(Debug, Default)]
struct Recorded {
    result: Value,
    error: String,
}

impl Recorded {
    fn total(&self) -> i64 {
        self.result["total"].as_i64().unwrap_or(0)
    }
    fn methodology(&self) -> &str {
        self.result["methodology"].as_str().unwrap_or("")
    }
    fn items(&self) -> Vec<Value> {
        self.result["items"].as_array().cloned().unwrap_or_default()
    }
}

fn query_result(e: &Env, round: usize) -> Recorded {
    let res = e.tool_result(round);
    let o = outcomes(&res);
    let r = &o[0];
    Recorded {
        result: r.get("result").cloned().unwrap_or(Value::Null),
        error: r
            .get("error")
            .and_then(Value::as_str)
            .unwrap_or("")
            .to_string(),
    }
}

async fn query_env(args: &str) -> (Env, Recorded) {
    let e = new_env(&[&call(TOOL_METHODOLOGY_QUERY, args), r#"{"message":"ok"}"#]).await;
    e.send("what is in it?", in_project("PROJ-A"))
        .await
        .unwrap();
    let r = query_result(&e, 1);
    (e, r)
}

#[tokio::test]
async fn methodology_query_defaults_to_the_projects_methodologies() {
    let (e, r) = query_env(r#"{"kind":"agent"}"#).await;
    assert!(
        r.error.is_empty() && r.total() == 3 && r.methodology().is_empty(),
        "{r:?}"
    );
    // PROJ-A applies sdlc (no agent) and delivery (3); "other" belongs to PROJ-B only
    for it in r.items() {
        assert_eq!(it["methodology"], "delivery", "{it:?}");
    }
    assert!(
        e.graph.created().is_empty()
            && e.engine.checks.load(Ordering::SeqCst) == 0
            && e.engine.starts().is_empty(),
        "read-only: no graph and no engine call"
    );
    // the call is a read: the message holds no UI action
    let a = e.answer().await.actions;
    assert!(a.is_empty(), "{a:?}");
    let got = e.model.0.lock().who[1].subject.clone();
    assert_eq!(got, "u1", "acts as the caller");
}

#[tokio::test]
async fn methodology_query_one_methodology() {
    let (_e, r) = query_env(r#"{"methodology":"delivery"}"#).await;
    assert!(
        r.error.is_empty()
            && r.methodology() == "delivery"
            && r.result["counts"]["agent"] == 3
            && r.result["counts"]["action"] == 2
            && r.items()[0]["kind"] == "agent",
        "{r:?}"
    );
    let (_e, r) =
        query_env(r#"{"methodology":"delivery","kind":"action","name":"rel*","detail":"full"}"#)
            .await;
    assert!(
        r.error.is_empty() && r.total() == 1 && r.items()[0]["builtin"] == "test.emit",
        "{r:?}"
    );
    let (_e, r) = query_env(
        r#"{"methodology":"delivery","kind":"agent","parent":"","q":"ships","fields":["roles"],"limit":1,"offset":0}"#,
    )
    .await;
    assert!(
        r.error.is_empty()
            && r.total() == 1
            && r.items()[0].get("roles").is_some_and(|v| !v.is_null()),
        "{r:?}"
    );
}

#[tokio::test]
async fn methodology_query_refuses_what_the_project_does_not_apply() {
    let (_e, r) = query_env(r#"{"methodology":"other"}"#).await;
    assert!(
        r.error
            .contains(r#"methodology "other" does not apply to the active project"#),
        "{r:?}"
    );
    // the message is the one create_change gives
    let e = new_env(&[
        &call(
            TOOL_CREATE_CHANGE,
            r#"{"title":"t","intent":"i","methodology":"other"}"#,
        ),
        r#"{"message":"ok"}"#,
    ])
    .await;
    e.searched().await;
    e.send("x", in_project("PROJ-A")).await.unwrap();
    let r2 = query_result(&e, 1);
    assert_eq!(r2.error, r.error);
    // an unknown methodology answers the same
    let (_e, r) = query_env(r#"{"methodology":"nope"}"#).await;
    assert!(r.error.contains("does not apply"), "{r:?}");
}

#[tokio::test]
async fn methodology_query_refuses_bad_arguments() {
    for (args, want) in [
        (r#"{"kind":"tool"}"#, "unknown kind"),
        (r#"{"detail":"huge"}"#, "unknown detail"),
        (r#"{"fields":["nope"]}"#, "unknown fields nope"),
        (r#"{"fields":"roles"}"#, "list of field names"),
        (r#"{"limit":"ten"}"#, "whole number"),
        (r#"{"limit":1.5}"#, "whole number"),
        (r#"{"offset":-1}"#, "negative"),
        (r#"{"colour":"red"}"#, r#"unknown argument "colour""#),
        (r#"{"kind":"goal","fields":[1]}"#, "list of field names"),
    ] {
        let (_e, r) = query_env(args).await;
        assert!(r.error.contains(want), "{args}: {r:?}");
    }
}

#[tokio::test]
async fn methodology_query_counts_as_a_tool_call() {
    let calls = [r#"{"name":"methodology_query","arguments":{"kind":"role"}}"#; MAX_TOOL_CALLS + 1]
        .join(",");
    let first = format!(r#"{{"message":"","tool_calls":[{calls}]}}"#);
    let e = new_env(&[&first, r#"{"message":"ok"}"#]).await;
    e.send("x", in_project("PROJ-A")).await.unwrap();
    let res = e.tool_result(1);
    assert!(res.contains("too many tool calls"), "{res}");
}

#[tokio::test]
async fn methodology_query_leaves_the_other_tools_alone() {
    let e = new_env(&[&call(TOOL_LIST_METHODOLOGIES, "{}"), r#"{"message":"ok"}"#]).await;
    e.send("x", in_project("PROJ-A")).await.unwrap();
    let res = e.tool_result(1);
    assert!(
        res.contains("Deliver software") && !res.contains("total"),
        "{res}"
    );
    let all = tools();
    for n in [
        TOOL_LIST_METHODOLOGIES,
        TOOL_SELECT_PROJECT,
        TOOL_CREATE_CHANGE,
        TOOL_OPEN_CHANGE,
        TOOL_LIST_AGENTS,
        TOOL_START_AGENT,
    ] {
        assert!(all.contains(&n), "{n}");
    }
    let rules = system_rules();
    assert!(
        rules.contains("methodology_query") && rules.contains("Always start narrow"),
        "prompt"
    );
}
