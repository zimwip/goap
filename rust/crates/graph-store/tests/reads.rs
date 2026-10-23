//! The reads of graph-core over the SQLite store, on rows written as the Go service writes them: changes and their
//! pages, the changes of a node, the head of a branch and its selection, the difference of two baselines.
use graph_core::{
    change::{self, ChangesFilter},
    model::{BaselineId, ChangeId, NodeId},
    read,
    store::ReadTx,
};
use graph_store::{Store, StoreConfig};

const ORG: &str = "lnk:organisation/OrgUnit/ORG-DEFAULT";
const PROJ: &str = "lnk:organisation/ProjectUnit/PROJ-ROOT";
const N1: &str = "00000000-0000-4000-8000-000000000001";

/// Two changes on main of `bench` writing two versions of one node, a sub-change merged into the second, a personal
/// change of ann, and the two baselines they left.
const FIXTURE: &str = r#"
INSERT INTO change (id, title, status, created_at, namespace, owner_org, project_id, parent_id) VALUES
 ('c1', 'First', 'applied', '2026-10-23T10:00:01.000000000Z', 'bench', '{ORG}', '{PROJ}', NULL),
 ('c2', 'Second', 'active', '2026-10-23T10:00:02.000000000Z', 'bench', '{ORG}', '{PROJ}', NULL),
 ('c3', 'Mine', 'active', '2026-10-23T10:00:03.000000000Z', 'bench', 'lnk:organisation/User/USR:ann', '{PROJ}', NULL),
 ('c4', 'Part of the second', 'applied', '2026-10-23T10:00:04.000000000Z', 'bench', '{ORG}', '{PROJ}', 'c2');
INSERT INTO baseline (id, name, parent_id, change_id, created_at, branch, namespace, kind, seq) VALUES
 ('b1', '', NULL, 'c1', '2026-10-23T10:00:01.500000000Z', 'main', 'bench', 'commit', 1),
 ('b2', '', 'b1', 'c2', '2026-10-23T10:00:02.500000000Z', 'main', 'bench', 'commit', 2);
INSERT INTO branch (namespace, name, parent, head_baseline, status, created_at) VALUES
 ('bench', 'main', '', 'b2', 'open', '2026-10-23T10:00:00.000000000Z');
INSERT INTO node (id, namespace, key, type, latest, project) VALUES ('{N1}', 'bench', 'K-1', 'bench@T1', 2, '{PROJ}');
INSERT INTO node_version (node_id, version, props, change_id, owner, created_at, branch, reason, committed) VALUES
 ('{N1}', 1, '{"title":"one"}', 'c1', '{ORG}', '2026-10-23T10:00:01.200000000Z', 'main', 'create', 1),
 ('{N1}', 2, '{"title":"two"}', 'c2', '{ORG}', '2026-10-23T10:00:02.200000000Z', 'main', 'revise', 2);
INSERT INTO change_impact (id, seq, change_id, node_id, key, type, intent, rationale, pre_version, post_version, created_at) VALUES
 ('i1', 1, 'c1', '{N1}', 'K-1', 'bench@T1', 'created', '', NULL, 1, '2026-10-23T10:00:01.100000000Z'),
 ('i2', 2, 'c2', '{N1}', 'K-1', 'bench@T1', 'modified', '', 1, 2, '2026-10-23T10:00:02.100000000Z');
"#;

async fn store() -> (tempfile::TempDir, Store) {
    let dir = tempfile::tempdir().unwrap();
    let path = dir.path().join("graph.db").display().to_string();
    let store = Store::open(&StoreConfig::Sqlite { path, readers: 2 }).await.unwrap();
    let Store::Sqlite(db) = &store else { unreachable!() };
    let sql = FIXTURE
        .replace("{ORG}", ORG)
        .replace("{PROJ}", PROJ)
        .replace("{N1}", N1);
    db.write(move |c| Ok(c.execute_batch(&sql)?)).await.unwrap();
    (dir, store)
}

fn ids(cs: &[change::Change]) -> Vec<&str> {
    cs.iter().map(|c| c.id.as_str()).collect()
}

