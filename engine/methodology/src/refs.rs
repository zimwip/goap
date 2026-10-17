//! References between the elements of a methodology version as links of the graph (ADR 0120, `refs.go`).

use std::collections::{BTreeMap, HashSet};
use std::sync::LazyLock;

use regex::Regex;
use serde_json::{Map, Value, json};

use crate::methodology::Methodology;

// Link types of the references (built-in meta-domain methodology).
/// An input: a condition an activity or a goal needs.
pub const LINK_REQUIRES: &str = "methodology@requires";
/// An output: a condition an activity establishes.
pub const LINK_PRODUCES: &str = "methodology@produces";
/// The actions an agent plans over, the pool of a method.
pub const LINK_USES: &str = "methodology@uses";
/// The goals of an agent.
pub const LINK_PURSUES: &str = "methodology@pursues";
/// The action a step runs, or its alternatives.
pub const LINK_PERFORMED_BY: &str = "methodology@performed_by";
/// The process a step nests.
pub const LINK_NESTS: &str = "methodology@nests";
/// The methods providing the capability a step names.
pub const LINK_CARRIED_OUT_BY: &str = "methodology@carried_out_by";
/// A role: allowed to run, acted as, or RACI.
pub const LINK_ASSIGNS: &str = "methodology@assigns";
/// The scoped condition an aggregate condition is over.
pub const LINK_AGGREGATES: &str = "methodology@aggregates";
/// The action a specialization specializes.
pub const LINK_SPECIALIZES: &str = "methodology@specializes";
/// The main goal of a version (a goal or a process).
pub const LINK_MAIN_GOAL: &str = "methodology@main_goal";
/// A condition an expression or a nested structure of an element reads.
pub const LINK_MENTIONS: &str = "methodology@mentions";

// Attributes of the reference links.
/// A map reference: the value the condition is required or established with.
pub const LINK_ATTR_VALUE: &str = "value";
/// A list reference: its position in the list.
pub const LINK_ATTR_POSITION: &str = "position";
/// What the reference is, when a link type carries several fields of a kind.
pub const LINK_ATTR_AS: &str = "as";

// Kinds of the elements of a definition, as the store keys their nodes ("<version key>/<kind>/<name>").
pub const ELEMENT_CONDITION: &str = "condition";
pub const ELEMENT_ACTION: &str = "action";
pub const ELEMENT_GOAL: &str = "goal";
pub const ELEMENT_AGENT: &str = "agent";
pub const ELEMENT_PROCESS: &str = "process";
pub const ELEMENT_METHOD: &str = "method";
pub const ELEMENT_ROLE: &str = "role";
/// A step of a process, keyed by its path.
pub const ELEMENT_STEP: &str = "step";
/// A step of a method.
pub const ELEMENT_METHOD_STEP: &str = "methodStep";
/// ElementVersion stands for the header of the version (the MethodologyVersion node).
pub const ELEMENT_VERSION: &str = "version";

#[derive(Clone, Copy, PartialEq, Eq)]
enum Shape {
    One,
    List,
    Map,
}

/// refField is a field of an element naming other elements of the version.
struct RefField {
    kinds: &'static [&'static str],
    path: &'static [&'static str],
    shape: Shape,
    link: &'static str,
    r#as: &'static str,
    to: &'static [&'static str],
}

const STEP_KINDS: &[&str] = &[ELEMENT_STEP, ELEMENT_METHOD_STEP];
const RACI_KINDS: &[&str] = &[ELEMENT_METHOD, ELEMENT_STEP, ELEMENT_METHOD_STEP];

