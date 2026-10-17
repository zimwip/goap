#![cfg(unix)]

use std::collections::HashMap;
use std::sync::Arc;
use std::time::{Duration, Instant};

use axum::body::{Body, Bytes};
use axum::extract::State;
use axum::http::{Method, StatusCode, Uri};
use axum::response::{IntoResponse, Response};
use goap_connect::Ctx;
use goap_connectorkit::Connector as _;
use goap_mcp::{CallContext, with_call};
use serde_json::{Value, json};
use tokio::io::AsyncReadExt;

use crate::docker::container_name;
use crate::workspace::Space;
use crate::workspace_test::{Fixture, TempDir, new_fixture, obj};
use crate::*;

/// fakeDocker is the part of the Docker Engine API the connector uses, its execs run on the host in the directory
/// mounted at /workspace (what a container of an image with a shell would do).
#[derive(Default)]
struct FakeDocker {
    /// the /tmp of the container
    tmp: String,
    state: parking_lot::Mutex<FakeState>,
}

#[derive(Default)]
struct FakeState {
    created: Vec<Value>,
    /// container name -> running
    running: HashMap<String, bool>,
    /// container name -> host directory of /workspace
    bind: HashMap<String, String>,
    execs: HashMap<String, FakeExec>,
    removed: Vec<String>,
    seq: u32,
    unexpected: Vec<String>,
}

struct FakeExec {
    container: String,
    spec: Value,
    running: bool,
    exit: i32,
}

fn reply(v: Value) -> Response {
    axum::Json(v).into_response()
}

fn frame(stream: u8, p: &[u8]) -> Bytes {
    let mut b = vec![stream, 0, 0, 0];
    b.extend_from_slice(&(p.len() as u32).to_be_bytes());
    b.extend_from_slice(p);
    Bytes::from(b)
}

/// An exec to start: its id, arguments, environment, directory and whether it is detached.
type Start = (String, Vec<String>, Vec<(String, String)>, String, bool);

fn route(d: &FakeDocker, method: &Method, uri: &Uri, body: &Bytes) -> Result<Response, Start> {
    let p = uri.path().trim_start_matches("/v1.43").to_string();
    let parts: Vec<String> = p.trim_matches('/').split('/').map(String::from).collect();
    let body: Value = serde_json::from_slice(body).unwrap_or(Value::Null);
    let query: HashMap<String, String> = uri
        .query()
        .unwrap_or("")
        .split('&')
        .filter_map(|kv| kv.split_once('=').map(|(k, v)| (k.into(), v.into())))
        .collect();
    let part = |i: usize| parts.get(i).map(String::as_str).unwrap_or("");
    let mut st = d.state.lock();
    if *method == Method::POST && p == "/containers/create" {
        let name = query.get("name").cloned().unwrap_or_default();
        st.created.push(body.clone());
        let bind = body["HostConfig"]["Binds"][0].as_str().unwrap_or("");
        let host = bind
            .strip_suffix(&format!(":{CONTAINER_DIR}"))
            .unwrap_or(bind);
        st.bind.insert(name.clone(), host.into());
        st.running.insert(name.clone(), false);
        return Ok(reply(json!({ "Id": name })));
    }
    if part(0) == "containers" && parts.len() == 3 && part(2) == "json" {
        let Some(&running) = st.running.get(part(1)) else {
            return Ok((StatusCode::NOT_FOUND, "no such container").into_response());
        };
        let status = if running { "running" } else { "exited" };
        return Ok(reply(
            json!({"Id": part(1), "State": {"Running": running, "Status": status}, "Config": {"Image": "goap/builder:1"}}),
        ));
    }
    if part(0) == "containers" && parts.len() == 3 && part(2) == "start" {
        st.running.insert(part(1).into(), true);
        return Ok(StatusCode::NO_CONTENT.into_response());
    }
    if *method == Method::DELETE && part(0) == "containers" {
        let ok = st.running.remove(part(1)).is_some();
        st.removed.push(part(1).into());
        if !ok {
            return Ok((StatusCode::NOT_FOUND, "no such container").into_response());
        }
        return Ok(StatusCode::OK.into_response());
    }
    if part(0) == "containers" && parts.len() == 3 && part(2) == "exec" {
        st.seq += 1;
        let id = format!("exec-{}", char::from(b'a' + st.seq as u8));
        st.execs.insert(
            id.clone(),
            FakeExec {
                container: part(1).into(),
                spec: body,
                running: false,
                exit: 0,
            },
        );
        return Ok(reply(json!({ "Id": id })));
    }
    if part(0) == "exec" && parts.len() == 3 && part(2) == "json" {
        let x = &st.execs[part(1)];
        return Ok(reply(json!({"Running": x.running, "ExitCode": x.exit})));
    }
    if part(0) == "exec" && parts.len() == 3 && part(2) == "start" {
        let id = part(1).to_string();
        let x = st.execs.get_mut(&id).unwrap();
        x.running = true;
        let (container, spec) = (x.container.clone(), x.spec.clone());
        let tmp = format!("{}/", d.tmp);
        let args: Vec<String> = spec["Cmd"]
            .as_array()
            .unwrap()
            .iter()
            .map(|a| a.as_str().unwrap().replace("/tmp/", &tmp))
            .collect();
        let mut env = vec![(
            "PATH".to_string(),
            std::env::var("PATH").unwrap_or_default(),
        )];
        for v in spec["Env"].as_array().into_iter().flatten() {
            let v = v.as_str().unwrap().replace("/tmp/", &tmp);
            if let Some((k, val)) = v.split_once('=') {
                env.push((k.into(), val.into()));
            }
        }
        let dir = spec["WorkingDir"].as_str().unwrap_or("").to_string();
        let dir = match dir.strip_prefix(CONTAINER_DIR) {
            Some(rest) => format!("{}{rest}", st.bind[&container]),
            None => dir,
        };
        let detach = body["Detach"].as_bool() == Some(true);
        return Err((id, args, env, dir, detach));
    }
    st.unexpected.push(format!("{method} {uri}"));
    Ok((StatusCode::BAD_REQUEST, "unexpected").into_response())
}

