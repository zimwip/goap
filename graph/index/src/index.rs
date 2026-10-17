//! Package index is the node index (ADR 0026): a read model of the graph that answers full-text, semantic (embedding)
//! and faceted searches. It is fed by the node and baseline events of the graph and knows nothing else of the platform.

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;

use async_trait::async_trait;
use chrono::{DateTime, Utc};
use goap_ctx::Ctx;
use goap_domain::{NodeID, Version, gotime};
use serde::{Deserialize, Serialize};

use crate::Error;

// Kinds of documents (ADR 0095): the versions of the nodes of the graph and the changes. One table, one text index
// and one vector index hold both, told apart by the kind: the filters, the facets, the ranking, the authorization and
// the reindex are the same code, and a search can mix them.
pub const KIND_NODE: &str = "node";
pub const KIND_CHANGE: &str = "change";

// Built-in facets, always present besides the ones the node type declares.
pub const FACET_KIND: &str = "kind";
pub const FACET_NAMESPACE: &str = "namespace";
pub const FACET_TYPE: &str = "type";
pub const FACET_STATE: &str = "state";
pub const FACET_BRANCH: &str = "branch";
/// "true" when the version is the head of main (a change document always is).
pub const FACET_MAIN: &str = "main";
pub const FACET_PROJECT: &str = "project";
pub const FACET_OWNER: &str = "owner";
/// Of a change.
pub const FACET_STATUS: &str = "status";
pub const FACET_METHODOLOGY: &str = "methodology";

// Search modes.
pub const MODE_HYBRID: &str = "hybrid";
pub const MODE_LEXICAL: &str = "lexical";
pub const MODE_SEMANTIC: &str = "semantic";

/// SnippetMax bounds the passage returned with a hit, in runes.
pub const SNIPPET_MAX: usize = 240;

/// PoolSize bounds the candidates a search ranks, authorizes and counts facets on.
pub const POOL_SIZE: usize = 500;

/// DefaultMinSimilarity is the cosine similarity below which a vector match is dropped: nearest-neighbour search
/// always returns something, however unrelated.
pub const DEFAULT_MIN_SIMILARITY: f64 = 0.3;

/// Ref identifies a document: a node version, or a change (ID is the change id, Version 0).
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, PartialOrd, Ord)]
pub struct Ref {
    pub kind: String,
    pub id: NodeID,
    pub version: Version,
}

/// Doc is one indexed document: a node version or a change (ADR 0095).
#[derive(Debug, Clone, PartialEq)]
pub struct Doc {
    /// KIND_NODE when empty.
    pub kind: String,
    pub id: NodeID,
    pub version: Version,
    pub namespace: String,
    /// Empty for a change.
    pub r#type: String,
    /// The change id for a change.
    pub key: String,
    /// Lifecycle state of a node; of a change.
    pub state: String,
    pub branch: String,
    pub main: bool,
    pub deleted: bool,
    /// Project and Owner are keys: the project and the unit owning the node version; for a change its project and
    /// its holding unit.
    pub project: String,
    pub owner: String,
    // Change documents only.
    pub status: String,
    pub methodology: String,
    /// Parent change, empty for a root change.
    pub parent: String,
    /// Subject a personal change belongs to.
    pub personal_to: String,
    pub title: String,
    /// Declared facets, values as text.
    pub facets: BTreeMap<String, String>,
    /// What full text and the embedding are built from.
    pub text: String,
    /// Of Text, to skip the embedding call when unchanged.
    pub hash: String,
    /// None: not embedded.
    pub embedding: Option<Vec<f32>>,
    /// Go's zero time when unset.
    pub time: DateTime<Utc>,
}