const REF_FIELDS: &[RefField] = &[
    RefField {
        kinds: &[ELEMENT_ACTION, ELEMENT_GOAL],
        path: &["pre"],
        shape: Shape::Map,
        link: LINK_REQUIRES,
        r#as: "",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: STEP_KINDS,
        path: &["input"],
        shape: Shape::Map,
        link: LINK_REQUIRES,
        r#as: "",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: &[ELEMENT_ACTION],
        path: &["effects"],
        shape: Shape::Map,
        link: LINK_PRODUCES,
        r#as: "",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: &[ELEMENT_METHOD],
        path: &["done"],
        shape: Shape::Map,
        link: LINK_PRODUCES,
        r#as: "",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: STEP_KINDS,
        path: &["output"],
        shape: Shape::Map,
        link: LINK_PRODUCES,
        r#as: "",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: &[ELEMENT_AGENT, ELEMENT_METHOD],
        path: &["actions"],
        shape: Shape::List,
        link: LINK_USES,
        r#as: "",
        to: &[ELEMENT_ACTION],
    },
    RefField {
        kinds: &[ELEMENT_AGENT],
        path: &["goals"],
        shape: Shape::List,
        link: LINK_PURSUES,
        r#as: "",
        to: &[ELEMENT_GOAL],
    },
    RefField {
        kinds: &[ELEMENT_AGENT, ELEMENT_ACTION],
        path: &["roles"],
        shape: Shape::List,
        link: LINK_ASSIGNS,
        r#as: "run",
        to: &[ELEMENT_ROLE],
    },
    RefField {
        kinds: &[ELEMENT_AGENT],
        path: &["role"],
        shape: Shape::One,
        link: LINK_ASSIGNS,
        r#as: "actsAs",
        to: &[ELEMENT_ROLE],
    },
    RefField {
        kinds: RACI_KINDS,
        path: &["roles", "responsible"],
        shape: Shape::One,
        link: LINK_ASSIGNS,
        r#as: "responsible",
        to: &[ELEMENT_ROLE],
    },
    RefField {
        kinds: RACI_KINDS,
        path: &["roles", "accountable"],
        shape: Shape::One,
        link: LINK_ASSIGNS,
        r#as: "accountable",
        to: &[ELEMENT_ROLE],
    },
    RefField {
        kinds: RACI_KINDS,
        path: &["roles", "consulted"],
        shape: Shape::List,
        link: LINK_ASSIGNS,
        r#as: "consulted",
        to: &[ELEMENT_ROLE],
    },
    RefField {
        kinds: RACI_KINDS,
        path: &["roles", "informed"],
        shape: Shape::List,
        link: LINK_ASSIGNS,
        r#as: "informed",
        to: &[ELEMENT_ROLE],
    },
    RefField {
        kinds: STEP_KINDS,
        path: &["action"],
        shape: Shape::One,
        link: LINK_PERFORMED_BY,
        r#as: "action",
        to: &[ELEMENT_ACTION],
    },
    RefField {
        kinds: STEP_KINDS,
        path: &["actions"],
        shape: Shape::List,
        link: LINK_PERFORMED_BY,
        r#as: "alternative",
        to: &[ELEMENT_ACTION],
    },
    RefField {
        kinds: STEP_KINDS,
        path: &["process"],
        shape: Shape::One,
        link: LINK_NESTS,
        r#as: "",
        to: &[ELEMENT_PROCESS],
    },
    RefField {
        kinds: &[ELEMENT_CONDITION],
        path: &["all"],
        shape: Shape::One,
        link: LINK_AGGREGATES,
        r#as: "all",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: &[ELEMENT_CONDITION],
        path: &["any"],
        shape: Shape::One,
        link: LINK_AGGREGATES,
        r#as: "any",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: &[ELEMENT_CONDITION],
        path: &["none"],
        shape: Shape::One,
        link: LINK_AGGREGATES,
        r#as: "none",
        to: &[ELEMENT_CONDITION],
    },
    RefField {
        kinds: &[ELEMENT_ACTION],
        path: &["specializes"],
        shape: Shape::One,
        link: LINK_SPECIALIZES,
        r#as: "",
        to: &[ELEMENT_ACTION],
    },
    RefField {
        kinds: &[ELEMENT_VERSION],
        path: &["goal"],
        shape: Shape::One,
        link: LINK_MAIN_GOAL,
        r#as: "",
        to: &[ELEMENT_GOAL, ELEMENT_PROCESS],
    },
];

/// RefLink is a reference written as a link: its type, the relative key of its target ("<kind>/<name>") and its
/// attributes.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RefLink {
    pub r#type: String,
    pub to: String,
    pub props: Map<String, Value>,
}

/// RefIndex resolves a name to the relative key of the element of a kind declaring it (the first one when names
/// repeat): kind -> name -> relative key.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct RefIndex(pub BTreeMap<String, BTreeMap<String, String>>);

impl RefIndex {
    /// Add declares an element of a kind under a name; the first one declaring a name keeps it.
    pub fn add(&mut self, kind: &str, name: &str, rel: &str) {
        self.0
            .entry(kind.to_string())
            .or_default()
            .entry(name.to_string())
            .or_insert_with(|| rel.to_string());
    }

