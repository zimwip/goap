//! The node types, link types, attributes and enums of a domain, and the issues of a validation.

use std::collections::BTreeMap;
use std::fmt;
use std::sync::LazyLock;

use goap_domain::{DocumentSpec, RequiredLink};
use goap_form::Form;
use regex::Regex;
use serde::de::{self, Deserializer, MapAccess, Visitor};
use serde::{Deserialize, Serialize, Serializer};
use serde_json::{Map, Value};

use crate::serde_util::{is_false, is_zero_i64, null_default};

/// TypeSet is what a methodology needs of the types in force (`goap_typecat::Catalog`, or [`crate::domain_types`]):
/// the qualified node and link types ("alm@Requirement") and the ancestors of each node type.
pub trait TypeSet: Send + Sync {
    fn has_node_type(&self, r#ref: &str) -> bool;
    fn has_link_type(&self, r#ref: &str) -> bool;
    /// Each node type with ancestors, mapped to them, nearest first.
    fn supertypes(&self) -> BTreeMap<String, Vec<String>>;
}

/// Implements the shorthand of a definition written as a plain scalar (its name) or as a full object, the object being
/// read by the derive of `#[serde(remote = "Self")]` (so that unknown fields stay visible to `serde_ignored`).
macro_rules! shorthand {
    ($t:ident, $field:ident) => {
        impl Serialize for $t {
            fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
                $t::serialize(self, s)
            }
        }

        impl<'de> Deserialize<'de> for $t {
            fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
                struct V;
                impl<'de> Visitor<'de> for V {
                    type Value = $t;
                    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
                        f.write_str(concat!("a name or a ", stringify!($t)))
                    }
                    fn visit_str<E: de::Error>(self, v: &str) -> Result<$t, E> {
                        Ok($t {
                            $field: v.to_string(),
                            ..Default::default()
                        })
                    }
                    fn visit_i64<E: de::Error>(self, v: i64) -> Result<$t, E> {
                        self.visit_str(&v.to_string())
                    }
                    fn visit_u64<E: de::Error>(self, v: u64) -> Result<$t, E> {
                        self.visit_str(&v.to_string())
                    }
                    fn visit_f64<E: de::Error>(self, v: f64) -> Result<$t, E> {
                        self.visit_str(&v.to_string())
                    }
                    fn visit_bool<E: de::Error>(self, v: bool) -> Result<$t, E> {
                        self.visit_str(&v.to_string())
                    }
                    fn visit_map<A: MapAccess<'de>>(self, map: A) -> Result<$t, A::Error> {
                        $t::deserialize(de::value::MapAccessDeserializer::new(map))
                    }
                }
                d.deserialize_any(V)
            }
        }
    };
}

/// NodeType is a domain node type.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(remote = "Self", default, rename_all = "camelCase")]
pub struct NodeType {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    /// Attributes define the properties the nodes of the type carry: their code, label, type, widget,
    /// validators... The user interface renders and edits a node from them. Subtypes inherit them
    /// and may override one by name.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub attributes: Vec<Attribute>,
    /// Extends makes the type a subtype: it inherits the attributes and link
    /// types of its parent, and conditions on the parent apply to it
    /// (x.types contains every supertype, ADR 0009 §6).
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub extends: String,
    /// Lifecycle names the state machine of the nodes of the type (one of the
    /// domain's lifecycles, ADR 0014). Inherited through extends; empty: the
    /// nodes have no state.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub lifecycle: String,
    /// Document makes the type a document embedding nodes of other types
    /// through outgoing "contains" links.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub document: Option<DocumentSpec>,
    /// ChangeControlled: nodes are only modified through a change (default
    /// true). False: direct writes, and no lifecycle.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub change_controlled: Option<bool>,
    /// Validators plug node validator instances (ADR 0018) on the type: rules on the node as a whole
    /// (across attributes). They run in this order when a node of the type is created or modified,
    /// after the validators of its attributes, the supertypes' first.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub validators: Vec<String>,
    /// Search declares which properties the node index keeps (ADR 0026): text
    /// goes into the full-text and embedding document, facet makes the value
    /// filterable and countable. Inherited through extends.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub search: Vec<SearchProperty>,
    /// Editor names the editor the user interface opens the nodes of the type
    /// with (agent, action, methodology, ...); the UI falls back to its default
    /// node editor when it has no editor of that name. Inherited through
    /// extends; empty: the default node editor.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub editor: String,
    /// Structure tags the type as the one building a hierarchy every node version is placed in (ADR 0054):
    /// the organisational units owning node versions and changes, or the projects nodes are created in. The graph
    /// bootstraps its root and checks every write against it. One type per kind across the domains in force.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub structure: Option<StructureTag>,
    /// Requires lists the links a node of the type must carry (ADR 0065): exactly `count` (default 1) outgoing links
    /// of each type, checked when the node is created and after each write. A subtype inherits them, redefining one
    /// by link.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub requires: Vec<RequiredLink>,
    /// AdminOnly marks the nodes of the type as written by platform administrators only (ADR 0068): the ones that
    /// govern access itself (who may do what, the organisation and project structure, what a unit may reach), not
    /// domain data the ordinary change / object / node rules let a project's members write. A subtype inherits it.
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub admin_only: bool,
    /// AdditionalProperties opts the type out of strict attributes: its nodes may carry properties that are no attribute
    /// of it (a free-form type). By default a property that is not an attribute of the type, or of a supertype, is
    /// refused by the graph. A subtype inherits it.
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub additional_properties: bool,
    /// Form is what a person fills in to create a node of the type (ADR 0115 §7): fields named after its attributes,
    /// link fields starting from the node (`fromKey: $self`) and explanations. The user interface shows it in its
    /// creation dialogs; the graph still checks the node as it checks any write. Inherited through extends.
    #[serde(skip_serializing_if = "Option::is_none")]
    pub form: Option<Form>,
}

