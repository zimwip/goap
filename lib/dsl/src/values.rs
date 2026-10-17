//! The arguments of a `ctx` method, read from the JSON values the engine hands over the way goja converted a
//! JavaScript value to the Go parameter (a missing, `null` or `undefined` argument is the zero value).

use serde::Serialize;
use serde_json::{Map, Value};

use crate::gofmt::js_string;

/// The argument `i`, `null` when missing.
pub(crate) fn arg(args: &[Value], i: usize) -> &Value {
    args.get(i).unwrap_or(&Value::Null)
}

/// A string parameter.
pub(crate) fn arg_str(args: &[Value], i: usize) -> String {
    js_string(arg(args, i))
}

/// A boolean parameter (JavaScript truthiness).
pub(crate) fn arg_bool(args: &[Value], i: usize) -> bool {
    match arg(args, i) {
        Value::Null => false,
        Value::Bool(b) => *b,
        Value::Number(n) => n.as_f64().is_some_and(|f| f != 0.0 && !f.is_nan()),
        Value::String(s) => !s.is_empty(),
        Value::Array(_) | Value::Object(_) => true,
    }
}

/// A float64 parameter.
pub(crate) fn arg_f64(args: &[Value], i: usize) -> f64 {
    match arg(args, i) {
        Value::Number(n) => n.as_f64().unwrap_or(0.0),
        Value::Bool(true) => 1.0,
        Value::String(s) => s.trim().parse().unwrap_or(f64::NAN),
        _ => 0.0,
    }
}

/// A `map[string]any` parameter: the object, `None` for anything else (Go's nil map).
pub(crate) fn arg_map(args: &[Value], i: usize) -> Option<Map<String, Value>> {
    match arg(args, i) {
        Value::Object(m) => Some(m.clone()),
        _ => None,
    }
}

/// A `[]string` parameter.
pub(crate) fn arg_strings(args: &[Value], i: usize) -> Vec<String> {
    match arg(args, i) {
        Value::Array(a) => a.iter().map(js_string).collect(),
        Value::Null => Vec::new(),
        v => vec![js_string(v)],
    }
}

/// A map value as Go stored a possibly nil `map[string]any` in a `map[string]any`: the object, else `null`.
pub(crate) fn map_or_null(m: Option<Map<String, Value>>) -> Value {
    m.map(Value::Object).unwrap_or(Value::Null)
}

/// A float64 as `encoding/json` writes it: an integral value is written without a fraction.
pub(crate) fn num(f: f64) -> Value {
    if f.fract() == 0.0 && f.abs() < 9_007_199_254_740_992.0 {
        Value::from(f as i64)
    } else {
        serde_json::Number::from_f64(f)
            .map(Value::Number)
            .unwrap_or(Value::Null)
    }
}

/// A value of the DSL as the script sees it (its JSON form).
pub(crate) fn to_value<T: Serialize>(v: &T) -> Value {
    serde_json::to_value(v).unwrap_or(Value::Null)
}

/// The string field of an object (`""` when absent or not a string, Go's `m["k"].(string)`).
pub(crate) fn field_str(m: &Map<String, Value>, k: &str) -> String {
    m.get(k)
        .and_then(Value::as_str)
        .map(str::to_string)
        .unwrap_or_default()
}

/// The object field of an object (`None` when absent or not an object).
pub(crate) fn field_map(m: &Map<String, Value>, k: &str) -> Option<Map<String, Value>> {
    m.get(k).and_then(Value::as_object).cloned()
}

/// Go's `asList`: a list value, else none.
pub(crate) fn as_list(v: Option<&Value>) -> &[Value] {
    match v {
        Some(Value::Array(a)) => a,
        _ => &[],
    }
}
