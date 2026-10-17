//! Port of shipped_domains_test.go.

use goap_def::parse_domain;

/// The domains shipped with the code parse and validate (attributes, enums, algorithms and their plugs).
#[test]
fn shipped_domains_validate() {
    let root = concat!(env!("CARGO_MANIFEST_DIR"), "/");
    let mut failed = Vec::new();
    for f in [
        "../../domains/alm.yaml",
        "../domains/builtin/methodology.yaml",
        "../domains/builtin/organisation.yaml",
        "../domains/builtin/platform.yaml",
    ] {
        let b = std::fs::read(format!("{root}{f}")).unwrap();
        let d = parse_domain(&b).unwrap_or_else(|e| panic!("{f}: {e}"));
        for i in d.validate().iter() {
            failed.push(format!("{f}: {}: {}", i.path, i.message));
        }
    }
    assert!(failed.is_empty(), "{}", failed.join("\n"));
}

/// The built-in domains are parsed once, in their order.
#[test]
fn builtin_domains_are_known() {
    let names: Vec<&str> = goap_def::builtin_domains()
        .iter()
        .map(|d| d.name.as_str())
        .collect();
    assert_eq!(names, ["methodology", "organisation", "platform"]);
    assert!(goap_def::is_builtin_domain("organisation") && !goap_def::is_builtin_domain("alm"));
}
