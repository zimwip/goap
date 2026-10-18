//! The confidence / rounds / deadline policy of the decision points.

use chrono::{DateTime, SecondsFormat, Utc};
use goap_domain::{
    DECISION_RATIFY_OP, DECISION_RULE_OP, DecisionEvent, DecisionPoint, DecisionPolicy, Error,
    OUTCOME_UNDECIDABLE, gotime,
};
use serde_json::{Map, Value};

use crate::parse_duration;

// Keys of the policy values of a decision point.
pub const KEY_DECIDER: &str = "decider";
pub const KEY_THRESHOLD: &str = "threshold";
pub const KEY_MAX_ROUNDS: &str = "maxRounds";
pub const KEY_MAX_DURATION: &str = "maxDuration";
pub const KEY_DEADLINE: &str = "deadline";
pub const KEY_ROUNDS: &str = "rounds";

// Deciders.
pub const DECIDER_AGENT: &str = "agent";
pub const DECIDER_HUMAN: &str = "human";

// Defaults of a decision point.
pub const DEFAULT_THRESHOLD: f64 = 0.7;
pub const DEFAULT_ROUNDS: i64 = 3;

/// Policy is the confidence / rounds / deadline policy (domain::DecisionPolicy). It holds no state.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Policy;

/// Go's `%v` of a value: a string as is, anything else as JSON.
fn v(x: Option<&Value>) -> String {
    match x {
        None | Some(Value::Null) => "<nil>".into(),
        Some(Value::String(s)) => s.clone(),
        Some(x) => x.to_string(),
    }
}

impl DecisionPolicy for Policy {
    /// Open resolves the values the opener gave: the defaults, their validation, the deadline from maxDuration.
    fn open(
        &self,
        given: &Map<String, Value>,
        now: DateTime<Utc>,
    ) -> Result<Map<String, Value>, Error> {
        for k in given.keys() {
            match k.as_str() {
                KEY_DECIDER | KEY_THRESHOLD | KEY_MAX_ROUNDS | KEY_MAX_DURATION => {}
                _ => {
                    return Err(Error::invalid(format!(
                        "unknown decision policy value {k:?}"
                    )));
                }
            }
        }
        let mut out = Map::new();
        match given.get(KEY_DECIDER) {
            None | Some(Value::Null) => {
                out.insert(KEY_DECIDER.into(), DECIDER_AGENT.into());
            }
            Some(Value::String(d)) if d.is_empty() || d == DECIDER_AGENT => {
                out.insert(KEY_DECIDER.into(), DECIDER_AGENT.into());
            }
            Some(Value::String(d)) if d == DECIDER_HUMAN => {
                out.insert(KEY_DECIDER.into(), DECIDER_HUMAN.into());
            }
            d => return Err(Error::invalid(format!("decider {}: agent or human", v(d)))),
        }
        let mut threshold = number(given, KEY_THRESHOLD)?;
        if threshold == 0.0 {
            threshold = DEFAULT_THRESHOLD;
        }
        if !(0.0..=1.0).contains(&threshold) {
            return Err(Error::invalid(format!(
                "a confidence threshold is between 0 and 1, not {threshold}"
            )));
        }
        out.insert(KEY_THRESHOLD.into(), threshold.into());
        let mut rounds = number(given, KEY_MAX_ROUNDS)?;
        if rounds == 0.0 {
            rounds = DEFAULT_ROUNDS as f64;
        }
        out.insert(KEY_MAX_ROUNDS.into(), (rounds as i64).into());
        match given.get(KEY_MAX_DURATION) {
            None | Some(Value::Null) => {}
            // an integer is a Go time.Duration: nanoseconds
            Some(Value::Number(n)) if n.as_i64().is_some() => {
                let d = chrono::TimeDelta::nanoseconds(n.as_i64().unwrap_or_default());
                if d > chrono::TimeDelta::zero() {
                    out.insert(KEY_DEADLINE.into(), gotime::format(&(now + d)).into());
                }
            }
            Some(Value::String(s)) => {
                if !s.is_empty() {
                    let d = parse_duration(s)
                        .map_err(|e| Error::invalid(format!("max duration: {e}")))?;
                    if d > chrono::TimeDelta::zero() {
                        out.insert(KEY_DEADLINE.into(), gotime::format(&(now + d)).into());
                    }
                }
            }
            d => {
                return Err(Error::invalid(format!(
                    "max duration {}: a duration such as \"24h\"",
                    v(d)
                )));
            }
        }
        Ok(out)
    }

