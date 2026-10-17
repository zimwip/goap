//! Port of lifecycle_test.go.

use goap_def::*;

const LIFECYCLE_DOMAIN: &str = r#"
name: docs
version: 1.0.0
lifecycles:
  - name: requirement
    initial: draft
    states:
      - {name: draft, notLandable: true}
      - {name: approved}
      - {name: obsolete, final: true}
    transitions:
      - {name: approve, from: draft, to: approved, permission: "requirement:approve"}
      - {name: reopen, from: approved, to: draft}
      - {name: retire, from: approved, to: obsolete}
  - name: spec
    initial: draft
    states: [{name: draft, notLandable: true}, {name: released}]
    transitions:
      - {name: release, from: draft, to: released, children: {states: [approved]}}
nodeTypes:
  - {name: Requirement, lifecycle: requirement}
  - {name: FunctionalRequirement, extends: Requirement}
  - {name: Spec, lifecycle: spec, document: {contains: [Requirement]}}
  - {name: Note, changeControlled: false}
linkTypes:
  - {name: contains, from: Spec, to: Requirement}
"#;

#[test]
fn lifecycle_parses_and_inherits() {
    let d = parse_domain(LIFECYCLE_DOMAIN.as_bytes()).unwrap();
    let is = d.validate();
    assert!(is.is_empty(), "unexpected issues: {is}");
    let l = d.lifecycle_of("FunctionalRequirement");
    assert!(
        l.is_some_and(|l| l.initial == "draft" && !l.landable("draft") && l.landable("approved")),
        "subtype must inherit the lifecycle: {l:?}"
    );
    assert!(d.lifecycle_of("Note").is_none(), "Note has no lifecycle");
    assert!(
        std::ptr::eq(
            d.lifecycle_of("Requirement").unwrap(),
            d.lifecycle("requirement").unwrap()
        ) && d.lifecycle_of("Spec").unwrap().name == "spec",
        "a type resolves the lifecycle it names"
    );
    assert!(
        !d.node_types[3].is_change_controlled() && d.node_types[0].is_change_controlled(),
        "changeControlled defaults to true"
    );
    // YAML round trip
    let y = d.yaml().unwrap();
    let d2 = parse_domain(y.as_bytes()).unwrap_or_else(|e| panic!("round trip: {e}\n{y}"));
    assert!(
        d2.lifecycle_of("Requirement").is_some()
            && d2.node_types[2].document.is_some()
            && d2.lifecycles.len() == 2,
        "round trip: {d2:?}"
    );
    assert_eq!(d2, d);
}

