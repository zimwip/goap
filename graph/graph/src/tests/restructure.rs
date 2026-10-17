//! Port of `restructure_test.go`: merge and split as modifications of the parents (ADR 0077, 0102).

use std::collections::HashMap;
use std::sync::Arc;
use std::time::Duration;

use goap_domain::*;
use goap_graphapi::{MergeInput, NodeCheckout, NodeCreate, NodeName, Restructured, SplitInput};
use parking_lot::Mutex;

use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{Error, ErrorKind, Graph, NewChange};

/// The domain of the tests: a Folder holds Items (contains is a composition), a Note points at an Item (refs is not).
const RESTRUCTURE_DOMAIN: &str = r#"
name: docs
version: 1.0.0
nodeTypes:
  - {name: Folder}
  - {name: Item}
  - {name: Note}
linkTypes:
  - {name: contains, from: Folder, to: Item, compose: true}
  - {name: refs, from: Note, to: Item}
"#;

pub(crate) struct Docs {
    pub(crate) g: Graph,
}

pub(crate) async fn new_docs(r: &TestRepo) -> Docs {
    let d = goap_def::parse_domain(RESTRUCTURE_DOMAIN.as_bytes()).expect("domain");
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    let mut g = r.graph();
    use_types(&mut g, Arc::new(cat));
    must(g.bootstrap(&ctx()).await);
    Docs { g }
}

impl Docs {
    pub(crate) async fn node(&self, key: &str, typ: &str) -> Node {
        must(
            import_node(
                &self.g,
                ImportNode {
                    namespace: "docs".into(),
                    key: key.into(),
                    r#type: format!("docs@{typ}"),
                    ..Default::default()
                },
            )
            .await,
        )
    }

    pub(crate) async fn link(&self, typ: &str, from: &Node, to: &Node) {
        must(
            import_link(
                &self.g,
                &format!("docs@{typ}"),
                &from.r#ref(),
                &to.r#ref(),
                Default::default(),
            )
            .await,
        );
    }

    pub(crate) async fn change(&self) -> Change {
        let head = must(self.g.branch_head(&ctx(), "docs", MAIN_BRANCH).await);
        must(
            self.g
                .create_change(
                    &ctx(),
                    NewChange {
                        project_id: "PROJ-ROOT".into(),
                        namespace: "docs".into(),
                        title: "restructure".into(),
                        intent: "restructure".into(),
                        baseline_id: head.id,
                        own_branch: true,
                        ..Default::default()
                    },
                )
                .await,
        )
    }

    /// outKeys are the keys the latest version of a node on main links to, as "type:key".
    async fn out_keys(&self, key: &str) -> Vec<String> {
        let n = must(self.g.node_by_key(&ctx(), "docs", key).await);
        let mut out = Vec::new();
        for l in must(self.g.out_links_of(&ctx(), &n.r#ref()).await) {
            let to = must(self.g.node(&ctx(), &l.to).await);
            out.push(format!("{}:{}", l.r#type, to.key));
        }
        out.sort();
        out
    }

    /// draftLinks are the keys the draft of a change impact links to (the change's view, ADR 0079).
    async fn draft_links(&self, c: &Change, post: &NodeRef) -> Vec<String> {
        let mut held = Vec::new();
        for l in must(self.g.change_node_view(&ctx(), &c.id, "", post).await).out {
            held.push(must(self.g.change_node(&ctx(), &c.id, "", &l.to).await).key);
        }
        held.sort();
        held
    }

    pub(crate) async fn merge(&self, c: &Change, into: &str) -> crate::Result<Restructured> {
        self.g
            .impact_node_merge(
                &ctx(),
                &c.id,
                MergeInput {
                    sources: vec![name("A"), name("B")],
                    into: item(into, "merge"),
                    ..Default::default()
                },
            )
            .await
    }
}

pub(crate) fn name(key: &str) -> NodeName {
    NodeName {
        key: key.into(),
        ..Default::default()
    }
}

pub(crate) fn item(key: &str, rationale: &str) -> NodeCreate {
    NodeCreate {
        key: key.into(),
        r#type: "docs@Item".into(),
        rationale: rationale.into(),
        ..Default::default()
    }
}

fn impact_of(list: &[ChangeImpact], key: &str) -> ChangeImpact {
    list.iter()
        .find(|cn| cn.key == key)
        .cloned()
        .unwrap_or_else(|| panic!("no change impact of {key}"))
}

fn any_version(id: &NodeID) -> NodeRef {
    NodeRef {
        id: id.clone(),
        version: 0,
    }
}

#[tokio::test]
async fn merge_from_the_parent() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b, x) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
            d.node("X", "Item").await,
        );
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        d.link("contains", &f, &x).await;
        let a1 = must(g.node_by_key(&ctx, "docs", "A").await);
        let b1 = must(g.node_by_key(&ctx, "docs", "B").await);

