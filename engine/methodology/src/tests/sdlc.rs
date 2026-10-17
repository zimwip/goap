use super::load;

#[test]
fn sdlc_methodology_is_valid() {
    let m = load("../../methodologies/sdlc.yaml");
    let cm = m.compile().unwrap();
    assert_eq!(cm.specializations_of("sdlc", "build").len(), 4);
    let st = &m.supertypes()["alm@SecurityRequirement"];
    assert!(
        st.len() == 2 && st[1] == "alm@Requirement",
        "supertypes: {st:?}"
    );
}