    fn resolve(&self, kinds: &[&str], name: &str) -> Option<String> {
        kinds
            .iter()
            .find_map(|k| self.0.get(*k).and_then(|m| m.get(name)).cloned())
    }
}

/// fieldsOf are the reference fields of a kind of element.
fn fields_of(kind: &str) -> Vec<&'static RefField> {
    REF_FIELDS
        .iter()
        .filter(|f| f.kinds.contains(&kind))
        .collect()
}

/// getField reads a field (one level of object at most) of the properties of an element.
fn get_field<'a>(p: &'a Map<String, Value>, path: &[&str]) -> Option<&'a Value> {
    if path.len() == 1 {
        return p.get(path[0]);
    }
    p.get(path[0])?.as_object()?.get(path[1])
}

/// setField writes a field (one level of object at most); None removes it.
fn set_field(p: &mut Map<String, Value>, path: &[&str], v: Option<Value>) {
    if path.len() == 1 {
        match v {
            None => {
                p.remove(path[0]);
            }
            Some(v) => {
                p.insert(path[0].to_string(), v);
            }
        }
        return;
    }
    let mut obj = p
        .get(path[0])
        .and_then(|x| x.as_object())
        .cloned()
        .unwrap_or_default();
    match v {
        None => {
            obj.remove(path[1]);
        }
        Some(v) => {
            obj.insert(path[1].to_string(), v);
        }
    }
    if obj.is_empty() {
        p.remove(path[0]);
    } else {
        p.insert(path[0].to_string(), Value::Object(obj));
    }
}

/// Encoded is what EncodeRefs makes of the properties of an element: the properties left and the links.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Encoded {
    pub props: Map<String, Value>,
    pub links: Vec<RefLink>,
}

/// EncodeRefs cuts the references of an element (kind, props) that resolve to an element of the version out of its
/// properties and returns them as links.
pub fn encode_refs(kind: &str, props: &Map<String, Value>, ix: &RefIndex) -> Encoded {
    let mut out = Encoded {
        props: props.clone(),
        links: Vec::new(),
    };
    for f in fields_of(kind) {
        let Some(v) = get_field(&out.props, f.path).cloned() else {
            continue;
        };
        if v.is_null() {
            continue;
        }
        let attrs = |mut p: Map<String, Value>| {
            if !f.r#as.is_empty() {
                p.insert(LINK_ATTR_AS.into(), Value::String(f.r#as.into()));
            }
            p
        };
        match f.shape {
            Shape::One => {
                let name = v.as_str().unwrap_or("");
                if let Some(rel) = ix.resolve(f.to, name) {
                    out.links.push(RefLink {
                        r#type: f.link.into(),
                        to: rel,
                        props: attrs(Map::new()),
                    });
                    set_field(&mut out.props, f.path, None);
                }
            }
            Shape::List => {
                let list = v.as_array().cloned().unwrap_or_default();
                let mut rest: Vec<Value> = Vec::new();
                for (i, item) in list.into_iter().enumerate() {
                    let name = item.as_str().unwrap_or("");
                    if let Some(rel) = ix.resolve(f.to, name) {
                        let mut p = Map::new();
                        p.insert(LINK_ATTR_POSITION.into(), json!(i as f64));
                        out.links.push(RefLink {
                            r#type: f.link.into(),
                            to: rel,
                            props: attrs(p),
                        });
                    } else {
                        rest.push(item);
                    }
                }
                if rest.is_empty() {
                    set_field(&mut out.props, f.path, None);
                } else {
                    set_field(&mut out.props, f.path, Some(Value::Array(rest)));
                }
            }
            Shape::Map => {
                let m = v.as_object().cloned().unwrap_or_default();
                let mut rest = Map::new();
                let mut names: Vec<&String> = m.keys().collect();
                names.sort();
                for name in names {
                    if let Some(rel) = ix.resolve(f.to, name) {
                        let mut p = Map::new();
                        p.insert(LINK_ATTR_VALUE.into(), m[name].clone());
                        out.links.push(RefLink {
                            r#type: f.link.into(),
                            to: rel,
                            props: attrs(p),
                        });
                    } else {
                        rest.insert(name.clone(), m[name].clone());
                    }
                }
                if rest.is_empty() && !m.is_empty() {
                    set_field(&mut out.props, f.path, None);
                } else {
                    set_field(&mut out.props, f.path, Some(Value::Object(rest)));
                }
            }
        }
    }
    out
}

/// RefTarget is a reference link read back: its type, its attributes and the kind and name of its target.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct RefTarget {
    pub r#type: String,
    pub props: Map<String, Value>,
    pub kind: String,
    pub name: String,
}

