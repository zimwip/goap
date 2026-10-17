//! The hub (Go's `hub.go`): the one event stream of the web (ADR 0053), the facts the platform publishes
//! (processes, changes, nodes, baselines, registry) as thin, ordered events a client keeps its state from, and the
//! presence of the users looking at them.

use std::collections::{BTreeMap, HashMap, VecDeque};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::Duration;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use futures::future::BoxFuture;
use goap_authz::{self as authz, Authorizer};
use goap_ctx::Ctx;
use goap_domain::{BaselineEvent, ChangeEvent, NodeEvent, gotime};
use goap_engine::{LogLine, Payload, Process};
use goap_gen::events::v1 as pb;
use goap_pbconv as pbconv;
use goap_platform::Stamp;
use parking_lot::Mutex;
use serde::{Deserialize, Serialize};
use serde_json::Value;
use tokio::sync::mpsc;

/// The number of events kept to resume a stream when Hub::history is zero.
pub const DEFAULT_HISTORY: usize = 5000;
/// How long a tab stays present without a heartbeat when Hub::presence_ttl is zero.
pub const DEFAULT_PRESENCE_TTL: Duration = Duration::from_secs(30);
pub(crate) const SUBSCRIBER_BUFFER: usize = 512;
const MAX_PROCESSES: usize = 20000;

/// PresenceSubject prefixes the bus subjects presence travels on between replicas: outside goap.>, so the GOAP
/// stream does not persist it (core NATS, at most once).
pub const PRESENCE_SUBJECT: &str = "presence.";

/// Finds the process of a log line that arrives before any event of its process.
pub type LookupFn = Arc<dyn Fn(Ctx, String) -> BoxFuture<'static, Option<Process>> + Send + Sync>;
/// Tells the other replicas of a presence change: the subject (under PRESENCE_SUBJECT) and the JSON message.
pub type RelayFn = Arc<dyn Fn(&str, &Value) + Send + Sync>;
/// The clock of a hub.
pub type NowFn = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

/// A process event as the hub reads it: what the engine publishes (`goap_engine::ProcessEvent`), or its JSON on the
/// bus.
#[derive(Debug, Clone, Deserialize)]
#[serde(default)]
pub struct ProcessEvent {
    pub event: String,
    pub process: Option<Process>,
    pub log: Option<LogLine>,
    #[serde(with = "gotime")]
    pub time: DateTime<Utc>,
}

impl Default for ProcessEvent {
    fn default() -> Self {
        ProcessEvent {
            event: String::new(),
            process: None,
            log: None,
            time: gotime::zero(),
        }
    }
}

impl From<&goap_engine::ProcessEvent> for ProcessEvent {
    fn from(e: &goap_engine::ProcessEvent) -> Self {
        ProcessEvent {
            event: e.event.clone(),
            process: e.process.clone(),
            log: e.log.clone(),
            time: e.time,
        }
    }
}

/// A publication the hub takes (Go's `any` of `Publish`): a process event, a node or baseline event of the graph,
/// a change event, or the `{name, version}` of a registry event.
#[derive(Debug, Clone)]
#[allow(clippy::large_enum_variant)]
pub enum Publication {
    Process(ProcessEvent),
    Node(NodeEvent),
    Baseline(BaselineEvent),
    Change(ChangeEvent),
    Registry(BTreeMap<String, String>),
}

/// record is an event with what decides who may see it.
#[derive(Debug)]
pub(crate) struct Record {
    pub(crate) ev: pb::Event,
    /// process the event is about (process events)
    pub(crate) proc: String,
    /// the subject a personal change's events are for ("" when they are public)
    pub(crate) personal_to: String,
}

#[derive(Debug, Clone, Default)]
pub(crate) struct ProcScope {
    pub(crate) id: String,
    pub(crate) methodology: String,
    pub(crate) org: String,
    pub(crate) owner: String,
    pub(crate) project: String,
}

impl ProcScope {
    pub(crate) fn of(p: &Process) -> ProcScope {
        ProcScope {
            id: p.id.clone(),
            methodology: p.methodology.clone(),
            org: p.initiator.org.clone(),
            owner: p.initiator.subject.clone(),
            project: p.project.clone(),
        }
    }
}

struct Sub {
    tx: mpsc::Sender<Arc<Record>>,
    overflow: Arc<AtomicBool>,
}

/// A stream's subscription: the records delivered to it, and whether it was cut for not keeping up.
pub(crate) struct Subscriber {
    pub(crate) rx: mpsc::Receiver<Arc<Record>>,
    pub(crate) overflow: Arc<AtomicBool>,
}

