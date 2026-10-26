//! PostgreSQL: a pool of writers and a pool of readers (`default_transaction_read_only`), the reads with no BEGIN and
//! COMMIT around them (ADR 0127 addendum).
use std::time::Duration;

use deadpool_postgres::{Manager, ManagerConfig, Pool, RecyclingMethod};
use tokio_postgres::NoTls;

use crate::{StoreError, migrate, migrate::Part};

pub struct Postgres {
    pub write: Pool,
    pub read: Pool,
    /// The versions kept between transactions ([`crate::read::Versions`]).
    pub(crate) versions: std::sync::Arc<crate::read::Versions>,
}

fn pool(cfg: tokio_postgres::Config, size: usize) -> Result<Pool, StoreError> {
    let mgr = Manager::from_config(
        cfg,
        NoTls,
        ManagerConfig {
            recycling_method: RecyclingMethod::Fast,
        },
    );
    Ok(Pool::builder(mgr).max_size(size).build()?)
}

impl Postgres {
    /// Connects (retrying for twenty seconds, as the Go service does while the database starts) and migrates.
    pub async fn open(dsn: &str, part: Part) -> Result<Postgres, StoreError> {
        let cfg: tokio_postgres::Config = dsn.parse()?;
        let size = std::thread::available_parallelism().map_or(8, |n| n.get() * 4).max(8);
        let write = pool(cfg.clone(), size)?;
        let mut rcfg = cfg;
        let opts = rcfg.get_options().map(|o| format!("{o} ")).unwrap_or_default();
        rcfg.options(format!("{opts}-c default_transaction_read_only=on"));
        let read = pool(rcfg, size)?;
        let mut attempt = 1;
        loop {
            match write.get().await {
                Ok(_) => break,
                Err(e) if attempt >= 20 => return Err(e.into()),
                Err(e) => {
                    tracing::info!(attempt, error = %e, "waiting for postgres");
                    attempt += 1;
                    tokio::time::sleep(Duration::from_secs(1)).await;
                }
            }
        }
        let pg = Postgres {
            write,
            read,
            versions: Default::default(),
        };
        pg.migrate(part).await?;
        drop(pg.read.get().await?);
        Ok(pg)
    }

    async fn migrate(&self, part: Part) -> Result<(), StoreError> {
        let mut conn = self.write.get().await?;
        conn.batch_execute("SELECT pg_advisory_lock(hashtext(current_schema()))")
            .await?;
        let res = async {
            conn.batch_execute(
                "CREATE TABLE IF NOT EXISTS schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())",
            )
            .await?;
            for (component, files) in migrate::sets(true, part) {
                for (name, sql) in files {
                    let done: bool = conn
                        .query_one("SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)", &[name])
                        .await?
                        .get(0);
                    if done {
                        continue;
                    }
                    let step = async {
                        let tx = conn.transaction().await?;
                        tx.batch_execute(sql).await?;
                        tx.execute("INSERT INTO schema_migrations(name) VALUES ($1)", &[name]).await?;
                        tx.commit().await?;
                        Ok::<_, StoreError>(())
                    };
                    step.await.map_err(|e| StoreError::Migration { component, name, source: Box::new(e) })?;
                    tracing::info!(component, name, "migration applied");
                }
            }
            Ok::<_, StoreError>(())
        }
        .await;
        let unlock = conn
            .batch_execute("SELECT pg_advisory_unlock(hashtext(current_schema()))")
            .await;
        res?;
        unlock?;
        Ok(())
    }

    pub async fn ping(&self) -> Result<(), StoreError> {
        self.write.get().await?.batch_execute("SELECT 1").await?;
        Ok(())
    }
}
