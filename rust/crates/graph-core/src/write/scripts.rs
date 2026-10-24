//! The algorithms a domain plugs (Go's `graph/graph/algorithms.go`, ADR 0018): the validators of the properties of a
//! node and of its links, the guards and actions of a lifecycle transition, run on QuickJS as Go runs them on goja.
use std::collections::HashMap;

use graph_catalog::{Catalog, algo, def::Transition};
use graph_script::guard::{Env, Guard};
use graph_script::js::{self, Input};
use serde_json::{Map, Value, json};

use crate::change::Change;
use crate::impact::{ChangeImpact, Draft, DraftLink};
use crate::model::{NodeRef, Props};
use crate::store::ReadTx;
use crate::{GraphError, Result};

/// A node as a script sees it (`dslNode`).
pub fn node(id: &str, version: i32, key: &str, typ: &str, state: &str, props: &Props) -> js::Node {
    js::Node {
        id: id.to_string(),
        version: i64::from(version),
        key: key.to_string(),
        typ: typ.to_string(),
        state: state.to_string(),
        props: props.clone().unwrap_or_default(),
    }
}

/// A draft as a script sees it (`draftNode`): version 0.
pub fn draft_node(d: &Draft) -> js::Node {
    node(d.node.as_str(), 0, &d.key, &d.typ, &d.state, &d.props)
}

fn quote(s: &str) -> String {
    serde_json::to_string(s).unwrap_or_default()
}

/// The validators of the type of a node over its properties (the validator half of Go's `validateProps`): the first
/// rejection is the error.
pub fn validate(cat: &Catalog, n: &js::Node) -> Result<()> {
    for v in cat.bound_validators(&n.typ) {
        let input = Input {
            node: n.clone(),
            property: v.property.clone(),
            value: n.props.get(&v.property).cloned(),
            ..Input::default()
        };
        let out = js::run_algorithm(v, &input);
        let (key, typ) = (&n.key, &n.typ);
        if v.typ == algo::NODE_VALIDATOR {
            match out {
                Err(e) => {
                    return Err(GraphError::invalid(format!(
                        "{key} ({typ}): node validator {}: {e}",
                        v.instance
                    )));
                }
                Ok(o) if !o.ok() => {
                    return Err(GraphError::invalid(format!(
                        "{key} ({typ}) is invalid: {} (validator {})",
                        o.failures[0], v.instance
                    )));
                }
                Ok(_) => continue,
            }
        }
        let p = quote(&v.property);
        match out {
            Err(e) => return Err(GraphError::invalid(format!("{key} ({typ}): property {p}: {e}"))),
            Ok(o) if !o.ok() => {
                return Err(GraphError::invalid(format!(
                    "{key} ({typ}): property {p} is invalid: {} (validator {})",
                    o.failures[0], v.instance
                )));
            }
            Ok(_) => {}
        }
    }
    Ok(())
}

/// The validators a link type plugs (Go's `runLinkValidators`): the property validators of its attributes, then its
/// link validators, given both ends; a target not resolved skips the link validators.
pub fn validate_link(
    cat: &Catalog,
    from: &js::Node,
    typ: &str,
    props: Option<&Map<String, Value>>,
    to: Option<&js::Node>,
) -> Result<()> {
    let (avs, lvs) = (cat.link_attribute_validators(typ), cat.link_type_validators(typ));
    if avs.is_empty() && lvs.is_empty() {
        return Ok(());
    }
    let key = &from.key;
    for v in avs {
        let input = Input {
            node: from.clone(),
            property: v.property.clone(),
            value: props.and_then(|p| p.get(&v.property)).cloned(),
            ..Input::default()
        };
        let p = quote(&v.property);
        match js::run_algorithm(v, &input) {
            Err(e) => return Err(GraphError::invalid(format!("{key}: link {typ}: property {p}: {e}"))),
            Ok(o) if !o.ok() => {
                return Err(GraphError::invalid(format!(
                    "{key}: link {typ}: property {p} is invalid: {} (validator {})",
                    o.failures[0], v.instance
                )));
            }
            Ok(_) => {}
        }
    }
    let Some(to) = to else { return Ok(()) };
    let input = Input {
        link_type: typ.to_string(),
        link_props: props.cloned().unwrap_or_default(),
        from: from.clone(),
        to: to.clone(),
        ..Input::default()
    };
    for v in lvs {
        let tk = &to.key;
        match js::run_algorithm(v, &input) {
            Err(e) => {
                return Err(GraphError::invalid(format!(
                    "{key}: link {typ} to {tk}: link validator {}: {e}",
                    v.instance
                )));
            }
            Ok(o) if !o.ok() => {
                return Err(GraphError::invalid(format!(
                    "{key}: link {typ} to {tk} is invalid: {} (validator {})",
                    o.failures[0], v.instance
                )));
            }
            Ok(_) => {}
        }
    }
    Ok(())
}

