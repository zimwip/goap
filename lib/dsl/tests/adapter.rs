//! Port of `lib/dsl/adapter_test.go`. `TestGoAdapter` is JavaScript here (the Go language left the DSL).

use std::sync::Arc;

use async_trait::async_trait;
use goap_algo::{Bound, JAVASCRIPT};
use goap_dsl::{
    AdapterInput, Error, MAX_ADAPTER_CALLS, OperationCall, check_algorithm_code, run_adapter,
};
use parking_lot::Mutex;
use serde_json::{Map, Value, json};

#[derive(Default)]
struct Recorder {
    ops: Mutex<Vec<String>>,
    args: Mutex<Vec<Map<String, Value>>>,
    fail: Mutex<Option<String>>,
    reply: Option<Map<String, Value>>,
}

#[async_trait]
impl OperationCall for Recorder {
    async fn call(
        &self,
        _: &goap_ctx::Ctx,
        op: &str,
        args: Map<String, Value>,
    ) -> Result<Map<String, Value>, Error> {
        self.ops.lock().push(op.to_string());
        self.args.lock().push(args);
        if let Some(f) = self.fail.lock().clone() {
            return Err(Error::host(f));
        }
        Ok(self
            .reply
            .clone()
            .unwrap_or_else(|| obj(json!({"text": "ok"}))))
    }
}

fn obj(v: Value) -> Map<String, Value> {
    v.as_object().cloned().unwrap_or_default()
}

fn adapter(lang: &str, code: &str, params: Value) -> Bound {
    Bound {
        instance: "test".into(),
        algorithm: "a".into(),
        language: lang.into(),
        code: code.into(),
        params: obj(params),
        ..Default::default()
    }
}

fn bg() -> goap_ctx::Ctx {
    goap_ctx::Ctx::background()
}

const JS_ADAPTER: &str = r#"
switch (ctx.tool()) {
  case "read":  return ctx.call("read_file", { path: ctx.args().path, base: ctx.param("root") });
  case "size":  return 42;
}
ctx.fail("not implemented: " + ctx.tool());
"#;

fn input(tool: &str, args: Value, ops: &[&str], rec: &Arc<Recorder>) -> AdapterInput {
    AdapterInput {
        tool: tool.into(),
        args: obj(args),
        operations: ops.iter().map(|s| s.to_string()).collect(),
        call: Some(rec.clone()),
    }
}

#[tokio::test]
async fn java_script_adapter_maps_the_tool_onto_the_connector() {
    let rec = Arc::new(Recorder {
        reply: Some(obj(json!({"text": "hello"}))),
        ..Default::default()
    });
    let mut inp = input("read", json!({"path": "a.txt"}), &["read_file"], &rec);
    let out = run_adapter(
        &bg(),
        &adapter("javascript", JS_ADAPTER, json!({"root": "/data"})),
        inp.clone(),
    )
    .await
    .unwrap();
    assert!(
        out.ok() && out.result.as_ref().unwrap()["text"] == "hello" && out.calls == 1,
        "outcome = {out:?}"
    );
    assert_eq!(rec.ops.lock()[0], "read_file");
    assert_eq!(
        Value::Object(rec.args.lock()[0].clone()),
        json!({"path": "a.txt", "base": "/data"})
    );

    // a scalar result is wrapped, an unmapped tool is rejected
    inp.tool = "size".into();
    let out = run_adapter(
        &bg(),
        &adapter("javascript", JS_ADAPTER, Value::Null),
        inp.clone(),
    )
    .await
    .unwrap();
    assert_eq!(out.result.unwrap()["value"], json!(42));
    inp.tool = "delete".into();
    let out = run_adapter(&bg(), &adapter("javascript", JS_ADAPTER, Value::Null), inp)
        .await
        .unwrap();
    assert!(
        !out.ok() && out.failures[0].contains("not implemented"),
        "unmapped tool = {out:?}"
    );
}

