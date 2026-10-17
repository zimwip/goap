use std::collections::BTreeMap;

use goap_connect::Code;
use goap_engine::{Error, Process};
use goap_enginesvc::{process_to_pb, to_connect};

#[test]
fn engine_errors_to_connect() {
    let c = |e: Error| to_connect(e).code();
    assert_eq!(c(Error::not_found("p")), Code::NotFound);
    assert_eq!(
        c(Error::UnknownMethodology("unknown methodology x".into())),
        Code::NotFound
    );
    assert_eq!(c(Error::forbidden("start")), Code::PermissionDenied);
    assert_eq!(
        c(Error::invalid_state("p is done")),
        Code::FailedPrecondition
    );
    assert_eq!(
        c(Error::from(goap_graphapi::Error::conflict("moved")).context("merge")),
        Code::FailedPrecondition
    );
    let ce = to_connect(Error::from(goap_graphapi::Error::conflict("moved")).context("merge"));
    assert_eq!(ce.message(), "merge: moved");
}

#[test]
fn process_to_pb_sorts_disabled() {
    let p = Process {
        id: "p".into(),
        disabled: BTreeMap::from([("b".to_string(), true), ("a".to_string(), true)]),
        ..Default::default()
    };
    let out = process_to_pb(&p);
    assert_eq!(out.disabled, vec!["a", "b"]);
    assert!(out.created_at.is_none(), "zero time is no timestamp");
}
