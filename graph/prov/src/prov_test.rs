use std::collections::BTreeSet;

use chrono::{DateTime, TimeZone, Utc};
use goap_domain::{
    self as domain, Change, ChangeImpact, ChangeItem, ImpactEvent, LogEntry, NodeRef, Review,
};
use serde_json::{Map, Value, json};

use crate::prov::*;

fn entries(evs: &[ImpactEvent]) -> Vec<LogEntry> {
    evs.iter()
        .enumerate()
        .map(|(i, ev)| {
            let mut e = domain::impact_entry(ev).unwrap();
            e.seq = i as i64 + 1;
            e
        })
        .collect()
}

fn find<'a>(doc: &'a Document, id: &str) -> &'a Map<String, Value> {
    doc.graph
        .iter()
        .find(|n| n.get("@id").and_then(Value::as_str) == Some(id))
        .unwrap_or_else(|| panic!("no {id}"))
}

fn refs(v: Option<&Value>) -> BTreeSet<String> {
    let l = match v {
        Some(Value::Array(a)) => a.clone(),
        Some(x) => vec![x.clone()],
        None => vec![],
    };
    l.iter()
        .filter_map(|x| x.get("@id").and_then(Value::as_str).map(str::to_string))
        .collect()
}

fn epoch() -> DateTime<Utc> {
    Utc.timestamp_opt(0, 0).unwrap()
}

fn nr(id: &str, v: i64) -> NodeRef {
    NodeRef::new(id, v)
}

/// A version merged on landing derives from the one written; an adopted flow replaces the runs it makes stale; a
/// rebased impact names the newer head it is checked against.
#[test]
fn impact_events() {
    let c = Change {
        id: "c1".into(),
        title: "t".into(),
        created_at: epoch(),
        ..Default::default()
    };
    let doc = export(
        &c,
        &entries(&[
            ImpactEvent {
                id: "v1".into(),
                change: "c1".into(),
                impact: "i1".into(),
                op: domain::IMPACT_PROPOSED,
                by: "bob".into(),
                state: Some(ChangeImpact {
                    id: "i1".into(),
                    key: "REQ-1".into(),
                    r#type: "alm@Requirement".into(),
                    intent: domain::INTENT_MODIFIED,
                    pre: Some(nr("n1", 1)),
                    ..Default::default()
                }),
                ..Default::default()
            },
            ImpactEvent {
                id: "v2".into(),
                change: "c1".into(),
                impact: "i1".into(),
                op: domain::IMPACT_REBASED,
                pre: Some(nr("n1", 2)),
                ..Default::default()
            },
            ImpactEvent {
                id: "v3".into(),
                change: "c1".into(),
                impact: "i1".into(),
                op: domain::IMPACT_TRANSITIONED,
                post: Some(nr("n1", 3)),
                execution: "e1".into(),
                ..Default::default()
            },
            ImpactEvent {
                id: "v4".into(),
                change: "c1".into(),
                op: domain::IMPACT_ADOPTED,
                flow: "f1".into(),
                stale: vec!["e0".into()],
                by: "bob".into(),
                ..Default::default()
            },
            ImpactEvent {
                id: "v5".into(),
                change: "c1".into(),
                impact: "i1".into(),
                op: domain::IMPACT_LANDED,
                landed: Some(nr("n1", 4)),
                baseline: "b9".into(),
                ..Default::default()
            },
        ]),
    )
    .unwrap();
    assert!(
        refs(find(&doc, "urn:goap:node:n1@v3").get("prov:wasRevisionOf"))
            .contains("urn:goap:node:n1@v2"),
        "the written version revises the head it was rebased on"
    );
    assert!(
        refs(find(&doc, "urn:goap:node:n1@v4").get("prov:wasDerivedFrom"))
            .contains("urn:goap:node:n1@v3"),
        "the merge version derives from the version written"
    );
    let b = find(&doc, "urn:goap:baseline:b9");
    assert!(
        refs(b.get("prov:hadMember")).contains("urn:goap:node:n1@v4")
            && refs(b.get("prov:wasGeneratedBy")).contains("urn:goap:change:c1"),
        "baseline: {b:?}"
    );
    assert!(
        refs(find(&doc, "urn:goap:adoption:v4").get("goap:replaces"))
            .contains("urn:goap:execution:e0"),
        "the adoption replaces the stale runs"
    );
    let n = find(&doc, "urn:goap:node:n1");
    assert!(
        n.get("goap:type") == Some(&json!("alm@Requirement"))
            && n.get("goap:key") == Some(&json!("REQ-1")),
        "node: {n:?}"
    );
    assert!(!doc.marshal().is_empty());
}

#[test]
fn bad_payload() {
    let c = Change {
        id: "c1".into(),
        ..Default::default()
    };
    let e = LogEntry {
        seq: 1,
        r#type: "fact.artifact".into(),
        payload: json!("["),
        ..Default::default()
    };
    let err = export(&c, &[e]).unwrap_err();
    assert!(
        err.to_string().starts_with("log entry 1 (fact.artifact): "),
        "{err}"
    );
}

