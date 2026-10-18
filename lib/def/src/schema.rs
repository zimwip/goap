//! The model of a domain (ADR 0013) and its validation.

use std::collections::{HashMap, HashSet};
use std::sync::LazyLock;

use goap_algo::{Algorithm, Instance, Set};
use goap_domain::{Criterion, DocumentSpec, Lifecycle, TYPE_SEP};
use goap_form::{SELF_KEY, WIDGET_DECISION, WIDGET_LINK, WIDGET_MARKDOWN, WIDGET_NEW_NODE};
use regex::Regex;
use serde::{Deserialize, Serialize};

use crate::itemtype::ItemType;
use crate::model::{
    ATTR_ENUM, ATTRIBUTE_TYPES, ATTRIBUTE_WIDGETS, Attribute, Enum, Issues, LIFECYCLE_NAME_RE,
    LinkType, NAME_RE, NodeType,
};
use crate::serde_util::null_default;

/// Schema is the model of a domain (ADR 0013): its node types, link types, lifecycles and algorithms.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Schema {
    #[serde(deserialize_with = "null_default")]
    pub node_types: Vec<NodeType>,
    #[serde(deserialize_with = "null_default")]
    pub link_types: Vec<LinkType>,
    /// Enums are the closed lists of values that enum attributes refer to.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub enums: Vec<Enum>,
    /// Lifecycles are the state machines node types refer to by name.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub lifecycles: Vec<Lifecycle>,
    /// Algorithms are the scripts of the domain (ADR 0018) and Instances their
    /// parameterized uses; node types and lifecycle transitions plug instances.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub algorithms: Vec<Algorithm>,
    #[serde(
        rename = "algorithmInstances",
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub instances: Vec<Instance>,
    /// ItemTypes are the types of change item the domain declares (ADR 0128).
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub item_types: Vec<ItemType>,
}

/// foreign reports a qualified reference ("base@Item"): a type of another domain, which the type catalogue resolves
/// (goap-typecat); a bare name is a type of the domain itself.
pub(crate) fn foreign(r: &str) -> bool {
    r.contains(TYPE_SEP)
}

static ATTRIBUTE_NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[A-Za-z][A-Za-z0-9_]*$").unwrap());

impl Schema {
    /// algorithms returns the algorithm set of the schema.
    pub(crate) fn algorithm_set(&self) -> Set {
        Set {
            algorithms: self.algorithms.clone(),
            instances: self.instances.clone(),
        }
    }

    pub(crate) fn by_name(&self) -> HashMap<&str, &NodeType> {
        self.node_types
            .iter()
            .map(|n| (n.name.as_str(), n))
            .collect()
    }

    /// HasAlgorithms tells whether the schema declares or plugs algorithms.
    pub fn has_algorithms(&self) -> bool {
        if !self.algorithms.is_empty() || !self.instances.is_empty() {
            return true;
        }
        let attrs = |as_: &[Attribute]| as_.iter().any(|a| !a.validators.is_empty());
        self.node_types
            .iter()
            .any(|n| !n.validators.is_empty() || attrs(&n.attributes))
            || self
                .link_types
                .iter()
                .any(|l| !l.validators.is_empty() || attrs(&l.attributes))
            || self.lifecycles.iter().any(|l| {
                l.transitions
                    .iter()
                    .any(|t| !t.guards.is_empty() || !t.actions.is_empty())
            })
    }

