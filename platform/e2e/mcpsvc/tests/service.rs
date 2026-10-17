//! The MCP hub over a real graph seeded by the graph service (ADR 0125: the hub reads the graph through its client).

mod common;

use std::sync::Arc;
use std::time::Duration;

use chrono::Utc;
use common::*;
use goap_access as access;
use goap_adapter as adapter;
use goap_algo as algo;
use goap_authz::Principal;
use goap_gen::connector::v1::InvokeResponse;
use goap_graph::{Graph, Memory};
use goap_mcp as mcp;
use goap_mcpsvc::{Directory, ModelEndpoint, Service};
use serde_json::{Map, Value, json};

fn read_args() -> Map<String, Value> {
    obj(json!({"path": "n.txt"}))
}

fn string_param(name: &str, required: bool, default: Option<Value>) -> algo::Param {
    algo::Param {
        name: name.into(),
        r#type: algo::PARAM_STRING.into(),
        required,
        default,
        ..Default::default()
    }
}

fn secret_param(name: &str, required: bool) -> algo::Param {
    algo::Param {
        name: name.into(),
        r#type: adapter::PARAM_SECRET.into(),
        required,
        ..Default::default()
    }
}

fn instance(unit: &str, mcp_name: &str, adapter_name: &str, params: Value) -> adapter::Instance {
    adapter::Instance {
        unit: unit.into(),
        mcp: mcp_name.into(),
        adapter: adapter_name.into(),
        params: obj(params),
        ..Default::default()
    }
}

// The same adapter of the library and the same connector, instantiated by each unit with its own parameter values; the
// nearest unit's instance wins, and every unit falls back on the default organisation.
#[tokio::test]
async fn nearest_adapter_wins() {
    let inv = FakeInvoker::answering(json!({"text": "hi"}));
    let svc = new_hub(&world().await, inv.clone(), &[]).await;
    for (unit, want_root) in [
        ("ORG-A", "/a"),             // its own instance
        ("ORG-A1", "/a"),            // inherited from ORG-A, not from the default organisation
        ("ORG-B", "/default"),       // no ancestor: the default organisation
        ("", "/default"),            // a change naming no unit
        ("ORG-UNKNOWN", "/default"), // a unit the graph does not know
        (access::DEFAULT_ORG, "/default"),
    ] {
        let out = svc
            .call(&ctx(), unit, "document-repository/read", read_args())
            .await
            .unwrap_or_else(|e| panic!("{unit:?}: {e}"));
        assert_eq!(out["text"], "hi", "{unit:?}: {out:?}");
        assert_eq!(inv.last_config()["root"], want_root, "{unit:?}: root");
        let last = inv.last();
        assert!(
            last.operation == "read_file"
                && goap_pbconv::to_map(last.arguments.as_ref())["path"] == "n.txt",
            "{unit:?}: call = {last:?}"
        );
    }

    let (chain, eff) = svc.effective(&ctx(), "ORG-A1").await.unwrap();
    assert_eq!(chain, ["ORG-A1", "ORG-A", access::DEFAULT_ORG], "chain");
    assert!(
        eff.len() == 1
            && eff[0].adapter.unit == "ORG-A"
            && eff[0].inherited
            && svc.connector_of(&ctx(), &eff[0].adapter).await == "localfs",
        "effective = {eff:?}"
    );
    let (_, eff) = svc.effective(&ctx(), "ORG-A").await.unwrap();
    assert!(!eff[0].inherited, "an own adapter is reported as inherited");

    let (tools, mcps) = svc.tools(&ctx(), "ORG-B").await.unwrap();
    assert!(
        tools.len() == 3 && mcps.len() == 1 && tools[0].name == "document-repository/list",
        "tools = {tools:?} {mcps:?}"
    );
}

