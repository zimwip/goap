//! Decision points of a change (ADR 0009 §4; Go's `contracts/domain/decision.go`): a question the change must answer,
//! ruled decided (an option, a confidence, a justification) or undecidable (with the questions that block it), the
//! questions answered, a ruling that did not settle the point ratified by a person. A point is the replay of its events,
//! items of type `decision_point` of the main flow.
//!
//! When a ruling settles a point and when only a person may rule it is the policy the Go graph service plugs
//! (`contracts/decision.Policy`, ADR 0067): a decider, a confidence threshold, a number of rounds, a deadline, a board.
//! It is the only one here: the opaque values of a point are resolved at its opening and folded with its events.
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::gojson;

/// The type of the items of the decision points (`domain.TypeDecisionPoint`).
pub const TYPE_DECISION_POINT: &str = "decision_point";

pub const OP_OPEN: &str = "open";
pub const OP_RULE: &str = "rule";
pub const OP_ANSWER: &str = "answer";
pub const OP_RATIFY: &str = "ratify";

pub const OUTCOME_DECIDED: &str = "decided";
pub const OUTCOME_UNDECIDABLE: &str = "undecidable";

pub const POINT_OPEN: &str = "open";
pub const POINT_BLOCKED: &str = "blocked";
pub const POINT_RATIFYING: &str = "ratifying";
pub const POINT_ESCALATED: &str = "escalated";
pub const POINT_DECIDED: &str = "decided";

pub const QUESTION_OPEN: &str = "open";
pub const QUESTION_ANSWERED: &str = "answered";

// The keys of the policy values of a point (`contracts/decision`).
const KEY_DECIDER: &str = "decider";
const KEY_THRESHOLD: &str = "threshold";
const KEY_MAX_ROUNDS: &str = "maxRounds";
const KEY_MAX_DURATION: &str = "maxDuration";
const KEY_DEADLINE: &str = "deadline";
const KEY_ROUNDS: &str = "rounds";
const KEY_BOARD: &str = "board";
const KEY_RULE: &str = "rule";
const KEY_VOTES: &str = "votes";
const KEY_CHAIR: &str = "chair";
const KEY_GATE: &str = "gate";
const KEY_TRANSITION: &str = "transition";

const RULE_UNANIMITY: &str = "unanimity";
const RULE_MAJORITY: &str = "majority";
const RULE_CONSENT: &str = "consent";

const OPTION_GO: &str = "go";
const OPTION_NO_GO: &str = "no_go";
const OPTION_ABSTAIN: &str = "abstain";

const DECIDER_AGENT: &str = "agent";
const DECIDER_HUMAN: &str = "human";

const DEFAULT_THRESHOLD: f64 = 0.7;
const DEFAULT_ROUNDS: f64 = 3.0;

fn is_false(b: &bool) -> bool {
    !b
}

fn is_zero(f: &f64) -> bool {
    *f == 0.0
}

/// The payload of a decision point item, as Go marshals a `domain.DecisionEvent`.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct DecisionEvent {
    pub op: String,
    /// the point: the id of the item that opened it (empty on open)
    #[serde(skip_serializing_if = "String::is_empty")]
    pub point: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub question: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub options: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub criteria: Vec<String>,
    #[serde(skip_serializing_if = "Map::is_empty")]
    pub policy: Map<String, Value>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub outcome: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub option: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub confidence: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub justification: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub questions: Vec<String>,
    #[serde(skip_serializing_if = "is_false")]
    pub human: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub question_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub answer: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process: String,
    #[serde(skip_serializing_if = "is_false")]
    pub accept: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub comment: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub by: String,
    /// the seat a person rules a point of a board in (ADR 0132 §3)
    #[serde(skip_serializing_if = "String::is_empty")]
    pub role: String,
}

/// An event of a point as the log holds it: the item that carries it.
#[derive(Debug, Clone)]
pub struct PointItem {
    pub id: String,
    pub flow: String,
    pub created_at: DateTime<Utc>,
    pub event: DecisionEvent,
}

/// A question raised by an undecidable ruling: it blocks its point until answered.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Question {
    pub id: String,
    pub point: String,
    pub text: String,
    pub status: String,
    pub answer: String,
    pub answered_by: String,
    pub process: String,
    pub asked_at: Option<DateTime<Utc>>,
}

/// The last ruling of a point.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Ruling {
    pub outcome: String,
    pub option: String,
    pub confidence: f64,
    pub justification: String,
    pub by: String,
    pub human: bool,
    pub role: String,
    pub at: Option<DateTime<Utc>>,
}

