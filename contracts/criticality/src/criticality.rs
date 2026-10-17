//! Criticality levels, the policy of a level and the check of the items of a change.

use std::{borrow::Cow, collections::BTreeMap, fmt, sync::Arc};

use chrono::{DateTime, SecondsFormat, TimeDelta, Utc};
use goap_ctx::Ctx;
use goap_domain::{Change, ChangeItem, DATA_CRITICALITY, gotime};
use goap_risk::{DEROGATION_CLOSED, TYPE_DEROGATION, parse_expiry};
use goap_verify::{KEY_ORACLE, KEY_STATE, PRODUCED, TYPE_VERIFICATION};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::goduration::{self, go_duration_string};

/// Level is the criticality of a change (a stored value may be one the code does not know: see valid).
#[derive(Clone, Default, PartialEq, Eq, PartialOrd, Ord, Hash, Serialize, Deserialize)]
pub struct Level(Cow<'static, str>);

impl Level {
    /// A level of a static text (the constants).
    pub const fn from_static(s: &'static str) -> Self {
        Level(Cow::Borrowed(s))
    }

    /// A level of any text.
    pub fn new(s: impl Into<String>) -> Self {
        Level(Cow::Owned(s.into()))
    }

    /// The text of the level.
    pub fn as_str(&self) -> &str {
        &self.0
    }
}

impl fmt::Display for Level {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl fmt::Debug for Level {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        fmt::Debug::fmt(&*self.0, f)
    }
}

impl From<&str> for Level {
    fn from(s: &str) -> Self {
        Level::new(s)
    }
}

impl From<String> for Level {
    fn from(s: String) -> Self {
        Level::new(s)
    }
}

impl PartialEq<&str> for Level {
    fn eq(&self, other: &&str) -> bool {
        &*self.0 == *other
    }
}

// The levels, from the lowest.
pub const C1: Level = Level::from_static("C1");
pub const C2: Level = Level::from_static("C2");
pub const C3: Level = Level::from_static("C3");

/// Default is the level of a change that names none: current.
pub const DEFAULT: Level = C2;

/// Levels lists the levels, lowest first.
pub const LEVELS: [Level; 3] = [C1, C2, C3];

/// Valid reports whether l is a level.
pub fn valid(l: &Level) -> bool {
    LEVELS.contains(l)
}

/// Rank orders the levels (C1 < C2 < C3); an unknown level ranks 0.
pub fn rank(l: &Level) -> usize {
    LEVELS.iter().position(|x| x == l).map_or(0, |i| i + 1)
}

/// Of returns the level held by the data of a change (DEFAULT when it names none or an unknown one).
pub fn of(data: &Map<String, Value>) -> Level {
    match data.get(DATA_CRITICALITY).and_then(Value::as_str) {
        Some(s) if valid(&Level::new(s)) => Level::new(s),
        _ => DEFAULT,
    }
}

/// Lowers reports whether moving from one level to another lowers the criticality.
pub fn lowers(from: &Level, to: &Level) -> bool {
    rank(to) < rank(from)
}

/// Policy is what the organisation requires of the changes of one level.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Policy {
    /// Oracles are the kinds of oracle (tool, human, model) accepted to verify an effect; empty: any.
    #[serde(deserialize_with = "null_default")]
    pub oracles: Vec<String>,
    /// Sampling says a human review may be sampled instead of covering every effect.
    pub sampling: bool,
    /// SignatoryRole is the role whoever signs a derogation holds on the project of the change; empty: any signatory
    /// (the permission derogation:sign still holds).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub signatory_role: String,
    /// MaxDerogation is the longest a derogation may last from its signature; zero: unbounded. In JSON an integer of
    /// nanoseconds (Go's time.Duration).
    #[serde(with = "goduration", skip_serializing_if = "goduration::is_zero")]
    pub max_derogation: TimeDelta,
}

fn null_default<'de, D, T>(d: D) -> Result<T, D::Error>
where
    D: serde::Deserializer<'de>,
    T: Default + Deserialize<'de>,
{
    Ok(Option::<T>::deserialize(d)?.unwrap_or_default())
}

impl Policy {
    /// Accepts reports whether the oracle kind may verify an effect.
    pub fn accepts(&self, oracle: &str) -> bool {
        self.oracles.is_empty() || self.oracles.iter().any(|o| o == oracle)
    }

    /// Map is the policy as the plain values CEL reads (criticalityPolicy): oracles, sampling, signatoryRole and
    /// maxDerogationHours.
    pub fn map(&self) -> Map<String, Value> {
        let mut m = Map::new();
        m.insert(
            "oracles".into(),
            Value::Array(
                self.oracles
                    .iter()
                    .map(|o| Value::from(o.as_str()))
                    .collect(),
            ),
        );
        m.insert("sampling".into(), self.sampling.into());
        m.insert("signatoryRole".into(), self.signatory_role.clone().into());
        m.insert(
            "maxDerogationHours".into(),
            self.max_derogation.num_hours().into(),
        );
        m
    }
}

