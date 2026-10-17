use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use chrono::Utc;
use goap_authz::{self as authz, Authorizer, Principal};
use goap_connect::{Ctx, Request};
use goap_domain::{Change, ChangeEvent, NodeEvent};
use goap_engine::{LogLine, Process};
use goap_gen::events::v1 as pb;
use goap_gen::events::v1::EventService;
use goap_identity::Extractor;
use goap_platform::Stamp;
use parking_lot::Mutex;

use super::hub::{DEFAULT_PRESENCE_TTL, Hub, ProcessEvent, Publication, Record, SUBSCRIBER_BUFFER};
use super::watch::Handler;
use super::with_command;

fn node(id: &str) -> NodeEvent {
    NodeEvent {
        id: id.into(),
        version: 1,
        branch: "main".into(),
        namespace: "alm".into(),
        r#type: "alm@Requirement".into(),
        time: Utc::now(),
        ..Default::default()
    }
}

fn publish(h: &Hub, subject: &str, v: Publication) {
    h.publish(&Ctx::background(), subject, v);
}

fn seqs(rs: &[Arc<Record>]) -> Vec<u64> {
    rs.iter().map(|r| r.ev.seq).collect()
}

#[test]
fn resume_from_seq() {
    let h = Hub::new();
    for id in ["a", "b", "c"] {
        publish(
            &h,
            &format!("goap.node.alm.x.{id}.written"),
            Publication::Node(node(id)),
        );
    }
    let (_s, backlog, resync, cancel) = h.subscribe(&h.epoch(), 1);
    assert!(
        !resync,
        "a stream inside the history must resume, not resync"
    );
    assert_eq!(seqs(&backlog), vec![2, 3]);
    cancel.cancel();
}

#[test]
fn resync_on_other_epoch_or_gap() {
    let mut h = Hub::new();
    h.history = 2;
    for id in ["a", "b", "c", "d"] {
        publish(
            &h,
            &format!("goap.node.alm.x.{id}.written"),
            Publication::Node(node(id)),
        );
    }
    let epoch = h.epoch();
    let cases: [(&str, &str, u64, bool); 5] = [
        ("first connection", "", 0, false),
        ("other epoch", "elsewhere", 3, true),
        ("history gap", &epoch, 1, true),
        ("in history", &epoch, 2, false),
        ("from the future", &epoch, 99, true),
    ];
    for (name, e, after, want) in cases {
        let (_s, _b, resync, cancel) = h.subscribe(e, after);
        cancel.cancel();
        assert_eq!(resync, want, "{name}: resync");
    }
}

struct DenyOthers;

#[async_trait]
impl Authorizer for DenyOthers {
    async fn authorize(&self, _: &Ctx, r: &authz::Request) -> Result<bool, authz::Error> {
        Ok(r.subject.subject == r.resource.owner)
    }
}

#[tokio::test]
async fn visibility() {
    let mut h = Hub::new();
    h.authz = Some(Arc::new(DenyOthers));
    let ctx = Ctx::background();
    publish(
        &h,
        "goap.process.p1.started",
        Publication::Process(ProcessEvent {
            event: "started".into(),
            time: Utc::now(),
            process: Some(Process {
                id: "p1".into(),
                methodology: "sdlc".into(),
                initiator: Principal {
                    subject: "alice".into(),
                    ..Default::default()
                },
                ..Default::default()
            }),
            log: None,
        }),
    );
    publish(
        &h,
        "goap.process.p1.log",
        Publication::Process(ProcessEvent {
            event: "log".into(),
            time: Utc::now(),
            process: None,
            log: Some(LogLine {
                process_id: "p1".into(),
                message: "hi".into(),
                ..Default::default()
            }),
        }),
    );
    publish(
        &h,
        "goap.change.C1.created",
        Publication::Change(ChangeEvent {
            r#type: "change.created".into(),
            change: Change {
                id: "C1".into(),
                owner_org: "USR:alice".into(),
                ..Default::default()
            },
            ..Default::default()
        }),
    );
    let mut n1 = node("n1");
    n1.change_id = "C1".into();
    publish(&h, "goap.node.alm.x.n1.written", Publication::Node(n1));
    publish(
        &h,
        "goap.node.alm.x.n2.written",
        Publication::Node(node("n2")),
    );

    let ring: Vec<Arc<Record>> = h.st.lock().ring.iter().cloned().collect();
    let count = async |who: &str| {
        let mut v = h.visibility(Principal {
            subject: who.into(),
            ..Default::default()
        });
        let mut n = 0;
        for r in &ring {
            if v.sees(&h, &ctx, r).await {
                n += 1;
            }
        }
        n
    };
    assert_eq!(count("alice").await, 5, "alice sees all 5");
    // bob: not the process (2 events), not the personal change nor its node (2), only the public node
    assert_eq!(count("bob").await, 1, "bob sees 1");
}

