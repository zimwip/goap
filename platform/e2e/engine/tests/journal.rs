//! Go: journal_test.go.

use std::collections::HashMap;

use goap_authz::Principal;
use goap_domain::LogFilter;
use goap_engine::{ItemInput, Process, STATUS_COMPLETED, STATUS_WAITING, StartRequest};
use goap_graph::Graph;
use goap_journal as journal;
use serde_json::Value;

use crate::common::*;
use crate::steps::STAGED_YAML;

const PSP: &str = "The PSP changes its API, what does this break?";

/// impact_run runs impact-analysis to its end.
async fn impact_run(s: Setup) -> (Process, std::sync::Arc<Graph>, Errs) {
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
    (p, g, errs)
}

async fn records(g: &Graph, p: &Process) -> Vec<journal::Record> {
    journal::read(
        &ctx(),
        g,
        &journal::Filter {
            change_id: p.change_id.clone(),
            ..Default::default()
        },
    )
    .await
    .unwrap()
}

#[tokio::test]
async fn journal_records_ticks_and_actions() {
    let ctx = ctx();
    // the intent text deliberately echoes the "assess_impact" goal example in methodologies/examples/impact-analysis.yaml.
    let (p, g, _errs) = impact_run(setup().await).await;
    let recs = records(&g, &p).await;
    let mut kinds = Vec::new();
    let mut by_id = HashMap::new();
    for (i, r) in recs.iter().enumerate() {
        kinds.push(r.kind.clone());
        by_id.insert(r.id.clone(), r.clone());
        assert!(
            r.seq == i as i64 + 1 && r.process_id == p.id && !r.methodology_version.is_empty(),
            "record {i}: {r:?}"
        );
    }
    // attach (ADR 0031, gap 5) precedes process.started: the change is bound eagerly inside start, before run's own
    // first cycle ever runs.
    let want = [
        "attach",
        "process.started",
        "schedule",
        "tick",
        "action",
        "tick",
        "action",
        "tick",
        "action",
        "tick",
        "process.ended",
    ];
    assert_eq!(kinds, want, "kinds");
    // the run says why it happens: the process was started, queued, then picked up
    let sch = &recs[2];
    assert!(
        sch.data.get("reason") == Some(&Value::from("started")) && sch.ended_at >= sch.started_at,
        "schedule record: {sch:?}"
    );
    let act = &recs[4];
    assert!(
        act.action == "identify_impacts"
            && act.action_kind == "llm"
            && act.model_calls.len() == 1
            && act.effects_met == Some(true)
            && act.nodes.len() == 1,
        "action record: {act:?}"
    );
    // the prompt of the call is an entry of the log of the change, linked to the action run; the record and the
    // process keep the counts only
    let (entries, _) = g
        .change_log(
            &ctx,
            &LogFilter {
                change: p.change_id.clone(),
                types: vec![format!("{}.call", journal::STREAM_MODEL)],
                execution: act.id.clone(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        entries.len() == 1 && entries[0].subject == "0" && entries[0].process == p.id,
        "model entries: {entries:?}"
    );
    let ex: journal::ModelExchange = serde_json::from_value(entries[0].payload.clone()).unwrap();
    assert!(
        !ex.system.is_empty() && !ex.messages.is_empty() && !ex.response.is_empty(),
        "exchange: {ex:?}"
    );
    assert!(
        act.model_calls[0].exchange.is_none(),
        "the record holds the prompt"
    );
    for st in &p.steps {
        for c in &st.llm_calls {
            assert!(
                c.exchange.is_none(),
                "the process holds a prompt: step {}",
                st.index
            );
        }
    }
    // provenance: every item produced by an action points to its record
    let c = g.change(&ctx, &p.change_id).await.unwrap();
    for it in &c.items {
        let r = by_id.get(&it.execution);
        assert!(
            r.is_some_and(|r| r.action == it.produced_by),
            "item {} ({}) without journal provenance",
            it.id,
            it.produced_by
        );
    }
    for n in &c.nodes {
        let r = by_id.get(&n.execution);
        assert!(
            r.is_some_and(|r| r.action == n.produced_by),
            "change impact {} ({}) without journal provenance",
            n.key,
            n.produced_by
        );
    }
    let end = &recs[recs.len() - 1];
    assert!(
        end.status == STATUS_COMPLETED
            && end.data.get("steps").and_then(Value::as_f64) == Some(3.0),
        "end record: {end:?}"
    );
    assert!(
        recs[3].plan[0] == "identify_impacts"
            && recs[3].data.get("replanned") == Some(&Value::Bool(false)),
        "tick: {:?}",
        recs[3]
    );
}

/// A step starts from a blackboard state, reads existing nodes and produces the next state: the marks of
/// consecutive steps chain, and the items produced by a step account for the growth of the board.
#[tokio::test]
async fn journal_steps_chain_blackboard_states() {
    let (p, g, _errs) = impact_run(setup().await).await;
    let acts: Vec<_> = records(&g, &p)
        .await
        .into_iter()
        .filter(|r| r.kind == journal::KIND_ACTION)
        .collect();
    assert!(
        acts.len() >= 2,
        "expected several actions, got {}",
        acts.len()
    );
    let mut saw_reads = false;
    for (i, a) in acts.iter().enumerate() {
        assert_eq!(
            a.board_after - a.board_before,
            a.items.len() as i64,
            "action {}: board {} -> {} but {} items",
            a.action,
            a.board_before,
            a.board_after,
            a.items.len()
        );
        if i > 0 {
            assert_eq!(
                a.board_before,
                acts[i - 1].board_after,
                "action {} starts from the board the previous one ended at",
                a.action
            );
        }
        saw_reads = saw_reads || !a.reads.is_empty();
    }
    assert!(saw_reads, "no step recorded the node versions it read");
}

/// An action generated for a process step journals its activity run against that step's path (ExecutionRecord
/// ActivityRef), not just its own action name.
#[tokio::test]
async fn journal_records_activity_ref() {
    let ctx = ctx();
    let mut s = setup().await;
    add_yaml(&mut s, STAGED_YAML);
    let (e, g, base, _errs) = s.share();
    let p = e
        .start(
            &ctx,
            StartRequest {
                methodology: "staged".into(),
                goal: "delivery".into(),
                intent: "deliver the note".into(),
                project_id: TEST_PROJECT.into(),
                baseline_id: base,
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
                .is_some_and(|t| t.action == "delivery/prepare/note"),
        "expected the note step, got {} {:?}",
        p.status,
        p.pending
    );
    let writer = as_who(&Principal {
        subject: "w".into(),
        roles: vec!["writer@ORG-DEFAULT".into()],
        ..Default::default()
    });
    e.submit(
        &writer,
        &p.id,
        vec![ItemInput {
            r#type: "artifact".into(),
            target: "note".into(),
            ..Default::default()
        }],
    )
    .await
    .unwrap();
    let recs = records(&g, &p).await;
    let mut found = false;
    for r in recs {
        if r.kind == journal::KIND_ACTION && r.action == "delivery/prepare/note" {
            found = true;
            assert_eq!(
                r.activity_ref, "delivery/prepare/note",
                "activityRef: the step path"
            );
        }
    }
    assert!(found, "no action record for the note step");
}
