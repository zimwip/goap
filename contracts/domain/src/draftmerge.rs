//! Rebasing a committed branch (ADR 0101, 0103: rebaseCommittedTx): a node changed both on the change's branch and on
//! the branch it is rebased onto is merged three-way, the change's version against the target's, from the version at
//! the fork. A field changed on both sides to different values is a conflict, named; the change settles it by editing
//! its draft.

use std::collections::{BTreeMap, BTreeSet, HashMap, HashSet};

use serde_json::{Map, Value};

use crate::{
    ChangeImpactID, Draft, DraftLink, IMPACT_CANCELLED, IMPACT_CHECKED_OUT, IMPACT_CREATED,
    IMPACT_LANDED, IMPACT_TRANSITIONED, IMPACT_UPDATED, IMPACT_WITHDRAWN, ImpactEvent, NodeID,
    draft::patch_of, serde_util::same_value,
};

/// Conflict names (first named by ADR 0082 §1).
pub const CONFLICT_OWNER: &str = "owner";
pub const CONFLICT_STATE: &str = "state";
/// ConflictNode is a conflict on the whole node: it is missing from the fork or from the target, so it cannot be
/// merged field by field.
pub const CONFLICT_NODE: &str = "node";

/// ConflictProp names a conflict on a property.
pub fn conflict_prop(key: &str) -> String {
    format!("props.{key}")
}

/// ConflictLink names a conflict on the outgoing link of a type to a node.
pub fn conflict_link(typ: &str, to: &NodeID) -> String {
    format!("link:{typ}:{to}")
}

/// MergeDrafts merges ours (the change's) with theirs (the target's) against base (the version at the fork), field by
/// field: each property (set or unset), the owner, the state and each outgoing link (identified by its type and target
/// node; its target version and properties are its value). A field changed on one side takes that side's value, on
/// both sides to one value that value; changed on both sides differently, it keeps ours and is named in the conflicts
/// (sorted). The result is ours otherwise (key, type, origins, base, links' ids); a link brought by theirs has no id
/// (the caller gives it one).
pub fn merge_drafts(base: &Draft, ours: &Draft, theirs: &Draft) -> (Draft, Vec<String>) {
    let mut out = ours.clone();
    let mut conflicts = Vec::new();
    let keys: BTreeSet<&String> = base
        .properties
        .keys()
        .chain(ours.properties.keys())
        .chain(theirs.properties.keys())
        .collect();
    for k in keys {
        let (b, o, t) = (
            base.properties.get(k),
            ours.properties.get(k),
            theirs.properties.get(k),
        );
        if same_value(o, b) {
            match t {
                Some(t) => {
                    out.properties.insert(k.clone(), t.clone());
                }
                None => {
                    out.properties.remove(k);
                }
            }
        } else if same_value(t, b) || same_value(o, t) {
        } else {
            conflicts.push(conflict_prop(k));
        }
    }
    let conflict;
    (out.owner, conflict) = merge3(&base.owner, &ours.owner, &theirs.owner);
    if conflict {
        conflicts.push(CONFLICT_OWNER.to_string());
    }
    let conflict;
    (out.state, conflict) = merge3(&base.state, &ours.state, &theirs.state);
    if conflict {
        conflicts.push(CONFLICT_STATE.to_string());
    }
    let (links, lc) = merge_links(&base.links, &ours.links, &theirs.links);
    out.links = links;
    conflicts.extend(lc);
    conflicts.sort();
    (out, conflicts)
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

pub(crate) fn link_key(l: &DraftLink) -> String {
    conflict_link(&l.r#type, &l.to.id)
}

pub(crate) fn links_by_key(ls: &[DraftLink]) -> HashMap<String, DraftLink> {
    let mut out = HashMap::new();
    for l in ls {
        out.entry(link_key(l)).or_insert_with(|| l.clone());
    }
    out
}

fn props_value(m: &Map<String, Value>) -> Value {
    Value::Object(m.clone())
}

fn same_link(a: Option<&DraftLink>, b: Option<&DraftLink>) -> bool {
    match (a, b) {
        (None, None) => true,
        (Some(a), Some(b)) => {
            a.to.version == b.to.version
                && same_value(
                    Some(&props_value(&a.properties)),
                    Some(&props_value(&b.properties)),
                )
        }
        _ => false,
    }
}

/// mergeLinks merges the outgoing links: ours in their order (the ones theirs changed or removed updated), then the
/// ones theirs added, by key.
fn merge_links(
    base: &[DraftLink],
    ours: &[DraftLink],
    theirs: &[DraftLink],
) -> (Vec<DraftLink>, Vec<String>) {
    let (bm, om, tm) = (links_by_key(base), links_by_key(ours), links_by_key(theirs));
    let mut conflicts = Vec::new();
    // the outcome of a key ours did not change: theirs (None: removed)
    let mut take: BTreeMap<String, Option<DraftLink>> = BTreeMap::new();
    let keys: BTreeSet<&String> = bm.keys().chain(om.keys()).chain(tm.keys()).collect();
    for k in keys {
        let (b, o, t) = (bm.get(k), om.get(k), tm.get(k));
        if same_link(o, b) {
            take.insert(k.clone(), t.cloned());
        } else if same_link(t, b) || same_link(o, t) {
        } else {
            conflicts.push(k.clone());
        }
    }
    let mut out = Vec::new();
    let mut seen = HashSet::new();
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
                l.properties = t.properties.clone();
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
                r#type: t.r#type.clone(),
                to: t.to.clone(),
                properties: t.properties.clone(),
                ..Default::default()
            });
        }
    }
    (out, conflicts)
}