// The adapter is code: it maps the tools the MCP expects onto the operations the connector exposes.
#[tokio::test]
async fn the_adapter_code_maps_the_mcp_onto_the_connector() {
    let g = world().await;
    let inv = FakeInvoker::answering(json!({"text": "raw"}));
    let custom = adapter::Def {
        name: "shouting-docs".into(),
        language: algo::JAVASCRIPT.into(),
        mcp: "document-repository".into(),
        connector: "localfs".into(),
        params: vec![
            string_param("root", true, None),
            string_param("prefix", false, Some(json!(">"))),
        ],
        code: r#"
			if (ctx.tool() === "read") {
				const r = ctx.call("read_file", { path: ctx.param("prefix") + ctx.args().path });
				return { text: r.text.toUpperCase() };
			}
			if (ctx.tool() === "list") {
				// several calls, reshaped result
				const a = ctx.call("list_dir", { path: "" });
				const b = ctx.call("list_dir", { path: "docs" });
				return { calls: ctx.operations().length };
			}
			ctx.fail("write is not allowed here: " + ctx.tool());"#
            .into(),
        ..Default::default()
    };
    let svc = new_hub(&g, inv.clone(), &[custom]).await;
    seed_adapter(
        &g,
        &instance(
            "ORG-B",
            "document-repository",
            "shouting-docs",
            json!({"root": "/b"}),
        ),
    )
    .await;
    let out = svc
        .call(
            &ctx(),
            "ORG-B",
            "document-repository/read",
            obj(json!({"path": "f.txt"})),
        )
        .await
        .unwrap();
    assert_eq!(out["text"], "RAW", "read = {out:?}");
    // the default of the parameter
    assert_eq!(
        goap_pbconv::to_map(inv.last().arguments.as_ref())["path"],
        ">f.txt"
    );
    let out = svc
        .call(&ctx(), "ORG-B", "document-repository/list", Map::new())
        .await
        .unwrap();
    assert!(
        out.get("calls").is_some_and(|v| !v.is_null()),
        "list = {out:?}"
    );
    let err = svc
        .call(
            &ctx(),
            "ORG-B",
            "document-repository/write",
            obj(json!({"path": "x", "content": "y"})),
        )
        .await
        .unwrap_err();
    assert!(
        err.as_tool().is_some_and(|t| t.msg.contains("not allowed")),
        "rejected by the code = {err}"
    );
    // units without their own instance keep using the library adapter of the default organisation
    svc.call(
        &ctx(),
        "ORG-A",
        "document-repository/read",
        obj(json!({"path": "f.txt"})),
    )
    .await
    .unwrap();
}

#[tokio::test]
async fn call_errors() {
    let inv = FakeInvoker::answering(json!({"text": "hi"}));
    let svc = new_hub(&world().await, inv.clone(), &[]).await;

    let err = svc
        .call(&ctx(), "ORG-A", "ticketing/create", Map::new())
        .await
        .unwrap_err();
    assert!(err.is_not_bound(), "unknown MCP = {err}");
    let err = svc
        .call(&ctx(), "ORG-A", "document-repository/delete", Map::new())
        .await
        .unwrap_err();
    assert!(err.is_not_found(), "unknown tool = {err}");
    let err = svc
        .call(&ctx(), "ORG-A", "document-repository/read", Map::new())
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "missing argument = {err}");
    let err = svc
        .call(&ctx(), "ORG-A", "read", Map::new())
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "unqualified tool = {err}");
    // failures reported by the connector reach the caller as tool errors
    inv.answer.lock().resp = Some(InvokeResponse {
        is_error: true,
        error: "no such file".into(),
        ..Default::default()
    });
    let err = svc
        .call(&ctx(), "ORG-A", "document-repository/read", read_args())
        .await
        .unwrap_err();
    assert!(
        err.as_tool()
            .is_some_and(|t| t.msg.contains("no such file")),
        "connector error = {err}"
    );
    // transport failures: the connector is unavailable
    {
        let mut a = inv.answer.lock();
        a.resp = None;
        a.err = Some("connection refused".into());
    }
    let err = svc
        .call(&ctx(), "ORG-A", "document-repository/read", read_args())
        .await
        .unwrap_err();
    assert!(err.is_unavailable(), "transport error = {err}");
    inv.answer.lock().err = None;
    // a connector that is not registered
    let other = hub_service(&world().await, Some(inv.clone()), &[]).await;
    let err = other
        .call(&ctx(), "ORG-A", "document-repository/read", read_args())
        .await
        .unwrap_err();
    assert!(err.is_unavailable(), "unregistered connector = {err}");
    // an adapter definition the graph does not have
    let gm = world().await;
    seed_adapter(
        &gm,
        &instance("ORG-B", "document-repository", "gone", json!({})),
    )
    .await;
    let err = new_hub(&gm, inv.clone(), &[])
        .await
        .call(&ctx(), "ORG-B", "document-repository/read", read_args())
        .await
        .unwrap_err();
    assert!(err.is_unavailable(), "missing definition = {err}");
    // parameter values that do not fit the algorithm
    let g = world().await;
    seed_adapter(
        &g,
        &instance(
            "ORG-B",
            "document-repository",
            "localfs-document-repository",
            json!({}),
        ),
    )
    .await;
    let err = new_hub(&g, inv.clone(), &[])
        .await
        .call(&ctx(), "ORG-B", "document-repository/read", read_args())
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "missing parameter = {err}");
}

