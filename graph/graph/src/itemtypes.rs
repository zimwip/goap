//! Port of `itemtypes.go`: the types of change item the domains declare (ADR 0128) and the check of an item before
//! the graph stores it.

use std::sync::Arc;

use goap_domain::ChangeItem;
use serde_json::Value;

use crate::Graph;
use crate::algorithms::check_attribute_value;
use crate::graph::TypeCatalog;
use crate::structure::builtin_types;

impl Graph {
    /// itemCatalog is the catalogue that declares the types of change item: the one in force, else (untyped graph)
    /// the built-in domains.
    fn item_catalog(&self) -> Arc<dyn TypeCatalog> {
        self.catalog().unwrap_or_else(builtin_types)
    }

    /// ItemTypes lists the types of change item the domains in force declare (ADR 0128), by name.
    pub fn item_types(&self) -> Vec<goap_typecat::ItemType> {
        self.item_catalog()
            .item_types()
            .into_iter()
            .cloned()
            .collect()
    }

    /// checkItem checks an item before the graph stores it: its structure and the validation of its use case
    /// (ChangeItem::validate), then that a domain in force declares its type and that the keys of its data the type
    /// declares as attributes have their type and enum (ADR 0128); the other keys stay free. The error is the bare
    /// message (the callers wrap it).
    pub(crate) fn check_item(&self, it: &ChangeItem) -> Result<(), String> {
        it.validate().map_err(|e| e.to_string())?;
        let Some(checks) = self
            .item_catalog()
            .item_attribute_checks(it.r#type.as_str())
        else {
            return Err(format!(
                "unknown item type {:?}: no domain in force declares it",
                it.r#type.as_str()
            ));
        };
        for a in &checks {
            let v = it.data.get(&a.name).unwrap_or(&Value::Null);
            check_attribute_value(a, v).map_err(|e| format!("data.{}: {e}", a.name))?;
        }
        Ok(())
    }
}
