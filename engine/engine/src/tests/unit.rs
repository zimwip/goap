//! unit_test.go, unwraptypes_test.go, exchange_behaviors_test.go and loopbudget_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use chrono::{Duration, TimeZone, Utc};
use goap_ctx::Ctx;
use goap_domain::{
    DecisionPoint, POINT_BLOCKED, POINT_DECIDED, POINT_ESCALATED, POINT_OPEN, QUESTION_OPEN,
    Question,
};
use goap_goap::{Action as Op, Goal, WorldState};
use goap_llm::{CallMeta, ClientFunc, Message, Request, Response, ToolResult};
use goap_methodology::Responsibilities;
use parking_lot::Mutex;
use serde_json::json;

use super::fakes::engine;
use crate::asks::{ASK_DECISION, ASK_QUESTION, decision_asks};
use crate::executors::{ELIDED, cap_result, compact, unwrap_types};
use crate::flow::base_title;
use crate::items::ItemInput;
use crate::planners::PlanRecorder;
use crate::process::exchange_of;
use crate::steps::StepContext;

fn conds(v: &[(&str, bool)]) -> BTreeMap<String, bool> {
    v.iter().map(|(k, b)| (k.to_string(), *b)).collect()
}

#[test]
fn waiting_is_told_from_stuck() {
    let e = engine();
    let write = Op {
        name: "write".into(),
        effects: conds(&[("written", true)]),
        cost: 1.0,
        ..Default::default()
    };
    let release = Op {
        name: "release".into(),
        pre: conds(&[("written", true), ("approved", true), ("frozen", false)]),
        effects: conds(&[("released", true)]),
        cost: 1.0,
    };
    let goal = Goal {
        name: "shipped".into(),
        pre: conds(&[("released", true)]),
        ..Default::default()
    };
    let all = vec![write, release.clone()];
    let world = WorldState(conds(&[("frozen", true)]));

    // approved and !frozen are established outside the agent: it waits for them
    assert_eq!(
        e.awaited(&world, &all, &all, &goal),
        vec!["!frozen".to_string(), "approved".to_string()]
    );
    // once they hold, nothing is awaited any more (the planner finds a plan)
    let ok = WorldState(conds(&[("approved", true), ("frozen", false)]));
    assert!(
        e.awaited(&ok, &all, &all, &goal).is_empty(),
        "nothing to wait for"
    );
    // the agent's own action that would establish what is missing is not admissible: stuck
    assert!(
        e.awaited(&ok, &all, std::slice::from_ref(&release), &goal)
            .is_empty(),
        "stuck, not waiting"
    );
    // what is established outside would not be enough: stuck
    assert!(
        e.awaited(&world, &all, std::slice::from_ref(&release), &goal)
            .is_empty(),
        "stuck, not waiting"
    );
}

