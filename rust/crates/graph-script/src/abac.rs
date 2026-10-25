//! The ABAC rules of the organisation (Go's `contracts/authz` Casbin model, ADR 0020, 0043, 0138): a policy line is a
//! subject rule over `r.sub`, `r.obj` and `r.act`, a resource type (or `*`), an action (or `*`) and an effect; a request
//! is allowed when an allow rule matches and no deny rule does. The rules are evaluated by casbin-rs (its expressions
//! are rhai's): the functions of Go's model and the fields of the principal and the resource under Go's names, so a
//! rule of the common syntax (`hasRole(r.sub, "admin") && r.obj.Owner != r.sub.Subject`) decides as Go's does.
use casbin::{
    CoreApi, DefaultModel, MemoryAdapter, MgmtApi,
    function_map::OperatorFunction,
    rhai::{Dynamic, serde::from_dynamic},
};
use serde::{Deserialize, Serialize};

/// The role an agent, an action, an operation or a model lists to be open to every member of the project
/// (`authz.AnyRole`).
pub const ANY_ROLE: &str = "*";

/// The principal of a request (`authz.Principal`), as the authorizer completed it.
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct Subject {
    pub subject: String,
    pub org: String,
    pub project: String,
    pub roles: Vec<String>,
    pub global: Vec<String>,
    pub project_admin: bool,
    pub project_owner: bool,
    /// the person a platform service acts for (`OnBehalfOf`, ADR 0114)
    pub on_behalf_of: String,
}

impl Subject {
    pub fn anonymous(&self) -> bool {
        self.subject.is_empty()
    }

    /// Holds a role on the project of the request or platform-wide (`HasRole`).
    pub fn has_role(&self, role: &str) -> bool {
        self.roles.iter().chain(&self.global).any(|r| r == role)
    }

    /// Holds a role for a resource: unscoped, or held in its unit or one above (`HasRoleIn`, `developer@DEP-IT`).
    pub fn has_role_in(&self, role: &str, res: &Resource) -> bool {
        if role.is_empty() {
            return false;
        }
        let one = [res.org.clone()];
        let chain: &[String] = if res.org_chain.is_empty() && !res.org.is_empty() {
            &one
        } else {
            &res.org_chain
        };
        self.roles.iter().any(|r| match r.split_once('@') {
            None => r == role,
            Some((name, unit)) => name == role && chain.iter().any(|c| c == unit),
        })
    }

    /// Works on the project of the resource (`OnProject`): a role there, or its administration.
    pub fn on_project(&self) -> bool {
        !self.roles.is_empty() || self.project_admin || self.project_owner
    }

    /// May run what is allowed to the resource's roles (`MayRun`).
    pub fn may_run(&self, res: &Resource) -> bool {
        if self.project_admin || self.project_owner {
            return true;
        }
        res.roles.iter().any(|r| {
            if r == ANY_ROLE {
                !self.roles.is_empty()
            } else {
                self.has_role_in(r, res)
            }
        })
    }
}

/// The object of a request (`authz.Resource`).
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct Resource {
    #[serde(rename = "Type")]
    pub typ: String,
    #[serde(rename = "ID")]
    pub id: String,
    pub org: String,
    pub namespace: String,
    pub owner: String,
    pub name: String,
    pub role: String,
    pub accountable: String,
    pub org_chain: Vec<String>,
    #[serde(rename = "ProjectID")]
    pub project_id: String,
    pub roles: Vec<String>,
}

/// One rule (`authz.Policy`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Policy {
    pub rule: String,
    pub resource: String,
    pub action: String,
    pub effect: String,
}

impl Policy {
    fn of(rule: &str, resource: &str, action: &str, effect: &str) -> Policy {
        Policy {
            rule: rule.into(),
            resource: resource.into(),
            action: action.into(),
            effect: effect.into(),
        }
    }
}