    /// check validates the schema and returns the sets of node and link type names. Issue paths start with prefix.
    pub(crate) fn check(
        &self,
        prefix: &str,
        issues: &mut Issues,
    ) -> (HashSet<String>, HashSet<String>) {
        let mut node_types = HashSet::new();
        for (i, n) in self.node_types.iter().enumerate() {
            let path = format!("{prefix}nodeTypes[{i}]");
            if n.name.is_empty() {
                issues.add(format!("{path}.name"), "name required");
            } else if node_types.contains(&n.name) {
                issues.add(
                    format!("{path}.name"),
                    format!("duplicate node type {}", n.name),
                );
            }
            node_types.insert(n.name.clone());
            if !n.editor.is_empty() && !NAME_RE.is_match(&n.editor) {
                issues.add(
                    format!("{path}.editor"),
                    format!(
                        "invalid editor name {:?} (lowercase letters, digits, - and _)",
                        n.editor
                    ),
                );
            }
        }
        let mut parents: HashMap<&str, &str> = HashMap::new();
        for (i, n) in self.node_types.iter().enumerate() {
            if n.extends.is_empty() || foreign(&n.extends) {
                continue;
            }
            if !node_types.contains(&n.extends) {
                issues.add(
                    format!("{prefix}nodeTypes[{i}].extends"),
                    format!("unknown node type {}", n.extends),
                );
                continue;
            }
            parents.insert(&n.name, &n.extends);
        }
        for (i, n) in self.node_types.iter().enumerate() {
            let mut seen: HashSet<&str> = HashSet::from([n.name.as_str()]);
            let mut t = parents.get(n.name.as_str()).copied().unwrap_or("");
            while !t.is_empty() {
                if seen.contains(t) {
                    issues.add(
                        format!("{prefix}nodeTypes[{i}].extends"),
                        format!("cyclic subtyping through {t}"),
                    );
                    break;
                }
                seen.insert(t);
                t = parents.get(t).copied().unwrap_or("");
            }
        }
        self.check_lifecycles(prefix, &node_types, issues);
        self.check_algorithms(prefix, issues);
        self.check_attributes(prefix, issues);
        let mut link_types = HashSet::new();
        for (i, l) in self.link_types.iter().enumerate() {
            let path = format!("{prefix}linkTypes[{i}]");
            if l.name.is_empty() {
                issues.add(format!("{path}.name"), "name required");
            } else if link_types.contains(&l.name) {
                issues.add(
                    format!("{path}.name"),
                    format!("duplicate link type {}", l.name),
                );
            }
            link_types.insert(l.name.clone());
            if !l.from.is_empty() && !foreign(&l.from) && !node_types.contains(&l.from) {
                issues.add(
                    format!("{path}.from"),
                    format!("unknown node type {}", l.from),
                );
            }
            if !l.to.is_empty() && !foreign(&l.to) && !node_types.contains(&l.to) {
                issues.add(format!("{path}.to"), format!("unknown node type {}", l.to));
            }
        }
        self.check_structures(prefix, issues);
        self.check_requires(prefix, issues);
        self.check_forms(prefix, issues);
        self.check_item_types(prefix, issues);
        (node_types, link_types)
    }

    /// checkForms validates the creation forms of the node types (ADR 0115 §7): a form the registry of widgets accepts,
    /// whose fields are attributes of the type (own or of an ancestor of the domain; unchecked past a type of another
    /// domain), links from the node itself (`fromKey: $self`) or explanations.
    fn check_forms(&self, prefix: &str, issues: &mut Issues) {
        let by_name = self.by_name();
        for (i, n) in self.node_types.iter().enumerate() {
            let Some(form) = &n.form else { continue };
            let path = format!("{prefix}nodeTypes[{i}].form");
            for is in goap_form::check_form(form).iter() {
                issues.add(format!("{path}.{}", is.path), is.message.clone());
            }
            let mut attrs: HashSet<&str> = HashSet::new();
            let mut known = true;
            let mut seen: HashSet<&str> = HashSet::new();
            let mut cur = n;
            loop {
                for a in &cur.attributes {
                    attrs.insert(&a.name);
                }
                seen.insert(&cur.name);
                if cur.extends.is_empty() {
                    break;
                }
                match by_name.get(cur.extends.as_str()) {
                    Some(next) if !seen.contains(next.name.as_str()) => cur = next,
                    found => {
                        known = !foreign(&cur.extends) && found.is_some();
                        break;
                    }
                }
            }
            for (j, f) in form.fields.iter().enumerate() {
                let at = format!("{path}.fields[{j}]");
                match f.widget.as_str() {
                    WIDGET_MARKDOWN => {}
                    WIDGET_LINK => {
                        let k = f
                            .constraints
                            .get("fromKey")
                            .and_then(|v| v.as_str())
                            .unwrap_or("");
                        if k != SELF_KEY {
                            issues.add(
                                format!("{at}.constraints.fromKey"),
                                format!(
                                    "a link field of a creation form starts from the node: fromKey {SELF_KEY}"
                                ),
                            );
                        }
                    }
                    WIDGET_NEW_NODE | WIDGET_DECISION => {
                        issues.add(
                            format!("{at}.widget"),
                            format!("a creation form sets the node: no {} field", f.widget),
                        );
                    }
                    _ => {
                        if known && !attrs.contains(f.name.as_str()) && !n.additional_properties {
                            issues.add(
                                format!("{at}.name"),
                                format!("{} is not an attribute of {}", f.name, n.name),
                            );
                        }
                    }
                }
            }
        }
    }

