//! The local storage, secrets and files of the single-process platform (Go's local.go).

use std::collections::BTreeMap;
use std::path::{Component, Path, PathBuf};
use std::sync::Arc;

use async_trait::async_trait;
use axum::Router;
use axum::http::{StatusCode, Uri, header};
use axum::response::{IntoResponse, Response};
use goap_ctx::Ctx;
use goap_engine::{Locker, Payload, Publisher, TriggerEvent};
use goap_graph::Repo;
use goap_platform::include_dir::Dir;
use goap_platform::{migrate_sqlite, open_sqlite};
use sqlx::SqlitePool;

use crate::Config;
use crate::app::{BoxError, TriggerRef};

/// Stores are the storage backends of the single-process platform.
pub(crate) struct Stores {
    pub graph: Arc<dyn Repo>,
    pub processes: Arc<dyn goap_engine::Store>,
    /// The process store as the locks of the engine, when it is shared by replicas (sqlite; Go's type assertion on
    /// the store).
    pub locks: Option<Arc<dyn Locker>>,
    pub models: Arc<dyn goap_modelgw::Store>,
    pub prefs: Arc<dyn goap_prefssvc::Store>,
    pub convs: Arc<dyn goap_convsvc::Store>,
    pub creds: Arc<dyn goap_credsvc::Store>,
    pub mcp: Arc<dyn goap_mcpsvc::Store>,
    pub index: Arc<dyn goap_index::Store>,
    /// The name of the index store, shown by its status.
    pub index_name: &'static str,
    pub domains: Arc<dyn goap_domainsvc::Store>,
    /// The directory of the local database (sqlite), empty in memory: where the generated JWT secret of local sign-in
    /// is kept (local_jwt_secret).
    pub dir: String,
    db: Option<SqlitePool>,
}

impl Stores {
    /// close closes the local database.
    pub async fn close(&self) {
        if let Some(db) = &self.db {
            db.close().await;
        }
    }
}