/// A decision point, replayed from its events.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct DecisionPoint {
    pub id: String,
    pub question: String,
    pub options: Vec<String>,
    pub criteria: Vec<String>,
    /// the policy values of the point, updated by the policy as it folds the events
    pub policy: Map<String, Value>,
    pub opened_at: Option<DateTime<Utc>>,
    pub opened_by: String,
    pub status: String,
    pub questions: Vec<Question>,
    pub ruling: Option<Ruling>,
    /// only a person may rule the point now: by design, or because it is escalated
    pub human_only: bool,
    /// why the policy reserved the point to a person (empty: not escalated)
    pub escalation: String,
    pub option: String,
    pub decided_at: Option<DateTime<Utc>>,
    pub decided_by: String,
}

impl DecisionPoint {
    /// The questions waiting for an answer.
    pub fn open_questions(&self) -> usize {
        self.questions.iter().filter(|q| q.status == QUESTION_OPEN).count()
    }

    /// Whether the point is not decided yet.
    pub fn pending(&self) -> bool {
        self.status != POINT_DECIDED
    }

    /// Whether only a person may rule the point.
    pub fn needs_human(&self) -> bool {
        self.human_only || !self.escalation.is_empty()
    }

    fn decide(&mut self, r: Ruling) {
        self.option = r.option.clone();
        self.decided_at = r.at;
        self.decided_by = r.by.clone();
        self.ruling = Some(r);
    }

    fn decided(&self) -> bool {
        !self.decided_by.is_empty() || self.decided_at.is_some()
    }
}

/// The id of the i-th question an undecidable ruling raised (`domain.QuestionID`).
fn question_id(rule: &str, i: usize) -> String {
    format!("{rule}:{}", i + 1)
}

/// The decision points of a change, oldest first, replayed at a moment from the items of its log (only the main
/// flow's count; `Change.DecisionPointsAt` under `decision.Policy`).
pub fn points_at(items: &[PointItem], now: DateTime<Utc>) -> Vec<DecisionPoint> {
    let mut out: Vec<DecisionPoint> = Vec::new();
    let mut at: std::collections::HashMap<&str, usize> = std::collections::HashMap::new();
    for it in items.iter().filter(|it| it.flow.is_empty()) {
        let e = &it.event;
        if e.op == OP_OPEN {
            at.insert(&it.id, out.len());
            out.push(DecisionPoint {
                id: it.id.clone(),
                question: e.question.clone(),
                options: e.options.clone(),
                criteria: e.criteria.clone(),
                policy: e.policy.clone(),
                opened_at: Some(it.created_at),
                opened_by: e.by.clone(),
                ..DecisionPoint::default()
            });
            continue;
        }
        let Some(&i) = at.get(e.point.as_str()) else { continue };
        let d = &mut out[i];
        if d.decided() {
            continue; // a decided point does not move
        }
        match e.op.as_str() {
            OP_RULE => {
                let r = Ruling {
                    outcome: e.outcome.clone(),
                    option: e.option.clone(),
                    confidence: e.confidence,
                    justification: e.justification.clone(),
                    by: e.by.clone(),
                    human: e.human,
                    role: e.role.clone(),
                    at: Some(it.created_at),
                };
                d.ruling = Some(r);
                let settled = fold(d, e);
                if e.outcome == OUTCOME_UNDECIDABLE {
                    for (j, q) in e.questions.iter().enumerate() {
                        d.questions.push(Question {
                            id: question_id(&it.id, j),
                            point: d.id.clone(),
                            text: q.clone(),
                            status: QUESTION_OPEN.into(),
                            asked_at: Some(it.created_at),
                            ..Question::default()
                        });
                    }
                } else if settled {
                    // the fold may have chosen the option a board settles on
                    let r = d.ruling.clone().unwrap_or_default();
                    d.decide(r);
                }
            }
            OP_RATIFY => {
                let Some(r) = d.ruling.clone().filter(|r| r.outcome == OUTCOME_DECIDED) else {
                    continue;
                };
                if fold(d, e) {
                    d.decide(Ruling {
                        outcome: OUTCOME_DECIDED.into(),
                        option: r.option,
                        confidence: r.confidence,
                        justification: r.justification,
                        by: e.by.clone(),
                        human: true,
                        role: String::new(),
                        at: Some(it.created_at),
                    });
                } else {
                    d.ruling = None;
                }
            }
            OP_ANSWER => {
                for q in &mut d.questions {
                    if q.id == e.question_id && q.status == QUESTION_OPEN {
                        q.status = QUESTION_ANSWERED.into();
                        q.answer = e.answer.clone();
                        q.answered_by = e.by.clone();
                        q.process = e.process.clone();
                    }
                }
            }
            _ => {}
        }
    }
    for d in &mut out {
        settle(d, now);
    }
    out
}

