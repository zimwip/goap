//! A domain as `ImportDomain` reads it (Go's `def.ParseDomain`, `Domain.Validate`): the YAML (or JSON) of a definition,
//! decoded as yaml.v3 decodes it into `def.Domain` ([`decode`](crate::decode)), written back as the JSON the registry
//! stores (`json.Marshal(def.Domain)`), and checked.
//!
//! Only the domains whose checks run no script nor guard are judged here: lifecycles, algorithms, validators, forms,
//! documents and structures are the Go service's ([`Definition::served`] answers `None`). A domain with issues is the
//! Go service's too: it keeps it as a draft and words the issues.
use serde::{Deserialize, Deserializer, Serialize, de::DeserializeOwned};
use serde_json::Value;

use crate::{decode, def::Domain};

/// A definition as `def.ParseDomain` reads it: its name and version, the JSON `json.Marshal` writes of it (or the error
/// it fails with: an unsupported value), and the domain when it is one judged here.
#[derive(Debug, Clone)]
pub struct Definition {
    pub name: String,
    pub version: String,
    pub json: Result<String, String>,
    domain: Option<Imported>,
}

impl Definition {
    /// Reads a YAML (or JSON) definition; the error is Go's (`parse domain: …`).
    pub fn read(src: &str) -> Result<Definition, String> {
        let v = decode::parse_domain(src.as_bytes()).map_err(|e| e.0)?;
        let text = |i: usize| match &v {
            decode::Value::Struct(fields) => match &fields[i] {
                decode::Value::Str(s) => s.clone(),
                _ => String::new(),
            },
            _ => String::new(),
        };
        let json = decode::domain_json(&v);
        let domain = json
            .as_ref()
            .ok()
            .and_then(|j| serde_json::from_str::<Imported>(j).ok())
            .and_then(Imported::checked);
        Ok(Definition {
            name: text(0),
            version: text(1),
            json,
            domain,
        })
    }

    /// The domain when it is one judged here: stored as JSON, with nothing only the Go service checks, and no issue.
    pub fn served(&self) -> Option<&Imported> {
        self.domain.as_ref()
    }
}

/// A domain definition, as stored.
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct Imported {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text")]
    pub version: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, deserialize_with = "short_list")]
    pub node_types: Option<Vec<NodeType>>,
    #[serde(default)]
    pub link_types: Option<Vec<LinkType>>,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "list")]
    pub enums: Vec<Enum>,
    #[serde(default, skip_serializing)]
    pub lifecycles: Value,
    #[serde(default, skip_serializing)]
    pub algorithms: Value,
    #[serde(default, skip_serializing)]
    pub algorithm_instances: Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "list")]
    pub item_types: Vec<ItemType>,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "list")]
    pub importers: Vec<Importer>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct NodeType {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "short_vec")]
    pub attributes: Vec<Attribute>,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub extends: String,
    #[serde(default, deserialize_with = "text", skip_serializing)]
    pub lifecycle: String,
    #[serde(default, skip_serializing)]
    pub document: Value,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub change_controlled: Option<bool>,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "texts")]
    pub validators: Vec<String>,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "list")]
    pub search: Vec<SearchProperty>,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub editor: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub icon: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub color: String,
    #[serde(default, skip_serializing)]
    pub structure: Value,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "list")]
    pub requires: Vec<RequiredLink>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub admin_only: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub additional_properties: bool,
    #[serde(default, skip_serializing)]
    pub form: Value,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct SearchProperty {
    #[serde(default, deserialize_with = "text")]
    pub property: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub text: bool,
    #[serde(default, skip_serializing_if = "is_false")]
    pub facet: bool,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct RequiredLink {
    #[serde(default, deserialize_with = "text")]
    pub link: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub count: i64,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct Attribute {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(
        default,
        rename = "type",
        deserialize_with = "text",
        skip_serializing_if = "String::is_empty"
    )]
    pub typ: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub widget: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub diff: String,
    #[serde(
        default,
        rename = "enum",
        deserialize_with = "text",
        skip_serializing_if = "String::is_empty"
    )]
    pub enumeration: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub default: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub section: String,
    #[serde(default, skip_serializing_if = "is_zero")]
    pub order: i64,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub tooltip: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub as_name: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "texts")]
    pub validators: Vec<String>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
