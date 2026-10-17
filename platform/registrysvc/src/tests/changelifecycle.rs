//! changelifecycle_test.go and the parts of activitygoals_test.go / subactivity_test.go that run through the real
//! graph: the registry plugged as the graph's guardian (`ChangeLifecycles`) and sub-change validator.

use std::sync::Arc;

use goap_domain::{
    ChangeItem, DEFAULT_NAMESPACE, ITEM_ACCEPTED, MAIN_BRANCH, OUTCOME_DECIDED, REVIEW_ACCEPTED,
    TYPE_ARTIFACT,
};
use goap_graph::Graph;
use goap_graphapi::{
    Commit, LinkEdit, NewChange, NodeCreate, NodeEdit, OpenDecisionRequest, RuleRequest,
    TransitionRequest,
};
use goap_methodology::{Condition, Methodology, Process, Step};
use serde_json::{Map, Value, json};

use super::realgraph::Late;
use super::{bg, registry};
use crate::activitygoals::{DATA_ACTIVITY, activity_of};
use crate::graphstore::{
    GraphStore, LINK_SUB_ACTIVITY, NAMESPACE_METHODOLOGY, methodology_version_key,
};
use crate::service::Service;
use crate::store::{Record, STATUS_PUBLISHED, Store};

/// A graph in memory guarded by a registry whose store is over that graph (Go: `g.Lifecycles = reg`, and
/// `g.SubChangeValidator = reg.SubChangeValidator` when `sub` is set).
fn guarded(
    configure: impl FnOnce(&mut Graph),
    sub: bool,
) -> (
    Arc<Graph>,
    Arc<GraphStore>,
    Arc<Service>,
    Arc<goap_domainsvc::Service>,
) {
    let late = Arc::new(Late::default());
    let mut g = Graph::new(Arc::new(goap_graph::Memory::new()));
    if sub {
        g.sub_change_validator = Some(late.clone());
    } else {
        g.lifecycles = Some(late.clone());
    }
    configure(&mut g);
    let g = Arc::new(g);
    let store = Arc::new(GraphStore::new(g.clone()));
    let (reg, d) = registry(store.clone(), false, |_| {});
    late.bind(&reg);
    (g, store, reg, d)
}

fn conds(names: &[&str]) -> std::collections::BTreeMap<String, bool> {
    names.iter().map(|n| (n.to_string(), true)).collect()
}

fn data(r: &str) -> Map<String, Value> {
    let mut m = Map::new();
    m.insert(DATA_ACTIVITY.into(), r.into());
    m
}

async fn publish(ctx: &goap_ctx::Ctx, store: &GraphStore, m: Methodology) {
    store
        .save(
            ctx,
            Record {
                methodology: m,
                status: STATUS_PUBLISHED,
                ..Record::default()
            },
        )
        .await
        .expect("save");
}