/// A derogation version is an entity attributed to its signatory (ADR 0075 §2).
#[test]
fn derogation_mapping() {
    let mut b = Builder::default();
    let n = b.node("x", &[]);
    let data = json!({"key": "DRG-1", "rule": "tests pass", "target": "REQ-1",
        "expires": "2030-01-01T00:00:00Z", "status": "open", "signatory": "alice"});
    let it = ChangeItem {
        r#type: "derogation".into(),
        data: data.as_object().unwrap().clone(),
        ..Default::default()
    };
    b.derogation(&n, &it);
    let n = &b.nodes["x"];
    assert!(
        n["goap:rule"] == "tests pass"
            && n["goap:target"] == "REQ-1"
            && n["goap:expires"] == "2030-01-01T00:00:00Z"
            && n["label"] == "derogation DRG-1",
        "derogation: {n:?}"
    );
    assert!(
        refs(n.get("prov:wasAttributedTo")).contains(&principal_iri("alice")),
        "attributed to the signatory: {n:?}"
    );
}

/// A review object is an activity grouping the reviewed events of its entries, with the record versions as entities
/// (ADR 0080).
#[test]
fn review_object_mapping() {
    let c = Change {
        id: "c1".into(),
        title: "t".into(),
        created_at: epoch(),
        ..Default::default()
    };
    let sub = Utc.with_ymd_and_hms(2026, 1, 2, 3, 4, 5).unwrap();
    let data = json!({"key": "REV-1", "status": goap_review::SUBMITTED, "comment": "all good", "by": "alice",
        "submittedAt": domain::gotime::format(&sub),
        "entries": [{"impact": "i1", "outcome": "accept"}]});
    let it = ChangeItem {
        id: "it1".into(),
        r#type: goap_review::KIND_REVIEW,
        status: domain::ITEM_PROPOSED,
        produced_by: "alice".into(),
        created_at: sub,
        data: data.as_object().unwrap().clone(),
        ..Default::default()
    };
    let mut fe = domain::fact_entry(&"c1".into(), &it).unwrap();
    fe.seq = 1;
    let mut ev = entries(&[ImpactEvent {
        id: "e1".into(),
        change: "c1".into(),
        impact: "i1".into(),
        op: domain::IMPACT_REVIEWED,
        by: "alice".into(),
        review: Some(Review {
            status: domain::REVIEW_ACCEPTED,
            by: "alice".into(),
            comment: "fine".into(),
            review_id: "REV-1".into(),
            ..Default::default()
        }),
        ..Default::default()
    }]);
    ev[0].seq = 2;
    let mut all = vec![fe];
    all.extend(ev);
    let doc = export(&c, &all).unwrap();
    let act = find(&doc, &review_object_iri("REV-1"));
    assert!(
        act["goap:status"] == goap_review::SUBMITTED
            && act["rdfs:comment"] == "all good"
            && act.contains_key("prov:endedAtTime")
            && refs(act.get("goap:about")).contains(&impact_iri(&"i1".into()))
            && refs(act.get("prov:wasAssociatedWith")).contains(&principal_iri("alice")),
        "activity: {act:?}"
    );
    let rv = find(&doc, &iri("review", "e1"));
    assert!(
        refs(rv.get("goap:partOf")).contains(&review_object_iri("REV-1")),
        "the reviewed event is part of the review object: {rv:?}"
    );
    let rec = find(&doc, &item_iri(&"it1".into()));
    assert!(
        refs(rec.get("goap:reviewOf")).contains(&review_object_iri("REV-1")),
        "record: {rec:?}"
    );
}

/// The export of a log covering every mapping, as Go's `json.MarshalIndent(doc, "", "  ")` wrote it (testdata,
/// generated by the Go implementation from the same input).
#[test]
fn golden_export() {
    let dir = std::path::Path::new(env!("CARGO_MANIFEST_DIR")).join("testdata");
    let input: Value =
        serde_json::from_slice(&std::fs::read(dir.join("export.input.json")).unwrap()).unwrap();
    let c: Change = serde_json::from_value(input["change"].clone()).unwrap();
    let entries: Vec<LogEntry> = serde_json::from_value(input["entries"].clone()).unwrap();
    let doc = export(&c, &entries).unwrap();
    let want = std::fs::read_to_string(dir.join("export.prov.jsonld")).unwrap();
    let got = doc.marshal_indent("", "  ");
    if got != want {
        let line = got
            .lines()
            .zip(want.lines())
            .position(|(a, b)| a != b)
            .unwrap_or(0);
        panic!(
            "export differs at line {}:\n got: {:?}\nwant: {:?}",
            line + 1,
            got.lines().nth(line),
            want.lines().nth(line)
        );
    }
}
