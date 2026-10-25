//! The ABAC rules of the organisation (Go's `contracts/authz` Casbin model, ADR 0020, 0043, 0138): a policy line is a
//! subject rule over `r.sub`, `r.obj` and `r.act`, a resource type (or `*`), an action (or `*`) and an effect; a request
//! is allowed when an allow rule matches and no deny rule does. The rules are the expressions Casbin evaluates, read
//! and run as Casbin runs them ([`crate::casbin`] over [`crate::govaluate`]).

/// The role an agent, an action, an operation or a model lists to be open to every member of the project
/// (`authz.AnyRole`).
pub const ANY_ROLE: &str = "*";

/// The principal of a request (`authz.Principal`), as the authorizer completed it.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
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
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Resource {
    pub typ: String,
    pub id: String,
    pub org: String,
    pub namespace: String,
    pub owner: String,
    pub name: String,
    pub role: String,
    pub accountable: String,
    pub org_chain: Vec<String>,
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

/// A set of rules (`authz.Casbin`): the policy lines Casbin holds, a line added twice held once.
pub struct Enforcer {
    rules: Vec<Policy>,
}

impl Enforcer {
    /// The rules, in their order (`NewCasbinWith`).
    pub fn new(policies: &[Policy]) -> Enforcer {
        let mut rules: Vec<Policy> = Vec::with_capacity(policies.len());
        for p in policies {
            if !rules.contains(p) {
                rules.push(p.clone());
            }
        }
        Enforcer { rules }
    }

    /// Whether a request is allowed (`Casbin.Authorize`): an anonymous subject never is; else Casbin's enforcement,
    /// its errors in Casbin's words.
    pub fn authorize(&self, sub: &Subject, res: &Resource, act: &str) -> Result<bool, String> {
        if sub.anonymous() {
            return Ok(false);
        }
        crate::casbin::enforce(&self.rules, sub, res, act)
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
    fn refuses_what_casbin_refuses() {
        let refusal = |rule: &str| validate(&Policy::of(rule, "*", "*", "allow")).err().unwrap_or_default();
        assert_eq!(
            refusal("r.sub.Subject =="),
            "invalid rule: error while parsing eval parameter: r_sub.Subject ==, Unexpected end of expression"
        );
        assert_eq!(
            refusal("r.sub.Subject"),
            "invalid rule: Value 'probe' cannot be used with the logical operator '&&', it is not a bool"
        );
        assert_eq!(
            refusal("r.sub.Nope == 1"),
            "invalid rule: No method or field 'Nope' present on parameter 'r_sub'"
        );
        assert_eq!(
            refusal("isAnonymous()"),
            "invalid rule: panic: runtime error: index out of range [0] with length 0"
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

    /// The answers of Go's Casbin, recorded by `contracts/authz` `TestRustCasesAreCurrent`.
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
        let check = |what: &str, policies: &[Policy], answers: &serde_json::Value, failures: &mut Vec<String>| {
            let e = Enforcer::new(policies);
            for (i, (sub, res, act)) in requests.iter().enumerate() {
                let want = (
                    answers[i]["allowed"].as_bool().unwrap_or(false),
                    text(&answers[i], "error"),
                );
                let got = match e.authorize(sub, res, act) {
                    Ok(b) => (b, String::new()),
                    Err(m) => (false, m),
                };
                if got != want {
                    failures.push(format!("{what} #{i}: got {got:?}, want {want:?}"));
                }
            }
        };
        let mut failures = Vec::new();
        for c in cases["rules"].as_array().unwrap() {
            let rule = text(c, "rule");
            let p = Policy::of(&rule, "*", "*", "allow");
            let got = validate(&p).err().unwrap_or_default();
            if got != text(c, "validate") {
                failures.push(format!("validate {rule}: got {got:?}, want {:?}", text(c, "validate")));
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
