//! Derogations: waivers with a rule, a signatory and an expiry (ADR 0075 §2).

use chrono::{DateTime, NaiveDate, SecondsFormat, TimeDelta, Utc};
use goap_domain::{Change, ChangeItem, ItemID, ItemType, gotime};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    Error,
    risk::{data_string, register_of},
};

/// TypeDerogation is a waiver with a rule, a signatory and an expiry (ADR 0075 §2): the decision to accept a gap (a
/// waived postcondition, gate criterion or policy rule) for a bounded time, by a person who answers for it. It names
/// what it waives, covers a target, is signed and expires. It is an item of the change versioned by its key like a
/// risk: closing it adds a version.
///
/// Data: key, rule (what is waived), target (the change impact, action run or gate it applies to), reason, signatory
/// (the principal who answers for it, checked by the ABAC permission derogation:sign when the item is written),
/// expires (RFC 3339, or a date: the end of that day, UTC) and status (open, the default, or closed). Every version
/// restates rule, target, reason, signatory and expires.
pub const TYPE_DEROGATION: ItemType = ItemType::from_static("derogation");

/// PermissionSign is the permission asked of whoever writes a derogation (ABAC resource "derogation", action "sign").
pub const PERMISSION_SIGN: &str = "derogation:sign";

// Derogation statuses.
pub const DEROGATION_OPEN: &str = "open";
pub const DEROGATION_CLOSED: &str = "closed";

/// DerogationStatuses lists the statuses a derogation may have.
pub const DEROGATION_STATUSES: &[&str] = &[DEROGATION_OPEN, DEROGATION_CLOSED];

/// Derogation is the current version of a derogation of the change.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Derogation {
    pub key: String,
    pub rule: String,
    pub target: String,
    pub reason: String,
    pub signatory: String,
    /// The expiry (Go's zero time when it did not parse: expired).
    #[serde(with = "gotime")]
    pub expires: DateTime<Utc>,
    pub status: String,
    /// Item is the item holding this version, By what produced it, Versions how many versions the key has.
    pub item: ItemID,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    pub versions: i64,
}

impl Default for Derogation {
    fn default() -> Self {
        Derogation {
            key: String::new(),
            rule: String::new(),
            target: String::new(),
            reason: String::new(),
            signatory: String::new(),
            expires: gotime::zero(),
            status: String::new(),
            item: ItemID::default(),
            by: String::new(),
            versions: 0,
        }
    }
}

impl Derogation {
    /// Open reports whether the derogation was not closed.
    pub fn open(&self) -> bool {
        self.status != DEROGATION_CLOSED
    }

    /// ExpiredAt reports whether the derogation, still open, ran out at now.
    pub fn expired_at(&self, now: DateTime<Utc>) -> bool {
        self.open() && self.expires <= now
    }

    /// InForce reports whether the derogation is open and has not run out at now: only then it covers anything.
    pub fn in_force(&self, now: DateTime<Utc>) -> bool {
        self.open() && self.expires > now
    }

    /// ClosingData returns the data of the version that closes a derogation.
    pub fn closing_data(&self, reason: &str) -> Map<String, Value> {
        let mut m = Map::new();
        m.insert("key".into(), self.key.clone().into());
        m.insert("rule".into(), self.rule.clone().into());
        m.insert("target".into(), self.target.clone().into());
        m.insert("reason".into(), self.reason.clone().into());
        m.insert("signatory".into(), self.signatory.clone().into());
        m.insert("expires".into(), rfc3339(&self.expires).into());
        m.insert("status".into(), DEROGATION_CLOSED.into());
        m.insert("closedBecause".into(), reason.into());
        m
    }
}

/// Go's `t.Format(time.RFC3339)`.
fn rfc3339(t: &DateTime<Utc>) -> String {
    t.to_rfc3339_opts(SecondsFormat::Secs, true)
}

