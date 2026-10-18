//! Port of personal_test.go.

use goap_access::*;
use goap_domain::Change;

fn owned_by(org: &str) -> Change {
    Change {
        owner_org: org.into(),
        ..Change::default()
    }
}

#[test]
fn personal_unit_is_the_user_node() {
    assert_eq!(
        personal_unit("alice"),
        user_key("alice"),
        "the personal unit is the user node"
    );
    assert!(is_personal_unit("USR:alice") && !is_personal_unit("ORG-DEFAULT"));
    assert!(personal_subject("USR:alice") == "alice" && personal_subject("ORG-DEFAULT").is_empty());
    let c = owned_by(&personal_unit("alice"));
    assert!(
        is_personal(&c)
            && is_personal_to(&c, "alice")
            && !is_personal_to(&c, "bob")
            && !is_personal_to(&c, ""),
        "change personality"
    );
    assert!(
        !is_personal(&owned_by("ORG-DEFAULT")),
        "a shared change is not personal"
    );
}
