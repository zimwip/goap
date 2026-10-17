use super::{load, parse};
use crate::CompileOptions;

#[test]
fn hints_warn_when_nothing_can_start() {
    for (name, src, want) in [
        (
            "a first step",
            r#"
name: ok
namespace: alm
goal: p
conditions: [{name: a, expr: "false"}]
actions: [{name: x, kind: human, effects: {a: true}}]
processes: [{name: p, steps: [{name: first, action: x}]}]
"#,
            "",
        ),
        (
            "every step waits",
            r#"
name: stuck
namespace: alm
goal: p
conditions: [{name: a, expr: "false"}, {name: b, expr: "false"}, {name: c, expr: "false"}]
actions:
  - {name: x, kind: human, pre: {c: true}, effects: {a: true}}
  - {name: y, kind: human, pre: {a: true}, effects: {b: true}}
processes: [{name: p, steps: [{name: one, action: x}, {name: two, action: y}]}]
"#,
            "a, c",
        ),
        (
            "declared agent, no process",
            r#"
name: plain
namespace: alm
goal: g
conditions: [{name: a, expr: "false"}, {name: b, expr: "false"}]
actions: [{name: x, kind: human, pre: {a: true}, effects: {b: true}}]
goals: [{name: g, pre: {b: true}}]
"#,
            "first: a",
        ),
    ] {
        let c = parse(src).compile().unwrap();
        let hints = c.hints();
        if want.is_empty() {
            assert!(hints.is_empty(), "{name}: {hints:?}");
        } else {
            assert!(
                hints.len() == 1 && hints[0].path == "goal" && hints[0].message.contains(want),
                "{name}: {hints:?}"
            );
        }
    }
}

// The shipped methodologies can start on an empty change.
#[test]
fn shipped_methodologies_have_no_hint() {
    for f in [
        "sdlc",
        "methodology-improvement",
        "examples/impact-analysis",
        "examples/test-design",
        "examples/option-decision",
    ] {
        let c = load(&format!("../../methodologies/{f}.yaml"))
            .compile()
            .unwrap();
        let h = c.hints();
        assert!(h.is_empty(), "{f}: {h:?}");
    }
}

const STUCK_SRC: &str = r#"
name: stuck
namespace: alm
goal: p
conditions: [{name: a, expr: "false"}, {name: c, expr: "false"}]
actions: [{name: x, kind: human, pre: {c: true}, effects: {a: true}}]
processes: [{name: p, steps: [{name: one, action: x}]}]
"#;

// A hint is a warning of the compile: lenient and strict builds carry it, nothing is refused for it.
#[test]
fn hint_is_a_warning_that_does_not_block() {
    let m = parse(STUCK_SRC);
    m.compile().expect("a warning must not refuse the compile");
    for (name, issues) in [("validate", m.validate()), ("stored", m.validate_stored())] {
        assert!(
            !issues.has_errors()
                && issues.warnings().0.len() == 1
                && issues.warnings().0[0].path == "goal"
                && issues.0[0].is_warning(),
            "{name}: {issues}"
        );
    }
    let (lc, issues) = m.compile_lenient();
    assert!(
        lc.is_some() && !issues.has_errors() && issues.warnings().0.len() == 1,
        "lenient: {issues}"
    );
}

// With an error the hints are left out and Compile refuses with the errors only.
#[test]
fn errors_refuse_and_keep_their_severity() {
    let m = parse(&STUCK_SRC.replacen("goal: p", "goal: nope", 1));
    assert!(m.compile().is_err(), "an unknown goal must refuse");
    let (_, issues) = m.compile_with(CompileOptions {
        lenient: true,
        ..CompileOptions::default()
    });
    assert!(
        issues.has_errors()
            && issues.warnings().0.is_empty()
            && !issues.0[0].is_warning()
            && issues.0[0].severity.is_empty(),
        "{issues}"
    );
}

#[test]
fn shipped_methodologies_have_no_warning() {
    for f in ["sdlc", "methodology-improvement", "risk-management"] {
        let w = load(&format!("../../methodologies/{f}.yaml"))
            .validate()
            .warnings();
        assert!(w.0.is_empty(), "{f}: {w}");
    }
}