/// The status of a replayed point.
fn settle(d: &mut DecisionPoint, now: DateTime<Utc>) {
    if d.decided_at.is_some() {
        d.status = POINT_DECIDED.into();
        return;
    }
    let (human, escalation) = reserve(d, now);
    d.human_only = human || !escalation.is_empty();
    d.escalation = escalation;
    d.status = if d.ruling.as_ref().is_some_and(|r| r.outcome == OUTCOME_DECIDED) {
        POINT_RATIFYING
    } else if d.open_questions() > 0 {
        POINT_BLOCKED
    } else if !d.escalation.is_empty() {
        POINT_ESCALATED
    } else {
        POINT_OPEN
    }
    .into();
}

/// Resolves the policy values an opener gave (`decision.Policy.Open`): defaults, validation, the deadline from
/// `maxDuration`; the error says what is wrong.
pub fn open_policy(given: &Map<String, Value>, now: DateTime<Utc>) -> Result<Map<String, Value>, String> {
    for k in given.keys() {
        if ![
            KEY_DECIDER,
            KEY_THRESHOLD,
            KEY_MAX_ROUNDS,
            KEY_MAX_DURATION,
            KEY_BOARD,
            KEY_RULE,
            KEY_CHAIR,
            KEY_GATE,
            KEY_TRANSITION,
        ]
        .contains(&k.as_str())
        {
            return Err(format!("unknown decision policy value {k:?}"));
        }
    }
    let mut out = Map::new();
    let decider = match given.get(KEY_DECIDER) {
        None | Some(Value::Null) => DECIDER_AGENT,
        Some(Value::String(s)) if s.is_empty() || s == DECIDER_AGENT => DECIDER_AGENT,
        Some(Value::String(s)) if s == DECIDER_HUMAN => DECIDER_HUMAN,
        Some(v) => return Err(format!("decider {}: agent or human", go_v(v))),
    };
    out.insert(KEY_DECIDER.into(), decider.into());
    let mut threshold = number(given, KEY_THRESHOLD)?;
    if threshold == 0.0 {
        threshold = DEFAULT_THRESHOLD;
    }
    if !(0.0..=1.0).contains(&threshold) {
        return Err(format!(
            "a confidence threshold is between 0 and 1, not {}",
            gojson::float(threshold)
        ));
    }
    out.insert(KEY_THRESHOLD.into(), Value::from(threshold));
    let mut rounds = number(given, KEY_MAX_ROUNDS)?;
    if rounds == 0.0 {
        rounds = DEFAULT_ROUNDS;
    }
    out.insert(KEY_MAX_ROUNDS.into(), Value::from(rounds as i64));
    match given.get(KEY_MAX_DURATION) {
        None | Some(Value::Null) => {}
        Some(Value::String(s)) if s.is_empty() => {}
        Some(Value::String(s)) => {
            let d = parse_duration(s).map_err(|e| format!("max duration: {e}"))?;
            if d > 0 {
                let deadline = now + chrono::Duration::nanoseconds(d);
                out.insert(KEY_DEADLINE.into(), gojson::time_text(&deadline).into());
            }
        }
        Some(v) => return Err(format!("max duration {}: a duration such as \"24h\"", go_v(v))),
    }
    for k in [KEY_GATE, KEY_TRANSITION, KEY_CHAIR] {
        if let Some(Value::String(s)) = given.get(k)
            && !s.is_empty()
        {
            out.insert(k.into(), s.clone().into());
        }
    }
    let board = names(given, KEY_BOARD)?;
    if !board.is_empty() {
        out.insert(KEY_BOARD.into(), Value::from(board));
        out.insert(KEY_DECIDER.into(), DECIDER_HUMAN.into()); // a board is people
        let rule = match given.get(KEY_RULE) {
            None | Some(Value::Null) => RULE_UNANIMITY.to_string(),
            Some(Value::String(s)) if s.is_empty() => RULE_UNANIMITY.to_string(),
            Some(Value::String(s)) if [RULE_UNANIMITY, RULE_MAJORITY, RULE_CONSENT].contains(&s.as_str()) => s.clone(),
            Some(v) => return Err(format!("rule {}: unanimity, majority or consent", go_v(v))),
        };
        out.insert(KEY_RULE.into(), rule.into());
    } else if let Some(Value::String(r)) = given.get(KEY_RULE)
        && !r.is_empty()
    {
        return Err(format!("rule {r} applies to a board: name its roles"));
    }
    Ok(out)
}

