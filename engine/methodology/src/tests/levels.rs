use super::compile_process;
use crate::{GAP_EXTERNAL, GAP_INNER, LEVEL_ACTION, LEVEL_METHOD, LevelCheck, METHOD_VARIANT};

fn level_of(ls: &[LevelCheck], path: &str) -> LevelCheck {
    ls.iter()
        .find(|l| l.path == path)
        .cloned()
        .unwrap_or_else(|| panic!("no level {path} in {ls:?}"))
}

fn levels(yaml: &str, name: &str) -> Vec<LevelCheck> {
    let (c, issues) = compile_process(yaml);
    assert!(!issues.has_errors(), "{issues}");
    c.unwrap().check_levels(name).unwrap_or_default()
}

#[test]
fn check_levels_chains_inputs_to_outputs() {
    let ls = levels(
        r#"
processes:
  - name: flow
    steps:
      - name: phase
        pre: {c: false}
        steps:
          - {name: first, action: do_a}
          - {name: second, action: do_b}
      - {name: last, action: do_c, pre: {b: true}}
"#,
        "flow",
    );
    // the process and its phase, and below each leaf step the action that does it as its internal step
    assert_eq!(ls.len(), 5, "levels {ls:?}");
    let a = level_of(&ls, "flow/last");
    assert!(
        a.kind == LEVEL_ACTION && a.steps.len() == 1 && a.steps[0].name == "do_c" && a.ok(),
        "a step done by an action opens on it: {a:?}"
    );
    let root = level_of(&ls, "flow");
    assert!(
        root.ok()
            && root.order.len() == 2
            && root.order[0].name == "phase"
            && root.order[1].name == "last"
            && root.order[1].layer == 1,
        "root: {root:?}"
    );
    let phase = level_of(&ls, "flow/phase");
    assert!(
        phase.ok() && phase.order[0].name == "first" && phase.order[1].name == "second",
        "phase: {phase:?}"
    );
}

#[test]
fn check_levels_finds_a_cycle_and_a_missing_input() {
    let (c, issues) = compile_process(
        r#"
processes:
  - name: loop
    steps:
      - {name: x, action: do_a, done: {a: true}, pre: {b: true}}
      - {name: y, action: do_b, done: {b: true}}
  - name: missing
    steps:
      - name: phase
        pre: {a: true}
        steps:
          - {name: only, action: do_c, pre: {b: true}}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let ls = c.check_levels("loop").unwrap();
    let root = level_of(&ls, "loop");
    assert!(!root.ok(), "nothing starts: {root:?}");
    assert!(
        root.gaps[0].message.contains("cycle"),
        "gaps {:?}",
        root.gaps
    );
    // what a sub-step needs and nothing gives is demanded of the context, level after level, up to the root
    let ls = c.check_levels("missing").unwrap();
    let phase = level_of(&ls, "missing/phase");
    let root = level_of(&ls, "missing");
    assert!(
        phase.ok() && phase.gaps.is_empty() && phase.inputs.get("b") == Some(&true),
        "phase: {phase:?}"
    );
    assert!(
        root.gaps.len() == 1
            && root.gaps[0].kind == GAP_EXTERNAL
            && root.gaps[0].missing.contains(&"b".to_string()),
        "root: {root:?}"
    );
}

// A condition a sub-step takes from a sibling of its parent is chained at the level above, not reported as missing.
#[test]
fn check_levels_lifts_what_a_sub_step_takes_from_a_sibling() {
    let ls = levels(
        r#"
processes:
  - name: lift
    steps:
      - name: analysis
        steps:
          - {name: impacts, action: do_a}
          - name: specification
            steps:
              - {name: requirements, action: do_b}
"#,
        "lift",
    );
    for l in &ls {
        assert!(l.ok() && l.gaps.is_empty(), "{}: {l:?}", l.path);
    }
    let an = level_of(&ls, "lift/analysis");
    assert!(
        an.order[0].name == "impacts" && an.order[1].name == "specification",
        "specification waits for what impacts makes: {:?}",
        an.order
    );
}

// A step not resolved inside is flagged in the level above, which then cannot run through it.
#[test]
fn check_levels_surfaces_an_incomplete_sub_step() {
    let ls = levels(
        r#"
processes:
  - name: inner
    steps:
      - name: phase
        steps:
          - {name: x, action: do_a, done: {a: true}, pre: {b: true}}
          - {name: y, action: do_b, done: {b: true}}
      - {name: after, action: do_c}
"#,
        "inner",
    );
    let root = level_of(&ls, "inner");
    assert!(
        !root.ok()
            && root.steps.len() == 2
            && root.steps[0].broken
            && root.steps[0].composite
            && !root.steps[1].broken,
        "root: {root:?}"
    );
    assert!(
        root.gaps
            .iter()
            .any(|g| g.kind == GAP_INNER && g.step == "phase"),
        "gaps {:?}",
        root.gaps
    );
}

// A method of actions has, as its level, the actions its agent carries out; an action itself is a node.
#[test]
fn check_levels_down_to_the_actions() {
    let (c, issues) = compile_process(
        r#"
methods:
  - name: by_agent
    for: working
    actions: [do_c]
    done: {c: true}
processes:
  - name: flow
    steps:
      - {name: work, method: working}
"#,
    );
    assert!(!issues.has_errors(), "{issues}");
    let c = c.unwrap();
    let ms = c.check_levels("by_agent").expect("method level");
    assert!(
        ms.len() == 1
            && ms[0].kind == LEVEL_METHOD
            && ms[0].agent == "by_agent"
            && !ms[0].steps.is_empty()
            && ms[0].steps[0].name == "do_c",
        "method level: {ms:?}"
    );
    let g = c.process_graph("by_agent").expect("method graph");
    assert_eq!(g.process, "by_agent");
}

// A step naming a capability opens on the methods that specialize it, not on the process around it.
#[test]
fn check_levels_of_a_capability_step_lists_its_methods() {
    let ls = levels(
        r#"
methods:
  - name: by_agent
    for: working
    actions: [do_c]
    done: {c: true}
  - name: by_steps
    for: working
    steps:
      - {name: one, action: do_c}
processes:
  - name: flow
    steps:
      - {name: work, method: working}
"#,
        "flow",
    );
    let lvl = level_of(&ls, "flow/work");
    assert!(
        lvl.steps.len() == 2
            && lvl.steps[0].method == METHOD_VARIANT
            && lvl.steps[0].name == "by_agent"
            && lvl.steps[0].exit.get("c") == Some(&true),
        "capability level: {lvl:?}"
    );
    let root = level_of(&ls, "flow");
    assert!(
        root.steps[0].composite && root.steps[0].sub_steps == 2,
        "root: {:?}",
        root.steps
    );
}
