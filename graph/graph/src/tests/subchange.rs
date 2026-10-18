//! Sub-changes on their own branch, split by owner, merged into their parent (ADR 0016,
//! 0039, 0054, 0103).

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_ctx::Ctx;
use goap_domain::*;
use goap_graphapi::{ChangePatch, LinkWrite};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

use crate::tests::helpers::*;
use crate::tests::lifecycle::MergeGate;
use crate::testsupport::*;
use crate::{Error, ErrorKind, Graph, NewChange, Result, SubChangeValidator};

/// acme <- digital <- {team1, team2}; CMP-1 owned by team1, CMP-2 by team2, CMP-3 by the root unit.
pub(crate) struct OrgWorld {
    pub(crate) g: Graph,
    pub(crate) base: Baseline,
    pub(crate) cmp1: Node,
    pub(crate) cmp2: Node,
    pub(crate) cmp3: Node,
}

pub(crate) async fn new_org_world(r: &TestRepo) -> OrgWorld {
    let ctx = ctx();
    let g = r.graph();
    must(g.bootstrap(&ctx).await);
    let root = must(g.node_by_key(&ctx, "organisation", &root_org(&g)).await);
    // a unit is created with its parent (ADR 0054)
    let mk = async |key: &str, name: &str, parent: &Node| {
        must(
            import_node(
                &g,
                ImportNode {
                    namespace: "organisation".into(),
                    key: key.into(),
                    r#type: NODE_TYPE_ORG_UNIT.into(),
                    properties: props(&[("name", json!(name))]),
                    links: vec![LinkWrite {
                        r#type: LINK_PART_OF.into(),
                        to: parent.r#ref(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        )
    };
    let acme = mk("ORG-ACME", "Acme", &root).await;
    let digital = mk("ORG-DIGITAL", "Digital", &acme).await;
    mk("ORG-T1", "Team 1", &digital).await;
    mk("ORG-T2", "Team 2", &digital).await;
    let owned = async |key: &str, owner: &str, title: &str| {
        must(
            import_node(
                &g,
                ImportNode {
                    key: key.into(),
                    r#type: "Component".into(),
                    properties: props(&[("title", json!(title))]),
                    owner: owner.into(),
                    ..Default::default()
                },
            )
            .await,
        )
    };
    let cmp1 = owned("CMP-1", "ORG-T1", "one").await;
    let cmp2 = owned("CMP-2", "ORG-T2", "two").await;
    let cmp3 = owned("CMP-3", "", "three").await; // owned by the root unit, outside ORG-DIGITAL
    let base = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
    OrgWorld {
        g,
        base,
        cmp1,
        cmp2,
        cmp3,
    }
}

pub(crate) fn root_change(base: &Baseline, title: &str) -> NewChange {
    NewChange {
        project_id: "PROJ-ROOT".into(),
        title: title.into(),
        baseline_id: base.id.clone(),
        own_branch: true,
        ..Default::default()
    }
}

pub(crate) fn sub_change(parent: &ChangeID, title: &str) -> NewChange {
    NewChange {
        title: title.into(),
        parent_id: parent.clone(),
        ..Default::default()
    }
}

fn modified(pre: &Node, rationale: &str) -> ChangeImpact {
    ChangeImpact {
        intent: INTENT_MODIFIED,
        pre: Some(pre.r#ref()),
        rationale: rationale.into(),
        ..Default::default()
    }
}

/// The title of a node as a change sees it.
pub(crate) async fn title_in(g: &Graph, c: &ChangeID, key: &str) -> String {
    let v = must(
        g.change_node_view_by_key(&ctx(), c, "", DEFAULT_NAMESPACE, key)
            .await,
    );
    v.properties
        .get("title")
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_string()
}

fn title_props(t: &str) -> Map<String, Value> {
    props(&[("title", json!(t))])
}

#[tokio::test]
async fn split_by_owner_and_merge() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_org_world(r).await;
        let g = &w.g;
        let parent = must(
            g.create_change(
                &ctx,
                NewChange {
                    owner_org: "ORG-DIGITAL".into(),
                    ..root_change(&w.base, "Upgrade")
                },
            )
            .await,
        );
        must(
            g.propose_impact(
                &ctx,
                &parent.id,
                vec![
                    modified(&w.cmp1, "upgrade one"),
                    modified(&w.cmp2, "upgrade two"),
                    modified(&w.cmp3, "upgrade three"),
                ],
            )
            .await,
        );
        let subs = must(g.split_by_owner(&ctx, &parent.id).await);
        assert_eq!(subs.len(), 2, "split");
        let again = must(g.split_by_owner(&ctx, &parent.id).await);
        assert!(
            again.is_empty(),
            "split must be idempotent: {}",
            again.len()
        );
        let mut orgs: BTreeMap<String, Change> = BTreeMap::new();
        for s in &subs {
            assert!(
                s.parent_id == parent.id
                    && s.namespace == parent.namespace
                    && s.status == CHANGE_ACTIVE
                    && s.items.is_empty(),
                "sub-change = {s:?}"
            );
            let full = must(g.change(&ctx, &s.id).await);
            let n = &full.nodes;
            assert!(
                n.len() == 1
                    && n[0].intent == INTENT_MODIFIED
                    && n[0].pre.is_some()
                    && n[0].planned()
                    && n[0].rationale.starts_with("upgrade ")
                    && n[0].derived_from.len() == 1,
                "change impacts not copied: {n:?}"
            );
            orgs.insert(s.owner_org.clone(), s.clone());
        }
        assert!(
            orgs.contains_key("ORG-T1") && orgs.len() == 2,
            "orgs = {:?}",
            orgs.keys()
        );

        // the parent cannot apply while its sub-changes are open
        assert_kind(
            apply(g, &parent.id, "").await,
            ErrorKind::Conflict,
            "apply with open sub-changes",
        );
        // each change writes its node through its change impact and accepts it
        let write = async |c: &Change, n: &Node, t: &str| {
            let nodes = must(g.list_change_impacts(&ctx, &c.id).await);
            let cn = nodes
                .iter()
                .find(|cn| cn.key == n.key)
                .unwrap_or_else(|| panic!("no change impact for {} in {}", n.key, c.id));
            must(
                edit(
                    g,
                    &c.id,
                    &cn.id,
                    Edit {
                        properties: title_props(t),
                        ..Default::default()
                    },
                )
                .await,
            );
            must(accept(g, &c.id, &cn.id, "u", "ok").await);
        };
        write(&orgs["ORG-T1"], &w.cmp1, "one v2").await;
        write(&orgs["ORG-T2"], &w.cmp2, "two v2").await;
        write(&parent, &w.cmp3, "three v2").await;
        for s in orgs.values() {
            must(apply(g, &s.id, "").await);
            let got = must(g.change(&ctx, &s.id).await);
            assert_eq!(got.status, CHANGE_APPLIED, "{}", s.owner_org);
        }
        // merged into the parent's branch (ADR 0103): main is untouched, the parent's branch holds the versions
        let n = crate::tests::changebranch::head_node(g, &w.cmp1.id).await;
        assert!(
            n.properties["title"] == "one" && n.version == w.cmp1.version,
            "sub-change leaked on main: {n:?}"
        );
        let parent = must(g.change(&ctx, &parent.id).await);
        let n = must(g.node_by_key_on(&ctx, "", &parent.branch, "CMP-1").await);
        assert_eq!(n.properties["title"], "one v2", "parent branch");
        // the parent holds the work as its own: an impact accepted with the sub-change's review, committed
        let mut found = 0;
        for cn in &parent.nodes {
            if cn.key == "CMP-1" || cn.key == "CMP-2" {
                found += 1;
                assert!(
                    cn.review == REVIEW_ACCEPTED
                        && cn.landed.is_some()
                        && !cn.drafted()
                        && cn
                            .reviews
                            .last()
                            .is_some_and(|r| r.comment.contains("merged from sub-change")),
                    "parent impact of {} = {cn:?}",
                    cn.key
                );
            }
        }
        assert_eq!(found, 2, "the parent holds the work of its sub-changes");
        // the parent applies with what its sub-changes brought in, then merges into main
        must(apply(g, &parent.id, "").await);
        let got = must(g.change(&ctx, &parent.id).await);
        assert_eq!(got.status, CHANGE_APPLIED, "parent");
        for (key, want) in [
            ("CMP-1", "one v2"),
            ("CMP-2", "two v2"),
            ("CMP-3", "three v2"),
        ] {
            let n = must(g.node_by_key(&ctx, "", key).await);
            assert_eq!(n.properties["title"], want, "{key} on main");
        }
        let subs = must(g.sub_changes(&ctx, &parent.id).await);
        assert_eq!(subs.len(), 2, "SubChanges");
    })
    .await;
}

#[tokio::test]
async fn sub_change_rules() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_org_world(r).await;
        let g = &w.g;
        // a parent without a branch of its own gets one: its sub-changes fork from it (ADR 0103)
        let flat = must(
            g.create_change(
                &ctx,
                NewChange {
                    own_branch: false,
                    ..root_change(&w.base, "flat")
                },
            )
            .await,
        );
        let sub = must(g.create_change(&ctx, sub_change(&flat.id, "x")).await);
        let flat = must(g.change(&ctx, &flat.id).await);
        let fb = must(g.branch(&ctx, &sub.namespace, &sub.branch).await);
        assert!(
            fb.parent == flat.branch && flat.branch != MAIN_BRANCH,
            "the sub-change forks from the branch its parent was given: {fb:?}, parent on {}",
            flat.branch
        );
        // unknown owner org
        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    own_branch: false,
                    owner_org: "ORG-NOPE".into(),
                    ..root_change(&w.base, "x")
                },
            )
            .await,
            ErrorKind::Invalid,
            "unknown org",
        );
        let parent = must(
            g.create_change(
                &ctx,
                NewChange {
                    owner_org: "ORG-T1".into(),
                    ..root_change(&w.base, "p")
                },
            )
            .await,
        );
        // the sub-change's unit must be inside the parent's unit
        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    owner_org: "ORG-T2".into(),
                    ..sub_change(&parent.id, "x")
                },
            )
            .await,
            ErrorKind::Invalid,
            "unit outside the parent unit",
        );
        // namespace is the parent's
        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    namespace: "other".into(),
                    ..sub_change(&parent.id, "x")
                },
            )
            .await,
            ErrorKind::Invalid,
            "other namespace",
        );
        let p2 = must(
            g.create_change(
                &ctx,
                NewChange {
                    owner_org: "ORG-DIGITAL".into(),
                    ..root_change(&w.base, "p2")
                },
            )
            .await,
        );
        let sub = must(
            g.create_change(
                &ctx,
                NewChange {
                    owner_org: "ORG-T2".into(),
                    ..sub_change(&p2.id, "s")
                },
            )
            .await,
        );
        assert!(
            sub.branch != p2.branch && sub.parent_id == p2.id,
            "sub = {sub:?}"
        );
        // abandoning the parent abandons its open sub-changes and their branches
        must(
            g.update_change(
                &ctx,
                &p2.id,
                &ChangePatch {
                    status: Some(CHANGE_ABANDONED),
                    ..Default::default()
                },
            )
            .await,
        );
        let got = must(g.change(&ctx, &sub.id).await);
        assert_eq!(got.status, CHANGE_ABANDONED, "sub");
        let b = must(g.branch(&ctx, &sub.namespace, &sub.branch).await);
        assert_eq!(b.status, BRANCH_ABANDONED, "sub branch");
    })
    .await;
}