fn oracles(o: &[&str]) -> Vec<String> {
    o.iter().map(|s| s.to_string()).collect()
}

/// Defaults is the compiled-in table, used where the units of the organisation hold none for a level: C1 is loose
/// (any oracle, sampling, a month), C2 asks a tool or a person (sampling, two weeks), C3 asks a person to verify and a
/// signatory of the role "derogation_signatory" to waive, for a week at most.
pub fn defaults() -> BTreeMap<Level, Policy> {
    BTreeMap::from([
        (
            C1,
            Policy {
                oracles: oracles(&["tool", "model", "human"]),
                sampling: true,
                max_derogation: TimeDelta::days(30),
                ..Default::default()
            },
        ),
        (
            C2,
            Policy {
                oracles: oracles(&["tool", "human"]),
                sampling: true,
                max_derogation: TimeDelta::days(14),
                ..Default::default()
            },
        ),
        (
            C3,
            Policy {
                oracles: oracles(&["human"]),
                signatory_role: "derogation_signatory".into(),
                max_derogation: TimeDelta::days(7),
                ..Default::default()
            },
        ),
    ])
}

/// Resolver gives the policy of a level for a change: the organisation of the change resolves it (goap-access). It
/// may read the organisation (I/O). A plain closure `Fn(&Ctx, &Change, &Level) -> Policy` is one.
#[async_trait::async_trait]
pub trait Resolver: Send + Sync {
    async fn resolve(&self, ctx: &Ctx, c: &Change, l: &Level) -> Policy;
}

#[async_trait::async_trait]
impl<F> Resolver for F
where
    F: Fn(&Ctx, &Change, &Level) -> Policy + Send + Sync,
{
    async fn resolve(&self, ctx: &Ctx, c: &Change, l: &Level) -> Policy {
        self(ctx, c, l)
    }
}

/// DefaultResolver resolves every level from the compiled-in table.
#[derive(Debug, Clone, Copy, Default)]
pub struct DefaultResolver;

#[async_trait::async_trait]
impl Resolver for DefaultResolver {
    async fn resolve(&self, _: &Ctx, _: &Change, l: &Level) -> Policy {
        defaults().remove(l).unwrap_or_default()
    }
}

/// An error of the crate: an item the policy of the change refuses.
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// The message (Go's text).
    #[error("{0}")]
    Refused(String),
}

/// ItemPolicy is the check the graph asks of the items about to be written (Graph.item_policy): a verification of an
/// effect names an oracle the policy of the change accepts, and a derogation lasts no longer than it allows. Items of
/// other types are not looked at. The signatory role is checked by the services that know the roles (graphsvc).
pub fn item_policy(resolve: Option<Arc<dyn Resolver>>) -> ItemPolicy {
    ItemPolicy {
        resolve: resolve.unwrap_or_else(|| Arc::new(DefaultResolver)),
    }
}

/// The check of [`item_policy`].
#[derive(Clone)]
pub struct ItemPolicy {
    resolve: Arc<dyn Resolver>,
}

impl fmt::Debug for ItemPolicy {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("ItemPolicy")
    }
}

