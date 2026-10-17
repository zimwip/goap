//! Events on NATS JetStream.
use std::{future::Future, time::Duration};

use async_nats::{
    ConnectOptions, Event, HeaderMap,
    connection::State,
    jetstream::{
        self,
        consumer::{AckPolicy, DeliverPolicy, pull},
        stream::{self, StorageType},
    },
};
use bytes::Bytes;
use futures::StreamExt;
use goap_ctx::Ctx;
use serde::Serialize;

use crate::{
    PlatformError, Result,
    stamp::{HEADER_ACTOR, HEADER_COMMAND, Stamp, stamp_of},
};

/// The JetStream stream every event of the platform goes to (subjects `goap.>`).
pub const STREAM: &str = "GOAP";

/// Publishes JSON events on NATS JetStream. Cheap to clone. Go's methods accept a nil `*Events` (events disabled);
/// here the caller holds an `Option<Events>`.
#[derive(Clone)]
pub struct Events {
    pub(crate) client: async_nats::Client,
    pub(crate) js: jetstream::Context,
}

impl std::fmt::Debug for Events {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Events")
            .field("state", &self.client.connection_state())
            .finish()
    }
}

impl Events {
    /// Connects to NATS (reconnecting forever, a second apart) and ensures the GOAP stream (subjects `goap.>`, seven
    /// days, file storage).
    pub async fn connect(_ctx: &Ctx, url: &str) -> Result<Events> {
        let client = ConnectOptions::new()
            .name("goap")
            .retry_on_initial_connect()
            .max_reconnects(None)
            .reconnect_delay_callback(|_| Duration::from_secs(1))
            .event_callback(|ev| async move {
                if let Event::Disconnected = ev {
                    tracing::warn!("nats disconnected");
                }
            })
            .connect(url)
            .await
            .map_err(PlatformError::nats)?;
        let js = jetstream::new(client.clone());
        js.create_or_update_stream(stream::Config {
            name: STREAM.to_string(),
            subjects: vec!["goap.>".to_string()],
            max_age: Duration::from_secs(7 * 24 * 3600),
            storage: StorageType::File,
            ..Default::default()
        })
        .await
        .map_err(PlatformError::nats)?;
        Ok(Events { client, js })
    }

    /// Publishes `v` as JSON on the GOAP stream, waiting for its acknowledgement; the [`Stamp`] of ctx goes as
    /// headers.
    pub async fn publish<T: Serialize + ?Sized>(
        &self,
        ctx: &Ctx,
        subject: &str,
        v: &T,
    ) -> Result<()> {
        let data = serde_json::to_vec(v)?;
        let mut headers = HeaderMap::new();
        let st = stamp_of(ctx);
        if !st.command.is_empty() {
            headers.insert(HEADER_ACTOR, st.actor.as_str());
            headers.insert(HEADER_COMMAND, st.command.as_str());
        }
        let ack = self
            .js
            .publish_with_headers(subject.to_string(), headers, data.into())
            .await
            .map_err(PlatformError::nats)?;
        ack.await.map_err(PlatformError::nats)?;
        Ok(())
    }

    /// Publishes without persistence (core NATS, at most once): for ephemeral facts such as presence, on subjects
    /// outside the GOAP stream (`goap.>`).
    pub async fn publish_core<T: Serialize + ?Sized>(&self, subject: &str, v: &T) -> Result<()> {
        let data = serde_json::to_vec(v)?;
        self.client
            .publish(subject.to_string(), data.into())
            .await
            .map_err(PlatformError::nats)
    }

    /// Checks the connection.
    pub async fn ready(&self, _ctx: &Ctx) -> Result<()> {
        if self.client.connection_state() != State::Connected {
            return Err(PlatformError::Other("nats not connected".into()));
        }
        Ok(())
    }

    /// Drains the connection.
    pub async fn close(&self) {
        let _ = self.client.drain().await;
    }

