//! The budget of the tool loop of an llm action (ADR 0118) (`loop.go`).

use std::time::Duration;

use serde::{Deserialize, Serialize};

use crate::methodology::KIND_LLM;
use crate::util::{is_zero_i64, q, st};

/// Loop is the budget of the tool loop of an llm action (ADR 0118). The platform bounds every value.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Loop {
    /// MaxRounds bounds the tool rounds (0: DefaultToolRounds, at most MaxLoopRounds).
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub max_rounds: i64,
    /// MaxToolResultBytes bounds a tool result given back to the model (0: the engine's bound).
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub max_tool_result_bytes: i64,
    /// MaxTokens bounds the tokens of the whole loop (0: no bound but the rounds).
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub max_tokens: i64,
    /// Timeout bounds the duration of the loop, a Go duration ("45m", "2h"; empty: no bound).
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub timeout: String,
    /// CompactBytes is the size of the conversation past which the older tool results are elided.
    #[serde(skip_serializing_if = "is_zero_i64")]
    pub compact_bytes: i64,
}

// Bounds of a loop.
pub const DEFAULT_TOOL_ROUNDS: i64 = 6;
pub const MAX_LOOP_ROUNDS: i64 = 500;
pub const MAX_LOOP_TOOL_RESULT: i64 = 200_000;
pub const MAX_LOOP_TIMEOUT: Duration = Duration::from_secs(4 * 3600);
pub const MAX_LOOP_COMPACT: i64 = 4 << 20;
/// MaxLoopTimeout as Go prints it.
const MAX_LOOP_TIMEOUT_TEXT: &str = "4h0m0s";

/// The duration of a Go duration text (None: not one).
fn parse(s: &str) -> Option<(bool, Duration)> {
    let d = goap_decision::parse_duration(s).ok()?;
    let neg = d < chrono::TimeDelta::zero();
    Some((neg || d.is_zero(), d.abs().to_std().ok()?))
}

impl Loop {
    /// Rounds is the number of tool rounds the loop allows (None: the default).
    pub fn rounds(l: Option<&Loop>) -> i64 {
        match l {
            Some(l) if l.max_rounds > 0 => l.max_rounds.min(MAX_LOOP_ROUNDS),
            _ => DEFAULT_TOOL_ROUNDS,
        }
    }

    /// Duration is the bound of the duration of the loop (zero: none).
    pub fn duration(l: Option<&Loop>) -> Duration {
        let Some(l) = l else { return Duration::ZERO };
        if l.timeout.is_empty() {
            return Duration::ZERO;
        }
        match parse(&l.timeout) {
            Some((false, d)) => d.min(MAX_LOOP_TIMEOUT),
            _ => Duration::ZERO,
        }
    }

    /// issues are the problems of the loop of an action of the given kind, as messages.
    pub(crate) fn issues(l: Option<&Loop>, kind: &str) -> Vec<String> {
        let Some(l) = l else { return Vec::new() };
        let mut out = Vec::new();
        if kind != KIND_LLM {
            out.push("loop is the budget of the tool loop of an llm action".to_string());
        }
        let mut check = |name: &str, v: i64, max: i64| {
            if v < 0 || v > max {
                out.push(format!("loop.{name} must be between 0 and {max}"));
            }
        };
        check("maxRounds", l.max_rounds, MAX_LOOP_ROUNDS);
        check(
            "maxToolResultBytes",
            l.max_tool_result_bytes,
            MAX_LOOP_TOOL_RESULT,
        );
        check("compactBytes", l.compact_bytes, MAX_LOOP_COMPACT);
        if l.max_tokens < 0 {
            out.push("loop.maxTokens must not be negative".into());
        }
        if !l.timeout.is_empty() {
            match parse(&l.timeout) {
                None => out.push(format!(
                    "loop.timeout {} is not a duration (45m, 2h)",
                    q(&l.timeout)
                )),
                Some((nonpos, d)) if nonpos || d > MAX_LOOP_TIMEOUT => out.push(format!(
                    "loop.timeout must be positive and at most {MAX_LOOP_TIMEOUT_TEXT}"
                )),
                _ => {}
            }
        }
        out
    }
}
