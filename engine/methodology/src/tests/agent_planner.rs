use std::sync::Arc;

use super::types::{ref_methodology, test_domain};
use crate::{Agent, PLANNER_LLM, PLANNER_LLM_SCORING};

#[test]
fn agent_model_required_for_llm_planners() {
    for planner in [PLANNER_LLM, PLANNER_LLM_SCORING] {
        let mut m = ref_methodology();
        m.agents = vec![Agent {
            name: "a".into(),
            planner: planner.into(),
            ..Agent::default()
        }];
        let types = Arc::new(goap_def::domain_types(&[test_domain()]));
        let issues = m.resolve(types.clone()).validate();
        assert!(
            issues.0.len() == 1 && issues.0[0].path == "agents[0].model",
            "{planner} planner without model: {issues}"
        );
        m.agents[0].model = "fast".into();
        let issues = m.resolve(types).validate();
        assert!(
            issues.0.is_empty(),
            "{planner} planner with model: {issues}"
        );
    }
}

#[test]
fn unknown_planner_message_lists_all_five() {
    let mut m = ref_methodology();
    m.agents = vec![Agent {
        name: "a".into(),
        planner: "bogus".into(),
        ..Agent::default()
    }];
    let issues = m
        .resolve(Arc::new(goap_def::domain_types(&[test_domain()])))
        .validate();
    assert!(
        issues.0.len() == 1 && issues.0[0].path == "agents[0].planner",
        "issues: {issues}"
    );
    for want in ["goap", "utility", "hybrid", "llm", "llm-scoring"] {
        assert!(
            issues.0[0].message.contains(want),
            "{}",
            issues.0[0].message
        );
    }
}
