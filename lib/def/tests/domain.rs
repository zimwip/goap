//! Port of domain_test.go.

use goap_def::*;
use goap_form::{Field, Form, WIDGET_LINK, WIDGET_NEW_NODE, WIDGET_NUMBER, WIDGET_TEXT};
use serde_json::json;

fn test_domain() -> Domain {
    Domain {
        name: "alm".into(),
        version: "1".into(),
        schema: Schema {
            node_types: vec![NodeType::named("Requirement"), NodeType::named("TestCase")],
            link_types: vec![LinkType {
                name: "verifies".into(),
                from: "TestCase".into(),
                to: "Requirement".into(),
                ..LinkType::default()
            }],
            ..Schema::default()
        },
        ..Domain::default()
    }
}

/// A link type may declare what it carries (architecture plan "Activity concept": specializes declares
/// when/priority), documentary only - not yet enforced by the type catalogue, same as node property lists are
/// for search/display rather than a hard schema.
#[test]
fn link_type_attributes_parse() {
    let d = parse_domain(
        br#"
name: m
version: "1"
nodeTypes: [{name: A}]
linkTypes:
  - {name: specializes, from: A, to: A, attributes: [when, priority]}
"#,
    )
    .unwrap();
    assert!(
        d.link_types.len() == 1
            && d.link_types[0].attributes.len() == 2
            && d.link_types[0].attributes[1].name == "priority",
        "link type attributes: {:?}",
        d.link_types
    );
}

#[test]
fn domain_validate() {
    let issues = test_domain().validate();
    assert!(issues.is_empty(), "{issues}");
    let mut bad = test_domain();
    bad.link_types.push(LinkType {
        name: "x".into(),
        from: "Nope".into(),
        ..LinkType::default()
    });
    let issues = bad.validate();
    assert!(
        issues.len() == 1 && issues[0].path == "linkTypes[1].from",
        "issues: {issues}"
    );
}

#[test]
fn domain_yaml_round_trip() {
    let out = test_domain().yaml().unwrap();
    let d = parse_domain(out.as_bytes()).unwrap();
    assert!(
        d.name == "alm" && d.node_types.len() == 2 && d.link_types[0].to == "Requirement",
        "round trip: {d:?}"
    );
    assert_eq!(d, test_domain());
}

/// The JSON form is flat too (Go's embedded Schema) and parses back.
#[test]
fn domain_json_is_flat() {
    let v = serde_json::to_value(test_domain()).unwrap();
    assert_eq!(
        v,
        json!({"name": "alm", "version": "1", "nodeTypes": [{"name": "Requirement"}, {"name": "TestCase"}],
            "linkTypes": [{"name": "verifies", "from": "TestCase", "to": "Requirement"}]})
    );
    let d = parse_domain(v.to_string().as_bytes()).unwrap();
    assert_eq!(d, test_domain());
}

/// Unknown fields are rejected (yaml.v3's KnownFields), except inside a node type, an attribute or an enum value: Go
/// decodes their shorthand with a custom UnmarshalYAML, which reads the object again without the check.
#[test]
fn unknown_fields_are_rejected() {
    for src in [
        "name: m\nversion: 1\nnodeTypes: [A]\nnope: 1\n",
        "name: m\nversion: 1\nnodeTypes: [A]\nlifecycles: [{name: l, initial: a, states: [{name: a, odd: true}]}]\n",
        "name: m\nversion: 1\nnodeTypes: [A]\nlinkTypes: [{name: l, odd: true}]\n",
        "name: m\nversion: 1\nnodeTypes: [A]\nenums: [{name: e, odd: true, values: [a]}]\n",
    ] {
        let err = parse_domain(src.as_bytes()).unwrap_err();
        assert!(err.to_string().starts_with("parse domain: field "), "{err}");
    }
    for src in [
        "name: m\nversion: 1\nnodeTypes: [{name: A, colour: red}]\n",
        "name: m\nversion: 1\nnodeTypes: [A]\nlinkTypes: [{name: l, attributes: [{name: a, size: 2}]}]\n",
        "name: m\nversion: 1\nnodeTypes: [A]\nenums: [{name: e, values: [{value: a, odd: 1}]}]\n",
    ] {
        parse_domain(src.as_bytes()).unwrap_or_else(|e| panic!("{src}: {e}"));
    }
    assert!(parse_domain(b"").is_err());
}

#[test]
fn node_type_editor() {
    let mut d = test_domain();
    d.node_types[0].editor = "requirement-board".into();
    let issues = d.validate();
    assert!(issues.is_empty(), "{issues}");
    let out = d.yaml().unwrap();
    let back = parse_domain(out.as_bytes()).unwrap();
    assert_eq!(back.node_types[0].editor, "requirement-board");
    d.node_types[1].editor = "Test Case".into();
    let issues = d.validate();
    assert!(
        issues.len() == 1 && issues[0].path == "nodeTypes[1].editor",
        "issues: {issues}"
    );
}

