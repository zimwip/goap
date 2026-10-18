//! Port of linklabels_test.go.

mod common;
use common::parse;

/// A link type reads from its source with its label and from its target with its inverse label; the label falls back
/// to the name, the inverse label to the label.
#[test]
fn link_labels() {
    let c = goap_typecat::new(&[parse(
        r#"
name: lbl
version: 1.0.0
nodeTypes: [Req, Test]
linkTypes:
  - {name: verifies, from: Test, to: Req, label: verifies, inverseLabel: verified by}
  - {name: traces, from: Req, to: Req, label: traces to}
  - {name: relates, from: Req, to: Test}
"#,
    )])
    .unwrap();
    for (r, want) in [
        ("lbl@verifies", ("verifies", "verified by")),
        ("lbl@traces", ("traces to", "traces to")),
        ("lbl@relates", ("relates", "relates")),
    ] {
        let l = c.link_type(r).unwrap_or_else(|| panic!("{r}: unknown"));
        assert!(
            l.label == want.0 && l.inverse_label == want.1,
            "{r}: labels {:?} / {:?}, want {want:?}",
            l.label,
            l.inverse_label
        );
    }
}
