use crate::{Action, AssessmentState, AssessmentView, Condition, Goal, Methodology};

#[test]
fn assessment_views_compile() {
    let mut m = Methodology {
        name: "m".into(),
        namespace: "alm".into(),
        conditions: vec![Condition {
            name: "ready".into(),
            expr: "true".into(),
            ..Condition::default()
        }],
        goals: vec![Goal {
            name: "g".into(),
            pre: [("ready".to_string(), true)].into(),
            ..Goal::default()
        }],
        actions: vec![Action {
            name: "a".into(),
            kind: "human".into(),
            effects: [("ready".to_string(), true)].into(),
            ..Action::default()
        }],
        assessments: vec![AssessmentView {
            subject: "description".into(),
            states: vec![
                AssessmentState {
                    conditions: [("ready".to_string(), true)].into(),
                    tone: "ok".into(),
                    ..AssessmentState::default()
                },
                AssessmentState {
                    conditions: [("unknown".to_string(), true)].into(),
                    tone: "blue".into(),
                    ..AssessmentState::default()
                },
            ],
        }],
        ..Methodology::default()
    };
    let (_, issues) = m.compile_lenient();
    let all: Vec<&str> = issues.0.iter().map(|i| i.path.as_str()).collect();
    let all = all.join(" ");
    for want in [
        "assessments[0].states[1].conditions.unknown",
        "assessments[0].states[1].tone",
    ] {
        assert!(all.contains(want), "missing {want} in {issues}");
    }
    m.assessments[0].states.truncate(1);
    m.compile().unwrap();
}