#[derive(Debug, Clone)]
pub(crate) struct Tab {
    pub(crate) subject: String,
    pub(crate) id: String,
    pub(crate) kind: String,
    pub(crate) loc: String,
    pub(crate) since: DateTime<Utc>,
    pub(crate) seen: DateTime<Utc>,
    /// the tab is connected to this replica (the others learn of it through the relay)
    pub(crate) local: bool,
}

impl Tab {
    fn pb(&self) -> pb::Presence {
        pb::Presence {
            tab_id: self.id.clone(),
            subject: self.subject.clone(),
            kind: self.kind.clone(),
            id: self.loc.clone(),
            since: pbconv::time(&self.since),
        }
    }
}

pub(crate) struct State {
    pub(crate) epoch: String,
    pub(crate) seq: u64,
    /// oldest first
    pub(crate) ring: VecDeque<Arc<Record>>,
    subs: HashMap<usize, Sub>,
    next: usize,
    /// process id -> who may read it
    pub(crate) procs: HashMap<String, ProcScope>,
    /// personal change id -> its subject
    personal: HashMap<String, String>,
    /// subject/tab id -> where it looks
    pub(crate) tabs: HashMap<String, Tab>,
}

impl State {
    /// personal_of is the subject only whom a tab on a personal change may be shown to.
    fn personal_of(&self, t: &Tab) -> String {
        if t.kind == "change" {
            return self.personal.get(&t.loc).cloned().unwrap_or_default();
        }
        String::new()
    }

    /// deliver sends a record to every subscriber; one that cannot keep up is cut (it resyncs on reconnect).
    fn deliver(&mut self, r: Arc<Record>) {
        self.subs.retain(|_, s| match s.tx.try_send(r.clone()) {
            Ok(()) => true,
            Err(mpsc::error::TrySendError::Full(_)) => {
                s.overflow.store(true, Ordering::SeqCst);
                false
            }
            // the stream went
            Err(mpsc::error::TrySendError::Closed(_)) => false,
        });
    }

    /// presence_event delivers a presence change; it is live only (not numbered, not kept): a stream starts from a
    /// snapshot.
    fn presence_event(&mut self, typ: &str, t: &Tab, now: DateTime<Utc>) {
        let r = Record {
            ev: pb::Event {
                epoch: self.epoch.clone(),
                r#type: typ.into(),
                kind: "presence".into(),
                id: t.loc.clone(),
                time: pbconv::time(&now),
                presence: vec![t.pb()],
                ..Default::default()
            },
            proc: String::new(),
            personal_to: self.personal_of(t),
        };
        self.deliver(Arc::new(r));
    }
}

/// Hub receives the platform's publications (it is an engine Publisher, and the graph's events reach it through
/// [`Hub::publish`]; ingest takes them from NATS), turns them into ordered events, keeps a short history to resume
/// streams, and holds the presence.
pub struct Hub {
    /// Authz decides which process events a caller may read; None allows every one.
    pub authz: Option<Arc<dyn Authorizer>>,
    /// Lookup finds the process of a log line that arrives before any event of its process (optional).
    pub lookup: Option<LookupFn>,
    /// History is the number of events kept to resume a stream (DEFAULT_HISTORY when zero).
    pub history: usize,
    /// PresenceTTL is how long a tab stays present without a heartbeat (DEFAULT_PRESENCE_TTL when zero).
    pub presence_ttl: Duration,
    /// Relay tells the other replicas of a presence change (None: a single replica). The subjects are under
    /// PRESENCE_SUBJECT, the message is JSON.
    pub relay: Option<RelayFn>,
    /// Now is the clock (Utc::now when None).
    pub now: Option<NowFn>,

    pub(crate) st: Arc<Mutex<State>>,
}

impl Default for Hub {
    fn default() -> Self {
        Hub::new()
    }
}

/// presenceMsg is what replicas tell each other.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
#[serde(default)]
struct PresenceMsg {
    origin: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    tab: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    id: String,
}

/// Ends a subscription (Go's cancel func): when called, or when dropped.
pub(crate) struct Cancel {
    st: Arc<Mutex<State>>,
    id: usize,
}

impl Cancel {
    #[cfg(test)]
    pub(crate) fn cancel(self) {}
}

impl Drop for Cancel {
    fn drop(&mut self) {
        // dropping the sender closes the stream's channel
        self.st.lock().subs.remove(&self.id);
    }
}