/// ParseExpiry reads the expiry of a derogation: RFC 3339, or a date (YYYY-MM-DD) meaning the end of that day, UTC.
pub fn parse_expiry(s: &str) -> Result<DateTime<Utc>, Error> {
    let s = s.trim();
    if let Ok(t) = DateTime::parse_from_rfc3339(s) {
        return Ok(t.with_timezone(&Utc));
    }
    if let Ok(d) = NaiveDate::parse_from_str(s, "%Y-%m-%d")
        && let Some(start) = d.and_hms_opt(0, 0, 0)
    {
        return Ok(start.and_utc() + TimeDelta::hours(24) - TimeDelta::nanoseconds(1));
    }
    Err(Error::Invalid(format!(
        "expiry {s:?} is neither a date (YYYY-MM-DD) nor RFC 3339"
    )))
}

/// validateDerogation checks the data of a derogation item: no reason, rule, target, signatory or expiry, no
/// derogation; an open version must expire in the future (a closing version may carry the expiry that passed).
pub(crate) fn validate_derogation(it: &ChangeItem) -> Result<(), goap_domain::Error> {
    let d = &it.data;
    for f in ["key", "rule", "target", "reason", "signatory", "expires"] {
        if data_string(d, f).is_empty() {
            return Err(goap_domain::Error::invalid(format!(
                "derogation item requires data.{f}"
            )));
        }
    }
    let status = data_string(d, "status");
    if !status.is_empty() && !DEROGATION_STATUSES.contains(&status.as_str()) {
        return Err(goap_domain::Error::invalid(format!(
            "derogation status must be one of {}",
            DEROGATION_STATUSES.join(", ")
        )));
    }
    let exp = parse_expiry(&data_string(d, "expires"))
        .map_err(|e| goap_domain::Error::invalid(format!("derogation: {e}")))?;
    if status != DEROGATION_CLOSED && exp <= Utc::now() {
        return Err(goap_domain::Error::invalid(format!(
            "derogation expires in the past ({})",
            rfc3339(&exp)
        )));
    }
    Ok(())
}

/// Derogations returns the derogations of the change: the current version of each key, in the order they were
/// signed.
pub fn derogations(c: &Change) -> Vec<Derogation> {
    let r = register_of(c, &TYPE_DEROGATION);
    r.order
        .iter()
        .map(|key| {
            let it = &r.last[key];
            let d = &it.data;
            // validated on write; an unreadable one is the zero time: expired
            let exp = parse_expiry(&data_string(d, "expires")).unwrap_or_else(|_| gotime::zero());
            let mut dr = Derogation {
                key: key.clone(),
                rule: data_string(d, "rule"),
                target: data_string(d, "target"),
                reason: data_string(d, "reason"),
                signatory: data_string(d, "signatory"),
                expires: exp,
                status: data_string(d, "status"),
                item: it.id.clone(),
                by: it.produced_by.clone(),
                versions: r.versions[key],
            };
            if dr.status.is_empty() {
                dr.status = DEROGATION_OPEN.into();
            }
            dr
        })
        .collect()
}

/// Expired returns the derogations of the change that are open and ran out at now: what the platform must close,
/// sending what they covered back to review. It is pure, the clock is the caller's (never the graph's, ADR 0075).
pub fn expired(c: &Change, now: DateTime<Utc>) -> Vec<Derogation> {
    derogations(c)
        .into_iter()
        .filter(|d| d.expired_at(now))
        .collect()
}

