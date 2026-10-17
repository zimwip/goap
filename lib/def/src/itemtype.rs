//! The types of change item a domain declares (ADR 0128).

use std::collections::HashSet;
use std::sync::LazyLock;

use regex::Regex;
use serde::{Deserialize, Serialize};

use crate::model::{Attribute, Issues};
use crate::schema::Schema;
use crate::serde_util::null_default;

/// ItemType is a type of change item a domain declares (ADR 0128): the items of a change (`goap_domain::ChangeItem`)
/// name it in their `type`. A name is unique across the domains in force (an item belongs to a change, not to a
/// namespace, so its type is never qualified). The attributes describe the keys of the data of its items; the graph
/// checks the type and enum membership of the keys an item carries, the others stay free. What a type means beyond its
/// data (a validation, a permission) is the code of its use case (`register_item_type`, `require_item_permission`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct ItemType {
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
    pub attributes: Vec<Attribute>,
}

/// ItemTypeNameRE is the form of the name of an item type: a lower camel case identifier (decisionPoint).
pub static ITEM_TYPE_NAME_RE: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[a-z][A-Za-z0-9_]*$").unwrap());

impl Schema {
    /// checkItemTypes checks the names of the item types of a schema (their attributes are checked with the others).
    pub(crate) fn check_item_types(&self, prefix: &str, issues: &mut Issues) {
        let mut seen = HashSet::new();
        for (i, t) in self.item_types.iter().enumerate() {
            let path = format!("{prefix}itemTypes[{i}].name");
            if t.name.is_empty() {
                issues.add(path, "name required");
            } else if !ITEM_TYPE_NAME_RE.is_match(&t.name) {
                issues.add(
                    path,
                    "name must be letters, digits and '_' and start with a lowercase letter",
                );
            } else if seen.contains(&t.name) {
                issues.add(path, format!("duplicate item type {}", t.name));
            }
            seen.insert(t.name.clone());
        }
    }
}
