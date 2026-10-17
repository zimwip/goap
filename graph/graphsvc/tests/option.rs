mod common;

use std::sync::Arc;

use chrono::Datelike;
use common::{client, graph, serve};
use goap_connect::Interceptors;
use goap_ctx::Ctx;
use goap_decision as decision;
use goap_domain::{
    DIFF_ADDED, DIFF_REMOVED, MAIN_BRANCH, MAIN_FLOW, OUTCOME_DECIDED, OUTCOME_UNDECIDABLE,
    POINT_BLOCKED, POINT_DECIDED, POINT_RATIFYING, TYPE_DECISION_POINT, decision_points_of,
    options_of,
};
use goap_graph::{
    NewChange, NodeCheckout, NodeUpdate, OpenDecisionRequest, OpenOptionRequest, RuleRequest,
    VIEW_WRITTEN,
};
use goap_graphapi::Port;
use goap_graphsvc::Handler;

/// The options of a change through the service (ADR 0032 §6): the client the engine and the built-in MCPs use in the
/// services round-trips what the in-process graph gives.
#[tokio::test]
async fn options_through_the_service() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    let cl = client(&serve(Handler::new(g.clone()), Interceptors::none()).await);

    let req1 = Box::pin(goap_graphtest::import(
        ctx,
        &g,
        goap_graphtest::Node {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            properties: obj! {"title" => "one"},
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "t".into(),
                baseline_id: base.id,
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let a = cl
        .open_option(
            ctx,
            &c.id,
            OpenOptionRequest {
                name: "a".into(),
                hypothesis: "h".into(),
                activate: true,
                ..Default::default()
            },
        )
        .await;
    let a = match a {
        Ok(a) if a.option.as_ref().is_some_and(|o| o.name == "a") && a.active => a,
        other => panic!("open: {other:?}"),
    };
    let pre = req1.r#ref();
    let added = g
        .impact_node_checkout(
            ctx,
            &c.id,
            NodeCheckout {
                node: pre.id.clone(),
                rationale: "why".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    g.impact_node_update(
        ctx,
        &c.id,
        &added.id,
        NodeUpdate {
            properties: obj! {"title" => "two"},
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let nodes = cl.change_graph(ctx, &c.id, "").await;
    assert!(
        matches!(&nodes, Ok((n, _)) if n.len() == 1 && n[0].properties.get("title") == Some(&serde_json::json!("two"))),
        "change graph on the active option: {nodes:?}"
    );
    let v = cl.change_view(ctx, &c.id, MAIN_FLOW, VIEW_WRITTEN).await;
    assert!(matches!(&v, Ok(v) if v.contains(&pre)), "main view: {v:?}");
    let cmp = cl.compare_options(ctx, &c.id, "", false).await;
    assert!(
        matches!(&cmp, Ok(cmp) if cmp.nodes.len() == 1
            && matches!(cmp.nodes[0].options.get(&a.id), Some(Some(_)))
            && cmp.nodes[0].props.get(&a.id).and_then(|p| p.get("title")) == Some(&serde_json::json!("two"))),
        "compare: {cmp:?}"
    );
    let df = cl.diff_flows(ctx, &c.id, "main", &a.id, "").await;
    assert!(
        matches!(&df, Ok(df) if df.impacts.len() == 1 && df.impacts[0].category == DIFF_ADDED && df.impacts[0].key == "REQ-1" && df.right == a.id),
        "diff flows: {df:?}"
    );
    let df = cl.diff_flows(ctx, &c.id, &a.id, "main", "").await;
    assert!(
        matches!(&df, Ok(df) if df.impacts.len() == 1
            && df.impacts[0].category == DIFF_REMOVED
            && df.impacts[0].left.as_ref().is_some_and(|l| l.impact == added.id)),
        "diff flows reversed: {df:?}"
    );
    let f = cl.evaluate_option(ctx, &c.id, &a.id, "", "good").await;
    assert!(
        matches!(&f, Ok(f) if f.evaluation == "good" && f.evaluated),
        "evaluate: {f:?}"
    );
    let active = cl.activate_option(ctx, &c.id, MAIN_FLOW, "").await;
    assert!(
        matches!(&active, Ok(a) if a.is_empty()),
        "deactivate: {active:?}"
    );
    let os = cl.options(ctx, &c.id).await;
    assert!(
        matches!(&os, Ok(os) if os.len() == 1 && !os[0].active),
        "options: {os:?}"
    );
    let vs = cl.change_view(ctx, &c.id, &a.id, VIEW_WRITTEN).await;
    assert!(
        matches!(&vs, Ok(v) if !v.contains(&pre)),
        "option view: {vs:?}"
    );
}

/// Decision points through the service (ADR 0009 §4): the engine's client opens, rules and answers them, the
/// blackboard carries them with the options, and a person ratifies through the API.
#[tokio::test]
async fn decisions_through_the_service() {
    let ctx = &Ctx::background();
    let mut g = graph();
    g.decision_policy = Some(Arc::new(decision::Policy));
    let g = Arc::new(g);
    let cl = client(&serve(Handler::new(g.clone()), Interceptors::none()).await);

    let _ = Box::pin(goap_graphtest::import(
        ctx,
        &g,
        goap_graphtest::Node {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            ..Default::default()
        },
    ))
    .await;
    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "t".into(),
                baseline_id: base.id,
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let option = |name: &str| OpenOptionRequest {
        name: name.into(),
        ..Default::default()
    };
    let a = g.open_option(ctx, &c.id, option("a")).await.unwrap();
    let _ = g.open_option(ctx, &c.id, option("b")).await;
    let d = cl
        .open_decision(
            ctx,
            &c.id,
            OpenDecisionRequest {
                question: "a or b?".into(),
                criteria: vec!["cost".into()],
                policy: obj! {decision::KEY_MAX_DURATION => "48h"},
                ..Default::default()
            },
        )
        .await;
    let d = match d {
        Ok(d)
            if d.options.len() == 2
                && d.policy.contains_key(decision::KEY_DEADLINE)
                && d.criteria[0] == "cost" =>
        {
            d
        }
        other => panic!("open: {other:?}"),
    };
    let d = cl
        .rule_decision(
            ctx,
            &c.id,
            RuleRequest {
                point: d.id.clone(),
                outcome: OUTCOME_UNDECIDABLE.into(),
                justification: "?".into(),
                questions: vec!["cost of a?".into()],
                ..Default::default()
            },
        )
        .await;
    let d = match d {
        Ok(d) if d.status == POINT_BLOCKED && d.questions.len() == 1 => d,
        other => panic!("undecidable: {other:?}"),
    };
    let bb = cl.blackboard_in(ctx, &c.id, "").await.unwrap();
    assert!(
        decision_points_of(&bb).len() == 1
            && options_of(&bb).len() == 2
            && bb.at.is_some_and(|t| t.year() > 1),
        "blackboard: {:?} {:?}",
        decision_points_of(&bb),
        options_of(&bb)
    );
    cl.answer_question(ctx, &c.id, &d.questions[0].id, "cheap", "", "")
        .await
        .unwrap();
    // the engine's rulings are an agent's: below the threshold, a person ratifies
    let d = cl
        .rule_decision(
            ctx,
            &c.id,
            RuleRequest {
                point: d.id.clone(),
                outcome: OUTCOME_DECIDED.into(),
                option: "a".into(),
                confidence: 0.4,
                justification: "cheaper".into(),
                ..Default::default()
            },
        )
        .await;
    let d = match d {
        Ok(d) if d.status == POINT_RATIFYING && d.ruling.as_ref().is_some_and(|r| !r.human) => d,
        other => panic!("agent ruling: {other:?}"),
    };
    let d = cl.ratify_decision(ctx, &c.id, &d.id, true, "", "ok").await;
    assert!(
        matches!(&d, Ok(d) if d.status == POINT_DECIDED && d.option == a.id),
        "ratified: {d:?}"
    );
    let ds = cl.decision_points(ctx, &c.id).await;
    assert!(
        matches!(&ds, Ok(ds) if ds.len() == 1 && ds[0].status == POINT_DECIDED),
        "list: {ds:?}"
    );
    // the facts of the decision come back as items with their events
    let ch = g.change(ctx, &c.id).await.unwrap();
    let n = ch
        .items
        .iter()
        .filter(|it| it.r#type == TYPE_DECISION_POINT && it.decision_event.is_some())
        .count();
    assert_eq!(n, 5, "decision events");
}