/// The nodes the links of a draft reach, for the link validators of their types (Go's `draftResolver`): a draft
/// reference, or the version a draft was checked out from, is that draft; else the stored version. A link of a type
/// with no link validator, an external target or a node not found is not resolved.
pub async fn draft_targets<T: ReadTx>(
    tx: &T,
    cat: &Catalog,
    links: &[DraftLink],
    drafts: &HashMap<String, Draft>,
) -> Result<Vec<Option<js::Node>>> {
    let mut out = Vec::with_capacity(links.len());
    for l in links {
        if cat.link_type_validators(&l.typ).is_empty() || !l.target.is_zero() {
            out.push(None);
            continue;
        }
        let draft = drafts
            .values()
            .find(|d| d.node == l.to.id && (l.to.is_draft() || d.base.as_ref() == Some(&l.to)));
        out.push(match draft {
            Some(d) => Some(draft_node(d)),
            None if l.to.is_draft() => None,
            None => stored_target(tx, &l.to).await?,
        });
    }
    Ok(out)
}

/// A stored version a link reaches, for the link validators (Go's `storedResolver`).
pub async fn stored_target<T: ReadTx>(tx: &T, r: &NodeRef) -> Result<Option<js::Node>> {
    Ok(tx
        .node(r)
        .await?
        .map(|n| node(n.id.as_str(), n.version, &n.key, &n.typ, &n.state, &n.props)))
}

/// What a transition is taken with (Go's `applier` of a move): the node in the state it goes to and its draft, the
/// parts it contains, the change and the impact of the node as its flow sees it.
pub struct Move<'a> {
    pub node: &'a js::Node,
    pub draft: &'a Draft,
    pub children: &'a [js::Node],
    pub change: &'a Change,
    pub impact: &'a ChangeImpact,
    pub transition: &'a Transition,
}

