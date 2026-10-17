//! Lifecycles of node types (ADR 0014) and the gates of the lifecycle of a change (ADR 0058, 0075).

use std::collections::{BTreeMap, HashMap, HashSet, VecDeque};

use goap_algo::Bound;
use serde::{Deserialize, Serialize};

use crate::{
    TYPE_SEP,
    serde_util::{is_false, null_default},
};

/// Lifecycle is a named state machine of a domain (ADR 0014). Node types refer to it by name (NodeType.Lifecycle),
/// inherit it through extends (a subtype that names its own lifecycle replaces the inherited one), and every node
/// version stores its state. Every state is landable unless it opts out (NotLandable, ADR 0078): a change can only
/// land when each node it holds ends in a landable state. A node is edited in any state while it is in a change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Lifecycle {
    /// Name identifies the lifecycle in its domain; node types refer to it.
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub initial: String,
    #[serde(deserialize_with = "null_default")]
    pub states: Vec<LifecycleState>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub transitions: Vec<Transition>,
}

/// LifecycleState is one state of a lifecycle.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LifecycleState {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    /// NotLandable opts the state out of landing (ADR 0078): a change cannot land while a node it holds rests in it
    /// (a draft to finish, a review to pass). Every other state is landable.
    #[serde(skip_serializing_if = "is_false")]
    pub not_landable: bool,
    /// Final states have no way out.
    #[serde(rename = "final", skip_serializing_if = "is_false")]
    pub r#final: bool,
}

/// Transition moves a node from one state to another.
///
/// Go writes GuardAlgos / ActionAlgos in JSON only (never in YAML); serde has one set of attributes, so a YAML
/// export carries them too when they are resolved.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Transition {
    pub name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub from: String,
    pub to: String,
    /// Permission ("type:action") the actor must hold; empty: node:transition.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub permission: String,
    /// Guard is a CEL predicate over the node (node.props, node.state) and its contained children (children),
    /// evaluated when the change is applied.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub guard: String,
    /// Vetos and Objectives split the criteria of a gate of the lifecycle of a change (ADR 0075 §3, ADR 0058),
    /// evaluated in the environment of Guard: a veto not met blocks the transition, whatever else holds; an objective
    /// not met blocks it too unless a derogation in force names it (its rule is the name of the objective), when the
    /// transition goes with reserve. They are two lists of the guard, not another kind of gate.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub vetos: Vec<Criterion>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub objectives: Vec<Criterion>,
    /// Guards are algorithm instances of type transition_guard (ADR 0018), run in order after the CEL guard; all must
    /// accept.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub guards: Vec<String>,
    /// Actions are algorithm instances of type transition_action, run in order once the transition is accepted; they
    /// may change properties of the node.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub actions: Vec<String>,
    /// GuardAlgos and ActionAlgos are the instances resolved with their algorithm. They are filled when the type
    /// catalogue resolves the lifecycle (never authored), so that evaluating a change needs no other lookup.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub guard_algos: Vec<Bound>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub action_algos: Vec<Bound>,
    /// Requires is always written in JSON (Go's omitempty has no effect on a struct).
    pub requires: TransitionRequires,
    /// Children (documents): every contained child must be in one of these states for the transition to be
    /// accepted. Validation only, no cascade.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub children: Option<ChildrenRule>,
}

/// Criterion is one criterion of a gate (ADR 0075 §3): a named CEL predicate over the same variables as a guard.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Criterion {
    pub name: String,
    pub expr: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
}

/// GateResult is what the criteria of a gate came to for a change.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct GateResult {
    /// Vetoed are the vetos not met: any blocks the transition.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub vetoed: Vec<String>,
    /// Unmet are the objectives not met, covered or not.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub unmet: Vec<String>,
    /// Reserve are the keys of the records (derogations in force) that cover the unmet objectives, by objective
    /// name: the transition goes with reserve.
    #[serde(
        skip_serializing_if = "BTreeMap::is_empty",
        deserialize_with = "null_default"
    )]
    pub reserve: BTreeMap<String, String>,
}

