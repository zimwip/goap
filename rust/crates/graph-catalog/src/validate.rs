//! The check of a domain's definition (`def.Domain.Validate`, `Schema.check`): every issue with Go's path and words,
//! in Go's order. What only Go words is [`Unsure`]: a guard or a script its compilers refuse, a parameter value its
//! coercion refuses, a form check reading what the JSON no longer says.
use std::collections::{HashMap, HashSet};

use serde::Deserialize;
use serde_json::Value;

use crate::algo::{self, Algorithm, Instance, Set};
use crate::def::{Attribute, Domain, Lifecycle, LinkType, NodeType};
use crate::form::{self, LINK_WIDGET, MARKDOWN_WIDGET, SELF_KEY};
pub use crate::form::{Issue, Unsure};

/// The types an attribute may have (`def.AttributeTypes`), its widgets and the diffs it may name.
const ATTRIBUTE_TYPES: [&str; 7] = ["string", "number", "boolean", "date", "enum", "json", "code"];
const ATTRIBUTE_WIDGETS: [&str; 7] = ["text", "textarea", "dropdown", "checkbox", "date", "directory", "code"];
const ATTRIBUTE_DIFFS: [&str; 5] = ["value", "words", "lines", "json", "code"];

const NAME_RULE: &str = "name must be lowercase letters, digits, '-' or '_' and start with a letter";

/// `^[a-z][a-z0-9_-]*$` (`def.NameRE`, the names of lifecycles and instances too).
fn lower_name(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_lowercase())
        && c.all(|x| x.is_ascii_lowercase() || x.is_ascii_digit() || x == '-' || x == '_')
}

/// `^[a-z][A-Za-z0-9]*$` (`def.IconRE`).
fn icon_name(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_lowercase()) && c.all(|x| x.is_ascii_alphanumeric())
}

/// `^#[0-9a-fA-F]{6}$` (`def.ColorRE`).
fn color(s: &str) -> bool {
    s.len() == 7 && s.starts_with('#') && s[1..].chars().all(|c| c.is_ascii_hexdigit())
}

/// `^[A-Za-z][A-Za-z0-9_]*$` (attribute names).
fn attribute_name(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_alphabetic()) && c.all(|x| x.is_ascii_alphanumeric() || x == '_')
}

/// `^[a-z][A-Za-z0-9_]*$` (`def.ItemTypeNameRE`).
fn item_type_name(s: &str) -> bool {
    let mut c = s.chars();
    c.next().is_some_and(|f| f.is_ascii_lowercase()) && c.all(|x| x.is_ascii_alphanumeric() || x == '_')
}

/// A qualified reference (`base@Item`): a type of another domain.
fn foreign(r: &str) -> bool {
    r.contains('@')
}

fn go_quote(s: &str) -> String {
    crate::decode::go_quote(s)
}

/// A criterion of a gate (`domain.Criterion`).
#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct Criterion {
    name: String,
    expr: String,
}

/// An importer (`def.Importer`): its name and parameters.
#[derive(Debug, Default, Deserialize)]
#[serde(default)]
struct Importer {
    name: String,
    params: Option<Vec<Attribute>>,
}

struct Check<'a> {
    d: &'a Domain,
    issues: Vec<Issue>,
    /// The node types by name, the last of a name winning (Go's maps).
    by_name: HashMap<&'a str, &'a NodeType>,
}

impl Check<'_> {
    fn add(&mut self, path: impl Into<String>, message: impl Into<String>) {
        self.issues.push(Issue {
            path: path.into(),
            message: message.into(),
        });
    }
}

/// Every issue of a domain (`Domain.Validate`). `exact`: the definition was read from JSON (a stored version or a
/// request), not from YAML.
pub fn validate(d: &Domain, exact: bool) -> Result<Vec<Issue>, Unsure> {
    let mut c = Check {
        d,
        issues: Vec::new(),
        by_name: d.node_types.iter().map(|n| (n.name.as_str(), n)).collect(),
    };
    if d.name.is_empty() {
        c.add("name", "name required");
    } else if !lower_name(&d.name) {
        c.add("name", NAME_RULE);
    }
    if d.node_types.is_empty() {
        c.add("nodeTypes", "at least one node type required");
    }
    let node_types = c.node_types();
    c.check_lifecycles(&node_types)?;
    c.check_algorithms()?;
    c.check_attributes();
    c.link_types(&node_types);
    c.check_structures();
    c.check_requires();
    c.check_forms(exact)?;
    c.check_item_types();
    c.check_importers();
    Ok(c.issues)
}

