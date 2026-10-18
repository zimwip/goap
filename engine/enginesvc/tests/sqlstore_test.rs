use std::collections::BTreeMap;
use std::time::Duration;

use chrono::Utc;
use goap_ctx::Ctx;
use goap_engine::{
    Locker, OrphanFinder, Process, ProcessLogEntry, STATUS_RUNNING, STATUS_WAITING, Status, Store,
};
use goap_enginesvc::{MIGRATIONS, SQLITE_MIGRATIONS, SQLStore, SqlDb};
use serde_json::json;

/// The process store in each dialect: SQLite always, PostgreSQL with GOAP_TEST_PG_DSN. Each opens the store of a
/// replica, by its holder, on one database.
struct Stores {
    all: Vec<(&'static str, SqlDb)>,
    dir: std::path::PathBuf,
    _pg: Option<goap_pgtest::PgTest>,
}

impl Drop for Stores {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

impl Stores {
    fn open(db: &SqlDb, holder: &str) -> SQLStore {
        SQLStore {
            db: db.clone(),
            holder: holder.to_string(),
        }
    }
}

async fn stores(name: &str) -> Stores {
    let ctx = Ctx::background();
    let dir = std::env::temp_dir().join(format!(
        "goap-enginesvc-{name}-{}-{}",
        std::process::id(),
        Utc::now().timestamp_nanos_opt().unwrap_or_default()
    ));
    std::fs::create_dir_all(&dir).unwrap();
    let db = goap_platform::open_sqlite(&ctx, dir.join("goap.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "engine", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    let mut all = vec![("sqlite", SqlDb::Sqlite(db))];
    let mut pg = None;
    if let Ok(dsn) = std::env::var(goap_pgtest::DSN_VAR)
        && !dsn.is_empty()
    {
        let t = goap_pgtest::pool_with(&dsn, None).await;
        goap_platform::migrate(&ctx, t.pool(), &MIGRATIONS, ".")
            .await
            .unwrap();
        all.push(("postgres", SqlDb::Postgres(t.pool().clone())));
        pg = Some(t);
    }
    Stores { all, dir, _pg: pg }
}

#[tokio::test]
async fn sql_store() {
    let st = stores("store").await;
    for (name, db) in &st.all {
        let ctx = Ctx::background();
        let s = Stores::open(db, "a");
        let now = Utc::now();
        for (i, status) in [STATUS_WAITING, STATUS_RUNNING].into_iter().enumerate() {
            let p = Process {
                id: ["a", "b"][i].to_string(),
                status: Status::from(status),
                created_at: now + chrono::TimeDelta::seconds(i as i64),
                children: BTreeMap::from([("x#0:y".to_string(), "c".to_string())]),
                vars: json!({"k": "v"}).as_object().unwrap().clone(),
                ..Default::default()
            };
            s.put(&ctx, &p).await.unwrap();
        }
        let err = s.get(&ctx, "zz").await.unwrap_err();
        assert!(err.is_not_found(), "{name}: missing process: {err}");
        let ps = s.list(&ctx).await.unwrap();
        assert!(
            ps.len() == 2 && ps[0].id == "b" && ps[1].status == STATUS_WAITING,
            "{name}: list: {ps:?}"
        );
        let p = s.get(&ctx, "a").await.unwrap();
        assert!(
            p.children["x#0:y"] == "c" && p.vars["k"] == "v",
            "{name}: round trip: {p:?}"
        );
        s.append_process_log(
            &ctx,
            "a",
            vec![ProcessLogEntry {
                r#type: "intent.turn".into(),
                payload: json!({"role": "user", "text": "hi"})
                    .as_object()
                    .unwrap()
                    .clone(),
                ..Default::default()
            }],
        )
        .await
        .unwrap();
        let p = s.get(&ctx, "a").await.unwrap();
        assert!(
            p.intent.turns.len() == 1 && p.intent.turns[0].text == "hi",
            "{name}: turns: {:?}",
            p.intent.turns
        );
    }
}

#[tokio::test]
async fn sql_store_locks() {
    let st = stores("locks").await;
    for (name, db) in &st.all {
        let ctx = Ctx::background();
        let (a, b) = (Stores::open(db, "a"), Stores::open(db, "b"));
        let ttl = Duration::from_secs(60);
        let (held, _) = a.acquire(&ctx, "p", ttl).await.unwrap();
        assert!(held, "{name}: a acquires");
        let (held, _) = b.acquire(&ctx, "p", ttl).await.unwrap();
        assert!(!held, "{name}: b acquires a held lock");
        // a write by another replica than the holder is refused; the holder's goes through
        let p = Process {
            id: "p".into(),
            status: Status::from(STATUS_RUNNING),
            created_at: Utc::now(),
            ..Default::default()
        };
        let err = b.put(&ctx, &p).await.unwrap_err();
        assert!(err.is_fenced(), "{name}: b writes a's process: {err}");
        a.put(&ctx, &p).await.unwrap();
        // an interruption reaches the holder at its renewal
        assert!(
            b.interrupt(&ctx, "p", "bob").await.unwrap(),
            "{name}: interrupt"
        );
        assert!(
            !a.interrupt(&ctx, "p", "self").await.unwrap(),
            "{name}: the holder interrupts itself"
        );
        let (held, by) = a.acquire(&ctx, "p", ttl).await.unwrap();
        assert!(held && by == "bob", "{name}: renewal: {held} {by:?}");
        // the holder is running and its lease is live: not an orphan
        let soon = Utc::now() + chrono::TimeDelta::seconds(1);
        let ids = b.orphans(&ctx, soon).await.unwrap();
        assert!(ids.is_empty(), "{name}: orphans while held: {ids:?}");
        a.release(&ctx, "p").await.unwrap();
        let soon = Utc::now() + chrono::TimeDelta::seconds(1);
        let ids = b.orphans(&ctx, soon).await.unwrap();
        assert_eq!(ids, vec!["p".to_string()], "{name}: orphans");
        let ids = b
            .orphans(&ctx, Utc::now() - chrono::TimeDelta::hours(1))
            .await
            .unwrap();
        assert!(ids.is_empty(), "{name}: orphans written later: {ids:?}");
        // b takes it, interruption cleared; a lease that ends lets a take it back, and fences b out
        let (held, by) = b
            .acquire(&ctx, "p", Duration::from_millis(50))
            .await
            .unwrap();
        assert!(
            held && by.is_empty(),
            "{name}: b after release: {held} {by:?}"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
        let (held, _) = a.acquire(&ctx, "p", ttl).await.unwrap();
        assert!(held, "{name}: a after b's lease");
        let (held, _) = b.acquire(&ctx, "p", ttl).await.unwrap();
        assert!(!held, "{name}: b renews a lock it lost");
        let err = b.put(&ctx, &p).await.unwrap_err();
        assert!(
            err.is_fenced(),
            "{name}: b writes after losing its lease: {err}"
        );
        a.release_all(&ctx).await.unwrap();
        let (held, _) = b.acquire(&ctx, "p", ttl).await.unwrap();
        assert!(held, "{name}: b after release all");
        // the store tells its orphans to the engine
        assert!(a.orphan_finder().is_some(), "{name}: orphan finder");
    }
}
