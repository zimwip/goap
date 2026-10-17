use std::collections::BTreeMap;
use std::sync::Arc;

use goap_def::{Issue, Issues};
use goap_domain::{Lifecycle, LifecycleState, Transition};

use super::load;
use crate::composition::literal_refs;
use crate::{
    Composition, Event, FROM_DEPENDENCY, FROM_INTEGRATED, FROM_INTEGRATOR, Guardian, Integration,
    Methodology, Process, Step, Subscription,
};

fn lifecycle(
    name: &str,
    initial: &str,
    states: &[(&str, bool)],
    ts: &[(&str, &str, &str)],
) -> Lifecycle {
    Lifecycle {
        name: name.into(),
        initial: initial.into(),
        states: states
            .iter()
            .map(|(n, f)| LifecycleState {
                name: n.to_string(),
                r#final: *f,
                ..LifecycleState::default()
            })
            .collect(),
        transitions: ts
            .iter()
            .map(|(n, f, t)| Transition {
                name: n.to_string(),
                from: f.to_string(),
                to: t.to_string(),
                ..Transition::default()
            })
            .collect(),
        ..Lifecycle::default()
    }
}

// a release (planning → integrating → delivered) and a piece of work (open → review → done).
fn release_lc() -> Lifecycle {
    lifecycle(
        "delivery",
        "planning",
        &[
            ("planning", false),
            ("integrating", false),
            ("delivered", true),
        ],
        &[
            ("integrate", "planning", "integrating"),
            ("deliver", "integrating", "delivered"),
        ],
    )
}

fn work_lc() -> Lifecycle {
    lifecycle(
        "work",
        "open",
        &[("open", false), ("review", false), ("done", true)],
        &[("submit", "open", "review"), ("approve", "review", "done")],
    )
}

fn by_lifecycle(m: &Methodology) -> Option<Lifecycle> {
    match m.lifecycle.as_str() {
        "delivery" => Some(release_lc()),
        "work" => Some(work_lc()),
        _ => None,
    }
}

fn composition(others: &[&Methodology]) -> Composition {
    let others: BTreeMap<String, Methodology> = others
        .iter()
        .map(|o| (o.name.clone(), (*o).clone()))
        .collect();
    Composition {
        others,
        lifecycle: Some(Arc::new(by_lifecycle)),
    }
}

fn step(name: &str) -> Step {
    Step {
        name: name.into(),
        ..Step::default()
    }
}

fn work(lifecycle: &str) -> Methodology {
    Methodology {
        name: "work".into(),
        lifecycle: lifecycle.into(),
        processes: vec![Process {
            name: "build".into(),
            steps: vec![step("code"), step("test")],
            ..Process::default()
        }],
        ..Methodology::default()
    }
}

fn release(mutate: impl FnOnce(&mut Methodology)) -> Methodology {
    let mut m = Methodology {
        name: "release".into(),
        lifecycle: "delivery".into(),
        processes: vec![Process {
            name: "release".into(),
            steps: vec![step("plan")],
            ..Process::default()
        }],
        integrates: vec![Integration {
            methodologies: vec!["work".into()],
            exclusive: true,
            ..Integration::default()
        }],
        guardian: Some(Guardian {
            merge: r#"integrator.state == "integrating""#.into(),
            ..Guardian::default()
        }),
        ..Methodology::default()
    };
    mutate(&mut m);
    m
}

fn sub(event: &str, from: &str, filter: &str) -> Subscription {
    Subscription {
        event: event.into(),
        from: from.into(),
        filter: filter.into(),
        ..Subscription::default()
    }
}

/// issueAt is the issue at path whose message holds every part.
fn issue_at<'a>(is: &'a Issues, path: &str, parts: &[&str]) -> Option<&'a Issue> {
    is.0.iter().find(|x| {
        x.path == path
            && parts
                .iter()
                .all(|p| format!("{} | {}", x.message, x.hint).contains(p))
    })
}

