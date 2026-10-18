//! The Casbin ABAC model, the default policies and the enforcer (casbin-rs, whose matchers rhai evaluates).

use std::{
    future::Future,
    sync::LazyLock,
    task::{Context, Poll, Waker},
};

use ::casbin::{
    CoreApi, DefaultModel, Enforcer, MgmtApi,
    function_map::OperatorFunction,
    rhai::{Dynamic, serde::from_dynamic},
};
use async_trait::async_trait;
use goap_ctx::Ctx;
use parking_lot::RwLock;
use serde::{Deserialize, Serialize};

use crate::authz::{Authorizer, Error, Principal, Request, Resource};

/// Model is the Casbin ABAC model. A policy line is
///
/// ```text
/// p, <subject rule>, <resource type | *>, <action | *>, <allow | deny>
/// ```
///
/// where the subject rule is an expression over r.sub (Principal), r.obj
/// (Resource) and r.act, e.g.
///
/// ```text
/// hasRole(r.sub, "tech_lead") && r.sub.Subject != r.obj.Owner
/// ```
///
/// Functions: hasRole(sub, role), hasAnyRole(sub, role...) (up to five roles), hasRoleIn(sub, role, obj),
/// onProject(sub) (holds a role on the resource's project), mayRun(sub, obj) (holds one of obj.Roles, or any role
/// when it lists none), isAnonymous(sub). The attributes of r.sub and r.obj keep the Go field names (Subject, Org,
/// Project, Roles, OnBehalfOf; Type, ID, Org, Namespace, Owner, Name, Role, Accountable, OrgChain, ProjectID, Roles).
///
/// A request is allowed when at least one allow rule matches and no deny rule does.
pub const MODEL: &str = r#"
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub_rule, obj_type, act, eft

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = (p.obj_type == "*" || r.obj.Type == p.obj_type) && (p.act == "*" || r.act == p.act) && eval(p.sub_rule)
"#;

/// Policy is one ABAC rule.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(default)]
pub struct Policy {
    pub rule: String,
    pub resource: String,
    pub action: String,
    /// allow | deny
    pub effect: String,
}

impl Policy {
    /// A rule from its four fields.
    pub fn new(rule: &str, resource: &str, action: &str, effect: &str) -> Policy {
        Policy {
            rule: rule.into(),
            resource: resource.into(),
            action: action.into(),
            effect: effect.into(),
        }
    }

    fn params(&self) -> Vec<String> {
        vec![
            self.rule.clone(),
            self.resource.clone(),
            self.action.clone(),
            self.effect.clone(),
        ]
    }
}

