//! The compiled view holds the activities as they are shown, and nothing of a change (ADR 0126 step 3).

use std::sync::Arc;

use goap_methodology::{
    ACTIVITY_PROCESS, Action, Condition, KIND_HUMAN, METHOD_ACTION, Methodology, Process, Step,
};

#[test]
fn compiled_view() {
    let m = Methodology {
        name: "m".into(),
        version: "1".into(),
        namespace: "alm".into(),
        conditions: vec![
            Condition {
                name: "ready".into(),
                expr: "true".into(),
                ..Condition::default()
            },
            Condition {
                name: "done".into(),
                expr: "false".into(),
                description: "the work is done".into(),
                ..Condition::default()
            },
        ],
        processes: vec![Process {
            name: "flow".into(),
            description: "the flow".into(),
            steps: vec![Step {
                name: "work".into(),
                action: "work".into(),
                pre: [("ready".to_string(), true)].into(),
                ..Step::default()
            }],
            ..Process::default()
        }],
        actions: vec![{
            let mut a = Action::default();
            a.name = "work".into();
            a.kind = KIND_HUMAN.into();
            a.instructions = "do it".into();
            a.effects = [("done".to_string(), true)].into();
            a
        }],
        ..Methodology::default()
    };
    let c = m.compile().unwrap();
    let v = c.view();
    assert!(Arc::ptr_eq(&v, &c.view()), "the view is computed again");
    let p = v.process("flow").expect("process");
    assert!(
        p.kind == ACTIVITY_PROCESS
            && !p.agent.is_empty()
            && p.description == "the flow"
            && !p.done.is_empty(),
        "process {p:?}"
    );
    let s = &p.steps[0];
    assert!(
        s.path == "flow/work"
            && s.owner == "flow"
            && s.method == METHOD_ACTION
            && s.target == "work"
            && s.entry.get("ready") == Some(&true)
            && s.exit.get("done") == Some(&true),
        "step {s:?}"
    );
    let d = v.condition("done");
    assert!(
        d.description == "the work is done" && !d.produced_by.is_empty(),
        "condition done {d:?}"
    );
    for name in p.done.keys() {
        assert!(
            !v.condition(name).description.is_empty(),
            "the generated condition {name} of the goal carries no description"
        );
    }
}