    /// Calls `f` for every message on `subject` (core NATS, at most once), from a task of its own.
    pub async fn subscribe<F>(&self, subject: &str, f: F) -> Result<()>
    where
        F: Fn(Bytes) + Send + Sync + 'static,
    {
        let mut sub = self
            .client
            .subscribe(subject.to_string())
            .await
            .map_err(PlatformError::nats)?;
        tokio::spawn(async move {
            while let Some(m) = sub.next().await {
                f(m.payload);
            }
        });
        Ok(())
    }

    /// [`Events::subscribe`] for a wildcard subject: `f` also gets the subject of each message and the stamp (who,
    /// which command) its publisher put on it.
    pub async fn subscribe_subject<F>(&self, subject: &str, f: F) -> Result<()>
    where
        F: Fn(String, Bytes, Stamp) + Send + Sync + 'static,
    {
        let mut sub = self
            .client
            .subscribe(subject.to_string())
            .await
            .map_err(PlatformError::nats)?;
        tokio::spawn(async move {
            while let Some(m) = sub.next().await {
                let get = |k: &str| {
                    m.headers
                        .as_ref()
                        .and_then(|h| h.get(k))
                        .map(|v| v.as_str().to_string())
                        .unwrap_or_default()
                };
                let st = Stamp {
                    actor: get(HEADER_ACTOR),
                    command: get(HEADER_COMMAND),
                };
                f(m.subject.to_string(), m.payload.clone(), st);
            }
        });
        Ok(())
    }

    /// Delivers, at least once and in order, every message of the GOAP stream matching `subjects` to `f` through a
    /// durable consumer named `name`. A message is acknowledged when `f` returns `Ok` and redelivered after 5 s
    /// otherwise. Returns when ctx is done (or the consumer cannot be created).
    pub async fn consume_durable<F, Fut, E>(
        &self,
        ctx: &Ctx,
        name: &str,
        subjects: &[&str],
        f: F,
    ) -> Result<()>
    where
        F: Fn(Ctx, String, Bytes) -> Fut + Send + Sync,
        Fut: Future<Output = std::result::Result<(), E>> + Send,
        E: std::fmt::Display,
    {
        let stream = self
            .js
            .get_stream(STREAM)
            .await
            .map_err(PlatformError::nats)?;
        let consumer: jetstream::consumer::Consumer<pull::Config> = stream
            .create_consumer(pull::Config {
                durable_name: Some(name.to_string()),
                filter_subjects: subjects.iter().map(|s| s.to_string()).collect(),
                ack_policy: AckPolicy::Explicit,
                ack_wait: Duration::from_secs(60),
                deliver_policy: DeliverPolicy::All,
                max_ack_pending: 1,
                ..Default::default()
            })
            .await
            .map_err(PlatformError::nats)?;
        let mut msgs = consumer.messages().await.map_err(PlatformError::nats)?;
        loop {
            let next = tokio::select! {
                _ = ctx.done() => return Ok(()),
                m = msgs.next() => m,
            };
            let m = match next {
                None => return Ok(()),
                Some(Err(e)) => {
                    tracing::warn!(consumer = name, err = %e, "event consumer");
                    continue;
                }
                Some(Ok(m)) => m,
            };
            let subject = m.subject.to_string();
            match f(ctx.clone(), subject.clone(), m.payload.clone()).await {
                Ok(()) => {
                    let _ = m.ack().await;
                }
                Err(e) => {
                    tracing::warn!(consumer = name, subject = %subject, err = %e, "event not processed, redelivered");
                    let _ = m
                        .ack_with(jetstream::AckKind::Nak(Some(Duration::from_secs(5))))
                        .await;
                }
            }
        }
    }
}

/// Connects to NATS and ensures the GOAP stream ([`Events::connect`]).
pub async fn connect_events(ctx: &Ctx, url: &str) -> Result<Events> {
    Events::connect(ctx, url).await
}