        let c = d.change().await;
        let res = must(
            g.impact_node_merge(
                &ctx,
                &c.id,
                MergeInput {
                    sources: vec![name("A"), name("B")],
                    into: item("C", "merge A and B"),
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            res.successors.len() == 1
                && res.successors[0].post.is_some()
                && res.parents.len() == 1
                && res.sources.len() == 2
                && res.suspect.is_empty(),
            "result: {res:?}"
        );
        let parent = res.parents[0].clone();
        assert!(
            parent.key == "F" && parent.intent == INTENT_MODIFIED,
            "the parent is modified: {parent:?}"
        );
        for s in &res.sources {
            assert!(
                s.via == parent.id && s.intent == INTENT_MODIFIED && s.post.is_none(),
                "source {}: via {:?}, intent {:?}, post {:?}; want via {}, modified, no version",
                s.key,
                s.via,
                s.intent,
                s.post,
                parent.id
            );
        }
        // lineage, on the draft of the successor: no version exists yet
        let succ_post = res.successors[0].post.clone().unwrap();
        let cn = must(g.change_node(&ctx, &c.id, "", &succ_post).await);
        assert!(
            cn.is_draft() && cn.origins == vec![a1.r#ref(), b1.r#ref()],
            "origins of C: {} {:?}",
            cn.version,
            cn.origins
        );
        let got = must(g.derived_nodes(&ctx, &a1.r#ref()).await);
        assert!(got.is_empty(), "a draft derives nothing: {got:?}");
        // the draft of the parent: X and C, no A, no B
        let held = d.draft_links(&c, parent.post.as_ref().unwrap()).await;
        assert_eq!(held, vec!["C", "X"], "parent: links");
        // the events: created with its origins, the parent updated with the patches
        let (mut created, mut removed, mut added) = (0, 0, 0);
        for e in must(g.change_events(&ctx, &c.id).await) {
            if e.op == IMPACT_CREATED && e.patch.get("origins").is_some_and(|v| !v.is_null()) {
                created += 1;
            } else if e.op == IMPACT_UPDATED
                && e.patch.get("removeLink").is_some_and(|v| !v.is_null())
            {
                removed += 1;
            } else if e.op == IMPACT_UPDATED && e.patch.get("addLink").is_some_and(|v| !v.is_null())
            {
                added += 1;
            }
        }
        assert!(
            created == 1 && removed == 2 && added == 1,
            "events: created {created}, removed {removed}, added {added}"
        );
        // the review gate: C waits for its origins, decided by the impact of their parent (via, ADR 0102); a source
        // modifies nothing itself, it is not accepted
        assert_kind(
            g.impact_node_review(
                &ctx,
                &c.id,
                &res.successors[0].id,
                REVIEW_ACCEPTED,
                "tester",
                "ok",
            )
            .await,
            ErrorKind::Conflict,
            "accepting C before the merge of A and B",
        );
        assert_kind(
            g.impact_node_review(
                &ctx,
                &c.id,
                &impact_of(&res.sources, "A").id,
                REVIEW_ACCEPTED,
                "tester",
                "ok",
            )
            .await,
            ErrorKind::Invalid,
            "a source of the merge modifies nothing",
        );
        must(accept_impact(g, &c.id, &parent.id, "").await);
        must(accept_impact(g, &c.id, &res.successors[0].id, "").await);
        let base = must(apply(g, &c.id, "").await);

        // the lineage, on the first version of the successor written at landing
        let landed_c = must(g.node_by_key(&ctx, "docs", "C").await);
        assert!(
            landed_c.version == 1 && landed_c.origins == vec![a1.r#ref(), b1.r#ref()],
            "origins of the landed C: v{} {:?}",
            landed_c.version,
            landed_c.origins
        );
        for o in [&a1, &b1] {
            let got = must(g.derived_nodes(&ctx, &o.r#ref()).await);
            assert!(
                got.len() == 1 && got[0].id == landed_c.id,
                "DerivedNodes({}) = {got:?}",
                o.key
            );
            let got = must(g.derived_nodes(&ctx, &any_version(&o.id)).await);
            assert_eq!(got.len(), 1, "DerivedNodes({}, any version)", o.key);
            let got = must(
                g.derived_nodes(
                    &ctx,
                    &NodeRef {
                        id: o.id.clone(),
                        version: o.version + 5,
                    },
                )
                .await,
            );
            assert!(got.is_empty(), "DerivedNodes of another version = {got:?}");
        }
        let got = must(g.derived_nodes(&ctx, &x.r#ref()).await);
        assert!(got.is_empty(), "X derives nothing: {got:?}");
        // one baseline holds it all, and nothing disappeared
        let (nodes, _) = must(g.baseline_graph(&ctx, &base.id).await);
        let keys: std::collections::HashSet<_> = nodes.iter().map(|n| n.key.clone()).collect();
        for k in ["A", "B", "C", "F", "X"] {
            assert!(keys.contains(k), "the baseline lost {k}");
        }
        assert_eq!(
            d.out_keys("F").await,
            vec!["docs@contains:C", "docs@contains:X"],
            "F after the landing"
        );
        let after = must(g.node_by_key(&ctx, "docs", "A").await);
        assert_eq!(after.version, a1.version, "A was written");
        let done = must(g.change(&ctx, &c.id).await);
        for cn in &done.nodes {
            assert!(
                !(cn.review == REVIEW_ACCEPTED && cn.post.is_some() && cn.landed.is_none()),
                "{} did not land",
                cn.key
            );
        }
    })
    .await;
}

// Two parents of a source: each is modified, and the source is realized through the first.
#[tokio::test]
async fn merge_two_parents() {
    for_each_repo(async |r: &TestRepo| {
        let d = new_docs(r).await;
        let (f1, f2) = (d.node("F1", "Folder").await, d.node("F2", "Folder").await);
        let (a, b) = (d.node("A", "Item").await, d.node("B", "Item").await);
        d.link("contains", &f1, &a).await;
        d.link("contains", &f2, &a).await;
        d.link("contains", &f2, &b).await;
        let c = d.change().await;
        let res = must(d.merge(&c, "C").await);
        assert_eq!(
            res.parents.len(),
            2,
            "one modification per parent: {:?}",
            res.parents
        );
        let (f1i, f2i) = (impact_of(&res.parents, "F1"), impact_of(&res.parents, "F2"));
        assert_eq!(
            impact_of(&res.sources, "A").via,
            f1i.id,
            "A is realized through the first parent F1"
        );
        assert_eq!(
            impact_of(&res.sources, "B").via,
            f2i.id,
            "B is realized through F2"
        );
        for p in &res.parents {
            let ls = d.draft_links(&c, p.post.as_ref().unwrap()).await;
            assert_eq!(ls, vec!["C"], "{} links", p.key);
        }
    })
    .await;
}

// The parent's impact is reused when the change already holds it.
#[tokio::test]
async fn merge_reuses_the_parent_impact() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let f = d.node("F", "Folder").await;
        let (a, b) = (d.node("A", "Item").await, d.node("B", "Item").await);
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        let c = d.change().await;
        let fi = must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    key: "F".into(),
                    rationale: "edit".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        let res = must(d.merge(&c, "C").await);
        assert!(
            res.parents[0].id == fi.id && res.sources[0].via == fi.id,
            "the impact of F is reused: {res:?}"
        );
        let got = must(g.list_change_impacts(&ctx, &c.id).await);
        assert_eq!(got.len(), 4, "F, A, B, C");
    })
    .await;
}

#[tokio::test]
async fn split() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, n) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("N", "Note").await,
        );
        d.link("contains", &f, &a).await;
        d.link("refs", &n, &a).await;
        let c = d.change().await;
        let res = must(
            g.impact_node_split(
                &ctx,
                &c.id,
                SplitInput {
                    source: name("A"),
                    into: vec![item("C", "split"), item("D", "split")],
                    ..Default::default()
                },
            )
            .await,
        );
        assert!(
            res.successors.len() == 2
                && res.parents.len() == 1
                && res.sources.len() == 1
                && res.sources[0].via == res.parents[0].id,
            "result: {res:?}"
        );
        // the non-compose link is left and reported
        assert!(
            res.suspect.len() == 1
                && res.suspect[0].from_key == "N"
                && res.suspect[0].to_key == "A"
                && res.suspect[0].r#type == "docs@refs",
            "suspect: {:?}",
            res.suspect
        );
        let held = d
            .draft_links(&c, res.parents[0].post.as_ref().unwrap())
            .await;
        assert_eq!(held, vec!["C", "D"], "F links");
        for s in &res.successors {
            let got = must(
                g.change_node(&ctx, &c.id, "", s.post.as_ref().unwrap())
                    .await,
            );
            assert_eq!(got.origins, vec![a.r#ref()], "{} origins", s.key);
        }
        // the split is decided by the impact of the parent: then the successors (ADR 0102)
        must(accept(g, &c.id, &res.parents[0].id, "tester", "ok").await);
        for s in &res.successors {
            must(accept(g, &c.id, &s.id, "tester", "ok").await);
        }
        must(apply(g, &c.id, "").await);
        let got = must(g.derived_nodes(&ctx, &a.r#ref()).await);
        assert_eq!(got.len(), 2, "A derives into C and D: {got:?}");
        // N was not touched
        for cn in must(g.list_change_impacts(&ctx, &c.id).await) {
            assert_ne!(cn.key, "N", "N is not part of the change");
        }
    })
    .await;
}

// A merge moves the other links to the sources onto the new node.
#[tokio::test]
async fn merge_retargets_other_links() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b, n) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
            d.node("N", "Note").await,
        );
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        d.link("refs", &n, &a).await;
        d.link("refs", &n, &b).await;
        let c = d.change().await;
        let res = must(d.merge(&c, "C").await);
        assert!(
            res.parents.len() == 2 && res.suspect.is_empty(),
            "F and N are modified: {res:?}"
        );
        let nv = must(
            g.change_node_view(
                &ctx,
                &c.id,
                "",
                impact_of(&res.parents, "N").post.as_ref().unwrap(),
            )
            .await,
        );
        assert!(
            nv.out.len() == 1
                && nv.out[0].r#type == "docs@refs"
                && must(g.change_node(&ctx, &c.id, "", &nv.out[0].to).await).key == "C",
            "N links {:?}",
            nv.out
        );
        // the sources are realized through their parent, not through N
        assert_eq!(
            impact_of(&res.sources, "A").via,
            impact_of(&res.parents, "F").id,
            "A is realized through F"
        );
    })
    .await;
}

