//! Port of index_test.go and changes_test.go: every test runs on each store (memory, SQLite, and PostgreSQL when
//! GOAP_TEST_PG_DSN is set: it needs the pgvector extension).

use std::collections::BTreeMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use async_trait::async_trait;
use goap_ctx::Ctx;
use goap_domain::{
    self as domain, BaselineEvent, ChangeDocEvent, ChangeDocRef, ChangeStatus, NodeEvent, NodeID,
    Version,
};
use serde_json::json;

use crate::*;

pub(crate) async fn sqlite_store() -> Arc<dyn Store> {
    let ctx = Ctx::background();
    let db = goap_platform::open_sqlite(&ctx, ":memory:").await.unwrap();
    goap_platform::migrate_sqlite(&ctx, &db, "index", &SQLITE_MIGRATIONS, ".")
        .await
        .unwrap();
    Arc::new(SQLite::new(db))
}

pub(crate) async fn postgres_store() -> Option<(Arc<dyn Store>, goap_pgtest::PgTest)> {
    let dsn = std::env::var(goap_pgtest::DSN_VAR)
        .ok()
        .filter(|d| !d.is_empty())?;
    let ctx = Ctx::background();
    let t = goap_pgtest::pool_with(&dsn, None).await;
    goap_platform::migrate(&ctx, t.pool(), &MIGRATIONS, ".")
        .await
        .unwrap();
    let st = Postgres::new(t.pool().clone());
    st.ensure_vector_index(&ctx, 16).await.unwrap();
    Some((Arc::new(st), t))
}

macro_rules! for_each_store {
    ($($name:ident),* $(,)?) => {
        $(
            mod $name {
                use super::*;

                #[tokio::test]
                async fn memory() {
                    super::$name(Arc::new(Memory::new())).await;
                }

                #[tokio::test]
                async fn sqlite() {
                    super::$name(sqlite_store().await).await;
                }

                #[tokio::test]
                async fn postgres() {
                    // skipped without GOAP_TEST_PG_DSN; needs the pgvector extension
                    let Some((s, _db)) = postgres_store().await else { return };
                    super::$name(s).await;
                }
            }
        )*
    };
}

for_each_store!(
    index_search,
    indexer_skips_unchanged_embedding,
    change_documents_and_filters,
    change_update_move_and_purge,
    search_modes,
    similar_to,
    snippets,
    node_project_and_owner_facets,
);

/// bagEmbedder is a deterministic embedder: a bag of hashed words over 16 dimensions.
struct BagEmbedder;

#[async_trait]
impl Embedder for BagEmbedder {
    async fn embed(&self, _ctx: &Ctx, texts: &[String]) -> Result<Vec<Vec<f32>>, Error> {
        Ok(texts
            .iter()
            .map(|t| {
                let mut v = vec![0f32; 16];
                for w in tokens(t) {
                    let mut h: i64 = 0;
                    for c in w.chars() {
                        h = h.wrapping_mul(31).wrapping_add(c as i64);
                    }
                    v[h.rem_euclid(16) as usize] += 1.0;
                }
                v
            })
            .collect())
    }
}

#[derive(Default)]
struct CountEmbedder {
    calls: AtomicUsize,
}

impl CountEmbedder {
    fn calls(&self) -> usize {
        self.calls.load(Ordering::SeqCst)
    }
}

#[async_trait]
impl Embedder for CountEmbedder {
    async fn embed(&self, ctx: &Ctx, texts: &[String]) -> Result<Vec<Vec<f32>>, Error> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        BagEmbedder.embed(ctx, texts).await
    }
}

struct Failing;

#[async_trait]
impl Embedder for Failing {
    async fn embed(&self, _ctx: &Ctx, _texts: &[String]) -> Result<Vec<Vec<f32>>, Error> {
        Err(Error::msg("down"))
    }
}

fn bag() -> Option<Arc<dyn Embedder>> {
    Some(Arc::new(BagEmbedder))
}

