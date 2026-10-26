//! A methodology version as the registry stores it (Go's `registrysvc` `versionJSON`, ADR 0023, 0120): a header node,
//! one node per element keyed `<header>/<kind>/<name>`, the references between them links, the steps of a process
//! or a method nodes of their own reached through `sub_activity`. This reads the definition back from them, as JSON.
use std::collections::HashMap;

use serde_json::{Map, Value};

/// A node of a stored version.
#[derive(Debug, Clone, Copy)]
pub struct Node<'a> {
    pub key: &'a str,
    pub r#type: &'a str,
    pub props: &'a Map<String, Value>,
}

/// A link of a stored version, in the order the store wrote them.
#[derive(Debug, Clone, Copy)]
pub struct Link<'a> {
    pub from: &'a str,
    pub r#type: &'a str,
    pub to: &'a str,
    pub props: Option<&'a Map<String, Value>>,
}

/// The type of the header node of a version.
pub const TYPE_VERSION: &str = "methodology@MethodologyVersion";

const LINK_SUB_ACTIVITY: &str = "methodology@sub_activity";

/// The properties of the header that are the record's, not the definition's.
const META_KEYS: [&str; 5] = ["status", "createdAt", "updatedAt", "publishedAt", "updatedBy"];

/// The element kinds, the collection of the definition each fills and its node type (`defKinds`).
const KINDS: [(&str, &str, &str); 9] = [
    ("condition", "conditions", "methodology@Condition"),
    ("action", "actions", "methodology@Action"),
    ("goal", "goals", "methodology@Goal"),
    ("agent", "agents", "methodology@Agent"),
    ("process", "processes", "methodology@Process"),
    ("method", "methods", "methodology@Method"),
    ("role", "roles", "methodology@Role"),
    ("step", "", "methodology@Step"),
    ("methodStep", "", "methodology@MethodStep"),
];

fn kind_of_type(t: &str) -> &'static str {
    KINDS.iter().find(|k| k.2 == t).map_or("", |k| k.0)
}

#[derive(Clone, Copy, PartialEq)]
enum Shape {
    One,
    List,
    Map,
    Uses,
}

/// A field of an element naming other elements of the version (`refField`).
struct Field {
    kinds: &'static [&'static str],
    path: &'static [&'static str],
    shape: Shape,
    link: &'static str,
    r#as: &'static str,
    to: &'static [&'static str],
}

const STEPS: &[&str] = &["step", "methodStep"];
const ACTIVITIES: &[&str] = &["method", "process", "step", "methodStep"];
const CONDITION: &[&str] = &["condition"];
const ACTION: &[&str] = &["action"];
const ROLE: &[&str] = &["role"];

/// The reference fields, in Go's order (`refFields`).
const FIELDS: &[Field] = &[
    Field {
        kinds: &["action", "goal"],
        path: &["pre"],
        shape: Shape::Map,
        link: "methodology@requires",
        r#as: "",
        to: CONDITION,
    },
    Field {
        kinds: STEPS,
        path: &["input"],
        shape: Shape::Map,
        link: "methodology@requires",
        r#as: "",
        to: CONDITION,
    },
    Field {
        kinds: ACTION,
        path: &["effects"],
        shape: Shape::Map,
        link: "methodology@produces",
        r#as: "",
        to: CONDITION,
    },
    Field {
        kinds: &["method"],
        path: &["done"],
        shape: Shape::Map,
        link: "methodology@produces",
        r#as: "",
        to: CONDITION,
    },
    Field {
        kinds: STEPS,
        path: &["output"],
        shape: Shape::Map,
        link: "methodology@produces",
        r#as: "",
        to: CONDITION,
    },
    Field {
        kinds: &["agent", "method"],
        path: &["actions"],
        shape: Shape::List,
        link: "methodology@uses",
        r#as: "",
        to: ACTION,
    },
    Field {
        kinds: &["agent"],
        path: &["goals"],
        shape: Shape::List,
        link: "methodology@pursues",
        r#as: "",
        to: &["goal"],
    },
    Field {
        kinds: &["agent", "action"],
        path: &["roles"],
        shape: Shape::List,
        link: "methodology@assigns",
        r#as: "run",
        to: ROLE,
    },
    Field {
        kinds: &["agent", "action"],
        path: &["role"],
        shape: Shape::One,
        link: "methodology@assigns",
        r#as: "actsAs",
        to: ROLE,
    },
    Field {
        kinds: ACTIVITIES,
        path: &["roles", "responsible"],
        shape: Shape::One,
        link: "methodology@assigns",
        r#as: "responsible",
        to: ROLE,
    },
    Field {
        kinds: ACTIVITIES,
        path: &["roles", "accountable"],
        shape: Shape::One,
        link: "methodology@assigns",
        r#as: "accountable",
        to: ROLE,
    },
    Field {
        kinds: ACTIVITIES,
        path: &["roles", "consulted"],
        shape: Shape::List,
        link: "methodology@assigns",
        r#as: "consulted",
        to: ROLE,
    },
    Field {
        kinds: ACTIVITIES,
        path: &["roles", "informed"],
        shape: Shape::List,
        link: "methodology@assigns",
        r#as: "informed",
        to: ROLE,
    },
    Field {
        kinds: STEPS,
        path: &["action"],
        shape: Shape::One,
        link: "methodology@performed_by",
        r#as: "action",
        to: ACTION,
    },
    Field {
        kinds: STEPS,
        path: &["actions"],
        shape: Shape::List,
        link: "methodology@performed_by",
        r#as: "alternative",
        to: ACTION,
    },
    Field {
        kinds: STEPS,
        path: &["process"],
        shape: Shape::One,
        link: "methodology@nests",
        r#as: "",
        to: &["process"],
    },
    Field {
        kinds: CONDITION,
        path: &["all"],
        shape: Shape::One,
        link: "methodology@aggregates",
        r#as: "all",
        to: CONDITION,
    },
    Field {
        kinds: CONDITION,
        path: &["any"],
        shape: Shape::One,
        link: "methodology@aggregates",
        r#as: "any",
        to: CONDITION,
    },
    Field {
        kinds: CONDITION,
        path: &["none"],
        shape: Shape::One,
        link: "methodology@aggregates",
        r#as: "none",
        to: CONDITION,
    },
    Field {
        kinds: ACTION,
        path: &["specializes"],
        shape: Shape::One,
        link: "methodology@specializes",
        r#as: "",
        to: ACTION,
    },
    Field {
        kinds: &["action", "step", "methodStep"],
        path: &["controls"],
        shape: Shape::Uses,
        link: "methodology@controlled_by",
        r#as: "",
        to: CONDITION,
    },
    Field {
        kinds: &["version"],
        path: &["goal"],
        shape: Shape::One,
        link: "methodology@main_goal",
        r#as: "",
        to: &["goal", "process"],
    },
];

