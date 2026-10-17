//! Broker fans process events out to live subscribers.

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use parking_lot::Mutex;
use tokio::sync::mpsc;

use crate::engine::ProcessEvent;
use crate::error::Result;
use crate::ports::{Payload, Publisher};

/// Broker fans process events out to live subscribers (WatchEvents). With several engine replicas it is fed from
/// NATS instead of local publications.
#[derive(Default)]
pub struct Broker {
    #[allow(clippy::type_complexity)]
    inner: Arc<Mutex<(HashMap<u64, mpsc::Sender<ProcessEvent>>, u64)>>,
}

/// Unsubscribe ends a subscription of a Broker (and closes its channel).
pub struct Unsubscribe {
    #[allow(clippy::type_complexity)]
    inner: Arc<Mutex<(HashMap<u64, mpsc::Sender<ProcessEvent>>, u64)>>,
    id: u64,
}

impl Unsubscribe {
    pub fn cancel(self) {
        self.inner.lock().0.remove(&self.id);
    }
}

impl Broker {
    /// NewBroker returns an empty broker.
    pub fn new() -> Broker {
        Broker::default()
    }

    /// Deliver sends an event to every subscriber; slow subscribers lose events rather than blocking the engine.
    pub fn deliver(&self, ev: ProcessEvent) {
        let g = self.inner.lock();
        for ch in g.0.values() {
            let _ = ch.try_send(ev.clone());
        }
    }

    /// Subscribe returns a channel of events and its cancel handle.
    pub fn subscribe(&self, buffer: usize) -> (mpsc::Receiver<ProcessEvent>, Unsubscribe) {
        let (tx, rx) = mpsc::channel(buffer.max(1));
        let mut g = self.inner.lock();
        let id = g.1;
        g.1 += 1;
        g.0.insert(id, tx);
        (
            rx,
            Unsubscribe {
                inner: self.inner.clone(),
                id,
            },
        )
    }
}

#[async_trait]
impl Publisher for Broker {
    /// Publish implements Publisher (local delivery).
    async fn publish(&self, _: &Ctx, _: &str, v: &Payload<'_>) -> Result<()> {
        if let Some(ev) = v.downcast::<ProcessEvent>() {
            self.deliver(ev.clone());
        }
        Ok(())
    }
}

/// Publishers publishes to several publishers.
#[derive(Clone, Default)]
pub struct Publishers(pub Vec<Arc<dyn Publisher>>);

#[async_trait]
impl Publisher for Publishers {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Payload<'_>) -> Result<()> {
        let mut first = None;
        for p in &self.0 {
            if let Err(e) = p.publish(ctx, subject, v).await
                && first.is_none()
            {
                first = Some(e);
            }
        }
        match first {
            Some(e) => Err(e),
            None => Ok(()),
        }
    }
}