#[test]
fn command_stamp() {
    let h = Hub::new();
    let ctx = with_command(&Ctx::background(), "alice", "cmd-1");
    h.publish(
        &ctx,
        "goap.node.alm.x.n.written",
        Publication::Node(node("n")),
    );
    let st = h.st.lock();
    let ev = &st.ring[0].ev;
    assert!(
        ev.actor == "alice" && ev.command_id == "cmd-1",
        "stamp = {:?}/{:?}",
        ev.actor,
        ev.command_id
    );
}

#[test]
fn presence_lifecycle() {
    let mut h = Hub::new();
    let now = Arc::new(Mutex::new(Utc::now()));
    let clock = now.clone();
    h.now = Some(Arc::new(move || *clock.lock()));
    let (mut s, _, _, cancel) = h.subscribe("", 0);
    h.heartbeat("alice", "t1", "node", "N1");
    h.heartbeat("alice", "t1", "node", "N1"); // nothing new
    h.heartbeat("alice", "t1", "change", "C1");
    h.heartbeat("bob", "t1", "node", "N1");
    let mut types = Vec::new();
    while let Ok(r) = s.rx.try_recv() {
        types.push(r.ev.r#type.clone());
    }
    assert_eq!(
        types,
        vec!["presence.joined", "presence.moved", "presence.joined"]
    );
    let bob = h.visibility(Principal {
        subject: "bob".into(),
        ..Default::default()
    });
    assert_eq!(h.snapshot(&bob).presence.len(), 2, "snapshot tabs");
    // tabs expire without heartbeat, a left tab goes at once
    let half = chrono::TimeDelta::from_std(DEFAULT_PRESENCE_TTL / 2).unwrap();
    let advance = |d: chrono::TimeDelta| {
        let mut n = now.lock();
        *n += d;
    };
    advance(half);
    h.heartbeat("bob", "t1", "node", "N1");
    advance(half + chrono::TimeDelta::seconds(1));
    h.expire();
    assert_eq!(h.st.lock().tabs.len(), 1, "after expiry, bob's only");
    h.leave("bob", "t1");
    assert!(
        h.st.lock().tabs.is_empty(),
        "a tab that left must go at once"
    );
    cancel.cancel();
}

#[test]
fn slow_subscriber_is_cut() {
    let h = Hub::new();
    let (s, _, _, cancel) = h.subscribe("", 0);
    for _ in 0..SUBSCRIBER_BUFFER + 1 {
        publish(
            &h,
            "goap.node.alm.x.n.written",
            Publication::Node(node("n")),
        );
    }
    assert!(
        s.overflow.load(std::sync::atomic::Ordering::SeqCst),
        "a subscriber that cannot keep up must be cut"
    );
    cancel.cancel();
}

#[tokio::test]
async fn watch_stream() {
    use futures::StreamExt;
    let h = Arc::new(Hub::new());
    let handler = Handler {
        hub: h.clone(),
        idle: Duration::from_millis(20),
        identity: Extractor {
            default: Some(Principal {
                subject: "alice".into(),
                ..Default::default()
            }),
        },
    };
    let app = pb::event_service_router(Arc::new(handler), goap_connect::Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    let server = tokio::spawn(async move {
        let _ = goap_connect::axum::serve(ln, app).await;
    });
    let client = pb::EventServiceClient::connect(&url);
    let ctx = Ctx::background();
    let run = async {
        let mut st = client
            .watch(&ctx, Request::new(pb::WatchRequest::default()))
            .await
            .unwrap()
            .into_inner();
        let mut next = async || -> pb::Event {
            match st.next().await {
                Some(Ok(ev)) => ev,
                other => panic!("stream ended: {other:?}"),
            }
        };
        let ev = next().await;
        assert_eq!(ev.r#type, "presence.snapshot", "first event");
        publish(
            &h,
            "goap.node.alm.x.n.written",
            Publication::Node(node("n")),
        );
        loop {
            let ev = next().await;
            if ev.r#type == "node.written" {
                assert!(ev.seq == 1 && ev.epoch == h.epoch(), "event = {ev:?}");
                break;
            }
            assert_eq!(ev.r#type, "heartbeat", "unexpected");
        }
        // a tab announces itself through the same service
        client
            .heartbeat(
                &ctx,
                Request::new(pb::HeartbeatRequest {
                    tab_id: "t".into(),
                    kind: "node".into(),
                    id: "n".into(),
                }),
            )
            .await
            .unwrap();
        loop {
            let ev = next().await;
            if ev.r#type == "presence.joined" {
                assert_eq!(
                    ev.presence[0].subject, "alice",
                    "presence = {:?}",
                    ev.presence
                );
                return;
            }
        }
    };
    tokio::time::timeout(Duration::from_secs(5), run)
        .await
        .expect("watch stream timed out");
    server.abort();
}

#[test]
fn presence_relay_between_replicas() {
    type Hubs = Arc<Mutex<Vec<Arc<Hub>>>>;
    let hubs: Hubs = Arc::new(Mutex::new(Vec::new()));
    let join = |mut h: Hub| -> Arc<Hub> {
        let all = hubs.clone();
        // what NATS does: every replica gets it (its own too)
        h.relay = Some(Arc::new(move |subject: &str, v: &serde_json::Value| {
            let data = serde_json::to_vec(v).unwrap();
            let list: Vec<Arc<Hub>> = all.lock().clone();
            for o in list {
                o.ingest(subject, &data, Stamp::default());
            }
        }));
        let h = Arc::new(h);
        hubs.lock().push(h.clone());
        h
    };
    let a = join(Hub::new());
    let b = join(Hub::new());
    let (mut sb, _, _, cancel) = b.subscribe("", 0);
    // alice is connected to replica a; replica b's viewers see her
    a.heartbeat("alice", "t1", "node", "N1");
    let bob = b.visibility(Principal {
        subject: "bob".into(),
        ..Default::default()
    });
    assert_eq!(
        b.snapshot(&bob).presence.len(),
        1,
        "replica b sees alice's tab"
    );
    let ev = sb.rx.try_recv().unwrap();
    assert_eq!(ev.ev.r#type, "presence.joined");
    let local = |h: &Hub| h.st.lock().tabs.get("alice/t1").map(|t| t.local);
    assert!(
        local(&a) == Some(true) && local(&b) == Some(false),
        "only the replica she is connected to holds her tab as local"
    );
    // a replica that just started learns the tabs of the others
    let c = join(Hub::new());
    c.hello();
    assert_eq!(
        c.st.lock().tabs.len(),
        1,
        "a replica that just started learns 1 tab"
    );
    a.leave("alice", "t1");
    let total = a.st.lock().tabs.len() + b.st.lock().tabs.len() + c.st.lock().tabs.len();
    assert_eq!(total, 0, "a tab that left goes from every replica");
    cancel.cancel();
}

#[test]
fn stamp_travels_on_the_bus() {
    let h = Hub::new();
    let data = serde_json::to_vec(&node("n")).unwrap();
    h.ingest(
        "goap.node.alm.x.n.written",
        &data,
        Stamp {
            actor: "alice".into(),
            command: "cmd-9".into(),
        },
    );
    let st = h.st.lock();
    let ev = &st.ring[0].ev;
    assert!(
        ev.actor == "alice" && ev.command_id == "cmd-9",
        "stamp = {:?}/{:?}",
        ev.actor,
        ev.command_id
    );
}

#[tokio::test]
async fn engine_publisher_way_in() {
    // the hub as the engine's publisher: a typed process event, and JSON read by the subject
    use goap_engine::{Payload, Publisher};
    let h = Hub::new();
    let ctx = Ctx::background();
    let ev = goap_engine::ProcessEvent {
        event: "started".into(),
        process: Some(Process {
            id: "p1".into(),
            ..Default::default()
        }),
        step: None,
        log: None,
        time: Utc::now(),
    };
    Publisher::publish(&h, &ctx, "goap.process.p1.started", &Payload::new(&ev))
        .await
        .unwrap();
    let reg: HashMap<String, String> = HashMap::from([
        ("name".into(), "sdlc".into()),
        ("version".into(), "1.0.0".into()),
    ]);
    Publisher::publish(
        &h,
        &ctx,
        "goap.registry.methodology.published",
        &Payload::new(&reg),
    )
    .await
    .unwrap();
    let st = h.st.lock();
    assert_eq!(st.ring.len(), 2);
    assert_eq!(st.ring[0].ev.r#type, "process.started");
    assert_eq!(st.ring[0].proc, "p1");
    let r = &st.ring[1].ev;
    assert!(
        r.kind == "methodology"
            && r.r#type == "methodology.published"
            && r.id == "sdlc"
            && r.label == "1.0.0",
        "registry event = {r:?}"
    );
}
