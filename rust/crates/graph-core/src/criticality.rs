//! The criticality of a change and what the organisation requires at each level (Go's `contracts/criticality`, ADR
//! 0075 §3): the kinds of oracle that may verify an effect and how long a derogation may last. The levels are C1 < C2 <
//! C3, a change naming none holds C2; the policy of a level is organisation data (`organisation@CriticalityPolicy`,
//! read by the access snapshot), else the compiled-in table.
use chrono::{DateTime, NaiveDate, TimeDelta, Utc};
use serde_json::{Map, Value};

/// The levels, the lowest first (`criticality.Levels`).
pub const LEVELS: [&str; 3] = ["C1", "C2", "C3"];

/// The level of a change that names none (`criticality.Default`).
pub const DEFAULT: &str = "C2";

/// The key of the level in the data of a change (`domain.DataCriticality`).
pub const DATA_CRITICALITY: &str = "criticality";

/// The rank of a level (0: not a level; `criticality.Rank`).
pub fn rank(l: &str) -> usize {
    LEVELS.iter().position(|x| *x == l).map_or(0, |i| i + 1)
}

/// The level the data of a change holds, the default when it names none or an unknown one (`criticality.Of`).
pub fn of(data: Option<&Map<String, Value>>) -> &'static str {
    let named = data.and_then(|d| d.get(DATA_CRITICALITY)).and_then(Value::as_str);
    LEVELS.into_iter().find(|l| Some(*l) == named).unwrap_or(DEFAULT)
}

/// What the organisation requires of the changes of one level (`criticality.Policy`).
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Policy {
    /// The kinds of oracle accepted to verify an effect; empty: any.
    pub oracles: Vec<String>,
    /// Whether a human review may be sampled.
    pub sampling: bool,
    /// The role whoever signs a derogation holds on the project of the change; empty: any signatory.
    pub signatory_role: String,
    /// The longest a derogation may last from its signature; zero: unbounded.
    pub max_derogation: TimeDelta,
}

impl Policy {
    /// Whether the oracle kind may verify an effect (`Policy.Accepts`).
    pub fn accepts(&self, oracle: &str) -> bool {
        self.oracles.is_empty() || self.oracles.iter().any(|o| o == oracle)
    }

    /// The compiled-in policy of a level (`criticality.Defaults`): C1 loose, C2 a tool or a person, C3 a person and a
    /// signatory of the role `derogation_signatory`.
    pub fn default_of(level: &str) -> Policy {
        let (oracles, sampling, role, days): (&[&str], bool, &str, i64) = match level {
            "C1" => (&["tool", "model", "human"], true, "", 30),
            "C2" => (&["tool", "human"], true, "", 14),
            "C3" => (&["human"], false, "derogation_signatory", 7),
            _ => return Policy::default(),
        };
        Policy {
            oracles: oracles.iter().map(|o| o.to_string()).collect(),
            sampling,
            signatory_role: role.into(),
            max_derogation: TimeDelta::days(days),
        }
    }

    /// The policy as an expression sees it (`Policy.Map`): its longest derogation in whole hours.
    pub fn map(&self) -> Map<String, Value> {
        let mut m = Map::new();
        m.insert("oracles".into(), self.oracles.clone().into());
        m.insert("sampling".into(), self.sampling.into());
        m.insert("signatoryRole".into(), self.signatory_role.clone().into());
        m.insert("maxDerogationHours".into(), self.max_derogation.num_hours().into());
        m
    }

    /// The policy of its map form, the reverse of [`Policy::map`].
    pub fn from_map(m: &Value) -> Policy {
        Policy {
            oracles: m["oracles"]
                .as_array()
                .map(|l| l.iter().filter_map(Value::as_str).map(str::to_string).collect())
                .unwrap_or_default(),
            sampling: m["sampling"].as_bool() == Some(true),
            signatory_role: m["signatoryRole"].as_str().unwrap_or_default().into(),
            max_derogation: TimeDelta::hours(m["maxDerogationHours"].as_f64().unwrap_or_default() as i64),
        }
    }

