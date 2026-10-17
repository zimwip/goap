use goap_form as form;
use serde_json::Value;

use crate::{struct_to_map, to_struct};

/// FormToPB carries the form of a human action (ADR 0115) as a Struct (None: none).
pub fn form_to_pb(f: Option<&form::Form>) -> Option<pbjson_types::Struct> {
    let f = f?;
    match serde_json::to_value(f).ok()? {
        Value::Object(m) => to_struct(&m),
        _ => None,
    }
}

/// FormFromPB reads the form of a human action from its Struct (None: none).
pub fn form_from_pb(s: Option<&pbjson_types::Struct>) -> Option<form::Form> {
    let s = s?;
    serde_json::from_value(Value::Object(struct_to_map(s))).ok()
}
