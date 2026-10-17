//! The session of a caller (GET /api/whoami, ADR 0070).

use goap_authz::{Authorizer as _, Principal, Request, Resource};
use goap_ctx::Ctx;
use goap_domain::{NAMESPACE_METHODOLOGY, NAMESPACE_PLATFORM, Structures, TYPE_ADAPTER, TYPE_MCP};
use serde::{Deserialize, Serialize};

use crate::Error;
use crate::access::{
    Authorizer, LINK_MEMBER_OF, NAMESPACE_ORGANISATION, NODE_TYPE_POLICY, NODE_TYPE_USER,
    POLICY_PREFIX, PROP_WAITING_UNIT, ROLE_ADMIN,
};
use crate::personal::USER_PREFIX;
use crate::project::{
    ASSIGNMENT_PREFIX, LINK_ASSIGNS_ORG, LINK_ASSIGNS_PROJECT, NODE_TYPE_ASSIGNMENT, PLATFORM_SCOPE,
};
use crate::roles::{Role, builtin_roles};

/// Session is what the web learns of the caller once, at sign-in and on each project switch (GET /api/whoami, ADR
/// 0070): the principal the platform sees, and the knowledge derived from the identity and the organisation that a
/// client would otherwise mirror: the names of the organisation, how its keys are built, what the caller may attempt.
/// The principal's fields are those of the JSON object, the rest are added to them.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Session {
    #[serde(flatten)]
    pub principal: Principal,
    /// Can are hints for the UI to show or hide what it offers; the server enforces each call anyway.
    pub can: Capabilities,
    /// Structures are the organisation and the projects (ADR 0054): their node types, parent links, roots.
    pub structures: Structures,
    pub names: Names,
    /// PlatformRoles are the built-in roles an Assignment grants platform-wide ([`builtin_roles`]).
    pub platform_roles: Vec<Role>,
}

/// Capabilities are what the caller may attempt, derived by the authorizer (never from a role name).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct Capabilities {
    /// Administer: what the compiled-in floor allows administrators (the organisation, projects, policies, adapters,
    /// methodologies, domains, the model catalogue and quotas, the usage of the whole platform): the controls that only
    /// administrators may use.
    pub administer: bool,
    /// Approve: the caller works on their active project (a platform role, or a role an Assignment grants them on it
    /// or a project above), so an approval of someone else's run may be theirs to give. The engine decides, run by run
    /// (step:approve, action:run), so this only says whether to tell them of the approvals waiting.
    pub approve: bool,
    /// LowerCriticality: the caller may lower the criticality of a change of their active project
    /// (change:lower-criticality, ADR 0075 §3); raising it asks nothing more than writing the change.
    pub lower_criticality: bool,
}