fn new_epoch() -> String {
    let b = uuid::Uuid::new_v4();
    b.as_bytes()[..8]
        .iter()
        .map(|x| format!("{x:02x}"))
        .collect()
}

/// WithCommand returns a context carrying the id of the client command being served and who issued it: the events
/// published under it are stamped with both (and the bus carries the stamp, goap_platform::Stamp).
pub fn with_command(ctx: &Ctx, actor: &str, command: &str) -> Ctx {
    goap_platform::with_stamp(
        ctx,
        Stamp {
            actor: actor.into(),
            command: command.into(),
        },
    )
}

fn stamp_of(ctx: &Ctx) -> (String, String) {
    let st = goap_platform::stamp_of(ctx);
    let mut actor = st.actor;
    if actor.is_empty() {
        actor = authz::principal_from(ctx).subject;
    }
    (actor, st.command)
}

fn change_type(t: &str, subject: &str) -> String {
    if !t.is_empty() {
        return t.to_string();
    }
    let last = subject.rsplit('.').next().unwrap_or(subject);
    format!("change.{last}")
}

/// decode reads a message of the bus by its subject; None for what is not for the web or does not decode.
fn decode(subject: &str, data: &[u8]) -> Option<Publication> {
    if subject.starts_with("goap.process.") {
        serde_json::from_slice(data).ok().map(Publication::Process)
    } else if subject.starts_with("goap.node.") {
        serde_json::from_slice(data).ok().map(Publication::Node)
    } else if subject.starts_with("goap.baseline.") {
        serde_json::from_slice(data).ok().map(Publication::Baseline)
    } else if subject.starts_with("goap.change.") || subject.starts_with("goap.changed.") {
        serde_json::from_slice(data).ok().map(Publication::Change)
    } else if subject.starts_with("goap.registry.") {
        serde_json::from_slice(data).ok().map(Publication::Registry)
    } else {
        None
    }
}

impl Hub {
    /// NewHub returns a hub with a fresh epoch.
    pub fn new() -> Hub {
        Hub {
            authz: None,
            lookup: None,
            history: 0,
            presence_ttl: Duration::ZERO,
            relay: None,
            now: None,
            st: Arc::new(Mutex::new(State {
                epoch: new_epoch(),
                seq: 0,
                ring: VecDeque::new(),
                subs: HashMap::new(),
                next: 0,
                procs: HashMap::new(),
                personal: HashMap::new(),
                tabs: HashMap::new(),
            })),
        }
    }

    pub(crate) fn now(&self) -> DateTime<Utc> {
        match &self.now {
            Some(f) => f(),
            None => Utc::now(),
        }
    }

    fn history(&self) -> usize {
        if self.history > 0 {
            self.history
        } else {
            DEFAULT_HISTORY
        }
    }

    fn presence_ttl(&self) -> Duration {
        if self.presence_ttl > Duration::ZERO {
            self.presence_ttl
        } else {
            DEFAULT_PRESENCE_TTL
        }
    }

    /// Epoch names this stream: a seq only means something within it.
    pub fn epoch(&self) -> String {
        self.st.lock().epoch.clone()
    }

    /// Seq is the number of the last event.
    pub fn seq(&self) -> u64 {
        self.st.lock().seq
    }

    /// Publish is the in-process way in (the engine's publisher, the graph's event sink): the publication is
    /// stamped with the actor and command of ctx.
    pub fn publish(&self, ctx: &Ctx, subject: &str, v: Publication) {
        let (actor, command) = stamp_of(ctx);
        self.accept(subject, v, actor, command);
    }

    /// Ingest takes a message of the bus (NATS) by its subject, with the stamp its publisher put on it.
    pub fn ingest(&self, subject: &str, data: &[u8], st: Stamp) {
        if subject.starts_with(PRESENCE_SUBJECT) {
            self.ingest_presence(subject, data);
            return;
        }
        if let Some(v) = decode(subject, data) {
            self.accept(subject, v, st.actor, st.command);
        }
    }