#[tokio::test]
async fn lists_and_pages_changes() {
    let (_dir, store) = store().await;
    let tx = store.reader().await.unwrap();
    let active = ChangesFilter {
        namespace: "bench".into(),
        status: vec!["active".into()],
        ..ChangesFilter::default()
    };
    // oldest first, the personal change of ann seen by ann only
    assert_eq!(ids(&tx.change_heads(&active).await.unwrap()), ["c2", "c3"]);
    let p = change::changes_page(&tx, &active, 50, "", |c| c.visible_to("system:bench"))
        .await
        .unwrap();
    assert_eq!((ids(&p.changes), p.total), (vec!["c2"], 1));
    // newest first, a page at a time
    let all = ChangesFilter::default();
    let p = change::changes_page(&tx, &all, 2, "", |_| true).await.unwrap();
    assert_eq!((ids(&p.changes), p.total), (vec!["c4", "c3"], 4));
    assert_eq!(p.next, "2026-10-23T10:00:03Z|c3");
    let p = change::changes_page(&tx, &all, 2, &p.next, |_| true).await.unwrap();
    assert_eq!((ids(&p.changes), p.next.as_str()), (vec!["c2", "c1"], ""));
    // the text query
    let q = ChangesFilter {
        query: "PART".into(),
        ..ChangesFilter::default()
    };
    let p = change::changes_page(&tx, &q, 50, "", |_| true).await.unwrap();
    assert_eq!(ids(&p.changes), ["c4"]);
    let subs = ChangesFilter {
        parent: ChangeId::from("c2"),
        ..ChangesFilter::default()
    };
    assert_eq!(ids(&tx.change_heads(&subs).await.unwrap()), ["c4"]);
}

#[tokio::test]
async fn pages_the_changes_of_a_node() {
    let (_dir, store) = store().await;
    let tx = store.reader().await.unwrap();
    let n1 = NodeId::from(N1);
    let (cs, next) = change::node_changes(&tx, &n1, 0, 50).await.unwrap();
    assert_eq!((ids(&cs), next), (vec!["c1", "c2"], 0));
    let (cs, next) = change::node_changes(&tx, &n1, 0, 1).await.unwrap();
    assert_eq!((ids(&cs), next), (vec!["c2"], 2));
    let (cs, next) = change::node_changes(&tx, &n1, next, 1).await.unwrap();
    assert_eq!((ids(&cs), next), (vec!["c1"], 0));
}

#[tokio::test]
async fn reads_a_head_and_diffs_two_baselines() {
    let (_dir, store) = store().await;
    let tx = store.reader().await.unwrap();
    let head = read::branch_head(&tx, "bench", "main").await.unwrap();
    assert_eq!(head.id.as_str(), "b2");
    let state = read::select(&tx, &head, None).await.unwrap();
    assert_eq!(state.get(&NodeId::from(N1)), Some(&2));
    let (b1, b2) = (BaselineId::from("b1"), BaselineId::from("b2"));
    let (ds, gap) = change::diff_baselines(&tx, &b1, &b2).await.unwrap();
    assert_eq!(ds.len(), 1);
    let d = &ds[0];
    assert_eq!((d.key.as_str(), d.kind), ("K-1", change::DIFF_CHANGED));
    assert_eq!(
        (d.from.as_ref().unwrap().version, d.to.as_ref().unwrap().version),
        (1, 2)
    );
    // the sub-change merged into c2 is in its stack, before it
    let added: Vec<&str> = gap.added.iter().map(ChangeId::as_str).collect();
    assert_eq!(added, ["c4", "c2"]);
    assert!(gap.removed.is_empty());
    let (back, gap) = change::diff_baselines(&tx, &b2, &b1).await.unwrap();
    assert_eq!(back[0].kind, change::DIFF_CHANGED);
    assert_eq!(gap.removed.len(), 2);
    // an unknown baseline
    let err = change::diff_baselines(&tx, &b1, &BaselineId::from("nope"))
        .await
        .unwrap_err();
    assert_eq!(err.message, "baseline nope: not found");
}
