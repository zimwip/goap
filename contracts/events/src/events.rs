//! The event names (`events.go`).

// The events of changes, processes and methodologies, as triggers (`event:`) and subscriptions (`on:`) name them.
pub const CHANGE_CREATED: &str = "change.created";
pub const CHANGE_APPLIED: &str = "change.applied";
pub const CHANGE_ITEM_ADDED: &str = "change.item_added";
pub const CHANGE_SIGNAL: &str = "change.signal";
// CHANGE_TRANSITIONED, CHANGE_COMMITTED, CHANGE_ATTACHED and CHANGE_DETACHED couple the changes of methodologies
// integrating one another (ADR 0106): a lifecycle move, a commit on its branch, an attachment to a change
// integrating it, a detachment from it.
pub const CHANGE_TRANSITIONED: &str = "change.transitioned";
pub const CHANGE_COMMITTED: &str = "change.committed";
pub const CHANGE_ATTACHED: &str = "change.attached";
pub const CHANGE_DETACHED: &str = "change.detached";
/// A field of the header was edited (title, intent, goal, data), the fields in event.fields (ADR 0110).
pub const CHANGE_EDITED: &str = "change.edited";
pub const PROCESS_COMPLETED: &str = "process.completed";
pub const PROCESS_FAILED: &str = "process.failed";
pub const PROCESS_STUCK: &str = "process.stuck";
pub const PROCESS_ATTACHED: &str = "process.attached";
pub const STEP_COMPLETED: &str = "step.completed";
pub const METHODOLOGY_PUBLISHED: &str = "methodology.published";

/// Lists the events a trigger can react to (methodology's `TRIGGER_EVENTS`); the web keeps one copy of it
/// (TRIGGER_EVENTS in web/src/lib/api/types/registry.ts), checked against this list by a test.
pub const ALL: &[&str] = &[
    CHANGE_CREATED,
    CHANGE_APPLIED,
    CHANGE_ITEM_ADDED,
    CHANGE_SIGNAL,
    CHANGE_TRANSITIONED,
    CHANGE_COMMITTED,
    CHANGE_ATTACHED,
    CHANGE_DETACHED,
    CHANGE_EDITED,
    PROCESS_COMPLETED,
    PROCESS_FAILED,
    PROCESS_STUCK,
    PROCESS_ATTACHED,
    STEP_COMPLETED,
    METHODOLOGY_PUBLISHED,
];

// The names the process broker gives some of its events (engine's `ProcessEvent.event`); the others are the process
// statuses ("completed", "failed", "stuck").
pub const BROKER_ATTACHED: &str = "attached";
pub const BROKER_STEP_COMPLETED: &str = "step_completed";

/// Maps the name of a process broker event to the event triggers see, for the two that are not a status (the one
/// place the two vocabularies meet).
pub fn from_broker(name: &str) -> Option<&'static str> {
    match name {
        BROKER_ATTACHED => Some(PROCESS_ATTACHED),
        BROKER_STEP_COMPLETED => Some(STEP_COMPLETED),
        _ => None,
    }
}

/// The broker subject a change event is published on: goap.change.<change>.<event without its "change." prefix>.
pub fn change_subject(change: &str, event: &str) -> String {
    format!(
        "goap.change.{}.{}",
        change,
        event.strip_prefix("change.").unwrap_or(event)
    )
}
