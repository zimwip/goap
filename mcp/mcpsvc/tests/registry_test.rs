use std::sync::Arc;
use std::time::Duration;

use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_gen::connector::v1::{ConnectorInfo, Operation};
use goap_mcpsvc::{MIGRATIONS, MemoryStore, SQLITE_MIGRATIONS, Service, SqlStore, Store};
use parking_lot::Mutex;

fn localfs_info() -> ConnectorInfo {
    ConnectorInfo {
        id: "localfs".into(),
        version: "1".into(),
        secret_names: vec!["token".into()],
        operations: ["list_dir", "read_file", "write_file"]
            .into_iter()
            .map(|n| Operation {
                name: n.into(),
                ..Default::default()
            })
            .collect(),
        ..Default::default()
    }
}

#[tokio::test]
async fn connector_registry_memory() {
    test_registry(Arc::new(MemoryStore::new())).await;
}

#[tokio::test]
async fn connector_registry_sqlite() {
    let ctx = Ctx::background();
    let dir = std::env::temp_dir().join(format!(
        "goap-mcpsvc-{}-{}",
        std::process::id(),
        Utc::now().timestamp_nanos_opt().unwrap_or_default()
    ));
    let db = goap_platform::open_sqlite(&ctx, dir.join("goap.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "mcp", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    test_registry(Arc::new(SqlStore::Sqlite(db.clone()))).await;
    db.close().await;
    let _ = std::fs::remove_dir_all(dir);
}

#[tokio::test]
async fn connector_registry_pg() {
    let Ok(dsn) = std::env::var(goap_pgtest::DSN_VAR) else {
        return;
    };
    if dsn.is_empty() {
        return;
    }
    let t = goap_pgtest::pool_with(&dsn, None).await;
    goap_platform::migrate(&Ctx::background(), t.pool(), &MIGRATIONS, ".")
        .await
        .unwrap();
    test_registry(Arc::new(SqlStore::Postgres(t.pool().clone()))).await;
}

async fn test_registry(st: Arc<dyn Store>) {
    let ctx = Ctx::background();
    // millisecond precision: what the SQL stores keep
    let now: Arc<Mutex<DateTime<Utc>>> = Arc::new(Mutex::new(
        DateTime::from_timestamp_millis(Utc::now().timestamp_millis()).unwrap(),
    ));
    let clock = now.clone();
    let mut svc = Service::new(st.clone());
    svc.now = Some(Arc::new(move || *clock.lock()));
    svc.lease = Duration::from_secs(60);
    let err = st.connector(&ctx, "localfs").await.unwrap_err();
    assert!(err.is_not_found(), "unknown connector = {err}");
    let bad = ConnectorInfo {
        id: "Bad Id".into(),
        ..Default::default()
    };
    let err = svc
        .register_connector(&ctx, Some(bad), "http://x")
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "bad id = {err}");
    svc.register_connector(&ctx, Some(localfs_info()), "http://localfs:8080")
        .await
        .unwrap();
    let cs = svc.connectors(&ctx).await.unwrap();
    assert!(
        cs.len() == 1
            && cs[0].live
            && cs[0].endpoint == "http://localfs:8080"
            && cs[0].info.operations.len() == 3,
        "connectors = {cs:?}"
    );
    assert_eq!(cs[0].info, localfs_info());
    assert_eq!(cs[0].last_seen, *now.lock());
    {
        let mut n = now.lock();
        *n += chrono::Duration::minutes(2);
    }
    let cs = svc.connectors(&ctx).await.unwrap();
    assert!(!cs[0].live, "expired registration is live");
    svc.register_connector(&ctx, Some(localfs_info()), "http://other:8080")
        .await
        .unwrap();
    let cs = svc.connectors(&ctx).await.unwrap();
    assert!(
        cs[0].live && cs[0].endpoint == "http://other:8080",
        "renewed registration = {:?}",
        cs[0]
    );
}