impl<'a> Check<'a> {
    fn node_types(&mut self) -> HashSet<&'a str> {
        let d = self.d;
        let mut names = HashSet::new();
        for (i, n) in d.node_types.iter().enumerate() {
            let path = format!("nodeTypes[{i}]");
            if n.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if names.contains(n.name.as_str()) {
                self.add(format!("{path}.name"), format!("duplicate node type {}", n.name));
            }
            names.insert(n.name.as_str());
            if !n.editor.is_empty() && !lower_name(&n.editor) {
                self.add(
                    format!("{path}.editor"),
                    format!(
                        "invalid editor name {} (lowercase letters, digits, - and _)",
                        go_quote(&n.editor)
                    ),
                );
            }
            if !n.icon.is_empty() && !icon_name(&n.icon) {
                self.add(
                    format!("{path}.icon"),
                    format!(
                        "invalid icon name {} (letters and digits, starting with a lowercase letter)",
                        go_quote(&n.icon)
                    ),
                );
            }
            if !n.color.is_empty() && !color(&n.color) {
                self.add(
                    format!("{path}.color"),
                    format!("invalid color {} (#rrggbb)", go_quote(&n.color)),
                );
            }
        }
        let mut parents: HashMap<&str, &str> = HashMap::new();
        for (i, n) in d.node_types.iter().enumerate() {
            if n.extends.is_empty() || foreign(&n.extends) {
                continue;
            }
            if !names.contains(n.extends.as_str()) {
                self.add(
                    format!("nodeTypes[{i}].extends"),
                    format!("unknown node type {}", n.extends),
                );
                continue;
            }
            parents.insert(&n.name, &n.extends);
        }
        for (i, n) in d.node_types.iter().enumerate() {
            let mut seen = HashSet::from([n.name.as_str()]);
            let mut t = parents.get(n.name.as_str()).copied().unwrap_or_default();
            while !t.is_empty() {
                if !seen.insert(t) {
                    self.add(
                        format!("nodeTypes[{i}].extends"),
                        format!("cyclic subtyping through {t}"),
                    );
                    break;
                }
                t = parents.get(t).copied().unwrap_or_default();
            }
        }
        names
    }

    /// The lifecycle of a type, its own or its nearest ancestor's (`Schema.LifecycleOf`).
    fn lifecycle_of(&self, name: &str) -> Option<&'a Lifecycle> {
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let n = self.by_name.get(name);
            if let Some(r) = n.map(|n| n.lifecycle.as_str()).filter(|r| !r.is_empty()) {
                return self.d.lifecycles.iter().find(|l| l.name == r);
            }
            name = n.map(|n| n.extends.as_str()).unwrap_or_default();
        }
        None
    }

    /// The document of a type, inherited through extends (`Schema.documentOf`).
    fn document_of(&self, name: &str) -> Option<&'a [String]> {
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let n = self.by_name.get(name);
            if let Some(d) = n.and_then(|n| n.document.as_ref()) {
                return Some(&d.contains);
            }
            name = n.map(|n| n.extends.as_str()).unwrap_or_default();
        }
        None
    }

    fn check_lifecycles(&mut self, node_types: &HashSet<&str>) -> Result<(), Unsure> {
        let d = self.d;
        let mut names = HashSet::new();
        for (i, l) in d.lifecycles.iter().enumerate() {
            let path = format!("lifecycles[{i}]");
            if l.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if !lower_name(&l.name) {
                self.add(format!("{path}.name"), NAME_RULE);
            } else if names.contains(l.name.as_str()) {
                self.add(format!("{path}.name"), format!("duplicate lifecycle {}", l.name));
            }
            names.insert(l.name.as_str());
            for msg in lifecycle_issues(l) {
                self.add(path.clone(), msg);
            }
            for (j, t) in l.transitions.iter().enumerate() {
                if !graph_script::celcheck::surely_compiles(&t.guard) {
                    return Err(Unsure("a guard cel-go may refuse"));
                }
                for (list, cs) in [("vetos", &t.vetos), ("objectives", &t.objectives)] {
                    let mut seen = HashSet::new();
                    for (k, c) in cs.iter().enumerate() {
                        let c: Criterion = serde_json::from_value(c.clone()).unwrap_or_default();
                        let cp = format!("{path}.transitions[{j}].{list}[{k}]");
                        if c.name.is_empty() {
                            self.add(format!("{cp}.name"), "a criterion needs a name");
                        } else if seen.contains(&c.name) {
                            self.add(format!("{cp}.name"), format!("duplicate criterion {}", c.name));
                        }
                        seen.insert(c.name.clone());
                        if c.expr.is_empty() {
                            self.add(format!("{cp}.expr"), "a criterion needs an expression");
                        } else if !graph_script::celcheck::surely_compiles(&c.expr) {
                            return Err(Unsure("a criterion cel-go may refuse"));
                        }
                    }
                }
            }
        }
        for (i, n) in d.node_types.iter().enumerate() {
            let path = format!("nodeTypes[{i}]");
            if !n.lifecycle.is_empty() {
                if !names.contains(n.lifecycle.as_str()) {
                    self.add(
                        format!("{path}.lifecycle"),
                        format!("unknown lifecycle {}", n.lifecycle),
                    );
                }
                if n.change_controlled == Some(false) {
                    self.add(
                        format!("{path}.changeControlled"),
                        "a type with a lifecycle must be change controlled",
                    );
                }
            }
            if let Some(doc) = &n.document {
                if doc.contains.is_empty() {
                    self.add(
                        format!("{path}.document.contains"),
                        "a document contains at least one node type",
                    );
                }
                for c in &doc.contains {
                    if !foreign(c) && !node_types.contains(c.as_str()) {
                        self.add(format!("{path}.document.contains"), format!("unknown node type {c}"));
                    }
                }
            }
        }
        for (i, n) in d.node_types.iter().enumerate() {
            let Some(l) = self.lifecycle_of(&n.name) else { continue };
            for t in &l.transitions {
                let Some(children) = &t.children else { continue };
                let path = format!("nodeTypes[{i}].lifecycle");
                let Some(doc) = self.document_of(&n.name) else {
                    self.add(
                        path,
                        format!(
                            "transition {} of lifecycle {} constrains children but {} is not a document",
                            t.name, l.name, n.name
                        ),
                    );
                    continue;
                };
                for st in &children.states {
                    let found = doc
                        .iter()
                        .any(|c| self.lifecycle_of(c).is_some_and(|cl| cl.state(st).is_some()));
                    if !found {
                        self.add(
                            path.clone(),
                            format!(
                                "child state {} of transition {} exists on none of the contained types",
                                go_quote(st),
                                t.name
                            ),
                        );
                    }
                }
            }
        }
        Ok(())
    }

    fn check_algorithms(&mut self) -> Result<(), Unsure> {
        let d = self.d;
        let set = Set::of(&d.algorithms, &d.algorithm_instances);
        if set.algorithms.len() != d.algorithms.len() || set.instances.len() != d.algorithm_instances.len() {
            return Err(Unsure("an algorithm Go reads in its own way"));
        }
        let mut algs: HashMap<&str, &Algorithm> = HashMap::new();
        for (i, a) in set.algorithms.iter().enumerate() {
            let path = format!("algorithms[{i}]");
            for msg in a.issues() {
                if msg.contains(": default: ") {
                    return Err(Unsure("a parameter default Go's coercion refuses"));
                }
                self.add(path.clone(), msg);
            }
            if !a.name.is_empty() {
                if algs.contains_key(a.name.as_str()) {
                    self.add(format!("{path}.name"), format!("duplicate algorithm {}", a.name));
                }
                algs.insert(&a.name, a);
            }
            if !a.code.is_empty() && a.language == graph_script::js::JAVASCRIPT && !script_compiles(&a.code) {
                return Err(Unsure("a script goja may refuse"));
            }
        }
        let mut insts: HashMap<&str, &Instance> = HashMap::new();
        for (i, inst) in set.instances.iter().enumerate() {
            let path = format!("algorithmInstances[{i}]");
            if inst.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if !lower_name(&inst.name) {
                self.add(format!("{path}.name"), NAME_RULE);
            } else {
                if insts.contains_key(inst.name.as_str()) {
                    self.add(
                        format!("{path}.name"),
                        format!("duplicate algorithm instance {}", inst.name),
                    );
                }
                insts.insert(&inst.name, inst);
            }
            let Some(a) = algs.get(inst.algorithm.as_str()) else {
                self.add(
                    format!("{path}.algorithm"),
                    format!("unknown algorithm {}", go_quote(&inst.algorithm)),
                );
                continue;
            };
            let (_, issues) = a.resolve(inst.values.as_ref());
            for msg in issues {
                if !msg.starts_with("unknown param ") && !msg.ends_with(" is required") {
                    return Err(Unsure("a parameter value Go's coercion refuses"));
                }
                self.add(format!("{path}.values"), msg);
            }
        }
        let mut plugs: Vec<(String, &str, &str)> = Vec::new();
        for (i, n) in d.node_types.iter().enumerate() {
            for (j, a) in n.attributes.iter().enumerate() {
                for (k, v) in a.validators.iter().enumerate() {
                    plugs.push((
                        format!("nodeTypes[{i}].attributes[{j}].validators[{k}]"),
                        v,
                        algo::PROPERTY_VALIDATOR,
                    ));
                }
            }
            for (j, v) in n.validators.iter().enumerate() {
                plugs.push((format!("nodeTypes[{i}].validators[{j}]"), v, algo::NODE_VALIDATOR));
            }
        }
        for (i, l) in d.link_types.iter().enumerate() {
            for (j, v) in l.validators.iter().enumerate() {
                plugs.push((format!("linkTypes[{i}].validators[{j}]"), v, algo::LINK_VALIDATOR));
            }
            for (j, a) in l.attributes.iter().enumerate() {
                for (k, v) in a.validators.iter().enumerate() {
                    plugs.push((
                        format!("linkTypes[{i}].attributes[{j}].validators[{k}]"),
                        v,
                        algo::PROPERTY_VALIDATOR,
                    ));
                }
            }
        }
        let plug = |c: &mut Self, path: String, instance: &str, want: &str| {
            let Some(inst) = insts.get(instance) else {
                c.add(path, format!("unknown algorithm instance {instance}"));
                return;
            };
            if let Some(a) = algs.get(inst.algorithm.as_str())
                && a.typ != want
            {
                c.add(
                    path,
                    format!("instance {instance} is of type {}, expected {want}", a.typ),
                );
            }
        };
        for (path, v, want) in plugs {
            plug(self, path, v, want);
        }
        let mut props: HashMap<&str, HashSet<&str>> = HashMap::new();
        for (i, n) in d.node_types.iter().enumerate() {
            for (j, sp) in n.search.iter().enumerate() {
                let known = props
                    .entry(&n.name)
                    .or_insert_with(|| self.properties_of(&n.name))
                    .contains(sp.property.as_str());
                if !known {
                    self.add(
                        format!("nodeTypes[{i}].search[{j}].property"),
                        format!("unknown property {} of {}", go_quote(&sp.property), n.name),
                    );
                } else if !sp.text && !sp.facet {
                    self.add(
                        format!("nodeTypes[{i}].search[{j}]"),
                        format!("property {} is neither text nor facet", go_quote(&sp.property)),
                    );
                }
            }
        }
        for (i, l) in d.lifecycles.iter().enumerate() {
            for (j, t) in l.transitions.iter().enumerate() {
                for (k, g) in t.guards.iter().enumerate() {
                    plug(
                        self,
                        format!("lifecycles[{i}].transitions[{j}].guards[{k}]"),
                        g,
                        algo::TRANSITION_GUARD,
                    );
                }
                for (k, a) in t.actions.iter().enumerate() {
                    plug(
                        self,
                        format!("lifecycles[{i}].transitions[{j}].actions[{k}]"),
                        a,
                        algo::TRANSITION_ACTION,
                    );
                }
            }
        }
        Ok(())
    }

    /// The attributes of a node type, its own and the inherited ones (`Schema.propertiesOf`).
    fn properties_of(&self, name: &str) -> HashSet<&'a str> {
        let mut out = HashSet::new();
        let mut seen = HashSet::new();
        let mut name = name;
        while !name.is_empty() && seen.insert(name) {
            let n = self.by_name.get(name);
            for a in n.map(|n| n.attributes.as_slice()).unwrap_or_default() {
                out.insert(a.name.as_str());
            }
            name = n.map(|n| n.extends.as_str()).unwrap_or_default();
        }
        out
    }

    fn check_attributes(&mut self) {
        let d = self.d;
        let mut enums = HashSet::new();
        for (i, e) in d.enums.iter().enumerate() {
            let path = format!("enums[{i}]");
            if e.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if enums.contains(e.name.as_str()) {
                self.add(format!("{path}.name"), format!("duplicate enum {}", e.name));
            }
            enums.insert(e.name.as_str());
            if e.values.is_empty() {
                self.add(format!("{path}.values"), "an enum has at least one value");
            }
            let mut seen = HashSet::new();
            for (j, v) in e.values.iter().enumerate() {
                if v.value.is_empty() {
                    self.add(format!("{path}.values[{j}].value"), "value required");
                } else if seen.contains(v.value.as_str()) {
                    self.add(
                        format!("{path}.values[{j}].value"),
                        format!("duplicate value {}", v.value),
                    );
                }
                seen.insert(v.value.as_str());
            }
        }
        for (i, n) in d.node_types.iter().enumerate() {
            self.attributes(&format!("nodeTypes[{i}]"), ".attributes", &n.attributes, true, &enums);
        }
        for (i, l) in d.link_types.iter().enumerate() {
            self.attributes(&format!("linkTypes[{i}]"), ".attributes", &l.attributes, false, &enums);
        }
        for (i, t) in d.item_types.iter().enumerate() {
            self.attributes(&format!("itemTypes[{i}]"), ".attributes", &t.attributes, false, &enums);
        }
        for (i, im) in importers(d).iter().enumerate() {
            let params = im.params.as_deref().unwrap_or_default();
            self.attributes(&format!("importers[{i}]"), ".params", params, false, &enums);
        }
    }

    fn attributes(&mut self, path: &str, field: &str, attrs: &[Attribute], node: bool, enums: &HashSet<&str>) {
        let mut names = HashSet::new();
        let mut as_name = 0;
        for (i, a) in attrs.iter().enumerate() {
            let p = format!("{path}{field}[{i}]");
            if a.name.is_empty() {
                self.add(format!("{p}.name"), "name required");
            } else if !attribute_name(&a.name) {
                self.add(
                    format!("{p}.name"),
                    "name must be letters, digits and '_' and start with a letter",
                );
            } else if names.contains(a.name.as_str()) {
                self.add(format!("{p}.name"), format!("duplicate attribute {}", a.name));
            }
            names.insert(a.name.as_str());
            if !a.typ.is_empty() && !ATTRIBUTE_TYPES.contains(&a.typ.as_str()) {
                self.add(
                    format!("{p}.type"),
                    format!("unknown type {} ({})", go_quote(&a.typ), ATTRIBUTE_TYPES.join(", ")),
                );
            }
            if !a.widget.is_empty() && !ATTRIBUTE_WIDGETS.contains(&a.widget.as_str()) {
                self.add(
                    format!("{p}.widget"),
                    format!(
                        "unknown widget {} ({})",
                        go_quote(&a.widget),
                        ATTRIBUTE_WIDGETS.join(", ")
                    ),
                );
            }
            if !a.diff.is_empty() && !ATTRIBUTE_DIFFS.contains(&a.diff.as_str()) {
                self.add(
                    format!("{p}.diff"),
                    format!("unknown diff {} ({})", go_quote(&a.diff), ATTRIBUTE_DIFFS.join(", ")),
                );
            }
            if a.typ == "enum" && a.enumeration.is_empty() {
                self.add(format!("{p}.enum"), "an enum attribute names its enum");
            } else if !a.enumeration.is_empty() && a.typ != "enum" {
                self.add(format!("{p}.enum"), "only an enum attribute names an enum");
            } else if !a.enumeration.is_empty() && !enums.contains(a.enumeration.as_str()) {
                self.add(format!("{p}.enum"), format!("unknown enum {}", a.enumeration));
            }
            if a.as_name {
                as_name += 1;
            }
        }
        if node && as_name > 1 {
            self.add(
                format!("{path}.attributes"),
                "at most one attribute is the name of the node",
            );
        }
    }

    fn link_types(&mut self, node_types: &HashSet<&str>) {
        let d = self.d;
        let mut names = HashSet::new();
        for (i, l) in d.link_types.iter().enumerate() {
            let path = format!("linkTypes[{i}]");
            if l.name.is_empty() {
                self.add(format!("{path}.name"), "name required");
            } else if names.contains(l.name.as_str()) {
                self.add(format!("{path}.name"), format!("duplicate link type {}", l.name));
            }
            names.insert(l.name.as_str());
            for (end, r) in [("from", &l.from), ("to", &l.to)] {
                if !r.is_empty() && !foreign(r) && !node_types.contains(r.as_str()) {
                    self.add(format!("{path}.{end}"), format!("unknown node type {r}"));
                }
            }
        }
    }

    fn check_structures(&mut self) {
        let d = self.d;
        let mut kinds = HashSet::new();
        for (i, n) in d.node_types.iter().enumerate() {
            let Some(t) = &n.structure else { continue };
            let path = format!("nodeTypes[{i}].structure");
            if t.kind.is_empty() {
                self.add(format!("{path}.kind"), "the structure kind is required");
            } else if kinds.contains(t.kind.as_str()) {
                self.add(format!("{path}.kind"), format!("structure {} tagged twice", t.kind));
            }
            kinds.insert(t.kind.as_str());
            if t.root.is_empty() {
                self.add(format!("{path}.root"), "the key of the root node is required");
            }
            if !n.lifecycle.is_empty() {
                self.add(
                    path.clone(),
                    "a structure type has no lifecycle: its root is created by the bootstrap",
                );
            }
            let parent: Option<&LinkType> = d.link_types.iter().rfind(|l| l.name == t.parent);
            match parent {
                _ if t.parent.is_empty() => self.add(format!("{path}.parent"), "the parent link type is required"),
                None => self.add(
                    format!("{path}.parent"),
                    format!("unknown link type {} (a link type of this domain)", t.parent),
                ),
                Some(p) if p.from != n.name || p.to != n.name => self.add(
                    format!("{path}.parent"),
                    format!("link type {} must go from {} to {}", t.parent, n.name, n.name),
                ),
                Some(_) => {}
            }
        }
    }

    fn check_requires(&mut self) {
        let d = self.d;
        for (i, n) in d.node_types.iter().enumerate() {
            let mut seen = HashSet::new();
            for (j, r) in n.requires.iter().enumerate() {
                let path = format!("nodeTypes[{i}].requires[{j}]");
                if r.link.is_empty() {
                    self.add(format!("{path}.link"), "the link type is required");
                } else if seen.contains(r.link.as_str()) {
                    self.add(format!("{path}.link"), format!("link type {} required twice", r.link));
                } else if !foreign(&r.link) && !d.link_types.iter().any(|l| l.name == r.link) {
                    self.add(format!("{path}.link"), format!("unknown link type {}", r.link));
                }
                seen.insert(r.link.as_str());
                if r.count < 0 {
                    self.add(format!("{path}.count"), "the count must be positive");
                }
            }
        }
    }

    fn check_forms(&mut self, exact: bool) -> Result<(), Unsure> {
        let d = self.d;
        for (i, n) in d.node_types.iter().enumerate() {
            let Some(f) = &n.form else { continue };
            let path = format!("nodeTypes[{i}].form");
            let fields = form::fields(f);
            for is in form::check(&fields, exact)? {
                self.add(format!("{path}.{}", is.path), is.message);
            }
            let mut attrs = HashSet::new();
            let mut known = true;
            let mut seen = HashSet::new();
            let mut cur = n;
            loop {
                for a in &cur.attributes {
                    attrs.insert(a.name.as_str());
                }
                seen.insert(cur.name.as_str());
                if cur.extends.is_empty() {
                    break;
                }
                match self.by_name.get(cur.extends.as_str()) {
                    Some(next) if !seen.contains(next.name.as_str()) => cur = next,
                    next => {
                        known = !foreign(&cur.extends) && next.is_some();
                        break;
                    }
                }
            }
            for (j, fd) in fields.iter().enumerate() {
                let at = format!("{path}.fields[{j}]");
                match fd.widget.as_str() {
                    MARKDOWN_WIDGET => {}
                    LINK_WIDGET => {
                        if fd.constraints.get("fromKey").and_then(Value::as_str) != Some(SELF_KEY) {
                            self.add(
                                format!("{at}.constraints.fromKey"),
                                format!("a link field of a creation form starts from the node: fromKey {SELF_KEY}"),
                            );
                        }
                    }
                    w @ (form::NEW_NODE_WIDGET | form::DECISION_WIDGET) => {
                        self.add(
                            format!("{at}.widget"),
                            format!("a creation form sets the node: no {w} field"),
                        );
                    }
                    _ => {
                        if known && !attrs.contains(fd.name.as_str()) && !n.additional_properties {
                            self.add(
                                format!("{at}.name"),
                                format!("{} is not an attribute of {}", fd.name, n.name),
                            );
                        }
                    }
                }
            }
        }
        Ok(())
    }

    fn check_item_types(&mut self) {
        let mut seen = HashSet::new();
        for (i, t) in self.d.item_types.iter().enumerate() {
            let path = format!("itemTypes[{i}].name");
            if t.name.is_empty() {
                self.add(path, "name required");
            } else if !item_type_name(&t.name) {
                self.add(
                    path,
                    "name must be letters, digits and '_' and start with a lowercase letter",
                );
            } else if seen.contains(t.name.as_str()) {
                self.add(path, format!("duplicate item type {}", t.name));
            }
            seen.insert(t.name.as_str());
        }
    }

    fn check_importers(&mut self) {
        let mut seen = HashSet::new();
        for (i, im) in importers(self.d).iter().enumerate() {
            let path = format!("importers[{i}].name");
            if im.name.is_empty() {
                self.add(path, "name required");
            } else if !lower_name(&im.name) {
                self.add(
                    path,
                    format!(
                        "invalid importer name {} (lowercase letters, digits, - and _)",
                        go_quote(&im.name)
                    ),
                );
            } else if seen.contains(&im.name) {
                self.add(path, format!("duplicate importer {}", im.name));
            }
            seen.insert(im.name.clone());
        }
    }
}

