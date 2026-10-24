//! The three-way merge of drafts a rebase makes (Go's `domain.MergeDrafts`, ADR 0101, 0103) and the patch that turns a
//! draft into another (`domain.DraftDiff`, ADR 0144).
use std::collections::{BTreeMap, BTreeSet, HashMap};

use serde_json::{Map, Value, json};

use crate::{
    gojson,
    impact::{Draft, DraftLink},
    lnk::Url,
    model::{NodeId, Props},
};

/// A conflict on the owner.
pub const CONFLICT_OWNER: &str = "owner";
/// A conflict on the state.
pub const CONFLICT_STATE: &str = "state";
/// A conflict on the whole node: missing from the fork or from the target.
pub const CONFLICT_NODE: &str = "node";

/// The name of a conflict on a property.
pub fn conflict_prop(key: &str) -> String {
    format!("props.{key}")
}

/// The name of a conflict on the link of a type to a node.
pub fn conflict_link(typ: &str, to: &NodeId) -> String {
    format!("link:{typ}:{to}")
}

/// A link of a draft named by its type and its target.
fn link_key(l: &DraftLink) -> String {
    if l.is_external() {
        return conflict_link(&l.typ, &NodeId(l.target.logical().to_string()));
    }
    conflict_link(&l.typ, &l.to.id)
}

fn links_by_key(ls: &[DraftLink]) -> HashMap<String, &DraftLink> {
    let mut out = HashMap::new();
    for l in ls {
        out.entry(link_key(l)).or_insert(l);
    }
    out
}

/// Two values the same once marshalled (Go's `sameValue`), none being the same as none.
fn same_value(a: Option<&Value>, b: Option<&Value>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => gojson::to_string(a) == gojson::to_string(b),
        _ => false,
    }
}

fn props_value(p: &Props) -> Value {
    p.as_ref().map_or(Value::Null, |m| Value::Object(m.clone()))
}

fn same_link(a: Option<&&DraftLink>, b: Option<&&DraftLink>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => {
            a.to.version == b.to.version
                && a.target == b.target
                && same_value(Some(&props_value(&a.props)), Some(&props_value(&b.props)))
        }
        _ => false,
    }
}

fn merge3<T: PartialEq + Clone>(base: &T, ours: &T, theirs: &T) -> (T, bool) {
    if ours == base {
        (theirs.clone(), false)
    } else if theirs == base || ours == theirs {
        (ours.clone(), false)
    } else {
        (ours.clone(), true)
    }
}

/// Merges ours (the change's) with theirs (the target's) against base (the version at the fork), field by field; a
/// field changed on both sides differently keeps ours and is named in the conflicts, sorted.
pub fn merge_drafts(base: &Draft, ours: &Draft, theirs: &Draft) -> (Draft, Vec<String>) {
    let mut out = ours.clone();
    let mut conflicts = Vec::new();
    let empty = Map::new();
    let (bp, op, tp) = (
        base.props.as_ref().unwrap_or(&empty),
        ours.props.as_ref().unwrap_or(&empty),
        theirs.props.as_ref().unwrap_or(&empty),
    );
    let keys: BTreeSet<&String> = bp.keys().chain(op.keys()).chain(tp.keys()).collect();
    for k in keys {
        let (b, o, t) = (bp.get(k), op.get(k), tp.get(k));
        if same_value(o, b) {
            let m = out.props.get_or_insert_with(Map::new);
            match t {
                Some(t) => {
                    m.insert(k.clone(), t.clone());
                }
                None => {
                    m.remove(k);
                }
            }
        } else if !(same_value(t, b) || same_value(o, t)) {
            conflicts.push(conflict_prop(k));
        }
    }
    let (owner, c) = merge3::<Url>(&base.owner, &ours.owner, &theirs.owner);
    out.owner = owner;
    if c {
        conflicts.push(CONFLICT_OWNER.into());
    }
    let (state, c) = merge3(&base.state, &ours.state, &theirs.state);
    out.state = state;
    if c {
        conflicts.push(CONFLICT_STATE.into());
    }
    let (links, lc) = merge_links(&base.links, &ours.links, &theirs.links);
    out.links = links;
    conflicts.extend(lc);
    conflicts.sort();
    (out, conflicts)
}

/// The outgoing links merged: ours in their order (the ones theirs changed or removed updated), then the ones theirs
/// added, by key.
fn merge_links(base: &[DraftLink], ours: &[DraftLink], theirs: &[DraftLink]) -> (Vec<DraftLink>, Vec<String>) {
    let (bm, om, tm) = (links_by_key(base), links_by_key(ours), links_by_key(theirs));
    let mut conflicts = Vec::new();
    // the outcome of a key ours did not change: theirs (None: removed)
    let mut take: BTreeMap<String, Option<DraftLink>> = BTreeMap::new();
    let keys: BTreeSet<&String> = bm.keys().chain(om.keys()).chain(tm.keys()).collect();
    for k in keys {
        let (b, o, t) = (bm.get(k), om.get(k), tm.get(k));
        if same_link(o, b) {
            take.insert(k.clone(), t.map(|t| (*t).clone()));
        } else if !(same_link(t, b) || same_link(o, t)) {
            conflicts.push(k.clone());
        }
    }
    let mut out = Vec::new();
    let mut seen = BTreeSet::new();
    for l in ours {
        let k = link_key(l);
        if !seen.insert(k.clone()) {
            out.push(l.clone()); // a second link of the same type to the same node: kept as it is
            continue;
        }
        match take.get(&k) {
            None => out.push(l.clone()),
            Some(Some(t)) => {
                let mut l = l.clone();
                l.to = t.to.clone();
                l.target = t.target.clone();
                l.props = t.props.clone();
                out.push(l);
            }
            Some(None) => {}
        }
    }
    for (k, t) in &take {
        if let Some(t) = t
            && !seen.contains(k)
        {
            out.push(DraftLink {
                id: Default::default(),
                typ: t.typ.clone(),
                to: t.to.clone(),
                target: t.target.clone(),
                props: t.props.clone(),
            });
        }
    }
    (out, conflicts)
}

