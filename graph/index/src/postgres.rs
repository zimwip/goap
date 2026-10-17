use std::collections::{BTreeMap, HashMap};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{NodeID, Version};
use include_dir::{Dir, include_dir};
use sqlx::{AssertSqlSafe, PgPool, Row, postgres::PgRow, types::Json};

use crate::index::kind_of;
use crate::{Doc, Error, Filter, Hit, Ref, Store, tokens};

/// Migrations holds the PostgreSQL schema of the index service (its `*.sql` files at the root:
/// `goap_platform::migrate(ctx, pool, &MIGRATIONS, ".")`).
pub static MIGRATIONS: Dir<'_> = include_dir!("$CARGO_MANIFEST_DIR/migrations");

/// Postgres is the production Store: tsvector for text, pgvector (HNSW, cosine) for embeddings.
///
/// Vectors travel in pgvector's text form (`[1,2,3]`, cast with `::public.vector`): the extension lives in `public`,
/// which a service role's `search_path` may not hold, so the type is never looked up by its bare name.
#[derive(Clone)]
pub struct Postgres {
    pool: PgPool,
}

/// A bound value of a dynamic query.
#[derive(Debug, Clone)]
enum PgArg {
    Str(String),
    Strs(Vec<String>),
    Bool(bool),
    Int(i64),
}

type PgQuery<'q> = sqlx::query::Query<'q, sqlx::Postgres, sqlx::postgres::PgArguments>;

