mod common;

use std::sync::Arc;

use common::{as_, casbin, catalog_of, code, domains_catalog, floor, graph, with_types};
use goap_access as access;
use goap_connect::{Code, ConnectError, Request};
use goap_ctx::Ctx;
use goap_domain::{MAIN_BRANCH, NodeRef, TYPE_ADAPTER};
use goap_gen::graph::v1::{self as pb, GraphService};
use goap_graph::{Graph, LinkWrite, NewChange};
use goap_graphsvc::Handler;
use goap_pbconv as pbconv;

/// createObject creates a node of a type in a change of its own, as a caller with roles ("-": anonymous).
async fn create_object(
    h: &Handler,
    roles: &str,
    typ: &str,
    key: &str,
) -> Result<Option<pb::ChangeImpact>, ConnectError> {
    let ctx = &Ctx::background();
    let head = h
        .graph
        .branch_head(ctx, "alm", MAIN_BRANCH)
        .await
        .map_err(goap_rpcerr::to_connect)?;
    let c = h
        .graph
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: "alm".into(),
                title: format!("create {key}"),
                baseline_id: head.id,
                ..Default::default()
            },
        )
        .await
        .map_err(goap_rpcerr::to_connect)?;
    let msg = pb::ImpactNodeCreateRequest {
        change_id: c.id.to_string(),
        r#type: typ.into(),
        key: key.into(),
        rationale: "new".into(),
        ..Default::default()
    };
    let req = if roles == "-" {
        Request::new(msg)
    } else {
        as_(msg, "u", roles)
    };
    Ok(h.impact_node_create(ctx, req).await?.msg.node)
}

/// Creating a node in a change asks object:create on the project of the change (ADR 0076: no write outside a
/// change).
#[tokio::test]
async fn impact_node_create_is_role_gated() {
    let mut g = graph();
    with_types(&mut g, domains_catalog());
    g.branch_head(&Ctx::background(), "alm", MAIN_BRANCH)
        .await
        .unwrap();
    let g = Arc::new(g);
    let h = Handler {
        authz: Some(casbin()),
        ..Handler::new(g.clone())
    };
    for (name, roles, key, want) in [
        ("anonymous", "-", "REQ-1", Some(Code::PermissionDenied)),
        (
            "not on the project",
            "",
            "REQ-2",
            Some(Code::PermissionDenied),
        ),
        ("member of the project", "developer", "REQ-3", None),
        ("another role on the project", "tester", "REQ-4", None),
        ("admin", "admin", "REQ-5", None),
    ] {
        let r = Box::pin(create_object(&h, roles, "alm@Need", key)).await;
        assert_eq!(code(&r), want, "{name}: {r:?}");
        if let Ok(cn) = r {
            let cn = cn.unwrap();
            assert!(
                cn.key == key && cn.r#type == "alm@Need" && cn.post.is_some(),
                "{name}: the node is created checked out in the change: {cn:?}"
            );
        }
    }
    // a denied caller created nothing
    assert!(
        g.node_by_key(&Ctx::background(), "alm", "REQ-2")
            .await
            .is_err(),
        "REQ-2 must not exist"
    );
    // the type catalogue judges it
    let r = Box::pin(create_object(&h, "admin", "alm@Nope", "X-1")).await;
    assert_eq!(code(&r), Some(Code::InvalidArgument), "unknown type: {r:?}");
}

#[tokio::test]
async fn access_nodes_are_gated_by_the_floor() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    // the overall authorizer lets methodologists do everything on policies, the floor only administrators
    let h = Handler {
        authz: Some(casbin()),
        floor: Some(floor()),
        ..Handler::new(g.clone())
    };
    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let add = async |roles: &str| {
        let c = g
            .create_change(
                ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    title: "t".into(),
                    baseline_id: base.id.clone(),
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        let req = as_(
            pb::ImpactNodeCreateRequest {
                change_id: c.id.to_string(),
                key: "USR:x".into(),
                r#type: access::NODE_TYPE_USER.into(),
                rationale: "why".into(),
                ..Default::default()
            },
            "u",
            roles,
        );
        h.impact_node_create(ctx, req).await.map(|_| ())
    };
    let r = add("methodologist").await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "only administrators may change users: {r:?}"
    );
    add("admin").await.expect("admin must");
    // creating an access node in a change: administrators only
    let org_base = g
        .branch_head(ctx, "organisation", MAIN_BRANCH)
        .await
        .unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: "organisation".into(),
                title: "policy".into(),
                baseline_id: org_base.id,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let req = as_(
        pb::ImpactNodeCreateRequest {
            change_id: c.id.to_string(),
            key: "POL:x".into(),
            r#type: access::NODE_TYPE_POLICY.into(),
            rationale: "why".into(),
            ..Default::default()
        },
        "u",
        "methodologist",
    );
    let r = h.impact_node_create(ctx, req).await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "a Policy node created by a methodologist: {r:?}"
    );
}