/// The roles that sit on a point (none: anyone rules it alone).
pub fn board(p: &DecisionPoint) -> Vec<String> {
    names(&p.policy, KEY_BOARD).unwrap_or_default()
}

fn rule_of(p: &DecisionPoint) -> &str {
    match p.policy.get(KEY_RULE) {
        Some(Value::String(s)) if !s.is_empty() => s,
        _ => RULE_UNANIMITY,
    }
}

fn votes(p: &DecisionPoint) -> Map<String, Value> {
    match p.policy.get(KEY_VOTES) {
        Some(Value::Object(m)) => m
            .iter()
            .filter(|(_, v)| v.is_string())
            .map(|(k, v)| (k.clone(), v.clone()))
            .collect(),
        _ => Map::new(),
    }
}

/// Takes an event into account and says whether it settles the point (`decision.Policy.Fold`).
fn fold(p: &mut DecisionPoint, ev: &DecisionEvent) -> bool {
    let b = board(p);
    if !b.is_empty() && ev.op == OP_RULE && ev.outcome == OUTCOME_DECIDED {
        return fold_board(p, &b, ev);
    }
    match ev.op.as_str() {
        OP_RULE => {
            if ev.outcome == OUTCOME_UNDECIDABLE {
                fail_round(p);
                return false;
            }
            let t = number(&p.policy, KEY_THRESHOLD).unwrap_or(0.0);
            ev.human || ev.confidence >= t
        }
        OP_RATIFY => {
            if ev.accept {
                return true;
            }
            fail_round(p);
            false
        }
        _ => false,
    }
}

/// Records the ruling of a seat and says whether the board settles the point, on the option it settles on; an
/// unsettling ruling is a vote, not a ruling waiting for its ratification.
fn fold_board(p: &mut DecisionPoint, board: &[String], ev: &DecisionEvent) -> bool {
    match board_ruling(p, board, ev) {
        Some(option) => {
            if let Some(r) = &mut p.ruling {
                r.option = option;
            }
            true
        }
        None => {
            p.ruling = None;
            false
        }
    }
}

fn board_ruling(p: &mut DecisionPoint, board: &[String], ev: &DecisionEvent) -> Option<String> {
    let mut v = votes(p);
    if let Some(Value::String(chair)) = p.policy.get(KEY_CHAIR)
        && !chair.is_empty()
        && ev.role == *chair
        && v.len() >= board.len()
    {
        return Some(ev.option.clone()); // every seat ruled and the rule settled nothing: the chair decides
    }
    if !board.contains(&ev.role) || v.get(&ev.role).and_then(Value::as_str).is_some_and(|o| !o.is_empty()) {
        return None;
    }
    v.insert(ev.role.clone(), ev.option.clone().into());
    let count = |o: &str| {
        board
            .iter()
            .filter(|role| v.get(*role).and_then(Value::as_str) == Some(o))
            .count()
    };
    let ruled = v.len();
    let mut options: Vec<&str> = board
        .iter()
        .filter_map(|role| v.get(role).and_then(Value::as_str))
        .collect();
    options.sort();
    options.dedup();
    let settled = match rule_of(p) {
        RULE_MAJORITY => options
            .iter()
            .find(|o| 2 * count(o) > board.len() && **o != OPTION_ABSTAIN)
            .map(|o| o.to_string()),
        RULE_CONSENT if ev.option == OPTION_NO_GO => Some(OPTION_NO_GO.to_string()),
        RULE_CONSENT if ruled >= board.len() => Some(OPTION_GO.to_string()),
        RULE_CONSENT => None,
        _ if ev.option != OPTION_GO => Some(ev.option.clone()),
        _ if count(OPTION_GO) == board.len() => Some(OPTION_GO.to_string()),
        _ => None,
    };
    p.policy.insert(KEY_VOTES.into(), Value::Object(v));
    settled
}

fn fail_round(p: &mut DecisionPoint) {
    let rounds = number(&p.policy, KEY_ROUNDS).unwrap_or(0.0) as i64;
    p.policy.insert(KEY_ROUNDS.into(), Value::from(rounds + 1));
}

