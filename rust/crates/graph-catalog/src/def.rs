//! The definition of a domain (lib/def): node types, link types, enums, lifecycles, item types, read from the YAML
//! of a built-in domain or the JSON the domain registry stores. What the graph never interprets (algorithms, forms,
//! importers, the criteria of a gate) stays a JSON value.
use serde::{Deserialize, Deserializer, de::DeserializeOwned};
use serde_json::{Map, Value};

/// A domain: the model of a namespace (ADR 0013).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Domain {
    pub name: String,
    #[serde(deserialize_with = "text")]
    pub version: String,
    pub description: String,
    #[serde(deserialize_with = "short_list")]
    pub node_types: Vec<NodeType>,
    pub link_types: Vec<LinkType>,
    pub enums: Vec<Enum>,
    pub lifecycles: Vec<Lifecycle>,
    pub algorithms: Vec<Value>,
    pub algorithm_instances: Vec<Value>,
    pub item_types: Vec<ItemType>,
    pub importers: Vec<Value>,
}

impl Domain {
    /// A domain from its YAML (a built-in domain) or its JSON (a stored version: JSON is YAML).
    pub fn parse(src: &str) -> Result<Domain, String> {
        serde_norway::from_str(src).map_err(|e| format!("parse domain: {e}"))
    }

    /// A domain from the JSON definition the registry stores.
    pub fn from_json(src: &str) -> Result<Domain, String> {
        serde_json::from_str(src).map_err(|e| e.to_string())
    }

    pub fn lifecycle(&self, name: &str) -> Option<&Lifecycle> {
        self.lifecycles.iter().find(|l| l.name == name)
    }

    pub fn enum_values(&self, name: &str) -> Option<&[EnumValue]> {
        if name.is_empty() {
            return None;
        }
        // the last of a name wins, as the Go loops keep overwriting
        self.enums.iter().rev().find(|e| e.name == name).map(|e| e.values.as_slice())
    }

    /// Whether the domain declares or plugs algorithms (def.Schema.HasAlgorithms): its checks run scripts.
    pub fn has_algorithms(&self) -> bool {
        !self.algorithms.is_empty()
            || !self.algorithm_instances.is_empty()
            || self
                .node_types
                .iter()
                .any(|n| !n.validators.is_empty() || n.attributes.iter().any(|a| !a.validators.is_empty()))
            || self
                .link_types
                .iter()
                .any(|l| !l.validators.is_empty() || l.attributes.iter().any(|a| !a.validators.is_empty()))
            || self
                .lifecycles
                .iter()
                .any(|l| l.transitions.iter().any(|t| !t.guards.is_empty() || !t.actions.is_empty()))
    }
}

/// A node type of a domain.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NodeType {
    pub name: String,
    pub description: String,
    #[serde(deserialize_with = "short_list")]
    pub attributes: Vec<Attribute>,
    pub extends: String,
    pub lifecycle: String,
    pub document: Option<DocumentSpec>,
    pub change_controlled: Option<bool>,
    pub validators: Vec<String>,
    pub search: Vec<SearchProperty>,
    pub editor: String,
    pub icon: String,
    pub color: String,
    pub structure: Option<StructureTag>,
    pub requires: Vec<RequiredLink>,
    pub admin_only: bool,
    pub additional_properties: bool,
    pub form: Option<Value>,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct DocumentSpec {
    pub contains: Vec<String>,
}

