//! The signals between engine replicas over NATS (Go's `signals.go`).

use std::sync::Arc;

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_engine::{Engine, Error, Result, Signals};
use goap_platform::Events;
use serde::{Deserialize, Serialize};

/// Subject of the wake signal between engine replicas (ADR 0124): core NATS, at most once, outside the GOAP stream
/// (a lost signal only delays the work: the shared locks stay the arbiter).
pub const SUBJECT_WAKE: &str = "engine.wake";
/// Subject of the interrupt signal between engine replicas.
pub const SUBJECT_INTERRUPT: &str = "engine.interrupt";

/// NATSSignals carries the signals between engine replicas over NATS (engine Signals).
#[derive(Debug, Clone)]
pub struct NATSSignals {
    pub bus: Events,
}

#[derive(Debug, Default, Serialize, Deserialize)]
#[serde(default)]
struct InterruptSignal {
    id: String,
    by: String,
}

fn bus_err(e: goap_platform::PlatformError) -> Error {
    Error::msg(e.to_string())
}

#[async_trait]
impl Signals for NATSSignals {
    async fn wake(&self, _ctx: &Ctx) -> Result<()> {
        self.bus
            .publish_core(SUBJECT_WAKE, &serde_json::json!({}))
            .await
            .map_err(bus_err)
    }

    async fn interrupt(&self, _ctx: &Ctx, id: &str, by: &str) -> Result<()> {
        self.bus
            .publish_core(
                SUBJECT_INTERRUPT,
                &InterruptSignal {
                    id: id.to_string(),
                    by: by.to_string(),
                },
            )
            .await
            .map_err(bus_err)
    }
}

impl NATSSignals {
    /// Listen hands the signals of the other replicas to e.
    pub async fn listen(&self, e: Arc<Engine>) -> Result<()> {
        let woken = e.clone();
        self.bus
            .subscribe(SUBJECT_WAKE, move |_| woken.woken())
            .await
            .map_err(bus_err)?;
        self.bus
            .subscribe(SUBJECT_INTERRUPT, move |data| {
                if let Ok(sig) = serde_json::from_slice::<InterruptSignal>(&data)
                    && !sig.id.is_empty()
                {
                    e.interrupted(&sig.id, &sig.by);
                }
            })
            .await
            .map_err(bus_err)
    }
}
