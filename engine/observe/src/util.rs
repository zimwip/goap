//! JSON helpers keeping Go's encodings.

use serde::{Deserialize, Deserializer, Serializer};

/// Reads a JSON null as the default value (Go's nil slice or map).
pub(crate) fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// Writes an empty list as null (a Go nil slice without `omitempty`).
pub(crate) fn null_if_empty<S, T>(v: &[T], s: S) -> Result<S::Ok, S::Error>
where
    S: Serializer,
    T: serde::Serialize,
{
    if v.is_empty() {
        s.serialize_none()
    } else {
        s.collect_seq(v)
    }
}

pub(crate) fn is_zero_i64(v: &i64) -> bool {
    *v == 0
}

pub(crate) fn is_zero_f64(v: &f64) -> bool {
    *v == 0.0
}

pub(crate) fn is_false(v: &bool) -> bool {
    !*v
}

/// Go's `strconv.Quote` (the `%q` verb on a string).
pub(crate) fn quote(s: &str) -> String {
    let mut out = String::with_capacity(s.len() + 2);
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{7}' => out.push_str("\\a"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{b}' => out.push_str("\\v"),
            c if (c as u32) < 0x20 || c as u32 == 0x7f => {
                out.push_str(&format!("\\x{:02x}", c as u32));
            }
            c if c.is_control() => {
                let n = c as u32;
                if n <= 0xffff {
                    out.push_str(&format!("\\u{n:04x}"));
                } else {
                    out.push_str(&format!("\\U{n:08x}"));
                }
            }
            c => out.push(c),
        }
    }
    out.push('"');
    out
}

/// Writes a float the way `encoding/json` does for an integral value (`1`, not `1.0`).
pub(crate) fn go_float<S: Serializer>(v: &f64, s: S) -> Result<S::Ok, S::Error> {
    if v.fract() == 0.0 && v.abs() < 1e15 {
        s.serialize_i64(*v as i64)
    } else {
        s.serialize_f64(*v)
    }
}

/// A JSON number of a float, integral values as integers (`encoding/json`'s encoding of a float64).
pub(crate) fn number(v: f64) -> serde_json::Value {
    if v.fract() == 0.0 && v.abs() < 1e15 {
        serde_json::Value::from(v as i64)
    } else {
        serde_json::Value::from(v)
    }
}