async fn serve(
    State(d): State<Arc<FakeDocker>>,
    method: Method,
    uri: Uri,
    body: Bytes,
) -> Response {
    let (id, args, env, dir, detach) = match route(&d, &method, &uri, &body) {
        Ok(r) => return r,
        Err(start) => start,
    };
    {
        let mut cmd = tokio::process::Command::new(&args[0]);
        cmd.args(&args[1..]).env_clear().envs(env);
        if !dir.is_empty() {
            cmd.current_dir(&dir);
        }
        if detach {
            let _ = cmd.status().await;
            d.state.lock().execs.get_mut(&id).unwrap().running = false;
            return StatusCode::OK.into_response();
        }
        cmd.stdout(std::process::Stdio::piped())
            .stderr(std::process::Stdio::piped());
        let mut child = cmd.spawn().unwrap();
        let (tx, rx) = tokio::sync::mpsc::unbounded_channel::<Bytes>();
        let mut readers = Vec::new();
        for (stream, pipe) in [
            (
                1u8,
                child
                    .stdout
                    .take()
                    .map(|p| Box::new(p) as Box<dyn tokio::io::AsyncRead + Unpin + Send>),
            ),
            (
                2u8,
                child
                    .stderr
                    .take()
                    .map(|p| Box::new(p) as Box<dyn tokio::io::AsyncRead + Unpin + Send>),
            ),
        ] {
            let tx = tx.clone();
            let mut pipe = pipe.unwrap();
            readers.push(tokio::spawn(async move {
                let mut buf = vec![0u8; 8192];
                loop {
                    match pipe.read(&mut buf).await {
                        Ok(0) | Err(_) => return,
                        Ok(n) => {
                            if tx.send(frame(stream, &buf[..n])).is_err() {
                                return;
                            }
                        }
                    }
                }
            }));
        }
        let d2 = d.clone();
        tokio::spawn(async move {
            let status = child.wait().await;
            for r in readers {
                let _ = r.await;
            }
            let mut st = d2.state.lock();
            let x = st.execs.get_mut(&id).unwrap();
            x.running = false;
            x.exit = status.ok().and_then(|s| s.code()).unwrap_or(0);
            drop(st);
            drop(tx);
        });
        let stream = futures::stream::unfold(rx, |mut rx| async move {
            rx.recv().await.map(|b| (Ok::<_, std::io::Error>(b), rx))
        });
        Response::builder()
            .status(StatusCode::OK)
            .header("Content-Type", "application/vnd.docker.multiplexed-stream")
            .body(Body::from_stream(stream))
            .unwrap()
    }
}

struct DockerFixture {
    f: Fixture,
    d: Arc<FakeDocker>,
    _tmp: TempDir,
}

impl Drop for DockerFixture {
    fn drop(&mut self) {
        let unexpected = self.d.state.lock().unexpected.clone();
        if !std::thread::panicking() {
            assert!(
                unexpected.is_empty(),
                "unexpected docker calls {unexpected:?}"
            );
        }
    }
}

async fn new_docker_fixture() -> DockerFixture {
    let tmp = TempDir::new();
    let d = Arc::new(FakeDocker {
        tmp: tmp.path().to_string_lossy().into_owned(),
        ..Default::default()
    });
    let app = axum::Router::new().fallback(serve).with_state(d.clone());
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        let _ = axum::serve(listener, app).await;
    });
    let mut f = new_fixture();
    f.c = Connector::new_docker(&format!("tcp://{addr}"), vec![]);
    f.config.insert("image".into(), json!("goap/builder:1"));
    DockerFixture { f, d, _tmp: tmp }
}

