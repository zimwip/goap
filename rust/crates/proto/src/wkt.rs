//! `google.protobuf.Timestamp` with the protobuf JSON form Go's `protojson` writes: UTC with a `Z`, its fraction in 0, 3,
//! 6 or 9 digits (pbjson-types writes `+00:00`, which the protobuf JSON mapping does not). The other well-known types
//! stay pbjson-types'.
use chrono::{DateTime, Utc};

#[derive(Clone, Copy, PartialEq, Eq, Hash, ::prost::Message)]
pub struct Timestamp {
    #[prost(int64, tag = "1")]
    pub seconds: i64,
    #[prost(int32, tag = "2")]
    pub nanos: i32,
}

impl From<DateTime<Utc>> for Timestamp {
    fn from(t: DateTime<Utc>) -> Timestamp {
        Timestamp {
            seconds: t.timestamp(),
            nanos: t.timestamp_subsec_nanos() as i32,
        }
    }
}

impl TryFrom<Timestamp> for DateTime<Utc> {
    type Error = String;

    fn try_from(t: Timestamp) -> Result<DateTime<Utc>, String> {
        u32::try_from(t.nanos)
            .ok()
            .and_then(|n| DateTime::from_timestamp(t.seconds, n))
            .ok_or_else(|| format!("invalid timestamp {}.{}", t.seconds, t.nanos))
    }
}

/// The text `protojson` gives a timestamp; its error out of 0001-9999 or of its nanoseconds.
fn text(t: Timestamp) -> Result<String, String> {
    // Go's bounds: 0001-01-01T00:00:00Z and 9999-12-31T23:59:59Z
    if !(-62_135_596_800..=253_402_300_799).contains(&t.seconds) {
        return Err(format!("google.protobuf.Timestamp: seconds out of range {}", t.seconds));
    }
    if !(0..1_000_000_000).contains(&t.nanos) {
        return Err(format!("google.protobuf.Timestamp: nanos out of range {}", t.nanos));
    }
    let d: DateTime<Utc> = t.try_into()?;
    let mut x = d.format("%Y-%m-%dT%H:%M:%S%.9f").to_string();
    for suffix in ["000", "000", ".000"] {
        if let Some(s) = x.strip_suffix(suffix) {
            x.truncate(s.len());
        }
    }
    x.push('Z');
    Ok(x)
}

impl serde::Serialize for Timestamp {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&text(*self).map_err(serde::ser::Error::custom)?)
    }
}

impl<'de> serde::Deserialize<'de> for Timestamp {
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Timestamp, D::Error> {
        let s = <std::borrow::Cow<'de, str>>::deserialize(d)?;
        let t = DateTime::parse_from_rfc3339(&s).map_err(serde::de::Error::custom)?;
        Ok(t.with_timezone(&Utc).into())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn writes_what_protojson_writes() {
        let at = |s: &str| Timestamp::from(DateTime::parse_from_rfc3339(s).unwrap().with_timezone(&Utc));
        for (t, want) in [
            ("2026-10-25T06:18:54.003759+00:00", "2026-10-25T06:18:54.003759Z"),
            ("2026-10-25T06:18:54Z", "2026-10-25T06:18:54Z"),
            ("2026-10-25T06:18:54.1Z", "2026-10-25T06:18:54.100Z"),
            ("2026-10-25T08:18:54.123456789+02:00", "2026-10-25T06:18:54.123456789Z"),
        ] {
            assert_eq!(serde_json::to_string(&at(t)).unwrap(), format!("\"{want}\""));
        }
        let back: Timestamp = serde_json::from_str("\"2026-10-25T06:18:54.1Z\"").unwrap();
        assert_eq!(back, at("2026-10-25T06:18:54.1Z"));
    }
}
