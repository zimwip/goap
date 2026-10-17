//! Port of `refs.go`. The references between the elements of a version are links (ADR 0120,
//! `methodology::encode_refs`): this is what the store adds to them: the steps' sub-activities and the capabilities
//! of the steps.

use std::collections::BTreeMap;

use goap_methodology::{
    ELEMENT_VERSION, LINK_CARRIED_OUT_BY, RefIndex, RefLink, derived_refs, encode_refs,
};
use serde_json::{Map, Value};

use crate::defs::{DefEl, KIND_METHOD, KIND_METHOD_STEP, KIND_STEP};
use crate::graphstore::LINK_SUB_ACTIVITY;

/// sameRef reports whether a stored link is the reference wanted (type, target and attributes).
pub(crate) fn same_ref(w: &RefLink, typ: &str, to: &str, props: &Map<String, Value>) -> bool {
    w.r#type == typ
        && w.to == to
        && goap_domain::json_equal(
            &Value::Object(w.props.clone()),
            &Value::Object(props.clone()),
        )
}

/// indexElements indexes the elements of a definition by kind and name (the first one when names repeat).
pub(crate) fn index_elements(els: &[DefEl]) -> RefIndex {
    let mut ix = RefIndex::default();
    for e in els {
        if let Some(name) = e.props.get("name").and_then(Value::as_str)
            && !name.is_empty()
        {
            ix.add(&e.kind, name, &format!("{}/{}", e.kind, e.name));
        }
    }
    ix
}

/// capabilityLinks are the carried_out_by links of the steps naming a capability (`method`): to every method
/// providing it (`for`). They are derived from the definition (the capability is a name, not an element) and never
/// read back.
pub(crate) fn capability_links(els: &[DefEl]) -> BTreeMap<String, Vec<RefLink>> {
    let mut providers: BTreeMap<&str, Vec<String>> = BTreeMap::new();
    for e in els {
        if e.kind != KIND_METHOD {
            continue;
        }
        if let Some(c) = e.props.get("for").and_then(Value::as_str)
            && !c.is_empty()
        {
            providers
                .entry(c)
                .or_default()
                .push(format!("{}/{}", e.kind, e.name));
        }
    }
    let mut out: BTreeMap<String, Vec<RefLink>> = BTreeMap::new();
    for e in els {
        if e.kind != KIND_STEP && e.kind != KIND_METHOD_STEP {
            continue;
        }
        let c = e
            .props
            .get("method")
            .and_then(Value::as_str)
            .unwrap_or_default();
        for rel in providers.get(c).into_iter().flatten() {
            out.entry(format!("{}/{}", e.kind, e.name))
                .or_default()
                .push(RefLink {
                    r#type: LINK_CARRIED_OUT_BY.to_string(),
                    to: rel.clone(),
                    props: Map::new(),
                });
        }
    }
    out
}

/// elementGraph is a definition as the graph holds it: the properties of the header and of every element with their
/// references cut out, the links of each element by relative key (references, sub-activities, capabilities) and
/// those of the header.
#[derive(Debug, Clone, Default)]
pub(crate) struct ElementGraph {
    pub header: Map<String, Value>,
    pub header_links: Vec<RefLink>,
    pub els: Vec<DefEl>,
    /// relative key -> outgoing links
    pub links: BTreeMap<String, Vec<RefLink>>,
}

pub(crate) fn build_element_graph(
    header: &Map<String, Value>,
    els: &[DefEl],
    sub_activity: &BTreeMap<String, Vec<String>>,
) -> ElementGraph {
    let ix = index_elements(els);
    let mut g = ElementGraph::default();
    let h = encode_refs(ELEMENT_VERSION, header, &ix);
    let mut header_links = h.links;
    header_links.extend(derived_refs(ELEMENT_VERSION, &h.props, &ix, ""));
    g.header = h.props;
    g.header_links = header_links;
    for (rel, ls) in capability_links(els) {
        g.links.entry(rel).or_default().extend(ls);
    }
    for e in els {
        let rel = format!("{}/{}", e.kind, e.name);
        let enc = encode_refs(&e.kind, &e.props, &ix);
        let mut links = enc.links;
        links.extend(g.links.remove(&rel).unwrap_or_default());
        links.extend(derived_refs(&e.kind, &enc.props, &ix, &rel));
        for child in sub_activity.get(&rel).into_iter().flatten() {
            links.push(RefLink {
                r#type: LINK_SUB_ACTIVITY.to_string(),
                to: child.clone(),
                props: Map::new(),
            });
        }
        g.links.insert(rel, links);
        g.els.push(DefEl {
            kind: e.kind.clone(),
            name: e.name.clone(),
            props: enc.props,
        });
    }
    g
}
