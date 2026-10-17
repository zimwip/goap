//! planners_test.go.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_goap::{Action as Op, Goal as PlanGoal, Planner, WorldState};
use goap_llm::{Client, ClientFunc, Request, Response};
use goap_methodology::{
    Action, Agent, Compiled, Condition, Goal, KIND_HUMAN, Methodology, PLANNER_HYBRID, PLANNER_LLM,
    PLANNER_LLM_SCORING,
};

use super::fakes::engine;
use crate::planners::reweight_plan;

fn conds(v: &[(&str, bool)]) -> BTreeMap<String, bool> {
    v.iter().map(|(k, b)| (k.to_string(), *b)).collect()
}

fn action(name: &str, pre: &[(&str, bool)], description: &str) -> Action {
    let mut a = Action::default();
    a.name = name.into();
    a.kind = KIND_HUMAN.into();
    a.pre = conds(pre);
    a.effects = conds(&[("ready", true)]);
    a.description = description.into();
    a
}

fn planner_test_compiled() -> Compiled {
    let m = Methodology {
        name: "plannertest".into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![
            Condition {
                name: "ready".into(),
                expr: "true".into(),
                ..Default::default()
            },
            Condition {
                name: "blocked".into(),
                expr: "true".into(),
                ..Default::default()
            },
        ],
        goals: vec![Goal {
            name: "done".into(),
            pre: conds(&[("ready", true)]),
            ..Default::default()
        }],
        actions: vec![
            action("prep", &[], "prepares things"),
            action("other", &[("blocked", true)], ""),
        ],
        ..Default::default()
    };
    m.compile().unwrap()
}

fn fixtures() -> (Vec<Op>, PlanGoal, WorldState) {
    let actions = vec![
        Op {
            name: "prep".into(),
            effects: conds(&[("ready", true)]),
            cost: 1.0,
            ..Default::default()
        },
        Op {
            name: "other".into(),
            pre: conds(&[("blocked", true)]),
            effects: conds(&[("ready", true)]),
            cost: 1.0,
        },
    ];
    let goal = PlanGoal {
        name: "done".into(),
        pre: conds(&[("ready", true)]),
        ..Default::default()
    };
    (actions, goal, WorldState::default())
}

fn answering(text: &'static str) -> Arc<dyn Client> {
    ClientFunc::shared(move |_ctx: Ctx, r: Request| async move {
        Ok(Response {
            text: text.into(),
            provider: "test".into(),
            model: r.model.clone(),
            ..Default::default()
        })
    })
}

fn agent(planner: &str) -> Agent {
    let mut ag = Agent::default();
    ag.name = "a".into();
    ag.planner = planner.into();
    ag.model = "fast".into();
    ag
}

#[tokio::test]
async fn llm_planner_picks_action() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let mut e = engine();
    e.llm = Some(answering(r#"{"action":"prep"}"#));
    let (plan, calls) = e
        .plan(
            &Ctx::background(),
            &m,
            &agent(PLANNER_LLM),
            &world,
            &actions,
            &goal,
            &BTreeMap::new(),
        )
        .await;
    let plan = plan.unwrap();
    assert!(
        plan.actions.len() == 1 && plan.actions[0].name == "prep",
        "plan: {plan:?}"
    );
    assert!(
        calls.len() == 1 && calls[0].model == "fast",
        "calls: {calls:?}"
    );
}

#[tokio::test]
async fn llm_planner_no_action() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let mut e = engine();
    e.llm = Some(answering(r#"{"action":""}"#));
    let (plan, _) = e
        .plan(
            &Ctx::background(),
            &m,
            &agent(PLANNER_LLM),
            &world,
            &actions,
            &goal,
            &BTreeMap::new(),
        )
        .await;
    let err = plan.unwrap_err();
    assert!(err.is_no_plan(), "err: {err}");
}

#[tokio::test]
async fn llm_planner_rejects_unavailable_action() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let mut e = engine();
    e.llm = Some(answering(r#"{"action":"nonexistent"}"#));
    let (plan, _) = e
        .plan(
            &Ctx::background(),
            &m,
            &agent(PLANNER_LLM),
            &world,
            &actions,
            &goal,
            &BTreeMap::new(),
        )
        .await;
    let err = plan.unwrap_err();
    assert!(err.is_planner_answer(), "err: {err}");
    assert!(
        !err.is_no_plan(),
        "must not be mistaken for ErrNoPlan: {err}"
    );
}

#[tokio::test]
async fn llm_planner_rejects_action_with_unmet_preconditions() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let mut e = engine();
    e.llm = Some(answering(r#"{"action":"other"}"#));
    let (plan, _) = e
        .plan(
            &Ctx::background(),
            &m,
            &agent(PLANNER_LLM),
            &world,
            &actions,
            &goal,
            &BTreeMap::new(),
        )
        .await;
    let err = plan.unwrap_err();
    assert!(err.is_planner_answer(), "err: {err}");
}

#[tokio::test]
async fn llm_scoring_planner_reweights() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let mut e = engine();
    e.llm = Some(answering(r#"{"utilities":{"prep":5}}"#));
    let (plan, calls) = e
        .plan(
            &Ctx::background(),
            &m,
            &agent(PLANNER_LLM_SCORING),
            &world,
            &actions,
            &goal,
            &BTreeMap::new(),
        )
        .await;
    let plan = plan.unwrap();
    let utilities: BTreeMap<String, f64> = [("prep".to_string(), 5.0)].into();
    let want = reweight_plan(&Planner::default(), &world, &actions, &goal, &utilities).unwrap();
    assert!(
        plan.actions.len() == want.actions.len() && plan.actions[0].name == want.actions[0].name,
        "plan {plan:?} want {want:?}"
    );
    assert_eq!(calls.len(), 1, "calls: {calls:?}");
}

#[tokio::test]
async fn llm_scoring_planner_excludes_unscored_actions() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let mut e = engine();
    e.llm = Some(answering(r#"{"utilities":{}}"#));
    let (plan, _) = e
        .plan(
            &Ctx::background(),
            &m,
            &agent(PLANNER_LLM_SCORING),
            &world,
            &actions,
            &goal,
            &BTreeMap::new(),
        )
        .await;
    let err = plan.unwrap_err();
    assert!(err.is_no_plan(), "err: {err}");
}

#[tokio::test]
async fn hybrid_planner_unaffected_by_reweight_extraction() {
    let m = planner_test_compiled();
    let (actions, goal, world) = fixtures();
    let e = engine();
    let mut ag = agent(PLANNER_HYBRID);
    ag.model = String::new();
    let utilities: BTreeMap<String, f64> =
        [("prep".to_string(), 2.0), ("other".to_string(), 5.0)].into();
    let (plan, calls) = e
        .plan(
            &Ctx::background(),
            &m,
            &ag,
            &world,
            &actions,
            &goal,
            &utilities,
        )
        .await;
    let plan = plan.unwrap();
    assert!(calls.is_empty(), "hybrid must not call an LLM: {calls:?}");
    let want = reweight_plan(&Planner::default(), &world, &actions, &goal, &utilities).unwrap();
    assert!(
        plan.actions.len() == want.actions.len() && plan.actions[0].name == want.actions[0].name,
        "plan {plan:?} want {want:?}"
    );
}
