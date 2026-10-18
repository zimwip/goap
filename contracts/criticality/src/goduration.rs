//! Go's `time.Duration`: its JSON form (an integer of nanoseconds) and its `String`.

use chrono::TimeDelta;
use serde::{Deserialize, Deserializer, Serializer};

/// Serializes a duration as Go encodes a time.Duration: an integer of nanoseconds.
pub(crate) fn serialize<S: Serializer>(d: &TimeDelta, s: S) -> Result<S::Ok, S::Error> {
    s.serialize_i64(d.num_nanoseconds().unwrap_or(i64::MAX))
}

/// Reads a duration of nanoseconds (null: zero).
pub(crate) fn deserialize<'de, D: Deserializer<'de>>(d: D) -> Result<TimeDelta, D::Error> {
    Ok(TimeDelta::nanoseconds(
        Option::<i64>::deserialize(d)?.unwrap_or_default(),
    ))
}

pub(crate) fn is_zero(d: &TimeDelta) -> bool {
    d.is_zero()
}

/// The fraction of v of prec digits, trailing zeros dropped ("" when it is zero), and v without it.
fn frac(v: u64, prec: u32) -> (String, u64) {
    let p = 10u64.pow(prec);
    let f = v % p;
    if f == 0 {
        return (String::new(), v / p);
    }
    let digits = format!("{f:0width$}", width = prec as usize);
    (format!(".{}", digits.trim_end_matches('0')), v / p)
}

/// The text Go's `time.Duration.String` gives of a duration: "72h3m0.5s", "1.5ms", "0s".
pub fn go_duration_string(d: TimeDelta) -> String {
    let ns = d.num_nanoseconds().unwrap_or(i64::MAX);
    let neg = ns < 0;
    let u = ns.unsigned_abs();
    let mut out = if u < 1_000_000_000 {
        // a duration under a second is written in a smaller unit (0.5s is 500ms)
        if u == 0 {
            return "0s".into();
        } else if u < 1_000 {
            format!("{u}ns")
        } else if u < 1_000_000 {
            let (f, i) = frac(u, 3);
            format!("{i}{f}\u{b5}s")
        } else {
            let (f, i) = frac(u, 6);
            format!("{i}{f}ms")
        }
    } else {
        let (f, secs) = frac(u, 9);
        let mut s = format!("{}{f}s", secs % 60);
        let mins = secs / 60;
        if mins > 0 {
            s = format!("{}m{s}", mins % 60);
            let hours = mins / 60;
            if hours > 0 {
                s = format!("{hours}h{s}");
            }
        }
        s
    };
    if neg {
        out.insert(0, '-');
    }
    out
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn writes_as_go() {
        for (ns, want) in [
            (0, "0s"),
            (1, "1ns"),
            (1_100, "1.1\u{b5}s"),
            (2_200_000, "2.2ms"),
            (3_300_000_000, "3.3s"),
            (4 * 60_000_000_000 + 5_000_000_000, "4m5s"),
            (
                5 * 3_600_000_000_000 + 6 * 60_000_000_000 + 7_001_000_000,
                "5h6m7.001s",
            ),
            (7 * 24 * 3_600_000_000_000, "168h0m0s"),
            (-1_500_000_000, "-1.5s"),
        ] {
            assert_eq!(go_duration_string(TimeDelta::nanoseconds(ns)), want);
        }
    }
}
