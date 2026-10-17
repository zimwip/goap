use super::load;

// The risk management methodology is transverse (ADR 0036 §3): no namespace, it applies to sdlc.
#[test]
fn risk_management_methodology() {
    let m = load("../../methodologies/risk-management.yaml");
    let issues = m.validate_stored();
    assert!(issues.0.is_empty(), "{issues}");
    let c = m.compile().unwrap();
    let g = c.goal("risk_management").expect("goal");
    assert!(
        g.pre.get("risks_reviewed") == Some(&true)
            && g.pre.get("risks_under_control") == Some(&true)
            && m.applies_to == vec!["sdlc"],
        "goal {g:?}, appliesTo {:?}",
        m.applies_to
    );
}