/// DefaultPolicies seeds an empty policy store (ADR 0043). A user holds no role of their own: an administrator
/// (RoleAdmin, a platform role) may do everything; anyone else acts on a project with the roles their
/// Assignments grant there, which the authorizer merges into the principal for the resource's project. Holding
/// any role on the project (onProject) lets them work on it; what they write goes through the steps, agents and
/// actions their roles may run (step perform/approve, action run). The platform itself (methodologies, domains,
/// triggers, organisation, policies, adapters) is administered by administrators.
pub static DEFAULT_POLICIES: LazyLock<Vec<Policy>> = LazyLock::new(|| {
    let p = Policy::new;
    vec![
        p(r#"hasRole(r.sub, "admin")"#, "*", "*", "allow"),
        // a platform-wide reader (ADR 0046, granted by an Assignment naming no project) reads everything, past
        // the organization/project scoping below
        p(r#"hasRole(r.sub, "reader")"#, "*", "read", "allow"),
        // read access within the organization of the resource (multi-tenant isolation), or on a project one works on
        p(
            r#"!isAnonymous(r.sub) && (r.obj.Org == "" || r.obj.Org == r.sub.Org || onProject(r.sub))"#,
            "*",
            "read",
            "allow",
        ),
        // processes, data objects, lifecycle transitions (ADR 0014) and tools (ADR 0019) of a project: its members
        p("onProject(r.sub)", "process", "*", "allow"),
        p("onProject(r.sub)", "object", "create", "allow"),
        p("onProject(r.sub)", "node", "transition", "allow"),
        // approving a requirement (alm): the control is the accepted review of the change, which its guard requires
        // (ADR 0076); the transition itself is a member's
        p("onProject(r.sub)", "requirement", "approve", "allow"),
        p("onProject(r.sub)", "tool", "call", "allow"),
        // the lifecycle of a change (ADR 0058): its gates are decisions and conditions, the move itself is a member's
        p("onProject(r.sub)", "change", "transition", "allow"),
        // moving a change to another project (ADR 0091): asked on both projects, so a member of both
        p("onProject(r.sub)", "change", "move", "allow"),
        // the prompts and answers of the model calls of a change (its log, stream model) may hold anything the project
        // works on: its members inspect them (a graph holding stored policies grants it to administrators until a rule
        // is added)
        p("onProject(r.sub)", "prompt", "inspect", "allow"),
        // four-eyes principle: a member of the project applies its changes, never their own
        p(
            "onProject(r.sub) && r.sub.Subject != r.obj.Owner",
            "change",
            "apply",
            "allow",
        ),
        // production deployments: a release manager of the project, never on its own change
        p(
            r#"hasRole(r.sub, "release_manager") && r.sub.Subject != r.obj.Owner"#,
            "release",
            "deploy",
            "allow",
        ),
        // steps of a process (ADR 0035 §2): whoever holds the responsible role on the project carries them out;
        // whoever holds the accountable role approves them, never on its own change
        p(
            "hasRoleIn(r.sub, r.obj.Role, r.obj)",
            "step",
            "perform",
            "allow",
        ),
        p(
            "hasRoleIn(r.sub, r.obj.Accountable, r.obj) && r.sub.Subject != r.obj.Owner",
            "step",
            "approve",
            "allow",
        ),
        // agents and actions (ADR 0043): one of the roles they declare (r.obj.Roles), any member of the project
        // when they declare none
        p("mayRun(r.sub, r.obj)", "action", "run", "allow"),
        p("mayRun(r.sub, r.obj)", "agent", "run", "allow"),
        // an operation on a change (ADR 0103) its methodology restricts to some roles (r.obj.Roles): one of them, held
        // on the project of the change; an operation no role allows is an administrator's
        p("mayRun(r.sub, r.obj)", "operation", "perform", "allow"),
        // signing a derogation (ADR 0075 §2): whoever holds the role of signatory (granted on the project); an
        // administrator signs through the first rule
        p(
            r#"hasRole(r.sub, "derogation_signatory")"#,
            "derogation",
            "sign",
            "allow",
        ),
        // the level of a change asks its signatory a role (policy of the organisation, ADR 0075 §3): one of r.obj.Roles,
        // held on the project
        p("mayRun(r.sub, r.obj)", "derogation", "sign-role", "allow"),
        // lowering the criticality of a change is an administrator's unless a policy grants it (ADR 0075 §3): no default
        // rule for change:lower-criticality
        // a model of the catalog restricted to some roles (ADR 0021): one of them, held on the caller's project
        p("mayRun(r.sub, r.obj)", "model", "use", "allow"),
    ]
});

/// FloorPolicies are the rules that hold whatever the stored policies say, so that a faulty
/// policy can never lock the administrators out.
pub static FLOOR_POLICIES: LazyLock<Vec<Policy>> =
    LazyLock::new(|| vec![DEFAULT_POLICIES[0].clone()]);

/// The error of an evaluation with no rule, as Go's Casbin words it (an eval() matcher needs a rule to evaluate).
const NO_RULE: &str = "please make sure rule exists in policy when using eval() in matcher";

/// r.sub as the rules see it: the Go field names, every field present.
#[derive(Debug, Clone, Default, Hash, Serialize, Deserialize)]
#[serde(default)]
struct SubView {
    #[serde(rename = "Subject")]
    subject: String,
    #[serde(rename = "Org")]
    org: String,
    #[serde(rename = "Project")]
    project: String,
    #[serde(rename = "Roles")]
    roles: Vec<String>,
    #[serde(rename = "OnBehalfOf")]
    on_behalf_of: String,
}

impl From<&Principal> for SubView {
    fn from(p: &Principal) -> Self {
        SubView {
            subject: p.subject.clone(),
            org: p.org.clone(),
            project: p.project.clone(),
            roles: p.roles.clone(),
            on_behalf_of: p.on_behalf_of.clone(),
        }
    }
}

impl From<SubView> for Principal {
    fn from(v: SubView) -> Self {
        Principal {
            subject: v.subject,
            org: v.org,
            project: v.project,
            roles: v.roles,
            on_behalf_of: v.on_behalf_of,
        }
    }
}

/// r.obj as the rules see it: the Go field names, every field present.
#[derive(Debug, Clone, Default, Hash, Serialize, Deserialize)]
#[serde(default)]
struct ObjView {
    #[serde(rename = "Type")]
    r#type: String,
    #[serde(rename = "ID")]
    id: String,
    #[serde(rename = "Org")]
    org: String,
    #[serde(rename = "Namespace")]
    namespace: String,
    #[serde(rename = "Owner")]
    owner: String,
    #[serde(rename = "Name")]
    name: String,
    #[serde(rename = "Role")]
    role: String,
    #[serde(rename = "Accountable")]
    accountable: String,
    #[serde(rename = "OrgChain")]
    org_chain: Vec<String>,
    #[serde(rename = "ProjectID")]
    project_id: String,
    #[serde(rename = "Roles")]
    roles: Vec<String>,
}

impl From<&Resource> for ObjView {
    fn from(r: &Resource) -> Self {
        ObjView {
            r#type: r.r#type.clone(),
            id: r.id.clone(),
            org: r.org.clone(),
            namespace: r.namespace.clone(),
            owner: r.owner.clone(),
            name: r.name.clone(),
            role: r.role.clone(),
            accountable: r.accountable.clone(),
            org_chain: r.org_chain.clone(),
            project_id: r.project_id.clone(),
            roles: r.roles.clone(),
        }
    }
}

impl From<ObjView> for Resource {
    fn from(v: ObjView) -> Self {
        Resource {
            r#type: v.r#type,
            id: v.id,
            org: v.org,
            namespace: v.namespace,
            owner: v.owner,
            name: v.name,
            role: v.role,
            accountable: v.accountable,
            org_chain: v.org_chain,
            project_id: v.project_id,
            roles: v.roles,
        }
    }
}

// The arguments of the functions of the rules, read the way Go's type assertions read them: a value of another kind
// is the zero value.
fn principal(d: &Dynamic) -> Principal {
    from_dynamic::<SubView>(d).unwrap_or_default().into()
}

fn resource(d: &Dynamic) -> Resource {
    from_dynamic::<ObjView>(d).unwrap_or_default().into()
}

fn string(d: Dynamic) -> String {
    d.into_string().unwrap_or_default()
}

fn has_role(sub: Dynamic, role: Dynamic) -> Dynamic {
    let role = string(role);
    principal(&sub).roles.contains(&role).into()
}

fn any_role(sub: &Dynamic, roles: Vec<Dynamic>) -> Dynamic {
    let p = principal(sub);
    roles
        .into_iter()
        .map(string)
        .any(|r| p.roles.contains(&r))
        .into()
}

fn has_any_role1(s: Dynamic, a: Dynamic) -> Dynamic {
    any_role(&s, vec![a])
}

fn has_any_role2(s: Dynamic, a: Dynamic, b: Dynamic) -> Dynamic {
    any_role(&s, vec![a, b])
}

fn has_any_role3(s: Dynamic, a: Dynamic, b: Dynamic, c: Dynamic) -> Dynamic {
    any_role(&s, vec![a, b, c])
}

fn has_any_role4(s: Dynamic, a: Dynamic, b: Dynamic, c: Dynamic, d: Dynamic) -> Dynamic {
    any_role(&s, vec![a, b, c, d])
}

fn has_any_role5(
    s: Dynamic,
    a: Dynamic,
    b: Dynamic,
    c: Dynamic,
    d: Dynamic,
    e: Dynamic,
) -> Dynamic {
    any_role(&s, vec![a, b, c, d, e])
}

fn has_role_in(sub: Dynamic, role: Dynamic, obj: Dynamic) -> Dynamic {
    principal(&sub)
        .has_role_in(&string(role), &resource(&obj))
        .into()
}

fn on_project(sub: Dynamic) -> Dynamic {
    principal(&sub).on_project().into()
}

fn may_run(sub: Dynamic, obj: Dynamic) -> Dynamic {
    principal(&sub).may_run(&resource(&obj)).into()
}

fn is_anonymous(sub: Dynamic) -> Dynamic {
    principal(&sub).anonymous().into()
}

/// Runs a future that completes without waiting (casbin-rs builds an in-memory enforcer through async functions that
/// do no I/O: a model read from a string, the null adapter).
fn at_once<F: Future>(f: F) -> Option<F::Output> {
    let mut f = std::pin::pin!(f);
    match f.as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(v) => Some(v),
        Poll::Pending => None,
    }
}

/// Casbin is an Authorizer backed by a Casbin enforcer. Its policies live in memory (the stored ones are Policy
/// nodes of the graph, which `lib/access` gives it).
pub struct Casbin {
    e: RwLock<Enforcer>,
}

impl std::fmt::Debug for Casbin {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("Casbin").finish_non_exhaustive()
    }
}