#[tokio::test]
async fn secret_parameters_reach_the_connector_but_not_the_script() {
    let g = world().await;
    let secure = adapter::Def {
        name: "secure-docs".into(),
        language: algo::JAVASCRIPT.into(),
        mcp: "document-repository".into(),
        connector: "localfs".into(),
        params: vec![
            string_param("root", true, None),
            secret_param("token", true),
            secret_param("other", false),
        ],
        code: r#"return { leaked: String(ctx.param("token")), out: ctx.call("read_file", { path: ctx.args().path }).text };"#.into(),
        ..Default::default()
    };
    seed_adapter(
        &g,
        &instance(
            "ORG-B",
            "document-repository",
            "secure-docs",
            json!({"root": "/b", "token": "env:TOK", "other": "env:X"}),
        ),
    )
    .await;
    let inv = FakeInvoker::answering(json!({"text": "ok"}));
    let out = new_hub(&g, inv.clone(), &[secure])
        .await
        .call(
            &ctx(),
            "ORG-B",
            "document-repository/read",
            obj(json!({"path": "f"})),
        )
        .await
        .unwrap();
    assert_eq!(out["leaked"], "null", "the script sees the secret: {out:?}");
    // only the secrets the connector declares leave the hub, resolved; the secret is not in its configuration
    let last = inv.last();
    assert!(
        last.secrets.len() == 1 && last.secrets["token"] == "secret:env:TOK",
        "secrets = {:?}",
        last.secrets
    );
    assert!(
        !inv.last_config().contains_key("token"),
        "a secret is in the configuration: {:?}",
        last.config
    );
}

