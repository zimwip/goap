use std::sync::Arc;

use super::{load, parse};
use crate::{ORACLE_MODEL, Verify};

const VERIFY_METHODOLOGY: &str = r#"
name: m
conditions: [{name: c, expr: "true"}]
actions:
  - {name: a, kind: human, instructions: x, effects: {c: true}, verify: {oracle: %s}}
  - {name: b, kind: human, instructions: x, effects: {c: true}, verify: {oracle: human, independent: false}}
goals: [{name: g, pre: {c: true}}]"#;

// verify parses from YAML, independent defaults to true when verify is set, and an unknown oracle is an issue.
#[test]
fn action_verify() {
    let m = parse(&VERIFY_METHODOLOGY.replacen("%s", "model", 1));
    let is = m.validate();
    assert!(is.0.is_empty(), "issues: {is}");
    let (a, b) = (&m.actions[0], &m.actions[1]);
    let av = a.verify.as_ref().expect("a.verify");
    assert!(av.oracle == ORACLE_MODEL && Verify::is_independent(Some(av)));
    let bv = b.verify.as_ref().expect("b.verify");
    assert!(!Verify::is_independent(Some(bv)));
    assert!(!Verify::is_independent(None), "no verify, no independence");

    let bad = parse(&VERIFY_METHODOLOGY.replacen("%s", "oracle9", 1));
    let is = bad.validate();
    assert!(
        is.0.len() == 1
            && is.0[0].path == "actions[0].verify.oracle"
            && is.0[0].message.contains(r#"unknown oracle "oracle9""#),
        "issues: {is}"
    );
    assert!(
        bad.compile().is_err(),
        "compile must refuse an unknown oracle"
    );
}

// The example that applies the expiry of the derogations compiles and names a known builtin (ADR 0075 §2).
#[test]
fn derogation_expiry_example() {
    let m = load("../../methodologies/examples/derogation-expiry.yaml")
        .with_builtins(Arc::new(goap_builtins::Known));
    m.compile().unwrap();
}

// A methodology names a default criticality: C1, C2 or C3, anything else is an issue (ADR 0075 §3).
#[test]
fn methodology_criticality() {
    const SRC: &str = r#"
name: m
criticality: %s
conditions: [{name: c, expr: "true"}]
actions: [{name: a, kind: human, instructions: x, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]"#;
    let ok = parse(&SRC.replacen("%s", "C3", 1));
    let is = ok.validate();
    assert!(is.0.is_empty() && ok.criticality == "C3", "C3: {is}");
    let bad = parse(&SRC.replacen("%s", "C9", 1));
    let is = bad.validate();
    assert!(
        is.0.len() == 1 && is.0[0].path == "criticality",
        "issues: {is}"
    );
    let none = parse(&SRC.replacen("criticality: %s\n", "", 1));
    let is = none.validate();
    assert!(is.0.is_empty(), "none: {is}");
}

// The main goal of a methodology is a declared goal or a process; unset, the first goal, else the first process.
#[test]
fn methodology_main_goal() {
    const HEAD: &str = r#"
name: m
conditions: [{name: c, expr: "true"}, {name: d, expr: "true"}]
actions: [{name: a, kind: human, instructions: x, effects: {c: true, d: true}}]
"#;
    let compile = |src: &str| parse(&format!("{HEAD}{src}")).compile();
    let c = compile("goals: [{name: g1, pre: {c: true}}, {name: g2, pre: {d: true}}]\ngoal: g2")
        .unwrap();
    assert_eq!(c.definition().main_goal(), "g2");
    let c = compile("goals: [{name: g1, pre: {c: true}}, {name: g2, pre: {d: true}}]").unwrap();
    assert_eq!(c.definition().main_goal(), "g1");
    let is = parse(&format!(
        "{HEAD}goals: [{{name: g1, pre: {{c: true}}}}]\ngoal: nope"
    ))
    .validate();
    assert!(
        is.0.len() == 1 && is.0[0].path == "goal",
        "unknown goal: {is}"
    );
    let m = crate::Methodology {
        processes: vec![crate::Process {
            name: "p1".into(),
            ..crate::Process::default()
        }],
        ..crate::Methodology::default()
    };
    assert_eq!(m.main_goal(), "p1");
    assert_eq!(crate::Methodology::default().main_goal(), "");
    let c = compile("processes: [{name: p1, steps: [{name: s, action: a}]}]\ngoal: p1").unwrap();
    assert_eq!(c.definition().main_goal(), "p1");
}
