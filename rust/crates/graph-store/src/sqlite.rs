//! SQLite: one writing connection (SQLite has one writer; its transactions take the write lock when they begin) and a
//! pool of read-only ones (`query_only`), which in WAL mode see the last commit and never wait for the writer.
use std::sync::{Arc, Mutex};

use rusqlite::{Connection, OpenFlags};
use tokio::sync::{OwnedSemaphorePermit, Semaphore};

use crate::{StoreError, migrate};

pub struct Sqlite {
    writer: Arc<Mutex<Connection>>,
    readers: Option<Arc<Readers>>,
}

struct Readers {
    idle: Mutex<Vec<Connection>>,
    slots: Arc<Semaphore>,
}

/// The connection a session runs its statements on.
pub type SqliteConn = Connection;

/// A read session: a reader of the pool held until the session is dropped (the writer, shared, for a database in
/// memory). Each statement runs on the blocking pool.
pub struct SqliteSession {
    conn: Option<Arc<Mutex<Connection>>>,
    pool: Option<(Arc<Readers>, OwnedSemaphorePermit)>,
}

impl SqliteSession {
    /// Runs `f` on the connection of the session.
    pub async fn run<T: Send + 'static>(
        &self,
        f: impl FnOnce(&Connection) -> Result<T, StoreError> + Send + 'static,
    ) -> Result<T, StoreError> {
        let c = self.conn.clone().expect("a connection until dropped");
        blocking(move || {
            let c = c
                .lock()
                .map_err(|_| StoreError::Other("sqlite connection poisoned".into()))?;
            f(&c)
        })
        .await
    }
}

impl Drop for SqliteSession {
    fn drop(&mut self) {
        if let (Some(conn), Some((pool, _permit))) = (self.conn.take(), self.pool.take())
            && let Ok(m) = Arc::try_unwrap(conn)
        {
            pool.idle
                .lock()
                .unwrap_or_else(|e| e.into_inner())
                .push(m.into_inner().unwrap_or_else(|e| e.into_inner()));
        }
    }
}

const WRITER_PRAGMAS: &str =
    "PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 30000; PRAGMA synchronous = NORMAL;";
const READER_PRAGMAS: &str = "PRAGMA query_only = 1; PRAGMA busy_timeout = 30000; PRAGMA foreign_keys = ON;";

async fn blocking<T: Send + 'static>(
    f: impl FnOnce() -> Result<T, StoreError> + Send + 'static,
) -> Result<T, StoreError> {
    tokio::task::spawn_blocking(f)
        .await
        .map_err(|e| StoreError::Other(format!("sqlite task: {e}")))?
}

impl Sqlite {
    /// Opens (and creates) the database file, migrates it, then opens its readers.
    pub async fn open(path: &str, readers: usize) -> Result<Sqlite, StoreError> {
        let p = path.to_string();
        let writer = blocking(move || {
            if let Some(dir) = std::path::Path::new(&p).parent().filter(|d| !d.as_os_str().is_empty()) {
                std::fs::create_dir_all(dir).map_err(|e| StoreError::Other(format!("{}: {e}", dir.display())))?;
            }
            let c = Connection::open(&p)?;
            c.execute_batch(WRITER_PRAGMAS)?;
            migrate_sqlite(&c)?;
            Ok(c)
        })
        .await?;
        let p = path.to_string();
        let n = readers.max(1);
        let conns = blocking(move || {
            (0..n)
                .map(|_| {
                    let c = Connection::open_with_flags(
                        &p,
                        OpenFlags::SQLITE_OPEN_READ_WRITE
                            | OpenFlags::SQLITE_OPEN_NO_MUTEX
                            | OpenFlags::SQLITE_OPEN_URI,
                    )?;
                    c.execute_batch(READER_PRAGMAS)?;
                    Ok(c)
                })
                .collect::<Result<Vec<_>, StoreError>>()
        })
        .await?;
        Ok(Sqlite {
            writer: Arc::new(Mutex::new(writer)),
            readers: Some(Arc::new(Readers {
                idle: Mutex::new(conns),
                slots: Arc::new(Semaphore::new(n)),
            })),
        })
    }

    /// A database in memory: one connection, which reads too.
    pub async fn memory() -> Result<Sqlite, StoreError> {
        let c = blocking(|| {
            let c = Connection::open_in_memory()?;
            c.execute_batch("PRAGMA foreign_keys = ON;")?;
            migrate_sqlite(&c)?;
            Ok(c)
        })
        .await?;
        Ok(Sqlite {
            writer: Arc::new(Mutex::new(c)),
            readers: None,
        })
    }

