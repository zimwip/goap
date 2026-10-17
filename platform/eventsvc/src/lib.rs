//! The one event stream of the web (ADR 0053). [`Hub`] takes
//! the platform's publications (in process through [`Hub::publish`] or as an engine `Publisher`, from the bus through
//! [`Hub::ingest`]), numbers them in an epoch, keeps a short history to resume streams and holds the presence of the
//! tabs; [`Handler`] serves `goap.events.v1.EventService` (Watch, Heartbeat, Leave).
//!
//! Differences from the Go package: Go's `Publish(ctx, subject, any)` takes a [`Publication`] (the graph's
//! `EventSink` is adapted by the composition: its node, baseline and change events map one to one); the engine's
//! `ProcessEvent` serializes only, so the hub reads the bus's process events into its own [`ProcessEvent`]; the
//! relay gets the JSON value of the presence message.

mod errors;
mod hub;
mod watch;

pub use hub::{
    DEFAULT_HISTORY, DEFAULT_PRESENCE_TTL, Hub, LookupFn, NowFn, PRESENCE_SUBJECT, ProcessEvent,
    Publication, RelayFn, with_command,
};
pub use watch::{HEADER_COMMAND, HEARTBEAT_EVERY, Handler, Presence};

#[cfg(test)]
mod hub_test;
