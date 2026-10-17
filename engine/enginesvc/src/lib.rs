//! Port of `engine/enginesvc` (see docs/rust-port.md): the process engine over Connect ([`Handler`], the
//! `goap.engine.v1.EngineService`), the process store on SQL ([`SQLStore`], SQLite and PostgreSQL, also the lock
//! service of the engine replicas) and the signals between replicas over NATS ([`NATSSignals`]).
//!
//! Differences from the Go package: `SQLStore` holds a [`SqlDb`] (the pool of its dialect) in place of a `*sql.DB`
//! and its `Dollar` flag; the handler logs through `tracing`; `Handler::broker` is an `Arc<Broker>`.

mod handler;
mod signals;
mod sqlstore;

pub use handler::{Handler, RunFn, log_to_pb, process_to_pb, starting_points_to_pb, to_connect};
pub use signals::{NATSSignals, SUBJECT_INTERRUPT, SUBJECT_WAKE};
pub use sqlstore::{MIGRATIONS, SQLITE_MIGRATIONS, SQLStore, SqlDb, new_holder};
