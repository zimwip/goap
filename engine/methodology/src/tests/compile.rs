use super::parse;
use crate::{
    Action, CompileOptions, Condition, DEFAULT_TOOL_ROUNDS, Goal, KIND_HUMAN, KIND_LLM, KIND_TOOL,
    Loop, MAX_LOOP_ROUNDS, MAX_LOOP_TIMEOUT, Methodology,
};

fn conds(list: &[(&str, bool)]) -> crate::Conds {
    list.iter().map(|(k, v)| (k.to_string(), *v)).collect()
}

// The one pipeline: strict builds nothing with issues, lenient builds what stands, stored adds the namespace rule
// without changing the others.
#[test]
fn compile_with_options() {
    let mut m = Methodology {
        name: "m".into(),
        version: "1".into(),
        conditions: vec![Condition {
            name: "done".into(),
            expr: "false".into(),
            ..Condition::default()
        }],
        goals: vec![Goal {
            name: "g".into(),
            pre: conds(&[("done", true)]),
            ..Goal::default()
        }],
        actions: vec![
            Action {
                name: "work".into(),
                kind: KIND_HUMAN.into(),
                instructions: "x".into(),
                effects: conds(&[("done", true)]),
                ..Action::default()
            },
            Action {
                name: "broken".into(),
                kind: KIND_HUMAN.into(),
                instructions: "x".into(),
                pre: conds(&[("nope", true)]),
                effects: conds(&[("done", true)]),
                ..Action::default()
            },
        ],
        ..Methodology::default()
    };
    let (c, issues) = m.compile_with(CompileOptions::default());
    assert!(c.is_none() && issues.0.len() == 1, "strict: {issues}");
    let (c, issues) = m.compile_with(CompileOptions {
        lenient: true,
        ..CompileOptions::default()
    });
    assert!(c.is_some() && issues.0.len() == 1, "lenient: {issues}");
    let issues = m.validate_stored();
    assert!(
        issues.0.len() == 2 && issues.0[0].path == "namespace",
        "stored: {issues}"
    );
    let (c, issues) = m.compile_with(CompileOptions {
        lenient: true,
        stored: true,
    });
    assert!(
        c.is_some() && issues.0.len() == 2,
        "lenient stored: {issues}"
    );
    m.namespace = "alm".into();
    m.actions.truncate(1);
    let (c, issues) = m.compile_with(CompileOptions {
        stored: true,
        ..CompileOptions::default()
    });
    assert!(c.is_some() && issues.0.is_empty(), "valid: {issues}");
}

#[test]
fn loop_is_checked() {
    let m = Methodology {
        name: "m".into(),
        version: "1".into(),
        namespace: "alm".into(),
        actions: vec![
            Action {
                name: "code".into(),
                kind: KIND_LLM.into(),
                prompt: "x".into(),
                effects: conds(&[("done", true)]),
                r#loop: Some(Loop {
                    max_rounds: 200,
                    timeout: "2h".into(),
                    ..Loop::default()
                }),
                ..Action::default()
            },
            Action {
                name: "bad".into(),
                kind: KIND_LLM.into(),
                prompt: "x".into(),
                effects: conds(&[("bad", true)]),
                r#loop: Some(Loop {
                    max_rounds: MAX_LOOP_ROUNDS + 1,
                    timeout: "soon".into(),
                    ..Loop::default()
                }),
                ..Action::default()
            },
            Action {
                name: "tool".into(),
                kind: KIND_TOOL.into(),
                tool: "workspace/bash".into(),
                effects: conds(&[("built", true)]),
                r#loop: Some(Loop {
                    max_rounds: 3,
                    ..Loop::default()
                }),
                ..Action::default()
            },
        ],
        goals: vec![Goal {
            name: "g".into(),
            pre: conds(&[("done", true)]),
            ..Goal::default()
        }],
        ..Methodology::default()
    };
    let (_, issues) = m.compile_lenient();
    let got: Vec<String> = issues
        .0
        .iter()
        .filter(|is| is.path.ends_with(".loop"))
        .map(|is| format!("{}: {}", is.path, is.message))
        .collect();
    let want = [
        "actions[1].loop: loop.maxRounds must be between 0 and 500",
        "actions[1].loop: loop.timeout \"soon\" is not a duration (45m, 2h)",
        "actions[2].loop: loop is the budget of the tool loop of an llm action",
    ];
    assert_eq!(got.join("\n"), want.join("\n"));
    assert_eq!(
        Loop::rounds(Some(&Loop {
            max_rounds: 200,
            ..Loop::default()
        })),
        200
    );
    assert_eq!(Loop::rounds(None), DEFAULT_TOOL_ROUNDS);
    assert_eq!(
        Loop::duration(Some(&Loop {
            timeout: "9h".into(),
            ..Loop::default()
        })),
        MAX_LOOP_TIMEOUT
    );
}

// Two scoped actions of the same scope chain in a plan (ADR 0118).
#[test]
fn scoped_actions_chain() {
    let m = parse(
        r#"
name: chain
version: 1.0.0
namespace: alm
conditions:
  - {name: written, scope: impact, expr: 'artifacts.exists(a, a.target == "w" && a.data.impact == self.key)'}
  - {name: checked, scope: impact, expr: 'artifacts.exists(a, a.target == "c" && a.data.impact == self.key)'}
  - {name: all_written, all: written}
  - {name: all_checked, all: checked}
actions:
  - {name: write, kind: human, scope: impact, effects: {written: true}}
  - {name: check, kind: human, scope: impact, pre: {written: true}, effects: {checked: true}}
goals:
  - {name: done, pre: {all_checked: true}}
"#,
    );
    let c = m.compile().unwrap();
    let w = c.action("write").unwrap();
    assert!(
        w.effects.get("pending:check") == Some(&true)
            && w.lifted.get("pending:check") == Some(&true)
            && w.effects.get("all_written") == Some(&true),
        "write {:?}",
        w.effects
    );
    let ch = c.action("check").unwrap();
    assert!(
        !ch.effects.contains_key("pending:write"),
        "check {:?}",
        ch.effects
    );
}
