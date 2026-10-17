//! Port of `branches_test.go`: branches of a namespace and their merge (ADR 0032, 0056).

use std::collections::BTreeMap;

use goap_domain::*;
use goap_graphapi::{Commit, LinkEdit, MergeCandidate, NodeEdit, Resolution};
use serde_json::{Map, Value, json};

use crate::tests::helpers::*;
use crate::testsupport::*;
use crate::{
    ErrorKind, Graph, MERGE_ADDED, MERGE_THREE_WAY, MergeRequest, NewBranch, NewChange,
    common_ancestor, merge_props,
};

/// setEdit is an edit of a node: new properties merged over the version the change starts from.
pub(crate) fn set_edit(r: NodeRef, p: Map<String, Value>) -> NodeEdit {
    NodeEdit {
        pre: Some(r),
        props: p,
        ..Default::default()
    }
}

/// commitOn makes a change of edits that lands on a branch ("" = main) and returns its resulting baseline.
pub(crate) async fn commit_on(
    g: &Graph,
    branch: &str,
    from: &BaselineID,
    edits: Vec<NodeEdit>,
) -> Baseline {
    must(
        g.commit(
            &ctx(),
            Commit {
                project_id: "PROJ-ROOT".into(),
                title: format!("c-{branch}"),
                baseline: from.clone(),
                branch: branch.into(),
                by: "test".into(),
                edits,
                ..Default::default()
            },
        )
        .await,
    )
    .baseline
}

fn new_branch(name: &str, from: &BaselineID) -> NewBranch {
    NewBranch {
        name: name.into(),
        from: from.clone(),
        ..Default::default()
    }
}

