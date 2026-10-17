//! The requests on the decision points of a change.

use serde_json::{Map, Value};

/// OpenDecisionRequest opens a decision point.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct OpenDecisionRequest {
    pub question: String,
    /// Options are the options it chooses among; None: the open options of the change, Some(vec![]) none (a free
    /// question).
    pub options: Option<Vec<String>>,
    pub criteria: Vec<String>,
    /// Policy is the policy values the opener gives (who decides, a confidence threshold, a number of rounds, a
    /// duration: the keys of goap-decision). The graph hands them to Graph.decision_policy, which resolves them, and
    /// stores the result on the point.
    pub policy: Map<String, Value>,
    pub by: String,
}

/// RuleRequest is a ruling of a decision point.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RuleRequest {
    pub point: String,
    /// Outcome is decided (option, confidence) or undecidable (questions: what blocks it).
    pub outcome: String,
    pub option: String,
    pub confidence: f64,
    pub justification: String,
    pub questions: Vec<String>,
    /// Human tells the ruling comes from a person: the only one a point reserved to a person accepts.
    pub human: bool,
    pub by: String,
}
