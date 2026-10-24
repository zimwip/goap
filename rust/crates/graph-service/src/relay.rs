//! The relays of the outboxes the graph process writes (`lib/outbox.Relay`, ADR 0136): the graph's `event_outbox`
//! and the domain registry's `domain_outbox`, delivered in order, at least once, on the GOAP stream of the bus,
//! deduplicated by the id of each event (`<origin>:<seq>`, `Nats-Msg-Id`). A drain runs after each commit that wrote
//! events; [`Relay::run`] delivers what a drain could not, and purges what was published.
use std::{
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};

use async_nats::{HeaderMap, jetstream};
use chrono::Utc;
use graph_store::{Store, read::Pending};
use tokio::sync::{Mutex, Notify};

/// The header carrying the id of an event on the bus (`platform.HeaderEventID`).
pub const HEADER_EVENT_ID: &str = "X-Goap-Event-Id";

/// How many messages a drain reads at a time (`outbox.batch`).
const BATCH: i64 = 100;

/// Where a relay delivers.
pub enum Sink {
    /// The GOAP stream of the bus.
    Nats(jetstream::Context),
    /// A list, for the tests: each message with its id.
    #[cfg(test)]
    Memory(std::sync::Mutex<Vec<(String, Pending)>>),
}

impl Sink {
    /// Connects to the bus at `url` and makes sure the GOAP stream is there (`platform.ConnectEvents`).
    pub async fn connect(url: &str) -> anyhow::Result<Sink> {
        let client = async_nats::connect(url).await?;
        let js = jetstream::new(client);
        js.create_or_update_stream(jetstream::stream::Config {
            name: "GOAP".into(),
            subjects: vec!["goap.>".into()],
            max_age: Duration::from_secs(7 * 24 * 3600),
            storage: jetstream::stream::StorageType::File,
            ..Default::default()
        })
        .await?;
        Ok(Sink::Nats(js))
    }

    async fn deliver(&self, id: &str, m: &Pending) -> anyhow::Result<()> {
        match self {
            Sink::Nats(js) => {
                let mut h = HeaderMap::new();
                for (k, v) in &m.headers {
                    h.insert(k.as_str(), v.as_str());
                }
                h.insert(HEADER_EVENT_ID, id);
                let msg = jetstream::message::PublishMessage::build()
                    .payload(m.data.clone().into())
                    .headers(h)
                    .message_id(id);
                js.send_publish(m.subject.clone(), msg).await?.await?;
                Ok(())
            }
            #[cfg(test)]
            Sink::Memory(l) => {
                l.lock().unwrap().push((id.to_string(), m.clone()));
                Ok(())
            }
        }
    }
}

/// The relay of one outbox table.
pub struct Relay {
    /// The service in the event ids (`graph`, `domains`).
    pub origin: &'static str,
    pub table: &'static str,
    sink: Arc<Sink>,
    poll: Duration,
    retention: Duration,
    /// Held by the drain at work.
    busy: Mutex<()>,
    /// Set by a drain that found one at work: the one at work looks again before it stops.
    again: AtomicBool,
    kick: Notify,
}

impl Relay {
    pub fn new(origin: &'static str, table: &'static str, sink: Arc<Sink>) -> Relay {
        Relay {
            origin,
            table,
            sink,
            poll: env_duration("GOAP_OUTBOX_POLL", Duration::from_secs(1)),
            retention: env_duration("GOAP_OUTBOX_RETENTION", Duration::from_secs(24 * 3600)),
            busy: Mutex::new(()),
            again: AtomicBool::new(false),
            kick: Notify::new(),
        }
    }