fn importers(d: &Domain) -> Vec<Importer> {
    d.importers
        .iter()
        .map(|v| serde_json::from_value(v.clone()).unwrap_or_default())
        .collect()
}

/// What is wrong with a lifecycle (`Lifecycle.Issues`).
fn lifecycle_issues(l: &Lifecycle) -> Vec<String> {
    let mut out = Vec::new();
    let mut names = HashSet::new();
    let mut committable = 0;
    for s in &l.states {
        if s.name.is_empty() {
            out.push("a state has no name".to_string());
            continue;
        }
        if !names.insert(s.name.as_str()) {
            out.push(format!("duplicate state {}", s.name));
            continue;
        }
        if !s.not_committable {
            committable += 1;
        } else if s.is_final {
            out.push(format!(
                "state {} is final and not committable: a node there could never commit",
                s.name
            ));
        }
    }
    if l.states.is_empty() {
        out.push("at least one state required".into());
        return out;
    }
    if !names.contains(l.initial.as_str()) {
        out.push(format!("initial state {} is not a state", go_quote(&l.initial)));
    }
    if committable == 0 {
        out.push("at least one committable state required (every state is flagged notCommittable)".into());
    }
    let mut seen = HashSet::new();
    let mut next: HashMap<&str, Vec<&str>> = HashMap::new();
    for (i, t) in l.transitions.iter().enumerate() {
        if t.name.is_empty() {
            out.push(format!("transition {i} has no name"));
        } else if !names.contains(t.from.as_str()) {
            out.push(format!("transition {}: unknown state {}", t.name, go_quote(&t.from)));
        } else if !names.contains(t.to.as_str()) {
            out.push(format!("transition {}: unknown state {}", t.name, go_quote(&t.to)));
        } else if !seen.insert((t.from.as_str(), t.name.as_str())) {
            out.push(format!("duplicate transition {} from {}", t.name, t.from));
        } else {
            next.entry(&t.from).or_default().push(&t.to);
            if l.state(&t.from).is_some_and(|s| s.is_final) {
                out.push(format!("transition {} leaves the final state {}", t.name, t.from));
            }
        }
        if let Some(c) = &t.children {
            for st in &c.states {
                if st.is_empty() {
                    out.push(format!("transition {}: empty child state", t.name));
                }
            }
            if c.states.is_empty() {
                out.push(format!("transition {}: children needs at least one state", t.name));
            }
        }
    }
    // a state that cannot commit must be able to reach one that can
    for s in &l.states {
        if !s.not_committable || !names.contains(s.name.as_str()) {
            continue;
        }
        let mut reach = HashSet::from([s.name.as_str()]);
        let mut todo = std::collections::VecDeque::from([s.name.as_str()]);
        let mut ok = false;
        while let Some(cur) = todo.pop_front() {
            for n in next.get(cur).map(Vec::as_slice).unwrap_or_default() {
                if !reach.insert(n) {
                    continue;
                }
                if l.state(n).is_none_or(|st| !st.not_committable) {
                    ok = true;
                    break;
                }
                todo.push_back(n);
            }
            if ok {
                break;
            }
        }
        if !ok {
            out.push(format!(
                "state {} is not committable and cannot reach a committable state",
                s.name
            ));
        }
    }
    out
}