fn indexer(s: &Arc<dyn Store>, emb: Option<Arc<dyn Embedder>>) -> Indexer {
    Indexer {
        store: s.clone(),
        embedder: emb,
        embed_failed: None,
    }
}

fn searcher(s: &Arc<dyn Store>, emb: Option<Arc<dyn Embedder>>) -> Searcher {
    Searcher {
        store: s.clone(),
        embedder: emb,
        authorize: None,
        min_similarity: 0.0,
    }
}

fn node(
    id: &str,
    ver: Version,
    key: &str,
    typ: &str,
    title: &str,
    prio: &str,
    branch: &str,
) -> NodeEvent {
    NodeEvent {
        id: id.into(),
        version: ver,
        branch: branch.into(),
        namespace: "alm".into(),
        key: key.into(),
        r#type: typ.into(),
        state: "draft".into(),
        text: BTreeMap::from([("title".to_string(), title.to_string())]),
        facets: json!({"prio": prio}).as_object().unwrap().clone(),
        ..Default::default()
    }
}

fn set(pairs: &[(&str, Version)]) -> BTreeMap<NodeID, Version> {
    pairs.iter().map(|(k, v)| (NodeID::from(*k), *v)).collect()
}

fn hit_keys(r: &SearchResult) -> String {
    r.hits
        .iter()
        .map(|h| format!("{}@{}", h.key, h.version))
        .collect::<Vec<_>>()
        .join(",")
}

fn strs(v: &[&str]) -> Vec<String> {
    v.iter().map(|s| s.to_string()).collect()
}

