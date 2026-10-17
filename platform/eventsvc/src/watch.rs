//! The event service (Go's `watch.go`): Watch, Heartbeat and Leave over Connect.

use std::collections::VecDeque;
use std::sync::Arc;
use std::sync::atomic::Ordering;
use std::time::Duration;

use goap_authz as authz;
use goap_connect::{ConnectError, Ctx, Request, Response, ServerStream};
use goap_gen::events::v1 as pb;
use goap_identity::Extractor;
use goap_pbconv as pbconv;

use crate::errors::ERR_ANONYMOUS;
use crate::hub::{Cancel, Hub, Subscriber, Viewer};

/// HeartbeatEvery is how often an idle stream says it is alive.
pub const HEARTBEAT_EVERY: Duration = Duration::from_secs(15);

/// HeaderCommand is the header a client names its command with.
pub const HEADER_COMMAND: &str = goap_platform::HEADER_COMMAND;

/// Presence of one tab.
pub type Presence = pb::Presence;

/// Handler implements the event service (`EventService`).
#[derive(Clone)]
pub struct Handler {
    pub hub: Arc<Hub>,
    pub identity: Extractor,
    /// Idle overrides HEARTBEAT_EVERY (tests).
    pub idle: Duration,
}

impl Handler {
    /// A handler of the hub, identities from the request headers only.
    pub fn new(hub: Arc<Hub>) -> Handler {
        Handler {
            hub,
            identity: Extractor::default(),
            idle: Duration::ZERO,
        }
    }

    fn who(&self, ctx: &Ctx, hdr: &goap_connect::axum::http::HeaderMap) -> (Ctx, authz::Principal) {
        let ctx = self.identity.context(ctx, hdr);
        let who = authz::principal_from(&ctx);
        (ctx, who)
    }
}

fn resync(h: &Hub) -> pb::Event {
    pb::Event {
        epoch: h.epoch(),
        r#type: "resync".into(),
        time: pbconv::time(&h.now()),
        ..Default::default()
    }
}

/// The state of a Watch stream.
struct WatchState {
    hub: Arc<Hub>,
    ctx: Ctx,
    viewer: Viewer,
    sub: Subscriber,
    _cancel: Cancel,
    queued: VecDeque<pb::Event>,
    tick: tokio::time::Interval,
    done: bool,
}

impl WatchState {
    /// The next event to send; None ends the stream.
    async fn next(&mut self) -> Option<pb::Event> {
        if let Some(ev) = self.queued.pop_front() {
            return Some(ev);
        }
        if self.done {
            return None;
        }
        loop {
            tokio::select! {
                _ = self.ctx.done() => return None,
                _ = self.tick.tick() => {
                    return Some(pb::Event {
                        epoch: self.hub.epoch(),
                        seq: self.hub.seq(),
                        r#type: "heartbeat".into(),
                        time: pbconv::time(&self.hub.now()),
                        ..Default::default()
                    });
                }
                rec = self.sub.rx.recv() => {
                    let Some(rec) = rec else {
                        // too slow to follow (or the hub let go): say so, the client reconnects and refetches
                        if self.sub.overflow.load(Ordering::SeqCst) {
                            tracing::debug!("events: a stream too slow to follow is cut");
                        }
                        self.done = true;
                        return Some(resync(&self.hub));
                    };
                    if self.viewer.sees(&self.hub, &self.ctx, &rec).await {
                        return Some(rec.ev.clone());
                    }
                }
            }
        }
    }
}

#[goap_connect::async_trait]
impl pb::EventService for Handler {
    /// Watch streams the events the caller may read.
    async fn watch(
        &self,
        ctx: &Ctx,
        r: Request<pb::WatchRequest>,
    ) -> Result<Response<ServerStream<pb::Event>>, ConnectError> {
        let (ctx, who) = self.who(ctx, &r.headers);
        if who.anonymous() {
            return Err(ConnectError::unauthenticated(ERR_ANONYMOUS));
        }
        let (sub, backlog, must_resync, cancel) = self.hub.subscribe(&r.msg.epoch, r.msg.after_seq);
        let mut viewer = self.hub.visibility(who);
        let mut queued = VecDeque::new();
        if must_resync {
            queued.push_back(resync(&self.hub));
        }
        // a stream starts from where everybody looks
        queued.push_back(self.hub.snapshot(&viewer));
        for rec in &backlog {
            if viewer.sees(&self.hub, &ctx, rec).await {
                queued.push_back(rec.ev.clone());
            }
        }
        let every = if self.idle > Duration::ZERO {
            self.idle
        } else {
            HEARTBEAT_EVERY
        };
        let mut tick = tokio::time::interval_at(tokio::time::Instant::now() + every, every);
        tick.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        let st = WatchState {
            hub: self.hub.clone(),
            ctx,
            viewer,
            sub,
            _cancel: cancel,
            queued,
            tick,
            done: false,
        };
        let stream = futures::stream::unfold(st, |mut st| async move {
            let ev = st.next().await?;
            Some((Ok(ev), st))
        });
        Ok(Response::new(goap_connect::server_stream(stream)))
    }

    /// Heartbeat records where a tab of the caller looks.
    async fn heartbeat(
        &self,
        ctx: &Ctx,
        r: Request<pb::HeartbeatRequest>,
    ) -> Result<Response<pb::HeartbeatResponse>, ConnectError> {
        let (_, who) = self.who(ctx, &r.headers);
        if who.anonymous() {
            return Err(ConnectError::unauthenticated(ERR_ANONYMOUS));
        }
        let m = r.msg;
        self.hub.heartbeat(&who.subject, &m.tab_id, &m.kind, &m.id);
        Ok(Response::new(pb::HeartbeatResponse {}))
    }

    /// Leave drops a tab of the caller.
    async fn leave(
        &self,
        ctx: &Ctx,
        r: Request<pb::LeaveRequest>,
    ) -> Result<Response<pb::LeaveResponse>, ConnectError> {
        let (_, who) = self.who(ctx, &r.headers);
        if who.anonymous() {
            return Err(ConnectError::unauthenticated(ERR_ANONYMOUS));
        }
        self.hub.leave(&who.subject, &r.msg.tab_id);
        Ok(Response::new(pb::LeaveResponse {}))
    }
}
