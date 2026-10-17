//! scope_test.go and helpers_test.go: the scope port and the permissions of the engine.

use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::{Authorizer, Casbin, Principal, Request, Resource};
use goap_ctx::Ctx;
use goap_domain::ChangeID;
use goap_methodology::Responsibilities;
use parking_lot::Mutex;

use super::fakes::engine;
use crate::engine::Engine;
use crate::process::Process;
use crate::scope::AuthzScope;
use crate::steps::StepContext;

/// roles.yaml declares who may run its agents and actions with its own roles (ADR 0043).
const ROLES_YAML: &str = include_str!("../../testdata/roles.yaml");

/// Recorder is an authorizer that remembers the requests it was asked and answers a fixed verdict.
#[derive(Default)]
struct Recorder {
    verdict: Mutex<(bool, Option<String>)>,
    reqs: Mutex<Vec<Request>>,
}

impl Recorder {
    fn new(ok: bool) -> Arc<Recorder> {
        let r = Recorder::default();
        r.verdict.lock().0 = ok;
        Arc::new(r)
    }
}

#[async_trait]
impl Authorizer for Recorder {
    async fn authorize(&self, _: &Ctx, req: &Request) -> Result<bool, goap_authz::Error> {
        self.reqs.lock().push(req.clone());
        let (ok, err) = self.verdict.lock().clone();
        match err {
            Some(e) => Err(goap_authz::Error::Message(e)),
            None => Ok(ok),
        }
    }
}

fn principal(subject: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: subject.into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    }
}

fn scope_process() -> Process {
    Process {
        id: "p1".into(),
        methodology: "m".into(),
        change_id: ChangeID::new("c1"),
        org: "acme".into(),
        project: "PROJ-X".into(),
        initiator: Principal {
            org: "acme-initiator".into(),
            ..principal("alice", &["developer"])
        },
        ..Default::default()
    }
}

fn scope_engine(a: Option<Arc<dyn Authorizer>>) -> Engine {
    let mut e = engine();
    e.scope = Some(Arc::new(AuthzScope {
        authz: a,
        hub: None,
    }));
    e
}

fn must_casbin() -> Option<Arc<dyn Authorizer>> {
    Some(Arc::new(Casbin::new().unwrap()))
}

fn bg() -> Ctx {
    Ctx::background()
}

#[test]
fn scope_placement_of_a_process() {
    let p = Process {
        id: "p".into(),
        initiator: Principal {
            project: "OTHER".into(),
            ..principal("alice", &["dev"])
        },
        ..Default::default()
    };
    let r = scope_engine(None).r#ref(&p);
    assert!(
        r.org.is_empty() && r.project.is_empty(),
        "an unplaced process names no organisation and project: {r:?}"
    );
    let a = scope_engine(None).r#ref(&scope_process()).actor();
    assert!(
        a.subject == "alice" && a.project == "PROJ-X" && a.roles.len() == 1,
        "a process acts as its initiator on its project: {a:?}"
    );
}

#[tokio::test]
async fn allowed_builds_the_resource_of_the_process() {
    let rec = Recorder::new(true);
    let (e, p) = (scope_engine(Some(rec.clone())), scope_process());
    let who = principal("bob", &[]);
    assert!(e.allowed(&bg(), &p, &who, "process:start").await.unwrap());
    let got = rec.reqs.lock()[0].clone();
    let want = Resource {
        r#type: "process".into(),
        id: "p1".into(),
        org: "acme-initiator".into(),
        owner: "alice".into(),
        name: "m".into(),
        project_id: "PROJ-X".into(),
        ..Default::default()
    };
    assert!(
        got.subject.subject == "bob" && got.action == "start" && got.resource == want,
        "request: {got:?}"
    );
    // a change permission names the change, not the process
    e.allowed(&bg(), &p, &who, "change:apply").await.unwrap();
    let r = rec.reqs.lock()[1].resource.clone();
    assert!(
        r.r#type == "change" && r.id == "c1",
        "change resource: {r:?}"
    );
    // a malformed permission is an error
    assert!(
        e.allowed(&bg(), &p, &who, "nocolon").await.is_err(),
        "a malformed permission must fail"
    );
    // the verdict and the error of the authorizer are passed on
    *rec.verdict.lock() = (false, Some("boom".into()));
    let res = e.allowed(&bg(), &p, &who, "process:start").await;
    assert!(res.is_err(), "authorizer error: {res:?}");
}

#[tokio::test]
async fn allowed_with_casbin() {
    let (e, p) = (scope_engine(must_casbin()), scope_process());
    let member = principal("bob", &["developer"]);
    let cases = [
        (
            "a member works on the processes of the project",
            member.clone(),
            "process:start",
            true,
        ),
        (
            "a holder of no role does not",
            principal("eve", &[]),
            "process:start",
            false,
        ),
        (
            "an administrator may do anything",
            principal("root", &["admin"]),
            "release:deploy",
            true,
        ),
        (
            "a member holds no release permission",
            member,
            "release:deploy",
            false,
        ),
        (
            "a release manager deploys",
            principal("rita", &["release_manager"]),
            "release:deploy",
            true,
        ),
        (
            "never on their own change",
            principal("alice", &["release_manager"]),
            "release:deploy",
            false,
        ),
    ];
    for (name, who, perm, want) in cases {
        let got = e.allowed(&bg(), &p, &who, perm).await.unwrap();
        assert_eq!(got, want, "{name}");
    }
}