impl Default for Doc {
    fn default() -> Self {
        Doc {
            kind: String::new(),
            id: NodeID::default(),
            version: 0,
            namespace: String::new(),
            r#type: String::new(),
            key: String::new(),
            state: String::new(),
            branch: String::new(),
            main: false,
            deleted: false,
            project: String::new(),
            owner: String::new(),
            status: String::new(),
            methodology: String::new(),
            parent: String::new(),
            personal_to: String::new(),
            title: String::new(),
            facets: BTreeMap::new(),
            text: String::new(),
            hash: String::new(),
            embedding: None,
            time: gotime::zero(),
        }
    }
}

impl Doc {
    /// Ref identifies the document.
    pub fn r#ref(&self) -> Ref {
        Ref {
            kind: kind_of(&self.kind).to_string(),
            id: self.id.clone(),
            version: self.version,
        }
    }

    pub(crate) fn hit(&self) -> Hit {
        Hit {
            kind: kind_of(&self.kind).to_string(),
            id: self.id.clone(),
            version: self.version,
            namespace: self.namespace.clone(),
            r#type: self.r#type.clone(),
            key: self.key.clone(),
            state: self.state.clone(),
            branch: self.branch.clone(),
            main: self.main,
            deleted: self.deleted,
            project: self.project.clone(),
            owner: self.owner.clone(),
            facets: self.facets.clone(),
            status: self.status.clone(),
            methodology: self.methodology.clone(),
            parent: self.parent.clone(),
            personal_to: self.personal_to.clone(),
            title: self.title.clone(),
            ..Default::default()
        }
    }
}

fn is_false(b: &bool) -> bool {
    !*b
}

fn is_zero(f: &f64) -> bool {
    *f == 0.0
}

/// Hit is a search result.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Hit {
    pub kind: String,
    pub id: NodeID,
    pub version: Version,
    pub namespace: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub r#type: String,
    pub key: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub state: String,
    pub branch: String,
    pub main: bool,
    #[serde(skip_serializing_if = "is_false")]
    pub deleted: bool,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub project: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub facets: BTreeMap<String, String>,
    // Change documents only.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub methodology: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub parent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub personal_to: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    /// Score ranks the hit (the fused rank in a hybrid search, the cosine similarity in a semantic one); Similarity is
    /// the cosine similarity when the hit came out of the vector side, else 0.
    pub score: f64,
    #[serde(skip_serializing_if = "is_zero")]
    pub similarity: f64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub snippet: String,
}

impl Hit {
    /// Ref identifies the document of the hit.
    pub fn r#ref(&self) -> Ref {
        Ref {
            kind: kind_of(&self.kind).to_string(),
            id: self.id.clone(),
            version: self.version,
        }
    }

    fn facet(&self, name: &str) -> Option<String> {
        let opt = |v: &str| (!v.is_empty()).then(|| v.to_string());
        match name {
            FACET_KIND => Some(kind_of(&self.kind).to_string()),
            FACET_NAMESPACE => Some(self.namespace.clone()),
            FACET_TYPE => opt(&self.r#type),
            FACET_PROJECT => opt(&self.project),
            FACET_OWNER => opt(&self.owner),
            FACET_STATUS => opt(&self.status),
            FACET_METHODOLOGY => opt(&self.methodology),
            FACET_STATE => opt(&self.state),
            FACET_BRANCH => Some(self.branch.clone()),
            FACET_MAIN => Some(if self.main { "true" } else { "false" }.to_string()),
            _ => self.facets.get(name).cloned(),
        }
    }
}

pub(crate) fn kind_of(k: &str) -> &str {
    if k.is_empty() { KIND_NODE } else { k }
}

/// Filter restricts a search. Values of one field are alternatives, fields are combined with AND: a field a kind of
/// document does not have (the type of a change, the status of a node) keeps no document of that kind once set.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Filter {
    /// Empty: every kind (Searcher::search defaults to nodes).
    pub kind: Vec<String>,
    pub namespace: Vec<String>,
    pub r#type: Vec<String>,
    pub state: Vec<String>,
    pub branch: Vec<String>,
    pub main: Option<bool>,
    pub project: Vec<String>,
    pub owner: Vec<String>,
    /// Of changes.
    pub status: Vec<String>,
    pub methodology: Vec<String>,
    /// Changes with no parent (a node is no root change).
    pub roots_only: bool,
    /// Declared facets.
    pub facets: BTreeMap<String, Vec<String>>,
}