#[test]
fn node_type_forms() {
    let field = |name: &str, widget: &str| Field {
        name: name.into(),
        widget: widget.into(),
        ..Field::default()
    };
    let mut link = field("parts", WIDGET_LINK);
    link.constraints = json!({"fromKey": "OTHER"}).as_object().unwrap().clone();
    let d = Domain {
        name: "x".into(),
        version: "1.0.0".into(),
        schema: Schema {
            node_types: vec![
                NodeType {
                    name: "Base".into(),
                    attributes: vec![Attribute::named("title")],
                    ..NodeType::default()
                },
                NodeType {
                    name: "Item".into(),
                    extends: "Base".into(),
                    attributes: vec![Attribute::named("size")],
                    form: Some(Form {
                        fields: vec![
                            field("title", WIDGET_TEXT),
                            field("size", WIDGET_NUMBER),
                            field("colour", WIDGET_TEXT),
                            link,
                            field("made", WIDGET_NEW_NODE),
                        ],
                        ..Form::default()
                    }),
                    ..NodeType::default()
                },
            ],
            ..Schema::default()
        },
        ..Domain::default()
    };
    let got = d.validate().to_string();
    for want in [
        "nodeTypes[1].form.fields[2].name: colour is not an attribute of Item",
        "nodeTypes[1].form.fields[3].constraints.fromKey: a link field of a creation form starts from the node",
        "nodeTypes[1].form.fields[4].widget: a creation form sets the node: no newNode field",
    ] {
        assert!(got.contains(want), "missing {want:?} in {got}");
    }
    assert!(
        !got.contains("title is not") && !got.contains("size is not"),
        "own and inherited attributes accepted: {got}"
    );
}

/// Issues split into errors and warnings; only errors block.
#[test]
fn issues_severities() {
    let mut is = Issues::default();
    assert!(!is.has_errors());
    is.push(Issue {
        path: "a".into(),
        message: "remark".into(),
        severity: SEVERITY_WARNING.into(),
        ..Issue::default()
    });
    assert!(!is.has_errors() && is.warnings().len() == 1 && is.errors().is_empty());
    is.add("", "broken");
    assert!(is.has_errors());
    assert_eq!(is.to_string(), "a: remark; broken");
    assert_eq!(
        serde_json::to_string(&is[1]).unwrap(),
        r#"{"path":"","message":"broken"}"#
    );
}

/// The TypeSet of files: the built-in domains and the domains given, a bare name being a type of its domain.
#[test]
fn domain_types_of_files() {
    let d = parse_domain(
        b"name: shop\nversion: 1\nnodeTypes: [{name: Item}, {name: Book, extends: Item}, {name: Unit, extends: organisation@OrgUnit}]\nlinkTypes: [{name: has}]\n",
    )
    .unwrap();
    let t = domain_types(&[d]);
    assert!(t.has_node_type("shop@Book") && t.has_node_type("organisation@User"));
    assert!(t.has_link_type("shop@has") && !t.has_link_type("shop@nope"));
    let st = t.supertypes();
    assert_eq!(st["shop@Book"], ["shop@Item"]);
    assert_eq!(st["shop@Unit"], ["organisation@OrgUnit"]);
    assert!(!st.contains_key("shop@Item"));
}

/// The latest version of each domain: the latest published one, else the latest draft; in force: published only.
#[test]
fn latest_and_in_force() {
    use chrono::{TimeZone, Utc};
    let at = |s| Some(Utc.timestamp_opt(s, 0).unwrap());
    let rec = |name: &str, version: &str, status: Status, created, published| Record {
        domain: Domain {
            name: name.into(),
            version: version.into(),
            ..Domain::default()
        },
        status,
        created_at: at(created),
        published_at: published,
        ..Record::default()
    };
    let rs = vec![
        rec("a", "1", STATUS_PUBLISHED, 1, at(2)),
        rec("b", "1", STATUS_DRAFT, 1, None),
        rec("a", "2", STATUS_DRAFT, 3, None),
        rec("a", "3", STATUS_PUBLISHED, 4, at(5)),
        rec("b", "2", STATUS_DRAFT, 2, None),
        rec("b", "3", STATUS_ARCHIVED, 9, None),
    ];
    let got: Vec<String> = latest(&rs)
        .iter()
        .map(|r| format!("{}@{}", r.domain.name, r.domain.version))
        .collect();
    assert_eq!(got, ["a@3", "b@2"]);
    let force: Vec<String> = in_force(&rs).iter().map(|d| d.version.clone()).collect();
    assert_eq!(force, ["3"]);
}

/// The domain files of a directory, in name order.
#[test]
fn load_domains_of_a_directory() {
    let dir = std::env::temp_dir().join(format!("goap-def-load-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("b.yaml"), "name: b\nversion: 1\nnodeTypes: [X]\n").unwrap();
    std::fs::write(dir.join("a.yml"), "name: a\nversion: 1\nnodeTypes: [X]\n").unwrap();
    std::fs::write(dir.join("c.txt"), "nothing").unwrap();
    let ds = load_domains(&dir).unwrap();
    assert_eq!(
        ds.iter().map(|d| d.name.as_str()).collect::<Vec<_>>(),
        ["a", "b"]
    );
    std::fs::write(dir.join("d.yaml"), "name: d\nbad: 1\n").unwrap();
    let err = load_domains(&dir).unwrap_err().to_string();
    assert!(err.contains("d.yaml: parse domain: "), "{err}");
    std::fs::remove_dir_all(&dir).unwrap();
}
