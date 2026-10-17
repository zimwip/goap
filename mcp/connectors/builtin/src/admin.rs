//! The goap-admin connector (`admin.go`).

use std::collections::HashMap;

use goap_access as access;
use goap_adapter as adapter;
use goap_authz as authz;
use goap_connectorkit::{Connector, InvokeError};
use goap_ctx::Ctx;
use goap_def as def;
use goap_domain::{self as domain, Node, NodeRef};
use goap_gen::connector::v1::ConnectorInfo;
use goap_gen::registry::v1::{ListMethodologiesRequest, MethodologySummary};
use goap_mcpbuiltin as mcpbuiltin;
use goap_mcpsvc as mcpsvc;
use serde_json::{Map, Value};

use crate::builtin::{
    Args, Error, Methodologies, Op, Ports, boxed, caller, info, json, obj, op, schema, unknown,
};

/// Hub is the part of the MCP hub goap-admin reads (`mcpsvc::Service`).
#[async_trait::async_trait]
pub trait Hub: Send + Sync {
    async fn effective(
        &self,
        ctx: &Ctx,
        unit: &str,
    ) -> Result<(Vec<String>, Vec<mcpsvc::Effective>), mcpsvc::Error>;
    async fn connectors(&self, ctx: &Ctx) -> Result<Vec<mcpsvc::ConnectorView>, mcpsvc::Error>;
    async fn connector_of(&self, ctx: &Ctx, a: &adapter::Instance) -> String;
}

#[async_trait::async_trait]
impl Hub for mcpsvc::Service {
    async fn effective(
        &self,
        ctx: &Ctx,
        unit: &str,
    ) -> Result<(Vec<String>, Vec<mcpsvc::Effective>), mcpsvc::Error> {
        mcpsvc::Service::effective(self, ctx, unit).await
    }
    async fn connectors(&self, ctx: &Ctx) -> Result<Vec<mcpsvc::ConnectorView>, mcpsvc::Error> {
        mcpsvc::Service::connectors(self, ctx).await
    }
    async fn connector_of(&self, ctx: &Ctx, a: &adapter::Instance) -> String {
        mcpsvc::Service::connector_of(self, ctx, a).await
    }
}

/// Domains lists the domains in force: the domain registry (its service in process, `goap_domainclient::Client`).
#[async_trait::async_trait]
pub trait Domains: Send + Sync {
    async fn domains(
        &self,
        ctx: &Ctx,
    ) -> Result<Vec<def::Domain>, Box<dyn std::error::Error + Send + Sync>>;
}

#[async_trait::async_trait]
impl Domains for goap_domainclient::Client {
    async fn domains(
        &self,
        ctx: &Ctx,
    ) -> Result<Vec<def::Domain>, Box<dyn std::error::Error + Send + Sync>> {
        Ok(goap_domainclient::Client::domains(self, ctx).await?)
    }
}

/// inForce lists the published methodologies the registry does not suspend (ADR 0125 §3).
pub(crate) async fn in_force(
    ctx: &Ctx,
    m: &Methodologies,
) -> Result<Vec<MethodologySummary>, Error> {
    let r = m
        .list_methodologies(
            ctx,
            goap_connect::Request::new(ListMethodologiesRequest::default()),
        )
        .await?;
    Ok(r.msg
        .methodologies
        .into_iter()
        .filter(|s| s.status == def::STATUS_PUBLISHED.as_str() && s.suspended.is_empty())
        .collect())
}

/// Admin is the goap-admin connector: it describes the platform (who, with what, what, how). It changes nothing: the
/// organisation, the adapters and the methodologies are graph data, changed through changes (goap-change).
#[derive(Clone)]
pub struct Admin {
    p: Ports,
}

fn admin_ops() -> Vec<Op> {
    vec![
        op(
            "units",
            "List the organisational units: {units}",
            schema(&[], &[]),
        ),
        op(
            "users",
            "List the users: {users}",
            schema(&[("unit", "string")], &[]),
        ),
        op(
            "mcps",
            "List the MCPs a unit can use: {unit, chain, mcps}",
            schema(&[("unit", "string")], &[]),
        ),
        op(
            "connectors",
            "List the registered connectors: {connectors}",
            schema(&[], &[]),
        ),
        op(
            "domains",
            "List the published domains: {domains}",
            schema(&[], &[]),
        ),
        op(
            "methodologies",
            "List the published methodologies: {methodologies}",
            schema(&[], &[]),
        ),
    ]
}

