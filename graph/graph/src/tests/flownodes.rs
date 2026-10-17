//! Port of `flownodes_test.go`: the change impacts as each flow sees them (ADR 0025, 0029 §5): a relaunched step's
//! flow starts without the stale impacts, writes its own drafts, is adopted (its impacts become the change's) or
//! discarded; parallel flows do not see each other, a nested flow sees its parent's writes.

use std::collections::HashMap;

use goap_domain::*;
use goap_graphapi::OpenFlowRequest;
use serde_json::{Map, Value, json};

use crate::api::flow_branch_name;
use crate::tests::draft::title;
use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

/// flowWorld is a change with its own branch where two steps ran (e1 wrote REQ-1, e2 created TST-2), then a flow
/// relaunches from the first step.
pub(crate) struct FlowWorld {
    pub(crate) f: Fixture,
    pub(crate) change: Change,
    /// REQ-1 declared and written by e1
    pub(crate) req: ChangeImpact,
    /// TST-2 declared and written by e2
    pub(crate) tst: ChangeImpact,
    pub(crate) flow: String,
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

fn exec_edit(flow: &str, execution: &str, t: &str) -> Edit {
    Edit {
        flow: flow.into(),
        execution: execution.into(),
        properties: title(t),
        ..Default::default()
    }
}

pub(crate) async fn new_flow_world(r: &TestRepo) -> FlowWorld {
    let ctx = ctx();
    let f = new_fixture(r).await;
    let g = &f.g;
    let c = must(
        g.create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "PSP v2".into(),
                baseline_id: f.base.id.clone(),
                own_branch: true,
                ..Default::default()
            },
        )
        .await,
    );
    let added = must(
        g.propose_impact(
            &ctx,
            &c.id,
            vec![ChangeImpact {
                intent: INTENT_MODIFIED,
                pre: Some(f.req.r#ref()),
                rationale: "PSP v2 changes the API".into(),
                execution: "e1".into(),
                ..Default::default()
            }],
        )
        .await,
    );
    let req = must(edit(g, &c.id, &added[0].id, exec_edit("", "e1", "A")).await);
    let req = must(accept_on(g, &c.id, "", "e1", &req.id, "bot", "first look").await);
    let added = must(
        propose_or_create(
            g,
            &c.id,
            vec![ChangeImpact {
                intent: INTENT_CREATED,
                key: "TST-2".into(),
                r#type: "TestCase".into(),
                rationale: "cover it".into(),
                execution: "e2".into(),
                ..Default::default()
            }],
        )
        .await,
    );
    let tst = must(edit(g, &c.id, &added[0].id, exec_edit("", "e2", "test")).await);
    let fl = must(
        g.open_flow(
            &ctx,
            &c.id,
            OpenFlowRequest {
                origin: obj(json!({"step": 0, "execution": "e1", "reason": "redo"})),
                stale_runs: vec!["e1".into(), "e2".into()],
                ..Default::default()
            },
        )
        .await,
    );
    FlowWorld {
        f,
        change: c,
        req,
        tst,
        flow: fl.id,
    }
}

pub(crate) async fn view_of(g: &Graph, id: &ChangeID, flow: &str) -> HashMap<String, ChangeImpact> {
    let bb = must(g.blackboard_in(&ctx(), id, flow).await);
    bb.change
        .nodes
        .into_iter()
        .map(|cn| (cn.key.clone(), cn))
        .collect()
}

impl FlowWorld {
    /// second declares REQ-1 again on the flow (run e3) and writes its title B.
    async fn second(&self, rationale: &str) -> ChangeImpact {
        let g = &self.f.g;
        let added = must(
            g.propose_impact(
                &ctx(),
                &self.change.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(self.f.req.r#ref()),
                    rationale: rationale.into(),
                    flow: self.flow.clone(),
                    execution: "e3".into(),
                    ..Default::default()
                }],
            )
            .await,
        );
        must(
            edit(
                g,
                &self.change.id,
                &added[0].id,
                exec_edit(&self.flow, "e3", "B"),
            )
            .await,
        );
        added[0].clone()
    }
}

