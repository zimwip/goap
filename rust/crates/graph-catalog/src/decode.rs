//! A domain definition read as `def.ParseDomain` reads it and stored as the Go registry stores it: the first YAML (or
//! JSON) document parsed by `serde_norway`, its merge keys applied, then laid into the Go structs of
//! [`shape`](crate::shape): fields known by their YAML name, an unknown one refused (ignored in a type taking a short
//! form, which Go decodes itself), a plain scalar setting the field of such a type, each value of its type, the empty fields left out where the JSON tag says so.
use serde_json::{Map, Value};

use crate::shape::{SHAPE, Type};

/// Why a definition does not read (`parse domain: …`).
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParseError(pub String);

/// The type of the definition of a domain (`def.Domain`).
pub fn domain_type() -> Type {
    Type {
        kind: "struct".into(),
        name: SHAPE.root.clone(),
        go: SHAPE.root.clone(),
        elem: None,
    }
}

/// The JSON of the definition of a YAML (or JSON) text (`def.ParseDomain`, then `json.Marshal`).
pub fn parse_domain(src: &str) -> Result<Value, ParseError> {
    let fail = |m: String| ParseError(format!("parse domain: {m}"));
    let mut y: serde_norway::Value = serde_norway::from_str(src).map_err(|e| fail(e.to_string()))?;
    if y.is_null() {
        return Err(fail("EOF".into()));
    }
    y.apply_merge().map_err(|e| fail(e.to_string()))?;
    let v = serde_json::to_value(&y).map_err(|e| fail(e.to_string()))?;
    shaped(&domain_type(), &v, true).map_err(fail)
}

/// A JSON value laid into a type, as `json.Unmarshal` reads it: a key matched exactly, else without regard to case,
/// an unknown one ignored.
pub fn from_json(t: &Type, v: &Value) -> Result<Value, String> {
    shaped(t, v, false)
}

/// A value laid into a type: from YAML (`yaml`, fields by their YAML name, unknown ones refused) or JSON.
fn shaped(t: &Type, v: &Value, yaml: bool) -> Result<Value, String> {
    let mismatch = || Err(format!("cannot read {v} into {}", t.go));
    Ok(match (t.kind.as_str(), v) {
        (_, Value::Null) => zero(t),
        ("ptr", _) => shaped(t.elem(), v, yaml)?,
        ("string", Value::String(_)) => v.clone(),
        // a YAML scalar read into a string is its text
        ("string", Value::Number(_) | Value::Bool(_)) if yaml => Value::String(v.to_string()),
        ("bool", Value::Bool(_)) | ("any", _) => v.clone(),
        ("int", Value::Number(n)) => match n
            .as_i64()
            .or_else(|| n.as_f64().filter(|f| f.fract() == 0.0).map(|f| f as i64))
        {
            Some(i) => Value::from(i),
            None => return mismatch(),
        },
        ("float", Value::Number(n)) => Value::from(n.as_f64().unwrap_or_default()),
        ("slice", Value::Array(xs)) => {
            Value::Array(xs.iter().map(|x| shaped(t.elem(), x, yaml)).collect::<Result<_, _>>()?)
        }
        ("map", Value::Object(m)) => Value::Object(
            m.iter()
                .map(|(k, x)| Ok((k.clone(), shaped(t.elem(), x, yaml)?)))
                .collect::<Result<_, String>>()?,
        ),
        ("struct", Value::Object(m)) => strukt(t, m, yaml)?,
        // a type taking a short form: a plain scalar sets its field
        ("struct", Value::String(_) | Value::Number(_) | Value::Bool(_)) if !SHAPE.strukt(&t.name).short.is_empty() => {
            let short = &SHAPE.strukt(&t.name).short;
            strukt(t, &Map::from_iter([(short.clone(), v.clone())]), yaml)?
        }
        _ => return mismatch(),
    })
}

fn strukt(t: &Type, m: &Map<String, Value>, yaml: bool) -> Result<Value, String> {
    let s = SHAPE.strukt(&t.name);
    if yaml
        && s.short.is_empty()
        && let Some(k) = m.keys().find(|k| !s.fields.iter().any(|f| &f.yaml == *k))
    {
        return Err(format!("field {k} not found in type {}", t.go));
    }
    let mut out = Map::new();
    for f in &s.fields {
        let x = if yaml {
            m.get(&f.yaml)
        } else if f.json.is_empty() {
            None
        } else {
            m.get(&f.json)
                .or_else(|| m.iter().find(|(k, _)| k.eq_ignore_ascii_case(&f.json)).map(|(_, x)| x))
        };
        let x = match x {
            Some(x) => shaped(&f.typ, x, yaml)?,
            None => zero(&f.typ),
        };
        if f.json.is_empty() || f.json_omit && empty(&f.typ, &x) {
            continue;
        }
        out.insert(f.json.clone(), x);
    }
    Ok(Value::Object(out))
}

/// The zero value of a type, as Go's JSON writes it.
fn zero(t: &Type) -> Value {
    match t.kind.as_str() {
        "string" => Value::String(String::new()),
        "bool" => Value::Bool(false),
        "int" | "float" => Value::from(0),
        "struct" => strukt(t, &Map::new(), false).unwrap_or_default(),
        _ => Value::Null,
    }
}

/// Whether `omitempty` leaves a value out (a pointer only when nil, a struct never).
fn empty(t: &Type, v: &Value) -> bool {
    match v {
        Value::Null => true,
        _ if t.kind == "ptr" || t.kind == "struct" => false,
        Value::String(s) => s.is_empty(),
        Value::Bool(b) => !b,
        Value::Number(n) => n.as_f64() == Some(0.0),
        Value::Array(a) => a.is_empty(),
        Value::Object(m) => m.is_empty(),
    }
}