#[test]
fn decision_asks_list_questions_and_escalations() {
    let at = Utc.with_ymd_and_hms(2026, 10, 1, 0, 0, 0).unwrap();
    let points = vec![
        DecisionPoint {
            id: "d1".into(),
            question: "Which option?".into(),
            status: POINT_BLOCKED.into(),
            opened_at: at,
            questions: vec![
                Question {
                    id: "q1".into(),
                    point: "d1".into(),
                    text: "What is the budget?".into(),
                    status: QUESTION_OPEN.into(),
                    asked_at: at + Duration::hours(1),
                    ..Default::default()
                },
                Question {
                    id: "q0".into(),
                    point: "d1".into(),
                    text: "Answered".into(),
                    status: "answered".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        },
        DecisionPoint {
            id: "d2".into(),
            question: "Go or no go?".into(),
            status: POINT_ESCALATED.into(),
            escalation: "too many rounds".into(),
            opened_at: at,
            ..Default::default()
        },
        DecisionPoint {
            id: "d3".into(),
            question: "Agents rule it".into(),
            status: POINT_OPEN.into(),
            opened_at: at,
            ..Default::default()
        },
        DecisionPoint {
            id: "d4".into(),
            question: "Done".into(),
            status: POINT_DECIDED.into(),
            opened_at: at,
            ..Default::default()
        },
    ];
    let asks = decision_asks(&points);
    assert_eq!(asks.len(), 2, "a question and an escalated point: {asks:?}");
    let a = &asks[0];
    assert!(
        a.kind == ASK_QUESTION
            && a.question == "q1"
            && a.point == "d1"
            && a.title == "What is the budget?"
            && a.since == at + Duration::hours(1),
        "question: {a:?}"
    );
    let a = &asks[1];
    assert!(
        a.kind == ASK_DECISION && a.point == "d2" && a.detail == "too many rounds",
        "decision: {a:?}"
    );
}

/// The brief of a step tells the agent instance its role and that it starts fresh (ADR 0050).
#[test]
fn step_section_tells_role_and_fresh_start() {
    let sc = StepContext {
        process: "p".into(),
        path: "p/s".into(),
        roles: Some(Responsibilities {
            responsible: "builder".into(),
            ..Default::default()
        }),
        ..Default::default()
    };
    let got = StepContext::section(Some(&sc));
    assert!(
        got.contains(r#"role "builder""#) && got.contains("start this step fresh"),
        "section: {got}"
    );
}

/// The calls of the planner are the first calls of the step they choose (the step takes them over, new_step).
#[tokio::test]
async fn planner_calls_are_numbered_in_their_step() {
    let metas: Arc<Mutex<Vec<CallMeta>>> = Arc::default();
    let m2 = metas.clone();
    let client = ClientFunc::shared(move |ctx: Ctx, _req: Request| {
        let m2 = m2.clone();
        async move {
            m2.lock().push(goap_llm::meta_from(&ctx));
            Ok(Response {
                text: "{}".into(),
                ..Default::default()
            })
        }
    });
    let mut rec = PlanRecorder {
        client: Some(client),
        calls: Vec::new(),
    };
    let ctx = goap_llm::with_meta(
        &Ctx::background(),
        CallMeta {
            source: goap_llm::SOURCE_ENGINE.into(),
            process_id: "p".into(),
            change_id: "c".into(),
            step: 3,
            agent: "ag".into(),
            ..Default::default()
        },
    );
    for _ in 0..2 {
        rec.complete(&ctx, Request::default()).await.unwrap();
    }
    let metas = metas.lock();
    assert!(
        metas.len() == 2
            && metas[0].step == 3
            && metas[0].call == 0
            && metas[1].call == 1
            && metas[1].source == goap_llm::SOURCE_ENGINE
            && metas[1].agent == "ag",
        "{metas:?}"
    );
}

#[test]
fn relaunched_titles_do_not_pile_up() {
    assert_eq!(
        base_title("design it (relaunch from step 4) (relaunch from step 2)"),
        "design it"
    );
}

/// A model told to write an assessment may wrap it as an artifact of that type: it gets its type back (ADR 0110).
#[test]
fn unwrap_kinds() {
    let item = |t: &str, target: &str, data: serde_json::Value| ItemInput {
        r#type: t.into(),
        target: target.into(),
        data: data.as_object().cloned().unwrap_or_default(),
        ..Default::default()
    };
    let mut items = vec![
        item(
            "artifact",
            "assessment",
            json!({"key": "description", "score": 2}),
        ),
        item(
            "artifact",
            "comment",
            json!({"about": "description", "text": "ok"}),
        ),
        item("artifact", "question", json!({"text": "why?"})),
        item("assessment", "", json!({"key": "title", "score": 4})),
    ];
    unwrap_types(&mut items);
    let want = [
        ("assessment", ""),
        ("comment", ""),
        ("artifact", "question"),
        ("assessment", ""),
    ];
    for (i, (t, target)) in want.iter().enumerate() {
        assert_eq!(
            (items[i].r#type.as_str(), items[i].target.as_str()),
            (*t, *target),
            "item {i}"
        );
    }
}

/// The log of the change records the system text the engine built and the behaviours the gateway added to it
/// (ADR 0093).
#[test]
fn exchange_records_gateway_behaviors() {
    let req = Request {
        system: "S".into(),
        messages: vec![Message {
            role: "user".into(),
            content: "q".into(),
            ..Default::default()
        }],
        ..Default::default()
    };
    let resp = Response {
        text: "a".into(),
        behaviors: vec!["terse".into(), "!big".into()],
        behavior_tokens: 40,
        ..Default::default()
    };
    let x = exchange_of(&req, &resp);
    assert!(
        x.system == "S"
            && x.behaviors.len() == 2
            && x.behaviors[0] == "terse"
            && x.behavior_tokens == 40,
        "{x:?}"
    );
}

#[test]
fn compact_elides_old_results() {
    let big = "x".repeat(5000);
    let mut msgs = vec![Message {
        role: "user".into(),
        content: format!("the task {big}"),
        ..Default::default()
    }];
    for i in 0..20 {
        // distinct results: identical ones are deduplicated
        msgs.push(Message {
            role: "assistant".into(),
            content: "calling".into(),
            ..Default::default()
        });
        msgs.push(Message {
            role: "user".into(),
            tool_results: vec![ToolResult {
                call_id: "c".into(),
                content: big[i..].to_string(),
                ..Default::default()
            }],
            ..Default::default()
        });
    }
    let got = compact(&msgs, 1 << 20);
    assert_eq!(
        got[2].tool_results[0].content, big,
        "a conversation under the limit is compacted"
    );
    let got = compact(&msgs, 50_000);
    assert!(
        got.len() == msgs.len() && got[0].content == msgs[0].content,
        "the task or the structure changed"
    );
    let c = &got[2].tool_results[0].content;
    assert!(
        c.ends_with(ELIDED) && c.len() <= 300,
        "old result: {} bytes",
        c.len()
    );
    assert_eq!(
        got[got.len() - 1].tool_results[0].content,
        big[19..],
        "a latest result was elided"
    );
    assert_eq!(
        msgs[2].tool_results[0].content, big,
        "compact changed its input"
    );
    // compacting again changes nothing
    let again = compact(&got, 50_000);
    assert_eq!(
        again[2].tool_results[0].content, got[2].tool_results[0].content,
        "compaction is not stable"
    );
}

#[test]
fn cap_result_keeps_the_tail() {
    let s = format!("BEGIN{}the build failed here", "a".repeat(1000));
    let got = cap_result(&s, 300);
    assert!(
        got.starts_with("BEGIN")
            && got.ends_with("the build failed here")
            && got.contains("bytes cut")
            && got.len() <= 350,
        "cap: {got:?}"
    );
}