async fn index_search(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    let x = indexer(&s, bag());
    for ev in [
        node(
            "n1",
            1,
            "REQ-1",
            "Requirement",
            "Users log in with a password",
            "high",
            "main",
        ),
        node(
            "n2",
            1,
            "REQ-2",
            "Requirement",
            "Export the report as PDF",
            "low",
            "main",
        ),
        node(
            "n3",
            1,
            "FN-1",
            "Function",
            "Password reset by mail",
            "high",
            "main",
        ),
        node(
            "n1",
            2,
            "REQ-1",
            "Requirement",
            "Users log in with a passkey",
            "high",
            "chg-1",
        ),
    ] {
        x.on_node(&ctx, &ev).await.unwrap();
    }
    // main head: n1@1, n2@1, n3@1
    x.on_baseline(
        &ctx,
        &BaselineEvent {
            branch: "main".into(),
            set: set(&[("n1", 1), ("n2", 1), ("n3", 1)]),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let mut se = searcher(&s, bag());

    // full text + hybrid, main only
    let r = se
        .search(
            &ctx,
            Query {
                text: "password".into(),
                filter: Filter {
                    main: Some(true),
                    ..Default::default()
                },
                facets: strs(&[FACET_TYPE, "prio"]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let keys = hit_keys(&r);
    assert!(
        r.semantic && !keys.is_empty() && keys.contains("REQ-1") && keys.contains("FN-1"),
        "hits = {keys} semantic={}",
        r.semantic
    );
    for h in &r.hits {
        assert!(h.main, "non-main hit {h:?}");
    }
    assert!(
        r.facets.get(FACET_TYPE).is_some_and(|f| !f.is_empty()),
        "facets = {:?}",
        r.facets
    );

    // all branches: both versions of REQ-1 are indexed
    let r = se
        .search(
            &ctx,
            Query {
                text: "log in".into(),
                filter: Filter {
                    r#type: strs(&["Requirement"]),
                    ..Default::default()
                },
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let k = hit_keys(&r);
    assert!(k.contains("REQ-1@1") && k.contains("REQ-1@2"), "hits = {k}");

    // facet filter without a query, counts
    let r = se
        .search(
            &ctx,
            Query {
                filter: Filter {
                    main: Some(true),
                    facets: BTreeMap::from([("prio".to_string(), strs(&["high"]))]),
                    ..Default::default()
                },
                facets: strs(&["prio", FACET_MAIN]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        r.total == 2 && r.facets["prio"][0] == FacetCount::new("high", 2),
        "total {} facets {:?}",
        r.total,
        r.facets
    );

    // a new main head moves the flag from REQ-1@1 to REQ-1@2 and drops FN-1
    x.on_baseline(
        &ctx,
        &BaselineEvent {
            branch: "main".into(),
            set: set(&[("n1", 2)]),
            removed: vec!["n3".into()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    let r = se
        .search(
            &ctx,
            Query {
                filter: Filter {
                    main: Some(true),
                    ..Default::default()
                },
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let k = hit_keys(&r);
    assert!(
        !k.contains("REQ-1@1") && !k.contains("FN-1"),
        "main after head move = {k}"
    );

    // FTS syntax in the input is inert
    se.search(
        &ctx,
        Query {
            text: r#"pass" OR (*"#.into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();

    // authorization runs before counting and paging
    se.authorize = Some(Arc::new(|_: &Ctx, hits: Vec<Hit>| {
        Ok(hits
            .into_iter()
            .filter(|h| h.r#type == "Function")
            .collect())
    }));
    let r = se
        .search(
            &ctx,
            Query {
                text: "password".into(),
                facets: strs(&[FACET_TYPE]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        r.total == 1 && r.facets[FACET_TYPE].len() == 1,
        "authorized total {} facets {:?}",
        r.total,
        r.facets
    );
}

async fn indexer_skips_unchanged_embedding(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    let emb = Arc::new(CountEmbedder::default());
    let x = indexer(&s, Some(emb.clone()));
    let ev = node("n1", 1, "REQ-1", "Requirement", "same text", "high", "main");
    for _ in 0..3 {
        x.on_node(&ctx, &ev).await.unwrap();
    }
    assert_eq!(emb.calls(), 1, "embed calls (redelivery is idempotent)");
    s.reset(&ctx).await.unwrap();
    let r = searcher(&s, None)
        .search(&ctx, Query::default())
        .await
        .unwrap();
    assert_eq!(r.total, 0, "reset left {}", r.total);
}

fn node_in(id: &str, key: &str, title: &str, project: &str, owner: &str) -> NodeEvent {
    let mut ev = node(id, 1, key, "Requirement", title, "high", "main");
    ev.project = project.into();
    ev.owner = owner.into();
    ev
}

#[allow(clippy::too_many_arguments)]
fn change_ev(
    id: &str,
    title: &str,
    intent: &str,
    project: &str,
    owner: &str,
    parent: &str,
    status: ChangeStatus,
    impacts: &[&str],
) -> ChangeDocEvent {
    ChangeDocEvent {
        id: id.into(),
        title: title.into(),
        intent: intent.into(),
        methodology: "sdlc".into(),
        namespace: "alm".into(),
        status,
        state: "draft".into(),
        project_id: project.into(),
        owner_org: owner.into(),
        parent_id: parent.into(),
        impacts: impacts
            .iter()
            .map(|k| ChangeDocRef {
                key: k.to_string(),
                r#type: "alm@Requirement".into(),
            })
            .collect(),
        ..Default::default()
    }
}

async fn seed_docs(s: &Arc<dyn Store>, emb: Option<Arc<dyn Embedder>>) -> Indexer {
    let ctx = Ctx::background();
    let x = indexer(s, emb);
    for ev in [
        node_in("n1", "REQ-1", "Users reset the password", "P1", "ORG-A"),
        node_in("n2", "REQ-2", "Export the report as PDF", "P2", "ORG-B"),
    ] {
        x.on_node(&ctx, &ev).await.unwrap();
    }
    x.on_baseline(
        &ctx,
        &BaselineEvent {
            branch: "main".into(),
            set: set(&[("n1", 1), ("n2", 1)]),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    for (ev, to) in [
        (
            change_ev(
                "C1",
                "Reset password flow",
                "Rework how users reset the password by mail",
                "P1",
                "ORG-A",
                "",
                domain::CHANGE_ACTIVE,
                &["REQ-1"],
            ),
            "",
        ),
        (
            change_ev(
                "C2",
                "Export report as PDF",
                "Add a PDF export of the report",
                "P2",
                "ORG-B",
                "C1",
                domain::CHANGE_APPLIED,
                &["REQ-2"],
            ),
            "",
        ),
        (
            change_ev(
                "C3",
                "Draft the password policy",
                "Private notes on the password policy",
                "P1",
                "USR:alice",
                "",
                domain::CHANGE_ACTIVE,
                &[],
            ),
            "alice",
        ),
    ] {
        x.on_change(&ctx, &ev, to).await.unwrap();
    }
    x
}

fn keys_of(r: &SearchResult) -> Vec<String> {
    r.hits.iter().map(|h| h.key.clone()).collect()
}

fn sorted_keys(r: &SearchResult) -> Vec<String> {
    let mut ks = keys_of(r);
    ks.sort();
    ks
}

fn kinds(k: &[&str]) -> Filter {
    Filter {
        kind: strs(k),
        ..Default::default()
    }
}

async fn change_documents_and_filters(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    seed_docs(&s, bag()).await;
    let se = searcher(&s, bag());
    let search = |q: Query| {
        let se = se.clone();
        let ctx = ctx.clone();
        async move { se.search(&ctx, q).await.unwrap() }
    };
    let both = [KIND_NODE, KIND_CHANGE];

    let r = search(Query::default()).await;
    assert!(
        r.total == 2 && r.hits[0].kind == KIND_NODE,
        "default kind is node: {r:?}"
    );
    let r = search(Query {
        filter: kinds(&[KIND_CHANGE]),
        ..Default::default()
    })
    .await;
    assert_eq!(r.total, 3, "changes: {r:?}");
    let r = search(Query {
        filter: kinds(&both),
        ..Default::default()
    })
    .await;
    assert_eq!(r.total, 5, "mixed: {r:?}");
    // a hit of a change carries its header fields
    let r = search(Query {
        text: "pdf".into(),
        mode: MODE_LEXICAL.into(),
        filter: kinds(&[KIND_CHANGE]),
        ..Default::default()
    })
    .await;
    assert_eq!(r.total, 1, "pdf: {r:?}");
    let h = &r.hits[0];
    assert!(
        h.kind == KIND_CHANGE
            && h.id == "C2"
            && h.key == "C2"
            && h.title == "Export report as PDF"
            && h.status == "applied"
            && h.project == "P2"
            && h.methodology == "sdlc"
            && h.owner == "ORG-B"
            && h.parent == "C1"
            && h.version == 0
            && h.main
            && h.namespace == "alm",
        "hit = {h:?}"
    );
    // the keys of the impacts are searchable
    let r = search(Query {
        text: "req".into(),
        mode: MODE_LEXICAL.into(),
        filter: kinds(&[KIND_CHANGE]),
        ..Default::default()
    })
    .await;
    assert_eq!(r.total, 2, "impact key: {r:?}");
    // filters
    let f = |g: fn(&mut Filter)| {
        let mut f = kinds(&both);
        g(&mut f);
        f
    };
    let cases: Vec<(&str, Filter, &str)> = vec![
        ("project", f(|f| f.project = strs(&["P1"])), "C1,C3,REQ-1"),
        ("project 2", f(|f| f.project = strs(&["P2"])), "C2,REQ-2"),
        ("owner", f(|f| f.owner = strs(&["ORG-A"])), "C1,REQ-1"),
        ("status", f(|f| f.status = strs(&["active"])), "C1,C3"),
        (
            "methodology",
            f(|f| f.methodology = strs(&["sdlc"])),
            "C1,C2,C3",
        ),
        ("roots", f(|f| f.roots_only = true), "C1,C3"),
        ("main", f(|f| f.main = Some(true)), "C1,C2,C3,REQ-1,REQ-2"),
        ("not main", f(|f| f.main = Some(false)), ""),
        (
            "types",
            f(|f| f.r#type = strs(&["Requirement"])),
            "REQ-1,REQ-2",
        ),
        (
            "namespace",
            f(|f| f.namespace = strs(&["alm"])),
            "C1,C2,C3,REQ-1,REQ-2",
        ),
    ];
    for (name, filter, want) in cases {
        let got = sorted_keys(
            &search(Query {
                filter,
                limit: 50,
                ..Default::default()
            })
            .await,
        )
        .join(",");
        assert_eq!(got, want, "{name}");
    }
    // facets
    let r = search(Query {
        filter: kinds(&both),
        facets: strs(&[FACET_KIND, FACET_PROJECT, FACET_STATUS, FACET_TYPE]),
        ..Default::default()
    })
    .await;
    let got = &r.facets[FACET_PROJECT];
    assert!(
        got.len() == 2 && got[0] == FacetCount::new("P1", 3),
        "project facet {got:?}"
    );
    let got = &r.facets[FACET_KIND];
    assert!(
        got.len() == 2 && got[0] == FacetCount::new(KIND_CHANGE, 3),
        "kind facet {got:?}"
    );
    assert_eq!(r.facets[FACET_TYPE].len(), 1, "a change has no type");
    assert_eq!(r.facets[FACET_STATUS].len(), 2, "status facet");
    // the personal subject is stored for the authorizer
    let r = search(Query {
        filter: Filter {
            kind: strs(&[KIND_CHANGE]),
            status: strs(&["active"]),
            ..Default::default()
        },
        limit: 10,
        ..Default::default()
    })
    .await;
    assert_eq!(r.total, 2, "{r:?}");
    for h in &r.hits {
        assert_eq!(h.id == "C3", h.personal_to == "alice", "personalTo {h:?}");
    }
    // paging
    let page = |offset| Query {
        filter: kinds(&both),
        limit: 2,
        offset,
        ..Default::default()
    };
    let p1 = search(page(0)).await;
    let p2 = search(page(2)).await;
    let p3 = search(page(4)).await;
    assert!(
        p1.total == 5
            && p1.hits.len() == 2
            && p2.hits.len() == 2
            && p3.hits.len() == 1
            && p1.hits[0].id != p2.hits[0].id,
        "paging {} {} {}",
        p1.hits.len(),
        p2.hits.len(),
        p3.hits.len()
    );
}

async fn change_update_move_and_purge(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    let emb = Arc::new(CountEmbedder::default());
    let x = indexer(&s, Some(emb.clone()));
    let mut ev = change_ev(
        "C1",
        "Reset password flow",
        "intent",
        "P1",
        "ORG-A",
        "",
        domain::CHANGE_ACTIVE,
        &["REQ-1"],
    );
    x.on_change(&ctx, &ev, "").await.unwrap();
    // the same text again (a new status, a move to another project): no new embedding
    ev.status = domain::CHANGE_COMMITTED;
    ev.project_id = "P9".into();
    ev.state = "review".into();
    x.on_change(&ctx, &ev, "").await.unwrap();
    assert_eq!(emb.calls(), 1, "embed calls (hash skip)");
    let se = searcher(&s, Some(emb.clone()));
    let r = se
        .search(
            &ctx,
            Query {
                filter: kinds(&[KIND_CHANGE]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        r.total == 1
            && r.hits[0].project == "P9"
            && r.hits[0].status == "committed"
            && r.hits[0].state == "review",
        "{r:?}"
    );
    let r = se
        .search(
            &ctx,
            Query {
                filter: Filter {
                    kind: strs(&[KIND_CHANGE]),
                    project: strs(&["P1"]),
                    ..Default::default()
                },
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(r.total, 0, "the old project still matches");
    // a reformulated title is a new text: embedded again, and found by its new words
    ev.title = "Passkeys instead of passwords".into();
    x.on_change(&ctx, &ev, "").await.unwrap();
    assert_eq!(emb.calls(), 2, "embed calls");
    let r = se
        .search(
            &ctx,
            Query {
                text: "passkeys".into(),
                filter: kinds(&[KIND_CHANGE]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(r.total, 1, "reformulated: {r:?}");
    let r = se
        .search(
            &ctx,
            Query {
                text: "reset".into(),
                filter: kinds(&[KIND_CHANGE]),
                mode: MODE_LEXICAL.into(),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(r.total, 0, "the old title is gone from the text: {r:?}");
    // a node with the same id is another document
    x.on_node(
        &ctx,
        &NodeEvent {
            id: "C1".into(),
            version: 1,
            namespace: "alm".into(),
            r#type: "T".into(),
            key: "K".into(),
            branch: "main".into(),
            ..Default::default()
        },
    )
    .await
    .unwrap();
    x.on_change(
        &ctx,
        &ChangeDocEvent {
            id: "C1".into(),
            deleted: true,
            ..Default::default()
        },
        "",
    )
    .await
    .unwrap();
    let r = se
        .search(
            &ctx,
            Query {
                filter: kinds(&[KIND_CHANGE, KIND_NODE]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(r.total == 1 && r.hits[0].kind == KIND_NODE, "purge: {r:?}");
}

async fn search_modes(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    seed_docs(&s, bag()).await;
    let change = kinds(&[KIND_CHANGE]);
    let with = searcher(&s, bag());
    let without = searcher(&s, None);
    let failing = searcher(&s, Some(Arc::new(Failing)));
    let q = |text: &str, mode: &str, min: Option<f64>| Query {
        text: text.into(),
        mode: mode.into(),
        filter: change.clone(),
        min_similarity: min,
        ..Default::default()
    };

    // semantic requires the embedder
    let err = without
        .search(&ctx, q("password", MODE_SEMANTIC, None))
        .await
        .unwrap_err();
    assert!(err.is_semantic_unavailable(), "no embedder: {err}");
    let err = failing
        .search(&ctx, q("password", MODE_SEMANTIC, None))
        .await
        .unwrap_err();
    assert!(err.is_semantic_unavailable(), "failing embedder: {err}");
    assert_eq!(
        err.to_string(),
        "semantic search is unavailable: no embedding model answers: down"
    );
    let err = with
        .search(&ctx, q("", MODE_SEMANTIC, None))
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "semantic without text: {err}");
    let err = with
        .search(
            &ctx,
            Query {
                text: "x".into(),
                mode: "fuzzy".into(),
                ..Default::default()
            },
        )
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "unknown mode: {err}");
    assert_eq!(err.to_string(), r#"invalid search: unknown mode "fuzzy""#);
    // hybrid degrades
    for (name, se) in [("no embedder", &without), ("failing", &failing)] {
        let r = se.search(&ctx, q("password", "", None)).await.unwrap();
        assert!(!r.semantic && r.total == 2, "hybrid with {name}: {r:?}");
    }
    // each mode
    let r = with
        .search(&ctx, q("password", MODE_LEXICAL, None))
        .await
        .unwrap();
    assert!(!r.semantic && r.total == 2, "lexical: {r:?}");
    let r = with
        .search(&ctx, q("password", MODE_SEMANTIC, Some(0.01)))
        .await
        .unwrap();
    assert!(
        r.semantic
            && r.total > 0
            && r.hits[0].similarity > 0.0
            && r.hits[0].score == r.hits[0].similarity,
        "semantic: {r:?}"
    );
    let r = with
        .search(&ctx, q("password", "", Some(0.0)))
        .await
        .unwrap();
    assert!(r.semantic && r.hits[0].similarity != 0.0, "hybrid: {r:?}");
    // the floor of the request replaces the global one
    let all = with
        .search(&ctx, q("password", MODE_SEMANTIC, Some(0.0)))
        .await
        .unwrap();
    let none = with
        .search(&ctx, q("password", MODE_SEMANTIC, Some(0.999)))
        .await
        .unwrap();
    assert!(
        all.total == 3 && none.total == 0,
        "min similarity: {} {}",
        all.total,
        none.total
    );
    let mut strict = searcher(&s, bag());
    strict.min_similarity = 0.999;
    let r = strict
        .search(&ctx, q("password", MODE_SEMANTIC, None))
        .await
        .unwrap();
    assert_eq!(r.total, 0, "global floor");
    let r = strict
        .search(&ctx, q("password", MODE_SEMANTIC, Some(0.0)))
        .await
        .unwrap();
    assert_eq!(r.total, 3, "the request overrides the global floor");
    for bad in [-0.1, 1.1] {
        let err = with
            .search(
                &ctx,
                Query {
                    text: "x".into(),
                    min_similarity: Some(bad),
                    ..Default::default()
                },
            )
            .await
            .unwrap_err();
        assert!(err.is_invalid(), "floor {bad}: {err}");
    }
}

fn change_ref(id: &str) -> Option<Ref> {
    Some(Ref {
        kind: KIND_CHANGE.into(),
        id: id.into(),
        version: 0,
    })
}

async fn similar_to(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    let x = seed_docs(&s, bag()).await;
    // a change nearly the same as C1, and an unembedded one
    x.on_change(
        &ctx,
        &change_ev(
            "C4",
            "Reset password flow again",
            "Rework how users reset the password by mail",
            "P1",
            "ORG-A",
            "",
            domain::CHANGE_ACTIVE,
            &["REQ-1"],
        ),
        "",
    )
    .await
    .unwrap();
    let bare = indexer(&s, None);
    bare.on_change(
        &ctx,
        &change_ev(
            "C5",
            "No embedding",
            "none",
            "P1",
            "ORG-A",
            "",
            domain::CHANGE_ACTIVE,
            &[],
        ),
        "",
    )
    .await
    .unwrap();
    let calls = Arc::new(CountEmbedder::default());
    let mut se = searcher(&s, Some(calls.clone()));
    se.min_similarity = 0.01;
    let change = kinds(&[KIND_CHANGE]);
    let like = |r: Option<Ref>, f: &Filter| Query {
        similar_to: r,
        filter: f.clone(),
        ..Default::default()
    };

    let r = se
        .search(&ctx, like(change_ref("C1"), &change))
        .await
        .unwrap();
    assert!(r.semantic && !r.hits.is_empty(), "{r:?}");
    assert_eq!(calls.calls(), 0, "similar_to embeds nothing");
    for h in &r.hits {
        assert!(h.id != "C1", "excludes itself");
    }
    assert_eq!(r.hits[0].id, "C4", "nearest");
    // the filters hold
    let r = se
        .search(
            &ctx,
            like(
                change_ref("C1"),
                &Filter {
                    kind: strs(&[KIND_CHANGE]),
                    project: strs(&["P2"]),
                    ..Default::default()
                },
            ),
        )
        .await
        .unwrap();
    assert!(r.total == 1 && r.hits[0].id == "C2", "filtered: {r:?}");
    // a node to documents of both kinds; itself excluded whatever its version
    let r = se
        .search(
            &ctx,
            like(
                Some(Ref {
                    id: "n1".into(),
                    ..Default::default()
                }),
                &kinds(&[KIND_NODE, KIND_CHANGE]),
            ),
        )
        .await
        .unwrap();
    assert!(!r.hits.is_empty(), "node: {r:?}");
    for h in &r.hits {
        assert!(!(h.id == "n1" && h.kind == KIND_NODE), "excludes itself");
    }
    assert!(
        r.hits[0].id == "C1" || r.hits[0].id == "C4",
        "nearest change = {:?}",
        r.hits[0]
    );
    // the same id of another kind is another document
    let err = se
        .search(&ctx, like(change_ref("n1"), &change))
        .await
        .unwrap_err();
    assert!(err.is_not_found(), "not found: {err}");
    let err = se
        .search(&ctx, like(change_ref("C5"), &change))
        .await
        .unwrap_err();
    assert!(err.is_semantic_unavailable(), "not embedded: {err}");
    // the source is authorized too: a document the caller cannot see is not found
    se.authorize = Some(Arc::new(|_: &Ctx, hits: Vec<Hit>| {
        Ok(hits.into_iter().filter(|h| h.id != "C1").collect())
    }));
    let err = se
        .search(&ctx, like(change_ref("C1"), &change))
        .await
        .unwrap_err();
    assert!(err.is_not_found(), "hidden source: {err}");
    let r = se
        .search(&ctx, like(change_ref("C4"), &change))
        .await
        .unwrap();
    assert!(r.total > 0, "{r:?}");
    for h in &r.hits {
        assert!(h.id != "C1", "hidden hit");
    }
}

async fn snippets(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    let long = format!(
        "{}The quarantine rule is the one that matters. {}",
        "filler words go on and on. ".repeat(40),
        "more filler. ".repeat(40)
    );
    let x = indexer(&s, None);
    x.on_change(
        &ctx,
        &change_ev(
            "C1",
            "Title",
            &long,
            "P1",
            "ORG-A",
            "",
            domain::CHANGE_ACTIVE,
            &[],
        ),
        "",
    )
    .await
    .unwrap();
    let se = searcher(&s, None);
    let r = se
        .search(
            &ctx,
            Query {
                text: "quarantine".into(),
                filter: kinds(&[KIND_CHANGE]),
                snippet: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(r.total, 1, "{r:?}");
    let sn = &r.hits[0].snippet;
    assert!(
        sn.contains("quarantine") && sn.chars().count() <= SNIPPET_MAX,
        "snippet ({}): {sn:?}",
        sn.chars().count()
    );
    let r = se
        .search(
            &ctx,
            Query {
                text: "quarantine".into(),
                filter: kinds(&[KIND_CHANGE]),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(r.hits[0].snippet, "", "no snippet unless asked");
    // no overlap with the text (a semantic match): the beginning of the document, bounded
    let r = se
        .search(
            &ctx,
            Query {
                filter: kinds(&[KIND_CHANGE]),
                snippet: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let sn = &r.hits[0].snippet;
    assert!(
        !sn.is_empty() && sn.chars().count() <= SNIPPET_MAX,
        "snippet {sn:?}"
    );
}

#[test]
fn change_text_is_bounded() {
    let mut ev = change_ev(
        "C1",
        &"t".repeat(5000),
        &"i".repeat(5000),
        "P",
        "O",
        "",
        domain::CHANGE_ACTIVE,
        &[],
    );
    for _ in 0..300 {
        ev.impacts.push(ChangeDocRef {
            key: "k".repeat(30),
            r#type: "alm@X".into(),
        });
    }
    let n = change_text(&ev).chars().count();
    assert!(n <= MAX_CHANGE_TEXT, "text of {n} runes");
    let txt = change_text(&change_ev(
        "C1",
        "T",
        "I",
        "P",
        "O",
        "",
        domain::CHANGE_ACTIVE,
        &["REQ-1"],
    ));
    for want in [
        "title: T",
        "intent: I",
        "methodology: sdlc",
        "namespace: alm",
        "nodes: REQ-1 (alm@Requirement)",
    ] {
        assert!(txt.contains(want), "{txt:?} misses {want:?}");
    }
}

async fn node_project_and_owner_facets(s: Arc<dyn Store>) {
    let ctx = Ctx::background();
    seed_docs(&s, None).await;
    let r = searcher(&s, None)
        .search(
            &ctx,
            Query {
                facets: strs(&[FACET_PROJECT, FACET_OWNER]),
                filter: Filter {
                    project: strs(&["P1"]),
                    ..Default::default()
                },
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert!(
        r.total == 1
            && r.hits[0].project == "P1"
            && r.hits[0].owner == "ORG-A"
            && r.facets[FACET_PROJECT][0] == FacetCount::new("P1", 1),
        "{r:?}"
    );
}
