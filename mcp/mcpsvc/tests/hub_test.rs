//! The resolution and the call of tools along the unit chain (no Go test of the package covers them: these check
//! the port end to end over an in-memory graph and an in-process connector).

use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use goap_adapter as adapter;
use goap_algo as algo;
use goap_authz::Principal;
use goap_connect::Request;
use goap_connectorkit::{Connector, InvokeError};
use goap_ctx::Ctx;
use goap_domain::{
    Baseline, BaselineID, Link, Node, NodeID, NodeRef, STRUCTURE_ORGANISATION, Structure,
    StructureSet, Structures, TYPE_ADAPTER, TYPE_ADAPTER_DEF,
};
use goap_gen::connector::v1::{ConnectorInfo, Operation};
use goap_gen::mcp::v1::{self as mcpv1, McpService};
use goap_graphapi::Error as GraphError;
use goap_mcp as mcp;
use goap_mcpsvc::{
    Baselines, Directory, Handler, InprocInvoker, MemoryStore, Service, build_snapshot,
};
use serde_json::{Map, Value, json};

const ORG_UNIT: &str = "organisation@OrgUnit";
const PART_OF: &str = "organisation@part_of";

fn structures() -> Structures {
    Structures(vec![StructureSet {
        structure: Structure {
            kind: STRUCTURE_ORGANISATION.into(),
            r#type: ORG_UNIT.into(),
            namespace: "organisation".into(),
            parent: PART_OF.into(),
            root: "ORG-DEFAULT".into(),
            ..Default::default()
        },
        types: vec![ORG_UNIT.into()],
    }])
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

fn node(id: &str, ns: &str, typ: &str, key: &str, owner: &str, props: Map<String, Value>) -> Node {
    Node {
        id: NodeID::from(id),
        version: 1,
        namespace: ns.into(),
        key: key.into(),
        r#type: typ.into(),
        owner: NodeID::from(owner),
        properties: props,
        ..Default::default()
    }
}

fn graph() -> (Vec<Node>, Vec<Link>) {
    let echo = mcp::Def {
        name: "echo".into(),
        description: "Echo".into(),
        tools: vec![
            mcp::Tool {
                name: "say".into(),
                read_only: true,
                input_schema: obj(json!({"type": "object", "required": ["text"]})),
                ..Default::default()
            },
            mcp::Tool {
                name: "shout".into(),
                ..Default::default()
            },
        ],
        ..Default::default()
    };
    let def = adapter::Def {
        name: "echo-adapter".into(),
        mcp: "echo".into(),
        connector: "echo".into(),
        language: algo::JAVASCRIPT.into(),
        code: r#"var r = ctx.call("echo", {text: ctx.args().text, prefix: ctx.param("prefix")}); return {said: r.text, tool: ctx.tool()};"#.into(),
        params: vec![
            algo::Param {
                name: "prefix".into(),
                r#type: "string".into(),
                ..Default::default()
            },
            algo::Param {
                name: "token".into(),
                r#type: adapter::PARAM_SECRET.into(),
                ..Default::default()
            },
        ],
        ..Default::default()
    };
    let inst = adapter::Instance {
        mcp: "echo".into(),
        adapter: "echo-adapter".into(),
        params: obj(json!({"prefix": ">", "token": "env:ECHO_TOKEN"})),
        ..Default::default()
    };
    // U2 restricts the MCP to read-only tools
    let restrict = adapter::Instance {
        mcp: "echo".into(),
        read_only: true,
        ..Default::default()
    };
    let nodes = vec![
        node(
            "n-root",
            "organisation",
            ORG_UNIT,
            "ORG-DEFAULT",
            "n-root",
            Map::new(),
        ),
        node("n-u1", "organisation", ORG_UNIT, "U1", "n-root", Map::new()),
        node("n-u2", "organisation", ORG_UNIT, "U2", "n-root", Map::new()),
        node(
            "n-mcp",
            "platform",
            mcp::NODE_TYPE_MCP,
            "MCP:echo",
            "n-root",
            echo.props(),
        ),
        node(
            "n-def",
            "platform",
            TYPE_ADAPTER_DEF,
            "AD:echo-adapter",
            "n-root",
            def.props(),
        ),
        node(
            "n-a",
            "organisation",
            TYPE_ADAPTER,
            "A:ORG-DEFAULT:echo",
            "n-root",
            inst.props(),
        ),
        node(
            "n-r",
            "organisation",
            TYPE_ADAPTER,
            "A:U2:echo",
            "n-u2",
            restrict.props(),
        ),
        // an adapter whose owner is no unit
        node(
            "n-bad",
            "organisation",
            TYPE_ADAPTER,
            "A:X:echo",
            "n-mcp",
            inst.props(),
        ),
    ];
    let link = |id: &str, from: &str, to: &str| Link {
        id: id.into(),
        r#type: PART_OF.into(),
        from: NodeRef {
            id: NodeID::from(from),
            version: 1,
        },
        to: NodeRef {
            id: NodeID::from(to),
            version: 1,
        },
        ..Default::default()
    };
    (
        nodes,
        vec![link("l1", "n-u1", "n-root"), link("l2", "n-u2", "n-u1")],
    )
}

struct FakeGraph;

#[async_trait]
impl goap_graphsnap::Graph for FakeGraph {
    async fn branch_head(
        &self,
        _ctx: &Ctx,
        namespace: &str,
        _name: &str,
    ) -> Result<Baseline, GraphError> {
        Ok(Baseline {
            id: BaselineID::from(format!("B-{namespace}")),
            ..Default::default()
        })
    }

    async fn baseline_graph(
        &self,
        _ctx: &Ctx,
        id: &BaselineID,
    ) -> Result<(Vec<Node>, Vec<Link>), GraphError> {
        let (nodes, links) = graph();
        let ns = id.trim_start_matches("B-").to_string();
        Ok((
            nodes.into_iter().filter(|n| n.namespace == ns).collect(),
            if ns == "organisation" { links } else { vec![] },
        ))
    }
}

#[async_trait]
impl goap_access::Graph for FakeGraph {
    async fn structures(&self, _ctx: &Ctx) -> Result<Structures, GraphError> {
        Ok(structures())
    }
}

struct Echo;

#[async_trait]
impl Connector for Echo {
    fn info(&self) -> ConnectorInfo {
        ConnectorInfo {
            id: "echo".into(),
            secret_names: vec!["token".into()],
            operations: vec![Operation {
                name: "echo".into(),
                ..Default::default()
            }],
            ..Default::default()
        }
    }

    async fn invoke(
        &self,
        _ctx: &Ctx,
        _op: &str,
        args: Map<String, Value>,
        config: Map<String, Value>,
        secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        assert_eq!(config.get("prefix"), Some(&json!(">")));
        assert!(
            !config.contains_key("token"),
            "a secret is no configuration"
        );
        assert!(secrets.is_empty(), "no resolver: no secret");
        let text = args.get("text").and_then(Value::as_str).unwrap_or_default();
        if text == "boom" {
            return Err("the connector failed".into());
        }
        Ok(obj(
            json!({"text": format!("{}{text}", args["prefix"].as_str().unwrap_or_default())}),
        ))
    }
}

async fn service() -> Arc<Service> {
    let ctx = Ctx::background();
    let mut s = Service::new(Arc::new(MemoryStore::new()));
    s.directory = Some(Arc::new(Directory::new(Arc::new(FakeGraph))));
    let mut connectors: HashMap<String, Arc<dyn Connector>> = HashMap::new();
    connectors.insert("echo".into(), Arc::new(Echo));
    s.invoker = Some(Arc::new(InprocInvoker {
        connectors: connectors.clone(),
        remote: None,
    }));
    let s = Arc::new(s);
    s.keep_registered(&ctx, connectors).await;
    s
}

#[test]
fn snapshot_resolves_along_the_chain() {
    let (nodes, links) = graph();
    let snap = build_snapshot(&structures(), Baselines::default(), &nodes, &links);
    assert_eq!(snap.chain("U2"), ["U2", "U1", "ORG-DEFAULT"]);
    assert_eq!(snap.unit(""), "ORG-DEFAULT");
    let (a, inherited) = snap.resolve("U2", "echo").expect("resolved");
    assert!(inherited && a.unit == "ORG-DEFAULT", "{a:?}");
    assert!(!snap.resolve("ORG-DEFAULT", "echo").unwrap().1);
    let eff = snap.effective("U2");
    assert_eq!(eff.len(), 1);
    assert_eq!(eff[0].restriction.by, ["U2"]);
    let allowed: Vec<String> = eff[0].allowed().tools.into_iter().map(|t| t.name).collect();
    assert_eq!(allowed, ["say"]);
    assert_eq!(snap.effective("U1")[0].allowed().tools.len(), 2);
    assert_eq!(
        snap.problems,
        ["A:X:echo: its owner n-mcp is not a unit of the organisation"]
    );
}

#[tokio::test]
async fn call_runs_the_adapter() {
    let ctx = Ctx::background();
    let s = service().await;
    let out = s
        .call(&ctx, "U1", "echo/say", obj(json!({"text": "hi"})))
        .await
        .unwrap();
    assert_eq!(Value::Object(out), json!({"said": ">hi", "tool": "say"}));
    // the connector's failure is the tool's
    let err = s
        .call(&ctx, "U1", "echo/say", obj(json!({"text": "boom"})))
        .await
        .unwrap_err();
    assert!(
        err.as_tool()
            .is_some_and(|t| t.msg.contains("the connector failed")),
        "{err}"
    );
    // a restricted tool
    let err = s
        .call(&ctx, "U2", "echo/shout", Map::new())
        .await
        .unwrap_err();
    assert!(err.is_not_bound(), "{err}");
    assert_eq!(
        err.to_string(),
        "tool echo/shout is restricted for U2 (by U2): mcp not bound"
    );
    // unknown MCP, tool, missing argument
    assert!(
        s.call(&ctx, "U1", "nope/x", Map::new())
            .await
            .unwrap_err()
            .is_not_bound()
    );
    assert!(
        s.call(&ctx, "U1", "echo/x", Map::new())
            .await
            .unwrap_err()
            .is_not_found()
    );
    assert!(
        s.call(&ctx, "U1", "echo/say", Map::new())
            .await
            .unwrap_err()
            .is_invalid()
    );
    let (tools, mcps) = s.tools(&ctx, "U2").await.unwrap();
    assert_eq!(mcps, ["echo"]);
    assert_eq!(tools.len(), 1);
    assert_eq!(tools[0].name, "echo/say");
    assert_eq!(tools[0].scope, "both");
    let warnings = s
        .check_adapter(
            &ctx,
            &adapter::Instance {
                unit: "U1".into(),
                mcp: "echo".into(),
                adapter: "echo-adapter".into(),
                params: obj(json!({"token": "plain"})),
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(
        warnings,
        [r#"secret token: "plain" is neither env:<VAR> nor <vault path>#<field>"#]
    );
    let (code, params) = s.template(&ctx, "echo", "echo").await.unwrap();
    assert!(!code.is_empty());
    assert_eq!(
        params.iter().map(|p| p.name.as_str()).collect::<Vec<_>>(),
        ["token"]
    );
}

fn principal(org: &str, roles: &[&str]) -> Principal {
    Principal {
        subject: "alice".into(),
        org: org.into(),
        roles: roles.iter().map(|r| r.to_string()).collect(),
        ..Default::default()
    }
}

/// The identity headers of a caller.
fn as_user(org: &str, roles: &[&str]) -> http::HeaderMap {
    let mut h = http::HeaderMap::new();
    goap_identity::set_headers(&principal(org, roles), &mut h);
    h
}

#[tokio::test]
async fn handler_checks_the_unit() {
    let ctx = Ctx::background();
    let s = service().await;
    let h = Handler::new(s);
    // a member of U1 may act for U2 (below) but not U1 for a member of U2
    let in_u1 = as_user("U1", &[]);
    assert_eq!(h.unit_of(&ctx, &in_u1, "U2").await.unwrap(), "U2");
    assert_eq!(h.unit_of(&ctx, &in_u1, "").await.unwrap(), "U1");
    let err = h
        .unit_of(&ctx, &as_user("U2", &[]), "U1")
        .await
        .unwrap_err();
    assert_eq!(err.code(), goap_connect::Code::PermissionDenied);
    assert_eq!(
        err.message(),
        "unit U1 is not within the unit of the caller: permission denied"
    );
    let admin = as_user("U2", &["admin"]);
    assert_eq!(h.unit_of(&ctx, &admin, "U1").await.unwrap(), "U1");

    // the identity of the request is the extractor's: the default principal stands for it here
    let mut h2 = h.clone();
    h2.identity.default = Some(principal("U2", &[]));
    let res = h2
        .call_tool(
            &ctx,
            Request::new(mcpv1::CallToolRequest {
                unit: "U2".into(),
                name: "echo/say".into(),
                arguments: goap_pbconv::to_struct(&obj(json!({"text": "boom"}))),
                ..Default::default()
            }),
        )
        .await
        .unwrap();
    assert!(
        res.msg.is_error && res.msg.error.contains("the connector failed"),
        "{:?}",
        res.msg
    );
    let err = h2
        .call_tool(
            &ctx,
            Request::new(mcpv1::CallToolRequest {
                unit: "U1".into(),
                name: "echo/say".into(),
                ..Default::default()
            }),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), goap_connect::Code::PermissionDenied);
    // registration needs the connector token
    let err = h
        .register_connector(
            &ctx,
            Request::new(mcpv1::RegisterConnectorRequest::default()),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), goap_connect::Code::PermissionDenied);
    let eff = h2
        .list_effective(&ctx, Request::new(mcpv1::ListEffectiveRequest::default()))
        .await
        .unwrap();
    assert_eq!(eff.msg.chain, ["U2", "U1", "ORG-DEFAULT"]);
    assert_eq!(eff.msg.mcps[0].connector, "echo");
    assert_eq!(eff.msg.mcps[0].allowed_tools, ["say"]);
}