#[tokio::test]
async fn check_adapter_and_template() {
    let inv = Arc::new(FakeInvoker::default());
    let svc = new_hub(&world().await, inv.clone(), &[]).await;
    let ok = goap_devseed::local_fs_adapter("ORG-B", "/b");
    let w = svc.check_adapter(&ctx(), &ok).await.unwrap();
    assert!(
        w.len() == 1 && w[0].contains(r#"secret "token""#),
        "valid instance: {w:?}"
    );
    let mut failures = Vec::new();
    for (name, bad) in [
        (
            "unknown MCP",
            instance("", "nope", "localfs-document-repository", json!({})),
        ),
        (
            "unknown definition",
            instance("", "document-repository", "nope", json!({})),
        ),
        (
            "missing parameter",
            instance(
                "",
                "document-repository",
                "localfs-document-repository",
                json!({}),
            ),
        ),
        (
            "unknown parameter",
            instance(
                "",
                "document-repository",
                "localfs-document-repository",
                json!({"root": "/", "x": 1}),
            ),
        ),
    ] {
        match svc.check_adapter(&ctx(), &bad).await {
            Err(e) if e.is_invalid() => {}
            r => failures.push(format!("{name}: {r:?}")),
        }
    }
    assert!(failures.is_empty(), "{failures:?}");
    // a definition of another MCP is refused
    let other = adapter::Def {
        name: "tickets".into(),
        language: algo::JAVASCRIPT.into(),
        mcp: "ticketing".into(),
        connector: "jira".into(),
        code: "return 1".into(),
        ..Default::default()
    };
    let svc = new_hub(&world().await, inv.clone(), &[other]).await;
    let err = svc
        .check_adapter(
            &ctx(),
            &instance("", "document-repository", "tickets", json!({})),
        )
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "definition of another MCP = {err}");

    // the template follows the MCP and what the connector exposes
    let (code, params) = svc
        .template(&ctx(), "document-repository", "localfs")
        .await
        .unwrap();
    assert!(
        code.contains(r#"ctx.call("read_file", { path: ctx.args().path })"#)
            && code.contains(r#"case "write""#),
        "template = {code:?}"
    );
    assert!(
        params.len() == 1 && params[0].name == "token" && params[0].r#type == adapter::PARAM_SECRET,
        "params = {params:?}"
    );
    let err = svc
        .template(&ctx(), "document-repository", "gdrive")
        .await
        .unwrap_err();
    assert!(
        err.is_not_found(),
        "template for an unregistered connector = {err}"
    );
}

#[tokio::test]
async fn snapshot_follows_the_graph() {
    let g = world().await;
    let d = Directory::new(g.clone());
    let s1 = d.snapshot(&ctx()).await.unwrap();
    assert!(s1.problems.is_empty(), "snapshot = {:?}", s1.problems);
    let s2 = d.snapshot(&ctx()).await.unwrap();
    assert!(
        Arc::ptr_eq(&s1, &s2),
        "the snapshot of an unchanged head must be reused"
    );
    seed_adapter(&g, &goap_devseed::local_fs_adapter("ORG-B", "/b")).await;
    let s3 = d.snapshot(&ctx()).await.unwrap();
    let (a, inherited) = s3
        .resolve("ORG-B", "document-repository")
        .expect("new adapter not seen");
    assert!(
        !inherited && a.params["root"] == "/b" && a.adapter == "localfs-document-repository",
        "new adapter not seen: {a:?} {inherited}"
    );
    let def = s3
        .adapter_def("localfs-document-repository")
        .expect("adapter definition not read from the graph");
    assert!(
        def.mcp == "document-repository"
            && def.connector == "localfs"
            && s3.adapter_defs().len() == 1,
        "adapter definition not read from the graph: {def:?}"
    );
    // an empty graph has no adapter and no MCP
    let empty = Directory::new(Arc::new(Graph::new(Arc::new(Memory::new()))));
    let s = empty.snapshot(&ctx()).await.unwrap();
    assert!(s.defs().is_empty(), "empty graph");
}

// A unit restricts an MCP it inherits without re-implementing it, for itself and its sub-units (ADR 0028); the
// restrictions of the chain add up.
#[tokio::test]
async fn unit_restricts_an_inherited_mcp() {
    let g = world().await;
    // ORG-A1 keeps list and read of the adapter it inherits from ORG-A; ORG-B has document-repository disabled
    for a in [
        adapter::Instance {
            unit: "ORG-A1".into(),
            mcp: "document-repository".into(),
            tools: vec!["list".into(), "read".into()],
            ..Default::default()
        },
        adapter::Instance {
            unit: "ORG-B".into(),
            mcp: "document-repository".into(),
            disabled: true,
            ..Default::default()
        },
    ] {
        seed_adapter(&g, &a).await;
    }
    Box::pin(goap_graphsvc::seed_unit(
        &ctx(),
        &g,
        "ORG-A2",
        "ORG-A2",
        "team",
        "ORG-A1",
    ))
    .await
    .unwrap();
    let inv = FakeInvoker::answering(json!({"text": "hi"}));
    let svc = new_hub(&g, inv.clone(), &[]).await;

    for unit in ["ORG-A1", "ORG-A2"] {
        // the sub-unit inherits the restriction
        let (tools, mcps) = svc.tools(&ctx(), unit).await.unwrap();
        assert!(
            tools.len() == 2 && mcps.len() == 1 && tools[1].name == "document-repository/read",
            "{unit}: tools = {tools:?} {mcps:?}"
        );
        let err = svc
            .call(
                &ctx(),
                unit,
                "document-repository/write",
                obj(json!({"path": "x", "content": "y"})),
            )
            .await
            .unwrap_err();
        assert!(err.is_not_bound(), "{unit}: restricted tool called: {err}");
        svc.call(
            &ctx(),
            unit,
            "document-repository/read",
            obj(json!({"path": "x"})),
        )
        .await
        .unwrap();
        // the implementation is still the one of ORG-A
        assert_eq!(inv.last_config()["root"], "/a", "{unit}: root");
    }
    let (_, eff) = svc.effective(&ctx(), "ORG-A2").await.unwrap();
    assert!(
        eff.len() == 1 && eff[0].adapter.unit == "ORG-A" && eff[0].restriction.by == ["ORG-A1"],
        "effective = {eff:?}"
    );

    // disabled: listed as effective (the pane shows it) but no tool left, hence no MCP to schedule
    let (tools, mcps) = svc.tools(&ctx(), "ORG-B").await.unwrap();
    assert!(
        tools.is_empty() && mcps.is_empty(),
        "disabled MCP usable: {tools:?} {mcps:?}"
    );
    let (_, eff) = svc.effective(&ctx(), "ORG-B").await.unwrap();
    assert!(
        eff.len() == 1 && eff[0].restriction.disabled && !eff[0].usable(),
        "effective = {eff:?}"
    );
    // ORG-A is not affected by the restriction of its sub-unit
    let (tools, _) = svc.tools(&ctx(), "ORG-A").await.unwrap();
    assert_eq!(tools.len(), 3, "ORG-A tools = {tools:?}");

    // a restriction-only instance checks against the MCP, not against an adapter definition
    let restrict = |deny: &str| adapter::Instance {
        unit: "ORG-A1".into(),
        mcp: "document-repository".into(),
        deny: vec![deny.into()],
        ..Default::default()
    };
    svc.check_adapter(&ctx(), &restrict("write")).await.unwrap();
    let err = svc
        .check_adapter(&ctx(), &restrict("delete"))
        .await
        .unwrap_err();
    assert!(err.is_invalid(), "unknown tool restricted: {err}");
}

// A connector that calls a model by itself gets the gateway's endpoint and a token of the call (ADR 0123 §6): a system
// subject of the connector acting for the caller, for the change and the process of the call. Another connector gets
// neither.
#[tokio::test]
async fn a_connector_calling_models_gets_a_token_of_the_call() {
    let inv = FakeInvoker::answering(json!({"text": "hi"}));
    let mut svc: Service = new_hub(&world().await, inv.clone(), &[]).await;
    svc.models = Some(ModelEndpoint {
        url: "http://modelgw/anthropic".into(),
        secret: b"s3cret".to_vec(),
        ..Default::default()
    });
    let c = goap_authz::with_principal(
        &ctx(),
        Principal {
            subject: "alice".into(),
            project: "P1".into(),
            ..Default::default()
        },
    );
    let c = mcp::with_call(
        &c,
        mcp::CallContext {
            change: "CH-1".into(),
            process: "proc-1".into(),
            ..Default::default()
        },
    );
    svc.call(&c, "ORG-A", "document-repository/read", read_args())
        .await
        .unwrap();
    let last = inv.last();
    assert!(
        last.model_url.is_empty() && last.model_token.is_empty(),
        "a connector that calls no model gets no token: {last:?}"
    );
    let mut info = localfs_info();
    info.calls_models = true;
    svc.register_connector(&ctx(), Some(info), "http://localfs:8080")
        .await
        .unwrap();
    svc.call(&c, "ORG-A", "document-repository/read", read_args())
        .await
        .unwrap();
    let last = inv.last();
    let claims = goap_modeltoken::verify(b"s3cret", &last.model_token, Utc::now())
        .unwrap_or_else(|e| panic!("token {e} {last:?}"));
    assert_eq!(last.model_url, "http://modelgw/anthropic");
    let left = claims.expires - Utc::now().timestamp();
    assert!(
        claims.subject == "system:connector:localfs"
            && claims.end_user == "alice"
            && claims.project == "P1"
            && claims.org == "ORG-A"
            && claims.change == "CH-1"
            && claims.process == "proc-1"
            && claims.source == "connector:localfs"
            && left >= Duration::from_secs(4 * 3600).as_secs() as i64,
        "claims {claims:?}"
    );
}
