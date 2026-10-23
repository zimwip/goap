//! The item types this service gives a meaning beyond their data (`domain.HasItemCode`): the core of the change
//! mechanism and the use cases the Go graph service registers (`risk.Register`, `assess.Register`, `verify.Register`),
//! with the permission a use case asks to write one (`domain.RequireItemPermission`).

/// The core item types (`domain.CoreItemTypes`).
pub const CORE: [&str; 7] = [
    "artifact",
    "decision",
    "decision_point",
    "flow",
    "review",
    "signal",
    "transition",
];

/// The item types of the use cases the graph service registers.
pub const REGISTERED: [&str; 7] = [
    "risk",
    "action",
    "derogation",
    "hypothesis",
    "assessment",
    "comment",
    "verification",
];

/// Whether the items of a type have a meaning beyond their data here.
pub fn has_code(t: &str) -> bool {
    CORE.contains(&t) || REGISTERED.contains(&t)
}

/// The permission a writer of an item of the type needs, and the field of its data naming its subject.
pub fn permission(t: &str) -> Option<(&'static str, &'static str)> {
    (t == "derogation").then_some(("derogation:sign", "signatory"))
}
