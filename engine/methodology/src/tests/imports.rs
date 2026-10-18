use super::parse;

// a methodology whose goal needs risks_under_control, a condition of the risks library.
fn library_yaml(imports: &str, extra: &str) -> String {
    format!(
        r#"
name: lib
version: 1.0.0
namespace: alm
{imports}
conditions:
  - {{name: done, expr: 'artifacts.exists(a, a.target == "x")'}}
{extra}
actions:
  - {{name: do, kind: human, effects: {{done: true}}}}
goals:
  - {{name: g, pre: {{done: true, risks_under_control: true}}}}
"#
    )
}

// The conditions of a library exist only in a methodology that imports it (ADR 0064).
#[test]
fn imports_add_only_the_libraries_asked() {
    let m = parse(&library_yaml("", ""));
    match m.compile() {
        Err(e) if e.to_string().contains("risks_under_control") => {}
        other => panic!("an unimported library condition must be unknown, got {other:?}"),
    }
    let m = parse(&library_yaml("imports: [risks]", ""));
    let c = m.compile().unwrap();
    assert!(
        c.conditions.has("risks_under_control") && !c.conditions.has("open_questions"),
        "conditions {:?}",
        c.conditions.names()
    );
}

#[test]
fn imports_refuse_an_unknown_library() {
    let m = parse(&library_yaml("imports: [risks, nope, risks]", ""));
    let issues = m.validate();
    let paths: Vec<&str> = issues.0.iter().map(|i| i.path.as_str()).collect();
    assert!(
        paths.contains(&"imports[1]") && paths.contains(&"imports[2]"),
        "issues {issues}"
    );
}

// A condition the methodology declares replaces the one of the library.
#[test]
fn declared_condition_overrides_the_library() {
    let m = parse(&library_yaml(
        "imports: [risks]",
        "  - {name: risks_under_control, expr: 'false'}",
    ));
    let c = m.compile().unwrap();
    assert_eq!(c.conditions.exprs()["risks_under_control"], "false");
}

#[test]
fn imports_yaml_round_trip() {
    let m = parse(&library_yaml("imports: [risks, decisions]", ""));
    let out = m.yaml().unwrap();
    let m2 = parse(&out);
    assert_eq!(m2.imports, vec!["risks", "decisions"], "{out}");
}
