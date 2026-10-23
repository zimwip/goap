//! What every edit of a draft is held to (Go's `typeIndex.checkAttributes`, `checkLinkAttributes`, `checkNode`,
//! `checkLink`): the properties of a node are attributes of its type, of the type and enum their attribute says.
use chrono::{DateTime, NaiveDate, NaiveDateTime};
use graph_catalog::{Catalog, catalog::AttributeCheck};
use serde_json::{Map, Value};

use super::tx::go_sprint;
use crate::{GraphError, Result};

/// What a value of an attribute must be (`domain.AttributeCheck.Check`); a code text is checked by the Go service.
fn check_value(a: &AttributeCheck, v: Option<&Value>) -> Result<(), String> {
    let Some(v) = v else { return Ok(()) };
    if v.is_null() || v.as_str() == Some("") {
        return Ok(());
    }
    let p = go_sprint(v);
    match a.typ.as_str() {
        "number" => {
            let ok = v.is_number() || v.as_str().is_some_and(|s| s.parse::<f64>().is_ok());
            if !ok {
                return Err(format!("a number is expected, got {p}"));
            }
        }
        "boolean" if !v.is_boolean() => return Err(format!("a boolean is expected, got {p}")),
        "date" => {
            let ok = v.as_str().is_some_and(|s| {
                (s.len() == 10 && NaiveDate::parse_from_str(s, "%Y-%m-%d").is_ok())
                    || DateTime::parse_from_rfc3339(s).is_ok()
                    || (s.len() == 19 && NaiveDateTime::parse_from_str(s, "%Y-%m-%dT%H:%M:%S").is_ok())
            });
            if !ok {
                return Err(format!("a date (YYYY-MM-DD or RFC 3339) is expected, got {p}"));
            }
        }
        "enum" => {
            if !v.as_str().is_some_and(|s| a.values.iter().any(|x| x == s)) {
                return Err(format!("{p} is not a value of the enum {}", a.enumeration));
            }
        }
        "string" if !v.is_string() => return Err(format!("a string is expected, got {p}")),
        "code" => return Err(String::new()),
        _ => {}
    }
    Ok(())
}

fn value_error(a: &AttributeCheck, e: String, what: impl FnOnce(String) -> String) -> GraphError {
    if a.typ == "code" {
        return GraphError::unimplemented("a code text (ADR 0152) is checked by the Go service");
    }
    GraphError::invalid(what(e))
}

/// The properties of a node of a type: attributes of its type only (unless it is open), each of its kind.
pub fn attributes(cat: &Catalog, key: &str, typ: &str, props: Option<&Map<String, Value>>) -> Result<()> {
    let empty = Map::new();
    let props = props.unwrap_or(&empty);
    let (names, open) = cat.attribute_names(typ);
    if !open {
        let mut keys: Vec<&String> = props.keys().collect();
        keys.sort();
        for k in keys {
            if !names.contains(&k.as_str()) {
                return Err(GraphError::invalid(format!(
                    "{key} ({typ}): property {k:?} is not an attribute of {typ}"
                )));
            }
        }
    }
    for a in cat.attribute_checks(typ) {
        if let Err(e) = check_value(&a, props.get(&a.name)) {
            return Err(value_error(&a, e, |e| {
                format!("{key} ({typ}): property {:?} is invalid: {e}", a.name)
            }));
        }
    }
    Ok(())
}

/// The properties of a link of a type, of the kinds its attributes say.
pub fn link_attributes(cat: &Catalog, typ: &str, props: Option<&Map<String, Value>>) -> Result<()> {
    let empty = Map::new();
    let props = props.unwrap_or(&empty);
    for a in cat.link_attribute_checks(typ) {
        if let Err(e) = check_value(&a, props.get(&a.name)) {
            return Err(value_error(&a, e, |e| {
                format!("link {typ}: property {:?} is invalid: {e}", a.name)
            }));
        }
    }
    Ok(())
}

/// The existence rule for a node of a namespace.
pub fn node(cat: &Catalog, ns: &str, typ: &str) -> Result<()> {
    cat.check_node(ns, typ).map_err(GraphError::invalid)
}

/// A link type and its ends.
pub fn link(cat: &Catalog, typ: &str, from: &str, to: &str) -> Result<()> {
    cat.check_link(typ, from, to).map_err(GraphError::invalid)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn check(typ: &str, v: Value) -> Result<(), String> {
        let a = AttributeCheck {
            name: "x".into(),
            typ: typ.into(),
            enumeration: "E".into(),
            values: vec!["a".into()],
        };
        check_value(&a, Some(&v))
    }

    #[test]
    fn checks_values_as_go() {
        assert!(check("number", json!(1.5)).is_ok());
        assert!(check("number", json!("2.5")).is_ok());
        assert_eq!(
            check("number", json!(true)).unwrap_err(),
            "a number is expected, got true"
        );
        assert!(check("date", json!("2026-10-23")).is_ok());
        assert!(check("date", json!("2026-10-23T10:00:00Z")).is_ok());
        assert!(check("date", json!("2026-10-23T10:00:00")).is_ok());
        assert!(check("date", json!("23/10/2026")).is_err());
        assert_eq!(check("enum", json!("b")).unwrap_err(), "b is not a value of the enum E");
        assert!(check("string", json!("")).is_ok());
        assert_eq!(check("string", json!(1.0)).unwrap_err(), "a string is expected, got 1");
    }
}
