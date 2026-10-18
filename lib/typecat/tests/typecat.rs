//! Port of typecat_test.go.

mod common;
use std::sync::Arc;

use async_trait::async_trait;
use common::{from_file, parse};
use goap_algo::USAGE_LINK_VALIDATOR;
use goap_ctx::Ctx;
use goap_domain::{
    STRUCTURE_ORGANISATION, STRUCTURE_PROJECT, StructureSet, Structures, TypeRef, parse_type_ref,
    qualify_in,
};
use goap_typecat::{BoxError, Live, Source, builtin};

fn tr(ns: &str, name: &str) -> TypeRef {
    TypeRef {
        namespace: ns.into(),
        name: name.into(),
    }
}

#[test]
fn type_ref() {
    let r = parse_type_ref("alm@Requirement").unwrap();
    assert!(r == tr("alm", "Requirement") && r.to_string() == "alm@Requirement");
    assert_eq!(
        qualify_in("alm", "Need").unwrap().to_string(),
        "alm@Need",
        "a bare name is a type of the domain"
    );
    assert_eq!(
        qualify_in("alm", "organisation@OrgUnit")
            .unwrap()
            .to_string(),
        "organisation@OrgUnit",
        "a qualified name keeps its namespace"
    );
    for bad in ["", "@X", "alm@", "a@b@c"] {
        let err = parse_type_ref(bad).unwrap_err();
        assert!(err.is_invalid_ref(), "{bad:?} must be refused: {err}");
    }
}