impl Casbin {
    /// NewCasbin creates an in-memory enforcer seeded with DefaultPolicies (Go's `NewCasbin(nil)`: its store is
    /// empty).
    pub fn new() -> Result<Casbin, Error> {
        Casbin::new_with(&DEFAULT_POLICIES)
    }

    /// NewCasbinWith creates an in-memory enforcer holding exactly the given policies.
    pub fn new_with(policies: &[Policy]) -> Result<Casbin, Error> {
        let c = new_casbin()?;
        for p in policies {
            c.add_policy(p)?;
        }
        Ok(c)
    }

    /// Authorize, synchronously: Casbin decides in memory. Anonymous principals are always denied.
    pub fn enforce(&self, req: &Request) -> Result<bool, Error> {
        if req.subject.anonymous() {
            return Ok(false);
        }
        let e = self.e.read();
        if e.get_policy().is_empty() {
            return Err(Error::Message(NO_RULE.into()));
        }
        Ok(e.enforce((
            SubView::from(&req.subject),
            ObjView::from(&req.resource),
            req.action.clone(),
        ))?)
    }

    /// Policies lists the rules.
    pub fn policies(&self) -> Result<Vec<Policy>, Error> {
        Ok(self
            .e
            .read()
            .get_policy()
            .into_iter()
            .filter(|r| r.len() == 4)
            .map(|r| Policy {
                rule: r[0].clone(),
                resource: r[1].clone(),
                action: r[2].clone(),
                effect: r[3].clone(),
            })
            .collect())
    }

