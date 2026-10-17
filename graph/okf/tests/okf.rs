//! Port of `okf_test.go`.

use std::collections::BTreeMap;
use std::io::Read;
use std::sync::Arc;

use chrono::{TimeZone, Utc};
use goap_graph::{SNAPSHOT_FORMAT, Snapshot, SnapshotLink, SnapshotNode};
use serde_json::{Map, Value, json};

fn props(v: Value) -> Map<String, Value> {
    match v {
        Value::Object(m) => m,
        _ => unreachable!(),
    }
}

// A snapshot is written as a zip of one Markdown file per node: the identity, properties and links in the YAML front
// matter, the name as heading, the text area attributes as sections, links and back links as relative Markdown links.
#[test]
fn render() {
    let dir = concat!(env!("CARGO_MANIFEST_DIR"), "/../../domains");
    let ds = goap_def::load_domains(dir).unwrap();
    let cat = Arc::new(goap_typecat::Catalog::new(&ds).unwrap());
    let at = Utc.with_ymd_and_hms(2026, 10, 13, 0, 0, 0).unwrap();
    let s = Snapshot {
        format: SNAPSHOT_FORMAT.to_string(),
        namespace: "alm".into(),
        branch: "main".into(),
        baseline: "B1".into(),
        exported_at: at,
        nodes: vec![
            SnapshotNode {
                key: "NEED:1".into(),
                r#type: "alm@Need".into(),
                state: "draft".into(),
                owner: "ORG-DEFAULT".into(),
                properties: props(
                    json!({"title": "Pay online", "description": "The customer\npays by card."}),
                ),
                links: vec![SnapshotLink {
                    r#type: "alm@refines".into(),
                    to: "REQ-1".into(),
                    properties: props(json!({"why": "x"})),
                }],
                ..Default::default()
            },
            SnapshotNode {
                key: "NEED-1".into(),
                r#type: "alm@Need".into(),
                properties: props(json!({"title": "Same file name"})),
                ..Default::default()
            },
            SnapshotNode {
                key: "REQ-1".into(),
                r#type: "alm@Requirement".into(),
                state: "approved".into(),
                properties: props(json!({"title": "Card payment", "state": "x"})),
                ..Default::default()
            },
        ],
    };
    let mut buf = Vec::new();
    goap_okf::write(&mut buf, &s, &goap_okf::from_catalog(cat), s.exported_at).unwrap();
    let mut z = zip::ZipArchive::new(std::io::Cursor::new(buf)).unwrap();
    let mut got = BTreeMap::new();
    let mut first = String::new();
    for i in 0..z.len() {
        let mut f = z.by_index(i).unwrap();
        let mut b = String::new();
        f.read_to_string(&mut b).unwrap();
        if i == 0 {
            first = f.name().unwrap().to_string();
        }
        got.insert(f.name().unwrap().to_string(), b);
    }
    assert_eq!(first, "alm/index.md", "the index comes first");
    let nonempty = |k: &str| got.get(k).is_some_and(|v| !v.is_empty());
    assert!(
        got.contains_key("alm/Need/NEED-1.md")
            && nonempty("alm/Need/NEED-1-2.md")
            && nonempty("alm/Requirement/REQ-1.md")
            && nonempty("alm/index.md"),
        "files: {:?}",
        got.keys().collect::<Vec<_>>()
    );
    let need = &got["alm/Need/NEED-1.md"];
    let parts: Vec<&str> = need.splitn(3, "---\n").collect();
    assert!(
        parts.len() == 3 && parts[0].is_empty(),
        "front matter: {need:?}"
    );
    let fm: Value = serde_yaml_ng::from_str(parts[1]).unwrap();
    let want = json!({
        "key": "NEED:1", "type": "alm@Need", "title": "Pay online", "state": "draft", "owner": "ORG-DEFAULT",
        "namespace": "alm", "baseline": "B1",
        "aliases": ["NEED:1"], "tags": ["alm/Need", "state/draft"],
        "refines": ["[[Requirement/REQ-1|Card payment]]"],
        "linkProperties": [{"type": "alm@refines", "to": "REQ-1", "properties": {"why": "x"}}],
    });
    assert_eq!(fm, want, "front matter:\n{}", parts[1]);
    assert!(
        parts[1].contains("- '[[Requirement/REQ-1|Card payment]]'")
            && parts[1].starts_with("key: "),
        "ordered front matter, quoted wikilinks:\n{}",
        parts[1]
    );
    for want in [
        "# Pay online\n",
        "## Description\n\nThe customer\npays by card.\n",
        "- refines: [Card payment](../Requirement/REQ-1.md)",
    ] {
        assert!(
            parts[2].contains(want),
            "body misses {want:?}:\n{}",
            parts[2]
        );
    }
    let req = &got["alm/Requirement/REQ-1.md"];
    assert!(
        req.contains("## Referenced by\n\n- [Pay online](../Need/NEED-1.md) refines"),
        "back link:\n{req}"
    );
    assert!(
        req.contains("\nprop_state: x\n"),
        "a property named like a key of the front matter takes the prefix:\n{req}"
    );
    let idx = &got["alm/index.md"];
    assert!(
        idx.contains("format: goap.okf/v1")
            && idx.contains("- [Card payment](Requirement/REQ-1.md) `REQ-1`"),
        "index:\n{idx}"
    );
}
