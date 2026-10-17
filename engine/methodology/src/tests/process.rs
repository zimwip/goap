use goap_domain::{Blackboard, ChangeItem, TYPE_ARTIFACT};

use super::{PROCESS_BASE, compile_process, has_issue, parse};
use crate::{
    CompileOptions, GraphAgent, KIND_CALL, KIND_HUMAN, METHOD_CAPABILITY, Methodology, Role,
    step_condition,
};

#[test]
fn process_compiles_to_step_actions() {
    let (c, issues) = compile_process(
        r#"
methods:
  - {name: by_worker, for: working, actions: [do_c], done: {c: true}}
processes:
  - name: flow
    references: [{title: Guide, ref: "document-repository:flow.md"}]
    steps:
      - name: phase
        pre: {d: true}
        steps:
          - {name: first, action: do_a}
          - {name: other, actions: [do_c, do_a], pre: {"step:flow/manual": true}}
          - {name: second, action: do_b}
      - {name: agentic, method: working, pre: {b: true}}
      - name: manual
        description: Sign off
        references: [{title: Checklist, ref: "doc:CHK-1", section: "2"}]
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let ag = c.agent("flow").expect("no agent for the process");
    assert_eq!(ag.process_of(), "flow");
    let names: Vec<String> = c.agent_actions(ag).iter().map(|a| a.name.clone()).collect();
    assert_eq!(
        names.join(","),
        "flow/phase/first,flow/phase/other:do_c,flow/phase/other:do_a,flow/phase/second,flow/agentic,flow/manual"
    );
    // no implicit order: a step needs what the phase needs, its own entry conditions and its action's preconditions
    let first = c.action("flow/phase/first").unwrap();
    assert!(
        first.pre.len() == 1 && first.pre.get("d") == Some(&true),
        "the first step only inherits the phase's entry: {:?}",
        first.pre
    );
    let second = c.action("flow/phase/second").unwrap();
    assert!(
        second.implements == "do_b"
            && second.pre.get("a") == Some(&true)
            && second.pre.get("d") == Some(&true)
            && second.pre.len() == 2
            && second.declared() == "do_b",
        "an action step keeps its action's preconditions, not the steps before it: {second:?}"
    );
    let other = c.action("flow/phase/other:do_a").unwrap();
    assert!(
        other.pre.get(&step_condition("flow/manual")) == Some(&true)
            && other.effects.get("c") == Some(&true)
            && other.effects.get("a") == Some(&true)
            && other.declared() == "do_a",
        "a step may wait for a later step; each alternative reaches the step's criteria: {other:?}"
    );
    let agentic = c.action("flow/agentic").unwrap();
    assert!(
        agentic.kind == KIND_CALL
            && agentic
                .call
                .as_ref()
                .is_some_and(|x| x.capability == "working")
            && agentic.pre.len() == 1
            && agentic.pre.get("b") == Some(&true)
            && agentic.effects.get("c") == Some(&true),
        "a capability step: {agentic:?}"
    );
    let manual = c.action("flow/manual").unwrap();
    assert!(
        manual.kind == KIND_HUMAN
            && manual.instructions == "Sign off"
            && manual.effects.get(&step_condition("flow/manual")) == Some(&true)
            && manual.pre.is_empty(),
        "a manual step is a human task, done once submitted: {manual:?}"
    );
    let info = c.step_by_path("flow/manual").expect("step");
    assert!(
        info.references.len() == 1
            && info.references[0].to_string() == "Checklist (doc:CHK-1), 2"
            && info.planned[0] == "flow/manual",
        "the compiled step keeps its references: {info:?}"
    );
    let info = c.step_by_path("flow/phase").expect("phase");
    assert!(
        info.steps.len() == 3 && info.exit.get("b") == Some(&true) && !info.entry.contains_key("c"),
        "a phase has its sub-steps, entry and exit: {info:?}"
    );
    let g = c.goal("flow").expect("goal");
    assert!(
        g.pre.get(&step_condition("flow/manual")) == Some(&true) && g.pre.get("c") == Some(&true),
        "the goal of the process is every step done: {g:?}"
    );
    assert!(
        c.conditions.has(&step_condition("flow/manual")),
        "the condition of the manual step is not compiled"
    );
    // the declared agents do not plan with the step actions
    let w = c.agent("worker").unwrap();
    for a in c.agent_actions(w) {
        assert!(
            !a.name.contains('/'),
            "worker plans with a step: {}",
            a.name
        );
    }
    let list = c.agent_list();
    assert!(list.len() == 3 && list[1].name == "flow", "agents {list:?}");
}

#[test]
fn process_validation() {
    for (name, yaml, want) in [
        (
            "two methods",
            "\nprocesses:\n  - name: x\n    steps: [{name: s, action: do_a, method: working}]",
            "one thing",
        ),
        (
            "unknown action",
            "\nprocesses:\n  - name: x\n    steps: [{name: s, action: nope}]",
            r#"unknown action "nope""#,
        ),
        (
            "unknown step condition",
            "\nprocesses:\n  - name: x\n    steps: [{name: s, action: do_a, pre: {\"step:x/nope\": true}}]",
            r#"unknown condition "step:x/nope""#,
        ),
        (
            "undeclared role",
            "\nroles: [{name: dev}]\nprocesses:\n  - name: x\n    steps: [{name: s, roles: {responsible: dev, accountable: boss}}]",
            r#"unknown role "boss""#,
        ),
        (
            "unknown operation",
            "\nroles: [{name: reviewer, operations: [reviewer, impact.approve]}]\nprocesses:\n  - name: x\n    steps: [{name: s, roles: {responsible: reviewer}}]",
            r#"unknown operation "impact.approve""#,
        ),
        (
            "reference without document",
            "\nprocesses:\n  - name: x\n    steps: [{name: s, references: [{title: Guide}]}]",
            "names its document",
        ),
        (
            "nesting itself",
            "\nprocesses:\n  - name: x\n    steps: [{name: s, process: y}]\n  - name: y\n    steps: [{name: s, process: x}]",
            "nests itself",
        ),
        (
            "unknown process",
            "\nprocesses:\n  - name: x\n    steps: [{name: s, process: nope}]",
            r#"unknown process "nope""#,
        ),
        (
            "name of an agent",
            "\nprocesses:\n  - name: worker\n    steps: [{name: s}]",
            "already an agent",
        ),
        (
            "conflicting conditions",
            "\nprocesses:\n  - name: x\n    steps: [{name: t, action: do_b, pre: {a: false}}]",
            "both true and false",
        ),
        (
            "no steps",
            "\nprocesses:\n  - name: x\n    steps: []",
            "at least one step",
        ),
    ] {
        let (_, issues) = compile_process(yaml);
        assert!(
            issues.to_string().contains(want),
            "{name}: want {want:?} in {issues}"
        );
    }
}

#[test]
fn process_only_methodology() {
    let m = parse(
        r#"
name: q
version: 1.0.0
namespace: alm
processes:
  - name: checklist
    steps: [{name: one}, {name: two, process: other/flow, pre: {"step:checklist/one": true}}]
"#,
    );
    let c = m.compile().unwrap();
    let list = c.agent_list();
    assert!(
        list.len() == 1 && list[0].name == "checklist",
        "a methodology of processes only has their agents: {list:?}"
    );
    let two = c.action("checklist/two").unwrap();
    assert!(
        two.kind == KIND_CALL
            && two
                .call
                .as_ref()
                .is_some_and(|x| x.methodology == "other" && x.agent == "flow" && x.goal == "flow")
            && two.pre.get(&step_condition("checklist/one")) == Some(&true),
        "nested process of another methodology: {two:?}"
    );
}

#[test]
fn nested_process_is_done_by_its_criteria() {
    let (c, issues) = compile_process(
        r#"
processes:
  - name: outer
    steps:
      - {name: inner_run, process: inner}
      - {name: last, action: do_b}
  - name: inner
    steps:
      - {name: first, action: do_a}
      - {name: sign}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let run = c.action("outer/inner_run").unwrap();
    assert!(
        run.effects.get("a") == Some(&true)
            && run.effects.get(&step_condition("inner/sign")) == Some(&true)
            && run.pre.is_empty(),
        "a nested process of the methodology is done by its exit criteria: {run:?}"
    );
    // what a nested process needs from outside is the entry of the step nesting it
    let (c2, issues) = compile_process(
        r#"
processes:
  - name: outer
    steps: [{name: run, process: needy}]
  - name: needy
    steps:
      - {name: b_step, action: do_b}
      - {name: c_step, action: do_c, pre: {b: true}}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let r = c2.unwrap().action("outer/run").unwrap().clone();
    assert!(
        r.pre.len() == 1 && r.pre.get("a") == Some(&true),
        "prerequisites of needy: {:?}",
        r.pre
    );
    let outer = c.agent("outer").unwrap();
    let names: Vec<String> = c
        .agent_actions(outer)
        .iter()
        .map(|a| a.name.clone())
        .collect();
    assert_eq!(names.join(","), "outer/inner_run,outer/last");
    let list = c.agent_list();
    assert!(
        list[1].name == "outer" && list[2].name == "inner",
        "process agents in declaration order: {list:?}"
    );
}

#[test]
fn methods_provide_a_capability_per_context() {
    let (c, issues) = compile_process(
        r#"
methods:
  - name: quick
    for: finishing
    actions: [do_c]
    done: {c: true}
    priority: 5
    when: 'artifacts.exists(x, x.target == "urgent")'
    guidance: Do it quickly
    references: [{ref: "doc:QUICK"}]
  - {name: thorough, for: finishing, actions: [do_c], done: {c: true}, description: Do it well}
processes:
  - name: flow
    steps:
      - {name: finish, method: finishing, pre: {a: true}}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let a = c.action("flow/finish").unwrap();
    assert!(
        a.kind == KIND_CALL
            && a.call.as_ref().is_some_and(|x| x.capability == "finishing")
            && a.effects.get("c") == Some(&true)
            && a.pre.get("a") == Some(&true),
        "a method step runs the chosen method: {a:?}"
    );
    let mut bb = Blackboard::default();
    let got = c.methods_for("finishing", &bb);
    assert!(
        got.len() == 1 && got[0].name == "thorough" && got[0].agent_goal == "thorough",
        "outside its context a method does not apply: {got:?}"
    );
    bb.change.items = vec![ChangeItem {
        id: "i1".into(),
        r#type: TYPE_ARTIFACT,
        target: "urgent".into(),
        ..ChangeItem::default()
    }];
    let got = c.methods_for("finishing", &bb);
    assert!(
        got.len() == 2 && got[0].name == "quick",
        "the highest priority applicable method comes first: {got:?}"
    );
    let info = c.step_by_path("flow/finish").unwrap();
    assert!(
        info.method() == METHOD_CAPABILITY && info.capability == "finishing",
        "step {info:?}"
    );
}

#[test]
fn method_validation() {
    for (name, yaml, want) in [
        (
            "no actions nor steps",
            "\nmethods: [{name: m, for: f}]\nprocesses: [{name: x, steps: [{name: s, method: f}]}]",
            "declare the actions",
        ),
        (
            "actions without done",
            "\nmethods: [{name: m, for: f, actions: [do_c]}]",
            "states what it reaches",
        ),
        (
            "unknown action",
            "\nmethods: [{name: m, for: f, actions: [nope], done: {c: true}}]",
            r#"unknown action "nope""#,
        ),
        (
            "unknown capability",
            "\nmethods: [{name: m, for: f, actions: [do_c], done: {c: true}}]\nprocesses: [{name: x, steps: [{name: s, method: g}]}]",
            r#"no valid method provides "g""#,
        ),
        (
            "bad context",
            "\nmethods: [{name: m, for: f, actions: [do_c], done: {c: true}, when: \"nope(\"}]\nprocesses: [{name: x, steps: [{name: s, method: f}]}]",
            "methods[0].when",
        ),
    ] {
        let (_, issues) = compile_process(yaml);
        assert!(
            issues.to_string().contains(want),
            "{name}: want {want:?} in {issues}"
        );
    }
}

#[test]
fn process_graph_draws_the_conditions() {
    let c = super::load("../../methodologies/sdlc.yaml")
        .compile()
        .unwrap();
    let g = c.process_graph("software_delivery").expect("no graph");
    let edges: std::collections::HashMap<String, String> = g
        .edges
        .iter()
        .map(|e| (format!("{} -> {}", e.from, e.to), e.conditions.join(",")))
        .collect();
    for (from, to) in [
        (
            "software_delivery/framing/review",
            "software_delivery/framing/confirmation",
        ),
        (
            "software_delivery/framing/confirmation",
            "software_delivery/analysis/impacts",
        ),
        (
            "software_delivery/analysis/impacts",
            "software_delivery/analysis/specification/requirements",
        ),
        (
            "software_delivery/analysis/specification/traceability",
            "software_delivery/design",
        ),
        ("software_delivery/release", "software_delivery/application"),
    ] {
        assert!(
            edges.contains_key(&format!("{from} -> {to}")),
            "no edge {from} -> {to} in {edges:?}"
        );
    }
    // transitive reduction: framing does not point at the application directly
    assert!(
        !edges.contains_key(
            "software_delivery/framing/confirmation -> software_delivery/application"
        ),
        "implied edge kept"
    );
    assert!(
        g.methods.len() == 6
            && g.methods[0].agent_goal == "description_rating"
            && g.methods[1].agent_goal == "impact_analysis"
            && g.methods[2].agent_goal == "solution_design",
        "methods of the capabilities: {:?}",
        g.methods
    );
    let architect: Option<&GraphAgent> = g.agents.iter().find(|a| a.name == "solution_design");
    assert!(
        architect.is_some_and(|a| !a.actions.is_empty()),
        "the agents of the methods, with their actions: {:?}",
        g.agents
    );
}

// A condition cannot be both an input and an output of an action or a step, whatever its values.
#[test]
fn an_input_is_never_also_an_output() {
    for (name, yaml, path, activity) in [
        ("action guard", "\n", "actions[3].pre.d", ""),
        (
            "step through its action",
            "\nprocesses:\n  - name: flow\n    steps:\n      - {name: allocate, action: guarded}\n",
            "processes[0].steps[0].pre.d",
            "flow/allocate",
        ),
        (
            "step entry and exit",
            "\nprocesses:\n  - name: flow\n    steps:\n      - {name: first, action: do_a, pre: {a: false}}\n",
            "processes[0].steps[0].done.a",
            "flow/first",
        ),
    ] {
        let src = PROCESS_BASE.replacen(
            "goals:",
            "  - {name: guarded, kind: human, pre: {d: false}, effects: {d: true}}\ngoals:",
            1,
        ) + yaml;
        let m = parse(&src);
        let (_, issues) = m.compile_with(CompileOptions::default());
        assert!(
            has_issue(&issues, path, "both an input and an output"),
            "{name}: want an issue at {path}: {issues}"
        );
        if !activity.is_empty() {
            let (_, issues) = m.compile_lenient();
            assert!(
                issues.0.iter().any(|is| is.activity == activity),
                "{name}: no issue tied to {activity}: {issues}"
            );
        }
    }
}

// The roles of a methodology list the operations they allow (ADR 0103).
#[test]
fn operation_roles() {
    let role = |name: &str, ops: &[&str]| Role {
        name: name.into(),
        operations: ops.iter().map(|s| s.to_string()).collect(),
        ..Role::default()
    };
    let m = Methodology {
        roles: vec![
            role("author", &["doer"]),
            role("reviewer", &["impact.review"]),
            role("lead", &[]),
        ],
        ..Methodology::default()
    };
    let (roles, restricted) = m.operation_roles("impact.review");
    assert!(
        restricted && roles.join(",") == "reviewer,lead",
        "{roles:?}"
    );
    let (roles, _) = m.operation_roles("impact.withdraw");
    assert_eq!(roles.join(","), "author,lead");
    let (_, restricted) = Methodology {
        roles: vec![role("dev", &[])],
        ..Methodology::default()
    }
    .operation_roles("impact.withdraw");
    assert!(
        !restricted,
        "no role lists an operation: nothing is restricted"
    );
}
