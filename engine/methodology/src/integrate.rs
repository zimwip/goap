//! Methodologies integrating methodologies (ADR 0106, `integrate.go`).

use std::collections::BTreeMap;

use goap_def::NAME_RE;
use serde::{Deserialize, Serialize};

use crate::compile::CompileState;
use crate::methodology::Methodology;
use crate::util::{is_false, nd, q, st};

/// AttachDecision: the integrator's process proposes, a decision point of its change settles (default).
pub const ATTACH_DECISION: &str = "decision";
/// AttachAuto: the integrator attaches at once.
pub const ATTACH_AUTO: &str = "auto";

/// FromSelf: the events of the change the methodology runs on (default).
pub const FROM_SELF: &str = "self";
/// FromIntegrated: the events of the changes attached to a change of the methodology through `integrates`.
pub const FROM_INTEGRATED: &str = "integrated";
/// FromIntegrator: the events of the change of an integrator a change of the methodology is attached to.
pub const FROM_INTEGRATOR: &str = "integrator";
/// FromDependency: the events of the changes a change of the methodology depends on (ADR 0108).
pub const FROM_DEPENDENCY: &str = "dependency";

/// AnyMethodology in Integration.Methodologies integrates every other methodology.
pub const ANY_METHODOLOGY: &str = "*";

/// Integration names methodologies whose changes the changes of this one integrate as sub-changes.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Integration {
    #[serde(deserialize_with = "nd")]
    pub methodologies: Vec<String>,
    /// Exclusive: those changes land only through one of its changes.
    #[serde(skip_serializing_if = "is_false")]
    pub exclusive: bool,
    /// Attach is how a candidate becomes a sub-change: AttachDecision (default) or AttachAuto.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub attach: String,
}

/// Guardian is what a methodology requires of the changes it integrates (ADR 0106).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Guardian {
    /// Merge must hold for the change to merge.
    #[serde(skip_serializing_if = "String::is_empty", deserialize_with = "st")]
    pub merge: String,
    /// Transitions adds a guard to transitions of the change's own lifecycle, by transition name.
    #[serde(skip_serializing_if = "BTreeMap::is_empty", deserialize_with = "nd")]
    pub transitions: BTreeMap<String, String>,
}

impl Methodology {
    /// IntegratesChangesOf reports whether the changes of m integrate the changes of the methodology named other.
    pub fn integrates_changes_of(&self, other: &str) -> bool {
        self.integration_of(other).is_some()
    }

    /// IntegrationOf returns the integration of m naming the methodology other (None: none).
    pub fn integration_of(&self, other: &str) -> Option<&Integration> {
        if other == self.name {
            return None;
        }
        self.integrates.iter().find(|i| {
            i.methodologies
                .iter()
                .any(|x| x == other || x == ANY_METHODOLOGY)
        })
    }

    /// MergeGuard is the guard m adds to the merge of a change it integrates ("" when none).
    pub fn merge_guard(&self, of: &str) -> String {
        let mut parts = Vec::new();
        if self.integration_of(of).is_some_and(|i| i.exclusive) {
            parts.push("integrator.attached".to_string());
        }
        if let Some(g) = &self.guardian
            && !g.merge.trim().is_empty()
        {
            parts.push(format!("({})", g.merge));
        }
        parts.join(" && ")
    }

    /// TransitionGuard is the guard m adds to a transition of the lifecycle of a change it integrates.
    pub fn transition_guard(&self, name: &str) -> String {
        self.guardian
            .as_ref()
            .and_then(|g| g.transitions.get(name).cloned())
            .unwrap_or_default()
    }
}

impl CompileState<'_> {
    /// checkIntegration checks integrates, guardian and the origins of the subscriptions.
    pub(crate) fn check_integration(&mut self) {
        let m = self.m;
        for (i, inn) in m.integrates.iter().enumerate() {
            let path = format!("integrates[{i}]");
            if inn.methodologies.is_empty() {
                self.add(
                    format!("{path}.methodologies"),
                    format!(
                        "name the methodologies whose changes this one integrates (or {})",
                        q(ANY_METHODOLOGY)
                    ),
                );
            }
            for (j, other) in inn.methodologies.iter().enumerate() {
                let p = format!("{path}.methodologies[{j}]");
                if other == ANY_METHODOLOGY {
                } else if !NAME_RE.is_match(other) {
                    self.add(p, "integrates names methodologies");
                } else if *other == m.name {
                    self.add(p, "a methodology does not integrate itself");
                }
            }
            match inn.attach.as_str() {
                "" | ATTACH_DECISION | ATTACH_AUTO => {}
                _ => self.add(
                    format!("{path}.attach"),
                    format!("attach must be {ATTACH_DECISION} or {ATTACH_AUTO}"),
                ),
            }
        }
        if let Some(g) = &m.guardian {
            if m.integrates.is_empty() {
                self.add(
                    "guardian",
                    "a guardian applies to the changes the methodology integrates: declare integrates",
                );
            }
            if let Err(e) = goap_guard::compile(&g.merge) {
                self.add("guardian.merge", e.to_string());
            }
            for (name, expr) in &g.transitions {
                if let Err(e) = goap_guard::compile(expr) {
                    self.add(format!("guardian.transitions.{name}"), e.to_string());
                }
            }
        }
        for (i, sub) in m.on.iter().enumerate() {
            let path = format!("on[{i}].from");
            self.check_from(path, &sub.from);
        }
    }

    /// The check of the origin of a subscription, at the path of its from.
    pub(crate) fn check_from(&mut self, path: String, from: &str) {
        match from {
            "" | FROM_SELF | FROM_INTEGRATOR | FROM_DEPENDENCY => {}
            FROM_INTEGRATED => {
                if self.m.integrates.is_empty() {
                    self.add(
                        path,
                        format!(
                            "from {} needs integrates: the methodology integrates no change",
                            q(FROM_INTEGRATED)
                        ),
                    );
                }
            }
            _ => self.add(
                path,
                format!(
                    "from must be {FROM_SELF}, {FROM_INTEGRATED}, {FROM_INTEGRATOR} or {FROM_DEPENDENCY}"
                ),
            ),
        }
    }
}
