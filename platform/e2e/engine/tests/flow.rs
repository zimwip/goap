//! Go: flow_test.go.

use std::collections::BTreeSet;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::{self as domain, Change, ChangeImpactID, ChangeItem, ItemID};
use goap_engine::{
    Engine, Process, STATUS_COMPLETED, STATUS_FAILED, STATUS_RUNNING, STATUS_STUCK,
    STATUS_SUPERSEDED, STATUS_WAITING, StartRequest, TASK_BOARD, TASK_FLOW, TASK_RELAUNCHED,
};
use goap_graph::Graph;
use goap_journal as journal;
use goap_llm as llm;
use parking_lot::Mutex;
use serde_json::Value;

use crate::common::*;

const PSP: &str = "The PSP changes its API, what does this break?";

struct Done {
    e: Arc<Engine>,
    g: Arc<Graph>,
    old: Process,
    c: Change,
    _errs: Errs,
}

/// completed_run runs impact-analysis to its end on the engine of s.
async fn completed_run_of(s: Setup) -> Done {
    let ctx = ctx();
    let (e, g, base, errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: base,
                intent: PSP.into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "run: {p:?}");
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    Done {
        e,
        g,
        old: p,
        c,
        _errs: errs,
    }
}

async fn completed_run() -> Done {
    completed_run_of(setup().await).await
}

fn executions(p: &Process, from: usize) -> BTreeSet<String> {
    p.steps[from..]
        .iter()
        .map(|s| s.execution.clone())
        .collect()
}

/// items_of_steps are the items produced by the steps of p from `from` on.
fn items_of_steps(c: &Change, p: &Process, from: usize) -> BTreeSet<ItemID> {
    let execs = executions(p, from);
    c.items
        .iter()
        .filter(|it| execs.contains(&it.execution))
        .map(|it| it.id.clone())
        .collect()
}

/// nodes_of_steps are the change impacts declared by the steps of p from `from` on.
fn nodes_of_steps(c: &Change, p: &Process, from: usize) -> BTreeSet<ChangeImpactID> {
    let execs = executions(p, from);
    c.nodes
        .iter()
        .filter(|n| execs.contains(&n.execution))
        .map(|n| n.id.clone())
        .collect()
}