// A root project links project_part_of to itself (ADR 0039, 0054: the bootstrap writes the self-link). The checks
// and the walk up the projects must handle it without looping forever.
#[tokio::test]
async fn project_self_link_terminates() {
    let ctx = ctx();
    let g = memory_repo().graph();
    must(g.bootstrap(&ctx).await);
    let root = must(g.node_by_key(&ctx, "organisation", &root_project(&g)).await);
    let links = must(g.out_links_of(&ctx, &root.r#ref()).await);
    assert!(
        links.len() == 1 && links[0].to == root.r#ref() && links[0].r#type == LINK_PROJECT_PART_OF,
        "the root project links to itself: {links:?}"
    );
    must(
        import_node(
            &g,
            ImportNode {
                namespace: "organisation".into(),
                key: "PROJ-SUB".into(),
                r#type: NODE_TYPE_PROJECT_UNIT.into(),
                properties: props(&[("name", json!("Sub project"))]),
                links: vec![LinkWrite {
                    r#type: LINK_PROJECT_PART_OF.into(),
                    to: root.r#ref(),
                    ..Default::default()
                }],
                ..Default::default()
            },
        )
        .await,
    );
    let walk = async {
        let mut tx = must(g.begin(&ctx).await);
        must(
            g.structure_node(&ctx, &mut tx, STRUCTURE_PROJECT, "PROJ-ROOT")
                .await,
        );
        for (key, ancestor, want) in [
            ("PROJ-ROOT", "PROJ-ROOT", true),
            ("PROJ-SUB", "PROJ-ROOT", true),
            ("PROJ-ROOT", "PROJ-SUB", false),
        ] {
            let ok = must(
                g.within(&ctx, &mut tx, STRUCTURE_PROJECT, key, ancestor)
                    .await,
            );
            assert_eq!(ok, want, "{key} within {ancestor}");
        }
        must(tx.commit(&ctx).await);
    };
    tokio::time::timeout(std::time::Duration::from_secs(5), walk)
        .await
        .expect("a self-link made the walk loop forever");
}

// Project rules mirror the owner-org ones (ADR 0039): an unknown project is refused, a sub-change's project must be
// within the parent's, and it is inherited when unset.
#[tokio::test]
async fn project_sub_change_rules() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        must(g.bootstrap(&ctx).await);
        let root = must(g.node_by_key(&ctx, "organisation", &root_project(&g)).await);
        let mk = async |key: &str, name: &str, parent: &Node| {
            must(
                import_node(
                    &g,
                    ImportNode {
                        namespace: "organisation".into(),
                        key: key.into(),
                        r#type: NODE_TYPE_PROJECT_UNIT.into(),
                        properties: props(&[("name", json!(name))]),
                        links: vec![LinkWrite {
                            r#type: LINK_PROJECT_PART_OF.into(),
                            to: parent.r#ref(),
                            ..Default::default()
                        }],
                        ..Default::default()
                    },
                )
                .await,
            )
        };
        let a = mk("PROJ-A", "A", &root).await;
        mk("PROJ-A1", "A1", &a).await;
        mk("PROJ-B", "B", &root).await;
        let base = must(g.branch_head(&ctx, DEFAULT_NAMESPACE, MAIN_BRANCH).await);
        let change = |project: &str| NewChange {
            title: "x".into(),
            baseline_id: base.id.clone(),
            project_id: project.into(),
            ..Default::default()
        };
        assert_kind(
            g.create_change(&ctx, change("PROJ-NOPE")).await,
            ErrorKind::Invalid,
            "unknown project",
        );
        // none named: refused (ADR 0091)
        assert_kind(
            g.create_change(&ctx, change("")).await,
            ErrorKind::Invalid,
            "a change naming no project",
        );
        // a unit named by none is the root unit
        let c = must(g.create_change(&ctx, change("PROJ-ROOT")).await);
        assert!(
            c.project_id == root_project(&g) && c.owner_org == root_org(&g),
            "a change naming no unit is held by the root unit: {c:?}"
        );
        // a node of the organisation that is not a project is refused
        assert_kind(
            g.create_change(&ctx, change(&root_org(&g))).await,
            ErrorKind::Invalid,
            "a unit as the project",
        );
        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    owner_org: "PROJ-A".into(),
                    ..change("PROJ-ROOT")
                },
            )
            .await,
            ErrorKind::Invalid,
            "a project as the owner unit",
        );
        let parent = must(
            g.create_change(
                &ctx,
                NewChange {
                    title: "p".into(),
                    own_branch: true,
                    ..change("PROJ-A")
                },
            )
            .await,
        );
        let sub = |project: &str| NewChange {
            project_id: project.into(),
            ..sub_change(&parent.id, "x")
        };
        // the sub-change's project must be within the parent's project
        assert_kind(
            g.create_change(&ctx, sub("PROJ-B")).await,
            ErrorKind::Invalid,
            "project outside the parent project",
        );
        // within (a descendant) is accepted
        let s = must(g.create_change(&ctx, sub("PROJ-A1")).await);
        assert_eq!(s.project_id, "PROJ-A1", "sub.ProjectID");
        // unset: inherited from the parent
        let s = must(g.create_change(&ctx, sub("")).await);
        assert_eq!(s.project_id, "PROJ-A", "project inherited from the parent");
    })
    .await;
}