// End-to-end (ADR 0058): a methodology names a lifecycle of its domain; its changes start in the initial state, and
// a gate of the lifecycle is a CEL guard over a decided decision point and the world state of the methodology,
// where "state:<name>" is the generated condition of a state.
#[tokio::test]
async fn change_lifecycle_gate() {
    let ctx = bg();
    let (g, store, reg, d) = guarded(
        |g| g.decision_policy = Some(Arc::new(goap_decision::Policy)),
        false,
    );
    let dom = goap_def::parse_domain(
        br#"
name: alm
version: "1"
lifecycles:
  - name: maturity
    initial: proposed
    states: [{name: proposed}, {name: analysing}, {name: implementing}, {name: done, final: true}]
    transitions:
      - {name: analyse, from: proposed, to: analysing}
      - name: implement
        from: analysing
        to: implementing
        guard: 'decisionPoints.exists(d, d.id == change.decision && d.status == "decided" && d.option == "go") && (world["analysed"] || actions.exists(a, a.for == change.decision))'
"#,
    )
    .expect("domain");
    d.store
        .save_domain(
            &ctx,
            goap_def::Record {
                domain: dom,
                status: goap_def::STATUS_PUBLISHED,
                ..Default::default()
            },
        )
        .await
        .expect("save domain");
    let mut m = Methodology::default();
    m.name = "delivery".into();
    m.version = "1".into();
    m.namespace = "alm".into();
    m.lifecycle = "maturity".into();
    m.conditions = vec![Condition {
        name: "analysed".into(),
        expr: r#"artifacts.exists(a, a.target == "analysis")"#.into(),
        ..Condition::default()
    }];
    let mut analyse = Step::default();
    analyse.name = "analyse".into();
    analyse.pre = conds(&["state:analysing"]);
    analyse.done = conds(&["analysed"]);
    let mut build = Step::default();
    build.name = "build".into();
    build.pre = conds(&["state:implementing"]);
    let mut p = Process::default();
    p.name = "deliver".into();
    p.steps = vec![analyse, build];
    m.processes = vec![p];
    publish(&ctx, &store, m).await;

    let c = reg
        .methodology(&ctx, "delivery")
        .await
        .expect("methodology");
    assert!(
        c.conditions.has("state:implementing"),
        "state:<name> is a generated condition"
    );
    let build = c.action("deliver/build").expect("deliver/build");
    assert!(
        build.pre.get("state:implementing") == Some(&true),
        "a step is active in the state it names: {:?}",
        build.pre
    );

    let ch = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "ship".into(),
                methodology: "delivery".into(),
                namespace: "alm".into(),
                ..Default::default()
            },
        )
        .await
        .expect("create");
    assert!(
        ch.lifecycle == "maturity" && ch.state == "proposed",
        "a change starts in the initial state: {:?} {:?}",
        ch.lifecycle,
        ch.state
    );
    // the change starts with the main goal of its methodology: its first process here (ADR 0096)
    assert_eq!(ch.goal, "deliver", "default goal");
    let other = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "free".into(),
                methodology: "no-such".into(),
                namespace: "alm".into(),
                ..Default::default()
            },
        )
        .await
        .expect("a methodology without lifecycle");
    assert_eq!(
        other.lifecycle, "",
        "a methodology without lifecycle leaves the change without state"
    );
    g.transition_change(
        &ctx,
        &ch.id,
        TransitionRequest {
            transition: "analyse".into(),
            ..Default::default()
        },
    )
    .await
    .expect("analyse");
    let mut policy = Map::new();
    policy.insert(
        goap_decision::KEY_DECIDER.into(),
        goap_decision::DECIDER_HUMAN.into(),
    );
    let pt = g
        .open_decision(
            &ctx,
            &ch.id,
            OpenDecisionRequest {
                question: "analysis complete?".into(),
                policy,
                ..Default::default()
            },
        )
        .await
        .expect("open");
    g.rule_decision(
        &ctx,
        &ch.id,
        RuleRequest {
            point: pt.id.clone(),
            outcome: OUTCOME_DECIDED.into(),
            option: "go".into(),
            confidence: 1.0,
            justification: "ok".into(),
            human: true,
            ..Default::default()
        },
    )
    .await
    .expect("rule");
    let implement = TransitionRequest {
        transition: "implement".into(),
        decision: pt.id.to_string(),
        ..Default::default()
    };
    let err = g
        .transition_change(&ctx, &ch.id, implement.clone())
        .await
        .expect_err("the expected world state is not reached");
    assert!(err.is_conflict(), "{err}");
    // the decision prevails over the world state once actions are taken to fill the gap
    let act = json!({"key": "ACT-1", "title": "finish the analysis", "for": pt.id});
    g.add_items(
        &ctx,
        &ch.id,
        vec![ChangeItem {
            r#type: goap_risk::TYPE_ACTION,
            status: ITEM_ACCEPTED,
            data: act.as_object().unwrap().clone(),
            ..ChangeItem::default()
        }],
    )
    .await
    .expect("action");
    let got = g
        .transition_change(&ctx, &ch.id, implement)
        .await
        .expect("gate");
    assert_eq!(got.state, "implementing");
}

fn shipping() -> Methodology {
    let mut m = Methodology::default();
    m.name = "shipping".into();
    m.version = "1".into();
    m.namespace = "alm".into();
    m.conditions = vec![Condition {
        name: "signed".into(),
        expr: r#"artifacts.exists(a, a.target == "signoff")"#.into(),
        ..Condition::default()
    }];
    let mut confirm = Step::default();
    confirm.name = "confirm".into();
    confirm.done = conds(&["signed"]);
    let mut p = Process::default();
    p.name = "ship".into();
    p.steps = vec![confirm];
    m.processes = vec![p];
    m
}

fn artifact(target: &str) -> ChangeItem {
    ChangeItem {
        r#type: TYPE_ARTIFACT,
        target: target.into(),
        status: ITEM_ACCEPTED,
        ..ChangeItem::default()
    }
}