#[tokio::test]
async fn relaunch_step_adopt_flow() {
    let ctx = ctx();
    let Done { e, g, old, c, .. } = completed_run().await;
    assert!(old.steps.len() >= 3, "steps = {}", old.steps.len());
    let replaced = items_of_steps(&c, &old, 1); // the propagation and report artifacts
    let replaced_nodes = nodes_of_steps(&c, &old, 1);
    let kept_nodes: BTreeSet<_> = nodes_of_steps(&c, &old, 0)
        .difference(&replaced_nodes)
        .cloned()
        .collect();
    assert!(
        !replaced.is_empty() && replaced_nodes.len() == 2 && kept_nodes.len() == 1,
        "replaced {} items, {} change impacts, kept {} change impacts",
        replaced.len(),
        replaced_nodes.len(),
        kept_nodes.len()
    );

    let np = e
        .relaunch(&ctx, &old.id, 1, "the PSP answer changed", "")
        .await
        .unwrap();
    assert!(
        !np.flow.is_empty()
            && np.relaunch_of == old.id
            && np.from_step == 1
            && np.status == STATUS_RUNNING,
        "relaunched process = {np:?}"
    );
    let c = g.change(&ctx, &old.change_id).await.unwrap();
    for id in &replaced {
        assert_eq!(c.effective_status(id), domain::ITEM_STALE, "item {id}");
    }
    // the flow sees the change impact of the earlier step, not the ones the relaunched steps declared
    let bb = g
        .blackboard_in(&ctx, &old.change_id, &np.flow)
        .await
        .unwrap();
    assert!(
        bb.change.nodes.len() == 1 && kept_nodes.contains(&bb.change.nodes[0].id),
        "flow view before the run: {:?}",
        bb.change.nodes
    );
    // the relaunched run replans from the state before step 1 and waits for a human once it reaches the goal
    let np = e.run(&ctx, &np.id).await.unwrap();
    assert!(
        np.status == STATUS_WAITING && np.pending.as_ref().is_some_and(|t| t.kind == TASK_FLOW),
        "after the run: {} {:?}",
        np.status,
        np.pending
    );
    // the journal of the relaunched run is on the flow, and starts with why it was scheduled
    let recs = journal::read(
        &ctx,
        &*g,
        &journal::Filter {
            change_id: old.change_id.clone(),
            process_ids: vec![np.id.clone()],
        },
    )
    .await
    .unwrap();
    assert!(
        recs.len() >= 2,
        "journal of the relaunched run: {} records",
        recs.len()
    );
    let sch = &recs[1];
    assert!(
        sch.kind == journal::KIND_SCHEDULE
            && sch.data.get("reason") == Some(&Value::from("relaunched"))
            && sch.data.get("flow") == Some(&Value::from(np.flow.clone()))
            && sch.data.get("process") == Some(&Value::from(old.id.clone())),
        "schedule record of the relaunched run: {sch:?}"
    );
    for r in &recs {
        assert_eq!(r.flow, np.flow, "record {} of the relaunched run", r.kind);
    }
    let c = g.change(&ctx, &old.change_id).await.unwrap();
    let mut candidates = 0;
    for it in c
        .items
        .iter()
        .filter(|it| it.flow == np.flow && it.r#type != domain::TYPE_FLOW)
    {
        candidates += 1;
        assert_eq!(
            c.effective_status(&it.id),
            domain::ITEM_CANDIDATE,
            "candidate {}",
            it.id
        );
    }
    assert_eq!(candidates, replaced.len(), "candidates");
    let flow_nodes = c.nodes.iter().filter(|n| n.flow == np.flow).count();
    assert_eq!(
        flow_nodes,
        replaced_nodes.len(),
        "the flow declares again the change impacts it replaces"
    );
    // the previous run is untouched until the human decides
    assert_eq!(get(&e, &old.id).await.status, STATUS_COMPLETED, "old run");
    // a second relaunch in parallel is allowed
    e.relaunch(&ctx, &old.id, 2, "again", "")
        .await
        .expect("parallel relaunch");

    let np = e
        .decide_flow(&ctx, &np.id, true, "looks right")
        .await
        .unwrap();
    assert_eq!(np.status, STATUS_COMPLETED, "adopted run");
    assert_eq!(get(&e, &old.id).await.status, STATUS_SUPERSEDED, "old run");
    let c = g.change(&ctx, &old.change_id).await.unwrap();
    for id in &replaced {
        assert_eq!(c.effective_status(id), domain::ITEM_SUPERSEDED, "item {id}");
    }
    let mut live = 0;
    for n in &c.nodes {
        assert!(
            !(replaced_nodes.contains(&n.id) && !n.superseded),
            "the stale change impact {} must be superseded",
            n.key
        );
        assert!(
            n.flow.is_empty(),
            "an adopted flow leaves no candidate: {n:?}"
        );
        if !n.superseded {
            live += 1;
        }
    }
    assert_eq!(
        live, 3,
        "REQ-1, TST-1 and CMP-1 are the change's change impacts"
    );
    for it in c
        .items
        .iter()
        .filter(|it| it.flow == np.flow && it.r#type != domain::TYPE_FLOW)
    {
        let st = c.effective_status(&it.id);
        assert!(
            st != domain::ITEM_CANDIDATE
                && st != domain::ITEM_SUPERSEDED
                && st != domain::ITEM_REJECTED,
            "adopted item {} = {st}",
            it.id
        );
    }
    // the journal records the decision
    let recs = journal::read(
        &ctx,
        &*g,
        &journal::Filter {
            change_id: old.change_id.clone(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert!(
        recs.iter().any(|r| r.kind == journal::KIND_APPROVAL
            && r.action == "flow"
            && r.data.get("decision") == Some(&Value::from("adopted"))),
        "flow decision not journaled"
    );
}

/// A relaunched run that stopped short of the goal (failed, or stuck with no plan) can still be adopted: what it
/// wrote so far is banked onto the change branch, so a later relaunch can continue from there.
#[tokio::test]
async fn relaunch_step_adopt_incomplete_flow() {
    for status in [STATUS_FAILED, STATUS_STUCK] {
        let ctx = ctx();
        let Done { e, g, old, .. } = completed_run().await;
        let mut np = e.relaunch(&ctx, &old.id, 1, "try again", "").await.unwrap();
        // running: not ready to decide yet
        let err = e
            .decide_flow(&ctx, &np.id, true, "")
            .await
            .expect_err("adopting a running flow");
        assert!(
            err.is_invalid_state(),
            "{status}: adopting a running flow: {err}"
        );
        np.status = status.into();
        np.error = "simulated".into();
        e.store.put(&ctx, &np).await.unwrap();
        let np = e
            .decide_flow(&ctx, &np.id, true, "bank the partial progress")
            .await
            .unwrap();
        assert_eq!(np.status, status, "adopted incomplete run, unchanged");
        assert_eq!(
            get(&e, &old.id).await.status,
            STATUS_SUPERSEDED,
            "{status}: old run"
        );
        let fs = g.flows(&ctx, &old.change_id).await.unwrap();
        let f = fs
            .iter()
            .find(|f| f.id == np.flow)
            .unwrap_or_else(|| panic!("{status}: flow {} not found", np.flow));
        assert_eq!(f.status, domain::FLOW_ADOPTED, "{status}: flow {}", f.id);
    }
}

#[tokio::test]
async fn relaunch_step_discard_flow() {
    let ctx = ctx();
    let Done { e, g, old, c, .. } = completed_run().await;
    let replaced = items_of_steps(&c, &old, 1);
    let replaced_nodes = nodes_of_steps(&c, &old, 1);
    let np = e.relaunch(&ctx, &old.id, 1, "try again", "").await.unwrap();
    let err = e
        .decide_flow(&ctx, &np.id, true, "")
        .await
        .expect_err("adopting before the goal is reached");
    assert!(
        err.is_invalid_state(),
        "adopting before the goal is reached: {err}"
    );
    let np = e.run(&ctx, &np.id).await.unwrap();
    assert_eq!(np.status, STATUS_WAITING, "run: {np:?}");
    let np = e
        .decide_flow(&ctx, &np.id, false, "not better")
        .await
        .unwrap();
    assert_eq!(np.status, STATUS_SUPERSEDED, "discard: {np:?}");
    assert_eq!(
        get(&e, &old.id).await.status,
        STATUS_COMPLETED,
        "old run must stay completed"
    );
    let c = g.change(&ctx, &old.change_id).await.unwrap();
    for id in &replaced {
        let st = c.effective_status(id);
        assert!(
            st != domain::ITEM_STALE
                && st != domain::ITEM_SUPERSEDED
                && st != domain::ITEM_REJECTED,
            "item {id} = {st}: the previous outputs count again"
        );
    }
    for it in c
        .items
        .iter()
        .filter(|it| it.flow == np.flow && it.r#type != domain::TYPE_FLOW)
    {
        assert_eq!(
            c.effective_status(&it.id),
            domain::ITEM_REJECTED,
            "discarded candidate {}",
            it.id
        );
    }
    for n in &c.nodes {
        assert!(
            !(replaced_nodes.contains(&n.id) && n.superseded),
            "the change impact {} counts again after a discard",
            n.key
        );
        assert!(
            !(n.flow == np.flow && n.review != domain::REVIEW_REJECTED),
            "a discarded candidate is rejected: {n:?}"
        );
    }
    // a new relaunch is possible once the flow is decided
    e.relaunch(&ctx, &old.id, 2, "later", "")
        .await
        .expect("relaunch after a decision");
}

#[tokio::test]
async fn relaunch_rules() {
    let ctx = ctx();
    let Done { e, old, .. } = completed_run().await;
    let err = e
        .relaunch(&ctx, &old.id, 99, "", "")
        .await
        .expect_err("unknown step");
    assert!(err.is_invalid_state(), "unknown step: {err}");
}

struct Corrupted {
    e: Arc<Engine>,
    a: Process,
    b: Process,
    scheduled: Arc<Mutex<Vec<String>>>,
    _errs: Errs,
}

/// corrupted_board completes a run, then adds an item that step 1 "produced" but that refers to an item that does
/// not exist, and starts a second process on the same change.
async fn corrupted_board() -> Corrupted {
    let ctx = ctx();
    let mut s = setup().await;
    let scheduled: Arc<Mutex<Vec<String>>> = Arc::default();
    {
        let scheduled = scheduled.clone();
        s.e.schedule = Some(Arc::new(move |id: String| scheduled.lock().push(id)));
    }
    let Done {
        e,
        g,
        old: a,
        _errs,
        ..
    } = completed_run_of(s).await;
    scheduled.lock().clear();
    let bad = ChangeItem {
        r#type: domain::TYPE_ARTIFACT,
        target: "note".into(),
        execution: a.steps[1].execution.clone(),
        derived_from: vec!["ghost".into()],
        produced_by: "test".into(),
        ..Default::default()
    };
    g.add_items(&ctx, &a.change_id, vec![bad]).await.unwrap();
    let b = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                change_id: a.change_id.clone(),
                goal: a.goal.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let b = e.run(&ctx, &b.id).await.unwrap();
    Corrupted {
        e,
        a,
        b,
        scheduled,
        _errs,
    }
}

#[tokio::test]
async fn board_validation_proposes_relaunch() {
    let ctx = ctx();
    let Corrupted {
        e, a, b, scheduled, ..
    } = corrupted_board().await;
    let pending = b.pending.clone().unwrap_or_default();
    assert!(
        b.status == STATUS_WAITING && pending.kind == TASK_BOARD && !pending.issues.is_empty(),
        "second process = {} {:?}",
        b.status,
        b.pending
    );
    let prop = pending.proposal.clone();
    assert!(
        prop.as_ref()
            .is_some_and(|p| p.process == a.id && p.step == 1 && p.action == a.steps[1].action),
        "proposal = {prop:?}, want step 1 of {}",
        a.id
    );
    assert_eq!(
        pending.issues[0].code, "dangling",
        "issues = {:?}",
        pending.issues
    );

    // accept: the step is relaunched, the second process waits for the flow decision
    let (b, np) = e.resolve_board(&ctx, &b.id, true, "").await.unwrap();
    let np = np.expect("relaunch");
    assert!(
        np.relaunch_of == a.id
            && np.from_step == 1
            && b.pending
                .as_ref()
                .is_some_and(|t| t.kind == TASK_RELAUNCHED && t.flow_id == np.flow),
        "relaunch = {np:?} / {:?}",
        b.pending
    );
    // the relaunched flow no longer contains the faulty item: it reaches the goal
    let np = e.run(&ctx, &np.id).await.unwrap();
    assert!(
        np.status == STATUS_WAITING && np.pending.as_ref().is_some_and(|t| t.kind == TASK_FLOW),
        "relaunched run: {} {:?}",
        np.status,
        np.pending
    );
    e.decide_flow(&ctx, &np.id, true, "").await.unwrap();
    let cur = get(&e, &b.id).await;
    assert!(
        cur.status == STATUS_RUNNING && cur.pending.is_none(),
        "the waiting process must resume: {} {:?}",
        cur.status,
        cur.pending
    );
    assert_eq!(*scheduled.lock(), std::slice::from_ref(&b.id), "scheduled");
    // and the board is consistent again for it
    let b = e.run(&ctx, &b.id).await.unwrap();
    assert_eq!(b.status, STATUS_COMPLETED, "resumed run: {:?}", b.pending);
}

#[tokio::test]
async fn board_validation_ignore() {
    let ctx = ctx();
    let Corrupted { e, b, .. } = corrupted_board().await;
    assert!(
        b.pending.as_ref().is_some_and(|t| t.kind == TASK_BOARD),
        "second process = {} {:?}",
        b.status,
        b.pending
    );
    let (b, np) = e
        .resolve_board(&ctx, &b.id, false, "known problem")
        .await
        .unwrap();
    assert!(np.is_none() && b.status == STATUS_RUNNING, "ignore: {b:?}");
    // the same issues are not asked twice
    let b = e.run(&ctx, &b.id).await.unwrap();
    assert_eq!(
        b.status, STATUS_COMPLETED,
        "run after ignoring: {:?}",
        b.pending
    );
}

#[tokio::test]
async fn board_validation_discarded_flow_resumes() {
    let ctx = ctx();
    let Corrupted { e, b, .. } = corrupted_board().await;
    let (b, np) = e.resolve_board(&ctx, &b.id, true, "").await.unwrap();
    let np = e.run(&ctx, &np.unwrap().id).await.unwrap();
    e.decide_flow(&ctx, &np.id, false, "no").await.unwrap();
    let cur = get(&e, &b.id).await;
    assert!(
        cur.status == STATUS_RUNNING && cur.dismissed.len() == 1,
        "after a discarded flow the process goes on with the issues ignored: {} {:?}",
        cur.status,
        cur.dismissed
    );
}

/// A methodology targets a namespace: the change it opens acts on it.
#[tokio::test]
async fn change_opens_in_the_namespace_of_the_methodology() {
    let ctx = ctx();
    let mut s = setup().await;
    // Go set the namespace on the compiled definition in place; here the compiled form takes a definition so edited
    let c = load_methodology("impact-analysis.yaml");
    let mut def = c.definition().clone();
    def.namespace = "platform".into();
    s.add(Arc::new(c.with_definition(def)));
    let (e, g, base, _errs) = s.share();
    // a change's reference baseline must be of its own (here the methodology's target) namespace
    let platform_base = g
        .branch_head(&ctx, "platform", domain::MAIN_BRANCH)
        .await
        .unwrap();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: platform_base.id,
                goal: "assess_impact".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert_eq!(c.namespace, domain::NAMESPACE_PLATFORM, "change namespace");
    // an explicit namespace on the request wins over the methodology default
    let p2 = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: base,
                goal: "assess_impact".into(),
                namespace: "alm".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let c2 = g.change(&ctx, &p2.change_id).await.unwrap();
    assert_eq!(c2.namespace, "alm", "explicit namespace");
}

#[tokio::test]
async fn relaunch_guidance_reaches_the_agent() {
    let ctx = ctx();
    let mut s = setup().await;
    let prompts: Arc<Mutex<Vec<String>>> = Arc::default();
    {
        let (inner, prompts) = (scripted(&s.errs), prompts.clone());
        s.llm(llm::ClientFunc::shared(
            move |ctx: Ctx, req: llm::Request| {
                let (inner, prompts) = (inner.clone(), prompts.clone());
                async move {
                    prompts.lock().push(req.messages[0].content.clone());
                    inner.complete(&ctx, req).await
                }
            },
        ));
    }
    let Done { e, old, .. } = completed_run_of(s).await;
    prompts.lock().clear();
    let np = e
        .relaunch(
            &ctx,
            &old.id,
            1,
            "the PSP answer changed",
            "the PSP now requires API v3",
        )
        .await
        .unwrap();
    let np = e.run(&ctx, &np.id).await.unwrap();
    assert!(
        np.status == STATUS_WAITING && np.pending.as_ref().is_some_and(|t| t.kind == TASK_FLOW),
        "run: {} {:?}",
        np.status,
        np.pending
    );
    {
        let prompts = prompts.lock();
        assert!(!prompts.is_empty(), "no LLM call in the relaunched flow");
        for (i, p) in prompts.iter().enumerate() {
            assert!(
                p.contains("the PSP now requires API v3"),
                "prompt {i} of the relaunched flow lacks the guidance:\n{p}"
            );
        }
    }
    // the main flow never sees the guidance
    prompts.lock().clear();
    e.start(
        &ctx,
        StartRequest {
            methodology: "impact-analysis".into(),
            change_id: old.change_id.clone(),
            goal: old.goal.clone(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    for p in prompts.lock().iter() {
        assert!(
            !p.contains("API v3"),
            "guidance leaked in a prompt of the main flow"
        );
    }
}

#[tokio::test]
async fn relaunched_process_titles_do_not_pile_up() {
    let ctx = ctx();
    let Done { e, old, .. } = completed_run().await;
    let np = e.relaunch(&ctx, &old.id, 1, "", "").await.unwrap();
    let want = format!("{} (relaunch from step 2)", old.title);
    assert_eq!(np.title, want, "steps are numbered from 1");
}
