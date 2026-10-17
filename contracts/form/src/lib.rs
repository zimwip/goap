//!
//! The form of a human action (ADR 0115): the fields a person fills in, the widgets that render them and the
//! constraints limiting their choices, checked when the methodology compiles ([`check_form`]) and when a person
//! submits the values ([`validate`]). It is a leaf crate: it knows no graph, no engine and no methodology.

mod condition;
mod form;
mod widgets;

pub use condition::*;
pub use form::{
    Field, Form, Issue, Issues, Link, NAME_RE, NewNode, Ruling, SELF_KEY, check_form, form_is_zero,
    links, new_nodes, rulings, validate,
};
pub use widgets::*;
