//! guardians_test.go, over the graph in memory of the tests.

use std::collections::BTreeMap;
use std::sync::Arc;

use goap_domain::{Blackboard, Change, ChangeID};
use goap_methodology::{
    FROM_INTEGRATED, Guardian, Integration, Methodology, Process, Step, Subscription,
};

use super::{FakeGraph, bg};
use crate::graphstore::GraphStore;
use crate::service::{Service, applicable_fn};
use crate::store::{Record, STATUS_PUBLISHED, Store};

fn steps(name: &str) -> Vec<Process> {
    vec![Process {
        name: name.into(),
        steps: vec![Step {
            name: "do".into(),
            ..Step::default()
        }],
        ..Process::default()
    }]
}

fn meth(name: &str, version: &str) -> Methodology {
    Methodology {
        name: name.into(),
        version: version.into(),
        namespace: "alm".into(),
        processes: steps(name),
        ..Methodology::default()
    }
}

fn change(id: &str, methodology: &str, state: &str) -> Change {
    Change {
        id: ChangeID::new(id),
        methodology: methodology.into(),
        state: state.into(),
        ..Change::default()
    }
}

// The guardian chain (ADR 0106): a methodology integrating another frames its changes, its guardian checked after
// theirs, with `integrator` the nearest ancestor change of its methodology.
#[tokio::test]
async fn guardian_chain() {
    let ctx = bg();
    let store = Arc::new(GraphStore::new(FakeGraph::new()));
    let mut reg = Service::new(store.clone());
    reg.applicable = Some(applicable_fn(|_, _| async {
        Ok(vec!["portfolio".into(), "release".into(), "work".into()])
    }));
    let save = |m: Methodology| {
        let store = store.clone();
        let ctx = ctx.clone();
        async move {
            store
                .save(
                    &ctx,
                    Record {
                        methodology: m,
                        status: STATUS_PUBLISHED,
                        ..Record::default()
                    },
                )
                .await
                .expect("save");
        }
    };
    save(meth("work", "1")).await;
    let mut release = meth("release", "2");
    release.integrates = vec![Integration {
        methodologies: vec!["work".into()],
        exclusive: true,
        ..Integration::default()
    }];
    release.guardian = Some(Guardian {
        merge: r#"integrator.state == "integrating""#.into(),
        transitions: BTreeMap::from([(
            "ready".to_string(),
            r#"!integrator.attached || integrator.state != "frozen""#.to_string(),
        )]),
    });
    save(release).await;
    let mut portfolio = meth("portfolio", "1");
    portfolio.integrates = vec![Integration {
        methodologies: vec!["release".into()],
        ..Integration::default()
    }];
    save(portfolio).await;

    let chain = reg
        .guardians(&ctx, "work", "PROJ-ROOT")
        .await
        .expect("chain");
    let names: Vec<String> = chain
        .iter()
        .map(|r| format!("{}@{}", r.methodology, r.version))
        .collect();
    assert_eq!(
        names,
        vec!["portfolio@1", "release@2", "work@1"],
        "the chain follows integrates upwards, outermost first"
    );
    assert!(
        reg.integrates(&ctx, "release", "work")
            .await
            .expect("integrates")
    );
    assert!(
        !reg.integrates(&ctx, "portfolio", "work")
            .await
            .expect("integrates")
    );

    let mut c = change("c1", "work", "");
    c.guardians = chain;
    let bb = Blackboard {
        change: c,
        ..Blackboard::default()
    };
    let refusal = reg
        .check_guardians(&ctx, &bb, "", &[])
        .await
        .expect("check");
    assert!(
        refusal.starts_with("release requires integrator.attached"),
        "an exclusive integrator refuses an unattached change: {refusal:?}"
    );
    let mut rel = change("r1", "release", "screening");
    let refusal = reg
        .check_guardians(&ctx, &bb, "", std::slice::from_ref(&rel))
        .await
        .expect("check");
    assert!(
        refusal.contains(r#"integrator.state == "integrating""#),
        "the merge waits for the release to integrate: {refusal:?}"
    );
    rel.state = "integrating".into();
    let refusal = reg
        .check_guardians(&ctx, &bb, "", std::slice::from_ref(&rel))
        .await
        .expect("check");
    assert_eq!(refusal, "", "attached to an integrating release, it merges");
    // a transition guard by name; other transitions are free
    rel.state = "frozen".into();
    let refusal = reg
        .check_guardians(&ctx, &bb, "ready", std::slice::from_ref(&rel))
        .await
        .expect("check");
    assert!(
        !refusal.is_empty(),
        "a frozen release holds the transition ready"
    );
    let refusal = reg
        .check_guardians(&ctx, &bb, "start", std::slice::from_ref(&rel))
        .await
        .expect("check");
    assert_eq!(refusal, "", "no guard for start");
    let refusal = reg
        .check_guardians(&ctx, &bb, "ready", &[])
        .await
        .expect("check");
    assert_eq!(refusal, "", "unattached, ready is free");

    // an integration cycle is reported on the methodology that closes it
    let mut cyc = meth("work", "2");
    cyc.integrates = vec![Integration {
        methodologies: vec!["portfolio".into()],
        ..Integration::default()
    }];
    let issues = reg.integration_issues(&ctx, &cyc).await;
    assert!(
        issues.0.len() == 1
            && issues.0[0]
                .message
                .contains("work integrates portfolio integrates release integrates work"),
        "cycle: {issues}"
    );
    let mut nocyc = Methodology {
        name: "release".into(),
        version: "3".into(),
        ..Methodology::default()
    };
    nocyc.integrates = vec![Integration {
        methodologies: vec!["work".into()],
        ..Integration::default()
    }];
    let issues = reg.integration_issues(&ctx, &nocyc).await;
    assert!(issues.0.is_empty(), "no cycle: {issues}");

    // the composition: release follows a move of the work changes, which have no lifecycle; its guardian names a
    // transition they do not have
    let mut rel3 = meth("release", "3");
    rel3.integrates = vec![Integration {
        methodologies: vec!["work".into()],
        ..Integration::default()
    }];
    rel3.guardian = Some(Guardian {
        transitions: BTreeMap::from([("ready".to_string(), "integrator.attached".to_string())]),
        ..Guardian::default()
    });
    rel3.on = vec![Subscription {
        event: "change.transitioned".into(),
        from: FROM_INTEGRATED.into(),
        ..Subscription::default()
    }];
    let mut got = Vec::new();
    for i in reg.composition_issues(&ctx, &rel3).await.0 {
        assert!(
            i.is_warning() && !i.hint.is_empty(),
            "a composition issue is a warning with a hint: {i:?}"
        );
        got.push(i.path);
    }
    assert_eq!(
        got,
        vec!["on[0].event", "guardian.transitions.ready"],
        "composition"
    );
}