#[tokio::test]
async fn adapter_without_arguments() {
    let rec = Arc::new(Recorder::default());
    let inp = AdapterInput {
        tool: "list".into(),
        operations: vec!["list_dir".into()],
        call: Some(rec.clone()),
        ..Default::default()
    };
    let out = run_adapter(
        &bg(),
        &adapter(
            "javascript",
            r#"return ctx.call("list_dir", { path: ctx.args().path || "" })"#,
            Value::Null,
        ),
        inp,
    )
    .await
    .unwrap();
    assert!(out.ok(), "{out:?}");
    assert_eq!(rec.args.lock()[0]["path"], "");
}

#[tokio::test]
async fn adapter_call_limits() {
    let rec = Arc::new(Recorder::default());
    let inp = input("x", Value::Null, &["read_file"], &rec);

    // an operation the connector does not expose
    let err = run_adapter(
        &bg(),
        &adapter("javascript", r#"return ctx.call("rm_rf", {})"#, Value::Null),
        inp.clone(),
    )
    .await
    .unwrap_err();
    assert!(
        err.to_string().contains(r#"no operation "rm_rf""#) && rec.ops.lock().is_empty(),
        "unknown operation = {err}"
    );
    // a connector failure throws in the script, which may catch it
    *rec.fail.lock() = Some("no such file".into());
    let err = run_adapter(
        &bg(),
        &adapter(
            "javascript",
            r#"return ctx.call("read_file", {})"#,
            Value::Null,
        ),
        inp.clone(),
    )
    .await
    .unwrap_err();
    assert!(
        err.to_string().contains("no such file"),
        "connector failure = {err}"
    );
    let out = run_adapter(
        &bg(),
        &adapter(
            "javascript",
            r#"try { ctx.call("read_file", {}) } catch (e) { return {error: String(e)} }"#,
            Value::Null,
        ),
        inp.clone(),
    )
    .await
    .unwrap();
    assert!(
        out.result.unwrap()["error"]
            .as_str()
            .unwrap()
            .contains("no such file")
    );
    // too many calls
    *rec.fail.lock() = None;
    let before = rec.ops.lock().len();
    let err = run_adapter(
        &bg(),
        &adapter(
            "javascript",
            r#"for (var i = 0; i < 100; i++) ctx.call("read_file", {})"#,
            Value::Null,
        ),
        inp,
    )
    .await
    .unwrap_err();
    let made = rec.ops.lock().len() - before;
    assert!(
        err.to_string().contains("connector calls") && made <= MAX_ADAPTER_CALLS,
        "call limit = {err} ({made} calls)"
    );
    assert!(matches!(err, Error::Adapter { .. }));
}

/// `TestGoAdapter`, the script in JavaScript.
#[tokio::test]
async fn adapter_returning_the_connector_result() {
    let rec = Arc::new(Recorder {
        reply: Some(obj(json!({"text": "from js"}))),
        ..Default::default()
    });
    let code = r#"const res = ctx.call("read_file", { path: ctx.args()["path"] }); return res;"#;
    let out = run_adapter(
        &bg(),
        &adapter("js", code, Value::Null),
        input("read", json!({"path": "b.txt"}), &["read_file"], &rec),
    )
    .await
    .unwrap();
    assert_eq!(out.result.unwrap()["text"], "from js");
    assert_eq!(rec.args.lock()[0]["path"], "b.txt");
    let err = run_adapter(
        &bg(),
        &adapter("go", "package main", Value::Null),
        AdapterInput::default(),
    )
    .await
    .unwrap_err();
    assert_eq!(
        err.to_string(),
        "adapter test: the go script language is no longer supported: rewrite the script in javascript"
    );
}

#[tokio::test]
async fn adapter_without_connector() {
    let out = run_adapter(
        &bg(),
        &adapter("js", r#"try { ctx.call("x", {}) } catch (e) { return {e: String(e), ops: ctx.operations()} }"#, Value::Null),
        AdapterInput {
            operations: vec!["x".into()],
            ..Default::default()
        },
    )
    .await
    .unwrap();
    assert_eq!(
        Value::Object(out.result.unwrap()),
        json!({"e": "Error: no connector to call", "ops": ["x"]})
    );
}

#[test]
fn adapter_code_compiles() {
    check_algorithm_code(JAVASCRIPT, "return ctx.call('x', {})").unwrap();
}
