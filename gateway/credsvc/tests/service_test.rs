use std::{sync::Arc, time::Duration};

use goap_connect::Ctx;
use goap_credsvc::{MIGRATIONS, MemoryStore, SQLITE_MIGRATIONS, Service, SqlStore, Store};

struct Stores {
    all: Vec<(&'static str, Arc<dyn Store>)>,
    _pg: Option<goap_pgtest::PgTest>,
}

async fn stores(name: &str) -> Stores {
    let ctx = Ctx::background();
    let dir = std::env::temp_dir().join(format!(
        "goap-credsvc-{name}-{}-{}",
        std::process::id(),
        chrono::Utc::now().timestamp_nanos_opt().unwrap_or_default()
    ));
    let db = goap_platform::open_sqlite(&ctx, dir.join("c.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "credentials", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    let mut all: Vec<(&'static str, Arc<dyn Store>)> = vec![
        ("memory", Arc::new(MemoryStore::new())),
        ("sqlite", Arc::new(SqlStore::Sqlite(db))),
    ];
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
    Stores { all, _pg: pg }
}

/// A subject registers once, logs in with the right password and is refused with a wrong one; a duplicate
/// registration is refused; changing the password takes effect immediately.
#[tokio::test]
async fn service_register_verify_set_password() {
    let st = stores("register").await;
    for (name, store) in &st.all {
        let ctx = Ctx::background();
        let s = Service::new(store.clone());
        s.register(&ctx, "alice", "correct horse").await.unwrap();
        let e = s
            .register(&ctx, "alice", "another password")
            .await
            .unwrap_err();
        assert!(
            e.is_exists(),
            "{name}: duplicate register = {e}, want ErrExists"
        );
        assert!(
            s.verify(&ctx, "alice", "correct horse").await.unwrap(),
            "{name}"
        );
        assert!(
            !s.verify(&ctx, "alice", "wrong password").await.unwrap(),
            "{name}"
        );
        assert!(!s.verify(&ctx, "bob", "whatever").await.unwrap(), "{name}");
        assert!(s.exists(&ctx, "alice").await.unwrap(), "{name}");
        assert!(!s.exists(&ctx, "bob").await.unwrap(), "{name}");

        s.set_password(&ctx, "alice", "new password").await.unwrap();
        assert!(
            !s.verify(&ctx, "alice", "correct horse").await.unwrap(),
            "{name}: old password still works"
        );
        assert!(
            s.verify(&ctx, "alice", "new password").await.unwrap(),
            "{name}"
        );
        let e = s
            .set_password(&ctx, "carol", "whatever1")
            .await
            .unwrap_err();
        assert!(
            e.is_not_found(),
            "{name}: set password of unknown subject = {e}"
        );
    }
}

#[tokio::test]
async fn service_validation() {
    let s = Service::new(Arc::new(MemoryStore::new()));
    let ctx = Ctx::background();
    let e = s.register(&ctx, "", "whatever1").await.unwrap_err();
    assert!(e.is_invalid(), "empty subject = {e}");
    let e = s.register(&ctx, "dave", "short").await.unwrap_err();
    assert!(e.is_invalid(), "short password = {e}");
}

/// A session accepts its tokens until it is ended, expires, or its subject changes their password; signing out
/// everywhere ends every session of the subject, and only theirs (ADR 0045).
#[tokio::test]
async fn sessions() {
    let st = stores("sessions").await;
    for (name, store) in &st.all {
        let ctx = Ctx::background();
        let s = Service::new(store.clone());
        s.register(&ctx, "alice", "correct horse").await.unwrap();
        let active =
            async |id: &str, subject: &str| s.session_active(&ctx, id, subject).await.unwrap();
        let start =
            async |subject: &str, d: Duration| s.start_session(&ctx, subject, d).await.unwrap();
        let hour = Duration::from_secs(3600);
        let (a1, a2, b) = (
            start("alice", hour).await,
            start("alice", hour).await,
            start("bob", hour).await,
        );
        assert!(
            a1 != a2
                && active(&a1, "alice").await
                && active(&a2, "alice").await
                && active(&b, "bob").await,
            "{name}: new sessions must be distinct and active"
        );
        assert!(
            !active(&a1, "bob").await
                && !active("unknown", "alice").await
                && !active("", "alice").await,
            "{name}: a session is someone's, and an unknown one is not active"
        );
        // signing out ends that session only
        s.end_session(&ctx, &a1).await.unwrap();
        assert!(
            !active(&a1, "alice").await && active(&a2, "alice").await,
            "{name}: signing out ends that session only"
        );
        s.end_session(&ctx, &a1).await.unwrap();
        // a new password ends every session of its subject, not the others'
        s.set_password(&ctx, "alice", "battery staple")
            .await
            .unwrap();
        assert!(
            !active(&a2, "alice").await && active(&b, "bob").await,
            "{name}: a new password ends the subject's sessions only"
        );
        // everywhere
        let b2 = start("bob", hour).await;
        s.end_sessions(&ctx, "bob").await.unwrap();
        assert!(
            !active(&b, "bob").await && !active(&b2, "bob").await,
            "{name}: signing out everywhere ends every session"
        );
        // expiry
        let short = start("alice", Duration::from_millis(50)).await;
        tokio::time::sleep(Duration::from_millis(80)).await;
        assert!(
            !active(&short, "alice").await,
            "{name}: an expired session is not active"
        );
    }
}
