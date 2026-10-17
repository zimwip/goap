//! Configuration from the environment, and the service logger.
use std::{fmt::Display, time::Duration};

use parking_lot::RwLock;

/// The environment variable `key`, or `def` when unset or empty.
pub fn env(key: &str, def: &str) -> String {
    match std::env::var(key) {
        Ok(v) if !v.is_empty() => v,
        _ => def.to_string(),
    }
}

/// An integer environment variable (`strconv.Atoi`), or `def`.
pub fn env_int(key: &str, def: i64) -> i64 {
    std::env::var(key)
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(def)
}

/// A boolean environment variable (`strconv.ParseBool`), or `def`.
pub fn env_bool(key: &str, def: bool) -> bool {
    std::env::var(key)
        .ok()
        .and_then(|v| parse_go_bool(&v))
        .unwrap_or(def)
}

/// A duration environment variable (`time.ParseDuration`: `300ms`, `1.5h`, `2h45m`), or `def`. A negative duration,
/// which Go accepts, is no `std::time::Duration`: it gives `def`.
pub fn env_duration(key: &str, def: Duration) -> Duration {
    std::env::var(key)
        .ok()
        .and_then(|v| parse_duration(&v).ok())
        .unwrap_or(def)
}

/// `strconv.ParseBool`: 1, t, T, TRUE, true, True, 0, f, F, FALSE, false, False.
pub fn parse_go_bool(s: &str) -> Option<bool> {
    match s {
        "1" | "t" | "T" | "TRUE" | "true" | "True" => Some(true),
        "0" | "f" | "F" | "FALSE" | "false" | "False" => Some(false),
        _ => None,
    }
}

/// `time.ParseDuration`: a sequence of decimal numbers, each with an optional fraction and a unit (`ns`, `us` / `µs`,
/// `ms`, `s`, `m`, `h`), with an optional `+` sign; `0` alone needs no unit. A negative duration is refused.
pub fn parse_duration(s: &str) -> Result<Duration, String> {
    let bad = || format!("time: invalid duration {s:?}");
    let mut rest = s;
    if let Some(r) = rest.strip_prefix('+') {
        rest = r;
    } else if rest.starts_with('-') {
        return Err(format!("time: negative duration {s:?}"));
    }
    if rest == "0" {
        return Ok(Duration::ZERO);
    }
    if rest.is_empty() {
        return Err(bad());
    }
    let mut total: u128 = 0;
    while !rest.is_empty() {
        let int_len = rest.bytes().take_while(u8::is_ascii_digit).count();
        let int = &rest[..int_len];
        rest = &rest[int_len..];
        let mut frac = "";
        if let Some(r) = rest.strip_prefix('.') {
            let n = r.bytes().take_while(u8::is_ascii_digit).count();
            frac = &r[..n];
            rest = &r[n..];
        }
        if int.is_empty() && frac.is_empty() {
            return Err(bad());
        }
        let unit_len = rest
            .find(|c: char| c == '.' || c.is_ascii_digit())
            .unwrap_or(rest.len());
        let unit: u128 = match &rest[..unit_len] {
            "ns" => 1,
            "us" | "\u{b5}s" | "\u{3bc}s" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            "" => return Err(format!("time: missing unit in duration {s:?}")),
            u => return Err(format!("time: unknown unit {u:?} in duration {s:?}")),
        };
        rest = &rest[unit_len..];
        let whole: u128 = if int.is_empty() {
            0
        } else {
            int.parse().map_err(|_| bad())?
        };
        let mut v = whole.checked_mul(unit).ok_or_else(bad)?;
        // the fraction, truncated to the nanosecond as Go does
        let mut scale = unit;
        for d in frac.bytes() {
            scale /= 10;
            if scale == 0 {
                break;
            }
            v += u128::from(d - b'0') * scale;
        }
        total = total.checked_add(v).ok_or_else(bad)?;
    }
    if total > i64::MAX as u128 {
        return Err(bad());
    }
    Ok(Duration::from_nanos(total as u64))
}

static SERVICE_NAME: RwLock<String> = RwLock::new(String::new());

/// The name given to [`logger`] (`goap` before).
pub fn service_name() -> String {
    let n = SERVICE_NAME.read();
    if n.is_empty() {
        "goap".to_string()
    } else {
        n.clone()
    }
}

/// Installs the service logger as the global `tracing` subscriber: JSON on stdout unless `GOAP_LOG_FORMAT=text`,
/// level info (`GOAP_LOG_LEVEL=debug`: debug). A second call only changes the service name.
pub fn logger(service: &str) {
    *SERVICE_NAME.write() = service.to_string();
    let level = if std::env::var("GOAP_LOG_LEVEL").is_ok_and(|v| v.eq_ignore_ascii_case("debug")) {
        tracing::Level::DEBUG
    } else {
        tracing::Level::INFO
    };
    let b = tracing_subscriber::fmt()
        .with_max_level(level)
        .with_writer(std::io::stdout);
    let _ = if std::env::var("GOAP_LOG_FORMAT").is_ok_and(|v| v == "text") {
        b.try_init()
    } else {
        b.json().flatten_event(true).try_init()
    };
    tracing::debug!(service, "logger ready");
}

/// Logs and exits (status 1).
pub fn fatal(msg: &str, err: impl Display) -> ! {
    tracing::error!(service = %service_name(), err = %err, "{msg}");
    std::process::exit(1)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations_as_go_reads_them() {
        let ok = [
            ("0", Duration::ZERO),
            ("5s", Duration::from_secs(5)),
            ("+5s", Duration::from_secs(5)),
            ("300ms", Duration::from_millis(300)),
            ("1.5h", Duration::from_secs(5400)),
            ("2h45m", Duration::from_secs(9900)),
            ("1h2m3.5s", Duration::from_millis(3_723_500)),
            (".5s", Duration::from_millis(500)),
            ("10us", Duration::from_micros(10)),
            ("10\u{b5}s", Duration::from_micros(10)),
            ("7ns", Duration::from_nanos(7)),
            ("1.0000000001s", Duration::from_secs(1)),
        ];
        for (s, d) in ok {
            assert_eq!(parse_duration(s), Ok(d), "{s}");
        }
        for s in ["", "5", "s", "1x", "-1s", ".s", "1.5", "9999999999999h"] {
            assert!(parse_duration(s).is_err(), "{s}");
        }
    }

    #[test]
    fn booleans_as_go_reads_them() {
        assert_eq!(parse_go_bool("True"), Some(true));
        assert_eq!(parse_go_bool("0"), Some(false));
        assert_eq!(parse_go_bool("yes"), None);
    }

    #[test]
    fn env_defaults() {
        assert_eq!(env("GOAP_PLATFORM_TEST_UNSET_VAR", "def"), "def");
        assert_eq!(env_int("GOAP_PLATFORM_TEST_UNSET_VAR", 7), 7);
        assert!(env_bool("GOAP_PLATFORM_TEST_UNSET_VAR", true));
        assert_eq!(
            env_duration("GOAP_PLATFORM_TEST_UNSET_VAR", Duration::from_secs(1)),
            Duration::from_secs(1)
        );
    }
}
