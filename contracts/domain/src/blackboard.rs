//! The blackboard a process observes.

use std::{any::Any, collections::HashMap, fmt, sync::Arc};

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};

use crate::{
    Change, DecisionPoint, Flow, Link, Node, NodeRef, Version, gotime,
    serde_util::{is_false, null_default},
};

/// NodeView is a node version hydrated with its neighbourhood in the domain graph. It lets conditions navigate the
/// reference graph without I/O. It derefs to its node (Go embeds it).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NodeView {
    #[serde(flatten)]
    pub node: Node,
    pub latest: Version,
    /// NotLandable: the node has a lifecycle and is in a state flagged notLandable (ADR 0078) (a change cannot land
    /// while a node rests there).
    #[serde(skip_serializing_if = "is_false")]
    pub not_landable: bool,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub out: Vec<Link>,
    #[serde(
        rename = "in",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#in: Vec<Link>,
}

impl std::ops::Deref for NodeView {
    type Target = Node;
    fn deref(&self) -> &Node {
        &self.node
    }
}

impl std::ops::DerefMut for NodeView {
    fn deref_mut(&mut self) -> &mut Node {
        &mut self.node
    }
}

/// A facet of a blackboard: any value of a use case, read back by its type (Facet).
pub type FacetValue = Arc<dyn Any + Send + Sync>;

/// Blackboard is the state an agent process observes: the change (axis change) plus a hydrated view of every domain
/// node it references (axis domain).
#[derive(Clone, Default, Serialize, Deserialize)]
#[serde(default)]
pub struct Blackboard {
    pub change: Change,
    #[serde(skip)]
    pub nodes: HashMap<NodeRef, NodeView>,
    /// Neighbors holds the endpoints of the links of hydrated nodes.
    #[serde(skip)]
    pub neighbors: HashMap<NodeRef, Node>,
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub vars: Map<String, Value>,
    /// Supertypes maps node types to their ancestors (subtyping of the methodology schema), exposed to conditions as
    /// x.types.
    #[serde(skip)]
    pub supertypes: HashMap<String, Vec<String>>,
    /// Facets are the use-case parts of the observation the graph gives apart from the change and its nodes, by name:
    /// what a view of a flow does not carry, replayed at At. The graph fills the built-in ones (options, active
    /// option, decision points: FACET_OPTIONS, FACET_ACTIVE_OPTION, FACET_DECISION_POINTS, ADR 0009, 0032) and the
    /// providers set on it (Graph.Facets); readers use the typed accessors (options_of, active_option_of,
    /// decision_points_of, facet) rather than the map.
    #[serde(skip)]
    pub facets: HashMap<String, FacetValue>,
    /// At is the moment of the observation (None: Go's zero time).
    #[serde(with = "gotime::option")]
    pub at: Option<DateTime<Utc>>,
}

impl fmt::Debug for Blackboard {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        let mut facets: Vec<&String> = self.facets.keys().collect();
        facets.sort();
        f.debug_struct("Blackboard")
            .field("change", &self.change)
            .field("nodes", &self.nodes)
            .field("neighbors", &self.neighbors)
            .field("vars", &self.vars)
            .field("supertypes", &self.supertypes)
            .field("facets", &facets)
            .field("at", &self.at)
            .finish()
    }
}

/// Names of the built-in facets of a blackboard: `Vec<Flow>`, the options of the change (ADR 0032 §6).
pub const FACET_OPTIONS: &str = "options";
/// `String`: the option the change works on.
pub const FACET_ACTIVE_OPTION: &str = "activeOption";
/// `Vec<DecisionPoint>`: replayed at Blackboard.at (ADR 0009 §4).
pub const FACET_DECISION_POINTS: &str = "decisionPoints";
/// FacetCriticalityPolicy is what the organisation of the change requires of its criticality (ADR 0075 §3), set by a
/// provider of Graph.Facets that reads the organisation; opaque to the graph. Absent: the compiled-in table.
pub const FACET_CRITICALITY_POLICY: &str = "criticalityPolicy";
/// FacetDependencies are the changes the change depends on (ADR 0108), `Vec<Dependency>`, each saying whether its
/// work has reached the branch the change merges into.
pub const FACET_DEPENDENCIES: &str = "dependencies";
/// FacetHistory is the history of the change (ADR 0109 §4): what the runs on it did, step by step, folded from its
/// log by whoever reads the blackboard to derive conditions (the engine); opaque to the graph.
pub const FACET_HISTORY: &str = "history";

/// Facet is the facet of the blackboard with a name, as the type the caller expects (the default value when absent or
/// of another type).
pub fn facet<T: Any + Clone + Default>(bb: &Blackboard, name: &str) -> T {
    bb.facet_ref::<T>(name).cloned().unwrap_or_default()
}

impl Blackboard {
    /// The facet with a name, borrowed, when it is of type T.
    pub fn facet_ref<T: Any>(&self, name: &str) -> Option<&T> {
        self.facets.get(name).and_then(|v| v.downcast_ref::<T>())
    }

    /// WithFacet is the blackboard with a facet set or replaced; the facets of the original are not changed.
    pub fn with_facet<T: Any + Send + Sync>(&self, name: &str, v: T) -> Blackboard {
        let mut bb = self.clone();
        bb.facets.insert(name.to_string(), Arc::new(v));
        bb
    }

    /// TypesOf returns a type followed by its supertypes.
    pub fn types_of(&self, t: &str) -> Vec<Value> {
        let mut out = vec![Value::from(t)];
        if let Some(s) = self.supertypes.get(t) {
            out.extend(s.iter().map(|s| Value::from(s.as_str())));
        }
        out
    }
}

/// OptionsOf are the options of the change.
pub fn options_of(bb: &Blackboard) -> Vec<Flow> {
    facet(bb, FACET_OPTIONS)
}

/// ActiveOptionOf is the option the change works on ("" when none).
pub fn active_option_of(bb: &Blackboard) -> String {
    facet(bb, FACET_ACTIVE_OPTION)
}

/// DecisionPointsOf are the decision points of the change, replayed at bb.at.
pub fn decision_points_of(bb: &Blackboard) -> Vec<DecisionPoint> {
    facet(bb, FACET_DECISION_POINTS)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn facets() {
        let bb = Blackboard::default().with_facet(FACET_ACTIVE_OPTION, "opt".to_string());
        assert_eq!(active_option_of(&bb), "opt");
        assert!(options_of(&bb).is_empty());
        let wrong = bb.with_facet(FACET_OPTIONS, 3_i32);
        assert!(options_of(&wrong).is_empty());
        assert!(bb.facet_ref::<i32>(FACET_OPTIONS).is_none());
        assert!(
            serde_json::to_string(&Blackboard::default())
                .unwrap()
                .ends_with(r#""at":"0001-01-01T00:00:00Z"}"#)
        );
    }

    #[test]
    fn node_view_flattens_its_node() {
        let nv = NodeView {
            node: Node {
                id: "n".into(),
                key: "K".into(),
                ..Default::default()
            },
            latest: 2,
            ..Default::default()
        };
        let v = serde_json::to_value(&nv).unwrap();
        assert_eq!(v["id"], "n");
        assert_eq!(v["latest"], 2);
        let back: NodeView = serde_json::from_value(v).unwrap();
        assert_eq!(back, nv);
        assert_eq!(back.key, "K");
    }
}