impl GateResult {
    /// Uncovered are the objectives not met that nothing covers.
    pub fn uncovered(&self) -> Vec<String> {
        self.unmet
            .iter()
            .filter(|n| !self.reserve.contains_key(*n))
            .cloned()
            .collect()
    }

    /// Passed reports whether the transition may go: no veto is unmet and every unmet objective is covered.
    pub fn passed(&self) -> bool {
        self.vetoed.is_empty() && self.uncovered().is_empty()
    }

    /// WithReserve reports whether the transition goes although objectives are unmet.
    pub fn with_reserve(&self) -> bool {
        self.vetoed.is_empty() && !self.unmet.is_empty() && self.passed()
    }
}

/// TransitionRequires lists what the node must have to take a transition.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct TransitionRequires {
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub attributes: Vec<String>,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub outgoing_links: Vec<String>,
}

/// ChildrenRule constrains the state of the children of a document.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ChildrenRule {
    #[serde(deserialize_with = "null_default")]
    pub states: Vec<String>,
}

/// DocumentSpec makes a node type a document: it embeds nodes of the listed types through outgoing LINK_CONTAINS
/// links.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct DocumentSpec {
    #[serde(deserialize_with = "null_default")]
    pub contains: Vec<String>,
}

/// LinkContains is the name of the link type between a document and the nodes it embeds (declared by the domain of
/// the document: alm@contains).
pub const LINK_CONTAINS: &str = "contains";

/// IsContains reports a document link: a link type named LINK_CONTAINS, bare or qualified.
pub fn is_contains(link_type: &str) -> bool {
    link_type == LINK_CONTAINS || link_type.ends_with(&format!("{TYPE_SEP}{LINK_CONTAINS}"))
}

impl Lifecycle {
    /// State returns the named state.
    pub fn state(&self, name: &str) -> Option<&LifecycleState> {
        self.states.iter().find(|s| s.name == name)
    }

    /// Landable tells whether a node in the state may land with its change. With no state (or one the lifecycle does
    /// not know), a node is always landable (a node without a lifecycle too: `landable_in(None, _)`).
    pub fn landable(&self, state: &str) -> bool {
        if state.is_empty() {
            return true;
        }
        self.state(state).is_none_or(|s| !s.not_landable)
    }

    /// Landable for a lifecycle that may be absent (Go's nil receiver): always landable without one.
    pub fn landable_in(l: Option<&Lifecycle>, state: &str) -> bool {
        l.is_none_or(|l| l.landable(state))
    }

    /// Transition finds the transition named name out of the state from.
    pub fn transition(&self, from: &str, name: &str) -> Option<&Transition> {
        self.transitions
            .iter()
            .find(|t| t.from == from && t.name == name)
    }

    /// Move finds the transition from one state to another (the first declared). Go's `Move`.
    pub fn move_to(&self, from: &str, to: &str) -> Option<&Transition> {
        self.transitions
            .iter()
            .find(|t| t.from == from && t.to == to)
    }