fn get_field<'a>(p: &'a Map<String, Value>, path: &[&str]) -> Option<&'a Value> {
    match path {
        [k] => p.get(*k),
        [o, k] => p.get(*o).and_then(Value::as_object).and_then(|m| m.get(*k)),
        _ => None,
    }
}

fn set_field(p: &mut Map<String, Value>, path: &[&str], v: Option<Value>) {
    match path {
        [k] => match v {
            None => {
                p.remove(*k);
            }
            Some(v) => {
                p.insert(k.to_string(), v);
            }
        },
        [o, k] => {
            let mut obj = p.get(*o).and_then(Value::as_object).cloned().unwrap_or_default();
            match v {
                None => {
                    obj.remove(*k);
                }
                Some(v) => {
                    obj.insert(k.to_string(), v);
                }
            }
            if obj.is_empty() {
                p.remove(*o);
            } else {
                p.insert(o.to_string(), Value::Object(obj));
            }
        }
        _ => {}
    }
}

/// A reference link read back: its type, its attributes and the kind and name of its target (`RefTarget`).
struct Target<'a> {
    r#type: &'a str,
    props: Option<&'a Map<String, Value>>,
    kind: &'static str,
    name: String,
}

fn position(p: Option<&Map<String, Value>>) -> f64 {
    p.and_then(|p| p.get("position"))
        .and_then(Value::as_f64)
        .unwrap_or_default()
}

