//! The context a tool call runs for.

use goap_ctx::Ctx;
use serde::{Deserialize, Serialize};

/// CallContext is what a tool call runs for: the unit holding the change, the change and the
/// process. Built-in connectors read it (the change a goap-change tool edits by default); the engine
/// sets Change and Process, the hub sets Unit, and ModelURL / ModelToken for a connector that calls a model by itself
/// (ADR 0123 §6: the gateway endpoint its calls go to, so they are rows of the ledger).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct CallContext {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub unit: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub change: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub process: String,
    #[serde(skip)]
    pub model_url: String,
    #[serde(skip)]
    pub model_token: String,
}

#[derive(Clone)]
struct CallKey(CallContext);

/// WithCall returns ctx carrying the call context; empty fields keep the ones ctx already carries.
pub fn with_call(ctx: &Ctx, mut c: CallContext) -> Ctx {
    let cur = call_from(ctx);
    if c.unit.is_empty() {
        c.unit = cur.unit;
    }
    if c.change.is_empty() {
        c.change = cur.change;
    }
    if c.process.is_empty() {
        c.process = cur.process;
    }
    if c.model_url.is_empty() {
        c.model_url = cur.model_url;
        c.model_token = cur.model_token;
    }
    ctx.with(CallKey(c))
}

/// CallFrom returns the call context of ctx.
pub fn call_from(ctx: &Ctx) -> CallContext {
    ctx.value::<CallKey>()
        .map(|k| k.0.clone())
        .unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn call_context_keeps_what_it_does_not_set() {
        let ctx = with_call(
            &Ctx::background(),
            CallContext {
                change: "C1".into(),
                process: "P1".into(),
                ..Default::default()
            },
        );
        let ctx = with_call(
            &ctx,
            CallContext {
                unit: "U".into(),
                ..Default::default()
            },
        );
        assert_eq!(
            call_from(&ctx),
            CallContext {
                unit: "U".into(),
                change: "C1".into(),
                process: "P1".into(),
                ..Default::default()
            }
        );
    }

    #[test]
    fn model_fields_stay_out_of_json() {
        let c = CallContext {
            unit: "U".into(),
            model_url: "http://x".into(),
            model_token: "t".into(),
            ..Default::default()
        };
        assert_eq!(serde_json::to_string(&c).unwrap(), r#"{"unit":"U"}"#);
    }
}