fn merge(from: &str, into: &str) -> MergeRequest {
    MergeRequest {
        project_id: "PROJ-ROOT".into(),
        from: from.into(),
        into: into.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn branch_merge() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let req1 = f.req.r#ref();

        assert_kind(
            g.create_change(
                &ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "x".into(),
                    baseline_id: f.base.id.clone(),
                    branch: "opt-a".into(),
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::NotFound,
            "change on unknown branch",
        );
        must(
            g.create_branch(
                &ctx,
                NewBranch {
                    origin: "option:a".into(),
                    ..new_branch("opt-a", &f.base.id)
                },
            )
            .await,
        );
        assert_kind(
            g.create_branch(&ctx, new_branch("opt-a", &f.base.id)).await,
            ErrorKind::Conflict,
            "duplicate branch",
        );

        // opt-a: REQ-1 derived (v2) and a new design node refining it.
        let b_a = commit_on(
            g,
            "opt-a",
            &f.base.id,
            vec![
                set_edit(
                    req1.clone(),
                    props(&[("title", json!("Use PSP v2")), ("psp", json!("stripe"))]),
                ),
                NodeEdit {
                    key: "DES-1".into(),
                    r#type: "Design".into(),
                    links: vec![LinkEdit {
                        r#type: "refines".into(),
                        to: Some(req1.clone()),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            ],
        )
        .await;
        assert!(
            b_a.branch == "opt-a" && b_a.nodes.get(&f.req.id) == Some(&2),
            "opt-a baseline: {b_a:?}"
        );
        let versions = async || must(g.versions(&ctx, &f.req.id).await);
        // written on the branch of the change, joined opt-a when the change landed there (ADR 0032)
        let v2 = versions().await[1].clone();
        assert!(
            v2.branch.starts_with("change-")
                && v2.joined == ["opt-a"]
                && v2.reason == REASON_DERIVE
                && v2.parents == [1],
            "derived version: {v2:?}"
        );
        let n = crate::tests::changebranch::head_node(g, &f.req.id).await;
        assert_eq!(n.version, 1, "main latest must stay v1");
        let h = must(g.branch_head(&ctx, "", "opt-a").await);
        assert_eq!(h.id, b_a.id, "opt-a head");

        // main moves in parallel: REQ-1 v3, derived from v1 on the branch of the change and landed on main.
        let b_m = commit_on(
            g,
            "",
            &f.base.id,
            vec![set_edit(req1.clone(), props(&[("prio", json!("high"))]))],
        )
        .await;
        let v3 = versions().await[2].clone();
        assert!(
            v3.joined == [MAIN_BRANCH] && v3.parents == [1] && b_m.nodes.get(&f.req.id) == Some(&3),
            "main version: {v3:?}"
        );
        // a change that started before and changes the same property conflicts
        assert_kind(
            g.commit(
                &ctx,
                Commit {
                    project_id: "PROJ-ROOT".into(),
                    title: "stale".into(),
                    baseline: f.base.id.clone(),
                    by: "test".into(),
                    edits: vec![set_edit(req1.clone(), props(&[("prio", json!("low"))]))],
                    ..Default::default()
                },
            )
            .await,
            ErrorKind::Conflict,
            "stale commit",
        );

        let anc = {
            let mut tx = must(g.begin(&ctx).await);
            let a = must(common_ancestor(&ctx, &mut tx, &f.req.id, 3, 2).await);
            must(tx.commit(&ctx).await);
            a
        };
        assert_eq!(anc.version, 1, "common ancestor");

        let plan = must(g.plan_merge(&ctx, "", "opt-a", "").await);
        assert!(
            plan.candidates.len() == 2 && plan.conflicting().is_empty(),
            "plan: {plan:?}"
        );
        let by_key: BTreeMap<String, MergeCandidate> = plan
            .candidates
            .iter()
            .map(|c| (c.key.clone(), c.clone()))
            .collect();
        let c = &by_key["REQ-1"];
        assert!(
            c.kind == MERGE_THREE_WAY
                && c.merged["prio"] == "high"
                && c.merged["psp"] == "stripe"
                && c.merged["title"] == "Use PSP v2",
            "REQ-1 candidate: {c:?}"
        );
        assert_eq!(by_key["DES-1"].kind, MERGE_ADDED, "DES-1 candidate");

        let res = must(g.merge_branch(&ctx, merge("opt-a", MAIN_BRANCH)).await);
        let b = res.baseline;
        let v4 = crate::tests::changebranch::head_node(g, &f.req.id).await;
        assert!(
            b.branch == MAIN_BRANCH && b.nodes.get(&f.req.id) == Some(&v4.version),
            "merged baseline: {b:?}"
        );
        assert!(
            b.parent_id == b_m.id && b.merged_from == b_a.id,
            "merged baseline parents: parent {} (want {}), mergedFrom {} (want {})",
            b.parent_id,
            b_m.id,
            b.merged_from,
            b_a.id
        );
        assert!(
            v4.version >= 4
                && v4.reason == REASON_MERGE
                && v4.parents == [3, 2]
                && v4.properties["prio"] == "high"
                && v4.properties["psp"] == "stripe",
            "merge version: {v4:?}"
        );
        let (nodes, links) = must(g.baseline_graph(&ctx, &b.id).await);
        assert_eq!(nodes.len(), 4, "merged nodes: {nodes:?}");
        assert!(
            links
                .iter()
                .any(|l| l.r#type == "satisfies" && l.from == v4.r#ref()),
            "merged links: {links:?}"
        );
        // DES-1, added on opt-a only, joins main as is (no merge version, ADR 0032): its link to the REQ-1 it refined
        // on the branch is suspect, REQ-1 got a merge version with the changes of main
        let des = must(g.node_by_key(&ctx, "", "DES-1").await);
        assert!(
            des.version == 1 && b.nodes.get(&des.id) == Some(&1),
            "DES-1 must land as is: {des:?}"
        );
        let suspect = must(g.suspect_links(&ctx, &b.id).await);
        assert!(
            suspect
                .iter()
                .any(|l| l.r#type == "refines" && l.from == des.r#ref() && l.to == v2.r#ref()),
            "suspect links: {suspect:?}"
        );
        let br = must(g.branch(&ctx, "", "opt-a").await);
        assert_eq!(br.status, BRANCH_MERGED, "branch status");
        let h = must(g.branch_head(&ctx, "", "").await);
        assert_eq!(h.id, b.id, "main head");
        assert_kind(
            g.plan_merge(&ctx, "", "opt-a", "").await,
            ErrorKind::Conflict,
            "merged branch must not merge again",
        );
    })
    .await;
}

#[tokio::test]
async fn branch_merge_conflict() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        let req1 = f.req.r#ref();
        must(g.create_branch(&ctx, new_branch("opt-b", &f.base.id)).await);
        commit_on(
            g,
            "opt-b",
            &f.base.id,
            vec![set_edit(
                req1.clone(),
                props(&[("title", json!("Use PSP B"))]),
            )],
        )
        .await;
        commit_on(
            g,
            "",
            &f.base.id,
            vec![set_edit(
                req1.clone(),
                props(&[("title", json!("Use PSP M"))]),
            )],
        )
        .await;

        let plan = must(g.plan_merge(&ctx, "", "opt-b", "").await);
        let cs = plan.conflicting();
        assert!(
            cs.len() == 1 && cs[0].conflicts == ["title"] && cs[0].merged["title"] == "Use PSP M",
            "conflicts: {plan:?}"
        );
        assert_kind(
            g.merge_branch(&ctx, merge("opt-b", "")).await,
            ErrorKind::Conflict,
            "unresolved merge",
        );
        let mut req = merge("opt-b", "");
        req.resolutions.insert(
            f.req.id.clone(),
            Resolution {
                props: props(&[("title", json!("Use PSP B+M"))]),
                ..Default::default()
            },
        );
        let res = must(g.merge_branch(&ctx, req).await);
        let n = must(
            g.node(
                &ctx,
                &NodeRef {
                    id: f.req.id.clone(),
                    version: res.baseline.nodes[&f.req.id],
                },
            )
            .await,
        );
        assert!(
            n.properties["title"] == "Use PSP B+M" && n.reason == REASON_MERGE,
            "resolved: {n:?}"
        );
    })
    .await;
}

// Go: TestMergeProps
#[test]
fn merge_props_three_way() {
    let (m, c) = merge_props(
        &props(&[
            ("a", json!(1)),
            ("b", json!(1)),
            ("c", json!(1)),
            ("d", json!(1)),
        ]),
        &props(&[
            ("a", json!(2)),
            ("b", json!(1)),
            ("c", json!(3)),
            ("e", json!(5)),
        ]),
        &props(&[
            ("a", json!(1)),
            ("b", json!(2)),
            ("c", json!(4)),
            ("d", json!(1)),
        ]),
    );
    assert!(
        m["a"] == 2 && m["b"] == 2 && m["c"] == 3 && m["e"] == 5 && m.len() == 4 && c == ["c"],
        "merged {m:?} conflicts {c:?}"
    );
}

// A branch merge is a change of the platform: each merge version records the change impact that explains it.
#[tokio::test]
async fn branch_merge_records_change_impacts() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let f = new_fixture(r).await;
        let g = &f.g;
        must(g.create_branch(&ctx, new_branch("opt-a", &f.base.id)).await);
        commit_on(
            g,
            "opt-a",
            &f.base.id,
            vec![
                set_edit(f.req.r#ref(), props(&[("title", json!("on the branch"))])),
                NodeEdit {
                    key: "DES-1".into(),
                    r#type: "Design".into(),
                    ..Default::default()
                },
            ],
        )
        .await;
        // main moves on the same node: a 3-way merge with a conflict, resolved by hand
        commit_on(
            g,
            "main",
            &f.base.id,
            vec![set_edit(
                f.req.r#ref(),
                props(&[("title", json!("on main"))]),
            )],
        )
        .await;
        let mut req = merge("opt-a", "main");
        req.resolutions.insert(
            f.req.id.clone(),
            Resolution {
                props: props(&[("title", json!("both"))]),
                ..Default::default()
            },
        );
        let res = must(g.merge_branch(&ctx, req).await);
        let nodes = must(g.list_change_impacts(&ctx, &res.change.id).await);
        assert_eq!(
            nodes.len(),
            2,
            "one change impact per merged node: {nodes:?}"
        );
        let by_key: BTreeMap<String, ChangeImpact> =
            nodes.iter().map(|n| (n.key.clone(), n.clone())).collect();
        let (req, des) = (&by_key["REQ-1"], &by_key["DES-1"]);
        assert!(
            req.intent == INTENT_MODIFIED
                && req.pre.is_some()
                && req.landed.is_some()
                && req.review == REVIEW_ACCEPTED
                && des.intent == INTENT_CREATED
                && des.pre.is_none(),
            "change impacts: {req:?} {des:?}"
        );
        let head = crate::tests::changebranch::head_node(g, &f.req.id).await;
        assert!(
            head.reason == REASON_MERGE
                && head.properties["title"] == "both"
                && head.change_impact == req.id
                && head.parents.len() == 2
                && Some(head.r#ref()) == req.landed,
            "merge version: {head:?}"
        );
        assert!(
            !head.comment.is_empty() && res.change.status == CHANGE_APPLIED,
            "origin: {head:?}"
        );
    })
    .await;
}

fn thing(ns: &str, typ: &str) -> ImportNode {
    ImportNode {
        namespace: ns.into(),
        key: "N-1".into(),
        r#type: typ.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn branch_names_are_scoped_to_their_namespace() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        must(import_node(&g, thing("", "Thing")).await);
        must(import_node(&g, thing("organisation", "OrgUnit")).await);
        let def_base = must(g.branch_head(&ctx, "", MAIN_BRANCH).await);
        let org_base = must(g.branch_head(&ctx, "organisation", MAIN_BRANCH).await);
        // two namespaces can each have their own branch of the same name
        let def_branch = must(
            g.create_branch(&ctx, new_branch("feature-x", &def_base.id))
                .await,
        );
        let org_branch = must(
            g.create_branch(
                &ctx,
                NewBranch {
                    namespace: "organisation".into(),
                    ..new_branch("feature-x", &org_base.id)
                },
            )
            .await,
        );
        assert!(
            def_branch.namespace == DEFAULT_NAMESPACE && org_branch.namespace == "organisation",
            "branch namespaces: {def_branch:?} {org_branch:?}"
        );
        let got = must(g.branch(&ctx, "", "feature-x").await);
        assert_eq!(got.fork_baseline, def_base.id, "default feature-x");
        let got = must(g.branch(&ctx, "organisation", "feature-x").await);
        assert_eq!(got.fork_baseline, org_base.id, "organisation feature-x");
    })
    .await;
}