    /// Runs `f` on the writing connection.
    pub async fn write<T: Send + 'static>(
        &self,
        f: impl FnOnce(&mut Connection) -> Result<T, StoreError> + Send + 'static,
    ) -> Result<T, StoreError> {
        let w = self.writer.clone();
        blocking(move || {
            let mut c = w
                .lock()
                .map_err(|_| StoreError::Other("sqlite writer poisoned".into()))?;
            f(&mut c)
        })
        .await
    }

    /// Runs `f` on a read-only connection (the writer when the database has none).
    pub async fn read<T: Send + 'static>(
        &self,
        f: impl FnOnce(&Connection) -> Result<T, StoreError> + Send + 'static,
    ) -> Result<T, StoreError> {
        let Some(r) = self.readers.clone() else {
            return self.write(move |c| f(c)).await;
        };
        let _slot = r.slots.acquire().await.map_err(|e| StoreError::Other(e.to_string()))?;
        let conn = r
            .idle
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .pop()
            .expect("a connection per slot");
        let r2 = r.clone();
        blocking(move || {
            let res = f(&conn);
            r2.idle.lock().unwrap_or_else(|e| e.into_inner()).push(conn);
            res
        })
        .await
    }

    /// A read session: a reader of the pool, held until the session is dropped.
    pub async fn session(&self) -> Result<SqliteSession, StoreError> {
        let Some(r) = self.readers.clone() else {
            return Ok(SqliteSession {
                conn: Some(self.writer.clone()),
                pool: None,
            });
        };
        let permit = r
            .slots
            .clone()
            .acquire_owned()
            .await
            .map_err(|e| StoreError::Other(e.to_string()))?;
        let conn = r
            .idle
            .lock()
            .unwrap_or_else(|e| e.into_inner())
            .pop()
            .expect("a connection per slot");
        Ok(SqliteSession {
            conn: Some(Arc::new(Mutex::new(conn))),
            pool: Some((r, permit)),
        })
    }

    pub async fn ping(&self) -> Result<(), StoreError> {
        self.read(|c| Ok(c.query_row("SELECT 1", [], |r| r.get::<_, i64>(0))?))
            .await?;
        Ok(())
    }
}

fn migrate_sqlite(c: &Connection) -> Result<(), StoreError> {
    c.execute_batch(
        "CREATE TABLE IF NOT EXISTS schema_migrations (
		component TEXT NOT NULL, name TEXT NOT NULL, applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		PRIMARY KEY (component, name))",
    )?;
    for (component, files) in migrate::sets(false) {
        for (name, sql) in files {
            let n: i64 = c.query_row(
                "SELECT count(*) FROM schema_migrations WHERE component = ? AND name = ?",
                [component, name],
                |r| r.get(0),
            )?;
            if n > 0 {
                continue;
            }
            let step = || -> Result<(), StoreError> {
                c.execute_batch("BEGIN IMMEDIATE")?;
                let res = c.execute_batch(sql).and_then(|_| {
                    c.execute(
                        "INSERT INTO schema_migrations (component, name) VALUES (?, ?)",
                        [component, name],
                    )
                });
                match res {
                    Ok(_) => Ok(c.execute_batch("COMMIT")?),
                    Err(e) => {
                        let _ = c.execute_batch("ROLLBACK");
                        Err(e.into())
                    }
                }
            };
            step().map_err(|e| StoreError::Migration {
                component,
                name,
                source: Box::new(e),
            })?;
            tracing::info!(component, name, "migration applied");
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn tables(c: &Connection) -> Vec<String> {
        let mut st = c
            .prepare("SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name")
            .unwrap();
        st.query_map([], |r| r.get(0))
            .unwrap()
            .collect::<Result<_, _>>()
            .unwrap()
    }

    #[tokio::test]
    async fn migrates_once_and_reads_on_readers() {
        let dir = tempfile::tempdir().unwrap();
        let path = dir.path().join("graph.db").display().to_string();
        let db = Sqlite::open(&path, 2).await.unwrap();
        let names = db.read(|c| Ok(tables(c))).await.unwrap();
        for t in [
            "node_version",
            "change",
            "change_log",
            "domain_version",
            "smart_tag",
            "schema_migrations",
        ] {
            assert!(names.iter().any(|n| n == t), "{t} missing in {names:?}");
        }
        let applied: i64 = db
            .read(|c| Ok(c.query_row("SELECT count(*) FROM schema_migrations", [], |r| r.get(0))?))
            .await
            .unwrap();
        let expected = migrate::sets(false).iter().map(|(_, f)| f.len() as i64).sum::<i64>();
        assert_eq!(applied, expected);
        // a reader refuses a write: the request runs again on the writer
        let err = db
            .read(|c| Ok(c.execute_batch("CREATE TABLE x (a int)")?))
            .await
            .unwrap_err();
        assert!(err.is_read_only(), "{err}");
        drop(db);
        // a second start applies nothing
        let db = Sqlite::open(&path, 1).await.unwrap();
        db.ping().await.unwrap();
    }

    #[tokio::test]
    async fn in_memory() {
        let db = Sqlite::memory().await.unwrap();
        db.ping().await.unwrap();
        assert!(
            db.read(|c| Ok(tables(c)))
                .await
                .unwrap()
                .contains(&"node_version".to_string())
        );
    }
}