/// Whether goja surely compiles the code of an algorithm: QuickJS compiles it, and it holds none of the syntax goja
/// may read otherwise (classes and their private names, generators, asynchronous functions, modules).
fn script_compiles(code: &str) -> bool {
    const RISKY: [&str; 6] = ["#", "class", "yield", "async", "import", "export"];
    !RISKY.iter().any(|w| code.contains(w)) && graph_script::js::check_code(graph_script::js::JAVASCRIPT, code).is_ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    fn issues(yaml: &str) -> Vec<String> {
        let d = Domain::parse(yaml).unwrap();
        validate(&d, true)
            .unwrap()
            .into_iter()
            .map(|i| format!("{}: {}", i.path, i.message))
            .collect()
    }

    #[test]
    fn words_the_issues_as_go() {
        assert_eq!(
            issues(
                "name: Bad\nversion: '1'\nnodeTypes: [{name: A, lifecycle: lc, extends: B, icon: Bad, color: red}]\n"
            ),
            [
                "name: name must be lowercase letters, digits, '-' or '_' and start with a letter",
                "nodeTypes[0].icon: invalid icon name \"Bad\" (letters and digits, starting with a lowercase letter)",
                "nodeTypes[0].color: invalid color \"red\" (#rrggbb)",
                "nodeTypes[0].extends: unknown node type B",
                "nodeTypes[0].lifecycle: unknown lifecycle lc",
            ]
        );
        assert_eq!(
            issues(
                "name: d\nversion: '1'\nnodeTypes: [A]\nlifecycles: [{name: lc, initial: x, states: [{name: a, notCommittable: true}], \
                 transitions: [{name: t, from: a, to: b}]}]\n"
            ),
            [
                "lifecycles[0]: initial state \"x\" is not a state",
                "lifecycles[0]: at least one committable state required (every state is flagged notCommittable)",
                "lifecycles[0]: transition t: unknown state \"b\"",
                "lifecycles[0]: state a is not committable and cannot reach a committable state",
            ]
        );
        assert!(issues(include_str!("../../../../domains/alm.yaml")).is_empty());
    }
}