impl Move<'_> {
    fn input(&self, n: &js::Node) -> Input {
        let (c, t) = (self.change, self.transition);
        Input {
            node: n.clone(),
            children: self.children.to_vec(),
            change: js::ChangeInfo {
                id: c.id.as_str().to_string(),
                title: c.title.clone(),
                intent: c.intent.clone(),
                methodology: c.methodology.clone(),
                goal: c.goal.clone(),
            },
            transition: [t.name.clone(), t.from.clone(), t.to.clone()],
            ..Input::default()
        }
    }

    /// The guard of the transition, then its guard algorithms (the end of Go's `checkTransition`, `runGuards`).
    pub fn check(&self) -> Result<()> {
        let (n, t) = (self.node, self.transition);
        if !t.guard.is_empty() {
            let g = Guard::compile(&t.guard).map_err(|e| GraphError::invalid(format!("guard of {}: {e}", t.name)))?;
            let ok = g
                .check(&self.env())
                .map_err(|e| GraphError::invalid(format!("guard of {} on {}: {e}", t.name, n.key)))?;
            if !ok {
                return Err(GraphError::invalid(format!(
                    "{} ({}) cannot take {}: guard not satisfied ({})",
                    n.key, n.typ, t.name, t.guard
                )));
            }
        }
        let mut input = self.input(n);
        input.impact = [self.impact.intent.clone(), self.impact.review.clone()];
        for g in &t.guard_algos {
            match js::run_algorithm(g, &input) {
                Err(e) => {
                    return Err(GraphError::invalid(format!(
                        "guard {} of {} on {}: {e}",
                        g.instance, t.name, n.key
                    )));
                }
                Ok(o) if !o.ok() => {
                    return Err(GraphError::invalid(format!(
                        "{} ({}) cannot take {}: {} (guard {})",
                        n.key, n.typ, t.name, o.failures[0], g.instance
                    )));
                }
                Ok(_) => {}
            }
        }
        Ok(())
    }

    /// The action algorithms of the transition in order (Go's `runActions`): the properties they set and unset, the
    /// properties left validated again.
    pub fn actions(&self, cat: &Catalog) -> Result<(Map<String, Value>, Vec<String>)> {
        let (n, t) = (self.node, self.transition);
        let (mut set, mut unset) = (Map::new(), Vec::new());
        if t.action_algos.is_empty() {
            return Ok((set, unset));
        }
        let mut cur = n.clone();
        let mut changed = false;
        for act in &t.action_algos {
            let out = js::run_algorithm(act, &self.input(&cur))
                .map_err(|e| GraphError::invalid(format!("action {} of {} on {}: {e}", act.instance, t.name, n.key)))?;
            if !out.ok() {
                return Err(GraphError::invalid(format!(
                    "{} ({}): action {} of {} failed: {}",
                    n.key, n.typ, act.instance, t.name, out.failures[0]
                )));
            }
            if !out.set.is_empty() || !out.unset.is_empty() {
                for (k, v) in out.set {
                    cur.props.insert(k.clone(), v.clone());
                    set.insert(k, v);
                }
                for k in out.unset {
                    cur.props.remove(&k);
                    set.remove(&k);
                    unset.push(k);
                }
                changed = true;
            }
        }
        if !changed {
            return Ok((Map::new(), Vec::new()));
        }
        // validated as the node stands (Go validates n with the new properties)
        let mut after = n.clone();
        after.props = cur.props;
        super::check::attributes(cat, &after.key, &after.typ, Some(&after.props))
            .and_then(|()| validate(cat, &after))
            .map_err(|e| e.with_prefix(&format!("after the actions of {}: ", t.name)))?;
        Ok((set, unset))
    }

    /// The variables of the guard (Go's `nodeView`, `impactView`, `draftView` and the change).
    fn env(&self) -> Env {
        let view = |n: &js::Node| json!({"key": n.key, "type": n.typ, "state": n.state, "props": n.props});
        let obj = |v: Value| v.as_object().cloned().unwrap_or_default();
        let c = self.change;
        let reviews: Vec<Value> = self
            .impact
            .reviews
            .iter()
            .map(|r| json!({"status": r.status, "by": r.by, "comment": r.comment}))
            .collect();
        let d = self.draft;
        let links: Vec<Value> = d
            .links
            .iter()
            .map(|l| {
                if l.target.is_zero() {
                    json!({"type": l.typ, "to": l.to.id.as_str(), "version": l.to.version})
                } else {
                    json!({"type": l.typ, "url": l.target.to_string()})
                }
            })
            .collect();
        Env {
            node: obj(view(self.node)),
            children: self.children.iter().map(view).collect(),
            change: obj(json!({"id": c.id.as_str(), "title": c.title, "intent": c.intent,
                "methodology": c.methodology, "goal": c.goal, "state": c.state, "status": c.status})),
            impact: obj(json!({"intent": self.impact.intent, "review": self.impact.review, "reviews": reviews})),
            draft: obj(json!({"key": d.key, "type": d.typ, "state": d.state,
                "props": d.props.clone().unwrap_or_default(), "owner": d.owner.to_string(), "links": links,
                "base": d.base.as_ref().map_or(0, |b| b.version), "new": d.base.is_none()})),
        }
    }
}
