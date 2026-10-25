//! JSON as the graph writes it, through `serde_json`: what Go stores reads the same whichever service wrote it, the
//! spelling apart (a number `1` and `1.0`, a key order, an escape). Two values are compared as Go compares the `any`
//! it decodes them to ([`floats`]), and a time is written as Go stores it (RFC 3339, trailing zeros dropped), so that
//! the times both services write sort alike.
use chrono::{DateTime, SecondsFormat, Utc};
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::Value;

/// The JSON of a value.
pub fn to_vec<T: ?Sized + Serialize>(v: &T) -> Vec<u8> {
    // what this crate serializes has no map with keys that are not strings, nor a failing Serialize
    serde_json::to_vec(v).expect("JSON of a value")
}

pub fn to_string<T: ?Sized + Serialize>(v: &T) -> String {
    serde_json::to_string(v).expect("JSON of a value")
}

/// The JSON of a value, indented by two spaces.
pub fn to_string_pretty<T: ?Sized + Serialize>(v: &T) -> String {
    serde_json::to_string_pretty(v).expect("JSON of a value")
}

/// A JSON text written again compact (`json.Marshal` of a decoded `any`).
pub fn normalize(raw: &str) -> Result<String, serde_json::Error> {
    let v: Value = serde_json::from_str(raw)?;
    Ok(to_string(&v))
}

/// A time as Go's `time.Time` marshals it: RFC 3339 with nanoseconds, trailing zeros dropped, `Z` for UTC.
pub fn time_text(t: &DateTime<Utc>) -> String {
    let s = t.to_rfc3339_opts(SecondsFormat::Nanos, true);
    let Some((head, frac)) = s.trim_end_matches('Z').split_once('.') else {
        return s;
    };
    let frac = frac.trim_end_matches('0');
    if frac.is_empty() {
        format!("{head}Z")
    } else {
        format!("{head}.{frac}Z")
    }
}

/// Go's zero time, the `at` of what was never set.
pub fn zero_time() -> DateTime<Utc> {
    DateTime::from_timestamp(-62135596800, 0).unwrap_or_default()
}

/// `#[serde(with = "jsontext::time")]`: a `DateTime<Utc>` as Go marshals a `time.Time`.
pub mod time {
    use super::*;

    pub fn serialize<S: Serializer>(t: &DateTime<Utc>, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&time_text(t))
    }

    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<DateTime<Utc>, D::Error> {
        let s = String::deserialize(d)?;
        DateTime::parse_from_rfc3339(&s)
            .map(|t| t.with_timezone(&Utc))
            .map_err(serde::de::Error::custom)
    }
}

/// A JSON text kept as is, written raw: the parts of a payload Go holds as `json.RawMessage`.
#[derive(Debug, Clone, PartialEq, Eq, PartialOrd, Ord)]
pub struct Raw(pub String);

impl Serialize for Raw {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let raw = serde_json::value::RawValue::from_string(self.0.clone()).map_err(serde::ser::Error::custom)?;
        raw.serialize(s)
    }
}

/// A value as Go decodes it into `any`: every number a float64, so that two values Go finds equal are equal here.
pub fn floats(v: Value) -> Value {
    match v {
        Value::Number(n) => n
            .as_f64()
            .and_then(serde_json::Number::from_f64)
            .map_or(Value::Null, Value::Number),
        Value::Array(a) => Value::Array(a.into_iter().map(floats).collect()),
        Value::Object(m) => Value::Object(m.into_iter().map(|(k, v)| (k, floats(v))).collect()),
        v => v,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn compares_numbers_as_go() {
        assert_eq!(floats(json!({"a": [1]})), floats(json!({"a": [1.0]})));
        assert_ne!(json!(1), json!(1.0));
    }

    #[test]
    fn writes_times_as_go() {
        let t = DateTime::parse_from_rfc3339("2026-10-23T10:00:01.120000000Z")
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(time_text(&t), "2026-10-23T10:00:01.12Z");
        assert_eq!(time_text(&zero_time()), "0001-01-01T00:00:00Z");
    }
}
