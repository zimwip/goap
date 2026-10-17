//! Port of `assessmentstates.go`: how the assessment of each subject of a change reads now (ADR 0116).

use goap_condition::AssessmentState;
use goap_ctx::Ctx;
use goap_domain::ChangeID;

use crate::engine::Engine;
use crate::error::Result;

impl Engine {
    /// assessment_states is how the assessment of each subject of a change reads now (ADR 0116): the states its
    /// methodology declares (assessments), else the platform's defaults, evaluated against the change. Nothing is
    /// stored.
    pub async fn assessment_states(
        &self,
        ctx: &Ctx,
        change_id: &ChangeID,
    ) -> Result<Vec<AssessmentState>> {
        let mut bb = self.blackboard(ctx, change_id, "").await?;
        if bb.change.methodology.is_empty() {
            return Ok(goap_condition::assessment_states(
                None,
                &bb,
                &goap_condition::Result::default(),
            ));
        }
        // the version the change follows (ADR 0113)
        let m = self
            .methodologies
            .methodology(ctx, &bb.change.methodology_ref())
            .await?;
        bb.supertypes = self.supertypes_of(&m).into_iter().collect();
        let res = m.conditions.evaluate(&bb);
        Ok(m.assessment_states(&bb, &res))
    }
}