    /// The level and the policy of a `CriticalityPolicy` node (`access.CriticalityPolicyFromProps`).
    pub fn from_props(props: Option<&Map<String, Value>>) -> Result<(&'static str, Policy), String> {
        let get = |k: &str| props.and_then(|p| p.get(k));
        let named = get("level").and_then(Value::as_str).unwrap_or_default();
        let Some(level) = LEVELS.into_iter().find(|l| *l == named) else {
            return Err(format!("criticality policy: unknown level {}", quote(named)));
        };
        let oracles = match get("oracles") {
            None | Some(Value::Null) => Vec::new(),
            Some(Value::Array(l)) => l
                .iter()
                .filter_map(Value::as_str)
                .filter(|s| !s.is_empty())
                .map(str::to_string)
                .collect(),
            Some(_) => return Err("criticality policy: oracles must be a list of strings".into()),
        };
        let hours = match get("maxDerogationHours") {
            None | Some(Value::Null) => 0.0,
            Some(Value::Number(n)) => n.as_f64().unwrap_or_default(),
            Some(_) => return Err("criticality policy: maxDerogationHours must be a number".into()),
        };
        if hours < 0.0 {
            return Err("criticality policy: maxDerogationHours must not be negative".into());
        }
        Ok((
            level,
            Policy {
                oracles,
                sampling: get("sampling").and_then(Value::as_bool) == Some(true),
                signatory_role: get("signatoryRole").and_then(Value::as_str).unwrap_or_default().into(),
                max_derogation: TimeDelta::nanoseconds((hours * 3.6e12) as i64),
            },
        ))
    }
}

/// The expiry of a derogation (`risk.ParseExpiry`): RFC 3339, or a date standing for the end of that day, UTC.
pub fn parse_expiry(s: &str) -> Result<DateTime<Utc>, String> {
    let s = s.trim();
    if let Ok(t) = DateTime::parse_from_rfc3339(s)
        && s.as_bytes().get(10) == Some(&b'T')
    {
        return Ok(t.with_timezone(&Utc));
    }
    let b = s.as_bytes();
    let date_only = b.len() == 10
        && b[4] == b'-'
        && b[7] == b'-'
        && b.iter()
            .enumerate()
            .all(|(i, c)| i == 4 || i == 7 || c.is_ascii_digit());
    if date_only && let Ok(d) = NaiveDate::parse_from_str(s, "%Y-%m-%d") {
        let end = d.and_hms_opt(0, 0, 0).expect("midnight") + TimeDelta::days(1) - TimeDelta::nanoseconds(1);
        return Ok(end.and_utc());
    }
    Err(format!(
        "expiry {} is neither a date (YYYY-MM-DD) nor RFC 3339",
        quote(s)
    ))
}

/// A time as Go formats it in RFC 3339 (no fraction of a second).
pub fn rfc3339(t: DateTime<Utc>) -> String {
    t.format("%Y-%m-%dT%H:%M:%SZ").to_string()
}

/// The check the graph asks of an item about to be written (`criticality.ItemPolicy`): a verification of an effect
/// names an oracle the policy of the change's level accepts, a derogation lasts no longer than it allows. `at` is when
/// the item says it was made (else now); `resolve` gives the policy of a level.
pub fn item_policy(
    level: &str,
    resolve: impl Fn(&str) -> Policy,
    typ: &str,
    data: Option<&Map<String, Value>>,
    at: DateTime<Utc>,
) -> Result<(), String> {
    let text = |k: &str| data.and_then(|d| d.get(k)).and_then(Value::as_str).unwrap_or_default();
    match typ {
        "verification" => {
            let oracle = text("oracle");
            if text("state") != "produced" || oracle.is_empty() {
                return Ok(());
            }
            let p = resolve(level);
            if !p.accepts(oracle) {
                return Err(format!(
                    "a {oracle} oracle cannot verify the effects of a {level} change (accepted: [{}])",
                    p.oracles.join(" ")
                ));
            }
        }
        "derogation" => {
            if text("status") == "closed" {
                return Ok(());
            }
            let p = resolve(level);
            if p.max_derogation <= TimeDelta::zero() {
                return Ok(());
            }
            let Ok(t) = parse_expiry(text("expires")) else {
                return Ok(()); // the validation of the derogation says so
            };
            if t - at > p.max_derogation {
                return Err(format!(
                    "a derogation on a {level} change lasts {} at most (policy of the organisation), not until {}",
                    go_duration(p.max_derogation),
                    rfc3339(t)
                ));
            }
        }
        _ => {}
    }
    Ok(())
}