shorthand!(NodeType, name);

impl NodeType {
    /// A node type of that name, nothing else declared.
    pub fn named(name: impl Into<String>) -> NodeType {
        NodeType {
            name: name.into(),
            ..NodeType::default()
        }
    }

    /// IsChangeControlled tells whether the nodes of the type are modified through changes only.
    pub fn is_change_controlled(&self) -> bool {
        self.change_controlled.unwrap_or(true)
    }
}

/// StructureTag tags a node type as a structure of the graph (ADR 0054, `goap_domain::Structure`).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct StructureTag {
    /// Kind names the axis (`STRUCTURE_ORGANISATION`, `STRUCTURE_PROJECT`), tagged once across the domains.
    #[serde(deserialize_with = "null_default")]
    pub kind: String,
    /// Parent is the link type (of the same domain) from a child to its parent, from and to the type.
    #[serde(deserialize_with = "null_default")]
    pub parent: String,
    /// Root is the key of the root node the bootstrap creates.
    #[serde(deserialize_with = "null_default")]
    pub root: String,
    /// SelfParent: the root links to itself through Parent (a project root) rather than being rootless.
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub self_parent: bool,
    /// Bootstrap are the initial properties of the root node (name, status...).
    #[serde(
        skip_serializing_if = "Map::is_empty",
        deserialize_with = "null_default"
    )]
    pub bootstrap: Map<String, Value>,
}

/// SearchProperty declares how the node index uses a property of a node type.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct SearchProperty {
    #[serde(deserialize_with = "null_default")]
    pub property: String,
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub text: bool,
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub facet: bool,
}

/// Attribute types.
pub const ATTR_STRING: &str = "string";
pub const ATTR_NUMBER: &str = "number";
pub const ATTR_BOOLEAN: &str = "boolean";
pub const ATTR_DATE: &str = "date";
pub const ATTR_ENUM: &str = "enum";
pub const ATTR_JSON: &str = "json";

/// AttributeTypes lists the attribute types.
pub const ATTRIBUTE_TYPES: [&str; 6] = [
    ATTR_STRING,
    ATTR_NUMBER,
    ATTR_BOOLEAN,
    ATTR_DATE,
    ATTR_ENUM,
    ATTR_JSON,
];

/// Attribute widgets: how the user interface lets a value be edited.
pub const WIDGET_TEXT: &str = "text";
pub const WIDGET_TEXTAREA: &str = "textarea";
pub const WIDGET_DROPDOWN: &str = "dropdown";
pub const WIDGET_CHECKBOX: &str = "checkbox";
pub const WIDGET_DATE: &str = "date";

/// AttributeWidgets lists the widgets.
pub const ATTRIBUTE_WIDGETS: [&str; 5] = [
    WIDGET_TEXT,
    WIDGET_TEXTAREA,
    WIDGET_DROPDOWN,
    WIDGET_CHECKBOX,
    WIDGET_DATE,
];