#[test]
fn lifecycle_validation() {
    let bad = [
        (
            "unknown initial",
            "{initial: nope, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}",
        ),
        (
            "no landable state",
            "{initial: a, states: [{name: a, notLandable: true}, {name: b, notLandable: true}], transitions: [{name: t, from: a, to: b}]}",
        ),
        (
            "unknown target",
            "{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: zz}]}",
        ),
        (
            "trapped not landable",
            "{initial: a, states: [{name: a, notLandable: true}, {name: b}]}",
        ),
        (
            "leaves a final",
            "{initial: a, states: [{name: a, notLandable: true}, {name: b, final: true}], transitions: [{name: t, from: a, to: b}, {name: u, from: b, to: a}]}",
        ),
        (
            "duplicate state",
            "{initial: a, states: [{name: a, notLandable: true}, {name: a}], transitions: [{name: t, from: a, to: a}]}",
        ),
        (
            "final not landable",
            "{initial: a, states: [{name: a}, {name: b, notLandable: true, final: true}], transitions: [{name: t, from: a, to: b}]}",
        ),
        (
            "duplicate transition",
            "{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b}, {name: t, from: a, to: b}]}",
        ),
        (
            "bad guard",
            r#"{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b, guard: "node.props.("}]}"#,
        ),
    ];
    for (name, lc) in bad {
        let src = format!(
            "name: x\nversion: 1\nlifecycles:\n  - {{name: l, {}\nnodeTypes:\n  - {{name: T, lifecycle: l}}\n",
            lc.strip_prefix('{').unwrap()
        );
        let d = parse_domain(src.as_bytes()).unwrap_or_else(|e| panic!("{name}: {e}"));
        assert!(!d.validate().is_empty(), "{name}: must be rejected");
    }
    // a lifecycle that flags nothing is valid: every state is landable, the initial one included
    let d = parse_domain(b"name: x\nversion: 1\nlifecycles:\n  - {name: l, initial: a, states: [{name: a}, {name: b}], transitions: [{name: t, from: a, to: b}]}\nnodeTypes:\n  - {name: T, lifecycle: l}\n").unwrap();
    let is = d.validate();
    assert!(is.is_empty(), "all states landable: {is}");
    // references and documents
    const OK: &str = "{initial: a, states: [{name: a, notLandable: true}, {name: b}], transitions: [{name: t, from: a, to: b}]}";
    let ok = OK.strip_prefix('{').unwrap();
    let base = format!(
        "name: x\nversion: 1\nlifecycles:\n  - {{name: c, {ok}\n  - {{name: d, initial: a, states: [{{name: a, notLandable: true}}, {{name: b}}], transitions: [{{name: t, from: a, to: b, children: {{states: [zz]}}}}]}}\nnodeTypes:\n  - {{name: C, lifecycle: c}}\n"
    );
    for (name, td) in [
        ("unknown lifecycle", "  - {name: D, lifecycle: nope}\n"),
        (
            "unknown child type",
            "  - {name: D, document: {contains: [Nope]}}\n",
        ),
        ("children on non-doc", "  - {name: D, lifecycle: d}\n"),
        (
            "unknown child state",
            "  - {name: D, lifecycle: d, document: {contains: [C]}}\n",
        ),
        (
            "lifecycle no control",
            "  - {name: D, lifecycle: c, changeControlled: false}\n",
        ),
    ] {
        let d = parse_domain(format!("{base}{td}").as_bytes())
            .unwrap_or_else(|e| panic!("{name}: {e}"));
        assert!(!d.validate().is_empty(), "{name}: must be rejected");
    }
    let dup = parse_domain(
        format!(
            "name: x\nversion: 1\nlifecycles:\n  - {{name: c, {ok}\n  - {{name: c, {ok}\nnodeTypes:\n  - {{name: T}}\n"
        )
        .as_bytes(),
    )
    .unwrap();
    assert!(
        !dup.validate().is_empty(),
        "duplicate lifecycle names must be rejected"
    );
}

/// The vetos and objectives of a gate compile like a guard; each needs a name (unique in its list) and an expression
/// (ADR 0075 §3).
#[test]
fn gate_criteria_issues() {
    const SRC: &str = r#"
name: g
version: 1.0.0
lifecycles:
  - name: maturity
    initial: a
    states: [{name: a, notLandable: true}, {name: b}]
    transitions:
      - name: go
        from: a
        to: b
        vetos: [{name: safe, expr: 'world["safe"]'}, %s]
        objectives: [{name: docs, expr: 'world["docs"]'}, %s]
nodeTypes:
  - {name: Note}
"#;
    let issues = |veto: &str, objective: &str| {
        let src = SRC.replacen("%s", veto, 1).replacen("%s", objective, 1);
        parse_domain(src.as_bytes()).unwrap().validate()
    };
    let is = issues(
        r#"{name: other, expr: "true"}"#,
        r#"{name: more, expr: "true"}"#,
    );
    assert!(is.is_empty(), "valid: {is}");
    for (name, (veto, objective)) in [
        (
            "unnamed veto",
            (r#"{expr: "true"}"#, r#"{name: more, expr: "true"}"#),
        ),
        (
            "duplicate veto",
            (
                r#"{name: safe, expr: "true"}"#,
                r#"{name: more, expr: "true"}"#,
            ),
        ),
        (
            "no expression",
            ("{name: other}", r#"{name: more, expr: "true"}"#),
        ),
        (
            "bad objective",
            (
                r#"{name: other, expr: "true"}"#,
                r#"{name: more, expr: "1 +"}"#,
            ),
        ),
        (
            "unknown variable",
            (
                r#"{name: other, expr: "nope"}"#,
                r#"{name: more, expr: "true"}"#,
            ),
        ),
        (
            "objective duplicate",
            (
                r#"{name: other, expr: "true"}"#,
                r#"{name: docs, expr: "true"}"#,
            ),
        ),
    ] {
        let is = issues(veto, objective);
        assert!(is.len() == 1, "{name}: issues {is}");
    }
}
