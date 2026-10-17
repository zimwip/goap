//! builtins_test.go.

use goap_methodology::{Action, KIND_BUILTIN};

use super::{as_, example, memory, registry, with_alm};

#[tokio::test]
async fn unknown_builtin_refused() {
    let (s, d) = registry(memory(), true, |_| {});
    with_alm(&d).await;
    let mut m = example();
    m.version = "3.0.0".into();
    let mut a = Action::default();
    a.name = "typo".into();
    a.kind = KIND_BUILTIN.into();
    a.builtin = "nosuch".into();
    a.effects.insert(m.conditions[0].name.clone(), true);
    m.actions.push(a);
    let (_, issues) = s.save(&as_(&["admin"]), m.clone()).await.expect("save");
    assert!(
        issues
            .0
            .iter()
            .any(|i| i.path.ends_with(".builtin") && i.message.contains("nosuch")),
        "unknown builtin not reported: {issues}"
    );
    let err = s
        .publish_version(&as_(&["admin"]), &m.name, &m.version)
        .await
        .err();
    assert!(
        err.as_ref().is_some_and(|e| e.is_invalid()),
        "published: {err:?}"
    );
}
