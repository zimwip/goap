use std::collections::{BTreeMap, HashMap};

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{NodeID, Version, gotime};
use include_dir::{Dir, include_dir};
use sqlx::{AssertSqlSafe, Row, SqlitePool, sqlite::SqliteRow};

use crate::index::{cosine, kind_of};
use crate::{Doc, Error, Filter, Hit, Ref, Store, tokens};

/// The SQLite schema of the index (its `*.sql` files at the root:
/// `goap_platform::migrate_sqlite(ctx, pool, "index", &SQLITE_MIGRATIONS, ".")`).
pub static SQLITE_MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations_sqlite");

/// SQLite is the Store of the local mode: FTS5 for text, exact cosine scan for vectors.
#[derive(Clone)]
pub struct SQLite {
    db: SqlitePool,
}

impl SQLite {
    /// NewSQLite returns a store on an opened, migrated database (component "index").
    pub fn new(db: SqlitePool) -> Self {
        SQLite { db }
    }

    async fn query(&self, q: String, args: Vec<Arg>) -> Result<Vec<Hit>, Error> {
        let rows = bind(sqlx::query(AssertSqlSafe(q)), args)
            .fetch_all(&self.db)
            .await?;
        rows.iter().map(scan_hit).collect()
    }
}

/// A bound value of a dynamic query.
#[derive(Debug, Clone)]
pub(crate) enum Arg {
    Str(String),
    Bool(bool),
    Int(i64),
}

fn bind<'q>(
    mut q: sqlx::query::Query<'q, sqlx::Sqlite, sqlx::sqlite::SqliteArguments>,
    args: Vec<Arg>,
) -> sqlx::query::Query<'q, sqlx::Sqlite, sqlx::sqlite::SqliteArguments> {
    for a in args {
        q = match a {
            Arg::Str(s) => q.bind(s),
            Arg::Bool(b) => q.bind(b),
            Arg::Int(i) => q.bind(i),
        };
    }
    q
}

fn facets_json(f: &BTreeMap<String, String>) -> String {
    serde_json::to_string(f).unwrap_or_else(|_| "{}".to_string())
}