impl Filter {
    /// matches tells whether a document passes the filter (memory store, tests).
    pub(crate) fn matches(&self, h: &Hit) -> bool {
        fn is_in(vals: &[String], v: &str) -> bool {
            vals.is_empty() || vals.iter().any(|x| x == v)
        }
        if !is_in(&self.kind, kind_of(&h.kind))
            || !is_in(&self.namespace, &h.namespace)
            || !is_in(&self.r#type, &h.r#type)
            || !is_in(&self.state, &h.state)
            || !is_in(&self.branch, &h.branch)
            || !is_in(&self.project, &h.project)
            || !is_in(&self.owner, &h.owner)
            || !is_in(&self.status, &h.status)
            || !is_in(&self.methodology, &h.methodology)
        {
            return false;
        }
        if self.roots_only && (kind_of(&h.kind) != KIND_CHANGE || !h.parent.is_empty()) {
            return false;
        }
        if let Some(m) = self.main
            && m != h.main
        {
            return false;
        }
        for (name, vals) in &self.facets {
            match h.facets.get(name) {
                Some(v) if is_in(vals, v) => {}
                _ => return false,
            }
        }
        true
    }
}

/// Query is a search request.
#[derive(Debug, Clone, Default, PartialEq)]
pub struct Query {
    pub text: String,
    pub filter: Filter,
    /// Facets names the facets to count (built-in or declared).
    pub facets: Vec<String>,
    /// 0: 20.
    pub limit: usize,
    pub offset: usize,
    /// Mode is MODE_HYBRID (default), MODE_LEXICAL or MODE_SEMANTIC. Semantic requires an embedding and fails with
    /// Error::SemanticUnavailable without one; hybrid degrades to lexical and reports Result.semantic false.
    pub mode: String,
    /// MinSimilarity, when set, replaces the searcher's floor for this request (cosine, 0..1).
    pub min_similarity: Option<f64>,
    /// Snippet asks for the best matching passage of each returned hit.
    pub snippet: bool,
    /// SimilarTo ranks the documents nearest to an existing one by its stored embedding (no text, no embedding call).
    /// It excludes the document itself (every version of a node) and honours the filter.
    pub similar_to: Option<Ref>,
}

/// FacetCount is one value of a facet and the number of hits holding it.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct FacetCount {
    pub value: String,
    pub count: usize,
}

impl FacetCount {
    pub fn new(value: impl Into<String>, count: usize) -> Self {
        FacetCount {
            value: value.into(),
            count,
        }
    }
}

/// Result is the answer of a search.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct SearchResult {
    pub hits: Vec<Hit>,
    pub total: usize,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub facets: BTreeMap<String, Vec<FacetCount>>,
    /// Semantic tells whether the embedding side took part in the ranking.
    pub semantic: bool,
    /// Truncated: more than POOL_SIZE candidates matched, the total and facets cover the best ones.
    #[serde(skip_serializing_if = "is_false")]
    pub truncated: bool,
}

