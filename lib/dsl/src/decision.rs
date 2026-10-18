//! Options and decision points of the change (ADR 0009 §3-4), as scripts see and work on them.

use goap_domain::{Blackboard, decision_points_of, options_of};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::dsl::null_default;

/// Option is an option of the change: a hypothesis explored on a flow of its own (Go's `dsl.Option`, renamed so it
/// does not shadow `std::option::Option`).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct ChangeOption {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(deserialize_with = "null_default")]
    pub hypothesis: String,
    /// Status is exploring, evaluated, selected or rejected.
    #[serde(deserialize_with = "null_default")]
    pub status: String,
    pub active: bool,
    #[serde(deserialize_with = "null_default")]
    pub evaluation: String,
}

/// Question is a question raised by an undecidable ruling.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Question {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub point: String,
    #[serde(deserialize_with = "null_default")]
    pub text: String,
    /// open | answered
    #[serde(deserialize_with = "null_default")]
    pub status: String,
    #[serde(deserialize_with = "null_default")]
    pub answer: String,
}

/// DecisionPoint is a decision point of the change.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct DecisionPoint {
    #[serde(deserialize_with = "null_default")]
    pub id: String,
    #[serde(deserialize_with = "null_default")]
    pub question: String,
    #[serde(deserialize_with = "null_default")]
    pub options: Vec<String>,
    #[serde(deserialize_with = "null_default")]
    pub criteria: Vec<String>,
    /// Policy holds the policy values of the point (who decides, thresholds, rounds: the keys of the decision
    /// policy).
    #[serde(deserialize_with = "null_default")]
    pub policy: Map<String, Value>,
    /// Status is open, blocked, ratifying, escalated or decided.
    #[serde(deserialize_with = "null_default")]
    pub status: String,
    #[serde(deserialize_with = "null_default")]
    pub questions: Vec<Question>,
    /// Option is the option decided (once decided).
    #[serde(deserialize_with = "null_default")]
    pub option: String,
}

/// OptionsFromBlackboard builds the snapshot of the options given to scripts.
pub fn options_from_blackboard(bb: &Blackboard) -> Vec<ChangeOption> {
    options_of(bb)
        .into_iter()
        .map(|o| {
            let (name, hypothesis) = o
                .option
                .as_ref()
                .map(|s| (s.name.clone(), s.hypothesis.clone()))
                .unwrap_or_default();
            ChangeOption {
                id: o.id.clone(),
                name,
                hypothesis,
                status: o.option_status().to_string(),
                active: o.active,
                evaluation: o.evaluation.clone(),
            }
        })
        .collect()
}

/// DecisionPointsFromBlackboard builds the snapshot of the decision points given to scripts.
pub fn decision_points_from_blackboard(bb: &Blackboard) -> Vec<DecisionPoint> {
    decision_points_of(bb)
        .into_iter()
        .map(|d| DecisionPoint {
            id: d.id,
            question: d.question,
            options: d.options,
            criteria: d.criteria,
            policy: d.policy,
            status: d.status,
            option: d.option,
            questions: d
                .questions
                .into_iter()
                .map(|q| Question {
                    id: q.id,
                    point: q.point,
                    text: q.text,
                    status: q.status,
                    answer: q.answer,
                })
                .collect(),
        })
        .collect()
}
