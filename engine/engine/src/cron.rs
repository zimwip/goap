//! A cron schedule as robfig/cron's standard parser reads it (the schedule triggers, UTC): five fields (minute,
//! hour, day of month, month, day of week) with `*`, `?`, lists, ranges, steps and names, or a descriptor
//! (`@yearly`, `@annually`, `@monthly`, `@weekly`, `@daily`, `@midnight`, `@hourly`, `@every <duration>`).

use chrono::{DateTime, Datelike, Duration, Timelike, Utc};

/// Schedule is a parsed cron expression.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Schedule {
    Fields {
        minute: u64,
        hour: u64,
        dom: u64,
        month: u64,
        dow: u64,
        /// A day field starting with `*` (or `?`): robfig's star bit, telling whether both day fields restrict.
        dom_star: bool,
        dow_star: bool,
    },
    Every(Duration),
}

const MONTHS: [&str; 12] = [
    "jan", "feb", "mar", "apr", "may", "jun", "jul", "aug", "sep", "oct", "nov", "dec",
];
const DAYS: [&str; 7] = ["sun", "mon", "tue", "wed", "thu", "fri", "sat"];

/// parse reads a cron expression; the error texts follow robfig/cron's.
pub fn parse(spec: &str) -> Result<Schedule, String> {
    let spec = spec.trim();
    if spec.is_empty() {
        return Err("empty spec string".into());
    }
    if let Some(d) = spec.strip_prefix('@') {
        return descriptor(d, spec);
    }
    let fields: Vec<&str> = spec.split_whitespace().collect();
    if fields.len() != 5 {
        return Err(format!(
            "expected exactly 5 fields, found {}: [{}]",
            fields.len(),
            fields.join(" ")
        ));
    }
    let names_m: Vec<(&str, u32)> = MONTHS
        .iter()
        .enumerate()
        .map(|(i, n)| (*n, i as u32 + 1))
        .collect();
    let names_d: Vec<(&str, u32)> = DAYS
        .iter()
        .enumerate()
        .map(|(i, n)| (*n, i as u32))
        .collect();
    let minute = field(fields[0], 0, 59, &[])?;
    let hour = field(fields[1], 0, 23, &[])?;
    let dom = field(fields[2], 1, 31, &[])?;
    let month = field(fields[3], 1, 12, &names_m)?;
    let dow = field(fields[4], 0, 6, &names_d)?;
    let star = |f: &str| f.starts_with('*') || f.starts_with('?');
    Ok(Schedule::Fields {
        minute,
        hour,
        dom,
        month,
        dow,
        dom_star: star(fields[2]),
        dow_star: star(fields[4]),
    })
}

fn descriptor(d: &str, spec: &str) -> Result<Schedule, String> {
    let f = |m: &str, h: &str, dom: &str, mo: &str, dow: &str| {
        parse(&format!("{m} {h} {dom} {mo} {dow}"))
    };
    match d {
        "yearly" | "annually" => f("0", "0", "1", "1", "*"),
        "monthly" => f("0", "0", "1", "*", "*"),
        "weekly" => f("0", "0", "*", "*", "0"),
        "daily" | "midnight" => f("0", "0", "*", "*", "*"),
        "hourly" => f("0", "*", "*", "*", "*"),
        _ => {
            if let Some(rest) = d.strip_prefix("every ") {
                let dur = parse_duration(rest.trim())
                    .ok_or_else(|| format!("failed to parse duration {spec}: {rest}"))?;
                // robfig rounds to the second, at least one
                let secs = dur.num_seconds().max(1);
                return Ok(Schedule::Every(Duration::seconds(secs)));
            }
            Err(format!("unrecognized descriptor: {spec}"))
        }
    }
}

/// parse_duration reads Go's time.ParseDuration form ("1h30m", "90s", "1.5h").
fn parse_duration(s: &str) -> Option<Duration> {
    if s.is_empty() {
        return None;
    }
    let mut total = 0f64;
    let mut rest = s;
    while !rest.is_empty() {
        let end = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        if end == 0 {
            return None;
        }
        let n: f64 = rest[..end].parse().ok()?;
        rest = &rest[end..];
        let uend = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let unit = &rest[..uend];
        rest = &rest[uend..];
        let mul = match unit {
            "ns" => 1e-9,
            "us" | "µs" => 1e-6,
            "ms" => 1e-3,
            "s" => 1.0,
            "m" => 60.0,
            "h" => 3600.0,
            _ => return None,
        };
        total += n * mul;
    }
    Some(Duration::milliseconds((total * 1000.0) as i64))
}

fn field(f: &str, min: u32, max: u32, names: &[(&str, u32)]) -> Result<u64, String> {
    let mut bits = 0u64;
    for part in f.split(',') {
        bits |= range(part, min, max, names)?;
    }
    Ok(bits)
}

