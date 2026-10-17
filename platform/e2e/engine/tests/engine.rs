//! Go: engine_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{self as domain, split_methodology_ref};
use goap_engine::{
    Engine, MethodologyPort, Process, STATUS_CLARIFYING, STATUS_COMPLETED, STATUS_RUNNING,
    STATUS_STUCK, STATUS_WAITING, StartRequest, TASK_APPROVAL, TASK_INPUT, unknown_methodology,
};
use goap_graph::{Graph, NewChange};
use goap_methodology::Compiled;

use crate::common::*;

#[tokio::test]
async fn assess_impact() {
    let ctx = ctx();
    let s = setup().await;
    let (e, g) = (Arc::new(s.e), s.g);
    // the intent text deliberately echoes the "assess_impact" goal example ("what does this break") in
    // methodologies/examples/impact-analysis.yaml.
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: s.base.clone(),
                project_id: TEST_PROJECT.into(),
                intent: "The PSP changes its API, what does this break?".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        p.status == STATUS_RUNNING && p.goal == "assess_impact",
        "intent not resolved: {p:?}"
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    assert_eq!(
        actions_of(&p),
        "identify_impacts,propagate_impacts,write_report"
    );
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    // REQ-1 direct, TST-1 and CMP-1 propagated (NEED-1 is upstream, not impacted)
    assert_eq!(
        c.nodes.len(),
        3,
        "expected 3 impacted nodes, got {:?}",
        c.nodes
    );
    for n in &c.nodes {
        assert!(
            n.intent == domain::INTENT_MODIFIED && !n.rationale.is_empty() && n.planned(),
            "an impact is a planned change impact with a rationale: {n:?}"
        );
    }
    // the run's goal lives in the process; the change keeps the main goal of its methodology (ADR 0096)
    assert_eq!(
        c.goal, "deliver_change",
        "change goal, want the main goal of the methodology"
    );
}

#[tokio::test]
async fn prepare_change_with_clarification_and_review() {
    let ctx = ctx();
    let s = setup().await;
    let (e, g) = (Arc::new(s.e), s.g);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: s.base.clone(),
                project_id: TEST_PROJECT.into(),
                intent: "The payment provider is changing".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        p.status == STATUS_CLARIFYING && !p.question.is_empty(),
        "expected a clarification, got {p:?}"
    );
    let p = e.answer(&ctx, &p.id, "prepare_change").await.unwrap();
    assert_eq!(p.goal, "prepare_change");
    let entries = e.store.list_process_log(&ctx, &p.id).await.unwrap();
    assert_eq!(
        entries.len(),
        p.intent.turns.len(),
        "one intent.turn log entry per turn: {entries:?}"
    );
    for (i, en) in entries.iter().enumerate() {
        assert!(
            en.r#type == "intent.turn" && en.seq == i as i64 + 1,
            "entry {i}: {en:?}"
        );
    }
    assert_eq!(
        entries.last().unwrap().payload.get("text"),
        Some(&serde_json::Value::from("prepare_change")),
        "last entry should be the user's answer"
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.action == "review_proposals"),
        "expected review task, got {p:?}"
    );
    e.submit(&ctx, &p.id, review_all(&g, &p.change_id).await)
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    let b2 = Box::pin(g.apply(&ctx, &p.change_id, "B2")).await.unwrap();
    let (nodes, links) = g.baseline_graph(&ctx, &b2.id).await.unwrap();
    assert_eq!(nodes.len(), 5, "expected 5 nodes in target graph");
    assert!(
        links
            .iter()
            .any(|l| l.r#type == "alm@verifies" && l.to.version == 2),
        "new test must verify REQ-1@v2: {links:?}"
    );
}

#[tokio::test]
async fn stuck_when_no_plan() {
    let ctx = ctx();
    let mut s = setup().await;
    // make the LLM useless: identify_impacts never delivers, gets disabled, then the human fallback is planned.
    s.llm(text_client(r#"{"items":[]}"#));
    let e = Arc::new(s.e);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: s.base.clone(),
                project_id: TEST_PROJECT.into(),
                goal: "assess_impact".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING && p.pending.as_ref().unwrap().action == "select_impacts",
        "expected fallback to human selection, got {p:?}"
    );
    assert_eq!(
        p.disabled.get("identify_impacts"),
        Some(&true),
        "identify_impacts should be disabled"
    );
}

/// deliver_until_reviewed starts a deliver_change process as who, accepts every proposal and runs until the next
/// blocking point.
pub async fn deliver_until_reviewed(
    who: goap_authz::Principal,
) -> (Arc<Engine>, Arc<Graph>, Process, Errs) {
    let s = setup().await;
    let (e, g) = (Arc::new(s.e), s.g);
    let ctx = as_who(&who);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                baseline_id: s.base.clone(),
                project_id: TEST_PROJECT.into(),
                goal: "deliver_change".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        p.initiator.subject, who.subject,
        "initiator not recorded: {:?}",
        p.initiator
    );
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.kind == TASK_INPUT && t.action == "review_proposals"),
        "expected review, got {p:?}"
    );
    e.submit(&ctx, &p.id, review_all(&g, &p.change_id).await)
        .await
        .unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    (e, g, p, s.errs)
}

