//! Port of `lifecycle.go`: the authorizers the compositions plug on the graph (transitions of nodes and changes,
//! items asking a permission, operations of the catalogue).

use std::sync::Arc;

use goap_authz::{Authorizer, Request, Resource, check, parse_permission, principal_from};
use goap_criticality::{DefaultResolver, Resolver};
use goap_ctx::Ctx;
use goap_domain::{Change, ChangeItem, ItemPermission, Node, Transition};
use goap_graph::{Error, Result};
use goap_risk::{DEROGATION_CLOSED, TYPE_DEROGATION};

use crate::boot::BoxFuture;

/// An authz error as an error of the graph, with its context (Go's `fmt.Errorf("<context>: %w", err)`).
pub(crate) fn authz_error(e: goap_authz::Error) -> Error {
    Error::other(e)
}

/// TransitionAuthorizer authorizes the lifecycle transitions of a change for the caller (ADR 0014). A transition
/// declares the permission it needs ("type:action"); by default it is node:transition. An unset authorizer grants
/// everything; an anonymous caller is refused by it, internal services act as a named system principal
/// (SYSTEM_PRINCIPAL).
pub fn transition_authorizer(
    a: Option<Arc<dyn Authorizer>>,
) -> Arc<dyn goap_graph::TransitionAuthorizer> {
    Arc::new(TransitionAuthz(a))
}

struct TransitionAuthz(Option<Arc<dyn Authorizer>>);

/// The resource type and action of the permission of a transition, by default `typ:action`.
fn permission_of(t: &Transition, typ: &str, action: &str) -> Result<(String, String)> {
    if t.permission.is_empty() {
        return Ok((typ.to_string(), action.to_string()));
    }
    parse_permission(&t.permission).map_err(authz_error)
}

#[async_trait::async_trait]
impl goap_graph::TransitionAuthorizer for TransitionAuthz {
    async fn authorize(&self, ctx: &Ctx, n: &Node, t: &Transition) -> Result<()> {
        let who = principal_from(ctx);
        let Some(a) = &self.0 else {
            return Ok(());
        };
        let (typ, action) = permission_of(t, "node", "transition")?;
        let resource = Resource {
            r#type: typ,
            id: n.id.to_string(),
            name: n.key.clone(),
            org: who.org.clone(),
            project_id: who.project.clone(),
            ..Default::default()
        };
        check(
            ctx,
            Some(a.as_ref()),
            &Request {
                subject: who,
                action,
                resource,
            },
        )
        .await
        .map_err(authz_error)
    }
}

/// ChangeTransitionAuthorizer authorizes the transitions of the lifecycle of a change for the caller (ADR 0058): the
/// permission a transition declares, by default change:transition, checked on the project of the change.
pub fn change_transition_authorizer(
    a: Option<Arc<dyn Authorizer>>,
) -> Arc<dyn goap_graph::ChangeTransitionAuthorizer> {
    Arc::new(ChangeTransitionAuthz(a))
}

struct ChangeTransitionAuthz(Option<Arc<dyn Authorizer>>);

#[async_trait::async_trait]
impl goap_graph::ChangeTransitionAuthorizer for ChangeTransitionAuthz {
    async fn authorize(&self, ctx: &Ctx, c: &Change, t: &Transition) -> Result<()> {
        let who = principal_from(ctx);
        let Some(a) = &self.0 else {
            return Ok(());
        };
        let (typ, action) = permission_of(t, "change", "transition")?;
        check(
            ctx,
            Some(a.as_ref()),
            &Request {
                subject: who,
                action,
                resource: change_resource(&typ, c),
            },
        )
        .await
        .map_err(authz_error)
    }
}

/// The resource of a change: its id, title, unit and project.
fn change_resource(typ: &str, c: &Change) -> Resource {
    Resource {
        r#type: typ.to_string(),
        id: c.id.to_string(),
        name: c.title.clone(),
        org: c.owner_org.clone(),
        project_id: c.project_id.clone(),
        ..Default::default()
    }
}

/// ItemAuthorizer authorizes the write of the items whose kind asks a permission of its writer
/// (domain::require_item_permission, ADR 0075: a derogation asks derogation:sign), checked on the project of the
/// change. When the kind names the field that holds who answers for the item, that must be the writer: one signs for
/// oneself. A platform service acting by itself (Principal::is_system) writes on behalf of the rules it applies (the
/// expiry of a derogation) and is not asked. An unset authorizer grants everything. A derogation also needs the role
/// its criticality level asks of a signatory (ADR 0075 §3, criticality::Policy::signatory_role, resolved by resolve;
/// unset: the compiled-in table), checked as derogation:sign-role.
pub fn item_authorizer(
    a: Option<Arc<dyn Authorizer>>,
    resolve: Option<Arc<dyn Resolver>>,
) -> Arc<dyn goap_graph::ItemAuthorizer> {
    Arc::new(ItemAuthz { a, resolve })
}