/// PendingConflicts are the conflicts a change impact's last rebase named on the main flow (ADR 0082 §2), less the
/// ones an edit of its draft settled since: a property set or unset settles its conflict, an owner edit the owner, a
/// transition the state, a link added, removed or updated the link of its type to its target, any edit the node; an
/// `updated` event with `resolved` settles all. A new draft (a checkout, an install that is not a rebase) or the end
/// of the draft clears them.
pub fn pending_conflicts(events: &[ImpactEvent], impact: &ChangeImpactID) -> Vec<String> {
    let mut pending: Vec<String> = Vec::new();
    let settle = |pending: &mut Vec<String>, name: &str| pending.retain(|c| c != name);
    for e in events {
        if e.impact != *impact || !e.flow.is_empty() {
            continue;
        }
        let op = &e.op;
        if *op == IMPACT_CREATED
            || *op == IMPACT_CHECKED_OUT
            || *op == IMPACT_CANCELLED
            || *op == IMPACT_WITHDRAWN
            || *op == IMPACT_LANDED
        {
            pending.clear();
        } else if *op == IMPACT_TRANSITIONED {
            if e.patch.get("rebased").is_some_and(|r| !r.is_null()) {
                pending = strings_of(e.patch.get("conflicts"));
                continue;
            }
            if e.draft.is_some() {
                pending.clear(); // a draft installed wholesale (an adoption)
                continue;
            }
            if e.patch.get("state").is_some_and(|s| !s.is_null()) {
                settle(&mut pending, CONFLICT_STATE);
            }
            let p = patch_of(&e.patch);
            for k in p.props.keys() {
                settle(&mut pending, &conflict_prop(k));
            }
        } else if *op == IMPACT_UPDATED {
            if e.patch
                .get("resolved")
                .and_then(Value::as_bool)
                .unwrap_or(false)
            {
                pending.clear();
                continue;
            }
            settle(&mut pending, CONFLICT_NODE);
            let p = patch_of(&e.patch);
            for k in p.props.keys() {
                settle(&mut pending, &conflict_prop(k));
            }
            for k in &p.unset {
                settle(&mut pending, &conflict_prop(k));
            }
            if !p.owner_id.is_empty() {
                settle(&mut pending, CONFLICT_OWNER);
            }
            for name in ["addLink", "removeLink", "updateLink"] {
                let l = e.patch.get(name).and_then(Value::as_object);
                let typ = l
                    .and_then(|l| l.get("type"))
                    .and_then(Value::as_str)
                    .unwrap_or("");
                let to = l
                    .and_then(|l| l.get("toId"))
                    .and_then(Value::as_str)
                    .unwrap_or("");
                if !typ.is_empty() && !to.is_empty() {
                    settle(&mut pending, &conflict_link(typ, &NodeID::from(to)));
                }
            }
        }
    }
    pending
}

fn strings_of(v: Option<&Value>) -> Vec<String> {
    match v {
        Some(Value::Array(x)) => x
            .iter()
            .filter_map(|s| s.as_str().map(str::to_string))
            .collect(),
        _ => Vec::new(),
    }
}

