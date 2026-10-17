//! The check of the cron expression of a schedule trigger: robfig/cron's `ParseStandard` (5 fields, or a descriptor
//! such as `@daily` / `@every 1h`, an optional `TZ=` prefix), with its error messages.

/// Bounds of a field and its names.
struct Bounds {
    min: u64,
    max: u64,
    names: &'static [(&'static str, u64)],
}

const MINUTES: Bounds = Bounds {
    min: 0,
    max: 59,
    names: &[],
};
const HOURS: Bounds = Bounds {
    min: 0,
    max: 23,
    names: &[],
};
const DOM: Bounds = Bounds {
    min: 1,
    max: 31,
    names: &[],
};
const MONTHS: Bounds = Bounds {
    min: 1,
    max: 12,
    names: &[
        ("jan", 1),
        ("feb", 2),
        ("mar", 3),
        ("apr", 4),
        ("may", 5),
        ("jun", 6),
        ("jul", 7),
        ("aug", 8),
        ("sep", 9),
        ("oct", 10),
        ("nov", 11),
        ("dec", 12),
    ],
};
const DOW: Bounds = Bounds {
    min: 0,
    max: 6,
    names: &[
        ("sun", 0),
        ("mon", 1),
        ("tue", 2),
        ("wed", 3),
        ("thu", 4),
        ("fri", 5),
        ("sat", 6),
    ],
};

/// ParseStandard checks a standard cron expression; the error is robfig/cron's message.
pub fn parse_standard(spec: &str) -> Result<(), String> {
    if spec.is_empty() {
        return Err("empty spec string".into());
    }
    let mut spec = spec;
    if spec.starts_with("TZ=") || spec.starts_with("CRON_TZ=") {
        let eq = spec.find('=').unwrap_or(0);
        let Some(i) = spec.find(' ') else {
            return Err(format!(
                "provided bad location {}: unknown time zone {}",
                &spec[eq + 1..],
                &spec[eq + 1..]
            ));
        };
        if eq + 1 > i {
            return Err("provided bad location: unknown time zone".into());
        }
        spec = spec[i..].trim();
    }
    if spec.starts_with('@') {
        return descriptor(spec);
    }
    let fields: Vec<&str> = spec.split_whitespace().collect();
    if fields.len() != 5 {
        return Err(format!(
            "expected exactly 5 fields, found {}: [{}]",
            fields.len(),
            fields.join(" ")
        ));
    }
    for (f, b) in fields.iter().zip([&MINUTES, &HOURS, &DOM, &MONTHS, &DOW]) {
        field(f, b)?;
    }
    Ok(())
}

fn descriptor(d: &str) -> Result<(), String> {
    match d {
        "@yearly" | "@annually" | "@monthly" | "@weekly" | "@daily" | "@midnight" | "@hourly" => {
            Ok(())
        }
        _ => {
            if let Some(rest) = d.strip_prefix("@every ") {
                return match goap_decision::parse_duration(rest) {
                    Ok(_) => Ok(()),
                    Err(e) => Err(format!("failed to parse duration {d}: {e}")),
                };
            }
            Err(format!("unrecognized descriptor: {d}"))
        }
    }
}

fn field(f: &str, b: &Bounds) -> Result<(), String> {
    for expr in f.split(',').filter(|s| !s.is_empty()) {
        range(expr, b)?;
    }
    Ok(())
}

fn range(expr: &str, b: &Bounds) -> Result<(), String> {
    let range_and_step: Vec<&str> = expr.split('/').collect();
    let low_and_high: Vec<&str> = range_and_step[0].split('-').collect();
    let single = low_and_high.len() == 1;
    let (start, mut end);
    if low_and_high[0] == "*" || low_and_high[0] == "?" {
        start = b.min;
        end = b.max;
    } else {
        start = int_or_name(low_and_high[0], b)?;
        end = match low_and_high.len() {
            1 => start,
            2 => int_or_name(low_and_high[1], b)?,
            _ => return Err(format!("too many hyphens: {expr}")),
        };
    }
    let step = match range_and_step.len() {
        1 => 1,
        2 => {
            let s = must_int(range_and_step[1])?;
            if single {
                end = b.max;
            }
            s
        }
        _ => return Err(format!("too many slashes: {expr}")),
    };
    if start < b.min {
        return Err(format!(
            "beginning of range ({start}) below minimum ({}): {expr}",
            b.min
        ));
    }
    if end > b.max {
        return Err(format!(
            "end of range ({end}) above maximum ({}): {expr}",
            b.max
        ));
    }
    if start > end {
        return Err(format!(
            "beginning of range ({start}) beyond end of range ({end}): {expr}"
        ));
    }
    if step == 0 {
        return Err(format!("step of range should be a positive number: {expr}"));
    }
    Ok(())
}

fn int_or_name(expr: &str, b: &Bounds) -> Result<u64, String> {
    let lower = expr.to_lowercase();
    if let Some((_, n)) = b.names.iter().find(|(name, _)| *name == lower) {
        return Ok(*n);
    }
    must_int(expr)
}

fn must_int(expr: &str) -> Result<u64, String> {
    let num: i64 = expr.parse().map_err(|_| {
        format!(
            "failed to parse int from {expr}: strconv.Atoi: parsing {}: invalid syntax",
            crate::util::q(expr)
        )
    })?;
    if num < 0 {
        return Err(format!("negative number ({num}) not allowed: {expr}"));
    }
    Ok(num as u64)
}

#[cfg(test)]
mod tests {
    use super::parse_standard;

    #[test]
    fn standard_specs() {
        for ok in [
            "* * * * *",
            "0 9 * * mon-fri",
            "*/15 0-6 1,15 jan *",
            "@daily",
            "@every 1h",
        ] {
            assert_eq!(parse_standard(ok), Ok(()), "{ok}");
        }
        assert_eq!(
            parse_standard("not a cron").unwrap_err(),
            "expected exactly 5 fields, found 3: [not a cron]"
        );
        assert_eq!(
            parse_standard("61 * * * *").unwrap_err(),
            "end of range (61) above maximum (59): 61"
        );
        assert_eq!(
            parse_standard("@often").unwrap_err(),
            "unrecognized descriptor: @often"
        );
    }
}
