use std::collections::HashMap;
use std::sync::Arc;

use async_trait::async_trait;
use axum::Router;
use goap_connect::Interceptors;
use goap_ctx::Ctx;
use goap_dsl as dsl;
use goap_engine::Sandboxes;
use goap_gen::runtime::v1::{RuntimeServiceClient, runtime_service_router, sandbox_service_router};
use parking_lot::Mutex;
use serde_json::{Map, Value};
use tokio::task::JoinHandle;

use super::*;

#[derive(Default)]
struct RecordingHost {
    calls: Mutex<Vec<String>>,
}

impl RecordingHost {
    fn add(&self, s: String) {
        self.calls.lock().push(s);
    }
}

#[async_trait]
impl dsl::Host for RecordingHost {
    async fn complete(
        &self,
        _: &Ctx,
        r: dsl::CompleteRequest,
    ) -> Result<dsl::CompleteResult, dsl::Error> {
        self.add(format!("llm:{}", r.prompt));
        Ok(dsl::CompleteResult {
            text: "summary".into(),
            input_tokens: 5,
            output_tokens: 2,
            ..Default::default()
        })
    }
    async fn run_agent(
        &self,
        _: &Ctx,
        name: &str,
        _: &str,
    ) -> Result<dsl::AgentResult, dsl::Error> {
        self.add(format!("agent:{name}"));
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
        self.add(format!("node:{key}"));
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

/// Serves a router on a free local port.
async fn serve(router: Router) -> (String, JoinHandle<()>) {
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    let h = tokio::spawn(async move {
        let _ = axum::serve(ln, router).await;
    });
    (url, h)
}

/// loopback starts runners as in-process HTTP servers.
#[derive(Default)]
struct Loopback {
    srvs: Mutex<HashMap<String, JoinHandle<()>>>,
}

#[async_trait]
impl Provisioner for Loopback {
    fn name(&self) -> &str {
        "loopback"
    }
    async fn start(&self, _: &Ctx, spec: Spec) -> Result<Instance, Error> {
        let router = sandbox_service_router(Arc::new(Runner::default()), Interceptors::none())
            .route("/healthz", axum::routing::get(|| async { "ok" }));
        let (url, h) = serve(router).await;
        self.srvs.lock().insert(spec.id.clone(), h);
        Ok(Instance {
            id: spec.id,
            endpoint: url,
        })
    }
    async fn stop(&self, _: &Ctx, id: &str) -> Result<(), Error> {
        if let Some(h) = self.srvs.lock().remove(id) {
            h.abort();
        }
        Ok(())
    }
}

async fn runtime_server() -> (Arc<Runtime>, String, JoinHandle<()>) {
    let rt = Arc::new(Runtime::new());
    let (url, h) = serve(runtime_service_router(rt.clone(), Interceptors::none())).await;
    (rt, url, h)
}

const SCRIPT: &str = r#"
const n = ctx.node("REQ-1");
const r = ctx.complete({ prompt: "summarize " + n.key });
ctx.addArtifact("summary", { text: r.text, tokens: r.inputTokens });
ctx.log("done " + n.type);
"#;

fn check_result(res: &dsl::ScriptResult, h: &RecordingHost) {
    assert!(
        res.items.len() == 1
            && res.items[0]["target"] == "summary"
            && res.items[0]["data"]["text"] == "summary",
        "unexpected items {:?}",
        res.items
    );
    assert!(
        res.logs.len() == 1 && res.logs[0].message == "done Requirement",
        "logs {:?}",
        res.logs
    );
    assert_eq!(h.calls.lock().join(","), "node:REQ-1,llm:summarize REQ-1");
}

fn js(code: &str) -> dsl::Job {
    dsl::Job {
        language: "javascript".into(),
        code: code.into(),
        ..Default::default()
    }
}

#[tokio::test]
async fn remote_execution() {
    let (rt, url, _srv) = runtime_server().await;
    let pool = Pool::new(Arc::new(Loopback::default()), rt.clone(), &url);
    let ctx = Ctx::background();
    let sb = pool.acquire(&ctx, "p1").await.unwrap();
    let h = RecordingHost::default();
    let (res, err) = sb.execute(&ctx, js(SCRIPT), &h).await;
    err.unwrap();
    check_result(&res, &h);
    // suspension crosses the sandbox boundary
    let (res, err) = sb
        .execute(
            &ctx,
            js(r#"ctx.impactNode("X", "y"); ctx.runAgent("child", "go")"#),
            &h,
        )
        .await;
    assert!(
        err.is_ok() && res.suspended && res.items.is_empty(),
        "expected suspension: {res:?} {err:?}"
    );
    // errors are reported, tokens of finished jobs are revoked
    let (_, err) = sb.execute(&ctx, js(r#"throw new Error("boom")"#), &h).await;
    assert!(
        err.as_ref().is_err_and(|e| e.to_string().contains("boom")),
        "expected script error, got {err:?}"
    );
    assert_eq!(rt.jobs(), 0, "job tokens must be revoked");
    pool.release(&ctx, "p1").await;
}

#[tokio::test]
async fn runtime_rejects_bad_token() {
    let (rt, url, _srv) = runtime_server().await;
    let (_token, _done) = rt.register("job", Arc::new(RecordingHost::default()));
    let h = RemoteHost {
        client: RuntimeServiceClient::connect(&url),
        job_id: "job".into(),
        token: "forged".into(),
    };
    assert!(
        dsl::Host::node(&h, &Ctx::background(), "REQ-1")
            .await
            .is_err(),
        "forged token accepted"
    );
}

#[tokio::test]
async fn runtime_serves_a_registered_host() {
    // not in the Go suite: the right token reaches the host
    let (rt, url, _srv) = runtime_server().await;
    let host = Arc::new(RecordingHost::default());
    let (token, done) = rt.register("job", host.clone());
    let h = RemoteHost {
        client: RuntimeServiceClient::connect(&url),
        job_id: "job".into(),
        token,
    };
    let n = dsl::Host::node(&h, &Ctx::background(), "REQ-1")
        .await
        .unwrap();
    assert_eq!(n.r#type, "Requirement");
    assert!(
        dsl::Host::nodes(&h, &Ctx::background(), "x")
            .await
            .unwrap()
            .is_empty()
    );
    assert!(
        dsl::Host::run_agent(&h, &Ctx::background(), "c", "i")
            .await
            .unwrap_err()
            .is_suspended()
    );
    done.done();
    assert_eq!(rt.jobs(), 0);
}

#[cfg(unix)]
#[tokio::test]
async fn docker_provisioner_requests() {
    use axum::body::Bytes;
    use axum::http::{Method, StatusCode, Uri};
    use axum::response::IntoResponse;

    let dir = std::env::temp_dir().join(format!("goap-docker-{}", uuid::Uuid::new_v4().simple()));
    std::fs::create_dir_all(&dir).unwrap();
    let sock = dir.join("docker.sock");
    let ln = tokio::net::UnixListener::bind(&sock).unwrap();
    let seen: Arc<Mutex<Vec<String>>> = Arc::default();
    let created: Arc<Mutex<Value>> = Arc::default();
    let (s, c) = (seen.clone(), created.clone());
    let app = Router::new().fallback(move |method: Method, uri: Uri, body: Bytes| {
        let (s, c) = (s.clone(), c.clone());
        async move {
            s.lock().push(format!("{method} {}", uri.path()));
            if uri.path().ends_with("/containers/create") {
                *c.lock() = serde_json::from_slice(&body).unwrap();
                (StatusCode::CREATED, r#"{"Id":"abc"}"#).into_response()
            } else {
                StatusCode::NO_CONTENT.into_response()
            }
        }
    });
    let srv = tokio::spawn(async move {
        let _ = axum::serve(ln, app).await;
    });

    let mut d = DockerProvisioner::new(&format!("unix://{}", sock.display()));
    d.network = "goap-sandbox".into();
    d.runtime = "runsc".into();
    let ctx = Ctx::background();
    let inst = d
        .start(
            &ctx,
            Spec {
                id: "goap-sbx-1".into(),
                process_id: "p1".into(),
                image: "goap/runner".into(),
                memory_mb: 256,
                cpus: 0.5,
                pids: 64,
            },
        )
        .await
        .unwrap();
    assert_eq!(inst.endpoint, "http://goap-sbx-1:8080");
    d.stop(&ctx, &inst.id).await.unwrap();
    srv.abort();
    let _ = std::fs::remove_dir_all(&dir);
    let created = created.lock().clone();
    let host = &created["HostConfig"];
    assert!(
        host["ReadonlyRootfs"] == true
            && host["NetworkMode"] == "goap-sandbox"
            && host["Runtime"] == "runsc"
            && host["Memory"].as_f64() == Some(256.0 * 1024.0 * 1024.0)
            && host["PidsLimit"].as_f64() == Some(64.0),
        "container not hardened: {host}"
    );
    assert_eq!(host["CapDrop"], serde_json::json!(["ALL"]), "capabilities");
    assert_eq!(created["User"], "65532:65532");
    assert_eq!(
        seen.lock().join(","),
        "POST /v1.43/containers/create,POST /v1.43/containers/abc/start,DELETE /v1.43/containers/goap-sbx-1"
    );
}

#[tokio::test]
async fn kubernetes_provisioner_requests() {
    use axum::body::Bytes;
    use axum::http::{HeaderMap, Method, StatusCode, Uri};
    use axum::response::IntoResponse;

    let pod: Arc<Mutex<Value>> = Arc::default();
    let gets: Arc<Mutex<i32>> = Arc::default();
    let (p, g) = (pod.clone(), gets.clone());
    let app = Router::new().fallback(
        move |method: Method, uri: Uri, headers: HeaderMap, body: Bytes| {
            let (p, g) = (p.clone(), g.clone());
            async move {
                if headers.get("authorization").and_then(|v| v.to_str().ok()) != Some("Bearer tok")
                {
                    return StatusCode::UNAUTHORIZED.into_response();
                }
                if method == Method::POST && uri.path() == "/api/v1/namespaces/goap/pods" {
                    *p.lock() = serde_json::from_slice(&body).unwrap();
                    return (StatusCode::CREATED, "{}").into_response();
                }
                if method == Method::GET {
                    let mut n = g.lock();
                    *n += 1;
                    if *n < 2 {
                        return r#"{"status":{"phase":"Pending"}}"#.into_response();
                    }
                    return r#"{"status":{"phase":"Running","podIP":"10.1.2.3"}}"#.into_response();
                }
                if method == Method::DELETE {
                    return "{}".into_response();
                }
                StatusCode::BAD_REQUEST.into_response()
            }
        },
    );
    let (url, srv) = serve(app).await;
    let mut k = KubernetesProvisioner::default();
    k.api_server = url;
    k.token = "tok".into();
    k.namespace = "goap".into();
    k.runtime_class = "gvisor".into();
    k.http = Some(reqwest::Client::new());
    let ctx = Ctx::background();
    let inst = k
        .start(
            &ctx,
            Spec {
                id: "goap-sbx-2".into(),
                process_id: "p2".into(),
                image: "goap/runner".into(),
                memory_mb: 128,
                ..Default::default()
            },
        )
        .await
        .unwrap();
    assert_eq!(inst.endpoint, "http://10.1.2.3:8080");
    let pod = pod.lock().clone();
    let spec = &pod["spec"];
    assert!(
        spec["automountServiceAccountToken"] == false && spec["runtimeClassName"] == "gvisor",
        "pod not hardened: {spec}"
    );
    let c = &spec["containers"][0]["securityContext"];
    assert!(
        c["readOnlyRootFilesystem"] == true && c["allowPrivilegeEscalation"] == false,
        "container security context: {c}"
    );
    k.stop(&ctx, &inst.id).await.unwrap();
    srv.abort();
}