#[test]
fn builtin_domains_are_always_there() {
    let c = builtin();
    for r in [
        "methodology@Agent",
        "methodology@MethodologyVersion",
        "organisation@OrgUnit",
        "organisation@Policy",
        "platform@MCP",
    ] {
        assert!(c.r#type(r).is_some(), "{r} is built in");
    }
    assert_eq!(
        c.r#type("methodology@Agent").unwrap().editor,
        "agent",
        "editor of the meta type"
    );
    c.check_link(
        "methodology@defines",
        "methodology@MethodologyVersion",
        "methodology@Agent",
    )
    .unwrap();
    assert!(
        !c.has_node_type("domain@NodeType"),
        "domain definitions are not graph data (ADR 0023)"
    );
    for name in ["methodology", "organisation", "platform"] {
        let err = goap_typecat::new(&[parse(&format!(
            "name: {name}\nversion: 1.0.0\nnodeTypes: [X]\n"
        ))])
        .unwrap_err();
        assert!(
            err.is_invalid(),
            "a domain cannot take the name of a built-in domain: {err}"
        );
    }
}

#[test]
fn repository_domains() {
    let c = goap_typecat::new(&[from_file("alm")]).unwrap();
    let sec = c
        .r#type("alm@SecurityRequirement")
        .expect("alm@SecurityRequirement");
    assert_eq!(
        sec.ancestors,
        [
            tr("alm", "NonFunctionalRequirement"),
            tr("alm", "Requirement")
        ],
        "ancestors, nearest first"
    );
    assert!(
        sec.lifecycle
            .as_ref()
            .is_some_and(|l| l.name == "requirement")
            && sec.change_controlled,
        "the lifecycle is inherited: {:?}",
        sec.lifecycle
    );
    assert!(
        !sec.validators.is_empty() && sec.validators[0].property == "title",
        "the validators are inherited: {:?}",
        sec.validators
    );
    let names = sec.property_names();
    assert!(
        names[0] == "title" && names.iter().any(|n| n == "category"),
        "the attributes, inherited first: {names:?}"
    );
    let facets: Vec<&str> = sec
        .search
        .iter()
        .filter(|s| s.facet)
        .map(|s| s.property.as_str())
        .collect();
    assert!(
        facets.contains(&"category") && facets.contains(&"priority"),
        "the search declarations add up: {:?}",
        sec.search
    );
    assert_eq!(c.r#type("organisation@OrgUnit").unwrap().editor, "unit");
    assert_eq!(
        c.supertypes()["alm@SecurityRequirement"],
        ["alm@NonFunctionalRequirement", "alm@Requirement"]
    );
    assert!(
        !c.domains()["alm"].is_empty(),
        "the version of each domain is known"
    );

    // the existence rule
    c.check_node("alm", "alm@Requirement").unwrap();
    for (ns, typ) in [
        ("alm", "alm@Nope"),
        ("alm", "Requirement"),
        ("organisation", "alm@Requirement"),
    ] {
        assert!(
            c.check_node(ns, typ).is_err(),
            "{typ} in {ns} must be refused"
        );
    }
    // links: known type, ends accepting subtypes, any source when the link type names none
    c.check_link("alm@verifies", "alm@TestCase", "alm@SecurityRequirement")
        .unwrap();
    let err = c
        .check_link("alm@verifies", "alm@Need", "alm@Requirement")
        .unwrap_err();
    assert!(err.is_invalid(), "wrong source: {err}");
    c.check_link(
        "organisation@member_of",
        "organisation@User",
        "organisation@OrgUnit",
    )
    .unwrap_or_else(|e| panic!("a link to a supertype: {e}"));
    let err = c
        .check_link("alm@nope", "alm@Need", "alm@Need")
        .unwrap_err();
    assert!(err.is_unknown(), "unknown link type: {err}");
    assert_eq!(err.to_string(), "link type \"alm@nope\": unknown type");
}

#[test]
fn cross_domain_references() {
    let base = parse(
        r#"
name: base
version: 1.0.0
lifecycles:
  - {name: simple, initial: open, states: [{name: open, notLandable: true}, {name: done}], transitions: [{name: close, from: open, to: done}]}
nodeTypes:
  - {name: Item, lifecycle: simple, editor: item, attributes: [title]}
"#,
    );
    let ext = parse(
        r#"
name: ext
version: 2.0.0
nodeTypes:
  - {name: Ticket, extends: base@Item, attributes: [severity]}
  - {name: Folder, document: {contains: [Ticket, base@Item]}}
linkTypes:
  - {name: blocks, from: Ticket, to: base@Item}
"#,
    );
    let c = goap_typecat::new(&[base, ext]).unwrap();
    let tk = c.r#type("ext@Ticket").unwrap();
    assert!(
        tk.lifecycle.as_ref().is_some_and(|l| l.name == "simple")
            && tk.editor == "item"
            && tk.property_names() == ["title", "severity"],
        "a type extends a type of another domain: {tk:?}"
    );
    assert_eq!(tk.attributes[0].from, "base@Item");
    let f = c.r#type("ext@Folder").unwrap();
    assert_eq!(
        f.document.as_ref().map(|d| d.contains.clone()),
        Some(vec!["ext@Ticket".to_string(), "base@Item".to_string()]),
        "document references are qualified"
    );
    c.check_link("ext@blocks", "ext@Ticket", "ext@Ticket")
        .unwrap_or_else(|e| panic!("a subtype is accepted where its supertype is: {e}"));

    let err = goap_typecat::new(&[parse(
        "name: bad\nversion: 1.0.0\nnodeTypes: [{name: A, extends: other@B}]\n",
    )])
    .unwrap_err();
    assert!(err.is_unknown(), "unknown supertype: {err}");
    let err = goap_typecat::new(&[parse(
        "name: bad\nversion: 1.0.0\nnodeTypes: [{name: A, extends: B}, {name: B, extends: A}]\n",
    )])
    .unwrap_err();
    assert!(err.is_invalid(), "cycle: {err}");
    let err = goap_typecat::new(&[parse(
        "name: bad\nversion: 1.0.0\nnodeTypes: [A]\nlinkTypes: [{name: l, to: x@Y}]\n",
    )])
    .unwrap_err();
    assert!(err.is_unknown(), "unknown link end: {err}");
}

/// ADR 0039: User extends OrgUnit in the built-in organisation domain, so a User node satisfies an OrgUnit-typed link
/// end (Assignment's assigns_org link can target a unit, a team or a person uniformly), without member_of folding User
/// into the part_of unit tree.
#[test]
fn user_satisfies_org_unit() {
    let c = builtin();
    let ut = c.r#type("organisation@User");
    assert!(
        ut.is_some_and(|u| u.is(&tr("organisation", "OrgUnit"))),
        "organisation@User must extend organisation@OrgUnit: {ut:?}"
    );
    c.check_link(
        "organisation@assigns_org",
        "organisation@Assignment",
        "organisation@User",
    )
    .unwrap();
    c.check_link(
        "organisation@assigns_org",
        "organisation@Assignment",
        "organisation@OrgUnit",
    )
    .unwrap();
    // part_of and project_part_of share a shape (child -> parent) but not a name, on purpose: the catalogue merges
    // same-named link types declared for several end pairs into "accepts any pair", which would erase part_of's
    // OrgUnit-only constraint. A ProjectUnit must stay rejected by part_of.
    assert!(
        c.check_link(
            "organisation@part_of",
            "organisation@ProjectUnit",
            "organisation@ProjectUnit"
        )
        .is_err(),
        "part_of must still reject a ProjectUnit"
    );
    c.check_link(
        "organisation@project_part_of",
        "organisation@ProjectUnit",
        "organisation@ProjectUnit",
    )
    .unwrap();
}

/// Process/Step/Method/MethodStep/Action all extend methodology@Activity (architecture plan "Activity concept"), so
/// the generic specializes link (declared from/to Activity) accepts any of them, at either end.
#[test]
fn activity_specializes_is_transitive() {
    let c = builtin();
    let activity = tr("methodology", "Activity");
    for name in ["Process", "Step", "Method", "MethodStep", "Action"] {
        let typ = c.r#type(&format!("methodology@{name}"));
        assert!(
            typ.is_some_and(|t| t.is(&activity)),
            "methodology@{name} must extend methodology@Activity"
        );
    }
    for (a, b) in [
        ("Action", "Action"),
        ("Method", "Action"),
        ("Process", "Process"),
        ("Step", "MethodStep"),
    ] {
        c.check_link(
            "methodology@specializes",
            &format!("methodology@{a}"),
            &format!("methodology@{b}"),
        )
        .unwrap_or_else(|e| panic!("specializes({a}, {b}): {e}"));
    }
    assert!(
        c.check_link(
            "methodology@specializes",
            "methodology@Role",
            "methodology@Action"
        )
        .is_err(),
        "a Role is not an Activity: specializes must still reject it"
    );
}

#[test]
fn compose_flag() {
    let c = builtin();
    for r in ["methodology@defines", "methodology@sub_activity"] {
        assert!(c.composes(r), "{r}: compose, want true");
    }
    assert!(
        c.link_type("methodology@specializes")
            .is_some_and(|l| !l.compose),
        "specializes: compose, want false"
    );
}

/// The structures (ADR 0054) are the types the domains tag: the built-in organisation domain tags the organisation
/// and the project, users are units through subtyping, and no other domain may tag a structure again.
#[test]
fn structures() {
    let c = builtin();
    let org = c.structure(STRUCTURE_ORGANISATION).unwrap().clone();
    assert!(
        org.r#type == "organisation@OrgUnit"
            && org.namespace == "organisation"
            && org.parent == "organisation@part_of"
            && org.root == "ORG-DEFAULT"
            && !org.self_parent
            && org.bootstrap["name"] == "Default organisation",
        "organisation = {org:?}"
    );
    let proj = c.structure(STRUCTURE_PROJECT).unwrap().clone();
    assert!(
        proj.r#type == "organisation@ProjectUnit"
            && proj.parent == "organisation@project_part_of"
            && proj.root == "PROJ-ROOT"
            && proj.self_parent,
        "project = {proj:?}"
    );
    assert!(
        c.is_a("organisation@User", &org.r#type)
            && !c.is_a(&proj.r#type, &org.r#type)
            && !c.is_a("organisation@Nope", &org.r#type)
    );
    // what the graph service tells the services reading the organisation (GetStructures): a User is a unit
    let want = Structures(vec![
        StructureSet {
            structure: org,
            types: vec!["organisation@OrgUnit".into(), "organisation@User".into()],
        },
        StructureSet {
            structure: proj,
            types: vec!["organisation@ProjectUnit".into()],
        },
    ]);
    assert_eq!(c.structures(), want);
    let other = parse(
        r#"
name: hr
version: 1.0.0
nodeTypes:
  - {name: Team, structure: {kind: organisation, parent: within, root: HR-ROOT}}
linkTypes:
  - {name: within, from: Team, to: Team}
"#,
    );
    let issues = other.validate();
    assert!(issues.is_empty(), "the domain itself is valid: {issues}");
    let err = goap_typecat::new(&[other]).unwrap_err();
    assert!(err.is_invalid(), "a second organisation structure: {err}");
}

/// A structure tag names a kind, a root and a parent link type of its domain from and to the tagged type.
#[test]
fn structure_tag_issues() {
    let d = parse(
        r#"
name: hr
version: 1.0.0
nodeTypes:
  - {name: Team, structure: {kind: "", parent: nope}}
  - {name: Site, structure: {kind: project, parent: near, root: S-1}}
  - {name: Other}
linkTypes:
  - {name: near, from: Site, to: Other}
"#,
    );
    let paths: Vec<String> = d.validate().iter().map(|i| i.path.clone()).collect();
    for want in [
        "nodeTypes[0].structure.kind",
        "nodeTypes[0].structure.root",
        "nodeTypes[0].structure.parent",
        "nodeTypes[1].structure.parent",
    ] {
        assert!(
            paths.iter().any(|p| p == want),
            "no issue on {want}: {paths:?}"
        );
    }
}

#[test]
fn link_type_validators_resolved() {
    let d = parse(
        r#"
name: lv
version: 1.0.0
algorithms:
  - {name: pv, type: property_validator, language: javascript, code: "return"}
  - {name: lv, type: link_validator, language: javascript, code: "return"}
algorithmInstances:
  - {name: v1, algorithm: pv}
  - {name: l1, algorithm: lv}
nodeTypes: [{name: A}]
linkTypes:
  - {name: r, from: A, to: A, validators: [l1], attributes: [{name: w, validators: [v1]}]}
  - {name: plain, from: A, to: A}
"#,
    );
    let c = goap_typecat::new(&[d]).unwrap();
    let vs = c.link_type_validators("lv@r");
    assert!(
        vs.len() == 1 && vs[0].instance == "l1" && vs[0].r#type == USAGE_LINK_VALIDATOR,
        "link validators: {vs:?}"
    );
    let vs = c.link_attribute_validators("lv@r");
    assert!(
        vs.len() == 1 && vs[0].instance == "v1" && vs[0].property == "w",
        "attribute validators: {vs:?}"
    );
    assert!(
        c.link_type_validators("lv@plain").is_empty()
            && c.link_attribute_validators("lv@nope").is_empty(),
        "none declared, none unknown"
    );
}

struct Domains(Result<Vec<goap_def::Domain>, String>);

#[async_trait]
impl Source for Domains {
    async fn domains(&self, _: &Ctx) -> Result<Vec<goap_def::Domain>, BoxError> {
        self.0.clone().map_err(|e| e.into())
    }
}

/// A live catalogue starts with the built-in domains and keeps the one in force when its source fails.
#[tokio::test]
async fn live_reloads() {
    let ctx = Ctx::background();
    let live = Live::new(Arc::new(Domains(Ok(vec![from_file("alm")]))));
    assert!(!live.get().has_node_type("alm@Requirement"));
    live.reload(&ctx).await.unwrap();
    assert!(live.get().has_node_type("alm@Requirement"));
    let failing = Live::new(Arc::new(Domains(Err("registry down".into()))));
    let err = failing.reload(&ctx).await.unwrap_err();
    assert_eq!(err.to_string(), "registry down");
    assert!(failing.get().has_node_type("organisation@User"));
}
