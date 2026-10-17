//! Part of `lifecycle.go` (G1): the type index a change is judged by and the views a guard reads. The checks made
//! when a transition is taken (`checkTransition`, `childOf`, which need the applier of `apply.go`) belong to a later
//! slice (see PORTING.md).
//!
//! The lifecycle rules (ADR 0014): a node is modified in any state while it is in a change, and a change lands only
//! when each node it holds rests in a landable state (ADR 0078); every transition is checked when it is taken, on the
//! draft of the node (ImpactNodeTransition, ADR 0076, 0079).

use std::sync::Arc;

use goap_algo::Bound;
use goap_domain::{ChangeImpact, Draft, Lifecycle, Node};
use serde_json::{Map, Value, json};

use crate::graph::TypeCatalog;
use crate::{Error, ErrorKind, Graph, Result};

/// typeIndex is the node type metadata a change is judged by: the type catalogue in force (ADR 0012 §2). Without one
/// (Graph::types unset) the graph is untyped: no lifecycle, no validator, no check.
#[derive(Clone, Default)]
pub(crate) struct TypeIndex {
    pub(crate) cat: Option<Arc<dyn TypeCatalog>>,
}

impl Graph {
    /// typesAt returns the types a change is judged by: the catalogue in force (Go takes a baseline it ignores).
    pub(crate) fn types_at(&self) -> TypeIndex {
        TypeIndex {
            cat: if self.types.is_some() {
                self.catalog()
            } else {
                None
            },
        }
    }

    /// LifecycleControlled tells whether direct writes to nodes of a type are refused (the type has a lifecycle):
    /// they must go through a change. The type catalogue is process-global, so this needs no baseline.
    pub fn lifecycle_controlled(&self, typ: &str) -> bool {
        self.catalog().is_some_and(|c| c.lifecycle(typ).is_some())
    }
}

impl TypeIndex {
    pub(crate) fn lifecycle_of(&self, typ: &str) -> Option<Lifecycle> {
        self.cat.as_ref().and_then(|c| c.lifecycle(typ).cloned())
    }

    /// checkNode is the existence rule (ADR 0012 §3) for a node of namespace ns; nothing is checked without a
    /// catalogue.
    pub(crate) fn check_node(&self, ns: &str, typ: &str) -> Result<()> {
        match &self.cat {
            Some(c) => c
                .check_node(ns, typ)
                .map_err(|e| Error::wrap(ErrorKind::Invalid, e)),
            None => Ok(()),
        }
    }

    /// checkLink checks a link type and its ends; nothing is checked without a catalogue.
    pub(crate) fn check_link(&self, typ: &str, from: &str, to: &str) -> Result<()> {
        match &self.cat {
            Some(c) => c
                .check_link(typ, from, to)
                .map_err(|e| Error::wrap(ErrorKind::Invalid, e)),
            None => Ok(()),
        }
    }

    /// validatorsOf lists the property validators of a type in call order: the ones of the supertypes first, each
    /// type's in declaration order (algorithms.go).
    pub(crate) fn validators_of(&self, typ: &str) -> Vec<Bound> {
        match &self.cat {
            Some(c) => c.validators(typ).to_vec(),
            None => Vec::new(),
        }
    }
}

/// invalidf: Go's `fmt.Errorf(format+": %w", ..., ErrInvalid)`.
pub(crate) fn invalidf(msg: impl std::fmt::Display) -> Error {
    Error::wrap(ErrorKind::Invalid, msg)
}

// ---- views a guard reads -----------------------------------------------------------------------------------------

/// nodeView is a node as a guard sees it: key, type, state, props.
pub(crate) fn node_view(n: &Node) -> Value {
    json!({"key": n.key, "type": n.r#type, "state": n.state, "props": Value::Object(n.properties.clone())})
}

/// impactView is the impact of a node as a guard sees it (ADR 0076): intent, review and the review history.
pub(crate) fn impact_view(cn: Option<&ChangeImpact>) -> Value {
    let Some(cn) = cn else {
        return Value::Object(Map::new());
    };
    let reviews: Vec<Value> = cn
        .reviews
        .iter()
        .map(|r| json!({"status": r.status.to_string(), "by": r.by, "comment": r.comment}))
        .collect();
    json!({"intent": cn.intent.to_string(), "review": cn.review.to_string(), "reviews": reviews})
}

/// draftView is the draft of the node a transition moves, as a guard sees it (ADR 0079): the state it goes to, its
/// properties, its outgoing links, the version it was checked out from and whether it is a creation.
pub(crate) fn draft_view(d: Option<&Draft>) -> Value {
    let Some(d) = d else {
        return Value::Object(Map::new());
    };
    let links: Vec<Value> = d
        .links
        .iter()
        .map(|l| json!({"type": l.r#type, "to": l.to.id.to_string(), "version": l.to.version}))
        .collect();
    let base = d.base.as_ref().map(|b| b.version).unwrap_or(0);
    json!({"key": d.key, "type": d.r#type, "state": d.state, "props": Value::Object(d.properties.clone()),
        "owner": d.owner.to_string(), "links": links, "base": base, "new": d.base.is_none()})
}