/// A type a domain flags `adminOnly` is gated by the handler with no code naming it (ADR 0068): creating one needs
/// the floor, and the RPC the connectors of a distributed deployment ask answers.
#[tokio::test]
async fn admin_only_type_of_a_domain_is_gated() {
    let ctx = &Ctx::background();
    let mut g = graph();
    with_types(
        &mut g,
        catalog_of(
            "name: vault\nversion: 1.0.0\nnodeTypes:\n  - {name: Secret, adminOnly: true, changeControlled: false}\n  - {name: Note, changeControlled: false}\n",
        ),
    );
    let g = Arc::new(g);
    let h = Handler {
        floor: Some(floor()),
        ..Handler::new(g.clone())
    };
    for (typ, want) in [("vault@Secret", true), ("vault@Note", false)] {
        let r = h
            .is_admin_only_type(
                ctx,
                Request::new(pb::IsAdminOnlyTypeRequest { r#type: typ.into() }),
            )
            .await;
        assert!(
            matches!(&r, Ok(r) if r.msg.admin_only == want),
            "IsAdminOnlyType({typ}) = {r:?}; want {want}"
        );
    }
    let base = g.branch_head(ctx, "vault", MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                namespace: "vault".into(),
                title: "secret".into(),
                baseline_id: base.id,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    for (roles, want) in [
        ("contributor", Some(Code::PermissionDenied)),
        ("admin", None),
    ] {
        let req = as_(
            pb::ImpactNodeCreateRequest {
                change_id: c.id.to_string(),
                key: format!("S-{roles}"),
                r#type: "vault@Secret".into(),
                rationale: "why".into(),
                ..Default::default()
            },
            "u",
            roles,
        );
        let r = h.impact_node_create(ctx, req).await;
        assert_eq!(code(&r), want, "{roles} creating a flagged type: {r:?}");
    }
}

#[tokio::test]
async fn change_impact_rpcs() {
    let ctx = &Ctx::background();
    let mut g = graph();
    g.caller = Some(Arc::new(goap_graphsvc::caller));
    let g = Arc::new(g);
    let h = Handler {
        authz: Some(casbin()),
        floor: Some(floor()),
        ..Handler::new(g.clone())
    };
    let req1 = Box::pin(goap_graphtest::import(
        ctx,
        &g,
        goap_graphtest::Node {
            key: "REQ-1".into(),
            r#type: "Requirement".into(),
            properties: obj! {"title" => "one"},
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let c = g
        .create_change(
            ctx,
            NewChange {
                project_id: "PROJ-ROOT".into(),
                title: "t".into(),
                baseline_id: base.id,
                own_branch: true,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    let cid = c.id.to_string();
    let add = async |roles: &str, pre: NodeRef| {
        let req = as_(
            pb::ProposeImpactRequest {
                change_id: cid.clone(),
                nodes: vec![pb::ChangeImpact {
                    intent: "modified".into(),
                    pre: Some(pbconv::ref_to_pb(&pre)),
                    rationale: "why".into(),
                    ..Default::default()
                }],
            },
            "u",
            roles,
        );
        h.propose_impact(ctx, req)
            .await
            .map(|out| out.msg.nodes[0].clone())
    };

    let cn = add("contributor", req1.r#ref()).await.unwrap();
    let co = as_(
        pb::ImpactNodeCheckoutRequest {
            change_id: cid.clone(),
            change_impact_id: cn.id.clone(),
            ..Default::default()
        },
        "u",
        "contributor",
    );
    let out = h.impact_node_checkout(ctx, co).await;
    assert!(
        matches!(&out, Ok(o) if o.msg.node.as_ref().and_then(|n| n.post.as_ref()).is_some_and(|p| p.version == 0)),
        "checkout: {out:?}"
    );
    let up = as_(
        pb::ImpactNodeUpdateRequest {
            change_id: cid.clone(),
            change_impact_id: cn.id.clone(),
            props: pbconv::to_struct(&obj! {"title" => "two"}),
            ..Default::default()
        },
        "u",
        "contributor",
    );
    let out = h.impact_node_update(ctx, up).await;
    assert!(
        matches!(&out, Ok(o) if o.msg.node.as_ref().and_then(|n| n.post.as_ref()).is_some_and(|p| p.version == 0)),
        "update the draft: {out:?}"
    );
    let review = |comment: &str| {
        as_(
            pb::ImpactNodeReviewRequest {
                change_id: cid.clone(),
                change_impact_id: cn.id.clone(),
                accept: true,
                comment: comment.into(),
                ..Default::default()
            },
            "u",
            "contributor",
        )
    };
    let r = h.impact_node_review(ctx, review("")).await;
    assert_eq!(
        code(&r),
        Some(Code::InvalidArgument),
        "a review without comment must be refused: {r:?}"
    );
    let out = h.impact_node_review(ctx, review("checked")).await;
    assert!(
        matches!(&out, Ok(o) if o.msg.node.as_ref().is_some_and(|n| n.review == "accepted" && n.reviews[0].by == "u")),
        "review: {out:?}"
    );
    let get = h
        .get_change(ctx, Request::new(pb::GetChangeRequest { id: cid.clone() }))
        .await;
    assert!(
        matches!(&get, Ok(g) if g.msg.change.as_ref().is_some_and(|c| c.nodes.len() == 1 && c.nodes[0].review == "accepted")),
        "GetChange carries the change impacts: {get:?}"
    );
    // every operation is an event of the impact log, with its caller (ADR 0029)
    let events = async || {
        h.list_change_events(
            ctx,
            Request::new(pb::ListChangeEventsRequest {
                change_id: cid.clone(),
            }),
        )
        .await
        .unwrap()
        .msg
        .events
    };
    for e in events().await {
        assert!(
            e.by == "u" && e.impact_id == cn.id,
            "event {}: by {:?}, impact {}",
            e.op,
            e.by,
            e.impact_id
        );
    }
    let vs = g.versions(ctx, &req1.id).await;
    let n = g
        .change_node(
            ctx,
            &c.id,
            "",
            &NodeRef {
                id: req1.id.clone(),
                ..Default::default()
            },
        )
        .await;
    assert!(
        matches!(&n, Ok(n) if n.is_draft()) && matches!(&vs, Ok(vs) if vs.len() == 1),
        "the accepted review leaves a draft, no version, until the change lands: {n:?} {vs:?}"
    );
    let ops: Vec<String> = events().await.into_iter().map(|e| e.op).collect();
    assert_eq!(
        ops.join(","),
        "proposed,checkedOut,updated,reviewed",
        "impact log"
    );
}

#[tokio::test]
async fn commit_edits_gates_access_nodes() {
    let ctx = &Ctx::background();
    let g = Arc::new(graph());
    let h = Handler {
        authz: Some(casbin()),
        ..Handler::new(g.clone())
    };
    let base = g.branch_head(ctx, "", MAIN_BRANCH).await.unwrap();
    let commit = async |roles: &str, typ: &str| {
        let req = as_(
            pb::CommitEditsRequest {
                title: "t".into(),
                baseline_id: base.id.to_string(),
                edits: vec![pb::NodeEdit {
                    key: format!("K-{roles}"),
                    r#type: typ.into(),
                    rationale: "why".into(),
                    ..Default::default()
                }],
                ..Default::default()
            },
            "u",
            roles,
        );
        Box::pin(h.commit_edits(ctx, req)).await.map(|out| out.msg)
    };
    let r = commit("contributor", access::NODE_TYPE_POLICY).await;
    assert_eq!(
        code(&r),
        Some(Code::PermissionDenied),
        "a contributor must not commit policies: {r:?}"
    );
    let out = commit("admin", access::NODE_TYPE_POLICY).await;
    let out = match out {
        Ok(o)
            if !o.change_id.is_empty() && o.baseline.as_ref().is_some_and(|b| !b.id.is_empty()) =>
        {
            o
        }
        other => panic!("an administrator may: {other:?}"),
    };
    commit("contributor", "alm@Need")
        .await
        .expect("ordinary nodes are not gated");
    let n = g.node_by_key(ctx, "", "K-admin").await;
    assert!(
        matches!(&n, Ok(n) if n.change_id == out.change_id && n.comment == "why"),
        "committed node: {n:?}"
    );
    // regression: organisation/project structure and Assignment must be gated too (a platform Assignment grants the
    // "admin" platform role, ADR 0046/0047 — an ungated Assignment write is privilege escalation to full
    // administrator, not just a stray node)
    for typ in [
        access::NODE_TYPE_ASSIGNMENT,
        access::NODE_TYPE_PROJECT_UNIT,
        access::NODE_TYPE_ORG_UNIT,
        TYPE_ADAPTER,
    ] {
        let r = commit("contributor", typ).await;
        assert_eq!(
            code(&r),
            Some(Code::PermissionDenied),
            "a contributor must not commit a {typ} node: {r:?}"
        );
    }
}

async fn must(g: &Graph, key: &str) -> goap_domain::Node {
    g.node_by_key(&Ctx::background(), "vault", key)
        .await
        .unwrap()
}

/// A merge modifies the parents it discovers: the gate of the access nodes stands in front of them too (ADR 0077).
#[tokio::test]
async fn merge_is_gated_on_the_parents() {
    let ctx = &Ctx::background();
    let mut g = graph();
    with_types(
        &mut g,
        catalog_of(
            "
name: vault
version: 1.0.0
nodeTypes:
  - {name: Safe, adminOnly: true, changeControlled: false}
  - {name: Entry, changeControlled: false}
linkTypes:
  - {name: holds, from: Safe, to: Entry, compose: true}
",
        ),
    );
    let g = Arc::new(g);
    let mut links = Vec::new();
    for k in ["E1", "E2"] {
        let n = Box::pin(goap_graphtest::import(
            ctx,
            &g,
            goap_graphtest::Node {
                namespace: "vault".into(),
                key: k.into(),
                r#type: "vault@Entry".into(),
                ..Default::default()
            },
        ))
        .await
        .unwrap();
        links.push(LinkWrite {
            r#type: "vault@holds".into(),
            to: n.r#ref(),
            ..Default::default()
        });
    }
    Box::pin(goap_graphtest::import(
        ctx,
        &g,
        goap_graphtest::Node {
            namespace: "vault".into(),
            key: "S".into(),
            r#type: "vault@Safe".into(),
            links,
            ..Default::default()
        },
    ))
    .await
    .unwrap();
    let h = Handler {
        floor: Some(floor()),
        ..Handler::new(g.clone())
    };
    let base = g.branch_head(ctx, "vault", MAIN_BRANCH).await.unwrap();
    for (roles, want) in [
        ("contributor", Some(Code::PermissionDenied)),
        ("admin", None),
    ] {
        let c = g
            .create_change(
                ctx,
                NewChange {
                    project_id: "PROJ-ROOT".into(),
                    namespace: "vault".into(),
                    title: format!("merge {roles}"),
                    baseline_id: base.id.clone(),
                    own_branch: true,
                    ..Default::default()
                },
            )
            .await
            .unwrap();
        let name = |k: &str| pb::NodeName {
            key: k.into(),
            ..Default::default()
        };
        let req = as_(
            pb::ImpactNodeMergeRequest {
                change_id: c.id.to_string(),
                sources: vec![name("E1"), name("E2")],
                into: Some(pb::NodeCreateSpec {
                    key: format!("E-{roles}"),
                    r#type: "vault@Entry".into(),
                    rationale: "one entry".into(),
                    ..Default::default()
                }),
                ..Default::default()
            },
            "u",
            roles,
        );
        let out = Box::pin(h.impact_node_merge(ctx, req)).await;
        assert_eq!(
            code(&out),
            want,
            "{roles} merging the entries of a safe: {out:?}"
        );
        match out {
            Ok(out) => assert!(
                out.msg.parents.len() == 1
                    && out.msg.parents[0].key == "S"
                    && out.msg.successors.len() == 1
                    && out.msg.sources.len() == 2,
                "result: {:?}",
                out.msg
            ),
            Err(_) => {
                let got = g.list_change_impacts(ctx, &c.id).await.unwrap();
                assert!(got.is_empty(), "a refused merge left {} impacts", got.len());
            }
        }
    }
    // nothing derives from E1 until the change lands: the successor is a draft (ADR 0079)
    let e1 = must(&g, "E1").await;
    let r = h
        .derived_nodes(
            ctx,
            Request::new(pb::DerivedNodesRequest {
                r#ref: Some(pb::NodeRef {
                    id: e1.id.to_string(),
                    ..Default::default()
                }),
            }),
        )
        .await;
    assert!(
        matches!(&r, Ok(r) if r.msg.nodes.is_empty()),
        "DerivedNodes: {r:?}"
    );
}
