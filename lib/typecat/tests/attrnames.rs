//! Port of attrnames_test.go.

mod common;
use common::parse;

/// A closed type's properties are its attributes, ancestors' included; `additionalProperties` opens a type and its
/// subtypes; a type the catalogue does not know is open (strict attributes).
#[test]
fn attribute_names() {
    let d = parse(
        r#"
name: shop
version: 1.0.0
nodeTypes:
  - {name: Item, attributes: [{name: title, type: string}]}
  - {name: Book, extends: Item, attributes: [{name: isbn, type: string}]}
  - {name: Bag, additionalProperties: true, attributes: [{name: label, type: string}]}
  - {name: Pouch, extends: Bag}
"#,
    );
    let issues = d.validate();
    assert!(issues.is_empty(), "valid: {issues}");
    let c = goap_typecat::new(&[d]).unwrap();
    let (names, open) = c.attribute_names("shop@Book");
    assert!(
        !open && names == ["title", "isbn"],
        "Book: {names:?} open={open}"
    );
    assert!(c.attribute_names("shop@Bag").1, "Bag should be open");
    let (names, open) = c.attribute_names("shop@Pouch");
    assert!(
        open && names == ["label"],
        "Pouch inherits the opening: {names:?} open={open}"
    );
    assert!(c.attribute_names("shop@Nope").1, "an unknown type is open");
}
