//! The engine's one meeting point with who and where (ADR 0063).

use std::sync::Arc;

use async_trait::async_trait;
use goap_authz::{Authorizer, Principal, Request, Resource};
use goap_ctx::Ctx;
use goap_domain::ChangeID;
use goap_mcp::ToolInfo;
use serde_json::{Map, Value};

use crate::engine::Engine;
use crate::error::{Error, Result};
use crate::process::Process;

/// ProcessRef is what the Scope needs to know of a process: who started it and where it runs. A snapshot, so the
/// port does not depend on Process. Org and Project are the scope of its change; empty (a process with no change),
/// the services behind the scope resolve them to the roots of the structures (the engine names none, ADR 0054).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ProcessRef {
    pub id: String,
    pub methodology: String,
    pub change_id: ChangeID,
    pub initiator: Principal,
    /// Org is the organisation holding the change (a unit key): its adapters decide which MCPs are bound.
    pub org: String,
    /// Project is the project of the change: the roles its Assignments grant are the ones that count (ADR 0043).
    pub project: String,
}

impl ProcessRef {
    /// Actor is who the process acts as: its initiator on the project of the process, whatever project their token
    /// had active.
    pub fn actor(&self) -> Principal {
        let mut a = self.initiator.clone();
        a.project = self.project.clone();
        a
    }
}

/// StepRef is the step of a process a gate is about (ADR 0035 §2): its path and the roles that carry it out.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct StepRef {
    pub path: String,
    pub responsible: String,
    pub accountable: String,
}

/// Scope is the engine's one meeting point with who and where: the organisation and project a process works in, the
/// authorization model that says what a principal may do there, and the MCPs the organisation binds.
#[async_trait]
pub trait Scope: Send + Sync {
    /// As returns ctx acting as the actor of the process (ProcessRef::actor).
    fn as_actor(&self, ctx: &Ctx, p: &ProcessRef) -> Ctx;
    /// HasTools reports whether a hub of MCPs is available at all.
    fn has_tools(&self) -> bool;
    /// Tools lists the tools the organisation of the process binds and the MCPs it binds, as the actor.
    async fn tools(&self, ctx: &Ctx, p: &ProcessRef) -> Result<(Vec<ToolInfo>, Vec<String>)>;
    /// CallTool calls "<mcp>/<tool>" for the organisation of the process, as the actor, on the change of the process.
    async fn call_tool(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Value>;
    /// Allowed reports whether who holds a permission "<type>:<action>" on the process (or its change).
    async fn allowed(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        who: &Principal,
        permission: &str,
    ) -> Result<bool>;
    /// MayRun reports whether who holds, on the project of the process, one of the roles allowed to run an agent or
    /// an action (kind "agent" or "action"); none listed: any member of the project.
    async fn may_run(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        who: &Principal,
        kind: &str,
        name: &str,
        roles: &[String],
    ) -> Result<bool>;
    /// MayStep reports whether who may act ("perform": the responsible role, "approve": the accountable one) on a
    /// step of the process.
    async fn may_step(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        who: &Principal,
        s: &StepRef,
        act: &str,
    ) -> Result<bool>;
}

/// ToolPort is the MCP hub seen by AuthzScope (ADR 0019). Tools are those of the MCPs the organization of the change
/// binds; the call runs with the principal of ctx.
#[async_trait]
pub trait ToolPort: Send + Sync {
    /// CallTool calls "<mcp>/<tool>" for an organization.
    async fn call_tool(
        &self,
        ctx: &Ctx,
        org: &str,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Value>;
    /// Tools lists the tools available to an organization and the MCPs it binds.
    async fn tools(&self, ctx: &Ctx, org: &str) -> Result<(Vec<ToolInfo>, Vec<String>)>;
}

/// AuthzScope is the Scope of the platform: an ABAC authorizer and the MCP hub.
///
/// Convenience for tests and goap-dev paths: a None authz grants every permission and a None hub binds no MCP. This
/// is the only place that behaviour lives; the engine itself never tests for them.
#[derive(Clone, Default)]
pub struct AuthzScope {
    pub authz: Option<Arc<dyn Authorizer>>,
    pub hub: Option<Arc<dyn ToolPort>>,
}

#[async_trait]
impl Scope for AuthzScope {
    fn as_actor(&self, ctx: &Ctx, p: &ProcessRef) -> Ctx {
        goap_authz::with_principal(ctx, p.actor())
    }

