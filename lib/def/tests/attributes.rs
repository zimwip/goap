//! Port of attributes_test.go.

use goap_algo::{USAGE_LINK_VALIDATOR, USAGE_NODE_VALIDATOR, USAGE_PROPERTY_VALIDATOR};
use goap_def::*;

fn domain_issues(src: &str) -> String {
    let d = parse_domain(src.as_bytes()).unwrap();
    d.validate()
        .iter()
        .map(|i| format!("{}: {}", i.path, i.message))
        .collect::<Vec<_>>()
        .join("\n")
}

#[test]
fn attribute_shorthand_and_validators() {
    let d = parse_domain(
        br#"
name: m
version: "1"
enums:
  - {name: prio, values: [low, {value: high, label: High}]}
nodeTypes:
  - name: A
    attributes: [title, {name: level, type: enum, enum: prio, validators: [v1]}]
    validators: [n1]
  - {name: B, extends: A, attributes: [{name: extra, validators: [v1]}]}
algorithms:
  - {name: pv, type: property_validator, language: javascript, code: "return"}
  - {name: nv, type: node_validator, language: javascript, code: "return"}
algorithmInstances:
  - {name: v1, algorithm: pv}
  - {name: n1, algorithm: nv}
"#,
    )
    .unwrap();
    let got = &d.node_types[0].attributes;
    assert!(
        got.len() == 2 && got[0].name == "title" && got[1].default_widget() == WIDGET_DROPDOWN,
        "attributes: {got:?}"
    );
    assert_eq!(d.enums[0].values[1].label, "High");
    let is = d.validate();
    assert!(is.is_empty(), "valid domain: {is:?}");
    // the supertype's validators first: its attributes', its node validators, then the subtype's
    let bs = d.bound_validators("B");
    assert!(
        bs.len() == 3
            && bs[0].property == "level"
            && bs[1].r#type == USAGE_NODE_VALIDATOR
            && bs[2].property == "extra",
        "bound validators: {bs:?}"
    );
}

#[test]
fn attribute_issues() {
    let got = domain_issues(
        r#"
name: m
version: "1"
enums: [{name: e}, {name: e, values: [a, a]}]
nodeTypes:
  - name: A
    attributes:
      - {name: x, type: nope}
      - {name: x}
      - {name: y, widget: nope}
      - {name: z, type: enum}
      - {name: w, type: enum, enum: missing}
      - {name: v, enum: e}
      - {name: n1, asName: true}
      - {name: n2, asName: true}
      - {name: bad-name}
    validators: [ghost]
"#,
    );
    for want in [
        "enums[0].values: an enum has at least one value",
        "enums[1].name: duplicate enum e",
        "enums[1].values[1].value: duplicate value a",
        "attributes[0].type: unknown type",
        "attributes[1].name: duplicate attribute x",
        "attributes[2].widget: unknown widget",
        "attributes[3].enum: an enum attribute names its enum",
        "attributes[4].enum: unknown enum missing",
        "attributes[5].enum: only an enum attribute",
        "attributes: at most one attribute is the name",
        "attributes[8].name: name must be",
        "validators[0]: unknown algorithm instance ghost",
    ] {
        assert!(got.contains(want), "missing issue {want:?} in:\n{got}");
    }
}

#[test]
fn link_validators_plug_and_bind() {
    let src = r#"
name: m
version: "1"
nodeTypes: [{name: A}]
algorithms:
  - {name: pv, type: property_validator, language: javascript, code: "return"}
  - {name: lv, type: link_validator, language: javascript, code: "return"}
algorithmInstances:
  - {name: v1, algorithm: pv}
  - {name: l1, algorithm: lv}
linkTypes:
  - {name: r, from: A, to: A, validators: [l1], attributes: [{name: w, validators: [v1]}]}
"#;
    let d = parse_domain(src.as_bytes()).unwrap();
    let is = d.validate();
    assert!(is.is_empty(), "valid domain: {is:?}");
    assert!(
        d.has_algorithms(),
        "a link type plugging validators has algorithms"
    );
    let (attrs, links) = d.own_bound_link_validators(&d.link_types[0]);
    assert!(
        attrs.len() == 1
            && attrs[0].property == "w"
            && attrs[0].r#type == USAGE_PROPERTY_VALIDATOR
            && links.len() == 1
            && links[0].r#type == USAGE_LINK_VALIDATOR,
        "bound: {attrs:?} {links:?}"
    );
    // a plug of the wrong type, or of an unknown instance, is an issue
    let got = domain_issues(&src.replacen(
        "validators: [l1], attributes: [{name: w, validators: [v1]}]",
        "validators: [v1, ghost], attributes: [{name: w, validators: [l1]}]",
        1,
    ));
    for want in [
        "linkTypes[0].validators[0]: instance v1 is of type property_validator, expected link_validator",
        "linkTypes[0].validators[1]: unknown algorithm instance ghost",
        "linkTypes[0].attributes[0].validators[0]: instance l1 is of type link_validator, expected property_validator",
    ] {
        assert!(got.contains(want), "missing {want:?} in\n{got}");
    }
}
