//! The structures of the graph (`structure.go`, ADR 0054): what the catalogue in force tags, the built-in ones for an
//! untyped graph, and the walk up a hierarchy.

use goap_domain::{STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};

use super::graph::write_org_unit;
use crate::testsupport::*;

#[test]
fn builtin_structures() {
    let g = memory_repo().graph();
    let org = g.structure(STRUCTURE_ORGANISATION);
    assert_eq!(org.namespace, NAMESPACE_ORGANISATION);
    assert_eq!(org.r#type, NODE_TYPE_ORG_UNIT);
    assert_eq!(org.parent, LINK_PART_OF);
    assert_eq!(org.root, "ORG-DEFAULT");
    let proj = g.structure(STRUCTURE_PROJECT);
    assert_eq!(proj.r#type, NODE_TYPE_PROJECT_UNIT);
    assert_eq!(proj.parent, LINK_PROJECT_PART_OF);
    assert_eq!(proj.root, "PROJ-ROOT");
    assert!(proj.self_parent);
    let all = g.structures();
    assert!(all.iter().any(|s| s.kind == STRUCTURE_ORGANISATION));
    let kinds: Vec<String> = g
        .bootstrap_structures()
        .iter()
        .map(|s| s.kind.clone())
        .collect();
    assert_eq!(&kinds[..2], &[STRUCTURE_ORGANISATION, STRUCTURE_PROJECT]);
    // a user is a unit (ADR 0039), and must be a member of one (ADR 0065)
    assert!(g.is_a(NODE_TYPE_USER, NODE_TYPE_ORG_UNIT));
    let req = g.requires(NODE_TYPE_USER);
    assert!(req.iter().any(|l| l.link == LINK_MEMBER_OF), "{req:?}");
    for (typ, want) in [
        (NODE_TYPE_ORG_UNIT, true),
        (NODE_TYPE_USER, true),
        (NODE_TYPE_PROJECT_UNIT, true),
        ("alm@Requirement", false),
    ] {
        assert_eq!(g.admin_only_type(typ), want, "admin_only_type({typ})");
    }
}

// A node type flagged `adminOnly:` in a domain is restricted, and its subtypes with it (ADR 0068); a type the domain
// does not flag is not.
#[test]
fn admin_only_follows_the_domain() {
    let d = goap_def::parse_domain(
        br#"
name: vault
version: 1.0.0
nodeTypes:
  - {name: Secret, adminOnly: true}
  - {name: SubSecret, extends: Secret}
  - {name: Note}
"#,
    )
    .expect("domain");
    let cat = goap_typecat::new(&[d]).expect("catalogue");
    let mut g = memory_repo().graph();
    use_types(&mut g, std::sync::Arc::new(cat));
    for (typ, want) in [
        ("vault@Secret", true),
        ("vault@SubSecret", true),
        ("vault@Note", false),
        (NODE_TYPE_USER, true),
    ] {
        assert_eq!(g.admin_only_type(typ), want, "admin_only_type({typ})");
    }
}

// within follows the parent links of the latest versions on main; structure_node refuses what is not a node of the
// structure.
#[tokio::test]
async fn within_and_structure_node() {
    for_each_repo(async |r: &TestRepo| {
        let c = ctx();
        let g = r.graph();
        must(g.bootstrap(&c).await);
        write_org_unit(&g, "ORG-A", "ORG-DEFAULT").await;
        write_org_unit(&g, "ORG-B", "ORG-A").await;
        let mut tx = must(g.begin(&c).await);
        for (key, anc, want) in [
            ("ORG-B", "ORG-DEFAULT", true),
            ("ORG-B", "ORG-A", true),
            ("ORG-B", "ORG-B", true),
            ("ORG-A", "ORG-B", false),
        ] {
            let got = must(
                g.within(&c, &mut tx, STRUCTURE_ORGANISATION, key, anc)
                    .await,
            );
            assert_eq!(got, want, "within({key}, {anc})");
        }
        must(
            g.structure_node(&c, &mut tx, STRUCTURE_ORGANISATION, "ORG-B")
                .await,
        );
        assert_kind(
            g.structure_node(&c, &mut tx, STRUCTURE_ORGANISATION, "ORG-NOPE")
                .await,
            goap_graphapi::ErrorKind::Invalid,
            "an unknown unit",
        );
        let e = g
            .structure_node(&c, &mut tx, STRUCTURE_PROJECT, "ORG-A")
            .await
            .expect_err("a unit is no project");
        assert!(
            e.to_string().contains("is not a node of namespace") || e.to_string().contains("is a"),
            "{e}"
        );
        must(tx.commit(&c).await);
    })
    .await;
}