/// Attribute defines a property of a node type or a link type: the code that keys the value, and what the user
/// interface needs to display and edit it. Its validators are the property validator instances (ADR 0018)
/// that check the value when a node is created or modified. A plain name is the shorthand of an attribute that
/// declares nothing else.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(remote = "Self", default, rename_all = "camelCase")]
pub struct Attribute {
    /// Name is the code: the key of the value in the node's properties.
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub label: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    /// Type of the value ([`ATTRIBUTE_TYPES`]); empty: untyped (any value is accepted, the editor shows a text field).
    #[serde(
        rename = "type",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#type: String,
    /// Widget edits the value ([`ATTRIBUTE_WIDGETS`]); empty: the usual one of the type.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub widget: String,
    /// Enum names the enum of the domain whose values an enum attribute takes.
    #[serde(
        rename = "enum",
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub r#enum: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub default: String,
    /// Section groups the attributes in the editor; Order sorts them within the type.
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub section: String,
    #[serde(skip_serializing_if = "is_zero_i64", deserialize_with = "null_default")]
    pub order: i64,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub tooltip: String,
    /// AsName makes the attribute the display name of the node.
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub as_name: bool,
    /// Validators are the property_validator instances plugged on the attribute, in call order.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub validators: Vec<String>,
}

shorthand!(Attribute, name);

impl Attribute {
    /// An attribute of that name, nothing else declared (the shorthand).
    pub fn named(name: impl Into<String>) -> Attribute {
        Attribute {
            name: name.into(),
            ..Attribute::default()
        }
    }

    /// DefaultWidget is the widget used when an attribute names none.
    pub fn default_widget(&self) -> &str {
        if !self.widget.is_empty() {
            return &self.widget;
        }
        match self.r#type.as_str() {
            ATTR_ENUM => WIDGET_DROPDOWN,
            ATTR_BOOLEAN => WIDGET_CHECKBOX,
            ATTR_DATE => WIDGET_DATE,
            _ => WIDGET_TEXT,
        }
    }
}

/// EnumValue is one value of an enum; a plain value is the shorthand of one with no label.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(remote = "Self", default)]
pub struct EnumValue {
    #[serde(deserialize_with = "null_default")]
    pub value: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub label: String,
}

shorthand!(EnumValue, value);

/// Enum is a closed list of values of the domain that enum attributes refer to; the values keep their order.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Enum {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub values: Vec<EnumValue>,
}

/// AttributeNames lists the names of attributes.
pub fn attribute_names(attrs: &[Attribute]) -> Vec<String> {
    attrs.iter().map(|a| a.name.clone()).collect()
}

/// LinkType is a domain link type.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct LinkType {
    #[serde(deserialize_with = "null_default")]
    pub name: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub description: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub from: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub to: String,
    /// Attributes define what a link of this type carries. The user interface edits a link from them; the graph checks
    /// the type and enum of the values on every link edit, when the draft is accepted and at landing (ADR 0076, 0079);
    /// their validators (property_validator instances, the value being the link property) run at those two moments
    /// too. "specializes" declares when/priority this way: the generic Activity-specialization condition
    /// (architecture plan "Activity concept") a link of that type would carry once one is created.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub attributes: Vec<Attribute>,
    /// Validators plug link_validator instances on the link type: rules on a link as a whole, seen with the node it
    /// leaves and the node it reaches, in call order. They run, like the validators of its attributes, when the draft of
    /// the node the link leaves is accepted and when the version lands.
    #[serde(
        skip_serializing_if = "Vec::is_empty",
        deserialize_with = "null_default"
    )]
    pub validators: Vec<String>,
    /// Compose flags a composition link: the target is a part of the source (an aggregate), so a browser or
    /// editor shows the targets as the children of the source. It is the semantics the editors read from the
    /// domain instead of knowing the link by name ("defines", "sub_activity").
    #[serde(skip_serializing_if = "is_false", deserialize_with = "null_default")]
    pub compose: bool,
    /// Label reads a link from its source ("implements"), InverseLabel from its target ("implemented by"): a link
    /// is shown from the node looking at it. Both are optional: Label falls back to the name, InverseLabel to Label
    /// (see [`LinkType::labels`]).
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub label: String,
    #[serde(
        skip_serializing_if = "String::is_empty",
        deserialize_with = "null_default"
    )]
    pub inverse_label: String,
}