    fn has_tools(&self) -> bool {
        self.hub.is_some()
    }

    async fn tools(&self, ctx: &Ctx, p: &ProcessRef) -> Result<(Vec<ToolInfo>, Vec<String>)> {
        match &self.hub {
            None => Ok((Vec::new(), Vec::new())),
            Some(h) => h.tools(&self.as_actor(ctx, p), &p.org).await,
        }
    }

    async fn call_tool(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        name: &str,
        args: Map<String, Value>,
    ) -> Result<Value> {
        let Some(h) = &self.hub else {
            return Err(Error::msg("no MCP hub configured"));
        };
        // the built-in connectors act on the change of the process by default (ADR 0028)
        let call = goap_mcp::with_call(
            &self.as_actor(ctx, p),
            goap_mcp::CallContext {
                change: p.change_id.to_string(),
                process: p.id.clone(),
                ..Default::default()
            },
        );
        h.call_tool(&call, &p.org, name, args).await
    }

    async fn allowed(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        who: &Principal,
        permission: &str,
    ) -> Result<bool> {
        let Some(a) = &self.authz else {
            return Ok(true);
        };
        let (typ, act) = goap_authz::parse_permission(permission)?;
        let mut res = Resource {
            r#type: typ.clone(),
            org: p.initiator.org.clone(),
            owner: p.initiator.subject.clone(),
            name: p.methodology.clone(),
            project_id: p.project.clone(),
            ..Default::default()
        };
        res.id = if typ == "change" {
            p.change_id.to_string()
        } else {
            p.id.clone()
        };
        Ok(a.authorize(
            ctx,
            &Request {
                subject: who.clone(),
                action: act,
                resource: res,
            },
        )
        .await?)
    }

    async fn may_run(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        who: &Principal,
        kind: &str,
        name: &str,
        roles: &[String],
    ) -> Result<bool> {
        let Some(a) = &self.authz else {
            return Ok(true);
        };
        Ok(a.authorize(
            ctx,
            &Request {
                subject: who.clone(),
                action: "run".into(),
                resource: Resource {
                    r#type: kind.into(),
                    id: p.id.clone(),
                    name: name.into(),
                    org: p.org.clone(),
                    project_id: p.project.clone(),
                    owner: p.initiator.subject.clone(),
                    roles: roles.to_vec(),
                    ..Default::default()
                },
            },
        )
        .await?)
    }

    async fn may_step(
        &self,
        ctx: &Ctx,
        p: &ProcessRef,
        who: &Principal,
        st: &StepRef,
        act: &str,
    ) -> Result<bool> {
        let Some(a) = &self.authz else {
            return Ok(true);
        };
        Ok(a.authorize(
            ctx,
            &Request {
                subject: who.clone(),
                action: act.into(),
                resource: Resource {
                    r#type: "step".into(),
                    id: p.id.clone(),
                    name: st.path.clone(),
                    org: p.org.clone(),
                    project_id: p.project.clone(),
                    owner: p.initiator.subject.clone(),
                    role: st.responsible.clone(),
                    accountable: st.accountable.clone(),
                    ..Default::default()
                },
            },
        )
        .await?)
    }
}

impl Engine {
    /// scope is the engine's Scope. Unset, an AuthzScope with neither authorizer nor hub: everything permitted, no
    /// MCP bound (tests and bare embeddings).
    pub(crate) fn scope(&self) -> Arc<dyn Scope> {
        match &self.scope {
            Some(s) => s.clone(),
            None => Arc::new(AuthzScope::default()),
        }
    }

    /// ref is the snapshot of p the scope works with.
    pub(crate) fn r#ref(&self, p: &Process) -> ProcessRef {
        ProcessRef {
            id: p.id.clone(),
            methodology: p.methodology.clone(),
            change_id: p.change_id.clone(),
            initiator: p.initiator.clone(),
            org: p.org.clone(),
            project: p.project.clone(),
        }
    }

    /// as is ctx acting as the actor of p.
    pub(crate) fn as_(&self, ctx: &Ctx, p: &Process) -> Ctx {
        self.scope().as_actor(ctx, &self.r#ref(p))
    }
}
