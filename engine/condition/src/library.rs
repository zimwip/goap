//! The built-in condition libraries (ADR 0064). A methodology gets the conditions of a library only by importing it
//! (`imports:`); the engine adds nothing the methodology did not ask for.
use std::sync::LazyLock;

use crate::condition::Definition;

/// LIBRARY_DECISIONS: questions, decision points and options (ADR 0009 §4).
pub const LIBRARY_DECISIONS: &str = "decisions";
/// LIBRARY_RISKS: the risk register and the actions of the change (ADR 0036 §1).
pub const LIBRARY_RISKS: &str = "risks";
/// LIBRARY_VERIFICATION: the verification of the effects of the action runs (ADR 0075).
pub const LIBRARY_VERIFICATION: &str = "verification";
/// LIBRARY_DEROGATIONS: the derogations of the change, waivers with a rule, a signatory and an expiry (ADR 0075 §2).
/// A library of its own, not part of `risks`: a methodology that signs none imports none (ADR 0064).
pub const LIBRARY_DEROGATIONS: &str = "derogations";
/// LIBRARY_DEPENDENCIES: the changes the change depends on (ADR 0108). A step waiting for them (`dependencies_met`)
/// waits for those changes to merge, which no action of the change produces.
pub const LIBRARY_DEPENDENCIES: &str = "dependencies";
/// LIBRARY_ASSESSMENTS: the ratings of what a person wrote on the change, the description first (ADR 0110).
pub const LIBRARY_ASSESSMENTS: &str = "assessments";

/// ACCEPTABLE_SCORE is the score (0 to 5) from which a rated description is accepted (`description_accepted`).
pub const ACCEPTABLE_SCORE: i64 = 3;

/// DEROGATION_DEBT_LIMIT is the number of open derogations from which the debt is too high (`derogation_debt`): the
/// parameter of the conditions of the `derogations` library, like HIGH_RISK for the risks. A gate guard or a decision
/// point reads it.
pub const DEROGATION_DEBT_LIMIT: i64 = 3;

/// HIGH_RISK is the score (probability × impact, 1–25) from which a live risk needs a mitigation action: the parameter
/// of the conditions of the `risks` library.
pub const HIGH_RISK: i64 = 9;

/// LIBRARY_NAMES lists the built-in libraries, in a stable order.
pub const LIBRARY_NAMES: [&str; 6] = [
    LIBRARY_DECISIONS,
    LIBRARY_RISKS,
    LIBRARY_VERIFICATION,
    LIBRARY_DEROGATIONS,
    LIBRARY_DEPENDENCIES,
    LIBRARY_ASSESSMENTS,
];

fn def(name: &str, expr: &str, description: &str) -> Definition {
    Definition {
        name: name.into(),
        expr: expr.into(),
        description: description.into(),
        ..Default::default()
    }
}