    /// AddPolicy adds a rule (a rule already held is not added twice).
    pub fn add_policy(&self, p: &Policy) -> Result<(), Error> {
        self.e
            .write()
            .get_mut_model()
            .add_policy("p", "p", p.params());
        Ok(())
    }

    /// RemovePolicy removes a rule.
    pub fn remove_policy(&self, p: &Policy) -> Result<(), Error> {
        self.e
            .write()
            .get_mut_model()
            .remove_policy("p", "p", p.params());
        Ok(())
    }

    /// Reload reloads the rules from the adapter (other replicas' changes). The enforcer has no adapter: its rules
    /// stay as they are.
    pub fn reload(&self) -> Result<(), Error> {
        Ok(())
    }
}

fn new_casbin() -> Result<Casbin, Error> {
    let pending = || Error::Message("casbin: the in-memory enforcer did not build at once".into());
    let m = at_once(DefaultModel::from_str(MODEL)).ok_or_else(pending)??;
    let mut e = at_once(Enforcer::new(m, ())).ok_or_else(pending)??;
    e.add_function("hasRole", OperatorFunction::Arg2(has_role));
    // hasAnyRole is variadic in Go: rhai overloads it by arity (a role up to five)
    e.add_function("hasAnyRole", OperatorFunction::Arg2(has_any_role1));
    e.add_function("hasAnyRole", OperatorFunction::Arg3(has_any_role2));
    e.add_function("hasAnyRole", OperatorFunction::Arg4(has_any_role3));
    e.add_function("hasAnyRole", OperatorFunction::Arg5(has_any_role4));
    e.add_function("hasAnyRole", OperatorFunction::Arg6(has_any_role5));
    e.add_function("hasRoleIn", OperatorFunction::Arg3(has_role_in));
    e.add_function("onProject", OperatorFunction::Arg1(on_project));
    e.add_function("mayRun", OperatorFunction::Arg2(may_run));
    e.add_function("isAnonymous", OperatorFunction::Arg1(is_anonymous));
    Ok(Casbin { e: RwLock::new(e) })
}

#[async_trait]
impl Authorizer for Casbin {
    /// Authorize implements Authorizer. Anonymous principals are always denied.
    async fn authorize(&self, _ctx: &Ctx, req: &Request) -> Result<bool, Error> {
        self.enforce(req)
    }
}

