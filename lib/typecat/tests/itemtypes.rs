//! Port of itemtypes_test.go.

use goap_def::{Domain, ItemType, Schema};
use goap_typecat::{builtin, is_builtin};

/// Every type of item the code knows (the change mechanism, the platform use cases) is declared by a built-in domain:
/// a domain is where an item type lives, the code only gives it a meaning (ADR 0128).
#[test]
fn code_item_types_are_declared() {
    goap_risk::register();
    goap_assess::register();
    goap_verify::register();
    let c = builtin();
    let mut all = goap_domain::core_item_types();
    all.extend(goap_domain::registered_item_types());
    let mut missing = Vec::new();
    for typ in all {
        match c.item_type(typ.as_str()) {
            None => missing.push(format!("item type {typ}: no built-in domain declares it")),
            Some(x) if x.description.is_empty() || !is_builtin(&x.domain) => {
                missing.push(format!("item type {typ}: {x:?}"))
            }
            Some(_) => {}
        }
    }
    assert!(missing.is_empty(), "{}", missing.join("\n"));
}

/// An item type name is one across the domains in force.
#[test]
fn item_type_names_are_unique() {
    let mut d = Domain {
        name: "itemtest".into(),
        version: "1.0.0".into(),
        schema: Schema {
            item_types: vec![ItemType {
                name: "artifact".into(),
                ..ItemType::default()
            }],
            ..Schema::default()
        },
        ..Domain::default()
    };
    let err = goap_typecat::new(&[d.clone()]).unwrap_err();
    assert!(err.is_invalid(), "a name a built-in domain declares: {err}");
    d.item_types[0].name = "testResult".into();
    let c = goap_typecat::new(&[d]).unwrap();
    let x = c.item_type("testResult").unwrap();
    assert!(
        x.domain == "itemtest" && x.version == "1.0.0",
        "declared: {x:?}"
    );
}