/// Store is the persistence of the index. Implementations: memory, SQLite (FTS5 + exact cosine scan), PostgreSQL
/// (tsvector + pgvector).
#[async_trait]
pub trait Store: Send + Sync {
    /// Upsert inserts or replaces the version (idempotent on ID + Version). The main flag of an existing version is
    /// kept unless the document says Main, and a nil embedding keeps the stored one when the text hash is unchanged.
    async fn upsert(&self, ctx: &Ctx, d: Doc) -> Result<(), Error>;
    /// Hash returns the text hash and the embedding presence of a stored document (None: not found).
    async fn hash(
        &self,
        ctx: &Ctx,
        kind: &str,
        id: &NodeID,
        v: Version,
    ) -> Result<Option<(String, bool)>, Error>;
    /// Delete removes every version of a document (a purged change).
    async fn delete(&self, ctx: &Ctx, kind: &str, id: &NodeID) -> Result<(), Error>;
    /// Texts returns the text of the documents (for snippets).
    async fn texts(&self, ctx: &Ctx, refs: &[Ref]) -> Result<HashMap<Ref, String>, Error>;
    /// Vector returns the document of a node (its head of main, else its latest version) or of a change, with its
    /// embedding; None when it does not exist, the vector is None when it is not embedded.
    async fn vector(
        &self,
        ctx: &Ctx,
        kind: &str,
        id: &NodeID,
    ) -> Result<Option<(Hit, Option<Vec<f32>>)>, Error>;
    /// SetMain marks the given versions as the head of main and clears the flag of the other versions of the same
    /// nodes (documents of kind node); removed nodes lose the flag.
    async fn set_main(
        &self,
        ctx: &Ctx,
        set: &BTreeMap<NodeID, Version>,
        removed: &[NodeID],
    ) -> Result<(), Error>;
    /// List returns the versions matching the filter, by key.
    async fn list(&self, ctx: &Ctx, f: &Filter, limit: usize) -> Result<Vec<Hit>, Error>;
    /// FullText returns the best full-text matches, best first.
    async fn full_text(
        &self,
        ctx: &Ctx,
        text: &str,
        f: &Filter,
        limit: usize,
    ) -> Result<Vec<Hit>, Error>;
    /// Nearest returns the versions nearest to the vector, best first, with the cosine similarity as Score.
    async fn nearest(
        &self,
        ctx: &Ctx,
        vec: &[f32],
        f: &Filter,
        limit: usize,
    ) -> Result<Vec<Hit>, Error>;
    /// Reset empties the index (before a full rebuild).
    async fn reset(&self, ctx: &Ctx) -> Result<(), Error>;
}

/// Embedder turns texts into vectors (the model gateway). None means text-only indexing.
#[async_trait]
pub trait Embedder: Send + Sync {
    async fn embed(&self, ctx: &Ctx, texts: &[String]) -> Result<Vec<Vec<f32>>, Error>;
}

/// Authorizer keeps the hits the caller may see (ABAC), preserving order. A plain function
/// `Fn(&Ctx, Vec<Hit>) -> Result<Vec<Hit>, Error>` is one.
#[async_trait]
pub trait Authorizer: Send + Sync {
    async fn authorize(&self, ctx: &Ctx, hits: Vec<Hit>) -> Result<Vec<Hit>, Error>;
}

#[async_trait]
impl<F> Authorizer for F
where
    F: Fn(&Ctx, Vec<Hit>) -> Result<Vec<Hit>, Error> + Send + Sync,
{
    async fn authorize(&self, ctx: &Ctx, hits: Vec<Hit>) -> Result<Vec<Hit>, Error> {
        self(ctx, hits)
    }
}

/// Searcher runs hybrid searches over a store.
#[derive(Clone)]
pub struct Searcher {
    pub store: Arc<dyn Store>,
    /// None: full text only.
    pub embedder: Option<Arc<dyn Embedder>>,
    /// None: no filtering.
    pub authorize: Option<Arc<dyn Authorizer>>,
    /// MinSimilarity drops vector matches less similar than this (cosine, 0 means DEFAULT_MIN_SIMILARITY, negative
    /// keeps everything).
    pub min_similarity: f64,
}

impl Searcher {
    /// A searcher of a store, full text only, no filtering.
    pub fn new(store: Arc<dyn Store>) -> Self {
        Searcher {
            store,
            embedder: None,
            authorize: None,
            min_similarity: 0.0,
        }
    }

