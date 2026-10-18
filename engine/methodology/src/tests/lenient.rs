use std::collections::HashMap;

use super::{PROCESS_BASE, parse};
use crate::{CompileOptions, Method, Methodology, Process, Step};

// A draft with broken rules still compiles as far as it stands.
#[test]
fn compile_lenient_keeps_what_stands() {
    let m = parse(&format!(
        "{PROCESS_BASE}{}",
        r#"
methods:
  - {name: by_worker, for: working, actions: [do_c], done: {c: true}}
  - {name: empty, for: working}
processes:
  - name: flow
    steps:
      - {name: first, action: do_a}
      - {name: broken, action: nope}
      - name: phase
        steps:
          - {name: deep, action: do_b}
          - {name: worse, method: missing}
      - {name: work, method: working, foreach: '1 + 1'}
"#
    ));
    let (_, strict) = m.compile_with(CompileOptions::default());
    assert!(!strict.0.is_empty(), "the draft is broken");
    let (c, issues) = m.compile_lenient();
    let c = c.expect("compiled");
    assert!(!issues.0.is_empty());
    let by_activity: HashMap<&str, &str> = issues
        .0
        .iter()
        .map(|is| (is.activity.as_str(), is.message.as_str()))
        .collect();
    for want in ["flow/broken", "flow/phase/worse", "flow/work", "empty"] {
        assert!(
            by_activity.contains_key(want),
            "no issue tied to {want}: {issues}"
        );
    }
    // what stands is drawn
    let g = c.process_graph("flow").expect("graph");
    assert!(!g.steps.is_empty());
    assert!(c.check_levels("flow").is_some(), "levels");
    for name in ["by_worker", "empty"] {
        c.process_graph(name);
        c.check_levels(name);
    }
}

#[test]
fn activity_of() {
    let step = |name: &str, steps: Vec<Step>| Step {
        name: name.into(),
        steps,
        ..Step::default()
    };
    let m = Methodology {
        processes: vec![Process {
            name: "flow".into(),
            steps: vec![
                step("a", vec![]),
                step("b", vec![step("x", vec![]), step("y", vec![])]),
            ],
            ..Process::default()
        }],
        methods: vec![Method {
            name: "meth".into(),
            steps: vec![step("s", vec![])],
            ..Method::default()
        }],
        ..Methodology::default()
    };
    for (path, want) in [
        ("processes[0].steps[1].steps[1].pre", "flow/b/y"),
        ("processes[0].steps[0]", "flow/a"),
        ("processes[0].name", "flow"),
        ("methods[0].steps[0].action", "meth/s"),
        ("methods[0].actions[2]", "meth"),
        ("processes[4].steps[0]", ""),
        ("agents[0].name", ""),
    ] {
        assert_eq!(m.activity_of(path), want, "{path}");
    }
}