fn bind(mut q: PgQuery<'_>, args: Vec<PgArg>) -> PgQuery<'_> {
    for a in args {
        q = match a {
            PgArg::Str(s) => q.bind(s),
            PgArg::Strs(s) => q.bind(s),
            PgArg::Bool(b) => q.bind(b),
            PgArg::Int(i) => q.bind(i),
        };
    }
    q
}

impl Postgres {
    /// NewPostgres returns a store on a migrated pool.
    pub fn new(pool: PgPool) -> Self {
        Postgres { pool }
    }

    /// EnsureVectorIndex creates the HNSW index for embeddings of the given dimension. The dimension belongs to the
    /// embedding model: changing the model means Reset, re-embedding and a new index.
    pub async fn ensure_vector_index(&self, _ctx: &Ctx, dim: i64) -> Result<(), Error> {
        if dim <= 0 {
            return Err(Error::msg(format!("invalid embedding dimension {dim}")));
        }
        sqlx::raw_sql(AssertSqlSafe(format!(
            "DROP INDEX IF EXISTS node_index_hnsw;
		CREATE INDEX node_index_hnsw ON node_index USING hnsw ((embedding::public.vector({dim})) public.vector_cosine_ops)"
        )))
        .execute(&self.pool)
        .await?;
        Ok(())
    }

    async fn query(
        &self,
        q: String,
        args: Vec<PgArg>,
        with_score: bool,
    ) -> Result<Vec<Hit>, Error> {
        let rows = bind(sqlx::query(AssertSqlSafe(q)), args)
            .fetch_all(&self.pool)
            .await?;
        rows.iter().map(|r| scan(r, with_score)).collect()
    }
}

/// The text form of a vector, "[1,2,3]".
fn vec_text(v: &[f32]) -> String {
    let parts: Vec<String> = v.iter().map(|x| x.to_string()).collect();
    format!("[{}]", parts.join(","))
}

/// parse_vec reads the text form of a pgvector value, "[1,2,3]".
fn parse_vec(s: &str) -> Result<Vec<f32>, Error> {
    let s = s.strip_prefix('[').unwrap_or(s);
    let s = s.strip_suffix(']').unwrap_or(s);
    if s.is_empty() {
        return Ok(Vec::new());
    }
    s.split(',')
        .map(|x| {
            x.trim()
                .parse::<f32>()
                .map_err(|e| Error::msg(format!("strconv.ParseFloat: parsing {x:?}: {e}")))
        })
        .collect()
}

#[async_trait]
impl Store for Postgres {
    async fn upsert(&self, _ctx: &Ctx, d: Doc) -> Result<(), Error> {
        sqlx::query("INSERT INTO node_index (kind, node_id, version, namespace, type, key, state, branch, main, deleted, project, owner, status, methodology, parent, personal_to, title, facets, doc, hash, embedding)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21::text::public.vector)
		ON CONFLICT (kind, node_id, version) DO UPDATE SET namespace=EXCLUDED.namespace, type=EXCLUDED.type, key=EXCLUDED.key, state=EXCLUDED.state,
			branch=EXCLUDED.branch, main = node_index.main OR EXCLUDED.main, deleted=EXCLUDED.deleted, project=EXCLUDED.project, owner=EXCLUDED.owner,
			status=EXCLUDED.status, methodology=EXCLUDED.methodology, parent=EXCLUDED.parent, personal_to=EXCLUDED.personal_to, title=EXCLUDED.title,
			facets=EXCLUDED.facets, doc=EXCLUDED.doc,
			embedding = COALESCE(EXCLUDED.embedding, CASE WHEN node_index.hash = EXCLUDED.hash THEN node_index.embedding END),
			hash=EXCLUDED.hash, updated=now()")
            .bind(kind_of(&d.kind))
            .bind(d.id.as_str())
            .bind(d.version as i32)
            .bind(&d.namespace)
            .bind(&d.r#type)
            .bind(&d.key)
            .bind(&d.state)
            .bind(&d.branch)
            .bind(d.main)
            .bind(d.deleted)
            .bind(&d.project)
            .bind(&d.owner)
            .bind(&d.status)
            .bind(&d.methodology)
            .bind(&d.parent)
            .bind(&d.personal_to)
            .bind(&d.title)
            .bind(Json(&d.facets))
            .bind(&d.text)
            .bind(&d.hash)
            .bind(d.embedding.as_deref().map(vec_text))
            .execute(&self.pool)
            .await?;
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
            "SELECT hash, embedding IS NOT NULL FROM node_index WHERE kind = $1 AND node_id = $2 AND version = $3",
        )
        .bind(kind_of(kind))
        .bind(id.as_str())
        .bind(v as i32)
        .fetch_optional(&self.pool)
        .await?;
        Ok(match row {
            None => None,
            Some(r) => Some((r.try_get(0)?, r.try_get(1)?)),
        })
    }

    async fn delete(&self, _ctx: &Ctx, kind: &str, id: &NodeID) -> Result<(), Error> {
        sqlx::query("DELETE FROM node_index WHERE kind = $1 AND node_id = $2")
            .bind(kind_of(kind))
            .bind(id.as_str())
            .execute(&self.pool)
            .await?;
        Ok(())
    }

    async fn texts(&self, _ctx: &Ctx, refs: &[Ref]) -> Result<HashMap<Ref, String>, Error> {
        let mut out = HashMap::with_capacity(refs.len());
        for r in refs {
            let doc: Option<String> = sqlx::query_scalar(
                "SELECT doc FROM node_index WHERE kind = $1 AND node_id = $2 AND version = $3",
            )
            .bind(kind_of(&r.kind))
            .bind(r.id.as_str())
            .bind(r.version as i32)
            .fetch_optional(&self.pool)
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
        let hits = self
            .query(
                format!("SELECT {PG_COLS}, 0::float8 AS score FROM node_index WHERE kind = $1 AND node_id = $2 ORDER BY main DESC, version DESC LIMIT 1"),
                vec![PgArg::Str(kind_of(kind).to_string()), PgArg::Str(id.to_string())],
                true,
            )
            .await?;
        let Some(h) = hits.into_iter().next() else {
            return Ok(None);
        };
        let text: Option<String> = sqlx::query_scalar(
            "SELECT embedding::text FROM node_index WHERE kind = $1 AND node_id = $2 AND version = $3",
        )
        .bind(kind_of(kind))
        .bind(id.as_str())
        .bind(h.version as i32)
        .fetch_one(&self.pool)
        .await?;
        match text {
            None => Ok(Some((h, None))),
            Some(t) => Ok(Some((h, Some(parse_vec(&t)?)))),
        }
    }

    async fn set_main(
        &self,
        _ctx: &Ctx,
        set: &BTreeMap<NodeID, Version>,
        removed: &[NodeID],
    ) -> Result<(), Error> {
        let mut tx = self.pool.begin().await?;
        for (id, v) in set {
            sqlx::query("UPDATE node_index SET main = (version = $2) WHERE kind = 'node' AND node_id = $1 AND main <> (version = $2)")
                .bind(id.as_str())
                .bind(*v as i32)
                .execute(&mut *tx)
                .await?;
        }
        for id in removed {
            sqlx::query(
                "UPDATE node_index SET main = false WHERE kind = 'node' AND node_id = $1 AND main",
            )
            .bind(id.as_str())
            .execute(&mut *tx)
            .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    async fn list(&self, _ctx: &Ctx, f: &Filter, limit: usize) -> Result<Vec<Hit>, Error> {
        let (w, mut args) = pg_where(f, Vec::new());
        args.push(PgArg::Int(limit as i64));
        let n = args.len();
        self.query(
            format!("SELECT {PG_COLS} FROM node_index WHERE {w} ORDER BY key, version LIMIT ${n}"),
            args,
            false,
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
        let toks = tokens(text);
        if toks.is_empty() {
            return Ok(Vec::new());
        }
        let q: Vec<String> = toks.iter().map(|t| format!("{t}:*")).collect();
        let (w, mut args) = pg_where(f, vec![PgArg::Str(q.join(" & "))]);
        args.push(PgArg::Int(limit as i64));
        let n = args.len();
        self.query(
            format!("SELECT {PG_COLS} FROM node_index, to_tsquery('simple', $1) q WHERE tsv @@ q AND {w} ORDER BY ts_rank_cd(tsv, q) DESC, key LIMIT ${n}"),
            args,
            false,
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
        let (w, mut args) = pg_where(f, vec![PgArg::Str(vec_text(vec))]);
        args.push(PgArg::Int(limit as i64));
        let n = args.len();
        // the expression matches the HNSW index of ensure_vector_index
        let dim = vec.len();
        let dist = format!(
            "embedding::public.vector({dim}) OPERATOR(public.<=>) $1::text::public.vector({dim})"
        );
        self.query(
            format!("SELECT {PG_COLS}, 1 - ({dist}) AS score FROM node_index WHERE embedding IS NOT NULL AND {w} ORDER BY {dist} LIMIT ${n}"),
            args,
            true,
        )
        .await
    }

    async fn reset(&self, _ctx: &Ctx) -> Result<(), Error> {
        sqlx::query("TRUNCATE node_index")
            .execute(&self.pool)
            .await?;
        Ok(())
    }
}

const PG_COLS: &str = "kind, node_id, version, namespace, type, key, state, branch, main, deleted, project, owner, status, methodology, parent, personal_to, title, facets";

/// pg_where renders the filter with numbered placeholders appended to args.
fn pg_where(f: &Filter, mut args: Vec<PgArg>) -> (String, Vec<PgArg>) {
    let mut conds = vec!["true".to_string()];
    let mut add = |cond: &str, v: PgArg, args: &mut Vec<PgArg>| {
        args.push(v);
        conds.push(cond.replace("$$", &format!("${}", args.len())));
    };
    for (col, vals) in [
        ("kind", &f.kind),
        ("namespace", &f.namespace),
        ("type", &f.r#type),
        ("state", &f.state),
        ("branch", &f.branch),
    ] {
        if !vals.is_empty() {
            add(
                &format!("{col} = ANY($$)"),
                PgArg::Strs(vals.clone()),
                &mut args,
            );
        }
    }
    if let Some(m) = f.main {
        add("main = $$", PgArg::Bool(m), &mut args);
    }
    for (col, vals) in [
        ("project", &f.project),
        ("owner", &f.owner),
        ("status", &f.status),
        ("methodology", &f.methodology),
    ] {
        if !vals.is_empty() {
            add(
                &format!("{col} = ANY($$)"),
                PgArg::Strs(vals.clone()),
                &mut args,
            );
        }
    }
    if f.roots_only {
        conds.push("kind = 'change' AND parent = ''".to_string());
    }
    for (name, vals) in &f.facets {
        if vals.is_empty() {
            continue;
        }
        args.push(PgArg::Str(name.clone()));
        let k = args.len();
        args.push(PgArg::Strs(vals.clone()));
        conds.push(format!("facets->>${k} = ANY(${})", k + 1));
    }
    (conds.join(" AND "), args)
}

fn scan(r: &PgRow, with_score: bool) -> Result<Hit, Error> {
    let id: String = r.try_get(1)?;
    let version: i32 = r.try_get(2)?;
    let Json(facets): Json<BTreeMap<String, String>> = r.try_get(17)?;
    Ok(Hit {
        kind: r.try_get(0)?,
        id: NodeID::from(id),
        version: version as Version,
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
        score: if with_score { r.try_get(18)? } else { 0.0 },
        ..Default::default()
    })
}