    /// accept turns a publication into an event; what is not for the web is dropped.
    fn accept(&self, subject: &str, v: Publication, actor: String, command: String) {
        let now = self.now();
        let mut ev = pb::Event {
            actor,
            command_id: command,
            ..Default::default()
        };
        let mut proc = String::new();
        let mut personal_to = String::new();
        let mut st = self.st.lock();
        match v {
            Publication::Process(m) => {
                ev.kind = "process".into();
                ev.r#type = format!("process.{}", m.event);
                if let Some(p) = &m.process {
                    ev.id = p.id.clone();
                    ev.project = p.project.clone();
                    ev.change_id = p.change_id.to_string();
                    ev.process = Some(goap_enginesvc::process_to_pb(p));
                    if st.procs.len() >= MAX_PROCESSES {
                        // bounded; a log line of a forgotten process is looked up again
                        st.procs.clear();
                    }
                    st.procs.insert(p.id.clone(), ProcScope::of(p));
                    proc = p.id.clone();
                } else if let Some(l) = &m.log {
                    ev.id = l.process_id.clone();
                    ev.log = Some(goap_enginesvc::log_to_pb(l));
                    ev.r#type = "process.log".into();
                    proc = l.process_id.clone();
                } else {
                    return;
                }
                ev.time = pbconv::time(&m.time);
            }
            Publication::Node(m) => {
                ev.kind = "node".into();
                ev.r#type = if m.deleted {
                    "node.deleted".into()
                } else {
                    "node.written".into()
                };
                ev.id = m.id.to_string();
                ev.namespace = m.namespace.clone();
                ev.branch = m.branch.clone();
                ev.change_id = m.change_id.to_string();
                ev.version = m.version;
                ev.time = pbconv::time(&m.time);
                personal_to = st
                    .personal
                    .get(m.change_id.as_str())
                    .cloned()
                    .unwrap_or_default();
            }
            Publication::Baseline(m) => {
                ev.kind = "baseline".into();
                ev.r#type = "baseline.advanced".into();
                ev.id = m.id.to_string();
                ev.branch = m.branch.clone();
                ev.time = pbconv::time(&m.time);
            }
            Publication::Change(m) => {
                let c = &m.change;
                ev.kind = "change".into();
                ev.r#type = change_type(&m.r#type, subject);
                ev.id = c.id.to_string();
                ev.namespace = c.namespace.clone();
                ev.branch = c.branch.clone();
                ev.project = c.project_id.clone();
                ev.change_id = c.id.to_string();
                ev.time = pbconv::time(&now);
                if goap_access::is_personal(c) {
                    st.personal.insert(
                        c.id.to_string(),
                        goap_access::personal_subject_of(c).to_string(),
                    );
                }
                personal_to = st.personal.get(c.id.as_str()).cloned().unwrap_or_default();
                if ev.r#type == "change.purged" {
                    st.personal.remove(c.id.as_str());
                }
            }
            Publication::Registry(m) => {
                // goap.registry.<methodology|domain>.<event>
                let parts: Vec<&str> = subject.split('.').collect();
                if parts.len() < 4 {
                    return;
                }
                ev.kind = parts[2].into();
                ev.r#type = parts[2..].join(".");
                ev.id = m.get("name").cloned().unwrap_or_default();
                ev.namespace = ev.id.clone();
                ev.label = m.get("version").cloned().unwrap_or_default();
                ev.time = pbconv::time(&now);
            }
        }
        // emit numbers an event, keeps it for resuming, and delivers it
        st.seq += 1;
        ev.epoch = st.epoch.clone();
        ev.seq = st.seq;
        let r = Arc::new(Record {
            ev,
            proc,
            personal_to,
        });
        st.ring.push_back(r.clone());
        let n = self.history();
        while st.ring.len() > n {
            st.ring.pop_front();
        }
        st.deliver(r);
    }

    fn relay(&self, subject: &str, mut m: PresenceMsg) {
        if let Some(relay) = &self.relay {
            m.origin = self.epoch();
            if let Ok(v) = serde_json::to_value(&m) {
                relay(subject, &v);
            }
        }
    }

    /// Heartbeat says where a tab of a subject, connected to this replica, looks.
    pub fn heartbeat(&self, subject: &str, tab_id: &str, kind: &str, id: &str) {
        if self.beat(subject, tab_id, kind, id, true) {
            self.relay(
                &format!("{PRESENCE_SUBJECT}beat"),
                PresenceMsg {
                    subject: subject.into(),
                    tab: tab_id.into(),
                    kind: kind.into(),
                    id: id.into(),
                    ..Default::default()
                },
            );
        }
    }

    /// beat records a tab (a local one, or one relayed by another replica); it reports whether it is valid.
    fn beat(&self, subject: &str, tab_id: &str, kind: &str, id: &str, local: bool) -> bool {
        if subject.is_empty() || tab_id.is_empty() {
            return false;
        }
        let now = self.now();
        let key = format!("{subject}/{tab_id}");
        let mut st = self.st.lock();
        let mut typ = "";
        if !st.tabs.contains_key(&key) {
            typ = "presence.joined";
            st.tabs.insert(
                key.clone(),
                Tab {
                    subject: subject.into(),
                    id: tab_id.into(),
                    kind: kind.into(),
                    loc: id.into(),
                    since: now,
                    seen: now,
                    local: false,
                },
            );
        }
        let Some(t) = st.tabs.get_mut(&key) else {
            return true;
        };
        if typ.is_empty() && (t.kind != kind || t.loc != id) {
            typ = "presence.moved";
            t.since = now;
        }
        t.kind = kind.into();
        t.loc = id.into();
        t.seen = now;
        t.local = t.local || local;
        let t = t.clone();
        if !typ.is_empty() {
            st.presence_event(typ, &t, now);
        }
        true
    }

    /// Leave drops a tab of this replica.
    pub fn leave(&self, subject: &str, tab_id: &str) {
        if self.drop_tab(subject, tab_id) {
            self.relay(
                &format!("{PRESENCE_SUBJECT}leave"),
                PresenceMsg {
                    subject: subject.into(),
                    tab: tab_id.into(),
                    ..Default::default()
                },
            );
        }
    }

    fn drop_tab(&self, subject: &str, tab_id: &str) -> bool {
        let key = format!("{subject}/{tab_id}");
        let now = self.now();
        let mut st = self.st.lock();
        match st.tabs.remove(&key) {
            Some(t) => {
                st.presence_event("presence.left", &t, now);
                true
            }
            None => false,
        }
    }

    /// Hello asks the other replicas to tell the tabs they hold: a replica that just started learns the picture.
    pub fn hello(&self) {
        self.relay(&format!("{PRESENCE_SUBJECT}hello"), PresenceMsg::default());
    }

    /// ingest_presence applies what another replica relayed (its own messages come back too: ignored).
    fn ingest_presence(&self, subject: &str, data: &[u8]) {
        let Ok(m) = serde_json::from_slice::<PresenceMsg>(data) else {
            return;
        };
        if m.origin == self.epoch() {
            return;
        }
        match subject.strip_prefix(PRESENCE_SUBJECT).unwrap_or(subject) {
            "beat" => {
                self.beat(&m.subject, &m.tab, &m.kind, &m.id, false);
            }
            "leave" => {
                self.drop_tab(&m.subject, &m.tab);
            }
            "hello" => {
                let mine: Vec<PresenceMsg> = {
                    let st = self.st.lock();
                    st.tabs
                        .values()
                        .filter(|t| t.local)
                        .map(|t| PresenceMsg {
                            subject: t.subject.clone(),
                            tab: t.id.clone(),
                            kind: t.kind.clone(),
                            id: t.loc.clone(),
                            ..Default::default()
                        })
                        .collect()
                };
                for b in mine {
                    self.relay(&format!("{PRESENCE_SUBJECT}beat"), b);
                }
            }
            _ => {}
        }
    }

    /// Expire drops the tabs with no heartbeat for the TTL.
    pub fn expire(&self) {
        let now = self.now();
        let cut = now - chrono::TimeDelta::from_std(self.presence_ttl()).unwrap_or_default();
        let mut st = self.st.lock();
        let gone: Vec<String> = st
            .tabs
            .iter()
            .filter(|(_, t)| t.seen < cut)
            .map(|(k, _)| k.clone())
            .collect();
        for key in gone {
            if let Some(t) = st.tabs.remove(&key) {
                st.presence_event("presence.left", &t, now);
            }
        }
    }

    /// Run expires the presence of closed tabs until ctx is done.
    pub async fn run(&self, ctx: &Ctx) {
        let mut tick = tokio::time::interval(self.presence_ttl() / 3);
        tick.tick().await; // the first tick is at once
        loop {
            tokio::select! {
                _ = ctx.done() => return,
                _ = tick.tick() => self.expire(),
            }
        }
    }

    /// subscribe registers a stream and returns what it missed since (epoch, after), or says it must resync.
    pub(crate) fn subscribe(
        &self,
        epoch: &str,
        after: u64,
    ) -> (Subscriber, Vec<Arc<Record>>, bool, Cancel) {
        let mut st = self.st.lock();
        let (tx, rx) = mpsc::channel(SUBSCRIBER_BUFFER);
        let overflow = Arc::new(AtomicBool::new(false));
        let id = st.next;
        st.next += 1;
        st.subs.insert(
            id,
            Sub {
                tx,
                overflow: overflow.clone(),
            },
        );
        let mut backlog = Vec::new();
        let mut resync = false;
        if epoch.is_empty() {
            // a first connection: nothing to catch up
        } else if epoch != st.epoch || after > st.seq {
            resync = true;
        } else {
            let oldest = st.ring.front().map(|r| r.ev.seq).unwrap_or(st.seq + 1);
            if after + 1 < oldest {
                resync = true;
            } else {
                let i = st.ring.partition_point(|r| r.ev.seq <= after);
                backlog.extend(st.ring.iter().skip(i).cloned());
            }
        }
        (
            Subscriber { rx, overflow },
            backlog,
            resync,
            Cancel {
                st: self.st.clone(),
                id,
            },
        )
    }

    /// snapshot is the presence a stream starts from, as the caller may see it.
    pub(crate) fn snapshot(&self, v: &Viewer) -> pb::Event {
        let now = self.now();
        let st = self.st.lock();
        let mut ev = pb::Event {
            epoch: st.epoch.clone(),
            seq: st.seq,
            r#type: "presence.snapshot".into(),
            kind: "presence".into(),
            time: pbconv::time(&now),
            ..Default::default()
        };
        for t in st.tabs.values() {
            let p = st.personal_of(t);
            if p.is_empty() || p == v.who.subject {
                ev.presence.push(t.pb());
            }
        }
        ev.presence.sort_by(|a, b| {
            format!("{}{}", a.subject, a.tab_id).cmp(&format!("{}{}", b.subject, b.tab_id))
        });
        ev
    }

    pub(crate) fn visibility(&self, who: authz::Principal) -> Viewer {
        Viewer {
            who,
            allowed: HashMap::new(),
        }
    }

    async fn can_read_process(&self, ctx: &Ctx, who: &authz::Principal, id: &str) -> bool {
        let known = self.st.lock().procs.get(id).cloned();
        let p = match known {
            Some(p) => p,
            None => {
                let found = match &self.lookup {
                    Some(lookup) => lookup(ctx.clone(), id.to_string()).await,
                    None => None,
                };
                match found {
                    Some(pr) => ProcScope::of(&pr),
                    None => return false,
                }
            }
        };
        authz::check(
            ctx,
            self.authz.as_deref(),
            &authz::Request {
                subject: who.clone(),
                action: "read".into(),
                resource: authz::Resource {
                    r#type: "process".into(),
                    id: p.id,
                    name: p.methodology,
                    org: p.org,
                    owner: p.owner,
                    project_id: p.project,
                    ..Default::default()
                },
            },
        )
        .await
        .is_ok()
    }
}