#[async_trait]
impl Store for SQLite {
    async fn upsert(&self, _ctx: &Ctx, mut d: Doc) -> Result<(), Error> {
        let facets = facets_json(&d.facets);
        d.kind = kind_of(&d.kind).to_string();
        let emb = pack_vec(d.embedding.as_deref());
        let mut tx = self.db.begin_with("BEGIN IMMEDIATE").await?;
        let found: Option<i64> = sqlx::query_scalar(
            "SELECT rowid FROM node_index WHERE kind = ? AND node_id = ? AND version = ?",
        )
        .bind(&d.kind)
        .bind(d.id.as_str())
        .bind(d.version)
        .fetch_optional(&mut *tx)
        .await?;
        let rowid = match found {
            Some(rowid) => {
                sqlx::query("UPDATE node_index SET namespace=?, type=?, key=?, state=?, branch=?, main = main OR ?, deleted=?, project=?, owner=?, status=?, methodology=?, parent=?, personal_to=?, title=?, facets=?, doc=?, embedding = COALESCE(?, CASE WHEN hash = ? THEN embedding END), hash=?, updated=? WHERE rowid=?")
                    .bind(&d.namespace).bind(&d.r#type).bind(&d.key).bind(&d.state).bind(&d.branch).bind(d.main).bind(d.deleted)
                    .bind(&d.project).bind(&d.owner).bind(&d.status).bind(&d.methodology).bind(&d.parent).bind(&d.personal_to)
                    .bind(&d.title).bind(&facets).bind(&d.text).bind(&emb).bind(&d.hash).bind(&d.hash).bind(ts(&d.time)).bind(rowid)
                    .execute(&mut *tx)
                    .await?;
                sqlx::query("DELETE FROM node_fts WHERE rowid = ?")
                    .bind(rowid)
                    .execute(&mut *tx)
                    .await?;
                rowid
            }
            None => sqlx::query("INSERT INTO node_index (kind, node_id, version, namespace, type, key, state, branch, main, deleted, project, owner, status, methodology, parent, personal_to, title, facets, doc, hash, embedding, updated)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)")
                .bind(&d.kind).bind(d.id.as_str()).bind(d.version).bind(&d.namespace).bind(&d.r#type).bind(&d.key).bind(&d.state)
                .bind(&d.branch).bind(d.main).bind(d.deleted).bind(&d.project).bind(&d.owner).bind(&d.status).bind(&d.methodology)
                .bind(&d.parent).bind(&d.personal_to).bind(&d.title).bind(&facets).bind(&d.text).bind(&d.hash).bind(&emb).bind(ts(&d.time))
                .execute(&mut *tx)
                .await?
                .last_insert_rowid(),
        };
        sqlx::query("INSERT INTO node_fts (rowid, doc) VALUES (?, ?)")
            .bind(rowid)
            .bind(&d.text)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    async fn hash(
        &self,
        _ctx: &Ctx,
        kind: &str,
        id: &NodeID,
        v: Version,
    ) -> Result<Option<(String, bool)>, Error> {
        let row = sqlx::query(
            "SELECT hash, embedding FROM node_index WHERE kind = ? AND node_id = ? AND version = ?",
        )
        .bind(kind_of(kind))
        .bind(id.as_str())
        .bind(v)
        .fetch_optional(&self.db)
        .await?;
        Ok(match row {
            None => None,
            Some(r) => {
                let emb: Option<Vec<u8>> = r.try_get(1)?;
                Some((r.try_get(0)?, emb.is_some_and(|e| !e.is_empty())))
            }
        })
    }

    async fn delete(&self, _ctx: &Ctx, kind: &str, id: &NodeID) -> Result<(), Error> {
        let mut tx = self.db.begin_with("BEGIN IMMEDIATE").await?;
        sqlx::query("DELETE FROM node_fts WHERE rowid IN (SELECT rowid FROM node_index WHERE kind = ? AND node_id = ?)")
            .bind(kind_of(kind))
            .bind(id.as_str())
            .execute(&mut *tx)
            .await?;
        sqlx::query("DELETE FROM node_index WHERE kind = ? AND node_id = ?")
            .bind(kind_of(kind))
            .bind(id.as_str())
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(())
    }

    async fn texts(&self, _ctx: &Ctx, refs: &[Ref]) -> Result<HashMap<Ref, String>, Error> {
        let mut out = HashMap::with_capacity(refs.len());
        for r in refs {
            let doc: Option<String> = sqlx::query_scalar(
                "SELECT doc FROM node_index WHERE kind = ? AND node_id = ? AND version = ?",
            )
            .bind(kind_of(&r.kind))
            .bind(r.id.as_str())
            .bind(r.version)
            .fetch_optional(&self.db)
            .await?;
            if let Some(doc) = doc {
                out.insert(r.clone(), doc);
            }
        }
        Ok(out)
    }

    async fn vector(
        &self,
        _ctx: &Ctx,
        kind: &str,
        id: &NodeID,
    ) -> Result<Option<(Hit, Option<Vec<f32>>)>, Error> {
        let row = sqlx::query(AssertSqlSafe(format!(
            "SELECT {HIT_COLS}, i.embedding FROM node_index i WHERE i.kind = ? AND i.node_id = ? ORDER BY i.main DESC, i.version DESC LIMIT 1"
        )))
        .bind(kind_of(kind))
        .bind(id.as_str())
        .fetch_optional(&self.db)
        .await?;
        let Some(row) = row else { return Ok(None) };
        let h = scan_hit(&row)?;
        let emb: Option<Vec<u8>> = row.try_get(18)?;
        Ok(Some((
            h,
            emb.filter(|e| !e.is_empty()).map(|e| unpack_vec(&e)),
        )))
    }

    async fn set_main(
        &self,
        _ctx: &Ctx,
        set: &BTreeMap<NodeID, Version>,
        removed: &[NodeID],
    ) -> Result<(), Error> {
        let mut tx = self.db.begin_with("BEGIN IMMEDIATE").await?;
        for (id, v) in set {
            sqlx::query(
                "UPDATE node_index SET main = (version = ?) WHERE kind = 'node' AND node_id = ?",
            )
            .bind(*v)
            .bind(id.as_str())
            .execute(&mut *tx)
            .await?;
        }
        for id in removed {
            sqlx::query("UPDATE node_index SET main = 0 WHERE kind = 'node' AND node_id = ?")
                .bind(id.as_str())
                .execute(&mut *tx)
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    async fn list(&self, _ctx: &Ctx, f: &Filter, limit: usize) -> Result<Vec<Hit>, Error> {
        let (w, mut args) = sql_where(f);
        args.push(Arg::Int(limit as i64));
        self.query(
            format!(
                "SELECT {HIT_COLS} FROM node_index i WHERE {w} ORDER BY i.key, i.version LIMIT ?"
            ),
            args,
        )
        .await
    }

    async fn full_text(
        &self,
        _ctx: &Ctx,
        text: &str,
        f: &Filter,
        limit: usize,
    ) -> Result<Vec<Hit>, Error> {
        let m = fts_query(text);
        if m.is_empty() {
            return Ok(Vec::new());
        }
        let (w, wargs) = sql_where(f);
        let mut args = vec![Arg::Str(m)];
        args.extend(wargs);
        args.push(Arg::Int(limit as i64));
        self.query(
            format!(
                "SELECT {HIT_COLS} FROM node_fts JOIN node_index i ON i.rowid = node_fts.rowid
		WHERE node_fts MATCH ? AND {w} ORDER BY bm25(node_fts) LIMIT ?"
            ),
            args,
        )
        .await
    }

    async fn nearest(
        &self,
        _ctx: &Ctx,
        vec: &[f32],
        f: &Filter,
        limit: usize,
    ) -> Result<Vec<Hit>, Error> {
        let (w, args) = sql_where(f);
        let rows = bind(
            sqlx::query(AssertSqlSafe(format!(
                "SELECT {HIT_COLS}, i.embedding FROM node_index i WHERE i.embedding IS NOT NULL AND {w}"
            ))),
            args,
        )
        .fetch_all(&self.db)
        .await?;
        let mut out = Vec::with_capacity(rows.len());
        for row in &rows {
            let mut h = scan_hit(row)?;
            let emb: Vec<u8> = row.try_get(18)?;
            h.score = cosine(vec, &unpack_vec(&emb));
            out.push(h);
        }
        out.sort_by(|a, b| {
            b.score
                .partial_cmp(&a.score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });
        out.truncate(limit);
        Ok(out)
    }

    async fn reset(&self, _ctx: &Ctx) -> Result<(), Error> {
        for q in ["DELETE FROM node_fts", "DELETE FROM node_index"] {
            sqlx::query(q).execute(&self.db).await?;
        }
        Ok(())
    }
}

const HIT_COLS: &str = "i.kind, i.node_id, i.version, i.namespace, i.type, i.key, i.state, i.branch, i.main, i.deleted, i.project, i.owner, i.status, i.methodology, i.parent, i.personal_to, i.title, i.facets";

/// sql_where renders the filter as SQL conditions on alias i.
pub(crate) fn sql_where(f: &Filter) -> (String, Vec<Arg>) {
    let mut conds: Vec<String> = Vec::new();
    let mut args: Vec<Arg> = Vec::new();
    let placeholders = |n: usize| vec!["?"; n].join(",");
    let mut is_in = |col: &str, vals: &[String], conds: &mut Vec<String>| {
        if vals.is_empty() {
            return;
        }
        conds.push(format!("{col} IN ({})", placeholders(vals.len())));
        args.extend(vals.iter().cloned().map(Arg::Str));
    };
    is_in("i.kind", &f.kind, &mut conds);
    is_in("i.namespace", &f.namespace, &mut conds);
    is_in("i.type", &f.r#type, &mut conds);
    is_in("i.state", &f.state, &mut conds);
    is_in("i.branch", &f.branch, &mut conds);
    is_in("i.project", &f.project, &mut conds);
    is_in("i.owner", &f.owner, &mut conds);
    is_in("i.status", &f.status, &mut conds);
    is_in("i.methodology", &f.methodology, &mut conds);
    if f.roots_only {
        conds.push("i.kind = 'change' AND i.parent = ''".to_string());
    }
    if let Some(m) = f.main {
        conds.push("i.main = ?".to_string());
        args.push(Arg::Bool(m));
    }
    for (n, vals) in &f.facets {
        if vals.is_empty() {
            continue;
        }
        conds.push(format!(
            "json_extract(i.facets, ?) IN ({})",
            placeholders(vals.len())
        ));
        args.push(Arg::Str(format!("$.\"{}\"", n.replace('"', ""))));
        args.extend(vals.iter().cloned().map(Arg::Str));
    }
    if conds.is_empty() {
        return ("1=1".to_string(), Vec::new());
    }
    (conds.join(" AND "), args)
}

fn scan_hit(r: &SqliteRow) -> Result<Hit, Error> {
    let id: String = r.try_get(1)?;
    let facets: String = r.try_get(17)?;
    let facets: BTreeMap<String, String> = serde_json::from_str(&facets).unwrap_or_default();
    Ok(Hit {
        kind: r.try_get(0)?,
        id: NodeID::from(id),
        version: r.try_get(2)?,
        namespace: r.try_get(3)?,
        r#type: r.try_get(4)?,
        key: r.try_get(5)?,
        state: r.try_get(6)?,
        branch: r.try_get(7)?,
        main: r.try_get(8)?,
        deleted: r.try_get(9)?,
        project: r.try_get(10)?,
        owner: r.try_get(11)?,
        status: r.try_get(12)?,
        methodology: r.try_get(13)?,
        parent: r.try_get(14)?,
        personal_to: r.try_get(15)?,
        title: r.try_get(16)?,
        facets,
        ..Default::default()
    })
}

/// fts_query turns free text into an AND of prefix terms, quoted so that FTS5 syntax in the input is inert.
fn fts_query(text: &str) -> String {
    tokens(text)
        .iter()
        .map(|t| format!("\"{t}\"*"))
        .collect::<Vec<_>>()
        .join(" ")
}

fn ts(t: &DateTime<Utc>) -> String {
    let t = if gotime::is_zero(t) { Utc::now() } else { *t };
    t.format("%Y-%m-%dT%H:%M:%S%.3fZ").to_string()
}

fn pack_vec(v: Option<&[f32]>) -> Option<Vec<u8>> {
    v.map(|v| v.iter().flat_map(|x| x.to_le_bytes()).collect())
}

fn unpack_vec(b: &[u8]) -> Vec<f32> {
    b.chunks_exact(4)
        .map(|c| f32::from_le_bytes([c[0], c[1], c[2], c[3]]))
        .collect()
}
