//! The types of item the code knows.

use crate::{
    ItemType, TYPE_ARTIFACT, TYPE_DECISION, TYPE_DECISION_POINT, TYPE_FLOW, TYPE_REVIEW,
    TYPE_SIGNAL, TYPE_TRANSITION,
    change::{item_validator, registered_types},
};

/// CoreItemTypes are the types of item ChangeItem::validate knows without a registration: the change mechanism. A
/// domain declares them like any other (the built-in methodology domain, ADR 0128).
pub fn core_item_types() -> Vec<ItemType> {
    vec![
        TYPE_ARTIFACT,
        TYPE_DECISION,
        TYPE_DECISION_POINT,
        TYPE_FLOW,
        TYPE_REVIEW,
        TYPE_SIGNAL,
        TYPE_TRANSITION,
    ]
}

/// RegisteredItemTypes lists the types of item a use case gave a validator (register_item_type), by name.
pub fn registered_item_types() -> Vec<ItemType> {
    registered_types()
}

/// HasItemCode reports whether this process gives the items of a type a meaning beyond their data: a core type, or a
/// validator a use case registered (register_item_type).
pub fn has_item_code(t: &ItemType) -> bool {
    core_item_types().contains(t) || item_validator(t).is_some()
}

#[cfg(test)]
mod tests {
    use std::sync::Arc;

    use super::*;
    use crate::{ChangeItem, Error, register_item_type};

    #[derive(Debug, thiserror::Error)]
    #[error("boom")]
    struct Boom;

    // A type no use case gave a validator passes ChangeItem::validate (whether a domain declares it is the graph's
    // question, ADR 0128); registering is idempotent and the last validation of a type wins.
    #[test]
    fn register_item_type_test() {
        let typ = ItemType::from_static("testType");
        let item = ChangeItem {
            r#type: typ.clone(),
            ..Default::default()
        };
        item.validate().expect("a type with no validator");
        assert!(!has_item_code(&typ), "no code yet");
        register_item_type(
            typ.clone(),
            Some(Arc::new(|_: &ChangeItem| Err(Error::other(Boom)))),
        );
        let err = item.validate().expect_err("registered validation");
        assert!(
            err.downcast_ref::<Boom>().is_some(),
            "registered validation: {err}"
        );
        register_item_type(typ.clone(), None);
        item.validate().expect("re-registered, accepting");
        assert!(
            has_item_code(&typ)
                && has_item_code(&TYPE_ARTIFACT)
                && registered_item_types().contains(&typ),
            "registered and core types have code"
        );
    }
}