#[tokio::test]
async fn restructure_refusals() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
        );
        d.node("O", "Item").await; // an orphan
        d.node("T", "Note").await;
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        let c = d.change().await;
        let merge = async |sources: &[&str], into: NodeCreate| {
            g.impact_node_merge(
                &ctx,
                &c.id,
                MergeInput {
                    sources: sources.iter().map(|k| name(k)).collect(),
                    into,
                    ..Default::default()
                },
            )
            .await
        };
        let into = item("C", "merge");
        let bare = |typ: &str| NodeCreate {
            key: "C".into(),
            r#type: typ.into(),
            ..Default::default()
        };
        assert_kind(
            merge(&["A", "O"], into.clone()).await,
            ErrorKind::Invalid,
            "a source with no parent",
        );
        assert_kind(
            merge(&["A"], into.clone()).await,
            ErrorKind::Invalid,
            "a merge of one node",
        );
        assert_kind(
            merge(&["A", "A"], into.clone()).await,
            ErrorKind::Invalid,
            "a node twice",
        );
        assert_kind(
            merge(&["A", "B"], bare("docs@Note")).await,
            ErrorKind::Invalid,
            "a type incompatible with the sources",
        );
        assert_kind(
            g.impact_node_split(
                &ctx,
                &c.id,
                SplitInput {
                    source: name("A"),
                    into: vec![bare("docs@Item")],
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Invalid,
            "a split into one",
        );
        assert_kind(
            merge(&["F", "A"], bare("docs@Item")).await,
            ErrorKind::Invalid,
            "a parent merged with its part",
        );
        // a refused call declares nothing
        let got = must(g.list_change_impacts(&ctx, &c.id).await);
        assert!(got.is_empty(), "a refusal left {} impacts", got.len());
        // a source checked out by the change
        must(
            g.impact_node_checkout(
                &ctx,
                &c.id,
                NodeCheckout {
                    key: "B".into(),
                    rationale: "edit".into(),
                    ..Default::default()
                },
            )
            .await,
        );
        assert_kind(
            merge(&["A", "B"], into).await,
            ErrorKind::Conflict,
            "a source checked out",
        );
    })
    .await;
}

