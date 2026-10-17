//! preview_test.go.

use std::collections::BTreeMap;
use std::path::Path;

use goap_domain::Blackboard;
use goap_methodology::{Compiled, PLANNER_GOAP, PLANNER_HYBRID, PLANNER_LLM, PLANNER_LLM_SCORING};
use serde_json::json;

use crate::preview::preview_plan;

/// load_methodology loads a methodology of methodologies/ (else methodologies/examples/, not seeded at start).
pub(crate) fn load_methodology(file: &str) -> Compiled {
    super::fakes::init();
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../methodologies");
    let mut path = root.join(file);
    if !path.exists() {
        path = root.join("examples").join(file);
    }
    goap_methodology::load_file(&path)
        .unwrap()
        .compile()
        .unwrap()
}

fn conds(v: &[(&str, bool)]) -> BTreeMap<String, bool> {
    v.iter().map(|(k, b)| (k.to_string(), *b)).collect()
}

/// The plain A* planner (test-designer, goap): from an empty world it chains collect_scope -> design_tests ->
/// summarize to reach "designed".
#[test]
fn preview_plan_goap() {
    let m = load_methodology("test-design.yaml");
    let p = preview_plan(
        &m,
        &Blackboard::default(),
        "test-designer",
        "designed",
        &BTreeMap::new(),
    )
    .unwrap();
    assert!(
        !p.reached && p.planner == PLANNER_GOAP && p.cost == 3.0,
        "{p:?}"
    );
    let got: Vec<&str> = p.actions.iter().map(|a| a.name.as_str()).collect();
    assert_eq!(got, ["collect_scope", "design_tests", "summarize"]);
}

/// The greedy utility planner (reviewer): the override makes both candidate actions admissible, and the utility CEL
/// (read from bb.vars) picks between them.
#[test]
fn preview_plan_utility() {
    let m = load_methodology("test-design.yaml");
    let overrides = conds(&[("tests_designed", true)]);
    let p = preview_plan(&m, &Blackboard::default(), "reviewer", "review", &overrides).unwrap();
    assert!(
        p.actions.len() == 1 && p.actions[0].name == "auto_review",
        "expected auto_review: {p:?}"
    );
    // a human reviewer is preferred: auto_review's utility drops below human_review's
    let bb = Blackboard {
        vars: json!({"review": "human"}).as_object().cloned().unwrap(),
        ..Default::default()
    };
    let p = preview_plan(&m, &bb, "reviewer", "review", &overrides).unwrap();
    assert!(
        p.actions.len() == 1 && p.actions[0].name == "human_review",
        "expected human_review: {p:?}"
    );
}

/// The hybrid (utility-weighted A*) planner on the coordinator agent.
#[test]
fn preview_plan_hybrid() {
    let m = load_methodology("test-design.yaml");
    let p = preview_plan(
        &m,
        &Blackboard::default(),
        "coordinator",
        "delivered",
        &BTreeMap::new(),
    )
    .unwrap();
    assert!(
        p.planner == PLANNER_HYBRID && p.actions.len() == 1 && p.actions[0].name == "delegate",
        "{p:?}"
    );
}

/// Overriding the goal's own conditions true needs no plan.
#[test]
fn preview_plan_reached() {
    let m = load_methodology("test-design.yaml");
    let p = preview_plan(
        &m,
        &Blackboard::default(),
        "reviewer",
        "review",
        &conds(&[("reviewed", true)]),
    )
    .unwrap();
    assert!(p.reached && p.actions.is_empty(), "{p:?}");
}

/// Forcing tests_designed false leaves the reviewer with no admissible action; it is waiting on tests_designed
/// (established outside this agent), not stuck.
#[test]
fn preview_plan_awaiting() {
    let m = load_methodology("test-design.yaml");
    let p = preview_plan(
        &m,
        &Blackboard::default(),
        "reviewer",
        "review",
        &conds(&[("tests_designed", false)]),
    )
    .unwrap();
    assert!(
        !p.reached && p.actions.is_empty() && p.awaiting == ["tests_designed"],
        "{p:?}"
    );
}

/// An llm / llm-scoring planner cannot be previewed (it would call a model live).
#[test]
fn preview_plan_live_planner() {
    let m = load_methodology("sdlc.yaml");
    for ag in m.agent_list() {
        if ag.planner != PLANNER_LLM && ag.planner != PLANNER_LLM_SCORING {
            continue;
        }
        let goals = m.agent_goals(&ag);
        let Some(g) = goals.first() else {
            continue;
        };
        let err = preview_plan(
            &m,
            &Blackboard::default(),
            &ag.name,
            &g.name,
            &BTreeMap::new(),
        )
        .unwrap_err();
        assert!(
            err.is_live_planner(),
            "agent {}: expected ErrLivePlanner, got {err}",
            ag.name
        );
        return;
    }
    // no llm / llm-scoring agent in sdlc.yaml: nothing to check (Go skips)
}
