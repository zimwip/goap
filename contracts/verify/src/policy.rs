//! The review rule of the platform.

use goap_domain::{Error, ReviewPolicy, ReviewRequest};

use crate::independent;

/// Policy is the review rule of the platform (domain::ReviewPolicy, ADR 0075): when the action run that produced a
/// change impact declared an independent verification (a produced entry with independent set), the reviewer is not
/// the producer. Principals are compared (the reviewer's and the impact's produced_by); when the producer is a model,
/// its alias is kept in the produced entry (data.model) and a reviewer that names the same alias is refused too. A
/// review of an effect that declared no verification is not constrained, as before. Policy holds no state.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Policy;

impl ReviewPolicy for Policy {
    /// Review refuses a review by the producer of an effect that asked for an independent verifier.
    fn review(&self, r: &ReviewRequest) -> Result<(), Error> {
        let Some(s) = independent(&r.items, &r.impact.id) else {
            return Ok(());
        };
        if r.reviewer.is_empty() {
            return Ok(());
        }
        if r.reviewer == r.impact.produced_by
            || r.reviewer == s.producer
            || (!s.model.is_empty() && r.reviewer == s.model)
        {
            return Err(Error::invalid(format!(
                "{} produced {} and may not verify it: its verification is independent",
                r.reviewer, r.impact.key
            )));
        }
        Ok(())
    }
}