// What a change creates cannot be merged: it has no parent in the baseline.
#[tokio::test]
async fn restructure_refuses_created_sources() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        d.node("A", "Item").await;
        let c = d.change().await;
        must(d.g.impact_node_create(&ctx, &c.id, item("N1", "new")).await);
        assert_kind(
            d.g.impact_node_merge(
                &ctx,
                &c.id,
                MergeInput {
                    sources: vec![name("A"), name("N1")],
                    into: NodeCreate {
                        key: "C".into(),
                        r#type: "docs@Item".into(),
                        ..Default::default()
                    },
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Invalid,
            "a created source",
        );
    })
    .await;
}

// The gate holds again at the commit, when an origin goes back to proposed after the review of the successor.
#[tokio::test]
async fn origins_gate_at_landing() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
        );
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        let c = d.change().await;
        let res = must(d.merge(&c, "C").await);
        must(accept_impact(g, &c.id, &res.parents[0].id, "").await);
        must(accept_impact(g, &c.id, &res.successors[0].id, "").await);
        // the merge goes back to proposed (its parent's impact, which decides the origins): the successor no longer
        // lands
        must(
            g.reopen_impacts(
                &ctx,
                &c.id,
                std::slice::from_ref(&res.parents[0].id),
                "rethink",
            )
            .await,
        );
        assert_kind(
            commit_change(g, &c.id, "").await,
            ErrorKind::Conflict,
            "landing with an origin awaiting its review",
        );
        must(
            g.impact_node_review(
                &ctx,
                &c.id,
                &res.parents[0].id,
                REVIEW_ACCEPTED,
                "tester",
                "ok again",
            )
            .await,
        );
        must(apply(g, &c.id, "").await);
    })
    .await;
}

