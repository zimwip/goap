//! The options derived from the type catalogue.

use std::sync::Arc;

use goap_def::{ATTR_STRING, WIDGET_TEXTAREA};
use goap_typecat::{Catalog, Type};

use crate::{BodyAttribute, Options};

/// FromTypes derives the options from the type catalogue: the `asName` attribute names a node, the string attributes
/// edited as a text area are the body. A type the catalogue does not know keeps the defaults.
pub fn from_types(lookup: impl Fn(&str) -> Option<Type> + Send + Sync + 'static) -> Options {
    let lookup = Arc::new(lookup);
    let name_lookup = lookup.clone();
    Options {
        name: Some(Arc::new(move |typ: &str| {
            name_lookup(typ)
                .and_then(|t| t.attributes.into_iter().find(|a| a.as_name))
                .map(|a| a.name.clone())
                .unwrap_or_default()
        })),
        body: Some(Arc::new(move |typ: &str| {
            let Some(t) = lookup(typ) else {
                return vec![BodyAttribute {
                    name: "description".into(),
                    label: "Description".into(),
                }];
            };
            t.attributes
                .into_iter()
                .filter(|a| {
                    a.widget == WIDGET_TEXTAREA && (a.r#type.is_empty() || a.r#type == ATTR_STRING)
                })
                .map(|a| BodyAttribute {
                    label: if a.label.is_empty() {
                        a.name.clone()
                    } else {
                        a.label.clone()
                    },
                    name: a.name.clone(),
                })
                .collect()
        })),
    }
}

/// The options of [`from_types`] over a catalogue (Go: `FromTypes(cat.Type)`).
pub fn from_catalog(cat: Arc<Catalog>) -> Options {
    from_types(move |r| cat.r#type(r).cloned())
}