#[tokio::test]
async fn nil_authz_grants_everything() {
    let (e, p) = (scope_engine(None), scope_process());
    let nobody = Principal::default();
    assert!(e.allowed(&bg(), &p, &nobody, "nocolon").await.unwrap());
    assert!(
        e.may_run(&bg(), &p, &nobody, "action", "a", &["x".to_string()])
            .await
            .unwrap()
    );
    let sc = StepContext {
        path: "s".into(),
        roles: Some(Responsibilities {
            responsible: "r".into(),
            accountable: "a".into(),
            ..Default::default()
        }),
        ..Default::default()
    };
    assert!(
        e.step_allowed(&bg(), &p, &nobody, Some(&sc), "perform")
            .await
            .unwrap()
    );
    assert!(e.may_unblock(&bg(), &p, &nobody).await.unwrap());
}

#[tokio::test]
async fn may_run_by_roles() {
    let (e, p) = (scope_engine(must_casbin()), scope_process());
    let dev = principal("bob", &["developer"]);
    let rm = principal("rita", &["release_manager"]);
    let roles = |v: &[&str]| v.iter().map(|r| r.to_string()).collect::<Vec<_>>();
    let cases = [
        (
            "one of the roles",
            rm,
            roles(&["developer", "release_manager"]),
            true,
        ),
        (
            "none of the roles",
            dev.clone(),
            roles(&["release_manager"]),
            false,
        ),
        (
            "no role required: any member",
            dev.clone(),
            Vec::new(),
            true,
        ),
        (
            "no role required: not a stranger",
            principal("eve", &[]),
            Vec::new(),
            false,
        ),
    ];
    for (name, who, rs, want) in cases {
        let got = e
            .may_run(&bg(), &p, &who, "action", "release", &rs)
            .await
            .unwrap();
        assert_eq!(got, want, "{name}");
    }
    // the request names the process, its project and organisation
    let rec = Recorder::new(true);
    scope_engine(Some(rec.clone()))
        .may_run(&bg(), &p, &dev, "agent", "shipper", &roles(&["developer"]))
        .await
        .unwrap();
    let got = rec.reqs.lock()[0].clone();
    let want = Resource {
        r#type: "agent".into(),
        id: "p1".into(),
        name: "shipper".into(),
        org: "acme".into(),
        project_id: "PROJ-X".into(),
        owner: "alice".into(),
        roles: roles(&["developer"]),
        ..Default::default()
    };
    assert!(
        got.action == "run" && got.resource == want,
        "request: {got:?}"
    );
}

#[test]
fn run_roles_are_the_actions_then_the_agents() {
    let c = goap_methodology::parse(ROLES_YAML.as_bytes())
        .unwrap()
        .compile()
        .unwrap();
    let e = scope_engine(None);
    let act = |name: &str| {
        c.action(name)
            .cloned()
            .unwrap_or_else(|| panic!("no action {name}"))
    };
    let shipper = Process {
        agent: "shipper".into(),
        ..Default::default()
    };
    let r = e.run_roles(&c, &shipper, &act("release"));
    assert_eq!(r, ["release_manager"], "an action's own roles win");
    assert_eq!(
        e.run_roles(&c, &shipper, &act("build")).len(),
        2,
        "else those of the agent"
    );
    let nobody = Process {
        agent: "nobody".into(),
        ..Default::default()
    };
    assert!(
        e.run_roles(&c, &nobody, &act("build")).is_empty(),
        "an unknown agent gives none"
    );
}

