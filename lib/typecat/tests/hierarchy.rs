//! Port of hierarchy_test.go.

use std::collections::HashMap;

use goap_domain::{Link, Node, NodeID, STRUCTURE_ORGANISATION, STRUCTURE_PROJECT};

/// The hierarchy of a structure keeps the links the structure names between nodes of its types (subtypes included).
#[test]
fn structures_hierarchy() {
    let st = goap_typecat::builtin().structures();
    let (org, proj) = (st.organisation(), st.project());
    let user = st.types_of(STRUCTURE_ORGANISATION).last().unwrap().clone(); // a User is a unit: a subtype
    let node = |id: &str, typ: &str| Node {
        id: NodeID::from(id),
        namespace: org.namespace.clone(),
        key: id.into(),
        r#type: typ.into(),
        ..Node::default()
    };
    let nodes = vec![
        node(&org.root, &org.r#type),
        node("ORG-A", &org.r#type),
        node("USR:x", &user),
        node(&proj.root, &proj.r#type),
    ];
    let by_key: HashMap<String, Node> = nodes.iter().map(|n| (n.key.clone(), n.clone())).collect();
    let link = |typ: &str, from: &str, to: &str| Link {
        r#type: typ.into(),
        from: by_key[from].r#ref(),
        to: by_key[to].r#ref(),
        ..Link::default()
    };
    let links = vec![
        link(&org.parent, "ORG-A", &org.root),
        link("organisation@member_of", "USR:x", "ORG-A"),
        link(&proj.parent, &proj.root, &proj.root),
    ];
    let h = st.hierarchy(STRUCTURE_ORGANISATION, &nodes, &links);
    assert_eq!(
        h.chain("ORG-A"),
        vec!["ORG-A".to_string(), org.root.clone()]
    );
    assert_eq!(h.parent("USR:x"), "", "member_of is not part_of");
    assert_eq!(
        st.hierarchy(STRUCTURE_PROJECT, &nodes, &links)
            .chain(&proj.root),
        vec![proj.root.clone()]
    );
}