/// A string as Go's `%q` writes it (the JSON form for what an item holds).
fn quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// A duration as Go's `time.Duration.String` writes it: `336h0m0s`, `1m30s`, `1.5s`, `250ms`.
pub fn go_duration(d: TimeDelta) -> String {
    let Some(n) = d.num_nanoseconds() else {
        return format!("{}h0m0s", d.num_hours());
    };
    if n == 0 {
        return "0s".into();
    }
    let (sign, n) = if n < 0 { ("-", n.unsigned_abs()) } else { ("", n as u64) };
    let fraction = |whole: u64, part: u64, digits: usize| {
        let f = format!("{part:0digits$}");
        let f = f.trim_end_matches('0');
        if f.is_empty() {
            whole.to_string()
        } else {
            format!("{whole}.{f}")
        }
    };
    if n < 1_000_000_000 {
        let (unit, scale, digits) = match n {
            0..1_000 => ("ns", 1, 0),
            1_000..1_000_000 => ("µs", 1_000, 3),
            _ => ("ms", 1_000_000, 6),
        };
        return format!("{sign}{}{unit}", fraction(n / scale, n % scale, digits));
    }
    let (h, rest) = (n / 3_600_000_000_000, n % 3_600_000_000_000);
    let (m, rest) = (rest / 60_000_000_000, rest % 60_000_000_000);
    let s = fraction(rest / 1_000_000_000, rest % 1_000_000_000, 9);
    match (h, m) {
        (0, 0) => format!("{sign}{s}s"),
        (0, m) => format!("{sign}{m}m{s}s"),
        (h, m) => format!("{sign}{h}h{m}m{s}s"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn durations_read_as_go_writes_them() {
        assert_eq!(go_duration(TimeDelta::days(14)), "336h0m0s");
        assert_eq!(go_duration(TimeDelta::minutes(90)), "1h30m0s");
        assert_eq!(go_duration(TimeDelta::milliseconds(1500)), "1.5s");
        assert_eq!(go_duration(TimeDelta::milliseconds(250)), "250ms");
    }

    #[test]
    fn a_date_expires_at_the_end_of_its_day() {
        let t = parse_expiry(" 2026-11-30 ").unwrap();
        assert_eq!(rfc3339(t), "2026-11-30T23:59:59Z");
        assert!(parse_expiry("2026-1-30").is_err());
        assert_eq!(
            rfc3339(parse_expiry("2026-11-30T10:00:00+01:00").unwrap()),
            "2026-11-30T09:00:00Z"
        );
    }

    #[test]
    fn the_policy_bounds_oracles_and_derogations() {
        let at = parse_expiry("2026-01-01T00:00:00Z").unwrap();
        let d = json!({"state": "produced", "oracle": "model"});
        let e = item_policy("C2", Policy::default_of, "verification", d.as_object(), at).unwrap_err();
        assert_eq!(
            e,
            "a model oracle cannot verify the effects of a C2 change (accepted: [tool human])"
        );
        let d = json!({"expires": "2026-02-01"});
        let e = item_policy("C3", Policy::default_of, "derogation", d.as_object(), at).unwrap_err();
        assert_eq!(
            e,
            "a derogation on a C3 change lasts 168h0m0s at most (policy of the organisation), not until 2026-02-01T23:59:59Z"
        );
        let d = json!({"expires": "2026-01-05"});
        assert!(item_policy("C3", Policy::default_of, "derogation", d.as_object(), at).is_ok());
    }
}