#[test]
fn composition_sound() {
    let r = release(|m| {
        m.on = vec![
            sub(
                "change.transitioned",
                FROM_INTEGRATED,
                r#"event.change.state == "review""#,
            ),
            sub(
                "step.completed",
                FROM_INTEGRATED,
                r#"event.step.name in ["code", "test"]"#,
            ),
        ];
        m.guardian.as_mut().unwrap().transitions = [(
            "approve".to_string(),
            r#"!integrator.attached || integrator.state != "delivered""#.to_string(),
        )]
        .into();
    });
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(is.0.is_empty(), "a sound composition: {is}");
    let is = work("work").composition_issues(&composition(&[&r]));
    assert!(is.0.is_empty(), "seen from the integrated one: {is}");
}

#[test]
fn composition_missing_events() {
    let r = release(|m| m.on = vec![sub("change.transitioned", FROM_INTEGRATED, "")]);
    let is = r.composition_issues(&composition(&[&work("")]));
    let i = issue_at(
        &is,
        "on[0].event",
        &[
            "never comes from",
            "work names no lifecycle",
            "give work a lifecycle",
        ],
    );
    assert!(
        i.is_some_and(|i| i.is_warning()),
        "change.transitioned of a methodology without lifecycle: {is}"
    );
    // reported on the integrated one too, as what this version breaks
    let is = work("").composition_issues(&composition(&[&r]));
    assert!(
        issue_at(&is, "lifecycle", &["release follows change.transitioned"]).is_some(),
        "on the source: {is}"
    );

    // from: integrator, while nothing integrates the methodology
    let mut w = work("work");
    w.on = vec![sub("change.transitioned", FROM_INTEGRATOR, "")];
    let is = w.composition_issues(&composition(&[]));
    assert!(
        issue_at(&is, "on[0].from", &["no methodology integrates work"]).is_some(),
        "from integrator: {is}"
    );
    let is = w.composition_issues(&composition(&[&release(|_| {})]));
    assert!(is.0.is_empty(), "integrated by release: {is}");

    // a filter naming a state, a step that do not exist
    let r = release(|m| {
        m.on = vec![
            sub(
                "change.transitioned",
                FROM_INTEGRATED,
                r#"event.change.state == "reveiw""#,
            ),
            sub(
                "step.completed",
                FROM_INTEGRATED,
                r#""deploy" == event.step.name"#,
            ),
        ];
    });
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(
        issue_at(
            &is,
            "on[0].filter",
            &[r#""reveiw""#, r#"did you mean "review""#]
        )
        .is_some()
            && issue_at(&is, "on[1].filter", &[r#""deploy""#, "steps: code, test"]).is_some(),
        "filters: {is}"
    );

    // an integration naming an unknown methodology
    let r = release(|m| m.integrates[0].methodologies = vec!["wrok".into()]);
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(
        issue_at(
            &is,
            "integrates[0].methodologies[0]",
            &[r#"did you mean "work""#]
        )
        .is_some(),
        "unknown integrated: {is}"
    );
}

fn transitions(list: &[(&str, &str)]) -> BTreeMap<String, String> {
    list.iter()
        .map(|(k, v)| (k.to_string(), v.to_string()))
        .collect()
}

#[test]
fn composition_guardian() {
    // a state the integrator's lifecycle does not have; a transition the integrated lifecycle does not have
    let r = release(|m| {
        let g = m.guardian.as_mut().unwrap();
        g.merge = r#"integrator.state == "integrate""#.into();
        g.transitions = transitions(&[("aprove", "integrator.attached")]);
    });
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(
        issue_at(
            &is,
            "guardian.merge",
            &[
                r#"integrator.state "integrate""#,
                r#"did you mean "integrating""#
            ]
        )
        .is_some(),
        "unknown integrator state: {is}"
    );
    assert!(
        issue_at(
            &is,
            "guardian.transitions.aprove",
            &["no transition aprove", r#"did you mean "approve""#]
        )
        .is_some(),
        "unknown transition: {is}"
    );
    assert!(
        issue_at(&is, "guardian.merge", &["never holds"]).is_some(),
        "a merge guard on a state that does not exist never holds: {is}"
    );

    // an exclusive integration contradicted by its merge guard
    let r = release(|m| m.guardian.as_mut().unwrap().merge = "!integrator.attached".into());
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(
        issue_at(
            &is,
            "guardian.merge",
            &["never holds", "exclusive integration"]
        )
        .is_some(),
        "exclusive and unattached: {is}"
    );

    // a transition guard on a state the change cannot be in when it takes the transition
    let r = release(|m| {
        m.guardian.as_mut().unwrap().transitions =
            transitions(&[("approve", r#"change.state == "open""#)])
    });
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(
        issue_at(
            &is,
            "guardian.transitions.approve",
            &["never holds", "approve leaves review"]
        )
        .is_some(),
        "the from state: {is}"
    );

    // a world condition the integrated methodology does not hold
    let r = release(|m| {
        m.guardian.as_mut().unwrap().merge =
            r#"integrator.state == "integrating" && world.reviewed"#.into()
    });
    let is = r.composition_issues(&composition(&[&work("work")]));
    assert!(
        issue_at(&is, "guardian.merge", &["world.reviewed"]).is_some(),
        "unknown condition: {is}"
    );
}

// Two guardians of a chain, each satisfiable alone, that never hold together on the same change.
#[test]
fn composition_composite_guardian() {
    let r = release(|m| {
        m.guardian.as_mut().unwrap().transitions =
            transitions(&[("approve", r#"change.state == "review""#)])
    });
    let mut portfolio = Methodology {
        name: "portfolio".into(),
        lifecycle: "delivery".into(),
        processes: vec![Process {
            name: "portfolio".into(),
            steps: vec![step("steer")],
            ..Process::default()
        }],
        integrates: vec![Integration {
            methodologies: vec!["release".into()],
            ..Integration::default()
        }],
        guardian: Some(Guardian {
            transitions: transitions(&[("approve", r#"change.state == "open""#)]),
            ..Guardian::default()
        }),
        ..Methodology::default()
    };
    // portfolio frames the work changes through release: its guard on approve (which leaves review) never holds
    let is = portfolio.composition_issues(&composition(&[&r, &work("work")]));
    assert!(
        issue_at(
            &is,
            "guardian.transitions.approve",
            &["never holds", "work"]
        )
        .is_some(),
        "portfolio's own guard: {is}"
    );

    // approve leaves open or review: each guard holds alone, never together
    let mut wlc = work_lc();
    wlc.transitions.push(Transition {
        name: "approve".into(),
        from: "open".into(),
        to: "done".into(),
        ..Transition::default()
    });
    let with = |others: &[&Methodology]| {
        let mut c = composition(others);
        let wlc = wlc.clone();
        c.lifecycle = Some(Arc::new(move |m: &Methodology| {
            if m.lifecycle == "work" {
                Some(wlc.clone())
            } else {
                by_lifecycle(m)
            }
        }));
        c
    };
    let conflict = "never hold together on the transition approve of a change of work";
    let is = r.composition_issues(&with(&[&portfolio, &work("work")]));
    assert!(
        issue_at(
            &is,
            "guardian.transitions.approve",
            &[conflict, r#"portfolio requires change.state == "open""#]
        )
        .is_some(),
        "on release: {is}"
    );
    let is = portfolio.composition_issues(&with(&[&r, &work("work")]));
    assert!(
        issue_at(&is, "guardian.transitions.approve", &[conflict]).is_some(),
        "on portfolio: {is}"
    );
    let is = work("work").composition_issues(&with(&[&r, &portfolio]));
    assert!(
        issue_at(&is, "", &[conflict]).is_some(),
        "on work, the guardians above it: {is}"
    );
    // once aligned, nothing
    portfolio.guardian.as_mut().unwrap().transitions.insert(
        "approve".into(),
        r#"change.state == "review" || !integrator.attached"#.into(),
    );
    let is = r.composition_issues(&with(&[&portfolio, &work("work")]));
    assert!(is.0.is_empty(), "aligned: {is}");
}

#[test]
fn test_literal_refs() {
    let refs = literal_refs(
        r#"integrator.state == "a" && "b" != change.state && event.step.name in ["x", 'y'] && world.ok && world["k"]"#,
    );
    for (sel, want) in [
        ("integrator.state", "a"),
        ("change.state", "b"),
        ("event.step.name", "x,y"),
        ("world", "ok,k"),
    ] {
        assert_eq!(
            refs.get(sel).map(|v| v.join(",")).unwrap_or_default(),
            want,
            "{sel}"
        );
    }
    let refs = literal_refs(r#"event.change.state == "s""#);
    assert!(
        !refs.contains_key("change.state") && refs["event.change.state"][0] == "s",
        "event.change is not change: {refs:?}"
    );
}

// The methodologies of the repository, against the domain of domains/: their composition holds, but for the
// lifecycle sdlc does not have yet (release-based follows its change.transitioned).
#[test]
fn composition_of_the_repository() {
    let ds = goap_def::load_domains("../../domains").unwrap();
    let mut others = BTreeMap::new();
    for f in [
        "sdlc",
        "release-based",
        "risk-management",
        "methodology-improvement",
    ] {
        let m = load(&format!("../../methodologies/{f}.yaml"));
        others.insert(m.name.clone(), m);
    }
    let c = Composition {
        others: others.clone(),
        lifecycle: Some(Arc::new(move |m: &Methodology| {
            ds.iter().find_map(|d| d.lifecycle(&m.lifecycle).cloned())
        })),
    };
    let mut got = Vec::new();
    for (n, m) in &others {
        for i in m.composition_issues(&c).0 {
            got.push(format!("{n}: {}", i.path));
        }
    }
    assert_eq!(got, vec!["release-based: on[1].event", "sdlc: lifecycle"]);
}

// the events of the changes a change depends on (ADR 0108) come from changes of any methodology: nothing to check.
#[test]
fn composition_from_dependency() {
    let mut m = work("work");
    m.on = vec![
        sub("change.applied", FROM_DEPENDENCY, ""),
        sub("change.transitioned", FROM_DEPENDENCY, ""),
    ];
    let is = m.composition_issues(&composition(&[&release(|_| {})]));
    assert!(is.0.is_empty(), "from: dependency is not checked: {is}");
    let is = release(|_| {}).composition_issues(&composition(&[&m]));
    assert!(is.0.is_empty(), "no consumer of the release: {is}");
}

// an event of a methodology (ADR 0110) followed from another one must be declared by the source.
#[test]
fn composition_declared_events() {
    let follow = |at: &dyn Fn(&mut Methodology)| {
        release(|m| {
            m.events = vec![Event {
                name: "work.tested".into(),
                ..Event::default()
            }];
            at(m);
        })
    };
    let via_on = follow(&|m| m.on = vec![sub("work.tested", FROM_INTEGRATED, "")]);
    let via_step =
        follow(&|m| m.processes[0].steps[0].on = vec![sub("work.tested", FROM_INTEGRATED, "")]);
    for (path, m) in [
        ("on[0].event", via_on),
        ("processes[0].steps[0].on[0].event", via_step),
    ] {
        let is = m.composition_issues(&composition(&[&work("work")]));
        assert!(
            issue_at(&is, path, &["work declares no event work.tested"]).is_some(),
            "{path}: the source declares no such event"
        );
        let mut declared = work("work");
        declared.events = vec![Event {
            name: "work.tested".into(),
            ..Event::default()
        }];
        let is = m.composition_issues(&composition(&[&declared]));
        assert!(
            issue_at(&is, path, &[]).is_none(),
            "{path}: declared by the source, got {is}"
        );
    }
}