// End-to-end: a change scoped to a real Activity (a methodology@Process node) is gated at Apply by that process's own
// compiled goal condition, resolved from the registry (Service::merge as the guardian of the merge).
#[tokio::test]
async fn activity_goals_met_gates_apply() {
    let ctx = bg();
    let (g, store, _reg, _d) = guarded(|_| {}, false);
    let m = shipping();
    publish(&ctx, &store, m.clone()).await;
    let activity_ref = format!(
        "{}/process/ship",
        methodology_version_key(&m.name, &m.version)
    );
    let base = g
        .branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH)
        .await
        .expect("head");
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "ship it".into(),
                baseline_id: base.id,
                data: data(&activity_ref),
                ..Default::default()
            },
        )
        .await
        .expect("create");

    // an empty change has nothing to commit (ADR 0102)
    let err = Box::pin(g.apply(&ctx, &c.id, ""))
        .await
        .expect_err("empty change");
    assert!(err.is_nothing_to_commit(), "empty change: {err}");
    // it ships a node: committed, its merge is what the goal of the activity holds
    let cn = g
        .impact_node_create(
            &ctx,
            &c.id,
            NodeCreate {
                key: "SHIP-1".into(),
                r#type: "Shipment".into(),
                rationale: "ship".into(),
                ..Default::default()
            },
        )
        .await
        .expect("create node");
    g.impact_node_review(&ctx, &c.id, &cn.id, REVIEW_ACCEPTED, "reviewer", "ok")
        .await
        .expect("review");
    let unmet = |e: goap_graph::Error, what: &str| {
        assert!(
            e.is_invalid() && e.to_string().contains("does not satisfy the goal"),
            "{what}: {e}"
        );
    };
    unmet(
        Box::pin(g.apply(&ctx, &c.id, "")).await.expect_err("unmet"),
        "goal unmet",
    );

    // an unrelated artifact still doesn't satisfy it
    g.add_items(&ctx, &c.id, vec![artifact("note")])
        .await
        .expect("note");
    unmet(
        Box::pin(g.apply(&ctx, &c.id, "")).await.expect_err("unmet"),
        "unrelated artifact",
    );

    // the signoff artifact satisfies the process's goal: Apply succeeds
    g.add_items(&ctx, &c.id, vec![artifact("signoff")])
        .await
        .expect("signoff");
    Box::pin(g.apply(&ctx, &c.id, "")).await.expect("goal met");
}

// An unresolvable ActivityRef (unknown methodology, or an item name that doesn't exist in it) is a hard error, not a
// silent pass: a stale or malformed reference must not quietly disable the gate it was meant to enforce.
#[tokio::test]
async fn activity_goals_met_refuses_an_unresolvable_ref_at_apply() {
    let ctx = bg();
    let (g, _store, _reg, _d) = guarded(|_| {}, false);
    let base = g
        .branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH)
        .await
        .expect("head");
    let c = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "x".into(),
                baseline_id: base.id,
                data: data("MV:no-such@1/process/p"),
                ..Default::default()
            },
        )
        .await
        .expect("create");
    assert!(
        Box::pin(g.apply(&ctx, &c.id, "")).await.is_err(),
        "an unresolvable activity ref must refuse, not silently pass"
    );
}

// A sub-change's Activity, when it names one, must be the parent's own or a descendant of it reached by
// sub_activity links (the cascade asked by the graph at CreateChange); unset is fine (no inheritance).
#[tokio::test]
async fn sub_change_activity_cascade_at_create_change() {
    let ctx = bg();
    let (g, _store, _reg, _d) = guarded(|_| {}, true);
    let mk = |key: &str, links: Vec<LinkEdit>| NodeEdit {
        key: key.into(),
        r#type: "methodology@Process".into(),
        links,
        ..NodeEdit::default()
    };
    let head = g
        .branch_head(&ctx, NAMESPACE_METHODOLOGY, MAIN_BRANCH)
        .await
        .expect("head");
    Box::pin(g.commit(
        &ctx,
        Commit {
            project_id: "PROJ-ROOT".into(),
            namespace: NAMESPACE_METHODOLOGY.into(),
            title: "activities".into(),
            baseline: head.id,
            by: "test".into(),
            edits: vec![
                mk(
                    "ACT-PARENT",
                    vec![LinkEdit {
                        r#type: LINK_SUB_ACTIVITY.into(),
                        to_key: "ACT-CHILD".into(),
                        ..LinkEdit::default()
                    }],
                ),
                mk("ACT-CHILD", vec![]),
                mk("ACT-OTHER", vec![]),
            ],
            ..Commit::default()
        },
    ))
    .await
    .expect("commit");

    let base = g
        .branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH)
        .await
        .expect("head");
    let parent = g
        .create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "p".into(),
                baseline_id: base.id,
                own_branch: true,
                data: data("ACT-PARENT"),
                ..Default::default()
            },
        )
        .await
        .expect("parent");
    let sub = |title: &str, d: Map<String, Value>| NewChange {
        title: title.into(),
        parent_id: parent.id.clone(),
        data: d,
        ..Default::default()
    };
    let within = g
        .create_change(&ctx, sub("within", data("ACT-CHILD")))
        .await
        .expect("activity within the parent's");
    assert_eq!(activity_of(&within), "ACT-CHILD");
    let err = g
        .create_change(&ctx, sub("outside", data("ACT-OTHER")))
        .await
        .expect_err("activity outside the parent's");
    assert!(err.is_invalid(), "activity outside the parent's: {err}");
    let no_act = g
        .create_change(&ctx, sub("unscoped", Map::new()))
        .await
        .expect("unscoped");
    assert_eq!(activity_of(&no_act), "", "activity not inherited");
}
