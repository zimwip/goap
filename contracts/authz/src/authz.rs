//! The principal, the resource and the request of an access decision, and the authorizer that decides it.

use async_trait::async_trait;
use goap_ctx::Ctx;
use serde::{Deserialize, Serialize};

/// Principal is an authenticated caller (subject attributes of ABAC rules:
/// r.sub.Subject, r.sub.Org, r.sub.Roles).
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(default)]
pub struct Principal {
    #[serde(skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub org: String,
    /// Project is the caller's active project (ADR 0039), from their token; a call that names no project
    /// of its own (Change.ProjectID, StartProcessRequest.project_id, ...) defaults to it.
    #[serde(skip_serializing_if = "String::is_empty")]
    pub project: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub roles: Vec<String>,
    /// OnBehalfOf is the person a platform service acts for (System): the end user whose action led, from one
    /// consequence to the next (an event, a trigger, a run, a sub-agent), to what the service does. It attributes the
    /// work (the LLM call ledger, ADR 0114) and never grants anything: authorization reads the Subject. Ignored on a
    /// person, who acts for themselves.
    #[serde(rename = "onBehalfOf", skip_serializing_if = "String::is_empty")]
    pub on_behalf_of: String,
}

/// SystemPrefix starts the subject of a platform service acting by itself (system:registry, system:trigger:<key>,
/// system:graph): never a person, so never a user of the organisation.
pub const SYSTEM_PREFIX: &str = "system:";

/// System is the identity of the platform service name acting by itself ("registry", "graph", "trigger:<key>"): the
/// one constructor of such principals, so that no caller spells the prefix by hand.
pub fn system(name: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: format!("{SYSTEM_PREFIX}{name}"),
        roles: roles.iter().map(|r| (*r).to_string()).collect(),
        ..Default::default()
    }
}

/// RoleScope separates a role from the unit it is held in: "developer@TEAM-PAY".
pub const ROLE_SCOPE: &str = "@";

impl Principal {
    /// Anonymous reports whether the principal is unauthenticated.
    pub fn anonymous(&self) -> bool {
        self.subject.is_empty()
    }

    /// System reports whether the principal is a platform service rather than a person.
    pub fn is_system(&self) -> bool {
        self.subject.starts_with(SYSTEM_PREFIX)
    }

    /// EndUser is the person the principal's work is attributed to (ADR 0114): a person themselves, a platform service
    /// the person it acts for (OnBehalfOf, "" when it acts for nobody).
    pub fn end_user(&self) -> &str {
        if self.is_system() {
            &self.on_behalf_of
        } else {
            &self.subject
        }
    }

    /// For returns the principal acting for the end user user (ADR 0114): a platform service takes it as OnBehalfOf
    /// (kept when user is ""); a person is unchanged, acting for themselves.
    pub fn for_user(mut self, user: &str) -> Principal {
        if !user.is_empty() && self.is_system() {
            self.on_behalf_of = user.to_string();
        }
        self
    }

    /// HasRoleIn reports whether the principal holds the role for a resource: unscoped ("developer"), or held in the
    /// resource's unit or one of its ancestors ("developer@DEP-IT" holds for every unit below DEP-IT).
    pub fn has_role_in(&self, role: &str, res: &Resource) -> bool {
        if role.is_empty() {
            return false;
        }
        let own;
        let chain: &[String] = if res.org_chain.is_empty() && !res.org.is_empty() {
            own = [res.org.clone()];
            &own
        } else {
            &res.org_chain
        };
        self.roles.iter().any(|r| match r.split_once(ROLE_SCOPE) {
            Some((name, unit)) => name == role && chain.iter().any(|c| c == unit),
            None => r == role,
        })
    }

    /// OnProject reports whether the principal holds a role on the resource it is checked against: one an
    /// Assignment grants on the resource's project (merged by the authorizer that knows the organisation, ADR
    /// 0043), one its token carries (an external identity provider, a service identity), or RoleAdmin.
    pub fn on_project(&self) -> bool {
        !self.roles.is_empty()
    }