#[test]
fn docker_info() {
    let info = Connector::new_docker("", vec![]).info();
    let schema = goap_pbconv::to_map(info.config_schema.as_ref());
    let req = schema["required"].as_array().unwrap();
    assert!(
        info.id == DOCKER_ID && req.contains(&json!("image")),
        "info {} {req:?}",
        info.id
    );
    assert!(
        schema["properties"].get("passEnv").is_none(),
        "the host environment never enters a container"
    );
}

// The commands of a workspace run in its container, created once, hardened, with the workspace mounted; the files
// stay on the host.
#[tokio::test(flavor = "multi_thread")]
async fn docker_runs_the_commands_in_a_container() {
    let df = new_docker_fixture().await;
    let f = &df.f;
    f.must(OP_WRITE, json!({"path": "src/a.txt", "content": "hello\n"}))
        .await;
    let out = f
        .must(
            OP_BASH,
            json!({"command": "cat a.txt; echo oops >&2; exit 3", "cwd": "src"}),
        )
        .await;
    assert!(
        out["stdout"] == "hello\n" && out["stderr"] == "oops\n" && out["exitCode"] == 3.0,
        "bash {out:?}"
    );
    // the commands write in the workspace
    f.must(
        OP_BASH,
        json!({"command": "echo built > out.txt", "cwd": "src"}),
    )
    .await;
    let got = f.must(OP_READ, json!({"path": "src/out.txt"})).await;
    assert!(
        got["text"].as_str().unwrap().contains("built"),
        "read {got:?}"
    );
    let created = df.d.state.lock().created.clone();
    assert_eq!(created.len(), 1, "{} containers", created.len());
    let c = &created[0];
    let host = &c["HostConfig"];
    assert!(
        c["Image"] == "goap/builder:1"
            && host["NetworkMode"] == "none"
            && host["ReadonlyRootfs"] == true
            && host["Binds"][0] == json!(format!("{}:{CONTAINER_DIR}", f.dir.to_string_lossy()))
            && host["CapDrop"].as_array().unwrap().contains(&json!("ALL")),
        "container {c:?}"
    );
    for e in c["Env"].as_array().unwrap() {
        let e = e.as_str().unwrap();
        assert!(
            !e.starts_with("PATH=") && !e.contains("s3cr3t"),
            "host environment or secret in the container: {:?}",
            c["Env"]
        );
    }
    let st = f.must(OP_STATUS, Value::Null).await;
    assert_eq!(st["container"]["status"], "running", "status {st:?}");
    f.must(OP_CLOSE, Value::Null).await;
    let name = container_name(&Space {
        id: "CHG-1".into(),
        ..Default::default()
    });
    let removed = df.d.state.lock().removed.clone();
    assert!(
        removed.contains(&name),
        "container not removed: {removed:?}"
    );
    assert!(std::fs::metadata(&f.dir).is_err(), "workspace kept");
}

#[tokio::test(flavor = "multi_thread")]
async fn docker_timeout_and_background() {
    let df = new_docker_fixture().await;
    let f = &df.f;
    let start = Instant::now();
    let out = f
        .must(OP_BASH, json!({"command": "sleep 30", "timeout": 1.0}))
        .await;
    assert!(
        out["timedOut"] == true && start.elapsed() <= Duration::from_secs(15),
        "timeout {out:?} after {:?}",
        start.elapsed()
    );
    let job = f
        .must(
            OP_BASH,
            json!({"command": "echo one; sleep 0.3; echo two", "background": true}),
        )
        .await;
    let id = job["id"].as_str().unwrap().to_string();
    let mut text = String::new();
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        let o = f.must(OP_BASH_OUTPUT, json!({ "id": id })).await;
        text += o["output"].as_str().unwrap();
        if o["running"] == false {
            assert_eq!(o["exitCode"], 0.0, "job {o:?}");
            break;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    assert_eq!(text, "one\ntwo\n", "output");
    let long = f
        .must(OP_BASH, json!({"command": "sleep 30", "background": true}))
        .await;
    f.must(OP_BASH_KILL, json!({"id": long["id"]})).await;
    let deadline = Instant::now() + Duration::from_secs(10);
    while Instant::now() < deadline {
        let o = f.must(OP_BASH_OUTPUT, json!({"id": long["id"]})).await;
        if o["running"] == false {
            return;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("the killed command still runs");
}

#[tokio::test(flavor = "multi_thread")]
async fn docker_needs_an_image() {
    let mut df = new_docker_fixture().await;
    df.f.config.remove("image");
    let err =
        df.f.call(OP_BASH, json!({"command": "true"}))
            .await
            .unwrap_err();
    assert!(err.to_string().contains("image"), "err {err}");
    // the call context names the workspace
    let ctx = with_call(&Ctx::background(), CallContext::default());
    assert!(
        df.f.c
            .invoke(
                &ctx,
                OP_BASH,
                obj(json!({"command": "true"})),
                df.f.config.clone(),
                HashMap::new()
            )
            .await
            .is_err(),
        "no change, no workspace"
    );
}
