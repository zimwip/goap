//! Go's `time.ParseDuration`.

use chrono::TimeDelta;

use crate::Error;

const OVERFLOW: u64 = 1 << 63;

fn quote(s: &str) -> String {
    format!("{s:?}")
}

fn invalid(orig: &str) -> Error {
    Error::Invalid(format!("time: invalid duration {}", quote(orig)))
}

/// leadingInt consumes the leading [0-9]* of s.
fn leading_int(s: &[u8]) -> Option<(u64, &[u8])> {
    let mut i = 0;
    let mut x: u64 = 0;
    while i < s.len() && s[i].is_ascii_digit() {
        if x > OVERFLOW / 10 {
            return None; // overflow
        }
        x = x * 10 + u64::from(s[i] - b'0');
        if x > OVERFLOW {
            return None; // overflow
        }
        i += 1;
    }
    Some((x, &s[i..]))
}

/// leadingFraction consumes the leading [0-9]* of s: the digits it can hold, the scale of the value, the rest.
fn leading_fraction(s: &[u8]) -> (u64, f64, &[u8]) {
    let mut i = 0;
    let mut x: u64 = 0;
    let mut scale = 1.0;
    let mut overflow = false;
    while i < s.len() && s[i].is_ascii_digit() {
        if !overflow {
            if x > (OVERFLOW - 1) / 10 {
                overflow = true;
            } else {
                let y = x * 10 + u64::from(s[i] - b'0');
                if y > OVERFLOW {
                    overflow = true;
                } else {
                    x = y;
                    scale *= 10.0;
                }
            }
        }
        i += 1;
    }
    (x, scale, &s[i..])
}

fn unit(u: &str) -> Option<u64> {
    Some(match u {
        "ns" => 1,
        "us" | "\u{b5}s" | "\u{3bc}s" => 1_000,
        "ms" => 1_000_000,
        "s" => 1_000_000_000,
        "m" => 60_000_000_000,
        "h" => 3_600_000_000_000,
        _ => return None,
    })
}

/// ParseDuration parses a duration string as Go's `time.ParseDuration` does: a possibly signed sequence of decimal
/// numbers, each with optional fraction and a unit suffix, such as "300ms", "-1.5h" or "2h45m" (units "ns", "us" (or
/// "µs"), "ms", "s", "m", "h"). The errors keep Go's text (`time: invalid duration "x"`).
pub fn parse_duration(orig: &str) -> Result<TimeDelta, Error> {
    let mut s = orig.as_bytes();
    let mut d: u64 = 0;
    let mut neg = false;
    if let Some(&c) = s.first()
        && (c == b'-' || c == b'+')
    {
        neg = c == b'-';
        s = &s[1..];
    }
    if s == b"0" {
        return Ok(TimeDelta::zero());
    }
    if s.is_empty() {
        return Err(invalid(orig));
    }
    while !s.is_empty() {
        // the next character must be [0-9.]
        if !(s[0] == b'.' || s[0].is_ascii_digit()) {
            return Err(invalid(orig));
        }
        // consume [0-9]*
        let pl = s.len();
        let (mut v, rest) = leading_int(s).ok_or_else(|| invalid(orig))?;
        s = rest;
        let pre = pl != s.len();
        // consume (\.[0-9]*)?
        let mut post = false;
        let (mut f, mut scale) = (0u64, 1.0f64);
        if s.first() == Some(&b'.') {
            s = &s[1..];
            let pl = s.len();
            let (ff, sc, rest) = leading_fraction(s);
            (f, scale, s) = (ff, sc, rest);
            post = pl != s.len();
        }
        if !pre && !post {
            // no digits (e.g. ".s" or "-.s")
            return Err(invalid(orig));
        }
        // consume the unit
        let i = s
            .iter()
            .position(|&c| c == b'.' || c.is_ascii_digit())
            .unwrap_or(s.len());
        if i == 0 {
            return Err(Error::Invalid(format!(
                "time: missing unit in duration {}",
                quote(orig)
            )));
        }
        let u = String::from_utf8_lossy(&s[..i]).into_owned();
        s = &s[i..];
        let Some(unit) = unit(&u) else {
            return Err(Error::Invalid(format!(
                "time: unknown unit {} in duration {}",
                quote(&u),
                quote(orig)
            )));
        };
        if v > OVERFLOW / unit {
            return Err(invalid(orig));
        }
        v *= unit;
        if f > 0 {
            // f64 is needed to be nanosecond accurate for fractions of hours; v >= 0 since unit is >= 1
            v += (f as f64 * (unit as f64 / scale)) as u64;
            if v > OVERFLOW {
                return Err(invalid(orig));
            }
        }
        d += v;
        if d > OVERFLOW {
            return Err(invalid(orig));
        }
    }
    if neg {
        return Ok(TimeDelta::nanoseconds((d as i64).wrapping_neg()));
    }
    if d > OVERFLOW - 1 {
        return Err(invalid(orig));
    }
    Ok(TimeDelta::nanoseconds(d as i64))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_as_go() {
        let ok = [
            ("0", 0),
            ("5s", 5_000_000_000),
            ("30m", 1_800_000_000_000),
            ("-1.5h", -5_400_000_000_000),
            ("2h45m", 9_900_000_000_000),
            ("300ms", 300_000_000),
            ("1.000000001s", 1_000_000_001),
            ("1\u{b5}s", 1_000),
            ("+.5s", 500_000_000),
        ];
        for (s, ns) in ok {
            assert_eq!(
                parse_duration(s).unwrap().num_nanoseconds(),
                Some(ns),
                "{s}"
            );
        }
        assert_eq!(
            parse_duration("tomorrow").unwrap_err().to_string(),
            r#"time: invalid duration "tomorrow""#
        );
        assert_eq!(
            parse_duration("1").unwrap_err().to_string(),
            r#"time: missing unit in duration "1""#
        );
        assert_eq!(
            parse_duration("1d").unwrap_err().to_string(),
            r#"time: unknown unit "d" in duration "1d""#
        );
        assert!(parse_duration("").is_err());
        assert!(parse_duration(".s").is_err());
        assert!(parse_duration("9999999999999999999h").is_err());
    }
}
