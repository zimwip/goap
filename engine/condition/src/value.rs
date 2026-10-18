//! Conversions between the plain values of the activation (JSON) and the values of CEL.
use std::{collections::HashMap, sync::Arc};

use cel::{
    Value as CelValue,
    common::value::Val,
    objects::{Key, Map as CelMap},
};
use serde_json::{Map, Number, Value};

/// A plain value as CEL sees it: a whole number is an int (an uint beyond the int range), any other number a double,
/// as cel-go adapts the int64 / float64 values of an activation.
pub(crate) fn to_cel(v: &Value) -> CelValue {
    match v {
        Value::Null => CelValue::Null,
        Value::Bool(b) => CelValue::Bool(*b),
        Value::Number(n) => {
            if let Some(i) = n.as_i64() {
                CelValue::Int(i)
            } else if let Some(u) = n.as_u64() {
                CelValue::UInt(u)
            } else {
                CelValue::Float(n.as_f64().unwrap_or(f64::NAN))
            }
        }
        Value::String(s) => CelValue::String(Arc::new(s.clone())),
        Value::Array(a) => CelValue::List(Arc::new(a.iter().map(to_cel).collect())),
        Value::Object(m) => {
            let map: HashMap<Key, CelValue> = m
                .iter()
                .map(|(k, v)| (Key::String(Arc::new(k.clone())), to_cel(v)))
                .collect();
            CelValue::Map(CelMap { map: Arc::new(map) })
        }
    }
}

/// A plain value as a boxed CEL value, ready to bind.
pub(crate) fn to_val(v: &Value) -> Box<dyn Val> {
    Box::<dyn Val>::try_from(to_cel(v)).expect("a JSON value converts")
}

/// A CEL value as a plain value (maps, lists, strings, numbers, bools, null), as cel-go converts one to a
/// `structpb.Value`: bytes as base64, a timestamp or a duration as its text, a value with no plain form null.
pub(crate) fn from_cel(v: &CelValue) -> Value {
    match v {
        CelValue::Null => Value::Null,
        CelValue::Bool(b) => Value::Bool(*b),
        CelValue::Int(i) => Value::from(*i),
        CelValue::UInt(u) => Value::from(*u),
        CelValue::Float(f) => Number::from_f64(*f)
            .map(Value::Number)
            .unwrap_or(Value::Null),
        CelValue::String(s) => Value::String(s.as_str().to_string()),
        CelValue::Bytes(b) => Value::String(base64(b)),
        CelValue::List(l) => Value::Array(l.iter().map(from_cel).collect()),
        CelValue::Map(m) => {
            let mut out = Map::new();
            for (k, v) in m.map.iter() {
                let key = match k {
                    Key::String(s) => s.as_str().to_string(),
                    Key::Int(i) => i.to_string(),
                    Key::Uint(u) => u.to_string(),
                    Key::Bool(b) => b.to_string(),
                };
                out.insert(key, from_cel(v));
            }
            Value::Object(out)
        }
        CelValue::Duration(d) => {
            Value::String(format!("{}s", d.num_milliseconds() as f64 / 1000.0))
        }
        CelValue::Timestamp(t) => Value::String(t.to_rfc3339()),
        _ => Value::Null,
    }
}

fn base64(b: &[u8]) -> String {
    const T: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::new();
    for c in b.chunks(3) {
        let n = (c[0] as u32) << 16
            | (*c.get(1).unwrap_or(&0) as u32) << 8
            | *c.get(2).unwrap_or(&0) as u32;
        out.push(T[(n >> 18) as usize & 63] as char);
        out.push(T[(n >> 12) as usize & 63] as char);
        out.push(if c.len() > 1 {
            T[(n >> 6) as usize & 63] as char
        } else {
            '='
        });
        out.push(if c.len() > 2 {
            T[n as usize & 63] as char
        } else {
            '='
        });
    }
    out
}

/// The text of a CEL value in an error message (`non-bool result <v>`), as Go prints the native value.
pub(crate) fn go_text(v: &CelValue) -> String {
    match v {
        CelValue::Null => "<nil>".into(),
        CelValue::String(s) => s.as_str().to_string(),
        CelValue::Float(f) => crate::unparse::format_double(*f),
        other => serde_json::to_string(&from_cel(other)).unwrap_or_default(),
    }
}