/// A validator of the tests: records what it was asked, refuses a child whose scope is "outside".
#[derive(Default)]
struct ScopeValidator {
    got: Mutex<Option<(Change, Change)>>,
}

#[async_trait::async_trait]
impl SubChangeValidator for ScopeValidator {
    async fn validate(&self, _: &Ctx, parent: &Change, child: &Change) -> Result<()> {
        *self.got.lock() = Some((parent.clone(), child.clone()));
        if child.data.get("scope") == Some(&json!("outside")) {
            return Err(Error::wrap(
                ErrorKind::Invalid,
                "scope is not part of its parent's",
            ));
        }
        Ok(())
    }
}

fn scope(s: &str) -> Map<String, Value> {
    props(&[("scope", json!(s))])
}

// CreateChange asks Graph.SubChangeValidator about a sub-change, with its parent as stored and the change about to
// be (the graph knows no use case: what the validator reads from Change.Data is its own business); the branch the
// sub-change gets defaults to Intent derive.
#[tokio::test]
async fn sub_change_validator_hook() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_org_world(r).await;
        let v = Arc::new(ScopeValidator::default());
        w.g.sub_change_validator = Some(v.clone());
        let g = &w.g;
        let parent = must(
            g.create_change(
                &ctx,
                NewChange {
                    data: scope("parent"),
                    ..root_change(&w.base, "p")
                },
            )
            .await,
        );
        let within = must(
            g.create_change(
                &ctx,
                NewChange {
                    data: scope("child"),
                    ..sub_change(&parent.id, "within")
                },
            )
            .await,
        );
        let (gp, gc) = v.got.lock().clone().expect("the validator is asked");
        assert!(
            gp.id == parent.id
                && gp.data["scope"] == "parent"
                && gc.data["scope"] == "child"
                && gc.parent_id == parent.id,
            "validator arguments: parent={gp:?} child={gc:?}"
        );
        assert_eq!(within.data["scope"], "child", "data");
        let b = must(g.branch(&ctx, &within.namespace, &within.branch).await);
        assert_eq!(b.intent, INTENT_DERIVE, "branch intent defaults to derive");
        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    data: scope("outside"),
                    ..sub_change(&parent.id, "outside")
                },
            )
            .await,
            ErrorKind::Invalid,
            "refused by the validator",
        );
        // a change with no parent is not asked
        *v.got.lock() = None;
        must(
            g.create_change(
                &ctx,
                NewChange {
                    own_branch: false,
                    data: scope("outside"),
                    ..root_change(&w.base, "root")
                },
            )
            .await,
        );
        assert!(
            v.got.lock().is_none(),
            "a change with no parent is not validated"
        );
    })
    .await;
}

