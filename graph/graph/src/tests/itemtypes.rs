//! Port of `itemtypes_test.go`: the graph stores only the items of a type a domain in force declares, and checks the
//! keys of their data the type declares as attributes; the other keys stay free (ADR 0128).

use std::sync::Arc;

use goap_def::{ATTR_ENUM, ATTR_NUMBER, Attribute, Domain, Enum, EnumValue, ItemType, Schema};
use goap_domain::{ChangeItem, TYPE_ARTIFACT};

use crate::testsupport::*;
use crate::{ErrorKind, NewChange};

fn declare_domain(g: &mut crate::Graph, d: Domain) {
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    use_types(g, Arc::new(cat));
}

fn item(typ: &str, data: serde_json::Value) -> ChangeItem {
    ChangeItem {
        r#type: goap_domain::ItemType::new(typ),
        data: data.as_object().cloned().unwrap_or_default(),
        ..Default::default()
    }
}

#[tokio::test]
async fn item_types_declared_by_domains() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let f = new_fixture(r).await;
        let mut g = f.g;
        let ch = must(
            g.create_change(
                &c,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "t".into(),
                    baseline_id: f.base.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        // untyped graph: the built-in domains declare the types of the change mechanism and of the use cases
        let mut art = item(TYPE_ARTIFACT.as_str(), serde_json::json!({}));
        art.target = "note".into();
        must(g.add_items(&c, &ch.id, vec![art]).await);
        let e = g
            .add_items(&c, &ch.id, vec![item("testResult", serde_json::json!({}))])
            .await
            .expect_err("an undeclared type");
        assert!(
            e.kind() == ErrorKind::Invalid
                && e.to_string().contains("no domain in force declares it"),
            "an undeclared type: {e}"
        );
        declare_domain(
            &mut g,
            Domain {
                name: "itemtest".into(),
                version: "1.0.0".into(),
                schema: Schema {
                    enums: vec![Enum {
                        name: "verdict".into(),
                        values: vec![
                            EnumValue {
                                value: "pass".into(),
                                ..Default::default()
                            },
                            EnumValue {
                                value: "fail".into(),
                                ..Default::default()
                            },
                        ],
                        ..Default::default()
                    }],
                    item_types: vec![ItemType {
                        name: "testResult".into(),
                        description: "The result of a test run".into(),
                        attributes: vec![
                            Attribute {
                                name: "verdict".into(),
                                r#type: ATTR_ENUM.into(),
                                r#enum: "verdict".into(),
                                ..Default::default()
                            },
                            Attribute {
                                name: "count".into(),
                                r#type: ATTR_NUMBER.into(),
                                ..Default::default()
                            },
                        ],
                    }],
                    ..Default::default()
                },
                ..Default::default()
            },
        );
        must(
            g.add_items(
                &c,
                &ch.id,
                vec![item(
                    "testResult",
                    serde_json::json!({"verdict": "pass", "count": 3, "free": true}),
                )],
            )
            .await,
        );
        for data in [
            serde_json::json!({"verdict": "maybe"}),
            serde_json::json!({"count": "many"}),
        ] {
            assert_kind(
                g.add_items(&c, &ch.id, vec![item("testResult", data.clone())])
                    .await,
                ErrorKind::Invalid,
                &format!("data {data}"),
            );
        }
        let found = g
            .item_types()
            .into_iter()
            .find(|x| x.name == "testResult")
            .expect("listed");
        assert_eq!(found.domain, "itemtest");
        assert_eq!(found.description, "The result of a test run");
        assert_eq!(found.attributes.len(), 2);
        assert_eq!(found.attributes[0].values.len(), 2);
    })
    .await;
}
