//! Shared fixtures of the tests (the Go package's test helpers).
#![allow(dead_code)]

use std::sync::Arc;

use goap_authz::Principal;
use goap_ctx::Ctx;
use goap_def::{Attribute, Domain, LinkType, NodeType, Schema};
use goap_domainsvc::{MIGRATIONS, MemoryStore, SQLITE_MIGRATIONS, Service, SqlStore, Store};

pub fn alm_domain(version: &str) -> Domain {
    Domain {
        name: "alm".into(),
        version: version.into(),
        schema: Schema {
            node_types: vec![
                NodeType::named("Requirement"),
                NodeType {
                    attributes: vec![Attribute::named("title")],
                    ..NodeType::named("TestCase")
                },
            ],
            link_types: vec![LinkType {
                name: "verifies".into(),
                from: "TestCase".into(),
                to: "Requirement".into(),
                ..LinkType::default()
            }],
            ..Schema::default()
        },
        ..Domain::default()
    }
}

/// as is a caller with roles (the default policies: admin may write the domains).
pub fn as_(roles: &[&str]) -> Ctx {
    goap_authz::with_principal(
        &Ctx::background(),
        Principal {
            subject: "mia".into(),
            org: "acme".into(),
            roles: roles.iter().map(|r| r.to_string()).collect(),
            ..Principal::default()
        },
    )
}

/// The default policies.
pub fn casbin() -> Arc<dyn goap_authz::Authorizer> {
    Arc::new(goap_authz::Casbin::new().unwrap())
}

/// withALM publishes the alm domain of the repository.
pub async fn with_alm(s: &Service) {
    let src = std::fs::read("../../domains/alm.yaml").unwrap();
    let (_, issues) = s
        .import_domain(&as_(&["admin"]), &src, true)
        .await
        .unwrap_or_else(|e| panic!("alm domain: {e}"));
    assert!(issues.is_empty(), "alm domain: {issues}");
}

/// The stores of the domain versions: in memory, the registry's database in local mode (SQLite) and, with
/// GOAP_TEST_PG_DSN, on PostgreSQL. Each is fresh.
pub struct Stores {
    pub all: Vec<(&'static str, Arc<dyn Store>)>,
    _dir: TempDir,
    _pg: Option<goap_pgtest::PgTest>,
}

pub struct TempDir(pub std::path::PathBuf);

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

pub fn temp_dir(name: &str) -> TempDir {
    static SEQ: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
    let dir = std::env::temp_dir().join(format!(
        "goap-domainsvc-{name}-{}-{}",
        std::process::id(),
        SEQ.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
    ));
    std::fs::create_dir_all(&dir).unwrap();
    TempDir(dir)
}

pub async fn domain_stores(name: &str) -> Stores {
    let ctx = Ctx::background();
    let dir = temp_dir(name);
    let db = goap_platform::open_sqlite(&ctx, dir.0.join("goap.db").to_str().unwrap())
        .await
        .unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "registry", &SQLITE_MIGRATIONS, ".")
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
    Stores {
        all,
        _dir: dir,
        _pg: pg,
    }
}