    /// Search ranks the candidates (full text and nearest vector merged by reciprocal rank fusion), authorizes them,
    /// counts the facets on the authorized set and returns the requested page. Nothing is counted, totalled or quoted
    /// before the authorization: a document the caller may not see leaves no trace in the answer.
    pub async fn search(&self, ctx: &Ctx, mut q: Query) -> Result<SearchResult, Error> {
        let mut res = SearchResult::default();
        if q.filter.kind.is_empty() {
            q.filter.kind = vec![KIND_NODE.to_string()];
        }
        if q.mode.is_empty() {
            q.mode = MODE_HYBRID.to_string();
        }
        if q.mode != MODE_HYBRID && q.mode != MODE_LEXICAL && q.mode != MODE_SEMANTIC {
            return Err(Error::invalid(format!("unknown mode {:?}", q.mode)));
        }
        let mut floor = self.min_similarity;
        if floor == 0.0 {
            floor = DEFAULT_MIN_SIMILARITY;
        }
        if let Some(m) = q.min_similarity {
            if !(0.0..=1.0).contains(&m) {
                return Err(Error::invalid(format!(
                    "min similarity {} is not in 0..1",
                    go_float(m)
                )));
            }
            floor = m;
        }
        let keep = |near: Vec<Hit>| -> Vec<Hit> {
            near.into_iter()
                .filter(|h| h.score >= floor)
                .map(|mut h| {
                    h.similarity = h.score;
                    h
                })
                .collect()
        };

        let mut lists: Vec<Vec<Hit>> = Vec::new();
        let mut this: Option<Ref> = None;
        if let Some(src) = &q.similar_to {
            let hits = self.similar(ctx, &q, src, &keep).await?;
            lists.push(hits);
            res.semantic = true;
            this = Some(src.clone());
        } else if q.text.is_empty() {
            if q.mode == MODE_SEMANTIC {
                return Err(Error::invalid("a semantic search needs a text"));
            }
            lists.push(self.store.list(ctx, &q.filter, POOL_SIZE + 1).await?);
        } else {
            if q.mode != MODE_SEMANTIC {
                lists.push(
                    self.store
                        .full_text(ctx, &q.text, &q.filter, POOL_SIZE + 1)
                        .await?,
                );
            }
            match (&self.embedder, q.mode != MODE_LEXICAL) {
                (Some(emb), true) => {
                    let got = emb.embed(ctx, std::slice::from_ref(&q.text)).await;
                    match got {
                        Ok(vecs) if vecs.len() == 1 => {
                            let near = self
                                .store
                                .nearest(ctx, &vecs[0], &q.filter, POOL_SIZE + 1)
                                .await?;
                            lists.push(keep(near));
                            res.semantic = true;
                        }
                        // in hybrid mode an embedding failure degrades the search to full text
                        other if q.mode == MODE_SEMANTIC => {
                            let why = match other {
                                Err(e) => e.to_string(),
                                Ok(_) => "<nil>".to_string(),
                            };
                            return Err(Error::semantic_unavailable(why));
                        }
                        _ => {}
                    }
                }
                _ if q.mode == MODE_SEMANTIC => {
                    return Err(Error::SemanticUnavailable(
                        crate::error::SEMANTIC_UNAVAILABLE.to_string(),
                    ));
                }
                _ => {}
            }
        }
        let mut hits = fuse(lists);
        if let Some(src) = &this {
            hits.retain(|h| !(kind_of(&h.kind) == kind_of(&src.kind) && h.id == src.id));
        }
        if hits.len() > POOL_SIZE {
            hits.truncate(POOL_SIZE);
            res.truncated = true;
        }
        if let Some(a) = &self.authorize {
            hits = a.authorize(ctx, hits).await?;
        }
        res.total = hits.len();
        res.facets = count_facets(&hits, &q.facets);
        if q.limit == 0 {
            q.limit = 20;
        }
        let lo = q.offset.min(hits.len());
        let hi = (lo + q.limit).min(hits.len());
        res.hits = hits[lo..hi].to_vec();
        if q.snippet && !res.hits.is_empty() {
            self.snippets(ctx, &mut res.hits, &q.text).await?;
        }
        Ok(res)
    }