struct ItemAuthz {
    a: Option<Arc<dyn Authorizer>>,
    resolve: Option<Arc<dyn Resolver>>,
}

#[async_trait::async_trait]
impl goap_graph::ItemAuthorizer for ItemAuthz {
    async fn authorize(
        &self,
        ctx: &Ctx,
        c: &Change,
        it: &ChangeItem,
        p: &ItemPermission,
    ) -> Result<()> {
        let Some(a) = &self.a else {
            return Ok(());
        };
        let who = principal_from(ctx);
        if who.is_system() {
            return Ok(());
        }
        let (typ, action) = parse_permission(&p.permission).map_err(authz_error)?;
        check(
            ctx,
            Some(a.as_ref()),
            &Request {
                subject: who.clone(),
                action,
                resource: change_resource(&typ, c),
            },
        )
        .await
        .map_err(authz_error)?;
        if !p.subject_field.is_empty() {
            let s = it
                .data
                .get(&p.subject_field)
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default();
            if s != who.subject {
                return Err(
                    authz_error(goap_authz::Error::Forbidden(String::new())).context(format!(
                        "{} {s:?} must be the one who writes it ({})",
                        p.subject_field, who.subject
                    )),
                );
            }
        }
        if it.r#type == TYPE_DEROGATION {
            let st = it
                .data
                .get("status")
                .and_then(serde_json::Value::as_str)
                .unwrap_or_default();
            if st != DEROGATION_CLOSED {
                let lvl = goap_criticality::of(&c.data);
                let policy = match &self.resolve {
                    Some(r) => r.resolve(ctx, c, &lvl).await,
                    None => DefaultResolver.resolve(ctx, c, &lvl).await,
                };
                let role = policy.signatory_role;
                if !role.is_empty() {
                    let mut resource = change_resource("derogation", c);
                    resource.roles = vec![role.clone()];
                    check(
                        ctx,
                        Some(a.as_ref()),
                        &Request {
                            subject: who,
                            action: "sign-role".into(),
                            resource,
                        },
                    )
                    .await
                    .map_err(|e| {
                        authz_error(e).context(format!(
                            "a derogation on a {lvl} change is signed by the role {role}"
                        ))
                    })?;
                }
            }
        }
        Ok(())
    }
}

/// OperationRoles resolves the roles of a methodology (a name or a reference at a version,
/// domain::methodology_ref) that allow an operation (registrysvc's Service::operation_roles): (roles, restricted).
pub type OperationRoles = Arc<
    dyn for<'a> Fn(&'a Ctx, &'a str, &'a str) -> BoxFuture<'a, Result<(Vec<String>, bool)>>
        + Send
        + Sync,
>;

/// noRole is a role name no one holds: an operation the methodology allows to no role is left to the administrators
/// (mayRun with no role would let any member of the project in).
const NO_ROLE: &str = "-";

/// OperationAuthorizer authorizes the operations of the catalogue (goap-operations, ADR 0103) on a change for the
/// caller: when the methodology of the change restricts the operation to some of its roles, operation:perform is
/// checked on the project of the change with those roles (one of them, held there). A platform service acting by
/// itself is not asked; an unset authorizer or resolver grants everything.
pub fn operation_authorizer(
    a: Option<Arc<dyn Authorizer>>,
    roles: Option<OperationRoles>,
) -> Arc<dyn goap_graph::OperationAuthorizer> {
    Arc::new(OperationAuthz { a, roles })
}

struct OperationAuthz {
    a: Option<Arc<dyn Authorizer>>,
    roles: Option<OperationRoles>,
}

#[async_trait::async_trait]
impl goap_graph::OperationAuthorizer for OperationAuthz {
    async fn authorize(&self, ctx: &Ctx, c: &Change, op: &str) -> Result<()> {
        let who = principal_from(ctx);
        let (Some(a), Some(roles)) = (&self.a, &self.roles) else {
            return Ok(());
        };
        if who.is_system() {
            return Ok(());
        }
        let methodology = c.methodology_ref();
        let (mut allowed, restricted) = roles(ctx, &methodology, op).await?;
        if !restricted {
            return Ok(());
        }
        if allowed.is_empty() {
            allowed = vec![NO_ROLE.to_string()];
        }
        let mut resource = change_resource("operation", c);
        resource.name = op.to_string();
        resource.roles = allowed.clone();
        check(
            ctx,
            Some(a.as_ref()),
            &Request {
                subject: who,
                action: "perform".into(),
                resource,
            },
        )
        .await
        .map_err(|e| {
            authz_error(e).context(format!(
                "{op} on change {} needs one of the roles [{}] of methodology {}",
                c.id,
                allowed.join(" "),
                c.methodology
            ))
        })
    }
}
