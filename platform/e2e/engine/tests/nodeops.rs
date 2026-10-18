//! Go: nodeops_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_domain::{self as domain, ChangeImpact, ChangeImpactID};
use goap_dsl::{NodeOp, NodeOpLink};
use goap_engine::nodeops::change_impacts_from_blackboard;
use goap_engine::{Engine, MemoryStore, Process, StaticMethodologies};
use goap_graph::{Graph, LinkWrite, Memory, NewChange, OpenFlowRequest, TypeCatalog};
use goap_graphtest as graphtest;
use serde_json::{Value, json};

use crate::common::*;

pub fn bare_engine(g: &Arc<Graph>) -> Arc<Engine> {
    Arc::new(Engine::new(
        g.clone(),
        Arc::new(StaticMethodologies::new()),
        Arc::new(MemoryStore::new()),
    ))
}

pub async fn root_change(
    g: &Graph,
    ns: &str,
    title: &str,
    base: &domain::BaselineID,
) -> domain::Change {
    g.create_change(
        &ctx(),
        NewChange {
            project_id: "PROJ-ROOT".into(),
            namespace: ns.into(),
            title: title.into(),
            baseline_id: base.clone(),
            own_branch: true,
            ..Default::default()
        },
    )
    .await
    .unwrap()
}

pub fn on_change(id: &domain::ChangeID) -> Process {
    Process {
        change_id: id.clone(),
        ..Default::default()
    }
}