    /// similar ranks the documents nearest to the stored embedding of an existing one. The source goes through the
    /// authorization too: a document the caller cannot see is as good as absent.
    async fn similar(
        &self,
        ctx: &Ctx,
        q: &Query,
        src: &Ref,
        keep: &(dyn Fn(Vec<Hit>) -> Vec<Hit> + Sync),
    ) -> Result<Vec<Hit>, Error> {
        let kind = kind_of(&src.kind);
        let mut found = self.store.vector(ctx, kind, &src.id).await?;
        if let (Some((hit, _)), Some(a)) = (&found, &self.authorize) {
            let seen = a.authorize(ctx, vec![hit.clone()]).await?;
            if seen.len() != 1 {
                found = None;
            }
        }
        let Some((_, vec)) = found else {
            return Err(Error::not_found(format!("{kind} {}", src.id)));
        };
        let vec = match vec {
            Some(v) if !v.is_empty() => v,
            _ => {
                return Err(Error::semantic_unavailable(format!(
                    "{kind} {} has no embedding (yet)",
                    src.id
                )));
            }
        };
        let near = self
            .store
            .nearest(ctx, &vec, &q.filter, POOL_SIZE + 2)
            .await?;
        Ok(keep(near))
    }

    /// snippets fills the passage of each hit that best matches the query (the beginning of the text when nothing
    /// does).
    async fn snippets(&self, ctx: &Ctx, hits: &mut [Hit], text: &str) -> Result<(), Error> {
        let refs: Vec<Ref> = hits.iter().map(Hit::r#ref).collect();
        let texts = self.store.texts(ctx, &refs).await?;
        for (h, r) in hits.iter_mut().zip(&refs) {
            h.snippet = snippet(texts.get(r).map_or("", String::as_str), text, SNIPPET_MAX);
        }
        Ok(())
    }
}

/// Go's `%v` of a float64 (strconv 'g' with the shortest precision).
fn go_float(f: f64) -> String {
    let abs = f.abs();
    if abs != 0.0 && !(1e-4..1e21).contains(&abs) {
        let s = format!("{f:e}");
        let (mant, exp) = s.split_once('e').unwrap_or((&s, "0"));
        let (sign, digits) = match exp.strip_prefix('-') {
            Some(d) => ('-', d),
            None => ('+', exp),
        };
        format!("{mant}e{sign}{digits:0>2}")
    } else {
        format!("{f}")
    }
}

/// Snippet returns the passage of a text that holds most terms of the query, at most limit runes (an ellipsis marks
/// a cut). The passages are the lines of the text, long ones cut at the sentences.
pub fn snippet(text: &str, query: &str, limit: usize) -> String {
    let terms = tokens(query);
    let mut best = "";
    let mut best_score: i64 = -1;
    for line in text.split('\n') {
        for p in sentences(line) {
            let p = p.trim();
            if p.is_empty() {
                continue;
            }
            let toks = tokens(p);
            let score = terms
                .iter()
                .filter(|t| toks.iter().any(|x| x.starts_with(t.as_str())))
                .count() as i64;
            if score > best_score {
                best = p;
                best_score = score;
            }
        }
    }
    clip(best, limit, &terms)
}

fn sentences(line: &str) -> Vec<&str> {
    if line.chars().count() <= SNIPPET_MAX {
        return vec![line];
    }
    line.split_inclusive(". ").collect()
}

/// clip cuts s to limit runes at most (ellipses included), around the first term it holds when it is long.
fn clip(s: &str, limit: usize, terms: &[String]) -> String {
    let r: Vec<char> = s.chars().collect();
    if r.len() <= limit {
        return s.to_string();
    }
    let mut start: i64 = 0;
    let low = go_lower(s);
    // lowering keeps the length but for rare scripts: then the window starts at the beginning
    if low.chars().count() == r.len() {
        for t in terms {
            if let Some(i) = low.find(t.as_str()) {
                start = low[..i].chars().count() as i64 - (limit / 4) as i64;
                break;
            }
        }
    }
    let start = start.max(0).min(r.len() as i64 - 1) as usize;
    let mut room = limit;
    if start > 0 {
        room -= 1;
    }
    let mut end = start + room;
    if end < r.len() {
        room -= 1;
        end = start + room;
    }
    let end = end.min(r.len());
    let mut out: String = r[start..end].iter().collect();
    if start > 0 {
        out = format!("…{out}");
    }
    if end < r.len() {
        out.push('…');
    }
    out
}

/// fuse merges ranked lists by reciprocal rank fusion (k = 60); a single list keeps its order.
fn fuse(mut lists: Vec<Vec<Hit>>) -> Vec<Hit> {
    if lists.len() == 1 {
        return lists.pop().unwrap_or_default();
    }
    let mut by: HashMap<Ref, usize> = HashMap::new();
    let mut order: Vec<(Hit, f64)> = Vec::new();
    for l in lists {
        for (rank, h) in l.into_iter().enumerate() {
            let i = match by.get(&h.r#ref()) {
                Some(&i) => i,
                None => {
                    by.insert(h.r#ref(), order.len());
                    order.push((h.clone(), 0.0));
                    order.len() - 1
                }
            };
            let e = &mut order[i];
            if h.similarity > e.0.similarity {
                e.0.similarity = h.similarity;
            }
            e.1 += 1.0 / (60 + rank + 1) as f64;
        }
    }
    order.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));
    order
        .into_iter()
        .map(|(mut h, s)| {
            h.score = s;
            h
        })
        .collect()
}

