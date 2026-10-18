use std::cell::RefCell;

use chrono::TimeDelta;
use goap_access as access;
use goap_authz::Policy;
use goap_criticality as criticality;
use goap_llmcfg as llmcfg;
use serde::Deserialize;
use serde::de::{self, Visitor};

/// The serialized names of the fields of a struct deriving `Deserialize` (Go's `jsonKeys` over the JSON tags): read
/// from what the derive asks of a deserializer (`deserialize_struct`'s fields), skipped fields left out.
fn json_keys<T: for<'de> Deserialize<'de>>() -> Vec<String> {
    let got = RefCell::new(Vec::new());
    let _ = T::deserialize(Fields(&got));
    got.into_inner()
}

struct Fields<'a>(&'a RefCell<Vec<String>>);

#[derive(Debug)]
struct Stop;

impl std::fmt::Display for Stop {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("stop")
    }
}

impl std::error::Error for Stop {}

impl de::Error for Stop {
    fn custom<T: std::fmt::Display>(_: T) -> Self {
        Stop
    }
}

impl<'de> de::Deserializer<'de> for Fields<'_> {
    type Error = Stop;

    fn deserialize_any<V: Visitor<'de>>(self, _: V) -> Result<V::Value, Stop> {
        Err(Stop)
    }

    fn deserialize_struct<V: Visitor<'de>>(
        self,
        _: &'static str,
        fields: &'static [&'static str],
        _: V,
    ) -> Result<V::Value, Stop> {
        self.0
            .borrow_mut()
            .extend(fields.iter().map(|f| f.to_string()));
        Err(Stop)
    }

    serde::forward_to_deserialize_any! {
        bool i8 i16 i32 i64 i128 u8 u16 u32 u64 u128 f32 f64 char str string bytes byte_buf option unit
        unit_struct newtype_struct seq tuple tuple_struct map enum identifier ignored_any
    }
}

/// The properties the platform writes on the nodes of the built-in domains (seeds, sign-in, the settings of the web)
/// are attributes of their types (strict attributes): the nodes built from fully populated values carry no other key,
/// and the structs that become properties through their JSON carry no field the type does not declare.
#[test]
fn builtin_node_props_are_attributes() {
    let cat = goap_typecat::builtin();
    let keys =
        |m: serde_json::Map<String, serde_json::Value>| m.keys().cloned().collect::<Vec<_>>();
    let cases: Vec<(&str, Vec<String>)> = vec![
        // built by hand: every key the constructors can put
        (
            access::NODE_TYPE_USER,
            keys(
                access::User {
                    subject: "s".into(),
                    display_name: "n".into(),
                    email: "e@x.y".into(),
                    locale: "en".into(),
                    ..Default::default()
                }
                .props(),
            ),
        ),
        (
            access::NODE_TYPE_POLICY,
            keys(access::policy_props(&Policy::new("r", "x", "y", "allow"))),
        ),
        (
            access::NODE_TYPE_ASSIGNMENT,
            keys(
                access::Assignment {
                    roles: vec!["r".into()],
                    description: "d".into(),
                }
                .props(),
            ),
        ),
        (
            access::NODE_TYPE_PROJECT_UNIT,
            keys(
                access::ProjectUnit {
                    name: "n".into(),
                    description: "d".into(),
                    kind: "k".into(),
                    status: "s".into(),
                    methodologies: vec!["m".into()],
                }
                .props(),
            ),
        ),
        (
            access::NODE_TYPE_CRITICALITY_POLICY,
            keys(access::criticality_props(
                &criticality::C2,
                &criticality::Policy {
                    oracles: vec!["tool".into()],
                    sampling: true,
                    signatory_role: "r".into(),
                    max_derogation: TimeDelta::hours(1),
                },
            )),
        ),
        (
            access::NODE_TYPE_ROLE,
            keys(
                access::Role {
                    name: "n".into(),
                    description: "d".into(),
                }
                .props(),
            ),
        ),
        (
            access::NODE_TYPE_ORG_UNIT,
            ["name", "kind", "description", access::PROP_WAITING_UNIT]
                .map(String::from)
                .to_vec(),
        ),
        // structs written through their JSON
        (
            goap_domain::TYPE_ADAPTER_DEF,
            json_keys::<goap_adapter::Def>(),
        ),
        // the unit is the owner of the node, not a property (Instance.props)
        (
            "organisation@Adapter",
            json_keys::<goap_adapter::Instance>()
                .into_iter()
                .filter(|k| k != "unit")
                .collect(),
        ),
        ("platform@MCP", json_keys::<goap_mcp::Def>()),
        (llmcfg::NODE_TYPE_PROVIDER, json_keys::<llmcfg::Provider>()),
        (llmcfg::NODE_TYPE_MODEL, json_keys::<llmcfg::Model>()),
        (llmcfg::NODE_TYPE_ALIAS, json_keys::<llmcfg::Alias>()),
        (llmcfg::NODE_TYPE_BEHAVIOR, json_keys::<llmcfg::Behavior>()),
    ];
    let mut errors = Vec::new();
    for (typ, ks) in cases {
        assert!(!ks.is_empty(), "{typ}: no key read");
        let (names, open) = cat.attribute_names(typ);
        if open || names.is_empty() {
            errors.push(format!(
                "{typ}: no closed type in the catalogue (names {names:?}, open {open})"
            ));
            continue;
        }
        for k in ks {
            if !names.contains(&k) {
                errors.push(format!(
                    "{typ}: the platform writes {k:?}, which is no attribute of the type"
                ));
            }
        }
    }
    assert!(errors.is_empty(), "{}", errors.join("\n"));
}