fn value(s: &str, names: &[(&str, u32)]) -> Result<u32, String> {
    let l = s.to_lowercase();
    if let Some((_, v)) = names.iter().find(|(n, _)| *n == l) {
        return Ok(*v);
    }
    s.parse::<u32>().map_err(|_| {
        format!("failed to parse int from {s}: strconv.Atoi: parsing {s:?}: invalid syntax")
    })
}

fn range(part: &str, min: u32, max: u32, names: &[(&str, u32)]) -> Result<u64, String> {
    let (r, step) = match part.split_once('/') {
        Some((r, s)) => (r, Some(s)),
        None => (part, None),
    };
    let (start, mut end, star) = if r == "*" || r == "?" {
        (min, max, true)
    } else if let Some((a, b)) = r.split_once('-') {
        (value(a, names)?, value(b, names)?, false)
    } else {
        let v = value(r, names)?;
        (v, v, false)
    };
    let step = match step {
        Some(s) => {
            let n = s
                .parse::<u32>()
                .map_err(|_| format!("failed to parse int from {s}"))?;
            if !star && !r.contains('-') {
                end = max;
            }
            n
        }
        None => 1,
    };
    if start < min {
        return Err(format!(
            "beginning of range ({start}) below minimum ({min}): {part}"
        ));
    }
    if end > max {
        return Err(format!(
            "end of range ({end}) above maximum ({max}): {part}"
        ));
    }
    if start > end {
        return Err(format!(
            "beginning of range ({start}) beyond end of range ({end}): {part}"
        ));
    }
    if step == 0 {
        return Err(format!("step of range should be a positive number: {part}"));
    }
    let mut bits = 0u64;
    let mut v = start;
    while v <= end {
        bits |= 1 << v;
        v += step;
    }
    Ok(bits)
}

impl Schedule {
    /// next is the first activation strictly after t (None when none within five years).
    pub fn next(&self, t: DateTime<Utc>) -> Option<DateTime<Utc>> {
        match self {
            Schedule::Every(d) => {
                let base = t.with_nanosecond(0).unwrap_or(t);
                Some(base + *d)
            }
            Schedule::Fields {
                minute,
                hour,
                dom,
                month,
                dow,
                dom_star,
                dow_star,
            } => {
                let mut c = t.with_nanosecond(0)?.with_second(0)? + Duration::minutes(1);
                let limit = t + Duration::days(366 * 5);
                let has = |bits: u64, v: u32| bits & (1 << v) != 0;
                while c <= limit {
                    if !has(*month, c.month()) {
                        // first minute of the next month
                        let (y, m) = if c.month() == 12 {
                            (c.year() + 1, 1)
                        } else {
                            (c.year(), c.month() + 1)
                        };
                        c = chrono::NaiveDate::from_ymd_opt(y, m, 1)?
                            .and_hms_opt(0, 0, 0)?
                            .and_utc();
                        continue;
                    }
                    let dom_ok = has(*dom, c.day());
                    let dow_ok = has(*dow, c.weekday().num_days_from_sunday());
                    let day_ok = if *dom_star || *dow_star {
                        dom_ok && dow_ok
                    } else {
                        dom_ok || dow_ok
                    };
                    if !day_ok {
                        c = (c.date_naive() + Duration::days(1))
                            .and_hms_opt(0, 0, 0)?
                            .and_utc();
                        continue;
                    }
                    if !has(*hour, c.hour()) {
                        c = c.with_minute(0)? + Duration::hours(1);
                        continue;
                    }
                    if !has(*minute, c.minute()) {
                        c += Duration::minutes(1);
                        continue;
                    }
                    return Some(c);
                }
                None
            }
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn at(s: &str) -> DateTime<Utc> {
        DateTime::parse_from_rfc3339(s).unwrap().with_timezone(&Utc)
    }

    #[test]
    fn next_activations() {
        let s = parse("*/15 * * * *").unwrap();
        assert_eq!(
            s.next(at("2026-01-01T10:07:30Z")),
            Some(at("2026-01-01T10:15:00Z"))
        );
        let s = parse("0 9 * * mon-fri").unwrap();
        assert_eq!(
            s.next(at("2026-01-02T10:00:00Z")),
            Some(at("2026-01-05T09:00:00Z"))
        );
        let s = parse("@daily").unwrap();
        assert_eq!(
            s.next(at("2026-01-02T10:00:00Z")),
            Some(at("2026-01-03T00:00:00Z"))
        );
        let s = parse("@every 1h30m").unwrap();
        assert_eq!(
            s.next(at("2026-01-02T10:00:00Z")),
            Some(at("2026-01-02T11:30:00Z"))
        );
        assert!(parse("* * *").is_err());
        assert!(parse("61 * * * *").is_err());
    }
}
