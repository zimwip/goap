//! The PostgreSQL store, when `GOAP_TEST_PG_DSN` names a database (as the Go tests, `lib/pgtest`); its schema is
//! dropped first.
use graph_store::{Dialect, Store, StoreConfig};

#[tokio::test]
async fn migrates_postgres_as_the_go_service() {
    let Ok(dsn) = std::env::var("GOAP_TEST_PG_DSN") else {
        eprintln!("GOAP_TEST_PG_DSN not set: skipped");
        return;
    };
    let (c, conn) = tokio_postgres::connect(&dsn, tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(conn);
    c.batch_execute("DROP SCHEMA public CASCADE; CREATE SCHEMA public;")
        .await
        .unwrap();
    let cfg = StoreConfig::Postgres { dsn: dsn.clone() };
    let store = Store::open(&cfg).await.unwrap();
    assert_eq!(store.dialect(), Dialect::Postgres);
    store.ping().await.unwrap();
    let names: Vec<String> = c
        .query("SELECT name FROM schema_migrations ORDER BY name", &[])
        .await
        .unwrap()
        .iter()
        .map(|r| r.get(0))
        .collect();
    for n in ["0001_schema.sql", "0001_smart_tag.sql", "0020_node_changes_index.sql"] {
        assert!(names.iter().any(|x| x == n), "{n} not recorded: {names:?}");
    }
    let Store::Postgres(pg) = &store else { unreachable!() };
    // the readers refuse a write
    let r = pg.read.get().await.unwrap();
    let err = graph_store::StoreError::from(r.batch_execute("CREATE TABLE x (a int)").await.unwrap_err());
    assert!(err.is_read_only(), "{err}");
    drop(store);
    // a second start applies nothing
    Store::open(&cfg).await.unwrap();
}
