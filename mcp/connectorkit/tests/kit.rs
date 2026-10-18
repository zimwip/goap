//! The kit served over Connect: the token, unknown operations, the call context and the result.

use std::collections::HashMap;
use std::sync::Arc;

use goap_connect::{Client, Code, Ctx, Interceptors, Request, async_trait};
use goap_connectorkit::*;
use goap_gen::connector::v1::{
    self as pb, ConnectorInfo, ConnectorService, ConnectorServiceClient, Operation,
};
use serde_json::{Map, Value, json};

struct Echo;

#[async_trait]
impl Connector for Echo {
    fn info(&self) -> ConnectorInfo {
        ConnectorInfo {
            id: "echo".into(),
            operations: vec![
                Operation {
                    name: "echo".into(),
                    ..Default::default()
                },
                Operation {
                    name: "fail".into(),
                    ..Default::default()
                },
            ],
            ..Default::default()
        }
    }

    async fn invoke(
        &self,
        ctx: &Ctx,
        op: &str,
        args: Map<String, Value>,
        _config: Map<String, Value>,
        _secrets: HashMap<String, String>,
    ) -> Result<Map<String, Value>, InvokeError> {
        if op == "fail" {
            return Err("boom".into());
        }
        let call = goap_mcp::call_from(ctx);
        let mut out = args;
        out.insert("change".into(), Value::String(call.change));
        Ok(out)
    }
}

async fn serve(token: &str) -> String {
    let app = handler(Arc::new(Echo), token, Interceptors::none());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = ln.local_addr().unwrap();
    tokio::spawn(async move { goap_connect::axum::serve(ln, app).await.unwrap() });
    format!("http://{addr}")
}

fn args(v: Value) -> Option<pbjson_types::Struct> {
    goap_pbconv::to_struct(v.as_object().unwrap())
}

#[tokio::test]
async fn invoke_through_the_kit() {
    let base = serve("secret").await;
    let ctx = Ctx::background();
    let invoke = |op: &str| pb::InvokeRequest {
        operation: op.into(),
        arguments: args(json!({"x": "y"})),
        change_id: "C1".into(),
        ..Default::default()
    };

    let anon = ConnectorServiceClient::new(Client::new(&base));
    let err = anon
        .invoke(&ctx, Request::new(invoke("echo")))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::PermissionDenied);
    assert_eq!(err.message(), "invalid connector token");

    let c = ConnectorServiceClient::new(
        Client::builder(&base)
            .header(TOKEN_HEADER, "secret")
            .build(),
    );
    let info = c
        .describe(&ctx, Request::new(pb::DescribeRequest {}))
        .await
        .unwrap();
    assert_eq!(info.msg.info.unwrap().id, "echo");

    let err = c
        .invoke(&ctx, Request::new(invoke("nope")))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::InvalidArgument);
    assert_eq!(err.message(), "nope: unknown operation");

    let got = c
        .invoke(&ctx, Request::new(invoke("echo")))
        .await
        .unwrap()
        .msg;
    assert!(!got.is_error);
    assert_eq!(
        Value::Object(goap_pbconv::to_map(got.result.as_ref())),
        json!({"x": "y", "change": "C1"})
    );

    let got = c
        .invoke(&ctx, Request::new(invoke("fail")))
        .await
        .unwrap()
        .msg;
    assert!(got.is_error && got.error == "boom", "{got:?}");
}
