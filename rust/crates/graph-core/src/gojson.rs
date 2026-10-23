//! JSON as Go's `encoding/json` writes it, byte for byte: what the Go service stores (SQLite keeps JSON as text, the log
//! payloads and the outbox messages are compared as bytes) must read the same whichever service wrote it.
//!
//! The differences with serde_json's own output: a number is a float64 (`1.0` is `1`, `1e21` is `1e+21`, an integer
//! past 2^53 loses its digits as Go's does), `<`, `>`, `&`, U+2028 and U+2029 are escaped (Go's HTML-safe default) and
//! a time is RFC 3339 with its trailing zeros dropped. Map keys are sorted (serde_json's `Map` is ordered), struct
//! fields keep their declaration order, as Go's do.
use std::io;

use chrono::{DateTime, SecondsFormat, Utc};
use serde::{Deserialize, Deserializer, Serialize, Serializer};
use serde_json::{Value, ser::Formatter};

/// The formatter writing Go's forms of numbers and strings.
#[derive(Debug, Clone, Copy, Default)]
pub struct GoFormatter;

/// The integers a float64 holds exactly: written as integers; the others as Go writes the float64 they decode to.
const EXACT: u64 = 1 << 53;

impl Formatter for GoFormatter {
    fn write_f64<W: ?Sized + io::Write>(&mut self, w: &mut W, v: f64) -> io::Result<()> {
        w.write_all(float(v).as_bytes())
    }

    fn write_f32<W: ?Sized + io::Write>(&mut self, w: &mut W, v: f32) -> io::Result<()> {
        w.write_all(float(f64::from(v)).as_bytes())
    }

    fn write_i64<W: ?Sized + io::Write>(&mut self, w: &mut W, v: i64) -> io::Result<()> {
        if v.unsigned_abs() > EXACT {
            return w.write_all(float(v as f64).as_bytes());
        }
        w.write_all(v.to_string().as_bytes())
    }

    fn write_u64<W: ?Sized + io::Write>(&mut self, w: &mut W, v: u64) -> io::Result<()> {
        if v > EXACT {
            return w.write_all(float(v as f64).as_bytes());
        }
        w.write_all(v.to_string().as_bytes())
    }

    fn write_string_fragment<W: ?Sized + io::Write>(&mut self, w: &mut W, fragment: &str) -> io::Result<()> {
        let mut start = 0;
        for (i, c) in fragment.char_indices() {
            let esc: &[u8] = match c {
                '<' => b"\\u003c",
                '>' => b"\\u003e",
                '&' => b"\\u0026",
                '\u{2028}' => b"\\u2028",
                '\u{2029}' => b"\\u2029",
                _ => continue,
            };
            w.write_all(&fragment.as_bytes()[start..i])?;
            w.write_all(esc)?;
            start = i + c.len_utf8();
        }
        w.write_all(&fragment.as_bytes()[start..])
    }
}

/// A float64 as `strconv.AppendFloat(f, 'f' or 'e', -1, 64)` in `encoding/json`: the shortest digits, in exponent
/// form below 1e-6 and from 1e21, the exponent with its sign and at least one digit.
pub fn float(v: f64) -> String {
    if !v.is_finite() {
        // Go refuses them; serde_json writes null
        return "null".into();
    }
    let a = v.abs();
    if a != 0.0 && !(1e-6..1e21).contains(&a) {
        let s = format!("{v:e}");
        let (mant, exp) = s.split_once('e').unwrap_or((&s, "0"));
        let (sign, digits) = match exp.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', exp),
        };
        // Go writes at least two digits but trims the leading zero of a negative one: e-07 → e-7, e+21 stays
        let digits = if digits.len() < 2 && sign == '+' {
            format!("0{digits}")
        } else {
            digits.to_string()
        };
        return format!("{mant}e{sign}{digits}");
    }
    format!("{v}")
}

/// The JSON of a value as Go writes it.
pub fn to_vec<T: ?Sized + Serialize>(v: &T) -> Vec<u8> {
    let mut out = Vec::with_capacity(128);
    let mut ser = serde_json::Serializer::with_formatter(&mut out, GoFormatter);
    // what this crate serializes has no map with keys that are not strings, nor a failing Serialize
    v.serialize(&mut ser).expect("JSON of a value");
    out
}

pub fn to_string<T: ?Sized + Serialize>(v: &T) -> String {
    // the formatter writes valid UTF-8 only
    String::from_utf8(to_vec(v)).unwrap_or_default()
}

/// A JSON text written again as Go writes it (`json.Marshal` of a decoded `any`): numbers as float64, keys sorted.
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

/// `#[serde(with = "gojson::time")]`: a `DateTime<Utc>` as Go marshals a `time.Time`.
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
    fn writes_numbers_as_go() {
        let cases = [
            (0.0, "0"),
            (1.0, "1"),
            (-2.5, "-2.5"),
            (1e20, "100000000000000000000"),
            (1e21, "1e+21"),
            (1.5e21, "1.5e+21"),
            (1e-6, "0.000001"),
            (1e-7, "1e-7"),
            (1.25e-10, "1.25e-10"),
            (1e100, "1e+100"),
            (0.1, "0.1"),
            (123456789.125, "123456789.125"),
        ];
        for (v, want) in cases {
            assert_eq!(float(v), want, "{v}");
        }
        assert_eq!(
            to_string(&json!([1, 2.0, 12345678901234567890u64, -9007199254740993i64])),
            "[1,2,12345678901234567000,-9007199254740992]"
        );
    }

    #[test]
    fn escapes_as_go() {
        let v = json!({"b": "a<b>&c\u{2028}\u{2029}\"\\\n\t\u{8}\u{c}\u{1}\u{7f}é", "a": 1});
        assert_eq!(
            to_string(&v),
            r#"{"a":1,"b":"a\u003cb\u003e\u0026c\u2028\u2029\"\\\n\t\b\f\u0001é"}"#
        );
    }

    #[test]
    fn writes_times_as_go() {
        let t = DateTime::parse_from_rfc3339("2026-10-23T10:00:01.120000000Z")
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(time_text(&t), "2026-10-23T10:00:01.12Z");
        let t = DateTime::parse_from_rfc3339("2026-10-23T10:00:01Z")
            .unwrap()
            .with_timezone(&Utc);
        assert_eq!(time_text(&t), "2026-10-23T10:00:01Z");
        assert_eq!(time_text(&zero_time()), "0001-01-01T00:00:00Z");
    }
}