/// A script declares, writes and reviews change impacts; the engine applies the operations.
#[tokio::test]
async fn script_change_impacts() {
    let ctx = ctx();
    let (e, g, base, _errs) = setup().await.share();
    let c = root_change(&g, "alm", "PSP v2", &base).await;
    let p = on_change(&c.id);
    let res = run_script(
        &ctx,
        js(
            "plan",
            r#"
const req = ctx.impactNode("REQ-1", "PSP v2 changes the payment API");
const tst = ctx.impactNodeCreate("TestCase", "TST-2", "cover the new API");
ctx.writeNode(req, { props: { title: "Use PSP v2" } });
ctx.writeNode(tst, { props: { title: "PSP v2 test" }, links: [{ type: "verifies", to: req }] });
ctx.impactNodeReview(req, true, "confirmed with the PSP team");
"#,
        ),
    )
    .await;
    assert!(
        res.nodes.len() == 5
            && res.nodes[0].op == "declare"
            && res.nodes[0].r#ref == "#n1"
            && res.nodes[3].links[0].to == "#n1",
        "unexpected operations: {:?}",
        res.nodes
    );
    let x = Engine::internals_of(&e);
    x.apply_node_ops(&ctx, &p, res.nodes, "plan", "exec-1")
        .await
        .unwrap();
    let ch = g.change(&ctx, &c.id).await.unwrap();
    assert_eq!(
        ch.nodes.len(),
        2,
        "expected 2 change impacts: {:?}",
        ch.nodes
    );
    let (req, tst) = (&ch.nodes[0], &ch.nodes[1]);
    assert!(
        req.key == "REQ-1"
            && req.post.is_some()
            && req.review == domain::REVIEW_ACCEPTED
            && req.produced_by == "plan"
            && req.execution == "exec-1",
        "REQ-1 change impact: {req:?}"
    );
    assert!(
        tst.key == "TST-2" && tst.post.is_some() && tst.review == domain::REVIEW_PROPOSED,
        "TST-2 change impact: {tst:?}"
    );
    // a created node has no proposal: declared then written in one script, it is one created event, then the edit
    let evs = g.change_events(&ctx, &c.id).await.unwrap();
    let tst_ops: Vec<_> = evs
        .iter()
        .filter(|ev| ev.impact == tst.id)
        .map(|ev| ev.op.clone())
        .collect();
    assert!(
        tst_ops.len() >= 2
            && tst_ops[0] == domain::IMPACT_CREATED
            && tst_ops[1] == domain::IMPACT_UPDATED
            && !tst_ops.contains(&domain::IMPACT_PROPOSED),
        "TST-2 log: {tst_ops:?}"
    );
    // a second script reads the change impacts and accepts the other one
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let snap = change_impacts_from_blackboard(&bb);
    assert!(
        snap.len() == 2
            && !snap[0].planned
            && snap[0].post.as_ref().is_some_and(|n| n.key == "REQ-1"),
        "snapshot: {snap:?}"
    );
    let mut job2 = js(
        "check",
        r#"
for (const n of ctx.changeImpacts()) if (n.review === "proposed") { ctx.impactNodeReview(n.key, true, "needed"); }
"#,
    );
    job2.nodes = snap;
    let res2 = run_script(&ctx, job2).await;
    assert_eq!(res2.nodes.len(), 1, "second script: {res2:?}");
    x.apply_node_ops(&ctx, &p, res2.nodes, "check", "")
        .await
        .unwrap();
    Box::pin(g.apply(&ctx, &c.id, "")).await.unwrap();
    let new_req = g.node_by_key(&ctx, "alm", "REQ-1").await.unwrap();
    let new_tst = g.node_by_key(&ctx, "alm", "TST-2").await.unwrap();
    let v = g.view(&ctx, &new_tst.r#ref()).await.unwrap();
    assert!(
        v.out.len() == 1
            && v.out[0].to == new_req.r#ref()
            && new_req.properties.get("title") == Some(&Value::from("Use PSP v2"))
            && new_req.change_impact == req.id,
        "the change must land with the link on the version it wrote: {:?} {new_req:?}",
        v.out
    );

    // a review without a comment and an unknown key are refused
    let review = NodeOp {
        op: "review".into(),
        node: "REQ-1".into(),
        accept: true,
        ..Default::default()
    };
    let write = NodeOp {
        op: "write".into(),
        node: "NOPE-1".into(),
        ..Default::default()
    };
    for (name, ops, msg) in [
        ("no comment", vec![review], "comment"),
        ("unknown", vec![write], "no change impact"),
    ] {
        let err = x.apply_node_ops(&ctx, &p, ops, "x", "").await;
        assert!(
            err.as_ref().is_err_and(|e| e.to_string().contains(msg)),
            "{name}: got {err:?}"
        );
    }
}

/// A relaunched step redoes its change impacts on a flow branch; adopting it replaces the stale ones.
#[tokio::test]
async fn script_change_impacts_on_a_flow() {
    let ctx = ctx();
    let (e, g, base, _errs) = setup().await.share();
    let c = root_change(&g, "alm", "PSP v2", &base).await;
    let x = Engine::internals_of(&e);
    let run = |p: Process, action: &'static str, execution: &'static str, code: &'static str| {
        let x = Engine::internals_of(&e);
        async move {
            let ctx = crate::common::ctx();
            let res = run_script(&ctx, js(action, code)).await;
            x.apply_node_ops(&ctx, &p, res.nodes, action, execution)
                .await
                .unwrap();
        }
    };
    run(
        on_change(&c.id),
        "plan",
        "run-1",
        r#"const r = ctx.impactNode("REQ-1", "first idea"); ctx.writeNode(r, { props: { title: "A" } });"#,
    )
    .await;

    let flow = g
        .open_flow(
            &ctx,
            &c.id,
            OpenFlowRequest {
                origin: obj(json!({"step": 0, "execution": "run-1", "reason": "redo"})),
                stale_runs: vec!["run-1".into()],
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let on_flow = Process {
        change_id: c.id.clone(),
        flow: flow.id.clone(),
        ..Default::default()
    };
    // on the flow the first idea is not there any more: a script sees no change impact and declares its own
    let bb = g.blackboard_in(&ctx, &c.id, &flow.id).await.unwrap();
    let snap = change_impacts_from_blackboard(&bb);
    assert!(
        snap.is_empty(),
        "a relaunched step starts without its stale change impacts: {snap:?}"
    );
    run(
        on_flow,
        "plan",
        "run-2",
        r#"const r = ctx.impactNode("REQ-1", "second idea"); ctx.writeNode(r, { props: { title: "B" } }); ctx.impactNodeReview(r, true, "better");"#,
    )
    .await;
    let bb = g.blackboard_in(&ctx, &c.id, &flow.id).await.unwrap();
    let snap = change_impacts_from_blackboard(&bb);
    assert!(
        snap.len() == 1
            && snap[0].rationale == "second idea"
            && snap[0].review == "accepted"
            && snap[0].post.is_some(),
        "flow snapshot: {snap:?}"
    );
    g.adopt_flow(&ctx, &c.id, &flow.id, "alice").await.unwrap();
    Box::pin(g.apply(&ctx, &c.id, "")).await.unwrap();
    let req = g.node_by_key(&ctx, "alm", "REQ-1").await.unwrap();
    assert!(
        req.properties.get("title") == Some(&Value::from("B")) && req.comment == "better",
        "the adopted flow lands: {req:?}"
    );
    let _ = x;
}

/// A script merges and splits nodes from the side of their parents (ADR 0077).
#[tokio::test]
async fn script_merge_and_split() {
    let ctx = ctx();
    let d = goap_def::parse_domain(
        br#"
name: docs
version: 1.0.0
nodeTypes: [{name: Folder}, {name: Item, attributes: [title]}]
linkTypes: [{name: contains, from: Folder, to: Item, compose: true}]
"#,
    )
    .unwrap();
    let cat: Arc<dyn TypeCatalog> = Arc::new(goap_typecat::new(&[d]).unwrap());
    let mut g = Graph::new(Arc::new(Memory::new()));
    g.types = Some(Arc::new(move || Some(cat.clone())));
    let g = Arc::new(g);
    let mut items = Vec::new();
    for k in ["A", "B", "S"] {
        items.push(
            graphtest::import(
                &ctx,
                &g,
                graphtest::Node {
                    namespace: "docs".into(),
                    key: k.into(),
                    r#type: "docs@Item".into(),
                    ..Default::default()
                },
            )
            .await
            .unwrap(),
        );
    }
    let links = items
        .iter()
        .map(|n| LinkWrite {
            r#type: "docs@contains".into(),
            to: n.r#ref(),
            ..Default::default()
        })
        .collect();
    graphtest::import(
        &ctx,
        &g,
        graphtest::Node {
            namespace: "docs".into(),
            key: "F".into(),
            r#type: "docs@Folder".into(),
            links,
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let head = g
        .branch_head(&ctx, "docs", domain::MAIN_BRANCH)
        .await
        .unwrap();
    let c = root_change(&g, "docs", "restructure", &head.id).await;
    let res = run_script(
        &ctx,
        js(
            "restructure",
            r#"
const m = ctx.impactNodeMerge(["A", "B"], "Item", "AB", "A and B say one thing");
ctx.writeNode(m, { props: { title: "AB" } });
const parts = ctx.impactNodeSplit("S", [{type: "Item", key: "S1", rationale: "first half"}, {type: "Item", key: "S2", rationale: "second half"}]);
ctx.writeNode(parts[1], { props: { title: "second" } });
"#,
        ),
    )
    .await;
    let e = bare_engine(&g);
    Engine::internals_of(&e)
        .apply_node_ops(&ctx, &on_change(&c.id), res.nodes, "restructure", "x1")
        .await
        .unwrap();
    let list = g.list_change_impacts(&ctx, &c.id).await.unwrap();
    let by_key: BTreeMap<String, ChangeImpact> =
        list.into_iter().map(|cn| (cn.key.clone(), cn)).collect();
    for k in ["A", "B", "S"] {
        let via: &ChangeImpactID = &by_key[k].via;
        assert!(
            !via.is_empty() && *via == by_key["F"].id,
            "{k} is realized through F: via {via:?}"
        );
    }
    for k in ["AB", "S1", "S2"] {
        let n = g
            .change_node(&ctx, &c.id, "", by_key[k].post.as_ref().unwrap())
            .await;
        assert!(
            n.as_ref().is_ok_and(|n| !n.origins.is_empty()),
            "{k}: origins {n:?}"
        );
    }
    let n = g
        .change_node(&ctx, &c.id, "", by_key["AB"].post.as_ref().unwrap())
        .await
        .unwrap();
    assert_eq!(
        n.properties.get("title"),
        Some(&Value::from("AB")),
        "AB was written"
    );
    let fv = g
        .change_node_view(&ctx, &c.id, "", by_key["F"].post.as_ref().unwrap())
        .await
        .unwrap();
    assert_eq!(fv.out.len(), 3, "F links AB, S1, S2");
}

/// A creation declared by a script and never written is created bare when the script ends; written, it is created
/// with its properties in one event (ADR 0077).
#[tokio::test]
async fn script_declared_creation() {
    let ctx = ctx();
    let (e, g, base, _errs) = setup().await.share();
    let c = root_change(&g, "alm", "late", &base).await;
    let declare = |r: &str, key: &str, rationale: &str| NodeOp {
        op: "declare".into(),
        r#ref: r.into(),
        intent: domain::INTENT_CREATED.to_string(),
        key: key.into(),
        r#type: "TestCase".into(),
        rationale: rationale.into(),
        ..Default::default()
    };
    let ops = vec![
        declare("#a", "TST-3", "bare"),
        declare("#b", "TST-4", "written"),
        NodeOp {
            op: "write".into(),
            node: "#b".into(),
            props: obj(json!({"title": "four"})),
            ..Default::default()
        },
    ];
    Engine::internals_of(&e)
        .apply_node_ops(&ctx, &on_change(&c.id), ops, "plan", "exec-2")
        .await
        .unwrap();
    let ch = g.change(&ctx, &c.id).await.unwrap();
    assert_eq!(ch.nodes.len(), 2, "change impacts: {:?}", ch.nodes);
    for cn in &ch.nodes {
        assert!(
            cn.intent == domain::INTENT_CREATED && cn.post.as_ref().is_some_and(|p| p.is_draft()),
            "a creation has its draft: {cn:?}"
        );
        assert_eq!(
            cn.review,
            domain::REVIEW_PROPOSED,
            "a write never reviews (ADR 0079): {cn:?}"
        );
    }
    let evs = g.change_events(&ctx, &c.id).await.unwrap();
    for ev in &evs {
        assert!(
            ev.op != domain::IMPACT_PROPOSED
                && ev.op != domain::IMPACT_CHECKED_OUT
                && ev.op != domain::IMPACT_REVIEWED,
            "a creation logs no {} event",
            ev.op
        );
    }
    let i = ch.nodes.iter().position(|cn| cn.key == "TST-4").unwrap();
    let n = g
        .change_node(&ctx, &c.id, "", ch.nodes[i].post.as_ref().unwrap())
        .await
        .unwrap();
    assert_eq!(
        n.properties.get("title"),
        Some(&Value::from("four")),
        "the properties of the write"
    );
}

/// A script plans a creation (an impact analysis naming what is new): an impact with no draft; another action writes
/// it under its key, which creates the node on the same impact; a link to a planned key creates it bare.
#[tokio::test]
async fn script_planned_creation() {
    let ctx = ctx();
    let (e, g, base, _errs) = setup().await.share();
    let c = root_change(&g, "alm", "green field", &base).await;
    let res = run_script(
        &ctx,
        js(
            "identify",
            r#"
ctx.impactNodePlan("Requirement", "REQ-9", "the change needs a new requirement");
ctx.impactNodePlan("TestCase", "TST-9", "and its test");
"#,
        ),
    )
    .await;
    let x = Engine::internals_of(&e);
    x.apply_node_ops(&ctx, &on_change(&c.id), res.nodes, "identify", "exec-1")
        .await
        .unwrap();
    let ch = g.change(&ctx, &c.id).await.unwrap();
    assert_eq!(ch.nodes.len(), 2, "change impacts: {:?}", ch.nodes);
    for cn in &ch.nodes {
        assert!(
            cn.intent == domain::INTENT_CREATED
                && cn.planned()
                && cn.produced_by == "identify"
                && cn.r#type.starts_with("alm@"),
            "a planned creation has no draft: {cn:?}"
        );
    }
    let bb = g.blackboard(&ctx, &c.id).await.unwrap();
    let snap = change_impacts_from_blackboard(&bb);
    assert!(
        snap[0].planned && snap[0].intent == "created",
        "a script sees it planned: {:?}",
        snap[0]
    );
    // the specification writes the planned keys
    let ops = vec![NodeOp {
        op: "write".into(),
        node: "TST-9".into(),
        props: obj(json!({"title": "nine"})),
        links: vec![NodeOpLink {
            r#type: "verifies".into(),
            to: "REQ-9".into(),
            ..Default::default()
        }],
        ..Default::default()
    }];
    x.apply_node_ops(&ctx, &on_change(&c.id), ops, "specify", "exec-2")
        .await
        .unwrap();
    let ch = g.change(&ctx, &c.id).await.unwrap();
    assert_eq!(
        ch.nodes.len(),
        2,
        "the writes fulfil the planned impacts: {:?}",
        ch.nodes
    );
    for cn in &ch.nodes {
        assert!(
            cn.post.as_ref().is_some_and(|p| p.is_draft()),
            "written, then a draft: {cn:?}"
        );
    }
    let i = ch.nodes.iter().position(|cn| cn.key == "TST-9").unwrap();
    let n = g
        .change_node_view(&ctx, &c.id, "", ch.nodes[i].post.as_ref().unwrap())
        .await
        .unwrap();
    assert!(
        n.node.properties.get("title") == Some(&Value::from("nine")) && n.out.len() == 1,
        "the write and its link: {n:?}"
    );
}
