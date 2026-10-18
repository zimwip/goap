use goap_domain::{Blackboard, ChangeItem, TYPE_ARTIFACT};

use super::compile_process;
use crate::{LEVEL_ACTION, PLANNER_GOAP, PLANNER_HYBRID};

// A method composing its own steps compiles exactly like a Process.
#[test]
fn method_with_steps_compiles_like_a_process() {
    let (c, issues) = compile_process(
        r#"
methods:
  - name: assemble
    for: assembling
    steps:
      - {name: first, action: do_a}
      - {name: second, action: do_b, pre: {"step:assemble/first": true}}
processes:
  - name: flow
    steps:
      - {name: finish, method: assembling, pre: {c: true}}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let ag = c.agent("assemble").expect("method-generated agent");
    assert!(
        ag.process_of() == "assemble" && ag.planner == PLANNER_GOAP,
        "{ag:?}"
    );
    let g = c.goal("assemble").expect("method-generated goal");
    assert_eq!(g.pre.get("b"), Some(&true), "{g:?}");
    let acts = c.step_actions("assemble");
    assert_eq!(acts.len(), 2, "method step actions: {acts:?}");
    let me = c.method_by_name("assemble").expect("method");
    assert_eq!(me.actor_agent(), "assemble");
    let bb = Blackboard::default();
    let choices = c.methods_for("assembling", &bb);
    assert!(
        choices.len() == 1 && choices[0].name == "assemble" && choices[0].agent_goal == "assemble",
        "choices: {choices:?}"
    );
    let a = c.action("flow/finish").unwrap();
    assert!(
        a.call
            .as_ref()
            .is_some_and(|x| x.capability == "assembling"),
        "flow step dispatches to the capability: {a:?}"
    );
}

#[test]
fn method_steps_validation() {
    for (name, yaml, want) in [
        (
            "done with steps",
            "\nmethods: [{name: m, for: f, done: {c: true}, steps: [{name: s, action: do_a}]}]",
            "done applies",
        ),
        (
            "unknown action with steps",
            "\nmethods: [{name: m, for: f, actions: [nobody], steps: [{name: s, action: do_a}]}]",
            "unknown action",
        ),
        (
            "neither actions nor steps",
            "\nmethods: [{name: m, for: f}]",
            "declare the actions",
        ),
        (
            "llm planner needs a model",
            "\nmethods: [{name: m, for: f, planner: llm, actions: [do_c], done: {c: true}}]",
            "model is required",
        ),
        (
            "name collides with an agent",
            "\nmethods: [{name: worker, for: f, steps: [{name: s, action: do_a}]}]",
            "already an agent",
        ),
        (
            "name collides with a process",
            "\nmethods: [{name: flow, for: f, steps: [{name: s, action: do_a}]}]\nprocesses: [{name: flow, steps: [{name: s, action: do_a}]}]",
            "already a process",
        ),
        (
            "name collides with a goal",
            "\nmethods: [{name: all, for: f, steps: [{name: s, action: do_a}]}]",
            "already a goal",
        ),
        (
            "a method step may not name a capability",
            "\nmethods:\n  - {name: inner, for: g, actions: [do_c], done: {c: true}}\n  - name: outer\n    for: f\n    steps: [{name: s, method: g}]",
            r#"no valid method provides "g""#,
        ),
    ] {
        let (_, issues) = compile_process(yaml);
        assert!(
            issues.to_string().contains(want),
            "{name}: want {want:?} in {issues}"
        );
    }
}

// The agent applying a method is generated from it.
#[test]
fn method_generates_its_agent() {
    let (c, issues) = compile_process(
        r#"
roles: [{name: builder}]
methods:
  - name: assemble
    for: assembling
    planner: hybrid
    roles: {responsible: builder}
    actions: [do_c]
    steps:
      - {name: first, action: do_a}
      - {name: second, action: do_b}
processes:
  - name: flow
    steps:
      - {name: finish, method: assembling}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let ag = c.agent("assemble").expect("agent");
    assert!(
        ag.planner == PLANNER_HYBRID
            && ag.role == "builder"
            && ag.goals.contains(&"assemble".to_string())
            && ag.actions.contains(&"assemble/first".to_string())
            && ag.actions.contains(&"do_c".to_string()),
        "agent: {ag:?}"
    );
    let ls = c.check_levels("assemble").unwrap_or_default();
    assert!(
        ls.len() == 3
            && ls[0].agent == "assemble"
            && ls[0].goal == "assemble"
            && ls[0].steps.len() == 2
            && ls[1].kind == LEVEL_ACTION,
        "levels: {ls:?}"
    );
}

// Among the methods whose context holds, the highest priority wins, then the most specific context.
#[test]
fn methods_rank_by_specificity() {
    let (c, issues) = compile_process(
        r#"
methods:
  - {name: generic, for: f, actions: [do_c], done: {c: true}}
  - {name: one, for: f, actions: [do_c], done: {c: true}, when: 'artifacts.exists(x, x.target == "u")'}
  - {name: two, for: f, actions: [do_c], done: {c: true}, when: 'artifacts.exists(x, x.target == "u") && artifacts.exists(x, x.target == "v")'}
  - {name: forced, for: f, actions: [do_c], done: {c: true}, priority: 1, when: 'artifacts.exists(x, x.target == "u")'}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let mut bb = Blackboard::default();
    bb.change.items = vec![
        ChangeItem {
            id: "1".into(),
            r#type: TYPE_ARTIFACT,
            target: "u".into(),
            ..ChangeItem::default()
        },
        ChangeItem {
            id: "2".into(),
            r#type: TYPE_ARTIFACT,
            target: "v".into(),
            ..ChangeItem::default()
        },
    ];
    let names: Vec<String> = c
        .methods_for("f", &bb)
        .iter()
        .map(|m| m.name.clone())
        .collect();
    assert_eq!(names, vec!["forced", "two", "one", "generic"]);
}