// The head of a namespace holds the nodes of that namespace only.
#[tokio::test]
async fn head_is_scoped_to_one_namespace() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let def = must(import_node(&g, thing("", "Thing")).await);
        let org = must(import_node(&g, thing("organisation", "OrgUnit")).await);
        let snap = must(g.branch_head(&ctx, "organisation", MAIN_BRANCH).await);
        assert_eq!(snap.namespace, "organisation", "head namespace");
        assert!(
            snap.nodes.contains_key(&org.id),
            "head must contain the organisation node: {:?}",
            snap.nodes
        );
        assert!(
            !snap.nodes.contains_key(&def.id),
            "head must not contain the default-namespace node: {:?}",
            snap.nodes
        );
    })
    .await;
}

// A namespace no change landed in has the empty state for head (empty id); its first change starts from it and
// leaves the first baseline, which has no parent but a change (ADR 0056).
#[tokio::test]
async fn first_change_starts_from_the_empty_state() {
    for_each_repo(async |r: &TestRepo| {
        let ctx = ctx();
        let g = r.graph();
        let ns = "nothing-here-yet";
        let head = must(g.branch_head(&ctx, ns, MAIN_BRANCH).await);
        assert!(
            head.id.is_empty() && head.nodes.is_empty(),
            "the head of a namespace without history is the empty state: {head:?}"
        );
        let bs = must(g.baselines(&ctx, ns).await);
        assert!(bs.is_empty(), "nothing stores the empty state: {bs:?}");
        let res = must(
            g.commit(
                &ctx,
                Commit {
                    project_id: "PROJ-ROOT".into(),
                    namespace: ns.into(),
                    title: "First".into(),
                    edits: vec![NodeEdit {
                        key: "N-1".into(),
                        r#type: "Thing".into(),
                        ..Default::default()
                    }],
                    ..Default::default()
                },
            )
            .await,
        );
        let c = must(g.change(&ctx, &res.change).await);
        assert!(
            c.baseline_id.is_empty() && c.result_baseline_id == res.baseline.id,
            "the first change starts from the empty state: {c:?}"
        );
        let b = &res.baseline;
        assert!(
            b.parent_id.is_empty() && b.change_id == c.id && b.nodes.len() == 1,
            "the first baseline has no parent and its change: {b:?}"
        );
        let after = must(g.branch_head(&ctx, ns, MAIN_BRANCH).await);
        assert_eq!(after.id, res.baseline.id, "head");
        // every baseline of the organisation, the bootstrap's included, is the result of a change
        for b in must(g.baselines(&ctx, NAMESPACE_ORGANISATION).await) {
            assert!(
                !b.change_id.is_empty(),
                "baseline {} ({}) has no change",
                b.id,
                b.name
            );
        }
    })
    .await;
}