    /// checkStructures validates the structure tags (ADR 0054): a kind, once per schema, a root key, and a parent link
    /// type of the schema itself going from and to the tagged type.
    fn check_structures(&self, prefix: &str, issues: &mut Issues) {
        let mut kinds = HashSet::new();
        for (i, n) in self.node_types.iter().enumerate() {
            let Some(t) = &n.structure else { continue };
            let path = format!("{prefix}nodeTypes[{i}].structure");
            if t.kind.is_empty() {
                issues.add(format!("{path}.kind"), "the structure kind is required");
            } else if kinds.contains(&t.kind) {
                issues.add(
                    format!("{path}.kind"),
                    format!("structure {} tagged twice", t.kind),
                );
            }
            kinds.insert(t.kind.clone());
            if t.root.is_empty() {
                issues.add(
                    format!("{path}.root"),
                    "the key of the root node is required",
                );
            }
            if !n.lifecycle.is_empty() {
                issues.add(
                    path.clone(),
                    "a structure type has no lifecycle: its root is created by the bootstrap",
                );
            }
            let parent = self.link_types.iter().rfind(|l| l.name == t.parent);
            if t.parent.is_empty() {
                issues.add(format!("{path}.parent"), "the parent link type is required");
            } else if let Some(parent) = parent {
                if parent.from != n.name || parent.to != n.name {
                    issues.add(
                        format!("{path}.parent"),
                        format!(
                            "link type {} must go from {} to {}",
                            t.parent, n.name, n.name
                        ),
                    );
                }
            } else {
                issues.add(
                    format!("{path}.parent"),
                    format!(
                        "unknown link type {} (a link type of this domain)",
                        t.parent
                    ),
                );
            }
        }
    }

    /// checkRequires validates the required links of the node types (ADR 0065): a link type of the schema or of
    /// another domain, and a count that is not negative.
    fn check_requires(&self, prefix: &str, issues: &mut Issues) {
        for (i, n) in self.node_types.iter().enumerate() {
            let mut seen = HashSet::new();
            for (j, r) in n.requires.iter().enumerate() {
                let path = format!("{prefix}nodeTypes[{i}].requires[{j}]");
                if r.link.is_empty() {
                    issues.add(format!("{path}.link"), "the link type is required");
                } else if seen.contains(&r.link) {
                    issues.add(
                        format!("{path}.link"),
                        format!("link type {} required twice", r.link),
                    );
                } else if !foreign(&r.link) && !self.link_types.iter().any(|l| l.name == r.link) {
                    issues.add(
                        format!("{path}.link"),
                        format!("unknown link type {}", r.link),
                    );
                }
                seen.insert(r.link.clone());
                if r.count < 0 {
                    issues.add(format!("{path}.count"), "the count must be positive");
                }
            }
        }
    }

    /// Lifecycle returns the named lifecycle of the schema.
    pub fn lifecycle(&self, name: &str) -> Option<&Lifecycle> {
        self.lifecycles.iter().find(|l| l.name == name)
    }