/// Two property maps alike as Go's `reflect.DeepEqual` sees decoded JSON: none differs from an empty map.
fn deep_equal(a: &Props, b: &Props) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => {
            gojson::to_string(&Value::Object(a.clone())) == gojson::to_string(&Value::Object(b.clone()))
        }
        _ => false,
    }
}

/// The patch of an updated event that turns a draft into another (props, unset, owner, state, and the links as
/// addLinks / updateLinks / removeLinks); none when they hold the same.
pub fn draft_diff(from: &Draft, to: &Draft) -> Option<Map<String, Value>> {
    let mut p = Map::new();
    let empty = Map::new();
    let (fp, tp) = (
        from.props.as_ref().unwrap_or(&empty),
        to.props.as_ref().unwrap_or(&empty),
    );
    let mut props = Map::new();
    for (k, v) in tp {
        if fp.get(k).is_none_or(|w| gojson::to_string(v) != gojson::to_string(w)) {
            props.insert(k.clone(), v.clone());
        }
    }
    if !props.is_empty() {
        p.insert("props".into(), Value::Object(props));
    }
    let mut unset: Vec<&String> = fp.keys().filter(|k| !tp.contains_key(*k)).collect();
    if !unset.is_empty() {
        unset.sort();
        p.insert("unset".into(), json!(unset));
    }
    if to.owner != from.owner {
        p.insert("owner".into(), Value::String(to.owner.to_string()));
    }
    if to.state != from.state {
        p.insert("state".into(), json!({"from": from.state, "to": to.state}));
    }
    let props_of = |p: &Props| props_value(p);
    let mut used = vec![false; to.links.len()];
    let find = |used: &[bool], l: &DraftLink, same_props: bool| {
        to.links.iter().enumerate().position(|(i, m)| {
            !used[i]
                && m.typ == l.typ
                && m.to == l.to
                && m.target == l.target
                && (!same_props || deep_equal(&m.props, &l.props))
        })
    };
    let mut left = Vec::new();
    for l in &from.links {
        match find(&used, l, true) {
            Some(i) => used[i] = true,
            None => left.push(l),
        }
    }
    let (mut adds, mut updates, mut removes) = (Vec::new(), Vec::new(), Vec::new());
    for l in left {
        match find(&used, l, false) {
            Some(i) => {
                used[i] = true;
                updates.push(json!({"id": l.id, "props": props_of(&to.links[i].props)}));
            }
            None => removes.push(json!({"id": l.id})),
        }
    }
    for (i, l) in to.links.iter().enumerate() {
        if used[i] {
            continue;
        }
        adds.push(if l.is_external() {
            json!({"id": l.id, "type": l.typ, "target": l.target.to_string(), "props": props_of(&l.props)})
        } else {
            json!({"id": l.id, "type": l.typ, "toId": l.to.id, "toVersion": l.to.version, "props": props_of(&l.props)})
        });
    }
    for (k, v) in [("addLinks", adds), ("updateLinks", updates), ("removeLinks", removes)] {
        if !v.is_empty() {
            p.insert(k.into(), Value::Array(v));
        }
    }
    (!p.is_empty()).then_some(p)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn draft(props: Value) -> Draft {
        Draft {
            props: props.as_object().cloned(),
            state: "active".into(),
            ..Draft::default()
        }
    }

    #[test]
    fn merges_the_fields_changed_on_one_side() {
        let base = draft(json!({"title": "a", "size": 1}));
        let ours = draft(json!({"title": "a", "size": 2}));
        let theirs = draft(json!({"title": "b", "size": 1}));
        let (m, conflicts) = merge_drafts(&base, &ours, &theirs);
        assert!(conflicts.is_empty());
        assert_eq!(m.props, json!({"title": "b", "size": 2}).as_object().cloned());
    }

    #[test]
    fn names_a_field_changed_on_both_sides() {
        let base = draft(json!({"title": "a"}));
        let ours = draft(json!({"title": "b"}));
        let mut theirs = draft(json!({"title": "c"}));
        theirs.state = "retired".into();
        let (m, conflicts) = merge_drafts(&base, &ours, &theirs);
        assert_eq!(conflicts, vec!["props.title".to_string()]);
        assert_eq!(m.props, ours.props, "ours is kept");
        assert_eq!(m.state, "retired");
    }

    #[test]
    fn diffs_the_props_and_the_state() {
        let from = draft(json!({"title": "a", "gone": 1}));
        let mut to = draft(json!({"title": "b"}));
        to.state = "retired".into();
        let p = draft_diff(&from, &to).unwrap();
        assert_eq!(
            Value::Object(p),
            json!({"props": {"title": "b"}, "unset": ["gone"], "state": {"from": "active", "to": "retired"}})
        );
        assert!(draft_diff(&from, &from).is_none());
    }
}
