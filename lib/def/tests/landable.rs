//! Port of landable_test.go.

use goap_domain::{Lifecycle, LifecycleState, Transition};

fn st(name: &str, not_landable: bool, r#final: bool) -> LifecycleState {
    LifecycleState {
        name: name.into(),
        not_landable,
        r#final,
        ..LifecycleState::default()
    }
}

fn tr(name: &str, from: &str, to: &str) -> Transition {
    Transition {
        name: name.into(),
        from: from.into(),
        to: to.into(),
        ..Transition::default()
    }
}

/// A state is landable unless flagged notLandable (ADR 0078); the issues name what could never land.
#[test]
fn lifecycle_landable() {
    let lc = Lifecycle {
        name: "l".into(),
        initial: "a".into(),
        states: vec![
            st("a", true, false),
            st("b", false, false),
            st("c", true, true),
        ],
        transitions: vec![tr("t", "a", "b"), tr("u", "b", "c")],
        ..Lifecycle::default()
    };
    assert!(
        !lc.landable("a") && lc.landable("b") && lc.landable("") && lc.landable("unknown"),
        "only a flagged state is not landable; no state or an unknown one is landable"
    );
    assert!(
        Lifecycle::landable_in(None, "x"),
        "no lifecycle: always landable"
    );
    let is = lc.issues().join("; ");
    assert!(
        is.contains("state c is final and not landable") && !is.contains("state a"),
        "issues: {is}"
    );
    let lc = Lifecycle {
        name: "l".into(),
        initial: "a".into(),
        states: vec![
            st("a", true, false),
            st("b", true, false),
            st("c", false, false),
        ],
        transitions: vec![tr("t", "a", "b")],
        ..Lifecycle::default()
    };
    let is = lc.issues().join("; ");
    assert!(
        is.contains("state a is not landable and cannot reach a landable state")
            && is.contains("state b is not landable and cannot reach"),
        "issues: {is}"
    );
    let lc = Lifecycle {
        name: "l".into(),
        initial: "a".into(),
        states: vec![st("a", true, false)],
        ..Lifecycle::default()
    };
    let is = lc.issues().join("; ");
    assert!(
        is.contains("at least one landable state required"),
        "issues: {is}"
    );
}