    /// Fold takes an event into account. A decided ruling settles the point when it comes from a person or reaches
    /// the threshold; an accepted ratification settles it; an undecidable ruling or a refused ratification is a failed
    /// round.
    fn fold(&self, p: &mut DecisionPoint, ev: &DecisionEvent) -> bool {
        match ev.op.as_str() {
            DECISION_RULE_OP => {
                if ev.outcome == OUTCOME_UNDECIDABLE {
                    fail_round(p);
                    return false;
                }
                let t = number(&p.policy, KEY_THRESHOLD).unwrap_or_default();
                ev.human || ev.confidence >= t
            }
            DECISION_RATIFY_OP => {
                if ev.accept {
                    return true;
                }
                fail_round(p);
                false
            }
            _ => false,
        }
    }

    /// Reserve reserves the point to a person when its decider is one, or escalates it: too many failed rounds, or
    /// past the deadline.
    fn reserve(&self, p: &DecisionPoint, now: DateTime<Utc>) -> (bool, String) {
        let human = p.policy.get(KEY_DECIDER).and_then(Value::as_str) == Some(DECIDER_HUMAN);
        let (rounds, max) = (rounds(p), max_rounds(p));
        if max > 0 && rounds >= max {
            return (human, format!("{rounds} rounds without a decision"));
        }
        if let Some(s) = p.policy.get(KEY_DEADLINE).and_then(Value::as_str)
            && !s.is_empty()
            && let Some(deadline) = gotime::parse(s)
            && now > deadline
        {
            return (
                human,
                format!(
                    "past the deadline {}",
                    deadline.to_rfc3339_opts(SecondsFormat::Secs, true)
                ),
            );
        }
        (human, String::new())
    }
}

/// Rounds is the number of rulings that failed to settle the point: undecidable, or a ratification refused.
pub fn rounds(p: &DecisionPoint) -> i64 {
    number(&p.policy, KEY_ROUNDS).unwrap_or_default() as i64
}

/// Decider is who rules the point: agent or human.
pub fn decider(p: &DecisionPoint) -> String {
    match p.policy.get(KEY_DECIDER).and_then(Value::as_str) {
        Some(d) if !d.is_empty() => d.to_string(),
        _ => DECIDER_AGENT.into(),
    }
}

/// Threshold is the confidence an agent ruling must reach to settle the point.
pub fn threshold(p: &DecisionPoint) -> f64 {
    number(&p.policy, KEY_THRESHOLD).unwrap_or_default()
}

/// MaxRounds is the number of failed rounds after which the point is escalated (0 or less: no limit).
pub fn max_rounds(p: &DecisionPoint) -> i64 {
    number(&p.policy, KEY_MAX_ROUNDS).unwrap_or_default() as i64
}

fn fail_round(p: &mut DecisionPoint) {
    let n = rounds(p) + 1;
    p.policy.insert(KEY_ROUNDS.into(), n.into());
}