impl ItemPolicy {
    /// Judges an item about to be written on a change; an error refuses it.
    pub async fn check(&self, ctx: &Ctx, c: &Change, it: &ChangeItem) -> Result<(), Error> {
        let str_of = |k: &str| it.data.get(k).and_then(Value::as_str).unwrap_or_default();
        if it.r#type == TYPE_VERIFICATION {
            if str_of(KEY_STATE) != PRODUCED {
                return Ok(());
            }
            let oracle = str_of(KEY_ORACLE);
            if oracle.is_empty() {
                return Ok(());
            }
            let lvl = of(&c.data);
            let p = self.resolve.resolve(ctx, c, &lvl).await;
            if !p.accepts(oracle) {
                return Err(Error::Refused(format!(
                    "a {oracle} oracle cannot verify the effects of a {lvl} change (accepted: [{}])",
                    p.oracles.join(" ")
                )));
            }
        } else if it.r#type == TYPE_DEROGATION {
            if str_of("status") == DEROGATION_CLOSED {
                return Ok(());
            }
            let lvl = of(&c.data);
            let p = self.resolve.resolve(ctx, c, &lvl).await;
            if p.max_derogation <= TimeDelta::zero() {
                return Ok(());
            }
            let Ok(t) = parse_expiry(str_of("expires")) else {
                return Ok(()); // validate_derogation says so
            };
            // unset: Go's zero time, or the Unix epoch of a `ChangeItem::default()` (chrono's default)
            let at =
                if gotime::is_zero(&it.created_at) || it.created_at == DateTime::<Utc>::default() {
                    Utc::now()
                } else {
                    it.created_at
                };
            if t - at > p.max_derogation {
                return Err(Error::Refused(format!(
                    "a derogation on a {lvl} change lasts {} at most (policy of the organisation), not until {}",
                    go_duration_string(p.max_derogation),
                    t.to_rfc3339_opts(SecondsFormat::Secs, true)
                )));
            }
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_verify::{KEY_ACTION, VERIFIED};
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            _ => unreachable!(),
        }
    }

    #[test]
    fn levels() {
        assert!(
            of(&Map::new()) == C2
                && of(&obj(json!({DATA_CRITICALITY: "C3"}))) == C3
                && of(&obj(json!({DATA_CRITICALITY: "high"}))) == C2,
            "of: a change that names no level is C2"
        );
        assert!(
            lowers(&C3, &C1) && !lowers(&C1, &C3) && !lowers(&C2, &C2),
            "lowers"
        );
        let d = defaults();
        assert!(
            d[&C1].accepts("model")
                && !d[&C3].accepts("tool")
                && d[&C3].accepts("human")
                && !d[&C3].signatory_role.is_empty()
                && d[&C1].signatory_role.is_empty(),
            "the table is loose at C1 and strict at C3: {d:?}"
        );
        assert!(
            d[&C1].max_derogation > d[&C2].max_derogation
                && d[&C2].max_derogation > d[&C3].max_derogation,
            "the stricter the level, the shorter a derogation"
        );
        let m = d[&C3].map();
        assert!(
            m["signatoryRole"] == "derogation_signatory"
                && m["maxDerogationHours"] == json!(7 * 24)
                && m["oracles"].as_array().is_some_and(|o| o.len() == 1),
            "map: {m:?}"
        );
        assert_eq!(
            serde_json::to_value(&d[&C3]).unwrap(),
            json!({"oracles": ["human"], "sampling": false, "signatoryRole": "derogation_signatory", "maxDerogation": 604_800_000_000_000i64})
        );
    }

    #[tokio::test]
    async fn item_policy_checks() {
        goap_verify::register();
        goap_risk::register();
        let check = item_policy(None);
        let ctx = Ctx::background();
        let change = |l: &Level| Change {
            data: obj(json!({DATA_CRITICALITY: l.as_str()})),
            ..Default::default()
        };
        let produced = |oracle: &str| ChangeItem {
            r#type: TYPE_VERIFICATION,
            data: obj(json!({KEY_STATE: PRODUCED, KEY_ACTION: "a", KEY_ORACLE: oracle})),
            ..Default::default()
        };
        check
            .check(&ctx, &change(&C1), &produced("model"))
            .await
            .expect("C1 accepts a model");
        let err = check
            .check(&ctx, &change(&C3), &produced("model"))
            .await
            .unwrap_err();
        assert!(
            err.to_string().contains("C3"),
            "C3 refuses a model oracle: {err}"
        );
        assert_eq!(
            err.to_string(),
            "a model oracle cannot verify the effects of a C3 change (accepted: [human])"
        );
        check
            .check(&ctx, &change(&C3), &produced(""))
            .await
            .expect("an effect with no declared oracle is not constrained");
        let verified = ChangeItem {
            r#type: TYPE_VERIFICATION,
            data: obj(json!({KEY_STATE: VERIFIED, KEY_ORACLE: "model"})),
            ..Default::default()
        };
        check
            .check(&ctx, &change(&C3), &verified)
            .await
            .expect("only the produced entry names the oracle");
        let drg = |d: TimeDelta, status: &str| {
            let mut data = obj(
                json!({"expires": (Utc::now() + d).to_rfc3339_opts(SecondsFormat::Secs, true)}),
            );
            if !status.is_empty() {
                data.insert("status".into(), status.into());
            }
            ChangeItem {
                r#type: TYPE_DEROGATION,
                data,
                ..Default::default()
            }
        };
        check
            .check(&ctx, &change(&C3), &drg(TimeDelta::hours(24), ""))
            .await
            .expect("a day at C3");
        let err = check
            .check(&ctx, &change(&C3), &drg(TimeDelta::days(30), ""))
            .await
            .unwrap_err();
        assert!(
            err.to_string().contains("lasts 168h0m0s at most"),
            "a month at C3 is refused: {err}"
        );
        check
            .check(
                &ctx,
                &change(&C1),
                &drg(TimeDelta::days(30) - TimeDelta::hours(1), ""),
            )
            .await
            .expect("a month less an hour at C1");
        check
            .check(&ctx, &change(&C3), &drg(-TimeDelta::hours(1), "closed"))
            .await
            .expect("a closing version is not bounded");
        // the resolver of the organisation replaces the table
        let loose = |_: &Ctx, _: &Change, _: &Level| Policy::default();
        let loose = item_policy(Some(Arc::new(loose)));
        loose
            .check(&ctx, &change(&C3), &produced("model"))
            .await
            .expect("a policy that names no oracle accepts any");
    }
}