#[tokio::test]
async fn step_allowed() {
    let p = scope_process();
    let sc = StepContext {
        path: "delivery/build".into(),
        roles: Some(Responsibilities {
            responsible: "developer".into(),
            accountable: "release_manager".into(),
            ..Default::default()
        }),
        ..Default::default()
    };
    let dev = principal("bob", &["developer"]);
    let rm = principal("rita", &["release_manager"]);

    // no step, no roles, or no role for the act: left to the permissions of the process, the authorizer is not asked
    let deny = Recorder::new(false);
    let e = scope_engine(Some(deny.clone()));
    let no_roles = StepContext {
        path: "s".into(),
        ..Default::default()
    };
    let no_accountable = StepContext {
        path: "s".into(),
        roles: Some(Responsibilities {
            responsible: "developer".into(),
            ..Default::default()
        }),
        ..Default::default()
    };
    for (name, c, act) in [
        ("no step", None, "perform"),
        ("no responsibilities", Some(&no_roles), "perform"),
        ("no accountable", Some(&no_accountable), "approve"),
    ] {
        assert!(
            e.step_allowed(&bg(), &p, &dev, c, act).await.unwrap(),
            "{name}"
        );
    }
    assert!(
        deny.reqs.lock().is_empty(),
        "the authorizer must not be asked"
    );

    let e = scope_engine(must_casbin());
    let cases = [
        (
            "the responsible role performs",
            dev.clone(),
            "perform",
            true,
        ),
        ("another role does not", rm.clone(), "perform", false),
        ("the accountable role approves", rm, "approve", true),
        (
            "the responsible role does not approve",
            dev.clone(),
            "approve",
            false,
        ),
        (
            "never their own work",
            principal("alice", &["release_manager"]),
            "approve",
            false,
        ),
    ];
    for (name, who, act, want) in cases {
        assert_eq!(
            e.step_allowed(&bg(), &p, &who, Some(&sc), act)
                .await
                .unwrap(),
            want,
            "{name}"
        );
    }

    // the request names the step
    let rec = Recorder::new(true);
    scope_engine(Some(rec.clone()))
        .step_allowed(&bg(), &p, &dev, Some(&sc), "approve")
        .await
        .unwrap();
    let want = Resource {
        r#type: "step".into(),
        id: "p1".into(),
        name: "delivery/build".into(),
        org: "acme".into(),
        project_id: "PROJ-X".into(),
        owner: "alice".into(),
        role: "developer".into(),
        accountable: "release_manager".into(),
        ..Default::default()
    };
    let got = rec.reqs.lock()[0].clone();
    assert!(
        got.action == "approve" && got.resource == want,
        "request: {got:?}"
    );
}

#[tokio::test]
async fn may_unblock() {
    let e = scope_engine(Some(Recorder::new(false))); // denies everything: only the initiator rule can grant
    let proc = |id: &str, parent: &str, initiator: &str| Process {
        id: id.into(),
        parent_id: parent.into(),
        initiator: principal(initiator, &[]),
        ..Default::default()
    };
    let alice = principal("alice", &[]);
    let stranger = principal("eve", &[]);

    let p = proc("p", "", "alice");
    assert!(
        e.may_unblock(&bg(), &p, &alice).await.unwrap(),
        "the initiator answers for the run"
    );
    assert!(
        !e.may_unblock(&bg(), &p, &Principal::default())
            .await
            .unwrap_or(false),
        "anonymous never matches"
    );
    assert!(
        !e.may_unblock(&bg(), &p, &stranger).await.unwrap_or(false),
        "a stranger does not answer for the run"
    );

    // the initiator of a run above it, within 16 runs: chain builds n runs, each the child of the next, the last
    // initiated by alice
    async fn chain(e: &Engine, prefix: &str, n: usize) -> Process {
        for i in 0..n {
            let (parent, who) = if i == n - 1 {
                (String::new(), "alice")
            } else {
                (format!("{prefix}{}", i + 1), "other")
            };
            let p = Process {
                id: format!("{prefix}{i}"),
                parent_id: parent,
                initiator: Principal {
                    subject: who.into(),
                    ..Default::default()
                },
                ..Default::default()
            };
            e.store.put(&Ctx::background(), &p).await.unwrap();
        }
        e.store
            .get(&Ctx::background(), &format!("{prefix}0"))
            .await
            .unwrap()
    }
    let a = chain(&e, "a", 16).await;
    assert!(
        e.may_unblock(&bg(), &a, &alice).await.unwrap(),
        "an owner 16 runs up"
    );
    let b = chain(&e, "b", 17).await;
    assert!(
        !e.may_unblock(&bg(), &b, &alice).await.unwrap_or(false),
        "the walk up stops after 16 runs"
    );
    // a parent that cannot be read ends the walk without an error
    let x = proc("x", "gone", "other");
    assert!(
        !e.may_unblock(&bg(), &x, &alice).await.unwrap(),
        "a missing parent"
    );

    // the accountable role of the step the run carries out
    let e = scope_engine(must_casbin());
    let step = StepContext {
        path: "s".into(),
        roles: Some(Responsibilities {
            responsible: "developer".into(),
            accountable: "release_manager".into(),
            ..Default::default()
        }),
        ..Default::default()
    };
    let q = Process {
        id: "q".into(),
        initiator: principal("alice", &[]),
        step: Some(step),
        ..Default::default()
    };
    assert!(
        e.may_unblock(&bg(), &q, &principal("rita", &["release_manager"]))
            .await
            .unwrap(),
        "accountable"
    );
    // else the permission process:unblock, which the members of the project hold by default
    assert!(
        e.may_unblock(&bg(), &q, &principal("bob", &["developer"]))
            .await
            .unwrap(),
        "a member of the project"
    );
    assert!(
        !e.may_unblock(&bg(), &q, &stranger).await.unwrap(),
        "a stranger"
    );
}