impl LinkType {
    /// Labels returns the label of the link read from its source and the one read from its target, with their
    /// fallbacks: the name, then the source label.
    pub fn labels(&self) -> (String, String) {
        let forward = if self.label.is_empty() {
            self.name.clone()
        } else {
            self.label.clone()
        };
        let inverse = if self.inverse_label.is_empty() {
            forward.clone()
        } else {
            self.inverse_label.clone()
        };
        (forward, inverse)
    }
}

/// Issue is a validation problem located by a field path such as
/// "conditions[2].expr" or "actions[0].pre.has_impacts".
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Issue {
    pub path: String,
    pub message: String,
    /// Activity is the flow path of the process, method or step the issue is about ("<process>/<step>/<sub-step>"),
    /// empty when it belongs to no activity: the flow draws the issue on that node.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub activity: String,
    /// Severity is [`SEVERITY_ERROR`] (the default, empty) or [`SEVERITY_WARNING`]: a warning is a remark that never
    /// refuses a validation, a compile or a publish (ADR 0097).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub severity: String,
    /// Hint says how to fix the issue, when the check knows (the closest name, the names allowed, what to change).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub hint: String,
}

/// Severities of an Issue.
pub const SEVERITY_ERROR: &str = "error";
pub const SEVERITY_WARNING: &str = "warning";

impl Issue {
    /// An error at a path.
    pub fn new(path: impl Into<String>, message: impl Into<String>) -> Issue {
        Issue {
            path: path.into(),
            message: message.into(),
            ..Issue::default()
        }
    }

    /// IsWarning reports whether the issue is a non-blocking remark; any other severity is an error.
    pub fn is_warning(&self) -> bool {
        self.severity == SEVERITY_WARNING
    }
}

impl fmt::Display for Issue {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.path.is_empty() {
            f.write_str(&self.message)
        } else {
            write!(f, "{}: {}", self.path, self.message)
        }
    }
}

/// Issues is a list of validation problems; it is an error (the issues joined by "; ").
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(transparent)]
pub struct Issues(pub Vec<Issue>);

impl Issues {
    /// Errors returns the blocking issues.
    pub fn errors(&self) -> Issues {
        self.of(false)
    }

    /// Warnings returns the non-blocking remarks.
    pub fn warnings(&self) -> Issues {
        self.of(true)
    }

    /// HasErrors reports whether any issue blocks: only errors refuse a validation, a compile or a publish.
    pub fn has_errors(&self) -> bool {
        self.0.iter().any(|x| !x.is_warning())
    }

    fn of(&self, warning: bool) -> Issues {
        Issues(
            self.0
                .iter()
                .filter(|x| x.is_warning() == warning)
                .cloned()
                .collect(),
        )
    }

    /// Appends an error at a path.
    pub fn add(&mut self, path: impl Into<String>, message: impl Into<String>) {
        self.0.push(Issue::new(path, message));
    }
}

impl fmt::Display for Issues {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        for (i, x) in self.0.iter().enumerate() {
            if i > 0 {
                f.write_str("; ")?;
            }
            write!(f, "{x}")?;
        }
        Ok(())
    }
}

impl std::error::Error for Issues {}

impl std::ops::Deref for Issues {
    type Target = Vec<Issue>;
    fn deref(&self) -> &Vec<Issue> {
        &self.0
    }
}

impl std::ops::DerefMut for Issues {
    fn deref_mut(&mut self) -> &mut Vec<Issue> {
        &mut self.0
    }
}

impl From<Vec<Issue>> for Issues {
    fn from(v: Vec<Issue>) -> Issues {
        Issues(v)
    }
}

impl FromIterator<Issue> for Issues {
    fn from_iter<I: IntoIterator<Item = Issue>>(iter: I) -> Issues {
        Issues(iter.into_iter().collect())
    }
}

impl IntoIterator for Issues {
    type Item = Issue;
    type IntoIter = std::vec::IntoIter<Issue>;
    fn into_iter(self) -> Self::IntoIter {
        self.0.into_iter()
    }
}

impl<'a> IntoIterator for &'a Issues {
    type Item = &'a Issue;
    type IntoIter = std::slice::Iter<'a, Issue>;
    fn into_iter(self) -> Self::IntoIter {
        self.0.iter()
    }
}

pub(crate) static LIFECYCLE_NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[a-z][a-z0-9_-]*$").unwrap());

/// NameRE is the form of the name of a domain (and of an editor): lowercase letters, digits, '-' or '_', starting
/// with a letter.
pub static NAME_RE: LazyLock<Regex> = LazyLock::new(|| Regex::new(r"^[a-z][a-z0-9_-]*$").unwrap());