pub struct LinkType {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub from: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub to: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "short_vec")]
    pub attributes: Vec<Attribute>,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "texts")]
    pub validators: Vec<String>,
    #[serde(default, skip_serializing_if = "is_false")]
    pub compose: bool,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub inverse_label: String,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Enum {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "short_vec")]
    pub values: Vec<EnumValue>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct EnumValue {
    #[serde(default, deserialize_with = "text")]
    pub value: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub label: String,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct ItemType {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "short_vec")]
    pub attributes: Vec<Attribute>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
#[serde(deny_unknown_fields)]
pub struct Importer {
    #[serde(default, deserialize_with = "text")]
    pub name: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, deserialize_with = "text", skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty", deserialize_with = "short_vec")]
    pub params: Vec<Attribute>,
}

impl Imported {
    /// The domain of a YAML (or JSON) definition, when it is one judged here ([`Definition::served`]).
    pub fn parse(src: &str) -> Option<Imported> {
        Definition::read(src).ok()?.domain
    }

    /// The definition when it is one read here (as [`Imported::parse`] decides): served, and with no issue.
    pub fn checked(self) -> Option<Imported> {
        (self.served() && self.valid()).then_some(self)
    }

    /// The domain as the catalogue reads it.
    pub fn domain(&self) -> Result<Domain, String> {
        Domain::from_json(&serde_json::to_string(self).map_err(|e| e.to_string())?)
    }

    pub fn node_types(&self) -> &[NodeType] {
        self.node_types.as_deref().unwrap_or_default()
    }

    pub fn link_types(&self) -> &[LinkType] {
        self.link_types.as_deref().unwrap_or_default()
    }

    /// Whether nothing of it runs a script, a guard or a form check, nor builds a structure.
    fn served(&self) -> bool {
        let none = |v: &Value| v.is_null() || v.as_array().is_some_and(Vec::is_empty);
        let plain = |as_: &[Attribute]| as_.iter().all(|a| a.validators.is_empty());
        none(&self.lifecycles)
            && none(&self.algorithms)
            && none(&self.algorithm_instances)
            && self.node_types().iter().all(|n| {
                n.lifecycle.is_empty()
                    && n.document.is_null()
                    && n.structure.is_null()
                    && n.form.is_null()
                    && n.validators.is_empty()
                    && plain(&n.attributes)
            })
            && self
                .link_types()
                .iter()
                .all(|l| l.validators.is_empty() && plain(&l.attributes))
            && self.item_types.iter().all(|t| plain(&t.attributes))
            && self.importers.iter().all(|i| plain(&i.params))
    }

    /// Whether `Domain.Validate` finds no issue (of what [`Imported::served`] lets through).
    fn valid(&self) -> bool {
        if !name(&self.name) || self.node_types().is_empty() {
            return false;
        }
        let nodes = self.node_types();
        let mut names = std::collections::HashSet::new();
        for n in nodes {
            if n.name.is_empty()
                || !names.insert(n.name.as_str())
                || (!n.editor.is_empty() && !name(&n.editor))
                || (!n.icon.is_empty() && !icon(&n.icon))
                || (!n.color.is_empty() && !color(&n.color))
            {
                return false;
            }
        }
        // extends a type of the domain (or of another, qualified), with no cycle
        let mut parents = std::collections::HashMap::new();
        for n in nodes {
            if n.extends.is_empty() || foreign(&n.extends) {
                continue;
            }
            if !names.contains(n.extends.as_str()) {
                return false;
            }
            parents.insert(n.name.as_str(), n.extends.as_str());
        }
        for n in nodes {
            let mut seen = std::collections::HashSet::from([n.name.as_str()]);
            let mut t = parents.get(n.name.as_str()).copied();
            while let Some(p) = t {
                if !seen.insert(p) {
                    return false;
                }
                t = parents.get(p).copied();
            }
        }
        if !self.attributes_valid() {
            return false;
        }
        let mut links = std::collections::HashSet::new();
        for l in self.link_types() {
            let known = |r: &str| r.is_empty() || foreign(r) || names.contains(r);
            if l.name.is_empty() || !links.insert(l.name.as_str()) || !known(&l.from) || !known(&l.to) {
                return false;
            }
        }
        for n in nodes {
            let mut seen = std::collections::HashSet::new();
            for r in &n.requires {
                if r.link.is_empty()
                    || !seen.insert(r.link.as_str())
                    || (!foreign(&r.link) && !links.contains(r.link.as_str()))
                    || r.count < 0
                {
                    return false;
                }
            }
        }
        let mut items = std::collections::HashSet::new();
        if !self
            .item_types
            .iter()
            .all(|t| item_type_name(&t.name) && items.insert(t.name.as_str()))
        {
            return false;
        }
        let mut importers = std::collections::HashSet::new();
        self.importers
            .iter()
            .all(|i| name(&i.name) && importers.insert(i.name.as_str()))
    }