// The export names what a successor derives from (ADR 0077).
#[tokio::test]
async fn merge_provenance() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
        );
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        let c = d.change().await;
        let res = must(d.merge(&c, "C").await);
        let (entries, _) = must(
            g.change_log(
                &ctx,
                &LogFilter {
                    change: c.id.clone(),
                    ..Default::default()
                },
            )
            .await,
        );
        let doc = goap_prov::export(&must(g.change(&ctx, &c.id).await), &entries).expect("export");
        let raw = serde_json::to_string(&doc).expect("json");
        // the draft: the node has no version yet (ADR 0079)
        let post = format!(
            "urn:goap:node:{}@v0",
            res.successors[0].post.as_ref().unwrap().id
        );
        for o in [&a, &b] {
            assert!(
                raw.contains(r#""prov:wasDerivedFrom":"#)
                    && raw.contains(&format!("urn:goap:node:{}@v1", o.id)),
                "no derivation of {} in {raw}",
                o.key
            );
        }
        let n = doc
            .graph
            .iter()
            .find(|n| n.get("@id").and_then(|v| v.as_str()) == Some(post.as_str()))
            .expect("C is not in the export");
        let got = serde_json::to_string(&n["prov:wasDerivedFrom"]).unwrap();
        assert!(
            got.contains(a.id.as_str()) && got.contains(b.id.as_str()),
            "C derives from {got}"
        );
    })
    .await;
}

