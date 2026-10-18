//! The type index a change is judged by, the views a guard reads and the checks made when a transition is taken.
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

// ---- the checks of a transition ----------------------------------------------------------------------------------

impl crate::apply::Applier<'_> {
    /// childOf is the node a link of a draft to a part of it targets as the flow sees it: the draft of the child, else
    /// the version of the view of the namespace (None: the node is not in it).
    pub(crate) async fn child_of(
        &self,
        ctx: &goap_ctx::Ctx,
        tx: &mut dyn crate::repo::Tx,
        l: &goap_domain::Link,
    ) -> Result<Option<Node>> {
        if let Some(r) = &self.drafts
            && let Some(d) = r.draft(&r.normalize(&l.to))
        {
            return Ok(Some(crate::draft::draft_node(&self.change, d)));
        }
        let Some(v) = self.target.get(&l.to.id) else {
            return Ok(None);
        };
        let c = tx
            .node(
                ctx,
                &goap_domain::NodeRef {
                    id: l.to.id.clone(),
                    version: *v,
                },
            )
            .await?;
        Ok(Some(c))
    }

    /// checkTransition checks a transition against the node as it stands (n, the state it leaves) and its outgoing
    /// links (out): the attributes and links it requires, the states of the parts of a document, its guard (CEL: node,
    /// children, change, impact, draft) and its guard algorithms. self.draft is the draft in the state the move goes
    /// to.
    pub(crate) async fn check_transition(
        &self,
        ctx: &goap_ctx::Ctx,
        tx: &mut dyn crate::repo::Tx,
        n: &Node,
        out: &[goap_domain::Link],
        t: &goap_domain::Transition,
    ) -> Result<Vec<Node>> {
        for k in &t.requires.attributes {
            let missing = match n.properties.get(k) {
                None | Some(Value::Null) => true,
                Some(Value::String(s)) => s.is_empty(),
                _ => false,
            };
            if missing {
                return Err(invalidf(format!(
                    "{} ({}) cannot take {}: attribute {k:?} is required",
                    n.key, n.r#type, t.name
                )));
            }
        }
        for typ in &t.requires.outgoing_links {
            if !out.iter().any(|l| &l.r#type == typ) {
                return Err(invalidf(format!(
                    "{} ({}) cannot take {}: an outgoing {typ:?} link is required",
                    n.key, n.r#type, t.name
                )));
            }
        }
        let mut children = Vec::new();
        if t.children.is_some()
            || !t.guard.is_empty()
            || !t.guard_algos.is_empty()
            || !t.action_algos.is_empty()
        {
            for l in out {
                if !goap_domain::is_contains(&l.r#type) {
                    continue;
                }
                let Some(c) = self.child_of(ctx, tx, l).await? else {
                    continue;
                };
                if let Some(rule) = &t.children
                    && !rule.states.contains(&c.state)
                {
                    let got = if c.state.is_empty() {
                        "no state".to_string()
                    } else {
                        c.state.clone()
                    };
                    return Err(invalidf(format!(
                        "{} ({}) cannot take {}: contained {} ({}) is in {got}, expected {}",
                        n.key,
                        n.r#type,
                        t.name,
                        c.key,
                        c.r#type,
                        rule.states.join(" or ")
                    )));
                }
                children.push(c);
            }
        }
        if !t.guard.is_empty() {
            let gd = goap_guard::compile(&t.guard)
                .map_err(|e| invalidf(format!("guard of {}: {e}", t.name)))?;
            let views: Vec<Value> = children.iter().map(node_view).collect();
            let change = json!({"id": self.change.id.to_string(), "title": self.change.title,
                "intent": self.change.intent, "methodology": self.change.methodology, "goal": self.change.goal,
                "state": self.change.state, "status": self.change.status.to_string()});
            let obj = |v: Value| match v {
                Value::Object(m) => m,
                _ => Map::new(),
            };
            let ok = gd
                .check(
                    &obj(node_view(n)),
                    &views,
                    &obj(change),
                    Some(&obj(impact_view(self.impact.as_ref()))),
                    Some(&obj(draft_view(self.draft.as_ref()))),
                )
                .map_err(|e| invalidf(format!("guard of {} on {}: {e}", t.name, n.key)))?;
            if !ok {
                return Err(invalidf(format!(
                    "{} ({}) cannot take {}: guard not satisfied ({})",
                    n.key, n.r#type, t.name, t.guard
                )));
            }
        }
        self.run_guards(ctx, n, t, &children).await?;
        Ok(children)
    }
}
