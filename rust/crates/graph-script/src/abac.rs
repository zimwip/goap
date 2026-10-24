//! The ABAC rules of the organisation (Go's `contracts/authz` Casbin model, ADR 0020, 0043, 0138): a policy line is a
//! subject rule over `r.sub`, `r.obj` and `r.act`, a resource type (or `*`), an action (or `*`) and an effect; a request
//! is allowed when an allow rule matches and no deny rule does. The rules are the expressions Casbin evaluates; they
//! are evaluated here as CEL, whose syntax the rules the platform writes share, with the functions of the Go model.
use std::{collections::HashMap, sync::Arc};

use cel::{Context, Program, Value, extractors::Arguments};

/// The role an agent, an action, an operation or a model lists to be open to every member of the project
/// (`authz.AnyRole`).
pub const ANY_ROLE: &str = "*";

/// The principal of a request (`authz.Principal`), as the authorizer completed it.
#[derive(Debug, Clone, Default)]
pub struct Subject {
    pub subject: String,
    pub org: String,
    pub project: String,
    pub roles: Vec<String>,
    pub global: Vec<String>,
    pub project_admin: bool,
    pub project_owner: bool,
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
#[derive(Debug, Clone, Default)]
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

/// A set of rules compiled (`authz.Casbin`).
pub struct Enforcer {
    rules: Vec<(Policy, Program)>,
}

impl Enforcer {
    /// Compiles rules; one that is no CEL expression is refused (the Go service's Casbin reads it).
    pub fn new(policies: &[Policy]) -> Result<Enforcer, String> {
        let mut rules = Vec::with_capacity(policies.len());
        for p in policies {
            if p.effect != "allow" && p.effect != "deny" {
                return Err(format!("policy {:?}: effect must be allow or deny", p.rule));
            }
            let prog = Program::compile(&p.rule).map_err(|e| format!("policy {:?}: {e}", p.rule))?;
            rules.push((p.clone(), prog));
        }
        Ok(Enforcer { rules })
    }

    /// Whether a request is allowed: some allow rule matches and no deny rule does (`Casbin.Authorize`); an
    /// anonymous subject never is.
    pub fn authorize(&self, sub: &Subject, res: &Resource, act: &str) -> Result<bool, String> {
        if sub.anonymous() {
            return Ok(false);
        }
        let ctx = context(sub, res, act)?;
        let (mut allow, mut deny) = (false, false);
        for (p, prog) in &self.rules {
            if (p.resource != "*" && p.resource != res.typ) || (p.action != "*" && p.action != act) {
                continue;
            }
            match prog.execute(&ctx) {
                Ok(Value::Bool(true)) => {
                    if p.effect == "deny" {
                        deny = true;
                    } else {
                        allow = true;
                    }
                }
                Ok(Value::Bool(false)) => {}
                Ok(_) => return Err(format!("policy {:?} did not return a bool", p.rule)),
                Err(e) => return Err(format!("policy {:?}: {e}", p.rule)),
            }
        }
        Ok(allow && !deny)
    }
}

fn strings(l: &[String]) -> Value {
    Value::List(Arc::new(l.iter().map(|s| Value::String(Arc::new(s.clone()))).collect()))
}

fn map(fields: Vec<(&str, Value)>) -> Value {
    let m: HashMap<String, Value> = fields.into_iter().map(|(k, v)| (k.to_string(), v)).collect();
    Value::Map(m.into())
}

/// The variable `r` of the rules and the functions of the model, over the request itself.
fn context(sub: &Subject, res: &Resource, act: &str) -> Result<Context<'static, 'static>, String> {
    let s = |v: &str| Value::String(Arc::new(v.to_string()));
    let r = map(vec![
        (
            "sub",
            map(vec![
                ("Subject", s(&sub.subject)),
                ("Org", s(&sub.org)),
                ("Project", s(&sub.project)),
                ("Roles", strings(&sub.roles)),
                ("Global", strings(&sub.global)),
                ("ProjectAdmin", Value::Bool(sub.project_admin)),
                ("ProjectOwner", Value::Bool(sub.project_owner)),
            ]),
        ),
        (
            "obj",
            map(vec![
                ("Type", s(&res.typ)),
                ("ID", s(&res.id)),
                ("Org", s(&res.org)),
                ("Namespace", s(&res.namespace)),
                ("Owner", s(&res.owner)),
                ("Name", s(&res.name)),
                ("Role", s(&res.role)),
                ("Accountable", s(&res.accountable)),
                ("OrgChain", strings(&res.org_chain)),
                ("ProjectID", s(&res.project_id)),
                ("Roles", strings(&res.roles)),
            ]),
        ),
        ("act", s(act)),
    ]);
    let mut ctx = Context::default();
    ctx.add_variable_from_value("r", r);
    let (sub, res) = (Arc::new(sub.clone()), Arc::new(res.clone()));
    let text = |args: &Arguments, i: usize| match args.0.get(i) {
        Some(Value::String(v)) => v.to_string(),
        _ => String::new(),
    };
    let p = sub.clone();
    ctx.add_function("hasRole", move |args: Arguments| p.has_role(&text(&args, 1)))
        .map_err(|e| e.to_string())?;
    let p = sub.clone();
    ctx.add_function("hasAnyRole", move |args: Arguments| {
        (1..args.0.len()).any(|i| p.has_role(&text(&args, i)))
    })
    .map_err(|e| e.to_string())?;
    let (p, o) = (sub.clone(), res.clone());
    ctx.add_function("hasRoleIn", move |args: Arguments| p.has_role_in(&text(&args, 1), &o))
        .map_err(|e| e.to_string())?;
    let p = sub.clone();
    ctx.add_function("onProject", move |_: Arguments| p.on_project())
        .map_err(|e| e.to_string())?;
    let (p, o) = (sub.clone(), res.clone());
    ctx.add_function("mayRun", move |_: Arguments| p.may_run(&o))
        .map_err(|e| e.to_string())?;
    let p = sub.clone();
    ctx.add_function("isProjectAdmin", move |_: Arguments| p.project_admin || p.project_owner)
        .map_err(|e| e.to_string())?;
    let p = sub.clone();
    ctx.add_function("isProjectOwner", move |_: Arguments| p.project_owner)
        .map_err(|e| e.to_string())?;
    let p = sub;
    ctx.add_function("isAnonymous", move |_: Arguments| p.anonymous())
        .map_err(|e| e.to_string())?;
    Ok(ctx)
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
        let e = Enforcer::new(&default_policies()).unwrap();
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
        assert!(Enforcer::new(&[Policy::of("r.sub.Subject ==", "*", "*", "allow")]).is_err());
    }
}
