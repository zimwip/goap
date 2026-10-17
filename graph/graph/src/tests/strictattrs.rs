//! Port of `attrcheck_test.go` and `strictattrs_test.go`: the checks of attribute values, and attributes are strict
//! (ADR 0055).

use std::sync::Arc;

use goap_domain::*;
use goap_graphapi::{NodeCreate, NodeUpdate};
use serde_json::{Value, json};

use crate::algorithms::check_attribute_value;
use crate::lifecycle::TypeIndex;
use crate::testsupport::*;
use crate::{ErrorKind, Graph, NewChange};

// Go: TestCheckAttributeValue
#[test]
fn check_attribute_value_by_type() {
    let typed = |t: &str| AttributeCheck {
        r#type: t.into(),
        ..Default::default()
    };
    let en = AttributeCheck {
        name: "p".into(),
        r#type: "enum".into(),
        r#enum: "prio".into(),
        values: vec!["low".into(), "high".into()],
    };
    let cases: Vec<(AttributeCheck, Value, bool)> = vec![
        (typed("string"), json!("x"), true),
        (typed("string"), json!(3), false),
        (typed("number"), json!(3.5), true),
        (typed("number"), json!("12"), true),
        (typed("number"), json!("abc"), false),
        (typed("boolean"), json!(true), true),
        (typed("boolean"), json!("true"), false),
        (typed("date"), json!("2026-10-04"), true),
        (typed("date"), json!("yesterday"), false),
        (en.clone(), json!("low"), true),
        (en.clone(), json!("mid"), false),
        (en.clone(), Value::Null, true),
        (en, json!(""), true),
        (AttributeCheck::default(), json!([1]), true),
        (typed("json"), json!({"a": 1}), true),
    ];
    for (a, v, ok) in cases {
        let r = check_attribute_value(&a, &v);
        assert_eq!(r.is_ok(), ok, "{a:?} {v}: {r:?}");
    }
}

/// strictGraph is a graph judged by a catalogue with a closed type, a subtype of it, and a free-form type.
async fn strict_graph() -> (Graph, Baseline) {
    let d = goap_def::parse_domain(
        br"
name: shop
version: 1.0.0
nodeTypes:
  - {name: Item, attributes: [title]}
  - {name: Book, extends: Item, attributes: [isbn]}
  - {name: Bag, additionalProperties: true, attributes: [label]}
",
    )
    .expect("domain");
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    let mut g = memory_repo().graph();
    use_types(&mut g, Arc::new(cat));
    let b = must(g.branch_head(&ctx(), "shop", MAIN_BRANCH).await);
    (g, b)
}

fn node(typ: &str) -> Node {
    Node {
        key: "K".into(),
        r#type: typ.into(),
        ..Default::default()
    }
}

// A property that is no attribute of the type of a node, ancestors' included, is refused (ErrInvalid), unless the
// type is free-form; a type the catalogue does not know, and the untyped graph, stay permissive.
#[tokio::test]
async fn check_attributes_is_strict() {
    let (g, _) = strict_graph().await;
    let ix = TypeIndex { cat: g.catalog() };
    let check = |typ: &str, p: serde_json::Map<String, Value>| ix.check_attributes(&node(typ), &p);

    must(check("shop@Item", props(&[("title", json!("a"))])));
    must(check(
        "shop@Book",
        props(&[("title", json!("a")), ("isbn", json!("1"))]),
    ));
    let err = check(
        "shop@Item",
        props(&[("title", json!("a")), ("colour", json!("red"))]),
    )
    .unwrap_err();
    assert!(
        err.is_invalid()
            && err
                .to_string()
                .contains(r#"property "colour" is not an attribute of shop@Item"#),
        "an unknown property: {err}"
    );
    assert_kind(
        check("shop@Item", props(&[("isbn", json!("1"))])),
        ErrorKind::Invalid,
        "an attribute of a subtype is no attribute of its supertype",
    );
    must(check(
        "shop@Bag",
        props(&[("label", json!("a")), ("anything", json!(1))]),
    ));
    must(check("shop@Nope", props(&[("anything", json!(1))])));
    must(
        TypeIndex { cat: None }
            .check_attributes(&node("shop@Item"), &props(&[("anything", json!(1))])),
    );
    // the landing check (validateProps) holds a version to it too
    assert_kind(
        g.validate_props(
            &ctx(),
            &ix,
            &node("shop@Item"),
            &props(&[("colour", json!("red"))]),
        )
        .await,
        ErrorKind::Invalid,
        "validateProps",
    );
    must(
        g.validate_props(
            &ctx(),
            &ix,
            &node("shop@Book"),
            &props(&[("isbn", json!("1"))]),
        )
        .await,
    );
}

// Creating a node and editing its working version go through the same rule: through the operations of a change.
#[tokio::test]
async fn impact_node_operations_are_strict() {
    let ctx = ctx();
    let (g, b) = strict_graph().await;
    let c = must(
        g.create_change(
            &ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: "shop".into(),
                title: "t".into(),
                baseline_id: b.id,
                ..Default::default()
            },
        )
        .await,
    );
    let create = |key: &str, typ: &str, p| NodeCreate {
        rationale: "r".into(),
        key: key.into(),
        r#type: typ.into(),
        properties: p,
        ..Default::default()
    };
    let err = g
        .impact_node_create(
            &ctx,
            &c.id,
            create("I1", "shop@Item", props(&[("colour", json!("red"))])),
        )
        .await
        .unwrap_err();
    assert!(
        err.is_invalid() && err.to_string().contains("not an attribute"),
        "create with an unknown property: {err}"
    );
    let cn = must(
        g.impact_node_create(
            &ctx,
            &c.id,
            create(
                "B1",
                "shop@Book",
                props(&[("title", json!("t")), ("isbn", json!("1"))]),
            ),
        )
        .await,
    );
    let err = g
        .impact_node_update(
            &ctx,
            &c.id,
            &cn.id,
            NodeUpdate {
                properties: props(&[("colour", json!("red"))]),
                ..Default::default()
            },
        )
        .await
        .unwrap_err();
    assert!(
        err.is_invalid() && err.to_string().contains("not an attribute"),
        "update with an unknown property: {err}"
    );
    must(
        g.impact_node_update(
            &ctx,
            &c.id,
            &cn.id,
            NodeUpdate {
                properties: props(&[("title", json!("u"))]),
                ..Default::default()
            },
        )
        .await,
    );
    must(
        g.impact_node_create(
            &ctx,
            &c.id,
            create(
                "G1",
                "shop@Bag",
                props(&[("label", json!("l")), ("whatever", json!(true))]),
            ),
        )
        .await,
    );
}