/// writeTitle declares a modification of CMP-3 in a change, sets its title and accepts it.
async fn write_title(
    g: &Graph,
    w: &OrgWorld,
    c: &Change,
    t: &str,
    accept_it: bool,
) -> ChangeImpactID {
    let added = must(
        g.propose_impact(&ctx(), &c.id, vec![modified(&w.cmp3, t)])
            .await,
    );
    must(
        edit(
            g,
            &c.id,
            &added[0].id,
            Edit {
                properties: title_props(t),
                ..Default::default()
            },
        )
        .await,
    );
    if accept_it {
        must(accept(g, &c.id, &added[0].id, "u", "ok").await);
    }
    added[0].id.clone()
}

// Two sibling sub-changes, each on its own branch forked from the same parent branch, touching the same node:
// whichever merges into the parent branch first wins; the other is rebased onto it at its merge, settles the conflict
// and merges again (ADR 0103).
#[tokio::test]
async fn sub_change_merge_precedence() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let w = new_org_world(r).await;
        let g = &w.g;
        let parent = must(g.create_change(&ctx, root_change(&w.base, "parent")).await);
        let a = must(g.create_change(&ctx, sub_change(&parent.id, "sub A")).await);
        let b = must(g.create_change(&ctx, sub_change(&parent.id, "sub B")).await);
        assert!(
            a.branch != b.branch && a.branch != parent.branch,
            "sibling sub-changes must each have their own branch: a={} b={} parent={}",
            a.branch,
            b.branch,
            parent.branch
        );
        write_title(g, &w, &a, "from A", true).await;
        write_title(g, &w, &b, "from B", true).await;
        // A merges first: a fast-forward of the parent's branch
        must(apply(g, &a.id, "").await);
        assert_eq!(
            title_in(g, &parent.id, "CMP-3").await,
            "from A",
            "parent after A"
        );
        // B, taken from the same version as A, is rebased onto it: a conflict on the title, not a silent clobber
        assert_kind(
            apply(g, &b.id, "").await,
            ErrorKind::Conflict,
            "sub B apply",
        );
        assert_eq!(
            title_in(g, &parent.id, "CMP-3").await,
            "from A",
            "parent after B's rebase"
        );
        // B's author settles it in B, reviews it again; B fast-forwards into the parent
        let bcn = must(g.change(&ctx, &b.id).await).nodes[0].clone();
        must(
            edit(
                g,
                &b.id,
                &bcn.id,
                Edit {
                    properties: title_props("from A and B"),
                    ..Default::default()
                },
            )
            .await,
        );
        must(accept(g, &b.id, &bcn.id, "u", "settled").await);
        must(apply(g, &b.id, "").await);
        assert_eq!(
            title_in(g, &parent.id, "CMP-3").await,
            "from A and B",
            "parent after B's integration"
        );
        // the parent lands what both brought in
        must(apply(g, &parent.id, "").await);
        let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "CMP-3").await);
        // A's commit, B's commit, then B's rebased merge of both (its second parent A's version)
        assert!(
            n.properties["title"] == "from A and B"
                && n.version == w.cmp3.version + 3
                && n.parents.len() == 2
                && n.parents[1] == w.cmp3.version + 1,
            "CMP-3 on main = {n:?}"
        );
    })
    .await;
}

