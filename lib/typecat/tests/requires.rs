//! Port of requires_test.go.

mod common;
use common::parse;
use goap_domain::RequiredLink;
use goap_typecat::{builtin, builtins};

fn rl(link: &str, count: i64) -> RequiredLink {
    RequiredLink {
        link: link.into(),
        count,
    }
}

/// The built-in domains flag these types, no more, no less (ADR 0068).
#[test]
fn builtin_admin_only() {
    let flagged = [
        "organisation@OrgUnit",
        "organisation@User",
        "organisation@ProjectUnit",
        "organisation@Adapter",
        "platform@AdapterDef",
        "organisation@Policy",
        "organisation@Assignment",
        "organisation@CriticalityPolicy",
        "platform@LlmBehavior",
    ];
    let c = builtin();
    let mut wrong = Vec::new();
    for d in builtins() {
        for n in &d.node_types {
            let typ = format!("{}@{}", d.name, n.name);
            let want = flagged.contains(&typ.as_str());
            if c.admin_only(&typ) != want {
                wrong.push(format!("{typ}: adminOnly {}, want {want}", !want));
            }
        }
    }
    assert!(wrong.is_empty(), "{}", wrong.join("\n"));
}

/// A node type declares the links its nodes must carry (ADR 0065): the built-in organisation domain requires one
/// member_of of a User, a subtype inherits and may redefine them, and a bad declaration is an issue.
#[test]
fn requires() {
    let c = builtin();
    assert_eq!(
        c.requires("organisation@User"),
        [rl("organisation@member_of", 1)]
    );
    assert!(c.requires("organisation@OrgUnit").is_empty());
    let d = parse(
        r#"
name: hr
version: 1.0.0
nodeTypes:
  - {name: Badge, requires: [{link: owner}, {link: site, count: 2}]}
  - {name: Guest, extends: Badge, requires: [{link: site, count: 1}]}
linkTypes:
  - {name: owner, from: Badge}
  - {name: site, from: Badge}
"#,
    );
    let issues = d.validate();
    assert!(issues.is_empty(), "valid: {issues}");
    let hr = goap_typecat::new(&[d]).unwrap();
    assert_eq!(
        hr.requires("hr@Guest"),
        [rl("hr@owner", 1), rl("hr@site", 1)]
    );
    let bad = parse(
        r#"
name: hr
version: 1.0.0
nodeTypes:
  - {name: Badge, requires: [{link: nope}, {link: ""}]}
"#,
    );
    let paths: Vec<String> = bad.validate().iter().map(|i| i.path.clone()).collect();
    for p in [
        "nodeTypes[0].requires[0].link",
        "nodeTypes[0].requires[1].link",
    ] {
        assert!(paths.iter().any(|x| x == p), "no issue on {p}: {paths:?}");
    }
}