fn count_facets(hits: &[Hit], names: &[String]) -> BTreeMap<String, Vec<FacetCount>> {
    let mut out = BTreeMap::new();
    for name in names {
        let mut counts: HashMap<String, usize> = HashMap::new();
        for h in hits {
            if let Some(v) = h.facet(name) {
                *counts.entry(v).or_default() += 1;
            }
        }
        let mut fc: Vec<FacetCount> = counts
            .into_iter()
            .map(|(value, count)| FacetCount { value, count })
            .collect();
        fc.sort_by(|a, b| b.count.cmp(&a.count).then_with(|| a.value.cmp(&b.value)));
        out.insert(name.clone(), fc);
    }
    out
}

pub(crate) fn cosine(a: &[f32], b: &[f32]) -> f64 {
    if a.len() != b.len() || a.is_empty() {
        return 0.0;
    }
    let (mut dot, mut na, mut nb) = (0.0f64, 0.0f64, 0.0f64);
    for (x, y) in a.iter().zip(b) {
        let (x, y) = (*x as f64, *y as f64);
        dot += x * y;
        na += x * x;
        nb += y * y;
    }
    if na == 0.0 || nb == 0.0 {
        return 0.0;
    }
    dot / (na.sqrt() * nb.sqrt())
}

/// Go's strings.ToLower: each rune mapped to its lower case (one rune).
pub(crate) fn go_lower(s: &str) -> String {
    s.chars()
        .map(|c| c.to_lowercase().next().unwrap_or(c))
        .collect()
}

/// Tokens lowercases and splits a text into alphanumeric terms.
pub fn tokens(s: &str) -> Vec<String> {
    use unicode_properties::{GeneralCategory, GeneralCategoryGroup, UnicodeGeneralCategory};
    let word = |c: char| {
        c.general_category_group() == GeneralCategoryGroup::Letter
            || c.general_category() == GeneralCategory::DecimalNumber
    };
    go_lower(s)
        .split(|c: char| !word(c))
        .filter(|t| !t.is_empty())
        .map(str::to_string)
        .collect()
}
