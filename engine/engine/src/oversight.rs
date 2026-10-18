//! The oversight floor (EU AI Act Art. 14, Go's `oversight.go`): with Engine::person_reviews_model_output, what a
//! model produced is accepted by a person, whatever the methodology says. An automated action may still reject a
//! change impact, or accept one no model produced; it may not accept an impact a model produced, nor may an llm
//! action accept anything. A human action is not constrained.

use goap_ctx::Ctx;
use goap_domain::{Change, ChangeID, ChangeImpactID, LogFilter};
use goap_methodology::{Action, KIND_HUMAN, KIND_LLM};

use crate::engine::Engine;
use crate::error::{Error, Result};

/// The mark of the context of the node operations of an automated action run: its kind.
#[derive(Debug, Clone)]
struct Automated(String);

/// with_automated marks the node operations of an action run that no person answers: the executor's result.
pub(crate) fn with_automated(ctx: &Ctx, a: &Action) -> Ctx {
    if a.kind == KIND_HUMAN {
        return ctx.clone();
    }
    ctx.with(Automated(a.kind.clone()))
}

/// automated_kind is the kind of the automated action whose operations run in ctx ("" for a person's).
pub(crate) fn automated_kind(ctx: &Ctx) -> String {
    ctx.value::<Automated>()
        .map(|a| a.0.clone())
        .unwrap_or_default()
}

impl Engine {
    /// check_oversight refuses the acceptance of impact by an automated action when the floor is on and a model is
    /// behind the verdict or the impact.
    pub(crate) async fn check_oversight(
        &self,
        ctx: &Ctx,
        c: &Change,
        impact: &ChangeImpactID,
    ) -> Result<()> {
        if !self.person_reviews_model_output {
            return Ok(());
        }
        let kind = automated_kind(ctx);
        if kind.is_empty() {
            return Ok(());
        }
        if kind == KIND_LLM {
            return Err(Error::forbidden(format!(
                "a model may not accept {impact}: a person reviews it (the oversight floor of the platform)"
            )));
        }
        let Some(cn) = c.nodes.iter().rev().find(|n| n.id == *impact) else {
            return Ok(());
        };
        if cn.execution.is_empty() {
            return Ok(());
        }
        if self.called_model(ctx, &c.id, &cn.execution).await? {
            return Err(Error::forbidden(format!(
                "{impact} was produced by a model: a person reviews it, not an automated action (the oversight floor of the platform)"
            )));
        }
        Ok(())
    }

    /// called_model reports whether the action run execution of a change called a model.
    pub(crate) async fn called_model(
        &self,
        ctx: &Ctx,
        change: &ChangeID,
        execution: &str,
    ) -> Result<bool> {
        let (entries, _) = self
            .graph
            .change_log(
                ctx,
                LogFilter {
                    change: change.clone(),
                    execution: execution.to_string(),
                    types: vec![
                        format!("{}.", goap_journal::STREAM_MODEL),
                        format!("{}.", goap_journal::STREAM_JOURNAL),
                    ],
                    ..Default::default()
                },
            )
            .await?;
        if entries
            .iter()
            .any(|en| en.stream() == goap_journal::STREAM_MODEL)
        {
            return Ok(true);
        }
        let records = goap_journal::decode(&entries)?;
        Ok(records
            .iter()
            .any(|r| !r.model_calls.is_empty() || r.action_kind == KIND_LLM))
    }
}
