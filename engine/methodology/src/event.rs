//! The events a methodology declares (ADR 0110, `event.go`).

use std::collections::HashSet;
use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Serialize};

use crate::compile::CompileState;
use crate::method::Subscription;
use crate::methodology::{Methodology, TRIGGER_EVENTS};
use crate::process::Step;
use crate::util::{q, st};

/// Event is an event a methodology declares (ADR 0110), in addition to the standard events (events.All).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Event {
    #[serde(deserialize_with = "st")]
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub description: String,
}

/// EventNameRE is the form of the name of a declared event: lower case words separated by dots.
pub static EVENT_NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[a-z][a-z0-9_-]*(\.[a-z0-9_-]+)*$").expect("event name regexp"));

/// The prefixes of the standard events: a declared event takes none of them.
const RESERVED_EVENT_FAMILIES: [&str; 4] = ["change.", "process.", "step.", "methodology."];

/// StandardEvent reports whether the event is one of the platform's (events.All).
pub fn standard_event(name: &str) -> bool {
    TRIGGER_EVENTS.contains(&name)
}

impl Methodology {
    /// Declares reports whether the methodology declares the event.
    pub fn declares(&self, name: &str) -> bool {
        self.events.iter().any(|e| e.name == name)
    }

    /// KnowsEvent reports whether a subscription or a trigger of the methodology may follow the event.
    pub fn knows_event(&self, name: &str) -> bool {
        standard_event(name) || self.declares(name)
    }

    /// EventNames are the events the methodology may follow: the standard ones, then its own.
    pub fn event_names(&self) -> Vec<String> {
        let mut out: Vec<String> = TRIGGER_EVENTS.iter().map(|s| s.to_string()).collect();
        out.extend(self.events.iter().map(|e| e.name.clone()));
        out
    }

    /// AllSubscriptions lists the subscriptions of the methodology: its own on, then those of its reactive
    /// activities (processes and their steps, methods and their steps, actions, ADR 0110).
    pub fn all_subscriptions(&self) -> Vec<PathSubscription> {
        let mut out = Vec::new();
        fn add(out: &mut Vec<PathSubscription>, path: &str, subs: &[Subscription]) {
            for (i, sub) in subs.iter().enumerate() {
                out.push(PathSubscription {
                    path: format!("{path}on[{i}]"),
                    sub: sub.clone(),
                });
            }
        }
        fn steps(out: &mut Vec<PathSubscription>, list: &[Step], path: &str) {
            for (i, st) in list.iter().enumerate() {
                let sp = format!("{path}.steps[{i}]");
                add(out, &format!("{sp}."), &st.on);
                steps(out, &st.steps, &sp);
            }
        }
        add(&mut out, "", &self.on);
        for (i, p) in self.processes.iter().enumerate() {
            let path = format!("processes[{i}]");
            add(&mut out, &format!("{path}."), &p.on);
            steps(&mut out, &p.steps, &path);
        }
        for (i, me) in self.methods.iter().enumerate() {
            let path = format!("methods[{i}]");
            add(&mut out, &format!("{path}."), &me.on);
            steps(&mut out, &me.steps, &path);
        }
        for (i, a) in self.actions.iter().enumerate() {
            add(&mut out, &format!("actions[{i}]."), &a.on);
        }
        out
    }
}

/// PathSubscription is a subscription of a methodology with the path of its definition.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct PathSubscription {
    pub path: String,
    pub sub: Subscription,
}

impl CompileState<'_> {
    /// checkEvents checks the declared events and the emits of the actions.
    pub(crate) fn check_events(&mut self) {
        let m = self.m;
        let mut seen: HashSet<&str> = HashSet::new();
        for (i, e) in m.events.iter().enumerate() {
            let path = format!("events[{i}].name");
            if !EVENT_NAME_RE.is_match(&e.name) {
                self.add(
                    path,
                    format!(
                        "an event name is lower case words separated by dots ({})",
                        EVENT_NAME_RE.as_str()
                    ),
                );
            } else if RESERVED_EVENT_FAMILIES
                .iter()
                .any(|p| e.name.starts_with(p))
            {
                self.add(
                    path,
                    format!(
                        "{} takes a prefix of the standard events ({}): name it after the methodology's own vocabulary",
                        q(&e.name),
                        RESERVED_EVENT_FAMILIES.join(" ")
                    ),
                );
            } else if seen.contains(e.name.as_str()) {
                self.add(path, format!("event {} is declared twice", q(&e.name)));
            }
            seen.insert(&e.name);
        }
        for (i, a) in m.actions.iter().enumerate() {
            for (j, name) in a.emits.iter().enumerate() {
                if !m.declares(name) {
                    self.add(
                        format!("actions[{i}].emits[{j}]"),
                        format!(
                            "unknown event {}: an action emits an event of the methodology (events)",
                            q(name)
                        ),
                    );
                }
            }
        }
    }

    /// checkEvent checks the event a subscription or a trigger follows.
    pub(crate) fn check_event(&mut self, path: String, name: &str) {
        if !self.m.knows_event(name) {
            self.add(
                path,
                format!(
                    "unknown event {}: one of the standard events ({}) or an event the methodology declares (events)",
                    q(name),
                    TRIGGER_EVENTS.join(", ")
                ),
            );
        }
    }
}
