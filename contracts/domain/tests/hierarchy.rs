use std::collections::BTreeMap;

use goap_domain::*;

#[test]
fn hierarchy_chain() {
    let mut h = Hierarchy::new("ROOT");
    h.add("C", "B");
    h.add("B", "A");
    h.add("A", "A"); // a self link is no parent
    h.add("X", "Y"); // a cycle stops the walk
    h.add("Y", "X");
    let cases: BTreeMap<&str, Vec<&str>> = [
        ("C", vec!["C", "B", "A", "ROOT"]),
        ("A", vec!["A", "ROOT"]),
        ("", vec!["ROOT"]),
        ("ROOT", vec!["ROOT"]),
        ("X", vec!["X", "Y", "ROOT"]),
        ("new", vec!["new", "ROOT"]),
    ]
    .into();
    for (key, want) in cases {
        assert_eq!(h.chain(key), want, "Chain({key:?})");
    }
}

#[test]
fn structures_hierarchy() {
    let s = Structures(vec![StructureSet {
        structure: Structure {
            kind: STRUCTURE_ORGANISATION.into(),
            r#type: "organisation@OrgUnit".into(),
            parent: "organisation@part_of".into(),
            root: "ORG-DEFAULT".into(),
            ..Default::default()
        },
        types: vec!["organisation@OrgUnit".into(), "organisation@User".into()],
    }]);
    let node = |id: &str, key: &str, t: &str| Node {
        id: id.into(),
        key: key.into(),
        r#type: t.into(),
        ..Default::default()
    };
    let nodes = vec![
        node("1", "ORG-DEFAULT", "organisation@OrgUnit"),
        node("2", "ORG-A", "organisation@OrgUnit"),
        node("3", "USR:ann", "organisation@User"),
    ];
    let link = |from: &str, to: &str| Link {
        r#type: "organisation@part_of".into(),
        from: NodeRef::new(from, 1),
        to: NodeRef::new(to, 1),
        ..Default::default()
    };
    let h = s.hierarchy(
        STRUCTURE_ORGANISATION,
        &nodes,
        &[link("2", "1"), link("3", "2")],
    );
    assert_eq!(h.chain("USR:ann"), vec!["USR:ann", "ORG-A", "ORG-DEFAULT"]);
    assert_eq!(s.organisation().root, "ORG-DEFAULT");
    assert!(s.project().root.is_empty());
    let v = serde_json::to_value(&s).unwrap();
    assert_eq!(v[0]["kind"], "organisation");
    assert_eq!(v[0]["types"][1], "organisation@User");
}