#[tokio::test]
async fn flow_change_impacts_view() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_flow_world(r).await;
        let (g, c) = (&w.f.g, &w.change);
        let pre = w.f.req.r#ref();

        // the main flow keeps what it has; the flow sees neither stale change impact
        let main = view_of(g, &c.id, "").await;
        assert!(
            main.len() == 2
                && main["REQ-1"].post.is_some()
                && main["REQ-1"].review == REVIEW_ACCEPTED,
            "main view: {main:?}"
        );
        let v = view_of(g, &c.id, &w.flow).await;
        assert!(
            v.is_empty(),
            "a flow relaunched from step 1 starts from nothing: {v:?}"
        );

        // the flow declares REQ-1 again, writes and reviews it on its own branch
        let added = w.second("PSP v2, second look").await;
        assert_kind(
            g.propose_impact(
                &ctx,
                &c.id,
                vec![ChangeImpact {
                    intent: INTENT_MODIFIED,
                    pre: Some(pre.clone()),
                    rationale: "twice".into(),
                    flow: w.flow.clone(),
                    execution: "e3".into(),
                    ..Default::default()
                }],
            )
            .await,
            ErrorKind::Conflict,
            "a node appears once per flow",
        );
        must(
            accept_on(
                g,
                &c.id,
                &w.flow,
                "e3",
                &added.id,
                "bot",
                "second look is right",
            )
            .await,
        );
        let fv = view_of(g, &c.id, &w.flow).await;
        assert!(
            fv.len() == 1
                && fv["REQ-1"].rationale == "PSP v2, second look"
                && fv["REQ-1"].post.is_some()
                && fv["REQ-1"].review == REVIEW_ACCEPTED,
            "flow view: {fv:?}"
        );
        // the main flow does not see the candidate, and its own post is untouched
        let m = view_of(g, &c.id, "").await;
        assert!(
            m.len() == 2
                && m["REQ-1"].rationale == "PSP v2 changes the API"
                && m["REQ-1"].post.as_ref().is_some_and(|p| p.is_draft()),
            "main view after the flow wrote: {m:?}"
        );
        let head = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: w.f.req.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert_eq!(
            head.properties.get("title"),
            Some(&Value::from("Use PSP v1")),
            "nothing reached main"
        );
        // the flow's draft is its own, checked out from the released version (the main flow's one is stale)
        let post = must(
            g.change_node(
                &ctx,
                &c.id,
                &w.flow,
                fv["REQ-1"].post.as_ref().expect("post"),
            )
            .await,
        );
        assert!(
            post.branch == flow_branch_name(&w.flow)
                && post.is_draft()
                && post.properties.get("title") == Some(&Value::from("B"))
                && post.execution == "e3",
            "flow draft: {post:?}"
        );
        let d = must(draft_of(g, &c.id, &w.flow, &added.id).await);
        assert!(
            d.as_ref().and_then(|d| d.base.as_ref()) == Some(&pre),
            "checked out from the released version: {d:?}"
        );
        // a change with an open flow is not applied
        assert_kind(
            apply(g, &c.id, "").await,
            ErrorKind::Conflict,
            "Apply must refuse an open flow",
        );
    })
    .await;
}

#[tokio::test]
async fn flow_change_impacts_adopt() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_flow_world(r).await;
        let (g, c) = (&w.f.g, &w.change);
        let added = w.second("second look").await;
        must(
            accept_on(
                g,
                &c.id,
                &w.flow,
                "e3",
                &added.id,
                "bot",
                "second look is right",
            )
            .await,
        );
        must(g.adopt_flow(&ctx, &c.id, &w.flow, "alice").await);

        let all = must(g.list_change_impacts(&ctx, &c.id).await);
        let by_id: HashMap<ChangeImpactID, ChangeImpact> =
            all.iter().map(|cn| (cn.id.clone(), cn.clone())).collect();
        assert!(
            by_id[&w.req.id].superseded && by_id[&w.tst.id].superseded,
            "the stale change impacts are superseded: {all:?}"
        );
        let adopted = &by_id[&added.id];
        assert!(
            adopted.flow.is_empty()
                && !adopted.superseded
                && adopted.review == REVIEW_ACCEPTED
                && adopted.post.is_some(),
            "the flow's change impact becomes the change's: {adopted:?}"
        );
        // the main flow's draft is what the flow saw
        let head = must(
            g.change_node(&ctx, &c.id, "", adopted.post.as_ref().expect("post"))
                .await,
        );
        assert!(
            head.is_draft()
                && head.properties.get("title") == Some(&Value::from("B"))
                && head.execution == "e3",
            "the main flow must see what the flow saw: {head:?}"
        );
        let d = must(draft_of(g, &c.id, "", &added.id).await);
        assert!(
            d.as_ref().is_some_and(
                |d| d.flow.is_empty() && d.properties.get("title") == Some(&Value::from("B"))
            ),
            "the flow's draft is the change's: {d:?}"
        );
        // TST-2 was created by steps that no longer exist: its draft goes with its impact
        let d = must(draft_of(g, &c.id, "", &w.tst.id).await);
        assert!(d.is_none(), "TST-2 must go: {d:?}");
        // the flow does not show any more, the main flow sees the adopted change impact only
        let m = view_of(g, &c.id, "").await;
        assert!(
            m.len() == 1 && m["REQ-1"].id == added.id,
            "main view: {m:?}"
        );
        must(apply(g, &c.id, "").await);
        let fin = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: w.f.req.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert!(
            fin.properties.get("title") == Some(&Value::from("B"))
                && fin.change_impact == added.id
                && fin.comment == "second look is right",
            "landed: {fin:?}"
        );
        if let Ok(n) = g.node_by_key(&ctx, DEFAULT_NAMESPACE, "TST-2").await {
            assert!(n.deleted, "TST-2 must not be live on main: {n:?}");
        }
    })
    .await;
}