/// DecodeRefs puts the references an element's links carry back into its properties.
pub fn decode_refs(
    kind: &str,
    props: &Map<String, Value>,
    links: &[RefTarget],
) -> Map<String, Value> {
    let mut out = props.clone();
    let mut lists: BTreeMap<usize, Vec<(f64, String)>> = BTreeMap::new();
    let fields = fields_of(kind);
    for l in links {
        let r#as = l
            .props
            .get(LINK_ATTR_AS)
            .and_then(|v| v.as_str())
            .unwrap_or("");
        for (i, f) in fields.iter().enumerate() {
            if f.link != l.r#type || f.r#as != r#as || !f.to.contains(&l.kind.as_str()) {
                continue;
            }
            match f.shape {
                Shape::One => set_field(&mut out, f.path, Some(Value::String(l.name.clone()))),
                Shape::List => {
                    let pos = l
                        .props
                        .get(LINK_ATTR_POSITION)
                        .and_then(|v| v.as_f64())
                        .unwrap_or(0.0);
                    lists.entry(i).or_default().push((pos, l.name.clone()));
                }
                Shape::Map => {
                    let mut m = get_field(&out, f.path)
                        .and_then(|v| v.as_object())
                        .cloned()
                        .unwrap_or_default();
                    let val = l
                        .props
                        .get(LINK_ATTR_VALUE)
                        .cloned()
                        .unwrap_or(Value::Bool(true));
                    m.insert(l.name.clone(), val);
                    set_field(&mut out, f.path, Some(Value::Object(m)));
                }
            }
            break;
        }
    }
    for (i, mut items) in lists {
        let f = fields[i];
        items.sort_by(|a, b| a.0.partial_cmp(&b.0).unwrap_or(std::cmp::Ordering::Equal));
        // the names that stayed in the property (unresolved) take the places the linked ones leave free
        let rest: Vec<Value> = get_field(&out, f.path)
            .and_then(|v| v.as_array())
            .cloned()
            .unwrap_or_default();
        let n = items.len() + rest.len();
        let mut names: Vec<Value> = vec![Value::Null; n];
        let mut taken = vec![false; n];
        let mut late = Vec::new();
        for (pos, name) in items {
            let p = pos as i64;
            if p >= 0 && (p as usize) < n && !taken[p as usize] {
                names[p as usize] = Value::String(name);
                taken[p as usize] = true;
            } else {
                late.push(name);
            }
        }
        let mut free = 0;
        let mut place = |v: Value| {
            while taken[free] {
                free += 1;
            }
            names[free] = v;
            taken[free] = true;
        };
        for v in rest {
            place(v);
        }
        for name in late {
            place(Value::String(name));
        }
        set_field(&mut out, f.path, Some(Value::Array(names)));
    }
    out
}

/// conditionRE finds the conditions a CEL expression reads (conditions.x, self.conditions.x).
static CONDITION_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"\bconditions\.([A-Za-z_][A-Za-z0-9_]*)").expect("regexp"));