static LIBRARIES: LazyLock<Vec<(&'static str, Vec<Definition>)>> = LazyLock::new(|| {
    let unmitigated = format!(
        r#"risks.exists(r, r.live && r.score >= {HIGH_RISK} && !actions.exists(a, a.for == r.key && a.status != "cancelled"))"#
    );
    vec![
        (
            LIBRARY_DECISIONS,
            vec![
                def(
                    "open_questions",
                    r#"questions.exists(q, q.status == "open")"#,
                    "a question of the change is still open",
                ),
                def(
                    "no_open_questions",
                    r#"!questions.exists(q, q.status == "open")"#,
                    "every question of the change is answered",
                ),
                // a decision point waits for a ruling it can take now: open, or escalated to a person
                def(
                    "decision_ready",
                    r#"decisionPoints.exists(d, d.status == "open" || d.status == "escalated")"#,
                    "a decision point can be ruled now (open, or escalated to a person)",
                ),
                def(
                    "decision_pending",
                    r#"decisionPoints.exists(d, d.status != "decided")"#,
                    "a decision point is not decided yet",
                ),
                def(
                    "no_decision_pending",
                    r#"!decisionPoints.exists(d, d.status != "decided")"#,
                    "every decision point of the change is decided",
                ),
                def(
                    "ratification_pending",
                    r#"decisionPoints.exists(d, d.status == "ratifying")"#,
                    "a decision waits for its ratification",
                ),
                def(
                    "decision_escalated",
                    r#"decisionPoints.exists(d, d.escalation != "")"#,
                    "a decision point was escalated",
                ),
                def(
                    "options_open",
                    r#"options.exists(o, o.status == "exploring" || o.status == "evaluated")"#,
                    "an option is still being explored or evaluated",
                ),
                // every open option is evaluated (and there is one)
                def(
                    "options_evaluated",
                    r#"options.exists(o, o.status == "evaluated") && !options.exists(o, o.status == "exploring")"#,
                    "every open option is evaluated",
                ),
                def(
                    "option_selected",
                    r#"options.exists(o, o.status == "selected")"#,
                    "an option has been selected",
                ),
            ],
        ),
        (
            LIBRARY_RISKS,
            vec![
                // a live risk is open or being mitigated; a high one (score >= HIGH_RISK) needs an action
                def(
                    "open_risks",
                    "risks.exists(r, r.live)",
                    "a risk of the change is live (open or being mitigated)",
                ),
                def(
                    "unmitigated_risks",
                    &unmitigated,
                    "a high risk has no mitigation action",
                ),
                def(
                    "risks_under_control",
                    &format!("!{unmitigated}"),
                    "every high risk has a mitigation action",
                ),
                def(
                    "open_actions",
                    r#"actions.exists(a, a.status == "open")"#,
                    "an action of the register is still open",
                ),
                def(
                    "no_open_actions",
                    r#"!actions.exists(a, a.status == "open")"#,
                    "every action of the register is done or cancelled",
                ),
            ],
        ),
        (
            LIBRARY_VERIFICATION,
            vec![
                // an effect produced or verified, with no verdict yet; rejected effects are not open: they await a new run
                def(
                    "unverified_effects",
                    "verifications.exists(v, v.open)",
                    "an effect produced by a run waits for its verification",
                ),
                def(
                    "all_verified",
                    "!verifications.exists(v, v.open)",
                    "every effect produced is verified",
                ),
                def(
                    "reserves_open",
                    r#"verifications.exists(v, v.state == "accepted_with_reserve")"#,
                    "an effect was accepted with a reserve that is still open",
                ),
            ],
        ),
        (
            LIBRARY_DEROGATIONS,
            vec![
                // a change cannot land on a derogation that ran out; the debt is the count of the open ones
                def(
                    "no_expired_derogation",
                    "!derogations.exists(d, d.expired)",
                    "no derogation of the change has run out",
                ),
                def(
                    "derogation_debt",
                    &format!("derogations.filter(d, d.open).size() >= {DEROGATION_DEBT_LIMIT}"),
                    "too many derogations are open",
                ),
            ],
        ),
        (
            LIBRARY_DEPENDENCIES,
            vec![
                // satisfied: the work of the dependency has reached the branch the change merges into
                def(
                    "dependencies_met",
                    "!dependencies.exists(d, !d.satisfied)",
                    "the changes this change depends on have merged",
                ),
                def(
                    "dependencies_pending",
                    "dependencies.exists(d, !d.satisfied)",
                    "a change this change depends on has not merged yet",
                ),
            ],
        ),
        (
            LIBRARY_ASSESSMENTS,
            vec![
                // two steps (ADR 0110): the rating exists, then it is good enough. A rating stands for the description
                // as it reads now (current) and for the discussion of it (answered: no remark came after it); an edit
                // or a remark asks for a new one. The effect of a rating action is description_rated, never the score:
                // the score is the rater's verdict, and description_accepted (the gate a methodology waits for) asks
                // for it on top of the rating.
                def(
                    "description_rated",
                    r#"assessments.exists(a, a.key == "description" && a.current && a.answered)"#,
                    "the description, as it reads now, has a rating answering its last remark",
                ),
                def(
                    "description_accepted",
                    &format!(
                        r#"assessments.exists(a, a.key == "description" && a.current && a.answered && a.score >= {ACCEPTABLE_SCORE})"#
                    ),
                    &format!(
                        "the description is rated, with a score of {ACCEPTABLE_SCORE} or more"
                    ),
                ),
            ],
        ),
    ]
});

/// Library returns a copy of the conditions of a built-in library.
pub fn library(name: &str) -> Option<Vec<Definition>> {
    LIBRARIES
        .iter()
        .find(|(n, _)| *n == name)
        .map(|(_, d)| d.clone())
}

/// MustLibrary is library for names known to exist (tests, shipped code).
///
/// # Panics
///
/// Panics on an unknown library.
pub fn must_library(names: &[&str]) -> Vec<Definition> {
    let mut out = Vec::new();
    for n in names {
        match library(n) {
            Some(d) => out.extend(d),
            None => panic!("condition: unknown library {n}"),
        }
    }
    out
}