/// The references an element's links carry, back in its properties (`DecodeRefs`).
fn decode_refs(kind: &str, props: &Map<String, Value>, links: &[Target]) -> Map<String, Value> {
    let mut out = props.clone();
    let fields: Vec<&Field> = FIELDS.iter().filter(|f| f.kinds.contains(&kind)).collect();
    let mut lists: HashMap<usize, Vec<(f64, Value)>> = HashMap::new();
    for l in links {
        let r#as = l
            .props
            .and_then(|p| p.get("as"))
            .and_then(Value::as_str)
            .unwrap_or_default();
        let Some((i, f)) = fields
            .iter()
            .enumerate()
            .find(|(_, f)| f.link == l.r#type && f.r#as == r#as && f.to.contains(&l.kind))
        else {
            continue;
        };
        match f.shape {
            Shape::One => set_field(&mut out, f.path, Some(l.name.clone().into())),
            Shape::List => lists
                .entry(i)
                .or_default()
                .push((position(l.props), l.name.clone().into())),
            Shape::Uses => {
                let mut obj = Map::new();
                obj.insert("condition".into(), l.name.clone().into());
                for (k, x) in l.props.into_iter().flatten() {
                    if k != "position" && k != "as" {
                        obj.insert(k.clone(), x.clone());
                    }
                }
                lists
                    .entry(i)
                    .or_default()
                    .push((position(l.props), Value::Object(obj)));
            }
            Shape::Map => {
                let mut m = get_field(&out, f.path)
                    .and_then(Value::as_object)
                    .cloned()
                    .unwrap_or_default();
                let val = l
                    .props
                    .and_then(|p| p.get("value"))
                    .cloned()
                    .unwrap_or(Value::Bool(true));
                m.insert(l.name.clone(), val);
                set_field(&mut out, f.path, Some(Value::Object(m)));
            }
        }
    }
    for (i, mut items) in lists {
        let f = fields[i];
        items.sort_by(|a, b| a.0.partial_cmp(&b.0).unwrap_or(std::cmp::Ordering::Equal));
        // the names that stayed in the property (unresolved) take the places the linked ones leave free
        let rest: Vec<Value> = get_field(&out, f.path)
            .and_then(Value::as_array)
            .cloned()
            .unwrap_or_default();
        let n = items.len() + rest.len();
        let mut names: Vec<Option<Value>> = vec![None; n];
        let mut late = Vec::new();
        for (pos, name) in items {
            let p = pos as i64;
            if p >= 0 && (p as usize) < n && names[p as usize].is_none() {
                names[p as usize] = Some(name);
            } else {
                late.push(name);
            }
        }
        let mut free = 0;
        for v in rest.into_iter().chain(late) {
            while names[free].is_some() {
                free += 1;
            }
            names[free] = Some(v);
        }
        set_field(
            &mut out,
            f.path,
            Some(Value::Array(names.into_iter().map(Option::unwrap_or_default).collect())),
        );
    }
    out
}

/// The JSON of a step in its process or method, from the properties of its node (`stepJSON`).
fn step_json(mut p: Map<String, Value>) -> Map<String, Value> {
    if let Some(v) = p.remove("input") {
        p.insert("pre".into(), v);
    }
    if let Some(v) = p.remove("output") {
        p.insert("done".into(), v);
    }
    p.remove("position");
    p.remove("removed");
    p
}

fn removed(n: &Node) -> bool {
    n.props.get("removed").and_then(Value::as_bool) == Some(true)
}

/// The definition of the version whose header is `header`, among the nodes and links of the namespace (nil when the
/// header is not there): the references put back, the steps of each process and method from its sub-activities.
pub fn definition(header: &str, nodes: &[Node], links: &[Link]) -> Option<Map<String, Value>> {
    let by_key: HashMap<&str, &Node> = nodes.iter().map(|n| (n.key, n)).collect();
    let head = by_key.get(header)?;
    let mut out: HashMap<&str, Vec<&Link>> = HashMap::new();
    for l in links {
        if by_key.contains_key(l.to) && by_key.contains_key(l.from) {
            out.entry(l.from).or_default().push(l);
        }
    }
    let refs = |key: &str| -> Vec<Target> {
        out.get(key)
            .into_iter()
            .flatten()
            .map(|l| {
                let to = by_key[l.to];
                Target {
                    r#type: l.r#type,
                    props: l.props,
                    kind: kind_of_type(to.r#type),
                    name: to
                        .props
                        .get("name")
                        .and_then(Value::as_str)
                        .unwrap_or_default()
                        .to_string(),
                }
            })
            .collect()
    };
    fn steps_of<'a>(
        key: &str,
        out: &HashMap<&str, Vec<&Link<'a>>>,
        by_key: &HashMap<&str, &Node<'a>>,
        refs: &dyn Fn(&str) -> Vec<Target<'a>>,
    ) -> Vec<Value> {
        let mut kids: Vec<&Node> = out
            .get(key)
            .into_iter()
            .flatten()
            .filter(|l| l.r#type == LINK_SUB_ACTIVITY)
            .filter_map(|l| by_key.get(l.to).copied())
            .filter(|c| !removed(c))
            .collect();
        kids.sort_by(|a, b| {
            position(Some(a.props))
                .partial_cmp(&position(Some(b.props)))
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        kids.into_iter()
            .map(|c| {
                let mut p = step_json(decode_refs(kind_of_type(c.r#type), c.props, &refs(c.key)));
                let sub = steps_of(c.key, out, by_key, refs);
                if !sub.is_empty() {
                    p.insert("steps".into(), Value::Array(sub));
                }
                Value::Object(p)
            })
            .collect()
    }
    let mut props = head.props.clone();
    for k in META_KEYS {
        props.remove(k);
    }
    let mut raw = decode_refs("version", &props, &refs(header));
    let prefix = format!("{header}/");
    let mut elements: Vec<&Node> = nodes.iter().filter(|n| n.key.starts_with(&prefix)).collect();
    elements.sort_by_key(|n| n.key);
    let mut by_kind: HashMap<&str, Vec<Map<String, Value>>> = HashMap::new();
    for e in elements {
        let kind = kind_of_type(e.r#type);
        if kind.is_empty() || removed(e) || kind == "step" || kind == "methodStep" {
            continue; // a step is read through its process or method
        }
        let mut p = decode_refs(kind, e.props, &refs(e.key));
        if kind == "process" || kind == "method" {
            let steps = steps_of(e.key, &out, &by_key, &refs);
            if !steps.is_empty() {
                p.insert("steps".into(), Value::Array(steps));
            }
        }
        by_kind.entry(kind).or_default().push(p);
    }
    for (kind, field, _) in KINDS {
        let Some(mut list) = by_kind.remove(kind) else { continue };
        list.sort_by(|a, b| {
            position(Some(a))
                .partial_cmp(&position(Some(b)))
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        let list = list
            .into_iter()
            .map(|mut p| {
                p.remove("position");
                Value::Object(p)
            })
            .collect();
        raw.insert(field.into(), Value::Array(list));
    }
    Some(raw)
}