    /// MayRun reports whether the principal may run an agent or an action allowed to res.Roles: one of them, or
    /// any role on the project when it lists none.
    pub fn may_run(&self, res: &Resource) -> bool {
        if res.roles.is_empty() {
            return self.on_project();
        }
        res.roles.iter().any(|r| self.has_role_in(r, res))
    }
}

/// Resource is the object of an access request (r.obj.Type, r.obj.ID,
/// r.obj.Org, r.obj.Owner, r.obj.Name).
#[derive(Debug, Clone, Default, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(default)]
pub struct Resource {
    /// change, process, methodology, policy…
    #[serde(rename = "type")]
    pub r#type: String,
    /// resource identifier
    #[serde(skip_serializing_if = "String::is_empty")]
    pub id: String,
    /// owning organization
    #[serde(skip_serializing_if = "String::is_empty")]
    pub org: String,
    /// graph namespace of the object
    #[serde(skip_serializing_if = "String::is_empty")]
    pub namespace: String,
    /// subject who created / owns it
    #[serde(skip_serializing_if = "String::is_empty")]
    pub owner: String,
    /// human name (methodology name, goal…)
    #[serde(skip_serializing_if = "String::is_empty")]
    pub name: String,
    /// Role and Accountable are the responsible and accountable roles of a step of a process (ADR 0035 §2).
    #[serde(skip_serializing_if = "String::is_empty")]
    pub role: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub accountable: String,
    /// OrgChain is Org followed by its ancestor units (part_of), filled by the authorizer that knows the
    /// organisation: a role held in a unit holds in the units below it.
    #[serde(rename = "orgChain", skip_serializing_if = "Vec::is_empty")]
    pub org_chain: Vec<String>,
    /// ProjectID is the project the resource's change belongs to (ADR 0039); when set, the authorizer that
    /// knows the organisation merges the roles an Assignment grants the subject's org chain on this
    /// project's chain into the subject's roles before evaluating the request.
    #[serde(rename = "projectId", skip_serializing_if = "String::is_empty")]
    pub project_id: String,
    /// Roles are the roles allowed to run an agent or an action (ADR 0043): one of them is required, held on
    /// the project; none means any member of the project.
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub roles: Vec<String>,
}

/// Request is an ABAC access request (the Go struct has no JSON tags: its field names are the JSON names).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct Request {
    pub subject: Principal,
    pub action: String,
    pub resource: Resource,
}

/// The text of Go's `ErrForbidden`.
pub const ERR_FORBIDDEN: &str = "permission denied";

/// The errors of the package.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// ErrForbidden is returned when access is denied; the detail (who may not do what) follows on its own line, as
    /// `errors.Join(ErrForbidden, detail)` prints it. An empty detail is the bare ErrForbidden.
    #[error("{}", forbidden_text(.0))]
    Forbidden(String),
    /// A permission that is not `<resource>:<action>`.
    #[error("permission must be <resource>:<action>")]
    Permission,
    /// A malformed policy (Validate), with its reason.
    #[error("{0}")]
    Invalid(String),
    /// The enforcer failed (a rule that does not evaluate, ...).
    #[error(transparent)]
    Casbin(#[from] ::casbin::Error),
    /// Any other error of an authorizer, as its message.
    #[error("{0}")]
    Message(String),
    /// Any other error of an authorizer.
    #[error(transparent)]
    Other(Box<dyn std::error::Error + Send + Sync>),
}

fn forbidden_text(detail: &str) -> String {
    if detail.is_empty() {
        ERR_FORBIDDEN.to_string()
    } else {
        format!("{ERR_FORBIDDEN}\n{detail}")
    }
}

impl Error {
    /// `errors.Is(err, ErrForbidden)`.
    pub fn is_forbidden(&self) -> bool {
        matches!(self, Error::Forbidden(_))
    }
}