/// open_stores selects the storage (GOAP_STORE): memory, or sqlite for the local mode (one SQLite file,
/// GOAP_SQLITE_PATH, default .goap/goap.db).
pub(crate) async fn open_stores(ctx: &Ctx, cfg: &Config) -> Result<Stores, BoxError> {
    match cfg.store.as_str() {
        "memory" => Ok(Stores {
            graph: Arc::new(goap_graph::Memory::new()),
            processes: Arc::new(goap_engine::MemoryStore::new()),
            locks: None,
            models: Arc::new(goap_modelgw::MemoryStore::new()),
            prefs: Arc::new(goap_prefssvc::MemoryStore::new()),
            convs: Arc::new(goap_convsvc::MemoryStore::new()),
            creds: Arc::new(goap_credsvc::MemoryStore::new()),
            mcp: Arc::new(goap_mcpsvc::MemoryStore::new()),
            index: Arc::new(goap_index::Memory::new()),
            index_name: "Memory",
            domains: Arc::new(goap_domainsvc::MemoryStore::new()),
            dir: String::new(),
            db: None,
        }),
        "sqlite" => {
            let path = if cfg.sqlite_path.is_empty() {
                Path::new(".goap").join("goap.db").display().to_string()
            } else {
                cfg.sqlite_path.clone()
            };
            let db = open_sqlite(ctx, &path).await?;
            let migrations: [(&str, &Dir<'static>); 9] = [
                ("graph", &goap_graph::SQLITE_MIGRATIONS),
                ("engine", &goap_enginesvc::SQLITE_MIGRATIONS),
                ("modelgw", &goap_modelgw::SQLITE_MIGRATIONS),
                ("preferences", &goap_prefssvc::SQLITE_MIGRATIONS),
                ("conversations", &goap_convsvc::SQLITE_MIGRATIONS),
                ("credentials", &goap_credsvc::SQLITE_MIGRATIONS),
                ("mcp", &goap_mcpsvc::SQLITE_MIGRATIONS),
                ("index", &goap_index::SQLITE_MIGRATIONS),
                // the domain registry (ADR 0125 §3), under the component name it had in the registry: a local database
                // keeps its table
                ("registry", &goap_domainsvc::SQLITE_MIGRATIONS),
            ];
            for (component, fs) in migrations {
                if let Err(e) = migrate_sqlite(ctx, &db, component, fs, ".").await {
                    db.close().await;
                    return Err(e.into());
                }
            }
            // the local mode is the one process on its file: the locks the previous start left are dropped, so that
            // its runs are resumed at once (ADR 0124)
            let processes = Arc::new(goap_enginesvc::SQLStore::sqlite(
                db.clone(),
                goap_enginesvc::new_holder(),
            ));
            if let Err(e) = processes.release_all(ctx).await {
                db.close().await;
                return Err(e.into());
            }
            let abs = std::path::absolute(&path)
                .map(|p| p.display().to_string())
                .unwrap_or_else(|_| path.clone());
            tracing::info!(sqlite = %abs, "local storage");
            let dir = match Path::new(&path).parent() {
                Some(d) if !d.as_os_str().is_empty() => d.display().to_string(),
                _ => ".".to_string(),
            };
            Ok(Stores {
                graph: Arc::new(goap_graph::Sqlite::new(db.clone())),
                processes: processes.clone(),
                locks: Some(processes),
                models: Arc::new(goap_modelgw::SqlStore::Sqlite(db.clone())),
                prefs: Arc::new(goap_prefssvc::SqlStore::Sqlite(db.clone())),
                convs: Arc::new(goap_convsvc::SqlStore::Sqlite(db.clone())),
                creds: Arc::new(goap_credsvc::SqlStore::Sqlite(db.clone())),
                mcp: Arc::new(goap_mcpsvc::SqlStore::Sqlite(db.clone())),
                index: Arc::new(goap_index::SQLite::new(db.clone())),
                index_name: "SQLite",
                domains: Arc::new(goap_domainsvc::SqlStore::Sqlite(db.clone())),
                dir,
                db: Some(db),
            })
        }
        kind => Err(format!("GOAP_STORE must be memory or sqlite, got {kind:?}").into()),
    }
}

/// local_jwt_secret is the secret local sign-in (ADR 0042, on by default) signs its tokens with when none is
/// configured (GOAP_JWT_SECRET, Vault): generated once and kept next to the local database (dir/jwt_secret), so a
/// restart does not sign everyone out; in memory (dir empty) the accounts do not survive a restart either, so a fresh
/// secret per process is right.
pub(crate) fn local_jwt_secret(dir: &str) -> std::io::Result<String> {
    local_secret(dir, "jwt_secret")
}

/// A random secret: 32 bytes, hex encoded.
fn generate() -> std::io::Result<String> {
    let mut b = [0u8; 32];
    getrandom::fill(&mut b).map_err(std::io::Error::other)?;
    Ok(b.iter().map(|x| format!("{x:02x}")).collect())
}

/// local_secret is a random secret kept in the file name of dir (generated once; a fresh one per process when dir is
/// empty).
pub(crate) fn local_secret(dir: &str, name: &str) -> std::io::Result<String> {
    if dir.is_empty() {
        return generate();
    }
    let path = Path::new(dir).join(name);
    match std::fs::read_to_string(&path) {
        Ok(s) if s.trim().len() >= 32 => return Ok(s.trim().to_string()),
        Ok(_) => {}
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {}
        Err(e) => return Err(e),
    }
    let secret = generate()?;
    write_private(&path, format!("{secret}\n").as_bytes())?;
    Ok(secret)
}

/// Writes a file only its owner may read (Go's `os.WriteFile(path, b, 0o600)`).
fn write_private(path: &Path, b: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    let mut o = std::fs::OpenOptions::new();
    o.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o600);
    }
    o.open(path)?.write_all(b)
}

/// serve_web serves a built IDE (npm run build) with a single-page fallback, leaving the Connect RPCs (/goap.*) and
/// HTTP endpoints (/api, health) alone: the router of the files, or None when dir holds no built IDE. A path no route
/// serves is a file of dir, else the IDE's index.html (Go's echo static middleware with HTML5).
pub(crate) fn serve_web(dir: &str) -> Option<Router> {
    let index = Path::new(dir).join("index.html");
    if !index.is_file() {
        tracing::info!(dir = %dir, "no built IDE to serve (cd web && npm run build, or npm run dev)");
        return None;
    }
    tracing::info!(dir = %dir, "serving the IDE");
    let root = Arc::new(PathBuf::from(dir));
    Some(Router::new().fallback(move |uri: Uri| {
        let root = root.clone();
        async move { static_file(&root, &uri).await }
    }))
}

/// skipped reports the paths the IDE never serves.
fn skipped(p: &str) -> bool {
    p.starts_with("/goap.") || p.starts_with("/api/") || p == "/healthz" || p == "/readyz"
}

/// The file of root a request path names (cleaned: no way out of root).
fn file_of(root: &Path, p: &str) -> PathBuf {
    let mut rel = PathBuf::new();
    for c in Path::new(p).components() {
        match c {
            Component::Normal(s) => rel.push(s),
            Component::ParentDir => {
                rel.pop();
            }
            _ => {}
        }
    }
    root.join(rel)
}