#[tokio::test]
async fn flow_change_impacts_discard() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_flow_world(r).await;
        let (g, c) = (&w.f.g, &w.change);
        let added = w.second("second look").await;
        must(g.discard_flow(&ctx, &c.id, &w.flow, "alice").await);
        for cn in must(g.list_change_impacts(&ctx, &c.id).await) {
            if cn.id == added.id {
                assert_eq!(
                    cn.review, REVIEW_REJECTED,
                    "the flow's change impact is rejected"
                );
            } else if cn.id == w.req.id || cn.id == w.tst.id {
                assert!(
                    !cn.superseded,
                    "the stale change impacts count again: {cn:?}"
                );
            }
        }
        let d = must(draft_of(g, &c.id, "", &added.id).await);
        assert!(d.is_none(), "nothing of the discarded flow is seen: {d:?}");
        // the change goes on as before: REQ-1 at the version step 1 wrote
        must(accept_on(g, &c.id, "", "e4", &w.tst.id, "bot", "needed").await);
        must(apply(g, &c.id, "").await);
        let fin = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: w.f.req.id.clone(),
                    version: 0,
                },
            )
            .await,
        );
        assert_eq!(
            fin.properties.get("title"),
            Some(&Value::from("A")),
            "the main flow's write must land"
        );
    })
    .await;
}

#[tokio::test]
async fn flow_change_impacts_conflict() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_flow_world(r).await;
        let (g, c) = (&w.f.g, &w.change);
        w.second("second look").await;
        // another run of the main flow (not stale) writes the same node meanwhile
        must(
            edit(
                g,
                &c.id,
                &w.req.id,
                Edit {
                    execution: "e9".into(),
                    properties: props(&[("owner", Value::from("carol"))]),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            g.adopt_flow(&ctx, &c.id, &w.flow, "alice").await,
            ErrorKind::Conflict,
            "adopting a flow whose node moved on the change branch must conflict",
        );
    })
    .await;
}

// Each flow sees the change impacts through its own chain of events (ADR 0029 §5): parallel flows do not see each
// other, a flow inside a flow sees its parent's writes except the ones of the step it relaunches.
#[tokio::test]
async fn parallel_and_nested_flows_on_change_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        // REQ-1 written by e1 ("A"), TST-2 by e2; w.flow relaunches from e1
        let w = new_flow_world(r).await;
        let (g, c) = (&w.f.g, &w.change);
        let open = async |parent: &str, stale: &[&str]| -> String {
            must(
                g.open_flow(
                    &ctx,
                    &c.id,
                    OpenFlowRequest {
                        parent: parent.into(),
                        origin: obj(json!({"step": 1, "reason": "again"})),
                        stale_runs: stale.iter().map(|s| s.to_string()).collect(),
                        ..Default::default()
                    },
                )
                .await,
            )
            .id
        };
        let title_of = async |flow: &str| -> Option<Value> {
            let v = view_of(g, &c.id, flow).await;
            let post = v.get("REQ-1")?.post.clone()?;
            let n = must(g.change_node(&ctx, &c.id, flow, &post).await);
            n.properties.get("title").cloned()
        };
        let a = Some(Value::from("A"));
        // two parallel flows relaunch the second step: both keep REQ-1 as the main flow wrote it
        let f1 = open("", &["e2"]).await;
        let f2 = open("", &["e2"]).await;
        assert!(
            title_of(&f1).await == a && title_of(&f2).await == a,
            "parallel flows start from the main flow"
        );
        // f1 rewrites REQ-1 (a change impact of the main flow): only f1 sees it
        must(edit(g, &c.id, &w.req.id, exec_edit(&f1, "e4", "F1")).await);
        let f1t = Some(Value::from("F1"));
        assert!(
            title_of(&f1).await == f1t && title_of(&f2).await == a && title_of("").await == a,
            "after f1 wrote: f1 {:?}, f2 {:?}, main {:?}",
            title_of(&f1).await,
            title_of(&f2).await,
            title_of("").await
        );
        // a flow inside f1 sees f1's write; one that relaunches f1's writing step does not
        let inner = open(&f1, &[]).await;
        assert_eq!(
            title_of(&inner).await,
            f1t,
            "a flow inside f1 sees f1's writes"
        );
        let redo = open(&f1, &["e4"]).await;
        assert_eq!(
            title_of(&redo).await,
            a,
            "a flow relaunching f1's step does not see what it wrote"
        );
        // it writes its own version, derived from the one it sees
        let cn = must(edit(g, &c.id, &w.req.id, exec_edit(&redo, "e5", "R")).await);
        let n = must(
            g.change_node(&ctx, &c.id, &redo, cn.post.as_ref().expect("post"))
                .await,
        );
        assert!(
            n.branch == flow_branch_name(&redo) && n.is_draft(),
            "draft of the nested flow: {n:?}"
        );
        assert!(
            title_of(&redo).await == Some(Value::from("R"))
                && title_of(&f1).await == f1t
                && title_of("").await == a,
            "after the nested flow wrote"
        );
    })
    .await;
}
