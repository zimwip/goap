//! Personal units: the user node doubles as the unit holding the changes of one person only (ADR 0037, 0039).

use goap_domain::Change;

/// UserPrefix starts the key of a user node ([`user_key`]). The node doubles as the personal unit of the user: the
/// OrgUnit of the "organisation" namespace that holds the changes of one person only (their preferences, ADR 0037,
/// 0039). A change held by it is personal: nobody else reads or changes it, and it is never shared through
/// sub-changes.
pub const USER_PREFIX: &str = "USR:";

/// UserKey is the key of the node of a user, also the key of its personal unit.
pub fn user_key(subject: &str) -> String {
    format!("{USER_PREFIX}{subject}")
}

/// PersonalUnit returns the key of the personal unit of a subject (the key of its user node).
pub fn personal_unit(subject: &str) -> String {
    user_key(subject)
}

/// IsPersonalUnit reports whether a unit key designates a personal unit.
pub fn is_personal_unit(key: &str) -> bool {
    key.starts_with(USER_PREFIX)
}

/// PersonalSubject returns the subject a personal unit belongs to ("" for any other unit).
pub fn personal_subject(key: &str) -> &str {
    key.strip_prefix(USER_PREFIX).unwrap_or("")
}

/// IsPersonal reports whether the change is held by a personal unit.
pub fn is_personal(c: &Change) -> bool {
    is_personal_unit(&c.owner_org)
}

/// PersonalSubjectOf returns the subject a personal change belongs to ("" for a change that is not personal).
pub fn personal_subject_of(c: &Change) -> &str {
    personal_subject(&c.owner_org)
}

/// IsPersonalTo reports whether the change is personal to the subject.
pub fn is_personal_to(c: &Change, subject: &str) -> bool {
    !subject.is_empty() && is_personal(c) && personal_subject_of(c) == subject
}