/// DraftChanged reports whether a draft modifies the version it was checked out from (base, the stored version seen
/// as a draft): a property set, changed or unset, the owner, the state, or an outgoing link added, removed or with
/// other properties (identified by its type and target node; its target version is not a modification: a link to a
/// node the change also drafts points at that draft). A draft with no base (a creation) is always a modification.
pub fn draft_changed(base: Option<&Draft>, d: &Draft) -> bool {
    let (Some(base), Some(dbase)) = (base, d.base.as_ref()) else {
        return true;
    };
    if let Some(m) = &d.merged_with
        && m != dbase
    {
        return true; // it brings another version in: a merge, whatever its values (ADR 0102)
    }
    if base.properties.len() != d.properties.len() {
        return true;
    }
    for (k, v) in &d.properties {
        if !same_value(Some(v), base.properties.get(k)) {
            return true;
        }
    }
    if (!d.owner.is_empty() && d.owner != base.owner) || d.state != base.state {
        return true;
    }
    if base.links.len() != d.links.len() {
        return true;
    }
    let mut count: HashMap<String, usize> = HashMap::new();
    for l in &base.links {
        *count.entry(link_key(l)).or_default() += 1;
    }
    let props = links_by_key(&base.links);
    for l in &d.links {
        let k = link_key(l);
        let c = count.get(&k).copied().unwrap_or(0);
        let base_props = props
            .get(&k)
            .map(|b| b.properties.clone())
            .unwrap_or_default();
        let same_props = (l.properties.is_empty() && base_props.is_empty())
            || same_value(
                Some(&props_value(&l.properties)),
                Some(&props_value(&base_props)),
            );
        if c == 0 || !same_props {
            return true;
        }
        count.insert(k, c - 1);
    }
    false
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{IMPACT_TRANSITIONED, IMPACT_UPDATED, ImpactOp, NodeRef, Version};
    use serde_json::json;

    fn obj(v: Value) -> Map<String, Value> {
        v.as_object().unwrap().clone()
    }

    fn to(id: &str, v: Version) -> NodeRef {
        NodeRef {
            id: id.into(),
            version: v,
        }
    }

    fn link(id: &str, t: NodeRef) -> DraftLink {
        DraftLink {
            id: id.into(),
            r#type: "uses".into(),
            to: t,
            ..Default::default()
        }
    }

    #[test]
    fn merge_drafts_test() {
        let base = Draft {
            key: "K".into(),
            state: "draft".into(),
            owner: "U1".into(),
            properties: obj(json!({"title": "t", "size": 1.0, "gone": "x", "both": "b"})),
            links: vec![
                link("l1", to("A", 1)),
                link("l2", to("B", 1)),
                link("l3", to("C", 1)),
            ],
            ..Default::default()
        };
        let mut ours = base.clone();
        ours.properties.insert("title".into(), json!("ours")); // ours only
        ours.properties.insert("both".into(), json!("o")); // both, differently: conflict
        ours.state = "review".into(); // both, differently: conflict
        ours.links[1].to = to("B", 2); // both, differently: conflict
        ours.links.push(link("l4", to("D", 1)));
        let mut theirs = base.clone();
        theirs.properties.insert("size".into(), json!(2)); // theirs only (an int: compared as JSON)
        theirs.properties.remove("gone");
        theirs.properties.insert("both".into(), json!("t"));
        theirs.properties.insert("new".into(), json!("n"));
        theirs.state = "done".into();
        theirs.owner = "U2".into();
        theirs.links = vec![
            link("x1", to("A", 3)),
            link("x2", to("B", 3)),
            link("x5", to("E", 1)),
        ]; // C removed, A moved, E added

        let (got, conflicts) = merge_drafts(&base, &ours, &theirs);
        assert_eq!(
            conflicts,
            vec![
                conflict_link("uses", &"B".into()),
                conflict_prop("both"),
                CONFLICT_STATE.to_string()
            ]
        );
        assert_eq!(
            got.properties,
            obj(json!({"title": "ours", "size": 2, "both": "o", "new": "n"}))
        );
        assert!(
            got.state == "review" && got.owner == "U2",
            "state {:?} owner {:?}",
            got.state,
            got.owner
        );
        let want_links = vec![
            link("l1", to("A", 3)),
            link("l2", to("B", 2)),
            link("l4", to("D", 1)),
            DraftLink {
                r#type: "uses".into(),
                to: to("E", 1),
                ..Default::default()
            },
        ];
        assert_eq!(got.links, want_links);
        let (_, c) = merge_drafts(&base, &base, &base);
        assert!(c.is_empty(), "no change, no conflict: {c:?}");
    }

    #[test]
    fn pending_conflicts_test() {
        let ev = |op: ImpactOp, patch: Value| ImpactEvent {
            impact: "i".into(),
            op,
            patch: obj(patch),
            ..Default::default()
        };
        let rebased = ev(
            IMPACT_TRANSITIONED,
            json!({"rebased": {"change": "p", "seq": 3},
                "conflicts": [conflict_prop("a"), conflict_prop("b"), CONFLICT_OWNER, CONFLICT_STATE, conflict_link("uses", &"N".into()), CONFLICT_NODE]}),
        );
        let log = vec![
            rebased.clone(),
            ev(IMPACT_UPDATED, json!({"props": {"a": 1}})),
            ev(IMPACT_UPDATED, json!({"ownerId": "U"})),
            ev(
                IMPACT_TRANSITIONED,
                json!({"state": {"from": "x", "to": "y"}}),
            ),
            ev(
                IMPACT_UPDATED,
                json!({"removeLink": {"id": "l", "type": "uses", "toId": "N"}}),
            ),
            ImpactEvent {
                impact: "other".into(),
                op: IMPACT_UPDATED,
                patch: obj(json!({"resolved": true})),
                ..Default::default()
            },
        ];
        assert_eq!(
            pending_conflicts(&log, &"i".into()),
            vec![conflict_prop("b")]
        );
        let mut resolved = log.clone();
        resolved.push(ev(IMPACT_UPDATED, json!({"resolved": true})));
        let got = pending_conflicts(&resolved, &"i".into());
        assert!(got.is_empty(), "resolved = {got:?}");
        let mut again = log.clone();
        again.push(rebased);
        let got = pending_conflicts(&again, &"i".into());
        assert_eq!(
            got.len(),
            6,
            "a new rebase names its conflicts again: {got:?}"
        );
    }
}
