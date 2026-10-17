//! Helpers of the JSON forms: Go's zero values and its encoding.

use serde::{Deserialize, Deserializer, Serialize, Serializer};

/// Reads a JSON null as the default value (Go decodes null into a nil slice or map).
pub(crate) fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

/// Writes an empty vector as null, as Go encodes a nil slice.
pub(crate) fn nil_vec<S: Serializer, T: Serialize>(v: &[T], s: S) -> Result<S::Ok, S::Error> {
    if v.is_empty() {
        s.serialize_none()
    } else {
        v.serialize(s)
    }
}

pub(crate) fn is_false(b: &bool) -> bool {
    !*b
}

pub(crate) fn is_zero_i64(n: &i64) -> bool {
    *n == 0
}

/// Go's `json.Marshal` of a value: serde's compact form with the HTML-sensitive characters escaped as Go does.
pub(crate) fn go_marshal<T: serde::Serialize + ?Sized>(v: &T) -> String {
    let s = serde_json::to_string(v).unwrap_or_else(|_| "null".into());
    if !s.contains(['<', '>', '&', '\u{2028}', '\u{2029}']) {
        return s;
    }
    s.replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('&', "\\u0026")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}
