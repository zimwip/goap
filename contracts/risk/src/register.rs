//! The registration of the item types of the risk register.

use std::sync::Arc;

use goap_domain::{ItemPermission, register_item_type, require_item_permission};

use crate::{
    PERMISSION_SIGN, TYPE_ACTION, TYPE_DEROGATION, TYPE_RISK, derogation::validate_derogation,
    risk::validate_record,
};

/// Register gives the types of item of the risk register their validation: risk, action and derogation (declared by
/// the built-in methodology domain, ADR 0128; see domain::register_item_type). The services that write or read such
/// items call it at start, and so does a test that builds them; it may be called any number of times.
pub fn register() {
    register_item_type(TYPE_RISK, Some(Arc::new(validate_record)));
    register_item_type(TYPE_ACTION, Some(Arc::new(validate_record)));
    register_item_type(TYPE_DEROGATION, Some(Arc::new(validate_derogation)));
    // whoever writes a derogation holds derogation:sign and is its signatory (ADR 0075)
    require_item_permission(
        TYPE_DEROGATION,
        ItemPermission {
            permission: PERMISSION_SIGN.into(),
            subject_field: "signatory".into(),
        },
    );
}