/// viewer decides what a caller may see, remembering the decisions of its stream.
pub(crate) struct Viewer {
    pub(crate) who: authz::Principal,
    allowed: HashMap<String, bool>,
}

impl Viewer {
    /// sees reports whether the caller may read an event: the events of a personal change are its subject's alone,
    /// those of a process are decided by the process's read rule, the others are open to every signed-in caller
    /// (the graph's reads are, a client refetches through the services, which decide again).
    pub(crate) async fn sees(&mut self, h: &Hub, ctx: &Ctx, r: &Record) -> bool {
        if !r.personal_to.is_empty() && r.personal_to != self.who.subject {
            return false;
        }
        if r.proc.is_empty() {
            return true;
        }
        if let Some(ok) = self.allowed.get(&r.proc) {
            return *ok;
        }
        let ok = h.can_read_process(ctx, &self.who, &r.proc).await;
        self.allowed.insert(r.proc.clone(), ok);
        ok
    }
}

/// The hub as the engine's publisher (the in-process way in): an engine process event, a graph event or a
/// registry message, else its JSON read by the subject the way the bus's are.
#[async_trait]
impl goap_engine::Publisher for Hub {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Payload<'_>) -> goap_engine::Result<()> {
        let p = if let Some(e) = v.downcast::<goap_engine::ProcessEvent>() {
            Some(Publication::Process(e.into()))
        } else if let Some(e) = v.downcast::<NodeEvent>() {
            Some(Publication::Node(e.clone()))
        } else if let Some(e) = v.downcast::<BaselineEvent>() {
            Some(Publication::Baseline(e.clone()))
        } else if let Some(e) = v.downcast::<ChangeEvent>() {
            Some(Publication::Change(e.clone()))
        } else {
            serde_json::to_vec(&v.json())
                .ok()
                .and_then(|data| decode(subject, &data))
        };
        if let Some(p) = p {
            Hub::publish(self, ctx, subject, p);
        }
        Ok(())
    }
}
