//! Reviewing steps (ADR 0130, `review.go`).

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::compile::CompileState;
use crate::method::Subscription;
use crate::methodology::Compiled;
use crate::process::{METHOD_MANUAL, Step};
use crate::util::{is_false, q, st};

/// Review declares a step a review (ADR 0130): the step rates a subject of the change and its verdict advises.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Review {
    /// Subject is what the review rates: description, title, intent, impact:<node key>, item:<record key or id>.
    #[serde(deserialize_with = "st")]
    pub subject: String,
    /// Automatic starts the step by itself on the events of its subject.
    #[serde(skip_serializing_if = "is_false")]
    pub automatic: bool,
}

/// The subjects of a review.
pub const REVIEW_DESCRIPTION: &str = "description";
pub const REVIEW_TITLE: &str = "title";
pub const REVIEW_INTENT: &str = "intent";
const REVIEW_IMPACT: &str = "impact:";
const REVIEW_ITEM: &str = "item:";

/// headerFields are the fields of the change header each header subject reads.
fn header_fields(s: &str) -> Option<&'static [&'static str]> {
    match s {
        REVIEW_DESCRIPTION => Some(&["title", "intent"]),
        REVIEW_TITLE => Some(&["title"]),
        REVIEW_INTENT => Some(&["intent"]),
        _ => None,
    }
}

/// validSubject reports whether a subject names something a review can rate.
fn valid_subject(s: &str) -> bool {
    if header_fields(s).is_some() {
        return true;
    }
    [REVIEW_IMPACT, REVIEW_ITEM]
        .iter()
        .any(|p| s.strip_prefix(p).is_some_and(|rest| !rest.is_empty()))
}

impl Review {
    /// Subscriptions are the events an automatic review of a header subject reacts to; empty for the other subjects.
    pub fn subscriptions(&self) -> Vec<Subscription> {
        let Some(fields) = header_fields(&self.subject) else {
            return Vec::new();
        };
        let edited: Vec<String> = fields
            .iter()
            .map(|f| format!("{} in event.fields", q(f)))
            .collect();
        vec![
            Subscription {
                event: "change.created".into(),
                ..Default::default()
            },
            Subscription {
                event: "change.edited".into(),
                filter: edited.join(" || "),
                supersede: true,
                ..Default::default()
            },
            Subscription {
                event: "change.item_added".into(),
                filter: format!(
                    r#"event.items.exists(i, i.type == "comment" && i.data.about == {})"#,
                    q(&self.subject)
                ),
                ..Default::default()
            },
        ]
    }
}

impl Step {
    /// ReactsTo is what the step reacts to: its own on, else the events of its automatic review.
    pub fn reacts_to(&self) -> Vec<Subscription> {
        match &self.review {
            Some(r) if self.on.is_empty() && r.automatic => r.subscriptions(),
            _ => self.on.clone(),
        }
    }
}

/// Carries reports whether a run towards goal carries out the automatic review step.
pub fn carries(goal: &str, step: &str) -> bool {
    goal == step || goal.starts_with(&format!("{step}/"))
}

impl Compiled {
    /// AutomaticReview reports, for an action generated for a step of an automatic review, the path of that step.
    pub fn automatic_review(&self, action: &str) -> Option<&str> {
        self.automatic_reviews.get(action).map(String::as_str)
    }
}

impl CompileState<'_> {
    /// checkReview checks the review of a step.
    fn check_review(&mut self, path: &str, st: &Step) {
        let Some(r) = &st.review else {
            return;
        };
        if !valid_subject(&r.subject) {
            self.add(
                format!("{path}.review.subject"),
                format!(
                    "a review rates description, title, intent, impact:<node key> or item:<record key or id>, not {}",
                    q(&r.subject)
                ),
            );
        } else if st.method() == METHOD_MANUAL && r.automatic {
            self.add(
                format!("{path}.review.automatic"),
                "an automatic review is done by an action or a method: a manual step waits for a person",
            );
        } else if r.automatic && st.reacts_to().is_empty() {
            self.add(
                format!("{path}.review.automatic"),
                format!(
                    "an automatic review of {} starts on events the step declares (on): only the header subjects have default ones",
                    q(&r.subject)
                ),
            );
        }
    }

    /// compileAutomaticReviews checks the reviews of the steps and records the actions generated for the automatic
    /// ones.
    pub(crate) fn compile_automatic_reviews(&mut self) {
        let m = self.m;
        let mut reviews: Vec<String> = Vec::new();
        fn steps(
            s: &mut CompileState<'_>,
            reviews: &mut Vec<String>,
            list: &[Step],
            path: &str,
            prefix: &str,
        ) {
            for (i, st) in list.iter().enumerate() {
                let sp = format!("{path}.steps[{i}]");
                let full = format!("{prefix}/{}", st.name);
                s.check_review(&sp, st);
                if st.review.as_ref().is_some_and(|r| r.automatic) {
                    reviews.push(full.clone());
                }
                steps(s, reviews, &st.steps, &sp, &full);
            }
        }
        for (i, p) in m.processes.iter().enumerate() {
            steps(
                self,
                &mut reviews,
                &p.steps,
                &format!("processes[{i}]"),
                &p.name,
            );
        }
        for (i, me) in m.methods.iter().enumerate() {
            steps(
                self,
                &mut reviews,
                &me.steps,
                &format!("methods[{i}]"),
                &me.name,
            );
        }
        if reviews.is_empty() {
            return;
        }
        let mut out = BTreeMap::new();
        for a in &self.procs.actions {
            if let Some(step) = reviews.iter().find(|step| carries(&a.step, step)) {
                out.insert(a.name.clone(), step.clone());
            }
        }
        self.automatic_reviews = out;
    }
}