/// ParsePermission splits "type:action" (e.g. "change:apply").
pub fn parse_permission(p: &str) -> Result<(String, String), Error> {
    match p.split_once(':') {
        Some((t, a)) if !t.is_empty() && !a.is_empty() => Ok((t.to_string(), a.to_string())),
        _ => Err(Error::Permission),
    }
}

#[derive(Clone)]
struct PrincipalKey(Principal);

/// With returns a context carrying the principal.
pub fn with_principal(ctx: &Ctx, p: Principal) -> Ctx {
    ctx.with(PrincipalKey(p))
}

/// From returns the principal of the context (anonymous when absent).
pub fn principal_from(ctx: &Ctx) -> Principal {
    ctx.value::<PrincipalKey>()
        .map(|k| k.0.clone())
        .unwrap_or_default()
}

/// Authorizer decides access requests.
#[async_trait]
pub trait Authorizer: Send + Sync {
    async fn authorize(&self, ctx: &Ctx, req: &Request) -> Result<bool, Error>;
}

/// Check returns ErrForbidden when the request is denied. A nil authorizer
/// grants everything.
pub async fn check(ctx: &Ctx, a: Option<&dyn Authorizer>, req: &Request) -> Result<(), Error> {
    let Some(a) = a else {
        return Ok(());
    };
    if a.authorize(ctx, req).await? {
        Ok(())
    } else {
        Err(fmt_forbidden(req))
    }
}

fn fmt_forbidden(req: &Request) -> Error {
    let who = if req.subject.subject.is_empty() {
        "anonymous"
    } else {
        &req.subject.subject
    };
    Error::Forbidden(format!(
        "{who} may not {} {} {}",
        req.action, req.resource.r#type, req.resource.id
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_permission_splits() {
        assert_eq!(
            parse_permission("change:apply").unwrap(),
            ("change".into(), "apply".into())
        );
        assert!(parse_permission("apply").is_err());
    }

    #[test]
    fn principals() {
        let s = system("graph", &["admin"]);
        assert_eq!(s.subject, "system:graph");
        assert!(s.is_system());
        assert_eq!(s.end_user(), "");
        assert_eq!(s.clone().for_user("alice").end_user(), "alice");
        assert_eq!(s.for_user("alice").for_user("").end_user(), "alice");
        let p = Principal {
            subject: "bob".into(),
            ..Default::default()
        };
        assert_eq!(p.clone().for_user("alice").end_user(), "bob");
        let ctx = with_principal(&Ctx::background(), p.clone());
        assert_eq!(principal_from(&ctx), p);
        assert!(principal_from(&Ctx::background()).anonymous());
        assert_eq!(
            serde_json::to_string(&Principal {
                subject: "s".into(),
                on_behalf_of: "u".into(),
                ..Default::default()
            })
            .unwrap(),
            r#"{"subject":"s","onBehalfOf":"u"}"#
        );
        assert_eq!(
            serde_json::to_string(&Resource {
                r#type: "change".into(),
                ..Default::default()
            })
            .unwrap(),
            r#"{"type":"change"}"#
        );
    }

    struct Deny;

    #[async_trait]
    impl Authorizer for Deny {
        async fn authorize(&self, _: &Ctx, _: &Request) -> Result<bool, Error> {
            Ok(false)
        }
    }

    #[tokio::test]
    async fn check_formats_the_refusal() {
        let req = Request {
            action: "apply".into(),
            resource: Resource {
                r#type: "change".into(),
                id: "c1".into(),
                ..Default::default()
            },
            ..Default::default()
        };
        check(&Ctx::background(), None, &req).await.unwrap();
        let err = check(&Ctx::background(), Some(&Deny), &req)
            .await
            .unwrap_err();
        assert!(err.is_forbidden());
        assert_eq!(
            err.to_string(),
            "permission denied\nanonymous may not apply change c1"
        );
    }
}
