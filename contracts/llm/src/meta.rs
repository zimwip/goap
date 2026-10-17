//! The call metadata of the ledger of the gateway (ADR 0089).

use goap_ctx::Ctx;
use serde::{Deserialize, Serialize};

/// CallMeta says who asked for an LLM call and for what, for the ledger of the gateway (ADR 0089). It is accounting
/// only: declared by the caller, never an authorisation input (the subject and the project of a call are the
/// principal's, taken by the gateway). The ledger stores no prompt.
///
/// The JSON form keeps the Go field names (the Go struct has no tags).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct CallMeta {
    /// Source is who asked: engine, assistant, helper, indexer, intent... (lower case, at most MaxSourceLen).
    #[serde(rename = "Source")]
    pub source: String,
    #[serde(rename = "ConversationID")]
    pub conversation_id: String,
    #[serde(rename = "ProcessID")]
    pub process_id: String,
    #[serde(rename = "ChangeID")]
    pub change_id: String,
    /// Step and Call locate the call of a process: the step of the process and the position of the call in the calls of
    /// that step, as the journal numbers them (ADR 0059: the exchange of the call is the model.call entry of the change
    /// log of that process, step and call). They mean something only with a ProcessID; otherwise they are NoStep.
    #[serde(rename = "Step")]
    pub step: i64,
    #[serde(rename = "Call")]
    pub call: i64,
    #[serde(rename = "Action")]
    pub action: String,
    #[serde(rename = "Agent")]
    pub agent: String,
}

/// NoStep is the Step and the Call of a call that belongs to no process.
pub const NO_STEP: i64 = -1;

/// Cap of the declared source (bytes).
pub const MAX_SOURCE_LEN: usize = 40;
/// Cap of the declared identifiers (bytes).
pub const MAX_ID_LEN: usize = 128;
/// Cap of the declared names (bytes).
pub const MAX_NAME_LEN: usize = 200;

// The sources of the platform's own callers; a source is free-form (see Clean), these are the ones the ledger's views know.
pub const SOURCE_ENGINE: &str = "engine";
pub const SOURCE_ASSISTANT: &str = "assistant";
pub const SOURCE_HELPER: &str = "helper";
pub const SOURCE_INDEXER: &str = "indexer";
pub const SOURCE_INTENT: &str = "intent";
/// SourceEval is the offline evaluation harness (pkg/eval, cmd/goap-eval).
pub const SOURCE_EVAL: &str = "eval";

/// SourceCalibration is the source of the gateway's own calls that measure what a behaviour costs on a model (ADR 0093,
/// "Measured cost"): set by the gateway only, and never a scope of a behaviour.
pub const SOURCE_CALIBRATION: &str = "calibration";

/// SourceOther is the source of a call that declares none, or an invalid one.
pub const SOURCE_OTHER: &str = "other";

#[derive(Clone)]
struct MetaKey(CallMeta);

/// WithMeta returns a context carrying the meta of the calls made with it.
pub fn with_meta(ctx: &Ctx, m: CallMeta) -> Ctx {
    ctx.with(MetaKey(m))
}

/// MetaFrom returns the meta of ctx (the zero CallMeta, cleaned, when there is none).
pub fn meta_from(ctx: &Ctx) -> CallMeta {
    ctx.value::<MetaKey>()
        .map(|k| k.0.clone())
        .unwrap_or_default()
        .clean()
}

impl CallMeta {
    /// Clean returns the meta with its values capped and sanitised: a source outside [a-z0-9._:-] (after lower-casing) is
    /// SourceOther, the identifiers lose their control characters, Step and Call are NoStep without a process.
    pub fn clean(mut self) -> CallMeta {
        self.source = self.source.trim().to_lowercase();
        if self.source.is_empty()
            || self.source.len() > MAX_SOURCE_LEN
            || self.source.chars().any(|r| {
                !(r.is_ascii_lowercase()
                    || r.is_ascii_digit()
                    || matches!(r, '.' | '_' | ':' | '-'))
            })
        {
            self.source = SOURCE_OTHER.to_string();
        }
        self.conversation_id = clip(&self.conversation_id, MAX_ID_LEN);
        self.process_id = clip(&self.process_id, MAX_ID_LEN);
        self.change_id = clip(&self.change_id, MAX_ID_LEN);
        self.action = clip(&self.action, MAX_NAME_LEN);
        self.agent = clip(&self.agent, MAX_NAME_LEN);
        if self.process_id.is_empty() || self.step < 0 || self.call < 0 {
            self.step = NO_STEP;
            self.call = NO_STEP;
        }
        self
    }
}

/// clip drops the control characters of s and cuts it to n bytes (on a character boundary).
fn clip(s: &str, n: usize) -> String {
    let mut s: String = s.chars().filter(|c| !c.is_control()).collect();
    if s.len() <= n {
        return s;
    }
    let mut end = n;
    while !s.is_char_boundary(end) {
        end -= 1;
    }
    s.truncate(end);
    s
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn clean_caps_and_sanitises() {
        let m = CallMeta {
            source: "  Engine ".into(),
            process_id: "P\u{7}1".into(),
            step: 2,
            call: 0,
            action: "é".repeat(150),
            ..Default::default()
        }
        .clean();
        assert_eq!(m.source, SOURCE_ENGINE);
        assert_eq!(m.process_id, "P1");
        assert_eq!((m.step, m.call), (2, 0));
        assert_eq!(m.action.len(), 200);
        let m = CallMeta {
            source: "bad source!".into(),
            step: 3,
            ..Default::default()
        }
        .clean();
        assert_eq!(m.source, SOURCE_OTHER);
        assert_eq!((m.step, m.call), (NO_STEP, NO_STEP));
        assert_eq!(
            CallMeta {
                source: "x".repeat(41),
                ..Default::default()
            }
            .clean()
            .source,
            SOURCE_OTHER
        );
    }

    #[test]
    fn meta_travels_in_the_context() {
        let ctx = Ctx::background();
        assert_eq!(meta_from(&ctx).source, SOURCE_OTHER);
        let ctx = with_meta(
            &ctx,
            CallMeta {
                source: "assistant".into(),
                ..Default::default()
            },
        );
        assert_eq!(meta_from(&ctx).source, SOURCE_ASSISTANT);
    }
}