/// Whether only a person may rule a point at a moment, and why when it is escalated (`decision.Policy.Reserve`).
fn reserve(p: &DecisionPoint, now: DateTime<Utc>) -> (bool, String) {
    let human = p.policy.get(KEY_DECIDER).and_then(Value::as_str) == Some(DECIDER_HUMAN);
    let rounds = number(&p.policy, KEY_ROUNDS).unwrap_or(0.0) as i64;
    let max = number(&p.policy, KEY_MAX_ROUNDS).unwrap_or(0.0) as i64;
    if max > 0 && rounds >= max {
        return (human, format!("{rounds} rounds without a decision"));
    }
    if let Some(s) = p.policy.get(KEY_DEADLINE).and_then(Value::as_str)
        && let Ok(deadline) = DateTime::parse_from_rfc3339(s)
        && now > deadline
    {
        let d = deadline.with_timezone(&Utc);
        return (human, format!("past the deadline {}", d.format("%Y-%m-%dT%H:%M:%SZ")));
    }
    (human, String::new())
}

/// A numeric policy value, absent 0 (`decision.number`).
fn number(m: &Map<String, Value>, key: &str) -> Result<f64, String> {
    match m.get(key) {
        None | Some(Value::Null) => Ok(0.0),
        Some(Value::Number(n)) => Ok(n.as_f64().unwrap_or(0.0)),
        Some(v) => Err(format!("{key} {}: a number", go_v(v))),
    }
}

/// A list of role names of the policy values (`decision.strings`).
fn names(m: &Map<String, Value>, key: &str) -> Result<Vec<String>, String> {
    match m.get(key) {
        None | Some(Value::Null) => Ok(Vec::new()),
        Some(Value::Array(a)) => a
            .iter()
            .map(|x| match x {
                Value::String(s) if !s.is_empty() => Ok(s.clone()),
                _ => Err(format!("{key}: a list of role names")),
            })
            .collect(),
        Some(v) => Err(format!("{key} {}: a list of role names", go_v(v))),
    }
}