/// DerivedRefs are the references of an element the definition keeps in its own words, so that they are never read
/// back from the graph (ADR 0120): the conditions its expressions read or a nested structure of it names (mentions),
/// the roles and goals of its triggers (assigns and pursues, as trigger).
pub fn derived_refs(
    kind: &str,
    props: &Map<String, Value>,
    ix: &RefIndex,
    this: &str,
) -> Vec<RefLink> {
    let mut out = Vec::new();
    let mut seen: HashSet<String> = HashSet::new();
    let mut add = |out: &mut Vec<RefLink>, typ: &str, rel: String, r#as: &str| {
        let k = format!("{typ} {rel} {}", r#as);
        if rel == this || seen.contains(&k) {
            return;
        }
        seen.insert(k);
        let mut p = Map::new();
        if !r#as.is_empty() {
            p.insert(LINK_ATTR_AS.into(), Value::String(r#as.into()));
        }
        out.push(RefLink {
            r#type: typ.into(),
            to: rel,
            props: p,
        });
    };
    let mut mentions: Vec<String> = Vec::new();
    fn walk(mentions: &mut Vec<String>, key: &str, v: &Value) {
        match v {
            Value::String(x) => {
                for m in CONDITION_RE.captures_iter(x) {
                    mentions.push(m[1].to_string());
                }
            }
            Value::Array(list) => {
                for it in list {
                    walk(mentions, key, it);
                }
            }
            Value::Object(x) => {
                let mut ks: Vec<&String> = x.keys().collect();
                ks.sort();
                for k in ks {
                    if key == "conditions" || key == "until" {
                        mentions.push(k.clone());
                    }
                    walk(mentions, k, &x[k]);
                }
            }
            _ => {}
        }
    }
    let mut ks: Vec<&String> = props.keys().collect();
    ks.sort();
    for k in ks {
        walk(&mut mentions, k, &props[k]);
    }
    for name in mentions {
        if let Some(rel) = ix.resolve(&[ELEMENT_CONDITION], &name) {
            add(&mut out, LINK_MENTIONS, rel, "");
        }
    }
    if kind == ELEMENT_AGENT
        && let Some(Value::Array(triggers)) = props.get("triggers")
    {
        for t in triggers {
            let Some(tm) = t.as_object() else { continue };
            if let Some(Value::Array(roles)) = tm.get("roles") {
                for r in roles {
                    if let Some(rel) = ix.resolve(&[ELEMENT_ROLE], r.as_str().unwrap_or("")) {
                        add(&mut out, LINK_ASSIGNS, rel, "trigger");
                    }
                }
            }
            if let Some(goal) = tm.get("goal").and_then(|g| g.as_str())
                && !goal.is_empty()
                && let Some(rel) = ix.resolve(&[ELEMENT_GOAL], goal)
            {
                add(&mut out, LINK_PURSUES, rel, "trigger");
            }
        }
    }
    out
}

/// RefKind is what tells a reference field from the others on a link: its link type and its "as".
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct RefKind {
    pub r#type: String,
    pub r#as: String,
}

/// RefKindsOf are the link types (and "as") of the reference fields of an element of a kind that props carries.
pub fn ref_kinds_of(kind: &str, props: &Map<String, Value>) -> Vec<RefKind> {
    fields_of(kind)
        .into_iter()
        .filter(|f| props.contains_key(f.path[0]))
        .map(|f| RefKind {
            r#type: f.link.into(),
            r#as: f.r#as.into(),
        })
        .collect()
}

/// RefIndexOf indexes the declared elements of a methodology (not its steps) by kind and name, each under its
/// relative key "<kind>/<name>".
pub fn ref_index_of(m: &Methodology) -> RefIndex {
    let mut ix = RefIndex::default();
    let mut add = |kind: &str, name: &str| ix.add(kind, name, &format!("{kind}/{name}"));
    for c in &m.conditions {
        add(ELEMENT_CONDITION, &c.name);
    }
    for a in &m.actions {
        add(ELEMENT_ACTION, &a.name);
    }
    for g in &m.goals {
        add(ELEMENT_GOAL, &g.name);
    }
    for a in &m.agents {
        add(ELEMENT_AGENT, &a.name);
    }
    for p in &m.processes {
        add(ELEMENT_PROCESS, &p.name);
    }
    for x in &m.methods {
        add(ELEMENT_METHOD, &x.name);
    }
    for r in &m.roles {
        add(ELEMENT_ROLE, &r.name);
    }
    ix
}

/// ElementOfType is the kind of element a node of the type is ("" for none).
pub fn element_of_type(typ: &str) -> &'static str {
    match typ {
        "methodology@Condition" => ELEMENT_CONDITION,
        "methodology@Action" => ELEMENT_ACTION,
        "methodology@Goal" => ELEMENT_GOAL,
        "methodology@Agent" => ELEMENT_AGENT,
        "methodology@Process" => ELEMENT_PROCESS,
        "methodology@Method" => ELEMENT_METHOD,
        "methodology@Role" => ELEMENT_ROLE,
        "methodology@Step" => ELEMENT_STEP,
        "methodology@MethodStep" => ELEMENT_METHOD_STEP,
        "methodology@MethodologyVersion" => ELEMENT_VERSION,
        _ => "",
    }
}