/// Names are the namespaces, node types, link types, key schemes and properties of the built-in domains the clients
/// build and read nodes with: the one place they are written (the units and projects come from the structures).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct Names {
    pub namespaces: NamesNamespaces,
    pub types: NamesTypes,
    pub links: NamesLinks,
    pub keys: NamesKeys,
    pub roles: NamesRoles,
    pub props: NamesProps,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct NamesNamespaces {
    pub organisation: String,
    pub platform: String,
    /// Meta is the namespace of the meta-domain (the methodology elements), left out of the views of what is worked
    /// on.
    pub meta: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NamesTypes {
    pub org_unit: String,
    pub project_unit: String,
    pub user: String,
    pub assignment: String,
    pub adapter: String,
    pub policy: String,
    #[serde(rename = "mcp")]
    pub mcp: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NamesLinks {
    pub part_of: String,
    pub project_part_of: String,
    pub member_of: String,
    pub assigns_org: String,
    pub assigns_project: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default, rename_all = "camelCase")]
pub struct NamesKeys {
    /// User prefixes the key of a user node (`user_key`): <prefix><subject>.
    pub user: String,
    /// Assignment prefixes the key of an Assignment (`assignment_key`): <prefix><unit>/<project>, the project being
    /// PlatformScope for a platform-wide one.
    pub assignment: String,
    pub platform_scope: String,
    /// Policy prefixes the key of a Policy node (`policy_key`; a client may add its own suffix).
    pub policy: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct NamesRoles {
    /// Admin is the platform role of an administrator ([`ROLE_ADMIN`]).
    pub admin: String,
}

#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
#[serde(default)]
pub struct NamesProps {
    /// Waiting flags the unit new users join ([`PROP_WAITING_UNIT`]).
    pub waiting: String,
}

/// NamesOf are the names of the built-in domains, the units and projects being those of the structures st.
pub fn names_of(st: &Structures) -> Names {
    let (org, proj) = (st.organisation(), st.project());
    Names {
        namespaces: NamesNamespaces {
            organisation: NAMESPACE_ORGANISATION.into(),
            platform: NAMESPACE_PLATFORM.into(),
            meta: NAMESPACE_METHODOLOGY.into(),
        },
        types: NamesTypes {
            org_unit: org.r#type.clone(),
            project_unit: proj.r#type.clone(),
            user: NODE_TYPE_USER.into(),
            assignment: NODE_TYPE_ASSIGNMENT.into(),
            adapter: TYPE_ADAPTER.into(),
            policy: NODE_TYPE_POLICY.into(),
            mcp: TYPE_MCP.into(),
        },
        links: NamesLinks {
            part_of: org.parent.clone(),
            project_part_of: proj.parent.clone(),
            member_of: LINK_MEMBER_OF.into(),
            assigns_org: LINK_ASSIGNS_ORG.into(),
            assigns_project: LINK_ASSIGNS_PROJECT.into(),
        },
        keys: NamesKeys {
            user: USER_PREFIX.into(),
            assignment: ASSIGNMENT_PREFIX.into(),
            platform_scope: PLATFORM_SCOPE.into(),
            policy: POLICY_PREFIX.into(),
        },
        roles: NamesRoles {
            admin: ROLE_ADMIN.into(),
        },
        props: NamesProps {
            waiting: PROP_WAITING_UNIT.into(),
        },
    }
}

/// The text of `ErrNoSession`: the organisation cannot be read, so the session cannot be derived yet.
pub const NO_SESSION: &str = "session: the organisation cannot be read";

impl Authorizer {
    /// Session derives the session of a caller from the current snapshot: the principal completed with its roles,
    /// what it may attempt, the structures and names. The graph being unreadable is an error ([`Error::NoSession`])
    /// rather than a session with no structures a client could not use.
    pub async fn session(&self, ctx: &Ctx, p: &Principal) -> Result<Session, Error> {
        let snap = self
            .dir
            .snapshot(ctx)
            .await
            .map_err(|e| Error::NoSession(Some(e.error)))?;
        let p = snap.enrich(p);
        let structures = snap.structures().clone();
        let mut s = Session {
            principal: p.clone(),
            names: names_of(&structures),
            structures,
            platform_roles: builtin_roles(),
            ..Session::default()
        };
        if p.anonymous() {
            return Ok(s);
        }
        s.can.administer = self
            .floor()
            .authorize(
                ctx,
                &Request {
                    subject: p.clone(),
                    action: "admin".into(),
                    resource: Resource {
                        r#type: "platform".into(),
                        ..Resource::default()
                    },
                },
            )
            .await?;
        let mut project = p.project.clone();
        if project.is_empty() {
            project = s.structures.project().root;
        }
        s.can.approve = snap.may_access_project(&p, &project);
        s.can.lower_criticality = self
            .authorize(
                ctx,
                &Request {
                    subject: p.clone(),
                    action: "lower-criticality".into(),
                    resource: Resource {
                        r#type: "change".into(),
                        project_id: p.project.clone(),
                        ..Resource::default()
                    },
                },
            )
            .await?;
        Ok(s)
    }
}