/// The tag of the type building a hierarchy (ADR 0054).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StructureTag {
    pub kind: String,
    pub parent: String,
    pub root: String,
    pub self_parent: bool,
    pub bootstrap: Map<String, Value>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct SearchProperty {
    pub property: String,
    pub text: bool,
    pub facet: bool,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct RequiredLink {
    pub link: String,
    pub count: i64,
}

impl RequiredLink {
    /// The number of links required: count, 1 when unset.
    pub fn exactly(&self) -> i64 {
        if self.count <= 0 { 1 } else { self.count }
    }
}

/// An attribute of a node type, a link type or an item type.
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Attribute {
    pub name: String,
    #[serde(deserialize_with = "text")]
    pub label: String,
    pub description: String,
    #[serde(rename = "type")]
    pub typ: String,
    pub widget: String,
    pub diff: String,
    #[serde(rename = "enum")]
    pub enumeration: String,
    #[serde(deserialize_with = "text")]
    pub default: String,
    pub section: String,
    pub order: i64,
    pub tooltip: String,
    pub as_name: bool,
    pub validators: Vec<String>,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LinkType {
    pub name: String,
    pub description: String,
    pub from: String,
    pub to: String,
    #[serde(deserialize_with = "short_list")]
    pub attributes: Vec<Attribute>,
    pub validators: Vec<String>,
    pub compose: bool,
    pub label: String,
    pub inverse_label: String,
}

impl LinkType {
    /// The label read from the source and the one read from the target, with their fallbacks.
    pub fn labels(&self) -> (String, String) {
        let forward = if self.label.is_empty() { &self.name } else { &self.label };
        let inverse = if self.inverse_label.is_empty() { forward } else { &self.inverse_label };
        (forward.clone(), inverse.clone())
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Enum {
    pub name: String,
    pub description: String,
    #[serde(deserialize_with = "short_list")]
    pub values: Vec<EnumValue>,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize)]
#[serde(default)]
pub struct EnumValue {
    #[serde(deserialize_with = "text")]
    pub value: String,
    pub label: String,
}

/// A state machine of nodes (contracts/domain.Lifecycle).
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Lifecycle {
    pub name: String,
    pub description: String,
    pub initial: String,
    pub states: Vec<LifecycleState>,
    pub transitions: Vec<Transition>,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LifecycleState {
    pub name: String,
    pub description: String,
    pub not_committable: bool,
    #[serde(rename = "final")]
    pub is_final: bool,
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct Transition {
    pub name: String,
    pub description: String,
    pub from: String,
    pub to: String,
    pub permission: String,
    pub guard: String,
    pub vetos: Vec<Value>,
    pub objectives: Vec<Value>,
    pub guards: Vec<String>,
    pub actions: Vec<String>,
}

impl Lifecycle {
    pub fn state(&self, name: &str) -> Option<&LifecycleState> {
        self.states.iter().find(|s| s.name == name)
    }

    /// Whether a node may rest in a state when its change commits (ADR 0078): every state but a not committable one.
    pub fn committable(&self, state: &str) -> bool {
        self.state(state).is_none_or(|s| !s.not_committable)
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default)]
pub struct ItemType {
    pub name: String,
    pub description: String,
    #[serde(deserialize_with = "short_list")]
    pub attributes: Vec<Attribute>,
}

/// What a plain scalar stands for in a list that accepts one (`attributes: [title]`): an element of that name.
trait Short: DeserializeOwned + Default {
    fn named(name: String) -> Self;
}

impl Short for Attribute {
    fn named(name: String) -> Self {
        Attribute { name, ..Attribute::default() }
    }
}

impl Short for NodeType {
    fn named(name: String) -> Self {
        NodeType { name, ..NodeType::default() }
    }
}

impl Short for EnumValue {
    fn named(value: String) -> Self {
        EnumValue { value, ..EnumValue::default() }
    }
}

fn short_list<'de, D, T>(d: D) -> Result<Vec<T>, D::Error>
where
    D: Deserializer<'de>,
    T: Short,
{
    let raw = Option::<Vec<Value>>::deserialize(d)?.unwrap_or_default();
    raw.into_iter()
        .map(|v| match v {
            Value::Object(_) => serde_json::from_value(v).map_err(serde::de::Error::custom),
            Value::Null => Ok(T::default()),
            other => Ok(T::named(scalar(other))),
        })
        .collect()
}

/// A string field read from any scalar, as yaml.v3 decodes a number or a boolean into a string.
fn text<'de, D: Deserializer<'de>>(d: D) -> Result<String, D::Error> {
    Ok(match Value::deserialize(d)? {
        Value::Null => String::new(),
        v => scalar(v),
    })
}

fn scalar(v: Value) -> String {
    match v {
        Value::String(s) => s,
        other => other.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_the_short_forms() {
        let d = Domain::parse(
            "name: x\nversion: 1.0\nnodeTypes: [A, {name: B, attributes: [title, {name: n, type: number, default: 0}]}]\n\
             linkTypes: [{name: l}]\nenums: [{name: e, values: [one, {value: two, label: Two}]}]\n",
        )
        .unwrap();
        assert_eq!(d.version, "1.0");
        assert_eq!(d.node_types[0].name, "A");
        let b = &d.node_types[1];
        assert_eq!((b.attributes[0].name.as_str(), b.attributes[1].default.as_str()), ("title", "0"));
        assert_eq!(d.enum_values("e").unwrap()[0].value, "one");
        assert!(!d.has_algorithms());
    }
}
