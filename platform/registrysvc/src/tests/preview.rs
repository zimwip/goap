//! preview_test.go and preview_draft_test.go: the handler over drafts sent as edited.

use std::sync::Arc;

use goap_connect::{Code, Request};
use goap_gen::registry::v1::{self as pb, RegistryService};
use goap_methodology::{
    Action, Agent, Condition, Goal, KIND_HUMAN, Methodology, PLANNER_LLM, Process, Step,
};

use super::{bg, memory};
use crate::convert::to_pb;
use crate::handler::Handler;
use crate::service::Service;
use crate::store::Record;

fn handler() -> Handler {
    super::setup();
    Handler::new(Arc::new(Service::new(memory())))
}

fn work(name: &str) -> Action {
    let mut a = Action::default();
    a.name = name.into();
    a.kind = KIND_HUMAN.into();
    a.instructions = "do it".into();
    a.effects.insert("done".into(), true);
    a
}

fn step(name: &str, action: &str) -> Step {
    Step {
        name: name.into(),
        action: action.into(),
        ..Step::default()
    }
}

fn flow(steps: Vec<Step>) -> Methodology {
    Methodology {
        name: "m".into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![Condition {
            name: "done".into(),
            expr: "false".into(),
            ..Condition::default()
        }],
        processes: vec![Process {
            name: "flow".into(),
            steps,
            ..Process::default()
        }],
        actions: vec![work("work")],
        ..Methodology::default()
    }
}

fn pbm(m: &Methodology) -> Option<pb::Methodology> {
    Some(to_pb(&Record {
        methodology: m.clone(),
        ..Record::default()
    }))
}

async fn preview(
    m: &Methodology,
    agent: &str,
    goal: &str,
) -> Result<pb::PreviewPlanResponse, goap_connect::ConnectError> {
    handler()
        .preview_plan(
            &bg(),
            Request::new(pb::PreviewPlanRequest {
                methodology: pbm(m),
                agent: agent.into(),
                goal: goal.into(),
                ..Default::default()
            }),
        )
        .await
        .map(|r| r.msg)
}

// A trivial one-step process is planned straight from the request's methodology.
#[tokio::test]
async fn preview_plan_handler() {
    let r = preview(&flow(vec![step("step1", "work")]), "flow", "flow")
        .await
        .expect("preview");
    assert!(r.issues.is_empty(), "issues: {:?}", r.issues);
    let p = r.preview.expect("a plan");
    assert!(
        !p.reached
            && p.actions.len() == 1
            && p.actions[0].step == "flow/step1"
            && p.actions[0].kind == KIND_HUMAN,
        "{p:?}"
    );
}

// Naming an agent or goal the methodology does not declare is a not-found RPC error.
#[tokio::test]
async fn preview_plan_handler_unknown_goal() {
    let err = preview(&flow(vec![step("step1", "work")]), "nope", "nope")
        .await
        .err()
        .expect("an error");
    assert_eq!(err.code(), Code::NotFound, "{err}");
}

// An llm agent cannot be previewed: reported as an issue, not an RPC error.
#[tokio::test]
async fn preview_plan_handler_live_planner() {
    let mut a = Agent::default();
    a.name = "a".into();
    a.planner = PLANNER_LLM.into();
    a.model = "fast".into();
    a.actions = vec!["work".into()];
    a.goals = vec!["g".into()];
    let m = Methodology {
        name: "m".into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![Condition {
            name: "done".into(),
            expr: "false".into(),
            ..Condition::default()
        }],
        goals: vec![Goal {
            name: "g".into(),
            pre: [("done".to_string(), true)].into_iter().collect(),
            ..Goal::default()
        }],
        actions: vec![work("work")],
        agents: vec![a],
        ..Methodology::default()
    };
    let r = preview(&m, "a", "g").await.expect("preview");
    assert!(r.preview.is_none() && r.issues.len() == 1, "{r:?}");
}

// A draft with a broken rule elsewhere is still previewed, the issues coming with the plan.
#[tokio::test]
async fn preview_plan_handler_of_a_draft_with_issues() {
    let mut m = flow(vec![step("step1", "work")]);
    let mut broken = work("broken");
    broken.instructions = "x".into();
    broken.pre.insert("nope".into(), true);
    m.actions.push(broken);
    let r = preview(&m, "flow", "flow").await.expect("preview");
    assert!(
        r.preview.is_some() && !r.issues.is_empty(),
        "want a plan with issues: {r:?}"
    );
}

// A draft that breaks a rule still gets its graph and levels, with the issues tied to the step they are about.
#[tokio::test]
async fn graph_and_levels_of_a_draft_with_broken_rules() {
    let m = flow(vec![step("fine", "work"), step("broken", "nope")]);
    let h = handler();
    let g = h
        .get_process_graph(
            &bg(),
            Request::new(pb::GetProcessGraphRequest {
                methodology: pbm(&m),
                process: "flow".into(),
                ..Default::default()
            }),
        )
        .await
        .expect("graph")
        .msg;
    assert!(
        g.graph.as_ref().is_some_and(|g| !g.steps.is_empty()),
        "graph {g:?}"
    );
    assert!(
        !g.issues.is_empty() && g.issues[0].activity == "flow/broken",
        "issues {:?}",
        g.issues
    );
    let l = h
        .check_levels(
            &bg(),
            Request::new(pb::CheckLevelsRequest {
                methodology: pbm(&m),
                root: "flow".into(),
                ..Default::default()
            }),
        )
        .await
        .expect("levels")
        .msg;
    assert!(!l.levels.is_empty() && !l.issues.is_empty(), "levels {l:?}");
}

// The view of a methodology as edited is drawn as far as it stands (ADR 0126 step 3).
#[tokio::test]
async fn methodology_view_of_a_draft() {
    let mut m = flow(vec![step("fine", "work"), step("broken", "nope")]);
    m.conditions[0].description = "the work is done".into();
    let r = handler()
        .get_methodology_view(
            &bg(),
            Request::new(pb::GetMethodologyViewRequest {
                methodology: pbm(&m),
                ..Default::default()
            }),
        )
        .await
        .expect("view")
        .msg;
    assert!(!r.issues.is_empty(), "issues");
    let v = r.view.expect("a view");
    assert!(
        v.processes.len() == 1 && v.processes[0].name == "flow" && !v.processes[0].steps.is_empty(),
        "processes {:?}",
        v.processes
    );
    let fine = &v.processes[0].steps[0];
    assert!(
        fine.path == "flow/fine" && fine.owner == "flow" && fine.exit.get("done") == Some(&true),
        "step {fine:?}"
    );
    let c = v.conditions.get("done").expect("condition");
    assert!(
        c.description == "the work is done" && !c.produced_by.is_empty(),
        "{c:?}"
    );
}
