//! The builtin names (`builtins.go`).

/// Follow links backwards from the impacted nodes.
pub const GRAPH_PROPAGATE: &str = "graph.propagate";
/// Apply the change.
pub const GRAPH_APPLY: &str = "graph.apply";
/// Open options on a decision point.
pub const DECISION_INVESTIGATE: &str = "decision.investigate";
/// Closes the derogations that ran out and sends what they covered back to review (ADR 0075 §2).
pub const DEROGATION_EXPIRE: &str = "derogation.expire";
/// Builtin of the self-observation methodology (ADR 0011).
pub const OBSERVE_ANALYZE: &str = "observe.analyze";
/// Builtin of the self-observation methodology (ADR 0011).
pub const OBSERVE_PROPOSE: &str = "observe.propose";
/// Builtin of the self-observation methodology (ADR 0011).
pub const METHODOLOGY_DRAFT: &str = "methodology.draft";

/// Lists the known builtin names.
pub fn all() -> Vec<&'static str> {
    vec![
        GRAPH_PROPAGATE,
        GRAPH_APPLY,
        DECISION_INVESTIGATE,
        DEROGATION_EXPIRE,
        OBSERVE_ANALYZE,
        OBSERVE_PROPOSE,
        METHODOLOGY_DRAFT,
    ]
}

/// The static set of the builtin names; it implements methodology's `BuiltinSet`.
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq)]
pub struct Known;

impl Known {
    /// Reports whether name is a builtin of the platform.
    pub fn has_builtin(&self, name: &str) -> bool {
        all().contains(&name)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn known_lists_all() {
        for n in all() {
            assert!(Known.has_builtin(n));
        }
        assert!(!Known.has_builtin("nope"));
    }
}
