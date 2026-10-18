//! The starting points served by the engine service over a real graph (ADR 0125 §5).

use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::{self as authz, Authorizer, Principal};
use goap_connect::{Code, ConnectError, Request};
use goap_ctx::Ctx;
use goap_engine::{Engine, HumanExecutor, MemoryStore, StaticMethodologies};
use goap_enginesvc::Handler;
use goap_gen::engine::v1::{self as pb, EngineService};
use goap_graph::{Graph, Memory, NewChange};
use goap_graphtest as graphtest;
use goap_methodology::{self as methodology, Compiled};

const POINTS_YAML: &str = r#"
name: pts
version: 1.0.0
namespace: alm
goal: ship
conditions:
  - {name: drafted, expr: 'artifacts.exists(a, a.target == "draft")'}
  - {name: shipped, expr: 'artifacts.exists(a, a.target == "ship")'}
actions:
  - {name: write, kind: human, effects: {drafted: true}}
  - {name: send, kind: human, pre: {drafted: true}, effects: {shipped: true}}
processes:
  - name: ship
    steps:
      - {name: draft, action: write, description: Write the draft}
      - {name: send, action: send}
"#;

/// DenySome refuses the "start" of the methodology named deny.
struct DenySome {
    deny: &'static str,
}

#[async_trait]
impl Authorizer for DenySome {
    async fn authorize(&self, _ctx: &Ctx, r: &authz::Request) -> Result<bool, authz::Error> {
        Ok(!(r.action == "start" && r.resource.name == self.deny))
    }
}

/// c compiles the methodology of the points.
fn c() -> Compiled {
    let m = methodology::parse(POINTS_YAML.as_bytes()).unwrap();
    m.compile().unwrap()
}

async fn points_handler(a: Arc<dyn Authorizer>) -> (Handler, Arc<Graph>) {
    let c = c();
    let g = Arc::new(Graph::new(Arc::new(Memory::new())));
    let ctx = Ctx::background();
    for k in ["PROJ-A", "PROJ-B"] {
        graphtest::project(&ctx, &g, k, k).await.unwrap();
    }
    graphtest::unit(&ctx, &g, &goap_access::personal_unit("alice"), "alice")
        .await
        .unwrap();
    let mut ms = StaticMethodologies::new();
    ms.0.insert("pts".into(), Arc::new(c));
    let mut e = Engine::new(g.clone(), Arc::new(ms), Arc::new(MemoryStore::new()));
    e.executors
        .insert(methodology::KIND_HUMAN.into(), Arc::new(HumanExecutor));
    let mut h = Handler::new(Arc::new(e));
    h.authz = Some(a);
    (h, g)
}

async fn call(
    h: &Handler,
    ctx: &Ctx,
    id: &str,
) -> Result<pb::ListStartingPointsResponse, ConnectError> {
    let mut req = Request::new(pb::ListStartingPointsRequest {
        change_id: id.to_string(),
        ..Default::default()
    });
    goap_identity::set_headers(&authz::principal_from(ctx), &mut req.headers);
    Ok(h.list_starting_points(ctx, req).await?.msg)
}

fn new_change(title: &str, owner_org: String) -> NewChange {
    NewChange {
        title: title.into(),
        methodology: "pts".into(),
        goal: "ship".into(),
        project_id: "PROJ-B".into(),
        owner_org,
        ..Default::default()
    }
}

#[tokio::test]
async fn list_starting_points() {
    let ctx = Ctx::background();
    let (h, g) = points_handler(Arc::new(DenySome { deny: "nope" })).await;
    let ch = g
        .create_change(&ctx, new_change("t", String::new()))
        .await
        .unwrap();
    let who = authz::with_principal(
        &ctx,
        Principal {
            subject: "bob".into(),
            project: "PROJ-A".into(),
            ..Default::default()
        },
    );
    let out = call(&h, &who, ch.id.as_str()).await.unwrap();
    let ok = out.points.len() == 1
        && {
            let p = &out.points[0];
            let l = p.launch.clone().unwrap_or_default();
            p.id == "ship/draft"
                && p.may_run
                && l.change_id == ch.id.as_str()
                && l.agent == "ship"
                && l.goal == "ship/draft"
        }
        && out.blocked_count == 1
        && out.goal == "ship";
    assert!(ok, "{out:?}");
    // the binding names the version whose view says what the step is (ADR 0126 step 5)
    let view = goap_registrysvc::methodology_view_to_pb(&c().view());
    let read = goap_startingpoint::compose(Some(&view), &out);
    assert!(
        out.version == "1.0.0"
            && read.points[0].name == "draft"
            && read.points[0].description == "Write the draft"
            && read.points[0].produces == ["drafted"]
            && read.points[0].process == "ship",
        "composed {read:?}"
    );

    // the "start" permission is checked on the methodology, on the project of the change
    let (h2, g2) = points_handler(Arc::new(DenySome { deny: "pts" })).await;
    let ch2 = g2
        .create_change(&ctx, new_change("t", String::new()))
        .await
        .unwrap();
    match call(&h2, &who, ch2.id.as_str()).await {
        Err(e) if e.code() == Code::PermissionDenied => {}
        r => panic!("start refused: {r:?}"),
    }

    // a personal change is its subject's only
    let pc = g
        .create_change(
            &ctx,
            new_change("mine", goap_access::personal_unit("alice")),
        )
        .await
        .unwrap();
    match call(&h, &who, pc.id.as_str()).await {
        Err(e) if e.code() == Code::NotFound => {}
        r => panic!("personal change of another: {r:?}"),
    }
    let alice = authz::with_principal(
        &ctx,
        Principal {
            subject: "alice".into(),
            ..Default::default()
        },
    );
    if let Err(e) = call(&h, &alice, pc.id.as_str()).await {
        panic!("personal change of its subject: {e:?}");
    }
}
