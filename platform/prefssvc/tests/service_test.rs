use std::sync::Arc;

use goap_authz::Principal;
use goap_connect::{Code, Ctx, Request};
use goap_gen::preferences::v1::{self as pb, PreferencesService};
use goap_prefssvc::{
    Handler, MAX_DOCUMENT_BYTES, MIGRATIONS, MemoryStore, Prefs, SQLITE_MIGRATIONS, Service,
    SqlStore, Store,
};
use serde_json::json;

fn prefs(v: serde_json::Value) -> Prefs {
    v.as_object().unwrap().clone()
}

struct Stores {
    all: Vec<(&'static str, Arc<dyn Store>)>,
    _dir: std::path::PathBuf,
    _pg: Option<goap_pgtest::PgTest>,
}

async fn stores(name: &str) -> Stores {
    let ctx = Ctx::background();
    let dir = std::env::temp_dir().join(format!(
        "goap-prefssvc-{name}-{}-{}",
        std::process::id(),
        chrono::Utc::now().timestamp_nanos_opt().unwrap_or_default()
    ));
    let db = goap_platform::open_sqlite(&ctx, dir.join("p.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "preferences", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    let mut all: Vec<(&'static str, Arc<dyn Store>)> = vec![
        ("memory", Arc::new(MemoryStore::new())),
        ("sqlite", Arc::new(SqlStore::Sqlite(db))),
    ];
    // PostgreSQL when GOAP_TEST_PG_DSN is set
    let mut pg = None;
    if let Ok(dsn) = std::env::var(goap_pgtest::DSN_VAR)
        && !dsn.is_empty()
    {
        let t = goap_pgtest::pool_with(&dsn, None).await;
        goap_platform::migrate(&ctx, t.pool(), &MIGRATIONS, ".")
            .await
            .unwrap();
        all.push(("postgres", Arc::new(SqlStore::Postgres(t.pool().clone()))));
        pg = Some(t);
    }
    Stores {
        all,
        _dir: dir,
        _pg: pg,
    }
}

#[tokio::test]
async fn service() {
    let ctx = Ctx::background();
    let st = stores("service").await;
    for (name, store) in &st.all {
        let s = Service::new(store.clone());
        let p = s.get(&ctx, "alice").await.unwrap();
        assert!(p.is_empty(), "{name}: empty = {p:?}");
        s.set(
            &ctx,
            "alice",
            prefs(json!({"theme": "dark", "voiceEnabled": false})),
        )
        .await
        .unwrap();
        // a patch merges; null clears a key
        let p = s
            .set(
                &ctx,
                "alice",
                prefs(json!({"usagePeriod": "30d", "voiceEnabled": null})),
            )
            .await
            .unwrap();
        assert!(
            p["theme"] == "dark" && p["usagePeriod"] == "30d" && p.len() == 2,
            "{name}: merged = {p:?}"
        );
        // each user has their own
        assert!(s.get(&ctx, "bob").await.unwrap().is_empty(), "{name}: bob");
        // the document is opaque: any key and value of JSON is kept, the interface judges them
        let p = s
            .set(
                &ctx,
                "alice",
                prefs(json!({"anything": {"nested": [1, "x"]}, "theme": "neon"})),
            )
            .await
            .unwrap();
        assert_eq!(p["theme"], "neon", "{name}");
        s.set(
            &ctx,
            "alice",
            prefs(json!({"theme": "dark", "anything": null})),
        )
        .await
        .unwrap();
        // what is too large is refused and changes nothing (a value that is not JSON cannot be built in Rust)
        let bad = prefs(json!({"big": "x".repeat(MAX_DOCUMENT_BYTES)}));
        let err = s.set(&ctx, "alice", bad).await.unwrap_err();
        assert!(err.is_invalid(), "{name}: {err}");
        assert_eq!(
            s.get(&ctx, "alice").await.unwrap()["theme"],
            "dark",
            "{name}"
        );
        assert!(s.get(&ctx, "").await.unwrap_err().is_anonymous(), "{name}");
        s.reset(&ctx, "alice").await.unwrap();
        assert!(s.get(&ctx, "alice").await.unwrap().is_empty(), "{name}");
    }
}

fn as_user<T>(subject: &str, msg: T) -> Request<T> {
    let mut r = Request::new(msg);
    goap_identity::set_headers(
        &Principal {
            subject: subject.into(),
            ..Default::default()
        },
        &mut r.headers,
    );
    r
}

/// The service answers the caller about themselves only.
#[tokio::test]
async fn handler_is_per_caller() {
    let ctx = Ctx::background();
    let h = Handler::new(Arc::new(Service::new(Arc::new(MemoryStore::new()))));
    let app = pb::preferences_service_router(Arc::new(h), goap_connect::Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, app).await });
    let cl = pb::PreferencesServiceClient::connect(&url);

    let v = to_struct(json!({"theme": "light"}));
    cl.set_preferences(
        &ctx,
        as_user("alice", pb::SetPreferencesRequest { values: Some(v) }),
    )
    .await
    .unwrap();
    let r = cl
        .get_preferences(&ctx, as_user("alice", pb::GetPreferencesRequest {}))
        .await
        .unwrap();
    assert_eq!(
        serde_json::to_value(r.msg.values.unwrap()).unwrap()["theme"],
        "light"
    );
    let r = cl
        .get_preferences(&ctx, as_user("bob", pb::GetPreferencesRequest {}))
        .await
        .unwrap();
    assert!(r.msg.values.unwrap_or_default().fields.is_empty());
    let err = cl
        .get_preferences(&ctx, Request::new(pb::GetPreferencesRequest {}))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Unauthenticated, "anonymous: {err}");
    let bad = to_struct(json!({"big": "x".repeat(MAX_DOCUMENT_BYTES)}));
    let err = cl
        .set_preferences(
            &ctx,
            as_user("alice", pb::SetPreferencesRequest { values: Some(bad) }),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument, "invalid: {err}");
}

fn to_struct(v: serde_json::Value) -> pbjson_types::Struct {
    serde_json::from_value(v).unwrap()
}