    /// LifecycleOf returns the lifecycle of a node type: the one it names, else the one of its nearest ancestor that
    /// names one. None when the nodes have no state.
    pub fn lifecycle_of(&self, name: &str) -> Option<&Lifecycle> {
        let by_name = self.by_name();
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let t = by_name.get(name);
            if let Some(t) = t
                && !t.lifecycle.is_empty()
            {
                return self.lifecycle(&t.lifecycle);
            }
            name = t.map(|t| t.extends.as_str()).unwrap_or("");
        }
        None
    }

    /// checkLifecycles validates the lifecycles, the references to them and the document declarations.
    fn check_lifecycles(&self, prefix: &str, node_types: &HashSet<String>, issues: &mut Issues) {
        let mut names = HashSet::new();
        for (i, l) in self.lifecycles.iter().enumerate() {
            let path = format!("{prefix}lifecycles[{i}]");
            if l.name.is_empty() {
                issues.add(format!("{path}.name"), "name required");
            } else if !LIFECYCLE_NAME_RE.is_match(&l.name) {
                issues.add(
                    format!("{path}.name"),
                    "name must be lowercase letters, digits, '-' or '_' and start with a letter",
                );
            } else if names.contains(&l.name) {
                issues.add(
                    format!("{path}.name"),
                    format!("duplicate lifecycle {}", l.name),
                );
            }
            names.insert(l.name.clone());
            for msg in l.issues() {
                issues.add(path.clone(), msg);
            }
            for (j, t) in l.transitions.iter().enumerate() {
                if let Err(e) = goap_guard::compile(&t.guard) {
                    issues.add(format!("{path}.transitions[{j}].guard"), e.to_string());
                }
                let lists: [(&str, &[Criterion]); 2] =
                    [("vetos", &t.vetos), ("objectives", &t.objectives)];
                for (list, cs) in lists {
                    let mut seen = HashSet::new();
                    for (k, c) in cs.iter().enumerate() {
                        let cp = format!("{path}.transitions[{j}].{list}[{k}]");
                        if c.name.is_empty() {
                            issues.add(format!("{cp}.name"), "a criterion needs a name");
                        } else if seen.contains(&c.name) {
                            issues.add(
                                format!("{cp}.name"),
                                format!("duplicate criterion {}", c.name),
                            );
                        }
                        seen.insert(c.name.clone());
                        if c.expr.is_empty() {
                            issues.add(format!("{cp}.expr"), "a criterion needs an expression");
                        } else if let Err(e) = goap_guard::compile(&c.expr) {
                            issues.add(format!("{cp}.expr"), e.to_string());
                        }
                    }
                }
            }
        }
        for (i, n) in self.node_types.iter().enumerate() {
            let path = format!("{prefix}nodeTypes[{i}]");
            if !n.lifecycle.is_empty() {
                if !names.contains(&n.lifecycle) {
                    issues.add(
                        format!("{path}.lifecycle"),
                        format!("unknown lifecycle {}", n.lifecycle),
                    );
                }
                if !n.is_change_controlled() {
                    issues.add(
                        format!("{path}.changeControlled"),
                        "a type with a lifecycle must be change controlled",
                    );
                }
            }
            if let Some(d) = &n.document {
                if d.contains.is_empty() {
                    issues.add(
                        format!("{path}.document.contains"),
                        "a document contains at least one node type",
                    );
                }
                for c in &d.contains {
                    if !foreign(c) && !node_types.contains(c) {
                        issues.add(
                            format!("{path}.document.contains"),
                            format!("unknown node type {c}"),
                        );
                    }
                }
            }
        }
        // child states of a document transition must exist on a contained type
        for (i, n) in self.node_types.iter().enumerate() {
            let Some(l) = self.lifecycle_of(&n.name) else {
                continue;
            };
            for t in &l.transitions {
                let Some(children) = &t.children else {
                    continue;
                };
                let path = format!("{prefix}nodeTypes[{i}].lifecycle");
                let Some(doc) = self.document_of(&n.name) else {
                    issues.add(
                        path,
                        format!(
                            "transition {} of lifecycle {} constrains children but {} is not a document",
                            t.name, l.name, n.name
                        ),
                    );
                    continue;
                };
                for st in &children.states {
                    let found = doc.contains.iter().any(|c| {
                        self.lifecycle_of(c)
                            .is_some_and(|cl| cl.state(st).is_some())
                    });
                    if !found {
                        issues.add(
                            path.clone(),
                            format!(
                                "child state {st:?} of transition {} exists on none of the contained types",
                                t.name
                            ),
                        );
                    }
                }
            }
        }
    }

    /// documentOf returns the document spec of a type (inherited through extends).
    pub(crate) fn document_of(&self, name: &str) -> Option<&DocumentSpec> {
        let by_name = self.by_name();
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let t = by_name.get(name);
            if let Some(d) = t.and_then(|t| t.document.as_ref()) {
                return Some(d);
            }
            name = t.map(|t| t.extends.as_str()).unwrap_or("");
        }
        None
    }

    /// checkAttributes validates the enums of the domain and the attributes of its node and link types.
    fn check_attributes(&self, prefix: &str, issues: &mut Issues) {
        let mut enums = HashSet::new();
        for (i, e) in self.enums.iter().enumerate() {
            let path = format!("{prefix}enums[{i}]");
            if e.name.is_empty() {
                issues.add(format!("{path}.name"), "name required");
            } else if enums.contains(&e.name) {
                issues.add(format!("{path}.name"), format!("duplicate enum {}", e.name));
            }
            enums.insert(e.name.clone());
            if e.values.is_empty() {
                issues.add(format!("{path}.values"), "an enum has at least one value");
            }
            let mut seen = HashSet::new();
            for (j, v) in e.values.iter().enumerate() {
                let p = format!("{path}.values[{j}].value");
                if v.value.is_empty() {
                    issues.add(p, "value required");
                } else if seen.contains(&v.value) {
                    issues.add(p, format!("duplicate value {}", v.value));
                }
                seen.insert(v.value.clone());
            }
        }
        let mut check = |path: String, attrs: &[Attribute], node: bool| {
            let mut names = HashSet::new();
            let mut as_name = 0;
            for (i, a) in attrs.iter().enumerate() {
                let p = format!("{path}.attributes[{i}]");
                if a.name.is_empty() {
                    issues.add(format!("{p}.name"), "name required");
                } else if !ATTRIBUTE_NAME_RE.is_match(&a.name) {
                    issues.add(
                        format!("{p}.name"),
                        "name must be letters, digits and '_' and start with a letter",
                    );
                } else if names.contains(&a.name) {
                    issues.add(
                        format!("{p}.name"),
                        format!("duplicate attribute {}", a.name),
                    );
                }
                names.insert(a.name.clone());
                if !a.r#type.is_empty() && !ATTRIBUTE_TYPES.contains(&a.r#type.as_str()) {
                    issues.add(
                        format!("{p}.type"),
                        format!(
                            "unknown type {:?} ({})",
                            a.r#type,
                            ATTRIBUTE_TYPES.join(", ")
                        ),
                    );
                }
                if !a.widget.is_empty() && !ATTRIBUTE_WIDGETS.contains(&a.widget.as_str()) {
                    issues.add(
                        format!("{p}.widget"),
                        format!(
                            "unknown widget {:?} ({})",
                            a.widget,
                            ATTRIBUTE_WIDGETS.join(", ")
                        ),
                    );
                }
                if a.r#type == ATTR_ENUM && a.r#enum.is_empty() {
                    issues.add(format!("{p}.enum"), "an enum attribute names its enum");
                } else if !a.r#enum.is_empty() && a.r#type != ATTR_ENUM {
                    issues.add(format!("{p}.enum"), "only an enum attribute names an enum");
                } else if !a.r#enum.is_empty() && !enums.contains(&a.r#enum) {
                    issues.add(format!("{p}.enum"), format!("unknown enum {}", a.r#enum));
                }
                if a.as_name {
                    as_name += 1;
                }
            }
            if node && as_name > 1 {
                issues.add(
                    format!("{path}.attributes"),
                    "at most one attribute is the name of the node",
                );
            }
        };
        for (i, n) in self.node_types.iter().enumerate() {
            check(format!("{prefix}nodeTypes[{i}]"), &n.attributes, true);
        }
        for (i, l) in self.link_types.iter().enumerate() {
            check(format!("{prefix}linkTypes[{i}]"), &l.attributes, false);
        }
        for (i, t) in self.item_types.iter().enumerate() {
            check(format!("{prefix}itemTypes[{i}]"), &t.attributes, false);
        }
    }
}
