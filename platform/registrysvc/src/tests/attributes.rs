//! attributes_test.go: every property the registry writes is an attribute of its type of the built-in meta-domain.

use std::cell::RefCell;

use goap_methodology::{Action, Agent, Condition, Goal, Method, Methodology, Process, Role, Step};
use serde::de::{self, Deserialize, Visitor};

use crate::graphstore::TYPE_METHODOLOGY_VERSION;

thread_local! {
    static FIELDS: RefCell<Vec<&'static str>> = const { RefCell::new(Vec::new()) };
}

/// A deserializer that only records the JSON names of the fields of the struct asked of it (Go's reflection over
/// the json tags).
struct Capture;

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

impl<'de> de::Deserializer<'de> for Capture {
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
        FIELDS.with(|f| *f.borrow_mut() = fields.to_vec());
        Err(Stop)
    }

    serde::forward_to_deserialize_any! {
        bool i8 i16 i32 i64 i128 u8 u16 u32 u64 u128 f32 f64 char str string bytes byte_buf option unit
        unit_struct newtype_struct seq tuple tuple_struct map enum identifier ignored_any
    }
}

fn json_fields<T: for<'de> Deserialize<'de>>() -> Vec<String> {
    FIELDS.with(|f| f.borrow_mut().clear());
    let _ = T::deserialize(Capture);
    FIELDS.with(|f| f.borrow().iter().map(|s| s.to_string()).collect())
}

// Every property the registry writes on the nodes of a methodology version is an attribute of its type of the
// built-in meta-domain (strict attributes): the header, the elements (their position, removed mark included) and
// the steps. The properties are the JSON fields of the definition structs, so a new field of one fails here until
// the meta-domain declares it.
#[test]
fn definition_properties_are_attributes() {
    let cat = goap_typecat::builtin();
    let extra = ["position", "removed"];
    let meta = [
        "status",
        "createdAt",
        "updatedAt",
        "publishedAt",
        "updatedBy",
    ];
    let step_drop = ["pre", "done", "steps"];
    let cases: Vec<(&str, Vec<String>, Vec<&str>, Vec<&str>)> = vec![
        (
            TYPE_METHODOLOGY_VERSION,
            json_fields::<Methodology>(),
            vec![
                "conditions",
                "actions",
                "goals",
                "agents",
                "processes",
                "methods",
                "roles",
            ],
            meta.to_vec(),
        ),
        (
            "methodology@Condition",
            json_fields::<Condition>(),
            vec![],
            extra.to_vec(),
        ),
        (
            "methodology@Action",
            json_fields::<Action>(),
            vec![],
            extra.to_vec(),
        ),
        (
            "methodology@Goal",
            json_fields::<Goal>(),
            vec![],
            extra.to_vec(),
        ),
        (
            "methodology@Agent",
            json_fields::<Agent>(),
            vec![],
            extra.to_vec(),
        ),
        // the steps of a process or a method are nodes of their own, reached through sub_activity (ADR 0120)
        (
            "methodology@Process",
            json_fields::<Process>(),
            vec!["steps"],
            extra.to_vec(),
        ),
        (
            "methodology@Method",
            json_fields::<Method>(),
            vec!["steps"],
            extra.to_vec(),
        ),
        (
            "methodology@Role",
            json_fields::<Role>(),
            vec![],
            extra.to_vec(),
        ),
        // a step's pre / done are renamed input / output, its sub-steps are nodes of their own (stepNodeProps)
        (
            "methodology@Step",
            json_fields::<Step>(),
            step_drop.to_vec(),
            vec!["input", "output"],
        ),
        (
            "methodology@MethodStep",
            json_fields::<Step>(),
            step_drop.to_vec(),
            vec!["input", "output"],
        ),
    ];
    let mut errors = Vec::new();
    for (typ, fields, drop, add) in cases {
        assert!(!fields.is_empty(), "{typ}: no field captured");
        let (names, open) = cat.attribute_names(typ);
        if open {
            errors.push(format!("{typ} is open: the check below would be void"));
        }
        for f in fields
            .iter()
            .map(String::as_str)
            .filter(|f| !drop.contains(f))
            .chain(add)
        {
            if !names.iter().any(|n| n == f) {
                errors.push(format!(
                    "{typ}: the registry writes {f:?}, which is no attribute of the type"
                ));
            }
        }
    }
    assert!(errors.is_empty(), "{}", errors.join("\n"));
}