    /// Issues checks the lifecycle and returns one message per problem.
    pub fn issues(&self) -> Vec<String> {
        let mut out = Vec::new();
        let mut names = HashSet::new();
        let mut landable = 0;
        for s in &self.states {
            if s.name.is_empty() {
                out.push("a state has no name".to_string());
                continue;
            }
            if names.contains(s.name.as_str()) {
                out.push(format!("duplicate state {}", s.name));
                continue;
            }
            names.insert(s.name.as_str());
            if !s.not_landable {
                landable += 1;
            } else if s.r#final {
                out.push(format!(
                    "state {} is final and not landable: a node there could never land",
                    s.name
                ));
            }
        }
        if self.states.is_empty() {
            out.push("at least one state required".to_string());
            return out;
        }
        if !names.contains(self.initial.as_str()) {
            out.push(format!("initial state {:?} is not a state", self.initial));
        }
        if landable == 0 {
            out.push(
                "at least one landable state required (every state is flagged notLandable)"
                    .to_string(),
            );
        }
        let mut seen: HashSet<(&str, &str)> = HashSet::new();
        let mut next: HashMap<&str, Vec<&str>> = HashMap::new();
        for (i, t) in self.transitions.iter().enumerate() {
            if t.name.is_empty() {
                out.push(format!("transition {i} has no name"));
            } else if !names.contains(t.from.as_str()) {
                out.push(format!("transition {}: unknown state {:?}", t.name, t.from));
            } else if !names.contains(t.to.as_str()) {
                out.push(format!("transition {}: unknown state {:?}", t.name, t.to));
            } else if seen.contains(&(t.from.as_str(), t.name.as_str())) {
                out.push(format!("duplicate transition {} from {}", t.name, t.from));
            } else {
                seen.insert((t.from.as_str(), t.name.as_str()));
                next.entry(t.from.as_str()).or_default().push(t.to.as_str());
                if self.state(&t.from).is_some_and(|s| s.r#final) {
                    out.push(format!(
                        "transition {} leaves the final state {}",
                        t.name, t.from
                    ));
                }
            }
            if let Some(c) = &t.children {
                for st in &c.states {
                    if st.is_empty() {
                        out.push(format!("transition {}: empty child state", t.name));
                    }
                }
                if c.states.is_empty() {
                    out.push(format!(
                        "transition {}: children needs at least one state",
                        t.name
                    ));
                }
            }
        }
        // a state that cannot land must be able to reach one that can
        for s in &self.states {
            if !s.not_landable || !names.contains(s.name.as_str()) {
                continue;
            }
            let mut reach: HashSet<&str> = HashSet::from([s.name.as_str()]);
            let mut todo: VecDeque<&str> = VecDeque::from([s.name.as_str()]);
            let mut ok = false;
            while !ok && let Some(cur) = todo.pop_front() {
                for &n in next.get(cur).map(Vec::as_slice).unwrap_or(&[]) {
                    if !reach.insert(n) {
                        continue;
                    }
                    if self.state(n).is_none_or(|st| !st.not_landable) {
                        ok = true;
                        break;
                    }
                    todo.push_back(n);
                }
            }
            if !ok {
                out.push(format!(
                    "state {} is not landable and cannot reach a landable state",
                    s.name
                ));
            }
        }
        out
    }

    /// StateNames lists the states.
    pub fn state_names(&self) -> Vec<String> {
        self.states.iter().map(|s| s.name.clone()).collect()
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn st(name: &str, not_landable: bool) -> LifecycleState {
        LifecycleState {
            name: name.into(),
            not_landable,
            ..Default::default()
        }
    }

    fn tr(name: &str, from: &str, to: &str) -> Transition {
        Transition {
            name: name.into(),
            from: from.into(),
            to: to.into(),
            ..Default::default()
        }
    }

    #[test]
    fn issues_and_landing() {
        let l = Lifecycle {
            name: "doc".into(),
            initial: "draft".into(),
            states: vec![st("draft", true), st("ok", false), st("stuck", true)],
            transitions: vec![tr("approve", "draft", "ok")],
            ..Default::default()
        };
        assert_eq!(
            l.issues(),
            vec!["state stuck is not landable and cannot reach a landable state".to_string()]
        );
        assert!(
            !l.landable("draft") && l.landable("ok") && l.landable("unknown") && l.landable("")
        );
        assert!(Lifecycle::landable_in(None, "draft"));
        assert_eq!(
            l.move_to("draft", "ok").map(|t| t.name.as_str()),
            Some("approve")
        );
        assert!(
            is_contains("alm@contains") && is_contains("contains") && !is_contains("xcontains")
        );
        let v = serde_json::to_value(tr("a", "x", "y")).unwrap();
        assert_eq!(v["requires"], serde_json::json!({}));
    }
}