/// number reads a numeric policy value, whatever its encoding (the values travel as JSON): absent is 0.
fn number(m: &Map<String, Value>, key: &str) -> Result<f64, Error> {
    match m.get(key) {
        None | Some(Value::Null) => Ok(0.0),
        Some(Value::Number(n)) => Ok(n.as_f64().unwrap_or_default()),
        x => Err(Error::invalid(format!("{key} {}: a number", v(x)))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{TimeDelta, TimeZone};
    use goap_domain::{
        Change, ChangeItem, DECISION_ANSWER_OP, DECISION_OPEN_OP, OUTCOME_DECIDED, POINT_BLOCKED,
        POINT_DECIDED, POINT_ESCALATED, POINT_OPEN, POINT_RATIFYING, TYPE_DECISION_POINT,
    };
    use serde_json::json;

    fn t0() -> DateTime<Utc> {
        Utc.with_ymd_and_hms(2026, 9, 1, 0, 0, 0).unwrap()
    }

    fn obj(v: Value) -> Map<String, Value> {
        match v {
            Value::Object(m) => m,
            Value::Null => Map::new(),
            _ => unreachable!(),
        }
    }

    /// replay builds a change from events and replays its decision points under the policy.
    #[derive(Default)]
    struct Replay {
        c: Change,
    }

    impl Replay {
        fn add(&mut self, id: &str, e: DecisionEvent) {
            let at = t0() + TimeDelta::minutes(self.c.items.len() as i64);
            self.c.items.push(ChangeItem {
                id: id.into(),
                r#type: TYPE_DECISION_POINT,
                decision_event: Some(e),
                created_at: at,
                ..Default::default()
            });
        }

        /// open opens a point with the values the policy resolves.
        fn open(&mut self, id: &str, given: Value, mut e: DecisionEvent) {
            let values = Policy.open(&obj(given), t0()).unwrap();
            e.op = DECISION_OPEN_OP.into();
            e.policy = values;
            self.add(id, e);
        }

        fn point(&self, id: &str, at: DateTime<Utc>) -> DecisionPoint {
            self.c
                .decision_point(id, at, Some(&Policy))
                .expect("no point")
        }
    }

    fn rule(point: &str, outcome: &str) -> DecisionEvent {
        DecisionEvent {
            op: DECISION_RULE_OP.into(),
            point: point.into(),
            outcome: outcome.into(),
            ..Default::default()
        }
    }

    fn question(q: &str) -> DecisionEvent {
        DecisionEvent {
            question: q.into(),
            ..Default::default()
        }
    }

    /// The confidence / rounds policy end to end through the replay: the expectations are those the graph had before
    /// the policy moved out of it (ADR 0067).
    #[test]
    fn policy_replay() {
        let mut r = Replay::default();
        let at = t0() + TimeDelta::hours(1);
        r.open(
            "P",
            json!({"threshold": 0.8, "maxRounds": 2}),
            DecisionEvent {
                question: "which PSP?".into(),
                options: vec!["a".into(), "b".into()],
                ..Default::default()
            },
        );
        let d = r.point("P", at);
        assert!(
            d.status == POINT_OPEN && decider(&d) == DECIDER_AGENT && threshold(&d) == 0.8,
            "opened: {d:?}"
        );
        // undecidable: two questions block the point
        r.add(
            "R1",
            DecisionEvent {
                justification: "costs unknown".into(),
                questions: vec!["fees of a?".into(), "fees of b?".into()],
                ..rule("P", OUTCOME_UNDECIDABLE)
            },
        );
        let d = r.point("P", at);
        assert!(
            d.status == POINT_BLOCKED
                && d.open_questions() == 2
                && rounds(&d) == 1
                && d.questions[0].id == "R1:1",
            "blocked: {d:?}"
        );
        for (id, q, a) in [("A1", "R1:1", "1%"), ("A2", "R1:2", "2%")] {
            r.add(
                id,
                DecisionEvent {
                    op: DECISION_ANSWER_OP.into(),
                    point: "P".into(),
                    question_id: q.into(),
                    answer: a.into(),
                    ..Default::default()
                },
            );
        }
        let d = r.point("P", at);
        assert!(
            d.status == POINT_OPEN && d.open_questions() == 0,
            "answered: {d:?}"
        );
        // an agent ruling below the threshold waits for a human; a refusal is a round
        r.add(
            "R2",
            DecisionEvent {
                option: "a".into(),
                confidence: 0.6,
                ..rule("P", OUTCOME_DECIDED)
            },
        );
        let d = r.point("P", at);
        assert!(d.status == POINT_RATIFYING, "ratifying: {d:?}");
        r.add(
            "X",
            DecisionEvent {
                op: DECISION_RATIFY_OP.into(),
                point: "P".into(),
                accept: false,
                ..Default::default()
            },
        );
        let d = r.point("P", at);
        assert!(
            d.status == POINT_ESCALATED
                && rounds(&d) == 2
                && d.needs_human()
                && d.escalation == "2 rounds without a decision",
            "two rounds: escalated: {d:?}"
        );
        r.add(
            "R3",
            DecisionEvent {
                option: "a".into(),
                human: true,
                by: "ann".into(),
                ..rule("P", OUTCOME_DECIDED)
            },
        );
        let d = r.point("P", at);
        assert!(
            d.status == POINT_DECIDED && d.option == "a" && d.decided_by == "ann",
            "decided: {d:?}"
        );
        // a decided point does not move any more
        r.add(
            "R4",
            DecisionEvent {
                option: "b".into(),
                human: true,
                ..rule("P", OUTCOME_DECIDED)
            },
        );
        assert_eq!(r.point("P", at).option, "a", "moved");
        // past the deadline, a point is escalated
        r.open("Q", json!({"maxDuration": "30m"}), question("when?"));
        let d = r.point("Q", at);
        assert!(
            d.status == POINT_ESCALATED && d.escalation == "past the deadline 2026-09-01T00:30:00Z",
            "deadline: {d:?}"
        );
        let d = r.point("Q", t0() + TimeDelta::minutes(10));
        assert!(
            d.status == POINT_OPEN && !d.needs_human(),
            "before the deadline: {d:?}"
        );
    }

    #[test]
    fn policy_threshold_and_ratification() {
        let mut r = Replay::default();
        r.open("P", Value::Null, question("go?")); // the defaults: 0.7 and 3 rounds
        let d = r.point("P", t0());
        assert!(
            threshold(&d) == DEFAULT_THRESHOLD && max_rounds(&d) == DEFAULT_ROUNDS,
            "defaults: {:?}",
            d.policy
        );
        // at the threshold an agent settles the point alone, below it a person ratifies
        r.add(
            "R1",
            DecisionEvent {
                option: "x".into(),
                confidence: 0.69,
                ..rule("P", OUTCOME_DECIDED)
            },
        );
        assert!(r.point("P", t0()).status == POINT_RATIFYING, "below");
        r.add(
            "X",
            DecisionEvent {
                op: DECISION_RATIFY_OP.into(),
                point: "P".into(),
                accept: true,
                by: "ann".into(),
                ..Default::default()
            },
        );
        let d = r.point("P", t0());
        assert!(
            d.status == POINT_DECIDED
                && d.option == "x"
                && d.decided_by == "ann"
                && d.ruling.as_ref().is_some_and(|r| r.human)
                && rounds(&d) == 0,
            "ratified: {d:?}"
        );
        r.open("S", Value::Null, question("again?"));
        r.add(
            "R2",
            DecisionEvent {
                option: "y".into(),
                confidence: 0.7,
                by: "agent".into(),
                ..rule("S", OUTCOME_DECIDED)
            },
        );
        let d = r.point("S", t0());
        assert!(
            d.status == POINT_DECIDED && d.decided_by == "agent",
            "at the threshold: {d:?}"
        );
        // three failed rounds escalate under the default
        r.open("T", Value::Null, question("stuck?"));
        for id in ["U1", "U2", "U3"] {
            r.add(
                id,
                DecisionEvent {
                    justification: "?".into(),
                    ..rule("T", OUTCOME_UNDECIDABLE)
                },
            );
        }
        let d = r.point("T", t0());
        assert!(
            rounds(&d) == 3 && d.escalation == "3 rounds without a decision",
            "rounds: {d:?}"
        );
    }

    #[test]
    fn policy_human_decider() {
        let mut r = Replay::default();
        r.open("P", json!({"decider": "human"}), question("go?"));
        // reserved to a person by design: not an escalation
        let d = r.point("P", t0());
        assert!(
            d.status == POINT_OPEN
                && d.needs_human()
                && d.human_only
                && d.escalation.is_empty()
                && decider(&d) == DECIDER_HUMAN,
            "human decider: {d:?}"
        );
    }

    #[test]
    fn policy_open() {
        let got = Policy
            .open(
                &obj(json!({"decider": "human", "threshold": 0.9, "maxRounds": 5, "maxDuration": "2h"})),
                t0(),
            )
            .unwrap();
        let want = json!({KEY_DECIDER: "human", KEY_THRESHOLD: 0.9, KEY_MAX_ROUNDS: 5i64,
            KEY_DEADLINE: gotime::format(&(t0() + TimeDelta::hours(2)))});
        assert_eq!(Value::Object(got), want);
        // a Go duration (nanoseconds) and the numbers a JSON round trip gives
        let got = Policy
            .open(
                &obj(json!({"maxDuration": 3_600_000_000_000i64, "maxRounds": 2.0})),
                t0(),
            )
            .unwrap();
        assert!(
            got[KEY_MAX_ROUNDS] == json!(2i64) && got.contains_key(KEY_DEADLINE),
            "typed values: {got:?}"
        );
        for (name, bad) in [
            ("decider", json!({"decider": "robot"})),
            ("threshold", json!({"threshold": 1.5})),
            ("negative", json!({"threshold": -0.1})),
            ("duration", json!({"maxDuration": "tomorrow"})),
            ("unknown", json!({"thresold": 0.5})),
            ("number", json!({"maxRounds": "many"})),
        ] {
            assert!(
                Policy.open(&obj(bad.clone()), t0()).is_err(),
                "{name}: accepted {bad}"
            );
        }
        assert_eq!(
            Policy
                .open(&obj(json!({"decider": "robot"})), t0())
                .unwrap_err()
                .to_string(),
            "decider robot: agent or human"
        );
        // an unlimited number of rounds
        let mut r = Replay::default();
        r.open("P", json!({"maxRounds": -1}), question("x?"));
        for id in ["U1", "U2", "U3", "U4"] {
            r.add(
                id,
                DecisionEvent {
                    justification: "?".into(),
                    ..rule("P", OUTCOME_UNDECIDABLE)
                },
            );
        }
        let d = r.point("P", t0());
        assert!(
            rounds(&d) == 4 && d.escalation.is_empty(),
            "no limit: {d:?}"
        );
    }
}