// A sub-change of a sub-change forks from the branch of its parent, recursively, and each level merges into the one
// above (ADR 0103): the guardian of the change being merged is asked, and a parent holding an uncommitted
// modification of a node its sub-change changed commits or cancels it first.
#[tokio::test]
async fn nested_sub_changes() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let mut w = new_org_world(r).await;
        let root = must(w.g.create_change(&ctx, root_change(&w.base, "root")).await);
        let mid = must(w.g.create_change(&ctx, sub_change(&root.id, "mid")).await);
        let leaf = must(w.g.create_change(&ctx, sub_change(&mid.id, "leaf")).await);
        let b = must(w.g.branch(&ctx, &leaf.namespace, &leaf.branch).await);
        assert_eq!(
            b.parent, mid.branch,
            "the leaf forks from the branch of its parent"
        );
        let b = must(w.g.branch(&ctx, &mid.namespace, &mid.branch).await);
        assert_eq!(
            b.parent, root.branch,
            "the middle change forks from the branch of the root"
        );
        write_title(&w.g, &w, &leaf, "from the leaf", true).await;
        must(commit_change(&w.g, &leaf.id, "").await);

        // the parent drafts the same node and has not committed it: the leaf waits
        let mid_impact = write_title(&w.g, &w, &mid, "from the middle", false).await;
        assert_kind(
            integrate_change(&w.g, &leaf.id).await,
            ErrorKind::Conflict,
            "merge under an uncommitted draft of the parent",
        );
        must(
            w.g.impact_node_cancel(&ctx, &mid.id, &mid_impact, "", "")
                .await,
        );

        // the guardian of the leaf holds its merge, then lets it go
        let allow = Arc::new(Mutex::new(false));
        let asked = Arc::new(Mutex::new(ChangeID::default()));
        {
            let (allow, asked) = (allow.clone(), asked.clone());
            w.g.lifecycles = Some(Arc::new(MergeGate(Box::new(move |c, _| {
                *asked.lock() = c.id.clone();
                Ok((true, *allow.lock()))
            }))));
        }
        let g = &w.g;
        let err = integrate_change(g, &leaf.id).await.unwrap_err();
        assert!(
            err.is_invalid() && *asked.lock() == leaf.id,
            "the guardian refuses the merge of {}: {err} (asked for {})",
            leaf.id,
            asked.lock()
        );
        *allow.lock() = true;
        must(integrate_change(g, &leaf.id).await);
        assert_eq!(
            title_in(g, &mid.id, "CMP-3").await,
            "from the leaf",
            "the middle change holds the leaf's work"
        );

        // each level merges into the one above, then the root lands on main
        must(apply(g, &mid.id, "").await);
        assert_eq!(
            title_in(g, &root.id, "CMP-3").await,
            "from the leaf",
            "the root holds the leaf's work"
        );
        let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "CMP-3").await);
        assert_ne!(
            n.properties["title"], "from the leaf",
            "main moved before the root merged"
        );
        must(apply(g, &root.id, "").await);
        let n = must(g.node_by_key(&ctx, DEFAULT_NAMESPACE, "CMP-3").await);
        assert!(
            n.properties["title"] == "from the leaf" && n.version == w.cmp3.version + 1,
            "CMP-3 on main = {n:?}"
        );
        for id in [&leaf.id, &mid.id, &root.id] {
            let c = must(g.change(&ctx, id).await);
            assert_eq!(c.status, CHANGE_APPLIED, "change {} ({id})", c.title);
        }
    })
    .await;
}