    /// `Schema.checkAttributes`: the enums, then the attributes of the node, link and item types and the parameters
    /// of the importers.
    fn attributes_valid(&self) -> bool {
        let mut enums = std::collections::HashSet::new();
        for e in &self.enums {
            if e.name.is_empty() || !enums.insert(e.name.as_str()) || e.values.is_empty() {
                return false;
            }
            let mut seen = std::collections::HashSet::new();
            if !e
                .values
                .iter()
                .all(|v| !v.value.is_empty() && seen.insert(v.value.as_str()))
            {
                return false;
            }
        }
        let check = |as_: &[Attribute], node: bool| {
            let mut names = std::collections::HashSet::new();
            let ok = as_.iter().all(|a| {
                attribute_name(&a.name)
                    && names.insert(a.name.as_str())
                    && (a.typ.is_empty() || TYPES.contains(&a.typ.as_str()))
                    && (a.widget.is_empty() || WIDGETS.contains(&a.widget.as_str()))
                    && (a.diff.is_empty() || DIFFS.contains(&a.diff.as_str()))
                    && if a.typ == "enum" {
                        !a.enumeration.is_empty() && enums.contains(a.enumeration.as_str())
                    } else {
                        a.enumeration.is_empty()
                    }
            });
            ok && (!node || as_.iter().filter(|a| a.as_name).count() <= 1)
        };
        self.node_types().iter().all(|n| check(&n.attributes, true))
            && self.link_types().iter().all(|l| check(&l.attributes, false))
            && self.item_types.iter().all(|t| check(&t.attributes, false))
            && self.importers.iter().all(|i| check(&i.params, false))
    }
}

const TYPES: [&str; 7] = ["string", "number", "boolean", "date", "enum", "json", "code"];
const WIDGETS: [&str; 7] = ["text", "textarea", "dropdown", "checkbox", "date", "directory", "code"];
const DIFFS: [&str; 5] = ["value", "words", "lines", "json", "code"];

/// A qualified reference (`base@Item`): a type of another domain, resolved by the catalogue.
fn foreign(r: &str) -> bool {
    r.contains('@')
}

/// `^[a-z]` followed by what `rest` accepts (the Go regular expressions of lib/def, anchored at both ends).
fn shaped(s: &str, rest: impl Fn(char) -> bool) -> bool {
    let mut cs = s.chars();
    cs.next().is_some_and(|c| c.is_ascii_lowercase()) && cs.all(rest)
}

/// `def.NameRE`: `^[a-z][a-z0-9_-]*$`.
fn name(s: &str) -> bool {
    shaped(s, |c| {
        c.is_ascii_lowercase() || c.is_ascii_digit() || c == '_' || c == '-'
    })
}

/// `def.IconRE`: `^[a-z][A-Za-z0-9]*$`.
fn icon(s: &str) -> bool {
    shaped(s, |c| c.is_ascii_alphanumeric())
}

/// `def.ItemTypeNameRE`: `^[a-z][A-Za-z0-9_]*$`.
fn item_type_name(s: &str) -> bool {
    shaped(s, |c| c.is_ascii_alphanumeric() || c == '_')
}

/// `attributeNameRE`: `^[A-Za-z][A-Za-z0-9_]*$`.
fn attribute_name(s: &str) -> bool {
    let mut cs = s.chars();
    cs.next().is_some_and(|c| c.is_ascii_alphabetic()) && cs.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

/// `def.ColorRE`: `^#[0-9a-fA-F]{6}$`.
fn color(s: &str) -> bool {
    s.len() == 7 && s.starts_with('#') && s[1..].chars().all(|c| c.is_ascii_hexdigit())
}

fn is_false(b: &bool) -> bool {
    !*b
}

fn is_zero(n: &i64) -> bool {
    *n == 0
}

/// A text: a YAML string, or nothing. A number or a boolean (which yaml.v3 would take as its text) is refused here.
fn text<'de, D: Deserializer<'de>>(d: D) -> Result<String, D::Error> {
    Ok(Option::<String>::deserialize(d)?.unwrap_or_default())
}

fn texts<'de, D: Deserializer<'de>>(d: D) -> Result<Vec<String>, D::Error> {
    Ok(Option::<Vec<String>>::deserialize(d)?.unwrap_or_default())
}

fn list<'de, D: Deserializer<'de>, T: Deserialize<'de>>(d: D) -> Result<Vec<T>, D::Error> {
    Ok(Option::<Vec<T>>::deserialize(d)?.unwrap_or_default())
}

