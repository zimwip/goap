use std::sync::Arc;

use super::parse;

#[test]
fn subtyping() {
    let mut d = goap_def::parse_domain(
        br#"
name: sec
version: "1.0.0"
nodeTypes:
  - {name: Requirement}
  - {name: SecurityRequirement, extends: Requirement}
  - {name: CryptoRequirement, extends: SecurityRequirement}
"#,
    )
    .unwrap();
    let m = parse(
        r#"
name: st
version: "1.0.0"
namespace: sec
conditions: [{name: c, expr: 'changeImpacts.exists(n, "sec@Requirement" in n.types)'}]
actions: [{name: a, kind: human, effects: {c: true}}]
goals: [{name: g, pre: {c: true}}]
"#,
    );
    let m = m.resolve(Arc::new(goap_def::domain_types(std::slice::from_ref(&d))));
    let is = m.validate();
    assert!(is.0.is_empty(), "{is}");
    assert_eq!(
        m.supertypes()["sec@CryptoRequirement"],
        vec!["sec@SecurityRequirement", "sec@Requirement"]
    );
    d.schema.node_types[0].extends = "CryptoRequirement".into();
    assert!(!d.validate().0.is_empty(), "cycle not reported");
    d.schema.node_types[0].extends = "Nope".into();
    let is = d.validate();
    assert!(
        !is.0.is_empty() && is.0[0].path == "nodeTypes[0].extends",
        "unknown parent: {is}"
    );
    d.schema.node_types[0].extends = "other@Base".into();
    let is = d.validate();
    assert!(
        is.0.is_empty(),
        "a type of another domain is resolved by the type catalogue: {is}"
    );
}