/// Covering returns the derogation in force at now whose target is one of targets (a change impact id or key, an
/// action name, an execution), if there is one. The rule is not matched: it names what is waived, a human reads it.
pub fn covering(c: &Change, now: DateTime<Utc>, targets: &[&str]) -> Option<Derogation> {
    derogations(c)
        .into_iter()
        .find(|d| d.in_force(now) && targets.contains(&d.target.as_str()))
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;
    use goap_domain::item_permission_of;
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => unreachable!(),
        }
    }

    fn derogation(extra: Value) -> ChangeItem {
        let mut d = obj(
            json!({"key": "DRG-1", "rule": "tests pass", "target": "REQ-1", "reason": "deadline",
            "signatory": "alice", "expires": rfc3339(&(Utc::now() + TimeDelta::hours(1)))}),
        );
        if let Value::Object(m) = extra {
            for (k, v) in m {
                if v.is_null() {
                    d.remove(&k);
                } else {
                    d.insert(k, v);
                }
            }
        }
        ChangeItem {
            r#type: TYPE_DEROGATION,
            data: d,
            ..Default::default()
        }
    }

    /// No reason, rule, target, signatory or expiry, no derogation; an open one cannot expire in the past (ADR 0075
    /// §2).
    #[test]
    fn derogation_validation() {
        crate::register();
        derogation(Value::Null).validate().expect("valid");
        for f in ["key", "rule", "target", "reason", "signatory", "expires"] {
            let mut m = Map::new();
            m.insert(f.into(), Value::Null);
            assert!(
                derogation(Value::Object(m)).validate().is_err(),
                "a derogation without {f} is accepted"
            );
        }
        let past = rfc3339(&(Utc::now() - TimeDelta::hours(1)));
        for (name, extra) in [
            ("past", json!({"expires": past})),
            ("unparsable", json!({"expires": "soon"})),
            ("status", json!({"status": "paused"})),
        ] {
            assert!(derogation(extra).validate().is_err(), "{name}: accepted");
        }
        // a closing version may carry the expiry that passed; a date is the end of that day
        derogation(json!({"expires": past, "status": "closed"}))
            .validate()
            .expect("closing version");
        let date = (Utc::now() + TimeDelta::hours(48))
            .format("%Y-%m-%d")
            .to_string();
        derogation(json!({"expires": date}))
            .validate()
            .expect("a date");
        let p = item_permission_of(&TYPE_DEROGATION).expect("permission");
        assert!(
            p.permission == PERMISSION_SIGN && p.subject_field == "signatory",
            "permission: {p:?}"
        );
    }

    #[test]
    fn derogations_fold_expired_and_covering() {
        let now = Utc.with_ymd_and_hms(2030, 1, 1, 12, 0, 0).unwrap();
        let item = |id: &str, data: Value| ChangeItem {
            id: id.into(),
            r#type: TYPE_DEROGATION,
            data: obj(data),
            produced_by: "p".into(),
            ..Default::default()
        };
        let base = |key: &str, target: &str, exp: &str| json!({"key": key, "rule": "r", "target": target, "reason": "why", "signatory": "alice", "expires": exp});
        let c = Change {
            items: vec![
                item("1", base("DRG-1", "REQ-1", "2030-01-01T11:00:00Z")), // ran out
                item("2", base("DRG-2", "REQ-2", "2030-01-01T13:00:00Z")), // in force
                item("3", base("DRG-3", "REQ-3", "2030-01-02")),           // in force (a date)
                item("4", base("DRG-3", "REQ-3", "2030-01-02")),           // new version
                item("5", json!({"key": "DRG-2", "status": "closed"})), // closes DRG-2, keeps the rest
            ],
            ..Default::default()
        };
        let ds = derogations(&c);
        assert!(
            ds.len() == 3
                && ds[2].versions == 2
                && ds[1].status == DEROGATION_CLOSED
                && ds[0].status == DEROGATION_OPEN,
            "fold: {ds:?}"
        );
        let ex = expired(&c, now);
        assert!(ex.len() == 1 && ex[0].key == "DRG-1", "expired: {ex:?}");
        assert!(expired(&c, now - TimeDelta::hours(2)).is_empty(), "not yet");
        let d = covering(&c, now, &["REQ-3"]).expect("covering");
        assert_eq!(d.key, "DRG-3");
        for target in ["REQ-1", "REQ-2", "REQ-9"] {
            // ran out, closed, none
            assert!(
                covering(&c, now, &[target]).is_none(),
                "{target} is covered"
            );
        }
        // the closing version a platform writes is valid
        crate::register();
        let closing = ChangeItem {
            r#type: TYPE_DEROGATION,
            data: ex[0].closing_data("expired"),
            ..Default::default()
        };
        closing.validate().expect("closing");
        assert_eq!(
            parse_expiry("2030-01-02")
                .unwrap()
                .to_rfc3339_opts(SecondsFormat::Nanos, true),
            "2030-01-02T23:59:59.999999999Z"
        );
    }
}
