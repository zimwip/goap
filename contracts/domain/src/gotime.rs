//! Times as Go's encoding/json writes them (RFC 3339 with the nanoseconds, trailing zeros trimmed, `Z` for UTC), and
//! Go's zero time (`0001-01-01T00:00:00Z`), which an `Option` field stands for with `None`.
//!
//! `#[serde(with = "goap_domain::gotime")]` on a `DateTime<Utc>`, `#[serde(with = "goap_domain::gotime::option")]` on
//! an `Option<DateTime<Utc>>` (a field the Go code tests with `IsZero`: `None` is written as the zero time, as Go
//! writes it whatever `omitempty` says, and the zero time or `null` reads as `None`).

use chrono::{DateTime, NaiveDate, SecondsFormat, Utc};
use serde::{Deserialize, Deserializer, Serializer};

/// Go's zero time, `0001-01-01T00:00:00Z`.
pub fn zero() -> DateTime<Utc> {
    NaiveDate::from_ymd_opt(1, 1, 1)
        .and_then(|d| d.and_hms_opt(0, 0, 0))
        .map(|d| d.and_utc())
        .unwrap_or_default()
}

/// Reports Go's zero time.
pub fn is_zero(t: &DateTime<Utc>) -> bool {
    *t == zero()
}

/// `time.RFC3339Nano`: `2006-01-02T15:04:05.999999999Z07:00` (trailing zeros of the fraction trimmed).
pub fn format(t: &DateTime<Utc>) -> String {
    let s = t.to_rfc3339_opts(SecondsFormat::Nanos, true);
    // "...SS.nnnnnnnnnZ": trim the zeros of the fraction, and the dot when nothing is left
    let Some(dot) = s.rfind('.') else { return s };
    let frac = s[dot + 1..s.len() - 1].trim_end_matches('0');
    if frac.is_empty() {
        format!("{}Z", &s[..dot])
    } else {
        format!("{}.{}Z", &s[..dot], frac)
    }
}

/// Parses an RFC 3339 time (`time.Parse(time.RFC3339Nano, s)`).
pub fn parse(s: &str) -> Option<DateTime<Utc>> {
    DateTime::parse_from_rfc3339(s)
        .ok()
        .map(|t| t.with_timezone(&Utc))
}

/// Serializes a time as Go does.
pub fn serialize<S: Serializer>(t: &DateTime<Utc>, s: S) -> Result<S::Ok, S::Error> {
    s.serialize_str(&format(t))
}

/// Deserializes a time; `null` reads as Go's zero time.
pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<DateTime<Utc>, D::Error> {
    match Option::<String>::deserialize(d)? {
        None => Ok(zero()),
        Some(s) => parse(&s).ok_or_else(|| serde::de::Error::custom(format!("invalid time {s:?}"))),
    }
}

/// An optional time: `None` is Go's zero time.
pub mod option {
    use super::*;

    /// Serializes `None` as Go's zero time.
    pub fn serialize<S: Serializer>(t: &Option<DateTime<Utc>>, s: S) -> Result<S::Ok, S::Error> {
        match t {
            Some(t) => s.serialize_str(&format(t)),
            None => s.serialize_str(&format(&zero())),
        }
    }

    /// Deserializes `null` or the zero time as `None`.
    pub fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<Option<DateTime<Utc>>, D::Error> {
        match Option::<String>::deserialize(d)? {
            None => Ok(None),
            Some(s) => {
                let t = parse(&s)
                    .ok_or_else(|| serde::de::Error::custom(format!("invalid time {s:?}")))?;
                Ok(if is_zero(&t) { None } else { Some(t) })
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn formats_like_go() {
        assert_eq!(format(&zero()), "0001-01-01T00:00:00Z");
        let t = Utc.with_ymd_and_hms(2026, 9, 1, 10, 2, 3).unwrap();
        assert_eq!(format(&t), "2026-09-01T10:02:03Z");
        let t = t + chrono::Duration::milliseconds(120);
        assert_eq!(format(&t), "2026-09-01T10:02:03.12Z");
        assert_eq!(parse("2026-09-01T10:02:03.12Z"), Some(t));
    }
}
