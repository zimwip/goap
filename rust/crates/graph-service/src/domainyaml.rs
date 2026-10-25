//! The YAML of a domain as Go's `Domain.YAML` lays it out (`yaml.Marshal` of `def.Domain`): the fields of each struct
//! in their order, omitted when empty where their tag says so, the maps with their keys sorted, from the JSON of the
//! definition the registry stores. The shape of `def.Domain` is generated from the Go structs
//! (`lib/def` `TestRustYAMLSchemaIsCurrent`), so that both stay the same.
use graph_catalog::shape::{Field, SHAPE, Type};
use graph_core::yaml::Yaml;
use serde_json::Value as Json;

/// The YAML document of a definition (Go's JSON of `def.Domain`, or the YAML of a built-in domain read as JSON).
pub fn document(def: &Json) -> Yaml {
    strukt(&SHAPE.root, def)
}

fn present(v: Option<&Json>) -> Option<&Json> {
    v.filter(|v| !v.is_null())
}

/// The value of a field in an object: by its JSON name, else (a field JSON leaves out) its YAML one.
fn of<'a>(f: &Field, o: &'a serde_json::Map<String, Json>) -> Option<&'a Json> {
    let name = if f.json.is_empty() { &f.yaml } else { &f.json };
    present(o.get(name))
}

/// Whether a value is empty as yaml.v3's `isZero` decides.
fn is_zero(t: &Type, v: Option<&Json>) -> bool {
    let Some(v) = present(v) else { return true };
    match t.kind.as_str() {
        "string" => v.as_str().is_none_or(str::is_empty),
        "bool" => v.as_bool() != Some(true),
        "int" | "float" => v.as_f64().is_none_or(|f| f == 0.0),
        "any" => false,
        "slice" => v.as_array().is_none_or(Vec::is_empty),
        "map" => v.as_object().is_none_or(serde_json::Map::is_empty),
        "ptr" => {
            let elem = t.elem.as_deref().expect("a pointer's element");
            elem.kind == "struct" && zero_by_method(&elem.name, v)
        }
        "struct" => {
            let s = &SHAPE.types[&t.name];
            if !s.zero.is_empty() {
                return zero_by_method(&t.name, v);
            }
            let o = v.as_object();
            s.fields.iter().all(|f| is_zero(&f.typ, o.and_then(|o| of(f, o))))
        }
        _ => false,
    }
}

/// The `IsZero` of a type implementing it: its fields named all empty.
fn zero_by_method(name: &str, v: &Json) -> bool {
    let s = &SHAPE.types[name];
    if s.zero.is_empty() {
        return false;
    }
    let o = v.as_object();
    s.zero.iter().all(|z| {
        s.fields
            .iter()
            .find(|f| &f.yaml == z)
            .is_none_or(|f| is_zero(&f.typ, o.and_then(|o| of(f, o))))
    })
}

fn strukt(name: &str, v: &Json) -> Yaml {
    let s = &SHAPE.types[name];
    let o = v.as_object();
    let mut out = Vec::new();
    for f in &s.fields {
        let x = o.and_then(|o| of(f, o));
        if f.omit && is_zero(&f.typ, x) {
            continue;
        }
        out.push((Yaml::str(f.yaml.clone()), value(&f.typ, x)));
    }
    Yaml::Map(out)
}

/// A value of a type, the zero value of it when absent.
fn value(t: &Type, v: Option<&Json>) -> Yaml {
    let v = present(v);
    match t.kind.as_str() {
        "string" => Yaml::str(v.and_then(Json::as_str).unwrap_or_default()),
        "bool" => Yaml::Plain(v.and_then(Json::as_bool).unwrap_or(false).to_string()),
        "int" => Yaml::Plain(
            v.and_then(|v| v.as_i64().or_else(|| v.as_f64().map(|f| f as i64)))
                .unwrap_or(0)
                .to_string(),
        ),
        "float" => Yaml::Plain(v.and_then(Json::as_f64).unwrap_or(0.0).to_string()),
        "any" => v.map_or_else(|| Yaml::Plain("null".into()), Yaml::of_json),
        "slice" => {
            let elem = t.elem.as_deref().expect("a slice's element");
            Yaml::Seq(
                v.and_then(Json::as_array)
                    .into_iter()
                    .flatten()
                    .map(|x| value(elem, Some(x)))
                    .collect(),
            )
        }
        "map" => {
            let elem = t.elem.as_deref().expect("a map's element");
            let Some(m) = v.and_then(Json::as_object) else {
                return Yaml::Map(Vec::new());
            };
            Yaml::Map(
                m.iter()
                    .map(|(k, x)| (Yaml::str(k.clone()), value(elem, Some(x))))
                    .collect(),
            )
        }
        "ptr" => match v {
            None => Yaml::Plain("null".into()),
            Some(x) => value(t.elem.as_deref().expect("a pointer's element"), Some(x)),
        },
        "struct" => strukt(&t.name, v.unwrap_or(&Json::Null)),
        k => unreachable!("a kind the generated shape does not hold: {k}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use graph_core::yaml::to_string;
    use serde_json::json;

    #[test]
    fn writes_the_fields_in_their_order_and_leaves_out_the_empty_ones() {
        let def = json!({
            "version": "1.0.0", "name": "dt",
            "nodeTypes": [{"name": "A"}],
            "linkTypes": null,
        });
        let out = to_string(&document(&def));
        assert!(out.starts_with("name: dt\nversion: 1.0.0\n"), "{out}");
        assert!(out.contains("nodeTypes:\n- name: A\n"), "{out}");
        assert!(out.contains("linkTypes: []\n"), "{out}");
    }
}