/// What a plain name stands for in a list that accepts one (`attributes: [title]`).
trait Short: DeserializeOwned {
    fn named(name: String) -> Self;
}

impl Short for Attribute {
    fn named(name: String) -> Self {
        Attribute {
            name,
            ..Attribute::default()
        }
    }
}

impl Short for NodeType {
    fn named(name: String) -> Self {
        NodeType {
            name,
            ..NodeType::default()
        }
    }
}

impl Short for EnumValue {
    fn named(value: String) -> Self {
        EnumValue {
            value,
            ..EnumValue::default()
        }
    }
}

fn short_vec<'de, D: Deserializer<'de>, T: Short>(d: D) -> Result<Vec<T>, D::Error> {
    Ok(short_list(d)?.unwrap_or_default())
}

/// A list whose elements are objects or plain names; a null element or a non-text scalar is refused here.
fn short_list<'de, D: Deserializer<'de>, T: Short>(d: D) -> Result<Option<Vec<T>>, D::Error> {
    let Some(raw) = Option::<Vec<Value>>::deserialize(d)? else {
        return Ok(None);
    };
    raw.into_iter()
        .map(|v| match v {
            Value::Object(_) => serde_json::from_value(v).map_err(serde::de::Error::custom),
            Value::String(s) => Ok(T::named(s)),
            other => Err(serde::de::Error::custom(format!("a plain name expected, not {other}"))),
        })
        .collect::<Result<Vec<T>, D::Error>>()
        .map(Some)
}

#[cfg(test)]
mod tests {
    use super::*;

    const BENCH: &str = "name: bench\nversion: 1.0.0\ndescription: A domain\nnodeTypes:\n  - name: Item\n    \
        attributes:\n      - {name: title, label: Title, type: string, asName: true}\n      - {name: value, type: number}\n    \
        search: [{property: title, text: true}]\n  - name: T000\n    extends: Item\n    attributes: [a0]\n\
        linkTypes:\n  - {name: relates, from: Item, to: Item, label: relates to}\n";

    #[test]
    fn reads_and_writes_as_go() {
        let d = Imported::parse(BENCH).unwrap();
        assert_eq!(
            serde_json::to_string(&d).unwrap(),
            r#"{"name":"bench","version":"1.0.0","description":"A domain","nodeTypes":[{"name":"Item","attributes":[{"name":"title","label":"Title","type":"string","asName":true},{"name":"value","type":"number"}],"search":[{"property":"title","text":true}]},{"name":"T000","attributes":[{"name":"a0"}],"extends":"Item"}],"linkTypes":[{"name":"relates","from":"Item","to":"Item","label":"relates to"}]}"#
        );
        assert_eq!(d.domain().unwrap().node_types.len(), 2);
    }

    #[test]
    fn reads_as_yaml_v3() {
        // a number for a text, an unknown key within a type reading its short form itself
        let d = Definition::read("name: x\nversion: 1.0\nnodeTypes: [{name: A, colour: red}]\n").unwrap();
        assert_eq!(d.version, "1.0");
        assert_eq!(
            d.json.as_deref().unwrap(),
            r#"{"name":"x","version":"1.0","nodeTypes":[{"name":"A"}],"linkTypes":null}"#
        );
        assert!(d.served().is_some());
        assert!(
            Definition::read("name: x\ncolour: red\n")
                .unwrap_err()
                .contains("field colour not found")
        );
    }

    #[test]
    fn leaves_the_rest_to_go() {
        for src in [
            "name: x\nversion: '1'\nnodeTypes: [{name: A, lifecycle: l}]\nlifecycles: [{name: l}]\n", // a lifecycle
            "name: x\nversion: '1'\nnodeTypes: []\n",                                                 // an issue
            "name: x\nversion: '1'\nnodeTypes: [{name: A, extends: B}]\n", // an unknown parent
            "name: x\nversion: '1'\nnodeTypes: [{name: A, attributes: [{name: s, type: enum}]}]\n",
        ] {
            assert!(Imported::parse(src).is_none(), "{src}");
        }
    }
}
