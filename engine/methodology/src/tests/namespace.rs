use super::load;

#[test]
fn methodology_improvement_targets_the_methodology_namespace() {
    let m = load("../../methodologies/methodology-improvement.yaml");
    assert_eq!(m.namespace, "methodology");
    let issues = m.validate_stored();
    assert!(issues.0.is_empty(), "{issues}");
}

#[test]
fn repository_methodologies_are_storable() {
    for f in [
        "../../methodologies/sdlc.yaml",
        "../../methodologies/examples/impact-analysis.yaml",
        "../../methodologies/examples/test-design.yaml",
    ] {
        let m = load(f);
        assert_eq!(m.namespace, "alm", "{f}");
        let issues = m.validate_stored();
        assert!(issues.0.is_empty(), "{f}: {issues}");
    }
}