/// Validate checks a policy by evaluating it once against a probe request.
pub fn validate(p: &Policy) -> Result<(), Error> {
    if p.effect != "allow" && p.effect != "deny" {
        return Err(Error::Invalid("effect must be allow or deny".into()));
    }
    if p.rule.is_empty() || p.resource.is_empty() || p.action.is_empty() {
        return Err(Error::Invalid(
            "rule, resource and action are required".into(),
        ));
    }
    let probe = Casbin::new_with(&[])?;
    probe.add_policy(p)?;
    let res = if p.resource == "*" {
        "probe"
    } else {
        &p.resource
    };
    let act = if p.action == "*" { "probe" } else { &p.action };
    let req = Request {
        subject: Principal {
            subject: "probe".into(),
            ..Default::default()
        },
        action: act.into(),
        resource: Resource {
            r#type: res.into(),
            ..Default::default()
        },
    };
    probe
        .enforce(&req)
        .map_err(|e| Error::Invalid(format!("invalid rule: {e}")))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::authz::check;

    fn who(subject: &str, org: &str, roles: &[&str]) -> Principal {
        Principal {
            subject: subject.into(),
            org: org.into(),
            roles: roles.iter().map(|r| (*r).to_string()).collect(),
            ..Default::default()
        }
    }

    fn res(typ: &str, org: &str, roles: &[&str]) -> Resource {
        Resource {
            r#type: typ.into(),
            org: org.into(),
            roles: roles.iter().map(|r| (*r).to_string()).collect(),
            ..Default::default()
        }
    }

    #[tokio::test]
    async fn default_policies() {
        let ctx = Ctx::background();
        let c = Casbin::new().unwrap();
        // a principal's roles are the ones it holds on the resource's project (merged by the authorizer, ADR 0043)
        let change = Resource {
            r#type: "change".into(),
            id: "c1".into(),
            org: "acme".into(),
            owner: "carol".into(),
            project_id: "PROJ-A".into(),
            ..Default::default()
        };
        let cases: Vec<(&str, Principal, &str, Resource, bool)> = vec![
            (
                "admin",
                who("root", "", &["admin"]),
                "apply",
                change.clone(),
                true,
            ),
            (
                "member of the project",
                who("alice", "acme", &["tech_lead"]),
                "apply",
                change.clone(),
                true,
            ),
            (
                "four eyes",
                who("carol", "acme", &["tech_lead"]),
                "apply",
                change.clone(),
                false,
            ),
            (
                "member from another org",
                who("bob", "globex", &["developer"]),
                "apply",
                change.clone(),
                true,
            ),
            (
                "not on the project",
                who("dave", "acme", &[]),
                "apply",
                change.clone(),
                false,
            ),
            (
                "process",
                who("dave", "acme", &["developer"]),
                "start",
                res("process", "acme", &[]),
                true,
            ),
            (
                "process off the project",
                who("dave", "acme", &[]),
                "start",
                res("process", "acme", &[]),
                false,
            ),
            (
                "read the project of another org",
                who("dave", "acme", &["developer"]),
                "read",
                res("process", "globex", &[]),
                true,
            ),
            (
                "read",
                who("eve", "acme", &[]),
                "read",
                res("methodology", "", &[]),
                true,
            ),
            (
                "read same org",
                who("eve", "acme", &[]),
                "read",
                res("process", "acme", &[]),
                true,
            ),
            (
                "read other org",
                who("eve", "acme", &[]),
                "read",
                res("process", "globex", &[]),
                false,
            ),
            (
                "write methodology",
                who("eve", "acme", &["developer"]),
                "write",
                res("methodology", "", &[]),
                false,
            ),
            (
                "methodology: administrators only",
                who("mia", "acme", &["methodologist"]),
                "publish",
                res("methodology", "acme", &[]),
                false,
            ),
            (
                "action open to the project",
                who("eve", "", &["developer"]),
                "run",
                res("action", "", &[]),
                true,
            ),
            (
                "action off the project",
                who("eve", "", &[]),
                "run",
                res("action", "", &[]),
                false,
            ),
            (
                "action of the role",
                who("eve", "", &["developer"]),
                "run",
                res("action", "", &["tester", "developer"]),
                true,
            ),
            (
                "action of another role",
                who("eve", "", &["developer"]),
                "run",
                res("action", "", &["tester"]),
                false,
            ),
            (
                "agent of another role",
                who("eve", "", &["developer"]),
                "run",
                res("agent", "", &["tester"]),
                false,
            ),
            (
                "anonymous",
                who("", "", &["admin"]),
                "read",
                res("methodology", "", &[]),
                false,
            ),
        ];
        for (name, sub, act, resource, want) in cases {
            let got = c
                .authorize(
                    &ctx,
                    &Request {
                        subject: sub,
                        action: act.into(),
                        resource,
                    },
                )
                .await;
            assert!(
                matches!(got, Ok(g) if g == want),
                "{name}: got {got:?}, want {want}"
            );
        }
    }

    #[tokio::test]
    async fn deny_overrides_and_admin() {
        let ctx = Ctx::background();
        let c = Casbin::new().unwrap();
        c.add_policy(&Policy::new(
            r#"r.sub.Org == "frozen""#,
            "change",
            "apply",
            "deny",
        ))
        .unwrap();
        let req = Request {
            subject: who("root", "frozen", &["admin"]),
            action: "apply".into(),
            resource: res("change", "", &[]),
        };
        assert!(
            !c.authorize(&ctx, &req).await.unwrap(),
            "deny rule must win"
        );
        let err = check(&ctx, Some(&c), &req).await.unwrap_err();
        assert!(err.is_forbidden(), "Check: {err}");
        assert_eq!(c.policies().unwrap().len(), DEFAULT_POLICIES.len() + 1);
    }

    #[test]
    fn validate_policies() {
        assert!(
            validate(&Policy::new(r#"hasRole(r.sub, "x") &&"#, "*", "*", "allow")).is_err(),
            "syntax error must be rejected"
        );
        assert!(
            validate(&Policy::new(
                r#"hasRole(r.sub, "x")"#,
                "change",
                "apply",
                "maybe"
            ))
            .is_err(),
            "bad effect must be rejected"
        );
        validate(&DEFAULT_POLICIES[4]).unwrap();
        for p in DEFAULT_POLICIES.iter() {
            validate(p).unwrap();
        }
        validate(&Policy::new(
            r#"hasAnyRole(r.sub, "a", "b", "c")"#,
            "*",
            "*",
            "allow",
        ))
        .unwrap();
    }

    #[tokio::test]
    async fn step_roles_held_in_a_unit_hold_below_it() {
        let c = Casbin::new().unwrap();
        let step = Resource {
            r#type: "step".into(),
            name: "delivery/build".into(),
            org: "TEAM-PAY".into(),
            org_chain: vec!["TEAM-PAY".into(), "DEP-IT".into(), "ORG-DEFAULT".into()],
            owner: "alice".into(),
            role: "developer".into(),
            accountable: "tech_lead".into(),
            ..Default::default()
        };
        for (sub, act, allowed) in [
            (who("bob", "", &["developer@DEP-IT"]), "perform", true), // held above the unit
            (who("bob", "", &["developer@TEAM-OPS"]), "perform", false), // held elsewhere
            (who("bob", "", &["developer"]), "perform", true),        // unscoped
            (who("bob", "", &["tech_lead@TEAM-PAY"]), "perform", false), // another role
            (who("bob", "", &["tech_lead@TEAM-PAY"]), "approve", true), // accountable
            (who("alice", "", &["tech_lead@TEAM-PAY"]), "approve", false), // never on its own change
        ] {
            let roles = sub.roles.clone();
            let ok = c
                .authorize(
                    &Ctx::background(),
                    &Request {
                        subject: sub,
                        action: act.into(),
                        resource: step.clone(),
                    },
                )
                .await;
            assert!(
                matches!(ok, Ok(g) if g == allowed),
                "{roles:?} {act}: {ok:?}, want {allowed}"
            );
        }
    }

    #[tokio::test]
    async fn has_any_role_and_policy_management() {
        let c = Casbin::new_with(&[Policy::new(
            r#"hasAnyRole(r.sub, "a", "b")"#,
            "*",
            "*",
            "allow",
        )])
        .unwrap();
        let req = |roles: &[&str]| Request {
            subject: who("x", "", roles),
            action: "do".into(),
            resource: res("thing", "", &[]),
        };
        assert!(c.authorize(&Ctx::background(), &req(&["b"])).await.unwrap());
        assert!(!c.authorize(&Ctx::background(), &req(&["c"])).await.unwrap());
        c.remove_policy(&Policy::new(
            r#"hasAnyRole(r.sub, "a", "b")"#,
            "*",
            "*",
            "allow",
        ))
        .unwrap();
        assert!(c.policies().unwrap().is_empty());
        assert!(
            c.authorize(&Ctx::background(), &req(&["b"])).await.is_err(),
            "no rule to evaluate"
        );
        assert_eq!(FLOOR_POLICIES.len(), 1);
    }
}