/// The rules seeded in an empty store (`authz.DefaultPolicies`), in Go's order.
pub fn default_policies() -> Vec<Policy> {
    let owner_only = r#"!isProjectOwner(r.sub) && !hasRole(r.sub, "admin")"#;
    let own = r#"r.sub.Subject == r.obj.Owner && !hasRole(r.sub, "admin")"#;
    let mut out = vec![
        Policy::of(r#"hasRole(r.sub, "admin")"#, "*", "*", "allow"),
        Policy::of(r#"hasRole(r.sub, "reader")"#, "*", "read", "allow"),
        Policy::of(
            r#"!isAnonymous(r.sub) && ((r.obj.Org != "" && r.obj.Org == r.sub.Org) || onProject(r.sub))"#,
            "*",
            "read",
            "allow",
        ),
        Policy::of(r#"!isAnonymous(r.sub) && r.obj.Namespace != """#, "*", "read", "allow"),
        Policy::of("!isAnonymous(r.sub)", "trigger", "read", "allow"),
        Policy::of("isProjectAdmin(r.sub)", "*", "*", "allow"),
        Policy::of(owner_only, "assignment", "flag", "deny"),
        Policy::of(owner_only, "project", "stop", "deny"),
        Policy::of(owner_only, "project", "move", "deny"),
    ];
    for (res, act) in [
        ("process", "*"),
        ("object", "create"),
        ("node", "transition"),
        ("requirement", "approve"),
        ("tool", "call"),
        ("change", "transition"),
        ("change", "move"),
        ("prompt", "inspect"),
        ("change", "apply"),
    ] {
        out.push(Policy::of("onProject(r.sub)", res, act, "allow"));
    }
    out.extend([
        Policy::of(own, "change", "apply", "deny"),
        Policy::of(
            r#"hasRole(r.sub, "release_manager") && r.sub.Subject != r.obj.Owner"#,
            "release",
            "deploy",
            "allow",
        ),
        Policy::of("hasRoleIn(r.sub, r.obj.Role, r.obj)", "step", "perform", "allow"),
        Policy::of("hasRoleIn(r.sub, r.obj.Accountable, r.obj)", "step", "approve", "allow"),
        Policy::of(own, "step", "approve", "deny"),
        Policy::of("mayRun(r.sub, r.obj)", "action", "run", "allow"),
        Policy::of("mayRun(r.sub, r.obj)", "agent", "run", "allow"),
        Policy::of("mayRun(r.sub, r.obj)", "operation", "perform", "allow"),
        Policy::of(
            r#"hasRole(r.sub, "derogation_signatory")"#,
            "derogation",
            "sign",
            "allow",
        ),
        Policy::of("mayRun(r.sub, r.obj)", "derogation", "sign-role", "allow"),
        Policy::of("mayRun(r.sub, r.obj)", "decision", "sit", "allow"),
        Policy::of("mayRun(r.sub, r.obj)", "model", "use", "allow"),
    ]);
    out
}

/// The rules that hold whatever the stored ones say (`authz.FloorPolicies`): an administrator may do everything.
pub fn floor_policies() -> Vec<Policy> {
    default_policies().into_iter().take(1).collect()
}

/// The model of Go's `contracts/authz`: a policy line is a subject rule, a resource type, an action and an effect.
const MODEL: &str = r#"
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub_rule, obj_type, act, eft

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = (p.obj_type == "*" || r.obj.Type == p.obj_type) && (p.act == "*" || r.act == p.act) && eval(p.sub_rule)
"#;

fn subject(d: Dynamic) -> Subject {
    from_dynamic(&d).unwrap_or_default()
}

fn resource(d: Dynamic) -> Resource {
    from_dynamic(&d).unwrap_or_default()
}

fn text(d: Dynamic) -> String {
    d.into_string().unwrap_or_default()
}

/// The functions of Go's model (`authz.casbinFuncs`).
fn functions() -> [(&'static str, OperatorFunction); 8] {
    [
        (
            "hasRole",
            OperatorFunction::Arg2(|s, r| subject(s).has_role(&text(r)).into()),
        ),
        (
            "hasAnyRole",
            OperatorFunction::Arg2(|s, roles| {
                let s = subject(s);
                let roles: Vec<String> = if roles.is_array() {
                    from_dynamic(&roles).unwrap_or_default()
                } else {
                    vec![text(roles)]
                };
                roles.iter().any(|r| s.has_role(r)).into()
            }),
        ),
        (
            "hasRoleIn",
            OperatorFunction::Arg3(|s, r, o| subject(s).has_role_in(&text(r), &resource(o)).into()),
        ),
        ("onProject", OperatorFunction::Arg1(|s| subject(s).on_project().into())),
        (
            "mayRun",
            OperatorFunction::Arg2(|s, o| subject(s).may_run(&resource(o)).into()),
        ),
        (
            "isProjectAdmin",
            OperatorFunction::Arg1(|s| {
                let s = subject(s);
                (s.project_admin || s.project_owner).into()
            }),
        ),
        (
            "isProjectOwner",
            OperatorFunction::Arg1(|s| subject(s).project_owner.into()),
        ),
        ("isAnonymous", OperatorFunction::Arg1(|s| subject(s).anonymous().into())),
    ]
}

/// A set of rules (`authz.Casbin`): the policy lines Casbin holds, a line added twice held once.
pub struct Enforcer {
    e: casbin::Enforcer,
}

impl Enforcer {
    /// The rules, in their order (`NewCasbinWith`).
    pub fn new(policies: &[Policy]) -> Enforcer {
        let mut rules: Vec<Vec<String>> = Vec::with_capacity(policies.len());
        for p in policies {
            let line = vec![p.rule.clone(), p.resource.clone(), p.action.clone(), p.effect.clone()];
            if !rules.contains(&line) {
                rules.push(line);
            }
        }
        // nothing here waits: the model is a text, the adapter memory
        let e = futures::executor::block_on(async {
            let model = DefaultModel::from_str(MODEL).await.expect("the ABAC model");
            let mut e = casbin::Enforcer::new(model, MemoryAdapter::default())
                .await
                .expect("an enforcer over memory");
            for (name, f) in functions() {
                e.add_function(name, f);
            }
            if !rules.is_empty() {
                e.add_policies(rules).await.expect("policies in memory");
            }
            e
        });
        Enforcer { e }
    }

    /// Whether a request is allowed (`Casbin.Authorize`): an anonymous subject never is; else Casbin's enforcement,
    /// a rule failing to evaluate an error, one that panics in rhai (built `unchecked` by casbin: a division by zero)
    /// too.
    pub fn authorize(&self, sub: &Subject, res: &Resource, act: &str) -> Result<bool, String> {
        if sub.anonymous() {
            return Ok(false);
        }
        let args = (sub.clone(), res.clone(), act.to_string());
        std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| self.e.enforce(args)))
            .map_err(|_| "a rule failed to evaluate".to_string())?
            .map_err(|e| e.to_string())
    }
}

/// Whether a stored rule holds (`authz.Validate`): its effect, its fields, then one evaluation against a probe; a rule
/// refused here is left out of the organisation's rules.
pub fn validate(p: &Policy) -> Result<(), String> {
    if p.effect != "allow" && p.effect != "deny" {
        return Err("effect must be allow or deny".into());
    }
    if p.rule.is_empty() || p.resource.is_empty() || p.action.is_empty() {
        return Err("rule, resource and action are required".into());
    }
    let probe = |v: &str| if v == "*" { "probe".to_string() } else { v.to_string() };
    let sub = Subject {
        subject: "probe".into(),
        ..Subject::default()
    };
    let res = Resource {
        typ: probe(&p.resource),
        ..Resource::default()
    };
    Enforcer::new(std::slice::from_ref(p))
        .authorize(&sub, &res, &probe(&p.action))
        .map(|_| ())
        .map_err(|e| format!("invalid rule: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn sub(subject: &str, roles: &[&str], global: &[&str]) -> Subject {
        Subject {
            subject: subject.into(),
            roles: roles.iter().map(|r| r.to_string()).collect(),
            global: global.iter().map(|r| r.to_string()).collect(),
            ..Subject::default()
        }
    }

    #[test]
    fn decides_as_the_default_policies() {
        let e = Enforcer::new(&default_policies());
        let change = Resource {
            typ: "change".into(),
            owner: "ann".into(),
            ..Resource::default()
        };
        let prompt = Resource {
            typ: "prompt".into(),
            ..Resource::default()
        };
        assert_eq!(e.authorize(&sub("bob", &["dev"], &[]), &prompt, "inspect"), Ok(true));
        assert_eq!(e.authorize(&sub("bob", &[], &[]), &prompt, "inspect"), Ok(false));
        assert_eq!(e.authorize(&sub("bob", &["dev"], &[]), &change, "apply"), Ok(true));
        assert_eq!(e.authorize(&sub("ann", &["dev"], &[]), &change, "apply"), Ok(false));
        assert_eq!(e.authorize(&sub("ann", &[], &["admin"]), &change, "apply"), Ok(true));
        assert_eq!(e.authorize(&sub("", &[], &["admin"]), &change, "apply"), Ok(false));
        let op = Resource {
            typ: "operation".into(),
            org: "TEAM".into(),
            roles: vec!["dev".into()],
            ..Resource::default()
        };
        assert_eq!(e.authorize(&sub("bob", &["dev@TEAM"], &[]), &op, "perform"), Ok(true));
        assert_eq!(e.authorize(&sub("bob", &["dev@OTHER"], &[]), &op, "perform"), Ok(false));
        let node = Resource {
            typ: "node".into(),
            namespace: "alm".into(),
            ..Resource::default()
        };
        assert_eq!(e.authorize(&sub("bob", &[], &[]), &node, "read"), Ok(true));
        for p in default_policies() {
            assert_eq!(validate(&p), Ok(()), "{}", p.rule);
        }
    }

    #[test]
    fn refuses_a_rule_that_does_not_evaluate() {
        for rule in ["r.sub.Subject ==", "r.sub.Subject", "isAnonymous()", "nope(r.sub)"] {
            assert!(validate(&Policy::of(rule, "*", "*", "allow")).is_err(), "{rule}");
        }
        assert_eq!(
            validate(&Policy::of("r.sub.Subject == \"x\"", "*", "*", "allow")),
            Ok(())
        );
    }

    fn strings(v: &serde_json::Value) -> Vec<String> {
        v.as_array()
            .map(|a| a.iter().map(|s| s.as_str().unwrap_or_default().to_string()).collect())
            .unwrap_or_default()
    }

    fn text(v: &serde_json::Value, k: &str) -> String {
        v[k].as_str().unwrap_or_default().to_string()
    }

    /// The decisions of Go's Casbin, recorded by `contracts/authz` `TestRustCasesAreCurrent`, on the rules both read.
    #[test]
    fn answers_as_go_casbin() {
        let cases: serde_json::Value = serde_json::from_str(include_str!("casbin_cases.gen.json")).unwrap();
        let requests: Vec<(Subject, Resource, String)> = cases["requests"]
            .as_array()
            .unwrap()
            .iter()
            .map(|r| {
                let s = &r["Subject"];
                let o = &r["Resource"];
                let sub = Subject {
                    subject: text(s, "subject"),
                    org: text(s, "org"),
                    project: text(s, "project"),
                    roles: strings(&s["roles"]),
                    global: strings(&s["global"]),
                    project_admin: s["projectAdmin"].as_bool().unwrap_or(false),
                    project_owner: s["projectOwner"].as_bool().unwrap_or(false),
                    on_behalf_of: text(s, "onBehalfOf"),
                };
                let res = Resource {
                    typ: text(o, "type"),
                    id: text(o, "id"),
                    org: text(o, "org"),
                    namespace: text(o, "namespace"),
                    owner: text(o, "owner"),
                    name: text(o, "name"),
                    role: text(o, "role"),
                    accountable: text(o, "accountable"),
                    org_chain: strings(&o["orgChain"]),
                    project_id: text(o, "projectId"),
                    roles: strings(&o["roles"]),
                };
                (sub, res, text(r, "Action"))
            })
            .collect();
        // the decisions, not the words: a refusal is a refusal whatever it says
        let check = |what: &str, policies: &[Policy], answers: &serde_json::Value, failures: &mut Vec<String>| {
            let e = Enforcer::new(policies);
            for (i, (sub, res, act)) in requests.iter().enumerate() {
                let want = answers[i]["allowed"].as_bool().unwrap_or(false);
                let got = e.authorize(sub, res, act).unwrap_or(false);
                if got != want {
                    failures.push(format!("{what} #{i}: got {got}, want {want}"));
                }
            }
        };
        let mut failures = Vec::new();
        let defaults: Vec<String> = default_policies().into_iter().map(|p| p.rule).collect();
        for c in cases["rules"].as_array().unwrap() {
            let rule = text(c, "rule");
            let p = Policy::of(&rule, "*", "*", "allow");
            // a rule only govaluate reads (`=~`, `??`, `in (…)`, methods of the principal, dates read from strings,
            // `**` to the right) is not compared
            let govaluate = rule.contains("**") || rule.contains("2006") || rule.contains("3:04PM");
            if govaluate || !text(c, "validate").is_empty() || validate(&p).is_err() {
                assert!(!defaults.contains(&rule), "a default rule refused: {rule}");
                continue;
            }
            check(&rule, &[p], &c["answers"], &mut failures);
        }
        for (n, c) in cases["sets"].as_array().unwrap().iter().enumerate() {
            let policies: Vec<Policy> = c["policies"]
                .as_array()
                .unwrap()
                .iter()
                .map(|p| {
                    Policy::of(
                        &text(p, "rule"),
                        &text(p, "resource"),
                        &text(p, "action"),
                        &text(p, "effect"),
                    )
                })
                .collect();
            check(&format!("set {n}"), &policies, &c["answers"], &mut failures);
        }
        assert!(
            failures.is_empty(),
            "{} divergences:\n{}",
            failures.len(),
            failures.join("\n")
        );
    }
}
