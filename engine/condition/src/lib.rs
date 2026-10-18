//! Compiles and evaluates the CEL conditions of a methodology.
//!
//! A condition is a named CEL expression evaluated against the blackboard: the change items (axis change) hydrated
//! with the domain nodes they reference (axis domain). The result of evaluating every condition is the world state the
//! GOAP planner reasons on.
//!
//! cel-go is replaced by the `cel` crate, which has no type checker: [`compile`] and the other compilers refuse what
//! cel-go's checker refused structurally (an undeclared variable, a result type decided as not the one asked for: see
//! `check.rs`), and there is no cost limit.
mod activation;
mod assessview;
mod check;
mod condition;
mod event;
mod expect;
mod explain;
mod facts;
mod gate;
mod implies;
mod library;
mod list;
mod number;
mod pathmatch;
mod scope;
mod unparse;
mod value;

pub use activation::{Activation, LAZY_VARIABLES, Object, activation};
pub use assessview::{
    AssessmentState, AssessmentViews, DEFAULT_ASSESSMENT_VIEWS, StateRule, SubjectView,
    TONE_DANGER, TONE_INFO, TONE_NEUTRAL, TONE_OK, TONE_WARN, TONES, ViewIssue, assessment_states,
    assessment_variables, check_subject_pattern, compile_assessment_views,
};
pub use condition::{Definition, Error, Result, Set, VARIABLES, compile, new_env};
pub use event::{EventFilter, compile_event_filter};
pub use expect::{Expectation, LinkSpec, ProduceSpec};
pub use explain::{Explanation, Term};
pub use facts::{Fact, missing};
pub use gate::{check_gate, check_guard, check_guardian};
pub use implies::implies;
pub use library::{
    ACCEPTABLE_SCORE, DEROGATION_DEBT_LIMIT, HIGH_RISK, LIBRARY_ASSESSMENTS, LIBRARY_DECISIONS,
    LIBRARY_DEPENDENCIES, LIBRARY_DEROGATIONS, LIBRARY_NAMES, LIBRARY_RISKS, LIBRARY_VERIFICATION,
    library, must_library,
};
pub use list::{List, Value, check_list, compile_list, compile_value};
pub use number::{Input, NumberSet, check_number, compile_numbers};
pub use scope::{
    AGGREGATE_ALL, AGGREGATE_ANY, AGGREGATE_NONE, SCOPE_ACTION, SCOPE_CHANGE, SCOPE_IMPACT,
    SCOPE_ITEM, SCOPE_RISK, SCOPE_STEP, SCOPES, Subject, check_definition, self_reads,
    subject_entry, subject_id, subjects_of,
};

#[cfg(test)]
mod tests;