#[tokio::test]
async fn apply_needs_approval() {
    let (e, g, p, _errs) = deliver_until_reviewed(contributor()).await;
    assert!(
        p.status == STATUS_WAITING
            && p.pending
                .as_ref()
                .is_some_and(|t| t.kind == TASK_APPROVAL && t.permission == "change:apply"),
        "expected an approval task, got {p:?}"
    );
    let c = g.change(&ctx(), &p.change_id).await.unwrap();
    assert_ne!(
        c.status,
        domain::CHANGE_APPLIED,
        "change applied without approval"
    );
    // the contributor may neither submit items nor approve
    let err = e
        .submit(&as_who(&contributor()), &p.id, vec![])
        .await
        .unwrap_err();
    assert!(err.is_invalid_state(), "submit on approval task: {err}");
    let err = e
        .approve(&as_who(&contributor()), &p.id, true, "")
        .await
        .unwrap_err();
    assert!(err.is_forbidden(), "expected forbidden, got {err}");
    let ctx = as_who(&approver());
    e.approve(&ctx, &p.id, true, "ok").await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert_eq!(p.status, STATUS_COMPLETED, "{p:?}");
    let last = p.steps.last().unwrap();
    assert!(
        last.action == "apply_change" && last.approved_by == "alice" && last.effects_met,
        "unexpected apply step {last:?}"
    );
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert!(
        c.status == domain::CHANGE_APPLIED && !c.result_baseline_id.is_empty(),
        "change not applied: {c:?}"
    );
}

#[tokio::test]
async fn apply_automatic_with_permission() {
    let (_e, g, p, _errs) = deliver_until_reviewed(admin()).await;
    assert_eq!(
        p.status, STATUS_COMPLETED,
        "admin initiator should apply directly, got {p:?}"
    );
    let c = g.change(&ctx(), &p.change_id).await.unwrap();
    assert_eq!(c.status, domain::CHANGE_APPLIED, "change not applied");
}

#[tokio::test]
async fn apply_rejected() {
    let (e, g, p, _errs) = deliver_until_reviewed(contributor()).await;
    let ctx = as_who(&approver());
    e.approve(&ctx, &p.id, false, "not now").await.unwrap();
    let p = e.run(&ctx, &p.id).await.unwrap();
    assert!(
        p.status == STATUS_STUCK && p.disabled.get("apply_change") == Some(&true),
        "expected stuck with apply disabled, got {} {:?}",
        p.status,
        p.disabled
    );
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    assert_ne!(
        c.status,
        domain::CHANGE_APPLIED,
        "rejected apply must not change the graph"
    );
}

#[tokio::test]
async fn approver_cannot_approve_own_change() {
    // alice (approver) starts the process: the four-eyes rule denies the automatic apply and her own approval;
    // another approver may approve.
    let (e, _g, p, _errs) = deliver_until_reviewed(approver()).await;
    assert!(
        p.status == STATUS_WAITING && p.pending.as_ref().is_some_and(|t| t.kind == TASK_APPROVAL),
        "expected approval task, got {p:?}"
    );
    let err = e
        .approve(&as_who(&approver()), &p.id, true, "")
        .await
        .unwrap_err();
    assert!(
        err.is_forbidden(),
        "self approval must be forbidden, got {err}"
    );
    let other = principal("bob", "acme", &["approver"]);
    e.approve(&as_who(&other), &p.id, true, "").await.unwrap();
}

/// A run on a change runs the version of the methodology the change follows, not the latest one (ADR 0113).
#[tokio::test]
async fn run_follows_the_version_of_its_change() {
    let ctx = ctx();
    let mut s = setup().await;
    let cur = s.ms.0["impact-analysis"].clone();
    let c =
        s.g.create_change(
            &ctx,
            NewChange {
                title: "pinned".into(),
                methodology: "impact-analysis".into(),
                namespace: "alm".into(),
                baseline_id: s.base.clone(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        c.methodology_version,
        cur.definition().version,
        "the change follows the published version"
    );
    // a later version is published: the change, and the runs on it, stay on theirs
    let mut def = cur.definition().clone();
    def.version = format!("{}.next", cur.definition().version);
    let next = Arc::new(cur.with_definition(def));
    let mut versions = VersionedPort::default();
    versions.0.insert(
        format!("{}@{}", cur.definition().name, cur.definition().version),
        cur.clone(),
    );
    versions
        .0
        .insert(cur.definition().name.clone(), next.clone());
    s.e.methodologies = Arc::new(versions);
    let e = Arc::new(s.e);
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                change_id: c.id.clone(),
                goal: "assess_impact".into(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        p.methodology_version,
        cur.definition().version,
        "the run follows the change"
    );
    let fresh = e
        .start(
            &ctx,
            StartRequest {
                methodology: "impact-analysis".into(),
                goal: "assess_impact".into(),
                baseline_id: s.base.clone(),
                project_id: TEST_PROJECT.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        fresh.methodology_version,
        next.definition().version,
        "a run with no change takes the latest"
    );
}

/// VersionedPort serves compiled methodologies by reference: "<name>@<version>" or the name for the latest.
#[derive(Default)]
pub struct VersionedPort(pub BTreeMap<String, Arc<Compiled>>);

#[async_trait]
impl MethodologyPort for VersionedPort {
    async fn methodology(&self, _: &Ctx, r: &str) -> goap_engine::Result<Arc<Compiled>> {
        if let Some(m) = self.0.get(r) {
            return Ok(m.clone());
        }
        let (name, version) = split_methodology_ref(r);
        if let Some(m) = self.0.get(name)
            && m.definition().version == version
        {
            return Ok(m.clone());
        }
        Err(unknown_methodology(r))
    }

    async fn list(&self, _: &Ctx) -> goap_engine::Result<Vec<Arc<Compiled>>> {
        Ok(self
            .0
            .iter()
            .filter(|(k, _)| !k.contains('@'))
            .map(|(_, m)| m.clone())
            .collect())
    }
}