/// The resource of each operation, read-authorized for the caller.
fn admin_resource(op: &str) -> Option<&'static str> {
    Some(match op {
        "units" => "unit",
        "users" => access::RESOURCE_POLICY,
        "mcps" => "adapter",
        "connectors" => "connector",
        "domains" => "domain",
        "methodologies" => "methodology",
        _ => return None,
    })
}

fn sort_key(m: &Value) -> &str {
    if let Some(Value::String(k)) = m.get("key") {
        return k;
    }
    m.get("subject").and_then(Value::as_str).unwrap_or("")
}

impl Admin {
    pub fn new(p: Ports) -> Self {
        Admin { p }
    }

    fn hub(&self) -> Result<&dyn Hub, Error> {
        self.p
            .hub
            .as_deref()
            .ok_or_else(|| Error::msg("goap-admin has no hub"))
    }

    /// Invoke without the boxing of the connector's error: the operation's result or its typed error.
    pub async fn call(
        &self,
        ctx: &Ctx,
        op: &str,
        raw: &Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        let who = caller(ctx)?;
        let Some(res) = admin_resource(op) else {
            return Err(unknown(op));
        };
        authz::check(
            ctx,
            self.p.authz(),
            &authz::Request {
                subject: who.clone(),
                action: "read".into(),
                resource: authz::Resource {
                    r#type: res.into(),
                    org: who.org.clone(),
                    ..Default::default()
                },
            },
        )
        .await?;
        let a = Args(raw);
        match op {
            "units" | "users" => self.organisation(ctx, op, &a.str("unit")).await,
            "mcps" => {
                let mut unit = a.unit(ctx, &who);
                if unit.is_empty() {
                    // the root of the organisation, as the structures name it
                    unit = self.p.graph()?.structures(ctx).await?.organisation().root;
                }
                let hub = self.hub()?;
                let (chain, eff) = hub.effective(ctx, &unit).await?;
                let mut list = Vec::new();
                for e in &eff {
                    let tools: Vec<Value> =
                        e.allowed().tools.iter().map(|t| json(&t.name)).collect();
                    list.push(Value::Object(obj([
                        ("mcp", json(&e.mcp.name)),
                        ("description", json(&e.mcp.description)),
                        ("builtin", json(mcpbuiltin::is(&e.mcp.name))),
                        ("adapter", json(&e.adapter.adapter)),
                        ("definedIn", json(&e.adapter.unit)),
                        ("inherited", json(e.inherited)),
                        ("connector", json(hub.connector_of(ctx, &e.adapter).await)),
                        ("tools", Value::Array(tools)),
                        ("restrictedBy", crate::builtin::nullable(&e.restriction.by)),
                        ("disabled", json(e.restriction.disabled)),
                    ])));
                }
                Ok(obj([
                    ("unit", json(&unit)),
                    ("chain", crate::builtin::nullable(&chain)),
                    ("mcps", Value::Array(list)),
                ]))
            }
            "connectors" => {
                let cs = self.hub()?.connectors(ctx).await?;
                let list: Vec<Value> = cs
                    .iter()
                    .map(|r| {
                        let ops: Vec<Value> =
                            r.info.operations.iter().map(|o| json(&o.name)).collect();
                        Value::Object(obj([
                            ("id", json(&r.info.id)),
                            ("version", json(&r.info.version)),
                            ("description", json(&r.info.description)),
                            ("operations", Value::Array(ops)),
                            ("live", json(r.live)),
                            ("lastSeen", json(domain::gotime::format(&r.last_seen))),
                            ("builtin", json(mcpbuiltin::is(&r.info.id))),
                        ]))
                    })
                    .collect();
                Ok(obj([("connectors", Value::Array(list))]))
            }
            "domains" => {
                let ds = self
                    .p
                    .domains
                    .as_deref()
                    .ok_or_else(|| Error::msg("goap-admin has no domain registry"))?
                    .domains(ctx)
                    .await
                    .map_err(Error::Domains)?;
                let list: Vec<Value> = ds
                    .iter()
                    .map(|d| {
                        let types: Vec<Value> = d
                            .node_types
                            .iter()
                            .map(|t| json(format!("{}@{}", d.name, t.name)))
                            .collect();
                        Value::Object(obj([
                            ("name", json(&d.name)),
                            ("version", json(&d.version)),
                            ("description", json(&d.description)),
                            ("nodeTypes", Value::Array(types)),
                        ]))
                    })
                    .collect();
                Ok(obj([("domains", Value::Array(list))]))
            }
            "methodologies" => {
                let m = self
                    .p
                    .methodologies
                    .as_deref()
                    .ok_or_else(|| Error::msg("goap-admin has no methodology registry"))?;
                let ms = in_force(ctx, m).await?;
                let list: Vec<Value> = ms
                    .iter()
                    .map(|m| {
                        let agents: Vec<Value> = m
                            .agents
                            .iter()
                            .map(|ag| {
                                Value::Object(obj([
                                    ("name", json(&ag.name)),
                                    ("description", json(&ag.description)),
                                    ("planner", json(&ag.planner)),
                                ]))
                            })
                            .collect();
                        Value::Object(obj([
                            ("name", json(&m.name)),
                            ("version", json(&m.version)),
                            ("description", json(&m.description)),
                            ("namespace", json(&m.namespace)),
                            ("agents", Value::Array(agents)),
                        ]))
                    })
                    .collect();
                Ok(obj([("methodologies", Value::Array(list))]))
            }
            _ => Err(unknown(op)),
        }
    }

