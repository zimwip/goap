//! Helpers of the JSON forms: Go's zero values and its encoding.

use serde::{Deserialize, Deserializer, Serialize};

/// Reads a JSON null as the default value (Go decodes null into a nil slice or map, or leaves the zero value).
pub(crate) fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

pub(crate) fn is_false(b: &bool) -> bool {
    !*b
}

pub(crate) fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// `json.Marshal`: the JSON text with `<`, `>`, `&`, U+2028 and U+2029 escaped the way Go escapes them (they occur
/// only inside strings, so a replacement over the text is exact).
pub(crate) fn go_marshal<T: Serialize + ?Sized>(v: &T) -> String {
    let s = serde_json::to_string(v).unwrap_or_default();
    let mut out = String::with_capacity(s.len());
    for c in s.chars() {
        match c {
            '<' => out.push_str("\\u003c"),
            '>' => out.push_str("\\u003e"),
            '&' => out.push_str("\\u0026"),
            '\u{2028}' => out.push_str("\\u2028"),
            '\u{2029}' => out.push_str("\\u2029"),
            c => out.push(c),
        }
    }
    out
}