    /// Delivers the pending messages in order until none is left or one fails (`Relay.Drain`); a drain already at
    /// work is left to deliver them, and this one returns at once (Go's `ErrBusy`, not an error here).
    pub async fn drain(&self, store: &Store) -> anyhow::Result<()> {
        let Ok(_held) = self.busy.try_lock() else {
            self.again.store(true, Ordering::SeqCst);
            self.kick.notify_one();
            return Ok(());
        };
        loop {
            self.again.store(false, Ordering::SeqCst);
            let msgs = store.reader().await?.outbox_pending(self.table, BATCH).await?;
            if msgs.is_empty() {
                if self.again.load(Ordering::SeqCst) {
                    continue;
                }
                return Ok(());
            }
            let mut done = Vec::new();
            let mut failed = None;
            for m in &msgs {
                if let Err(e) = self.sink.deliver(&format!("{}:{}", self.origin, m.seq), m).await {
                    failed = Some(e);
                    break;
                }
                done.push(m.seq);
            }
            if !done.is_empty() {
                let tx = store.writer().await?;
                tx.outbox_mark(self.table, &done, Utc::now()).await?;
                tx.commit().await?;
            }
            if let Some(e) = failed {
                return Err(e);
            }
        }
    }

    /// Drains after a commit; what it cannot deliver now, [`Relay::run`] delivers later.
    pub async fn after_commit(&self, store: &Store) {
        if let Err(e) = self.drain(store).await {
            tracing::debug!(origin = self.origin, error = %e, "outbox delivery deferred");
            self.kick.notify_one();
        }
    }

    /// Delivers when kicked or every poll, backing off while the sink fails, and purges the published messages
    /// hourly (`Relay.Run`); it runs as long as the process.
    pub async fn run(self: Arc<Self>, state: Arc<crate::State>) {
        let store = &state.store;
        let mut wait = self.poll;
        let mut purged: Option<std::time::Instant> = None;
        loop {
            tokio::select! {
                () = self.kick.notified() => {}
                () = tokio::time::sleep(wait) => {}
            }
            match self.drain(store).await {
                Ok(()) => wait = self.poll,
                Err(e) => {
                    tracing::warn!(origin = self.origin, error = %e, "outbox delivery failed, retried");
                    wait = (2 * wait).min(Duration::from_secs(60));
                }
            }
            if purged.is_none_or(|t| t.elapsed() > Duration::from_secs(3600)) {
                let before = Utc::now() - chrono::Duration::from_std(self.retention).unwrap_or_default();
                let purge = async {
                    let tx = store.writer().await?;
                    tx.outbox_purge(self.table, before).await?;
                    tx.commit().await?;
                    anyhow::Ok(())
                };
                if let Err(e) = purge.await {
                    tracing::warn!(origin = self.origin, error = %e, "outbox purge");
                }
                purged = Some(std::time::Instant::now());
            }
        }
    }
}

/// A duration of the environment in Go's notation (`platform.EnvDuration`: `1s`, `1m30s`, `500ms`), else `def`.
fn env_duration(key: &str, def: Duration) -> Duration {
    std::env::var(key).ok().and_then(|v| go_duration(&v)).unwrap_or(def)
}

/// A duration in Go's notation (`time.ParseDuration`), none when it does not read or is not positive.
fn go_duration(s: &str) -> Option<Duration> {
    let mut rest = s.trim();
    let mut total = 0f64;
    if rest.is_empty() {
        return None;
    }
    while !rest.is_empty() {
        let n = rest
            .find(|c: char| !(c.is_ascii_digit() || c == '.'))
            .unwrap_or(rest.len());
        let value: f64 = rest[..n].parse().ok()?;
        rest = &rest[n..];
        let u = rest
            .find(|c: char| c.is_ascii_digit() || c == '.')
            .unwrap_or(rest.len());
        let unit = match &rest[..u] {
            "ns" => 1e-9,
            "us" | "µs" => 1e-6,
            "ms" => 1e-3,
            "s" => 1.0,
            "m" => 60.0,
            "h" => 3600.0,
            _ => return None,
        };
        total += value * unit;
        rest = &rest[u..];
    }
    (total > 0.0).then(|| Duration::from_secs_f64(total))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn reads_go_durations() {
        assert_eq!(go_duration("1s"), Some(Duration::from_secs(1)));
        assert_eq!(go_duration("1m30s"), Some(Duration::from_secs(90)));
        assert_eq!(go_duration("500ms"), Some(Duration::from_millis(500)));
        assert_eq!(go_duration("24h"), Some(Duration::from_secs(86400)));
        assert_eq!(go_duration("3"), None);
        assert_eq!(go_duration(""), None);
    }
}