async fn static_file(root: &Path, uri: &Uri) -> Response {
    let p = uri.path();
    if skipped(p) {
        return not_found();
    }
    let mut f = file_of(root, p);
    if f.is_dir() {
        f = f.join("index.html");
    }
    if !f.is_file() {
        f = root.join("index.html");
    }
    match tokio::fs::read(&f).await {
        Ok(b) => ([(header::CONTENT_TYPE, content_type(&f))], b).into_response(),
        Err(_) => not_found(),
    }
}

fn not_found() -> Response {
    (
        StatusCode::NOT_FOUND,
        [(header::CONTENT_TYPE, "application/json")],
        "{\"message\":\"Not Found\"}\n",
    )
        .into_response()
}

/// The media type of a file by its extension.
fn content_type(f: &Path) -> &'static str {
    match f
        .extension()
        .and_then(|e| e.to_str())
        .map(str::to_ascii_lowercase)
        .as_deref()
    {
        Some("html" | "htm") => "text/html; charset=utf-8",
        Some("js" | "mjs") => "text/javascript; charset=utf-8",
        Some("css") => "text/css; charset=utf-8",
        Some("json" | "map") => "application/json",
        Some("svg") => "image/svg+xml",
        Some("png") => "image/png",
        Some("jpg" | "jpeg") => "image/jpeg",
        Some("gif") => "image/gif",
        Some("webp") => "image/webp",
        Some("ico") => "image/vnd.microsoft.icon",
        Some("woff") => "font/woff",
        Some("woff2") => "font/woff2",
        Some("ttf") => "font/ttf",
        Some("wasm") => "application/wasm",
        Some("txt" | "md") => "text/plain; charset=utf-8",
        _ => "application/octet-stream",
    }
}

/// The subject of a methodology published (the registry's event the triggers follow).
const METHODOLOGY_PUBLISHED_SUBJECT: &str = "goap.registry.methodology.published";

/// RegistryEvents receives the methodology registry events in-process (no NATS here) and reacts to publications of
/// methodologies (the domain registry calls its changed hook in process).
pub(crate) struct RegistryEvents {
    pub triggers: Arc<TriggerRef>,
}

#[async_trait]
impl Publisher for RegistryEvents {
    async fn publish(&self, ctx: &Ctx, subject: &str, v: &Payload<'_>) -> goap_engine::Result<()> {
        let Some(ev) = v.downcast::<BTreeMap<String, String>>() else {
            return Ok(());
        };
        if subject == METHODOLOGY_PUBLISHED_SUBJECT {
            let get = |k: &str| ev.get(k).cloned().unwrap_or_default();
            self.triggers
                .handle(
                    &ctx.without_cancel(),
                    TriggerEvent {
                        r#type: goap_events::METHODOLOGY_PUBLISHED.to_string(),
                        methodology: get("name"),
                        version: get("version"),
                        ..Default::default()
                    },
                )
                .await;
        }
        Ok(())
    }
}

/// local_url is the base URL of this process for a listening address (":8080" is http://localhost:8080).
pub(crate) fn local_url(addr: &str) -> String {
    if addr.starts_with(':') {
        format!("http://localhost{addr}")
    } else {
        format!("http://{addr}")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn local_urls() {
        assert_eq!(local_url(":8080"), "http://localhost:8080");
        assert_eq!(local_url("127.0.0.1:9"), "http://127.0.0.1:9");
    }

    #[test]
    fn a_local_secret_is_kept() {
        let dir = std::env::temp_dir().join(format!("goap-dev-secret-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let d = dir.display().to_string();
        let a = local_secret(&d, "s").unwrap();
        assert_eq!(a.len(), 64);
        assert_eq!(local_secret(&d, "s").unwrap(), a);
        // a short one is replaced
        std::fs::write(dir.join("s"), "short\n").unwrap();
        let b = local_secret(&d, "s").unwrap();
        assert_ne!(b, a);
        assert_eq!(b.len(), 64);
        // in memory: a fresh one each time
        assert_ne!(
            local_secret("", "s").unwrap(),
            local_secret("", "s").unwrap()
        );
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn web_paths_stay_in_their_directory() {
        let root = Path::new("/srv/web");
        assert_eq!(
            file_of(root, "/../../etc/passwd"),
            Path::new("/srv/web/etc/passwd")
        );
        assert_eq!(file_of(root, "/a/./b.js"), Path::new("/srv/web/a/b.js"));
        assert!(skipped("/goap.graph.v1.GraphService/ListNamespaces"));
        assert!(skipped("/api/status"));
        assert!(!skipped("/assets/app.js"));
    }
}
