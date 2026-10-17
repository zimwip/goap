//! The process provisioner of goap-sandbox running this goap-runner (Go's TestProcessProvisioner of engine/sandbox,
//! here because the binary is built for the tests of its own package).
use std::sync::Arc;
use std::time::{Duration, Instant};

use async_trait::async_trait;
use goap_connect::Interceptors;
use goap_ctx::Ctx;
use goap_dsl as dsl;
use goap_engine::Sandboxes;
use goap_gen::runtime::v1::runtime_service_router;
use goap_sandbox::{Pool, ProcessProvisioner, Runtime};
use parking_lot::Mutex;
use serde_json::{Map, Value};

#[derive(Default)]
struct RecordingHost {
    calls: Mutex<Vec<String>>,
}

#[async_trait]
impl dsl::Host for RecordingHost {
    async fn complete(
        &self,
        _: &Ctx,
        r: dsl::CompleteRequest,
    ) -> Result<dsl::CompleteResult, dsl::Error> {
        self.calls.lock().push(format!("llm:{}", r.prompt));
        Ok(dsl::CompleteResult {
            text: "summary".into(),
            input_tokens: 5,
            output_tokens: 2,
            ..Default::default()
        })
    }
    async fn run_agent(&self, _: &Ctx, _: &str, _: &str) -> Result<dsl::AgentResult, dsl::Error> {
        Err(dsl::Error::Suspended)
    }
    async fn call_tool(
        &self,
        _: &Ctx,
        _: &str,
        _: Map<String, Value>,
    ) -> Result<Value, dsl::Error> {
        Ok(Value::Null)
    }
    async fn node(&self, _: &Ctx, key: &str) -> Result<dsl::Node, dsl::Error> {
        self.calls.lock().push(format!("node:{key}"));
        Ok(dsl::Node {
            key: key.into(),
            r#type: "Requirement".into(),
            ..Default::default()
        })
    }
    async fn nodes(&self, _: &Ctx, _: &str) -> Result<Vec<dsl::Node>, dsl::Error> {
        Ok(Vec::new())
    }
    async fn links(
        &self,
        _: &Ctx,
        _: &str,
        _: &str,
        _: &str,
    ) -> Result<Vec<dsl::Link>, dsl::Error> {
        Ok(Vec::new())
    }
}

const SCRIPT: &str = r#"
const n = ctx.node("REQ-1");
const r = ctx.complete({ prompt: "summarize " + n.key });
ctx.addArtifact("summary", { text: r.text, tokens: r.inputTokens });
ctx.log("done " + n.type);
"#;

fn js(code: &str) -> dsl::Job {
    dsl::Job {
        language: "javascript".into(),
        code: code.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn process_provisioner() {
    let rt = Arc::new(Runtime::new());
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    let router = runtime_service_router(rt.clone(), Interceptors::none());
    let srv = tokio::spawn(async move {
        let _ = axum::serve(ln, router).await;
    });
    let prov = ProcessProvisioner::new(env!("CARGO_BIN_EXE_goap-runner"));
    let pool = Pool::new(Arc::new(prov), rt, &url);
    let (ctx, _cancel) = Ctx::background().with_timeout(Duration::from_secs(30));
    let sb = pool.acquire(&ctx, "p1").await.unwrap();
    let h = RecordingHost::default();
    let (res, err) = sb.execute(&ctx, js(SCRIPT), &h).await;
    err.unwrap();
    assert!(
        res.items.len() == 1 && res.items[0]["data"]["text"] == "summary",
        "items {:?}",
        res.items
    );
    assert!(
        res.logs.len() == 1 && res.logs[0].message == "done Requirement",
        "logs {:?}",
        res.logs
    );
    assert_eq!(h.calls.lock().join(","), "node:REQ-1,llm:summarize REQ-1");
    // a script cannot read the environment: there is no process object (and the child's environment is empty)
    let (_, err) = sb
        .execute(&ctx, js(r#"ctx.log(process.env.GOAP_SECRET_FOR_TEST)"#), &h)
        .await;
    assert!(err.is_err(), "the environment must not be reachable");
    let id = sb.id();
    assert!(id.starts_with("goap-sbx-"), "sandbox id {id}");
    let endpoint = format!("{}/healthz", pool.instance("p1").unwrap().endpoint);
    pool.release(&ctx, "p1").await;
    let deadline = Instant::now() + Duration::from_secs(5);
    let hc = reqwest::Client::new();
    loop {
        if hc.get(&endpoint).send().await.is_err() {
            break; // stopped
        }
        assert!(
            Instant::now() < deadline,
            "sandbox process still running after release"
        );
        tokio::time::sleep(Duration::from_millis(100)).await;
    }
    srv.abort();
}