    /// organisation lists the units or the users of the head of the organisation namespace: the units are the nodes
    /// of the organisation structure the graph names (ADR 0054), their hierarchy and the root every unit hangs from
    /// are read through domain::Hierarchy, the one resolver.
    async fn organisation(
        &self,
        ctx: &Ctx,
        op: &str,
        unit: &str,
    ) -> Result<Map<String, Value>, Error> {
        let g = self.p.graph()?;
        let st = g.structures(ctx).await?;
        let org = st.organisation();
        let head = g
            .branch_head(ctx, &org.namespace, domain::MAIN_BRANCH)
            .await?;
        let (nodes, links) = g.baseline_graph(ctx, &head.id).await?;
        let units = st.hierarchy(domain::STRUCTURE_ORGANISATION, &nodes, &links);
        let by_ref: HashMap<NodeRef, &Node> = nodes.iter().map(|n| (n.r#ref(), n)).collect();
        let mut member: HashMap<&str, &str> = HashMap::new();
        for l in &links {
            if let Some(from) = by_ref.get(&l.from)
                && l.r#type == access::LINK_MEMBER_OF
                && from.r#type == access::NODE_TYPE_USER
            {
                member.insert(&from.key, by_ref.get(&l.to).map_or("", |n| n.key.as_str()));
            }
        }
        let mut list: Vec<Value> = Vec::new();
        for n in &nodes {
            if n.deleted {
                continue;
            }
            if op == "units" && n.r#type == org.r#type {
                let mut u = obj([
                    ("key", json(&n.key)),
                    (
                        "name",
                        n.properties.get("name").cloned().unwrap_or(Value::Null),
                    ),
                    (
                        "kind",
                        n.properties.get("kind").cloned().unwrap_or(Value::Null),
                    ),
                ]);
                let p = units.parent(&n.key);
                if !p.is_empty() {
                    u.insert("parent".into(), json(p));
                } else if n.key != units.root() {
                    // the implicit root of every unit
                    u.insert("parent".into(), json(units.root()));
                }
                list.push(Value::Object(u));
            } else if op == "users" && n.r#type == access::NODE_TYPE_USER {
                let m = member.get(n.key.as_str()).copied().unwrap_or("");
                if !unit.is_empty() && m != unit {
                    continue;
                }
                let Ok(usr) = access::user_from_props(&n.properties) else {
                    continue;
                };
                list.push(Value::Object(obj([
                    ("subject", json(&usr.subject)),
                    ("displayName", json(&usr.display_name)),
                    ("email", json(&usr.email)),
                    ("unit", json(m)),
                ])));
            }
        }
        list.sort_by(|x, y| sort_key(x).cmp(sort_key(y)));
        let mut out = Map::new();
        out.insert(op.into(), Value::Array(list));
        Ok(out)
    }
}

#[async_trait::async_trait]
impl Connector for Admin {
    fn info(&self) -> ConnectorInfo {
        info(
            mcpbuiltin::ADMIN,
            "Description of the platform: organisation, users, MCPs, connectors, domains, methodologies (built in).",
            &admin_ops(),
        )
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        _config: Map<String, Value>,
        _secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        self.call(ctx, op, &args).await.map_err(boxed)
    }
}