/// A value as Go's `%v` prints the `any` it decodes to.
pub fn go_v(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".into(),
        Value::Bool(b) => b.to_string(),
        Value::Number(n) => gojson::float(n.as_f64().unwrap_or(0.0)),
        Value::String(s) => s.clone(),
        Value::Array(a) => format!("[{}]", a.iter().map(go_v).collect::<Vec<_>>().join(" ")),
        Value::Object(m) => format!(
            "map[{}]",
            m.iter()
                .map(|(k, v)| format!("{k}:{}", go_v(v)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
    }
}

/// A duration as Go's `time.ParseDuration` reads it, in nanoseconds.
pub fn parse_duration(orig: &str) -> Result<i64, String> {
    let invalid = || format!("time: invalid duration {orig:?}");
    let mut s = orig.as_bytes();
    let mut neg = false;
    if let Some(&c) = s.first()
        && (c == b'-' || c == b'+')
    {
        neg = c == b'-';
        s = &s[1..];
    }
    if s == b"0" {
        return Ok(0);
    }
    if s.is_empty() {
        return Err(invalid());
    }
    const MAX: u64 = 1 << 63;
    let mut d: u64 = 0;
    while !s.is_empty() {
        if !(s[0] == b'.' || s[0].is_ascii_digit()) {
            return Err(invalid());
        }
        // [0-9]*
        let mut v: u64 = 0;
        let mut i = 0;
        while i < s.len() && s[i].is_ascii_digit() {
            if v > (MAX - 1) / 10 {
                return Err(invalid());
            }
            v = v * 10 + u64::from(s[i] - b'0');
            if v > MAX - 1 {
                return Err(invalid());
            }
            i += 1;
        }
        let pre = i > 0;
        s = &s[i..];
        // (\.[0-9]*)?
        let (mut f, mut scale, mut post) = (0u64, 1f64, false);
        if s.first() == Some(&b'.') {
            s = &s[1..];
            let mut i = 0;
            let mut overflow = false;
            while i < s.len() && s[i].is_ascii_digit() {
                if !overflow {
                    if f > (MAX - 1) / 10 {
                        overflow = true;
                    } else {
                        let y = f * 10 + u64::from(s[i] - b'0');
                        if y > MAX {
                            overflow = true;
                        } else {
                            f = y;
                            scale *= 10.0;
                        }
                    }
                }
                i += 1;
            }
            post = i > 0;
            s = &s[i..];
        }
        if !pre && !post {
            return Err(invalid());
        }
        let i = s
            .iter()
            .position(|&c| c == b'.' || c.is_ascii_digit())
            .unwrap_or(s.len());
        if i == 0 {
            return Err(format!("time: missing unit in duration {orig:?}"));
        }
        let u = std::str::from_utf8(&s[..i]).unwrap_or_default();
        s = &s[i..];
        let unit: u64 = match u {
            "ns" => 1,
            "us" | "µs" | "μs" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            _ => return Err(format!("time: unknown unit {u:?} in duration {orig:?}")),
        };
        if v > MAX / unit {
            return Err(invalid());
        }
        v *= unit;
        if f > 0 {
            v += (f as f64 * (unit as f64 / scale)) as u64;
            if v > MAX {
                return Err(invalid());
            }
        }
        d += v;
        if d > MAX {
            return Err(invalid());
        }
    }
    if neg {
        return Ok((d as i128).wrapping_neg() as i64);
    }
    if d > MAX - 1 {
        return Err(invalid());
    }
    Ok(d as i64)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations() {
        assert_eq!(parse_duration("24h"), Ok(24 * 3_600_000_000_000));
        assert_eq!(parse_duration("1.5s"), Ok(1_500_000_000));
        assert_eq!(parse_duration("-2m"), Ok(-120_000_000_000));
        assert_eq!(parse_duration("0"), Ok(0));
        assert_eq!(parse_duration("x"), Err("time: invalid duration \"x\"".into()));
        assert_eq!(parse_duration("3"), Err("time: missing unit in duration \"3\"".into()));
        assert_eq!(
            parse_duration("3d"),
            Err("time: unknown unit \"d\" in duration \"3d\"".into())
        );
    }

    fn item(id: &str, event: DecisionEvent) -> PointItem {
        PointItem {
            id: id.into(),
            flow: String::new(),
            created_at: Utc::now(),
            event,
        }
    }

    #[test]
    fn a_low_confidence_waits_for_its_ratification() {
        let policy = open_policy(&Map::new(), Utc::now()).unwrap();
        let open = DecisionEvent {
            op: OP_OPEN.into(),
            question: "which".into(),
            options: vec!["a".into(), "b".into()],
            policy,
            ..DecisionEvent::default()
        };
        let rule = DecisionEvent {
            op: OP_RULE.into(),
            point: "p".into(),
            outcome: OUTCOME_DECIDED.into(),
            option: "a".into(),
            confidence: 0.5,
            justification: "j".into(),
            ..DecisionEvent::default()
        };
        let ps = points_at(&[item("p", open.clone()), item("r", rule.clone())], Utc::now());
        assert_eq!(ps[0].status, POINT_RATIFYING);
        let ratify = DecisionEvent {
            op: OP_RATIFY.into(),
            point: "p".into(),
            accept: true,
            human: true,
            ..DecisionEvent::default()
        };
        let ps = points_at(&[item("p", open), item("r", rule), item("t", ratify)], Utc::now());
        assert_eq!((ps[0].status.as_str(), ps[0].option.as_str()), (POINT_DECIDED, "a"));
    }

    #[test]
    fn a_board_settles_by_consent() {
        let mut given = Map::new();
        given.insert(KEY_BOARD.into(), Value::from(vec!["x", "y"]));
        given.insert(KEY_RULE.into(), RULE_CONSENT.into());
        let policy = open_policy(&given, Utc::now()).unwrap();
        let open = DecisionEvent {
            op: OP_OPEN.into(),
            question: "go?".into(),
            options: vec![OPTION_GO.into(), OPTION_NO_GO.into()],
            policy,
            ..DecisionEvent::default()
        };
        let vote = |role: &str| DecisionEvent {
            op: OP_RULE.into(),
            point: "p".into(),
            outcome: OUTCOME_DECIDED.into(),
            option: OPTION_GO.into(),
            justification: "j".into(),
            human: true,
            role: role.into(),
            ..DecisionEvent::default()
        };
        let ps = points_at(&[item("p", open.clone()), item("1", vote("x"))], Utc::now());
        assert_eq!(ps[0].status, POINT_OPEN);
        let ps = points_at(
            &[item("p", open), item("1", vote("x")), item("2", vote("y"))],
            Utc::now(),
        );
        assert_eq!(
            (ps[0].status.as_str(), ps[0].option.as_str()),
            (POINT_DECIDED, OPTION_GO)
        );
    }
}