// A Gate reads the graph (the access snapshot does): it must not run inside the transaction of the restructuring,
// the stores are not reentrant. It is asked outside, once per type, and its refusal wins. (The gate is synchronous:
// it reads the graph by blocking on the runtime, hence the multi-thread runtime.)
#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn restructure_gate_reads_the_graph() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let d = new_docs(r).await;
        let g = &d.g;
        let (f, a, b) = (
            d.node("F", "Folder").await,
            d.node("A", "Item").await,
            d.node("B", "Item").await,
        );
        d.link("contains", &f, &a).await;
        d.link("contains", &f, &b).await;
        let asked: Arc<Mutex<HashMap<String, i32>>> = Arc::new(Mutex::new(HashMap::new()));
        let reader = Arc::new(r.graph());
        let (asked2, reader2) = (asked.clone(), reader.clone());
        let gate: goap_graphapi::Gate = Arc::new(move |typ: &str| {
            *asked2.lock().entry(typ.to_string()).or_default() += 1;
            // a read of the graph, as the access snapshot does
            let rd = reader2.clone();
            tokio::task::block_in_place(|| {
                tokio::runtime::Handle::current().block_on(async move {
                    rd.nodes_of_type(&crate::testsupport::ctx(), "docs", "Item")
                        .await
                })
            })
            .map(|_| ())
        });
        let c = d.change().await;
        let res = tokio::time::timeout(
            Duration::from_secs(10),
            g.impact_node_merge(
                &ctx,
                &c.id,
                MergeInput {
                    sources: vec![name("A"), name("B")],
                    into: item("C", "merge"),
                    gate: Some(gate),
                    ..Default::default()
                },
            ),
        )
        .await;
        match res {
            Ok(r) => {
                must(r);
            }
            Err(_) => panic!("deadlock: the gate ran inside the transaction"),
        }
        for typ in ["docs@Item", "docs@Folder"] {
            assert!(
                asked.lock().get(typ).is_some_and(|n| *n > 0),
                "the gate was not asked about {typ}: {:?}",
                asked.lock()
            );
        }
        // a refusal of the gate wins and leaves nothing behind
        let c2 = d.change().await;
        let err = g
            .impact_node_split(
                &ctx,
                &c2.id,
                SplitInput {
                    source: name("A"),
                    into: vec![item("S1", "s"), item("S2", "s")],
                    gate: Some(Arc::new(|_: &str| Err(Error::msg("not for you")))),
                    ..Default::default()
                },
            )
            .await
            .unwrap_err();
        assert!(
            err.to_string().contains("not for you"),
            "the gate refuses: {err}"
        );
        let refs = must(g.change_impacts(&ctx, &c2.id).await);
        assert!(refs.is_empty(), "a refused split leaves nothing: {refs:?}");
    })
    .await;
}
