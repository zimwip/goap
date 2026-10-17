//! The commands of a workspace in a container of its own, through the Docker Engine API (`docker.go`).

use std::collections::BTreeMap;
use std::fmt;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

use goap_connect::{Ctx, async_trait};
use goap_connectorkit::InvokeError;
use serde_json::{Map, Value, json};
use tokio::sync::{Mutex, OnceCell};

use crate::runner::{Output, Process, Runner};
use crate::shell::{unit_vars, workdir};
use crate::workspace::{Space, rel, to_map};

// Defaults of the containers of the docker connector.
pub const DEFAULT_CONTAINER_MEMORY_MB: f64 = 2048.0;
pub const DEFAULT_CONTAINER_CPUS: f64 = 2.0;
pub const DEFAULT_CONTAINER_PIDS: f64 = 1024.0;
/// ContainerDir is where the workspace is mounted in its container.
pub const CONTAINER_DIR: &str = "/workspace";

/// dockerRunner runs the commands of a workspace in a container of its own (ADR 0118 §7): created at the first
/// command from the image the unit configures, the directory of the workspace mounted at /workspace, no network by
/// default, every capability dropped, no privilege escalation, a read-only root file system, limits; the commands
/// are `docker exec`s in it. The files and git stay on the host: the connector reads, writes and searches the
/// directory itself and clones and pushes with the token, which never enters the container.
pub(crate) struct DockerRunner {
    api: Arc<DockerApi>,
    /// serializes the creation of the containers
    mu: Mutex<()>,
    seq: AtomicU64,
}

impl DockerRunner {
    pub(crate) fn new(host: &str) -> Self {
        DockerRunner {
            api: Arc::new(DockerApi {
                host: host.into(),
                client: OnceCell::new(),
            }),
            mu: Mutex::new(()),
            seq: AtomicU64::new(0),
        }
    }
}

/// containerName is the name of the container of a workspace.
pub(crate) fn container_name(s: &Space) -> String {
    format!("goap-ws-{}", s.id.trim_start_matches(['.', '_', '-']))
}

/// containerEnv is the environment of the commands in the container: the image's, plus what the unit sets, the
/// identity of the commits and the workspace. Nothing of the host.
pub(crate) fn container_env(s: &Space) -> Vec<String> {
    let mut vars = BTreeMap::new();
    unit_vars(s, &mut vars);
    // the root file system is read-only: the home of the toolchains (caches, settings) is in the workspace
    vars.insert("HOME".into(), format!("{CONTAINER_DIR}/.home"));
    vars.insert("GOAP_WORKSPACE".into(), CONTAINER_DIR.into());
    vars.insert("GOAP_CHANGE".into(), s.change.clone());
    vars.insert("TERM".into(), "dumb".into());
    vars.insert("GIT_TERMINAL_PROMPT".into(), "0".into());
    vars.into_iter().map(|(k, v)| format!("{k}={v}")).collect()
}

/// A positive number of the configuration, else def.
fn number(v: Option<&Value>, def: f64) -> f64 {
    match v.and_then(Value::as_f64) {
        Some(n) if n > 0.0 => n,
        _ => def,
    }
}

/// user is the user of the commands: the one the unit configures, else the connector's own, so that the files the
/// commands write are the connector's to read, change and remove.
fn user(s: &Space) -> String {
    if let Some(u) = s.config.get("user").and_then(Value::as_str)
        && !u.is_empty()
    {
        return u.into();
    }
    host_user()
}

#[cfg(unix)]
fn host_user() -> String {
    format!("{}:{}", nix::unistd::getuid(), nix::unistd::getgid())
}

#[cfg(not(unix))]
fn host_user() -> String {
    String::new()
}

/// `url.PathEscape` / `url.QueryEscape` of a name: every byte but the unreserved ones percent-encoded.
fn escape(s: &str) -> String {
    let mut out = String::new();
    for b in s.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

impl DockerRunner {
    /// container returns the id of the running container of a workspace, creating or starting it when needed.
    async fn container(&self, ctx: &Ctx, s: &Space) -> Result<String, InvokeError> {
        let image = s.config.get("image").and_then(Value::as_str).unwrap_or("");
        if image.is_empty() {
            return Err(
                "the adapter has no \"image\" configured for the containers of the workspaces"
                    .into(),
            );
        }
        let _guard = self.mu.lock().await;
        let name = container_name(s);
        match self
            .api
            .call(
                ctx,
                "GET",
                &format!("/containers/{}/json", escape(&name)),
                None,
            )
            .await
        {
            Ok(info) => {
                let id = info["Id"].as_str().unwrap_or("").to_string();
                if info["State"]["Running"].as_bool() == Some(true) {
                    return Ok(id);
                }
                self.api
                    .call(ctx, "POST", &format!("/containers/{id}/start"), None)
                    .await?;
                return Ok(id);
            }
            Err(e) if !is_not_found(&e) => return Err(e),
            Err(_) => {}
        }
        std::fs::create_dir_all(s.local(".home"))?;
        let network = match s.config.get("network").and_then(Value::as_str) {
            None | Some("") => "none",
            Some(n) => n,
        };
        let host = json!({
            "Binds": [format!("{}:{CONTAINER_DIR}", s.dir_str())],
            "NetworkMode": network,
            "ReadonlyRootfs": true,
            "CapDrop": ["ALL"],
            "SecurityOpt": ["no-new-privileges"],
            "Tmpfs": {"/tmp": "rw,nosuid,size=512m"},
            "Init": true,
            "Memory": (number(s.config.get("memoryMB"), DEFAULT_CONTAINER_MEMORY_MB) as i64) * 1024 * 1024,
            "NanoCpus": (number(s.config.get("cpus"), DEFAULT_CONTAINER_CPUS) * 1e9) as i64,
            "PidsLimit": number(s.config.get("pids"), DEFAULT_CONTAINER_PIDS) as i64,
        });
        let body = json!({
            "Image": image, "User": user(s), "WorkingDir": CONTAINER_DIR, "Env": container_env(s),
            "Cmd": ["sleep", "infinity"],
            "Labels": {"goap.workspace": s.change},
            "HostConfig": host,
        });
        let created = self
            .api
            .call(
                ctx,
                "POST",
                &format!("/containers/create?name={}", escape(&name)),
                Some(body),
            )
            .await?;
        let id = created["Id"].as_str().unwrap_or("").to_string();
        if let Err(e) = self
            .api
            .call(ctx, "POST", &format!("/containers/{id}/start"), None)
            .await
        {
            let _ = self
                .api
                .call(
                    &Ctx::background(),
                    "DELETE",
                    &format!("/containers/{id}?force=true"),
                    None,
                )
                .await;
            return Err(e);
        }
        Ok(id)
    }
}

/// wrap is the command line run by an exec: it records its pid (to be killed) and runs the line with bash when the
/// image has it, else sh.
const WRAP: &str = r#"echo $$ > "$GOAP_PIDFILE"; if command -v bash >/dev/null 2>&1; then exec bash -c "$1"; else exec sh -c "$1"; fi"#;

struct DockerProcess {
    api: Arc<DockerApi>,
    ctx: Ctx,
    container: String,
    exec: String,
    pidfile: String,
    body: Option<reqwest::Response>,
    stdout: Arc<dyn Output>,
    stderr: Arc<dyn Output>,
}

#[async_trait]
impl Runner for DockerRunner {
    async fn start(
        &self,
        ctx: &Ctx,
        s: &Space,
        line: &str,
        cwd: &str,
        stdout: Arc<dyn Output>,
        stderr: Arc<dyn Output>,
    ) -> Result<Box<dyn Process>, InvokeError> {
        workdir(s, cwd)?;
        let id = self.container(ctx, s).await?;
        let seq = self.seq.fetch_add(1, Ordering::SeqCst) + 1;
        let pidfile = format!("/tmp/.goap-cmd-{seq}.pid");
        let mut env = container_env(s);
        env.push(format!("GOAP_PIDFILE={pidfile}"));
        let r = rel(cwd);
        let dir = if r == "." {
            CONTAINER_DIR.to_string()
        } else {
            format!("{CONTAINER_DIR}/{r}")
        };
        let exec = json!({"AttachStdout": true, "AttachStderr": true, "Tty": false, "User": user(s),
            "WorkingDir": dir, "Env": env,
            "Cmd": ["/bin/sh", "-c", WRAP, "goap", line]});
        let created = self
            .api
            .call(ctx, "POST", &format!("/containers/{id}/exec"), Some(exec))
            .await?;
        let exec_id = created["Id"].as_str().unwrap_or("").to_string();
        let body = self
            .api
            .stream(
                &format!("/exec/{exec_id}/start"),
                json!({"Detach": false, "Tty": false}),
            )
            .await?;
        Ok(Box::new(DockerProcess {
            api: self.api.clone(),
            ctx: ctx.clone(),
            container: id,
            exec: exec_id,
            pidfile,
            body: Some(body),
            stdout,
            stderr,
        }))
    }

    async fn remove(&self, ctx: &Ctx, s: &Space) -> Result<(), InvokeError> {
        match self
            .api
            .call(
                ctx,
                "DELETE",
                &format!("/containers/{}?force=true", escape(&container_name(s))),
                None,
            )
            .await
        {
            Err(e) if is_not_found(&e) => Ok(()),
            Err(e) => Err(e),
            Ok(_) => Ok(()),
        }
    }

    async fn status(&self, ctx: &Ctx, s: &Space) -> Option<Map<String, Value>> {
        let name = container_name(s);
        match self
            .api
            .call(
                ctx,
                "GET",
                &format!("/containers/{}/json", escape(&name)),
                None,
            )
            .await
        {
            Err(_) => Some(to_map(json!({"name": name, "status": "none"}))),
            Ok(info) => Some(to_map(json!({"name": name,
                "image": info["Config"]["Image"].as_str().unwrap_or(""),
                "status": info["State"]["Status"].as_str().unwrap_or("")}))),
        }
    }
}

impl DockerProcess {
    /// kill kills the command and what it started (its children).
    async fn kill(&self) {
        let ctx = Ctx::background();
        let (ctx, _cancel) = ctx.with_timeout(Duration::from_secs(10));
        let script = format!(
            r#"p=$(cat "{}" 2>/dev/null) && {{ pkill -KILL -P "$p" 2>/dev/null; kill -KILL "$p"; }}"#,
            self.pidfile
        );
        if let Ok(created) = self
            .api
            .call(
                &ctx,
                "POST",
                &format!("/containers/{}/exec", self.container),
                Some(json!({"Cmd": ["/bin/sh", "-c", script]})),
            )
            .await
        {
            let id = created["Id"].as_str().unwrap_or("");
            let _ = self
                .api
                .call(
                    &ctx,
                    "POST",
                    &format!("/exec/{id}/start"),
                    Some(json!({"Detach": true})),
                )
                .await;
        }
    }
}

#[async_trait]
impl Process for DockerProcess {
    async fn wait(self: Box<Self>) -> Result<i32, InvokeError> {
        let mut p = *self;
        let Some(mut body) = p.body.take() else {
            return Ok(-1);
        };
        let ctx = p.ctx.clone();
        let (stdout, stderr) = (p.stdout.clone(), p.stderr.clone());
        let mut killed = false;
        let demuxed = tokio::select! {
            r = demux(&mut body, &*stdout, &*stderr) => r,
            _ = ctx.done() => {
                killed = true;
                p.kill().await;
                Ok(())
            }
        };
        drop(body); // stops reading the outputs
        let (ictx, _cancel) = Ctx::background().with_timeout(Duration::from_secs(30));
        let mut info;
        let mut i = 0;
        loop {
            info = p
                .api
                .call(&ictx, "GET", &format!("/exec/{}/json", p.exec), None)
                .await?;
            if info["Running"].as_bool() != Some(true) || i >= 50 {
                break;
            }
            i += 1;
            // the stream ended a moment before the exit is recorded
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
        if killed {
            return Ok(-1);
        }
        demuxed?;
        Ok(info["ExitCode"].as_i64().unwrap_or(0) as i32)
    }
}

/// demux copies the multiplexed stream of an exec without a terminal (8-byte headers: stream, 0, 0, 0, size) to
/// stdout and stderr, until its end.
async fn demux(
    r: &mut reqwest::Response,
    stdout: &dyn Output,
    stderr: &dyn Output,
) -> Result<(), InvokeError> {
    let mut buf: Vec<u8> = Vec::new();
    loop {
        // whole frames first
        while buf.len() >= 8 {
            let size = u32::from_be_bytes([buf[4], buf[5], buf[6], buf[7]]) as usize;
            if buf.len() < 8 + size {
                break;
            }
            let w = if buf[0] == 2 { stderr } else { stdout };
            w.write(&buf[8..8 + size]);
            buf.drain(..8 + size);
        }
        match r.chunk().await {
            Ok(Some(chunk)) => buf.extend_from_slice(&chunk),
            // the end of the stream, a frame cut included
            Ok(None) => {
                if buf.len() > 8 {
                    let w = if buf[0] == 2 { stderr } else { stdout };
                    w.write(&buf[8..]);
                }
                return Ok(());
            }
            Err(e) if e.is_body() || e.is_decode() => return Ok(()),
            Err(e) => return Err(Box::new(e)),
        }
    }
}

// ---- the Docker Engine API ----------------------------------------------------------------------------------

const DOCKER_VERSION: &str = "/v1.43";

/// A call of the Docker API answered 404.
#[derive(Debug)]
pub(crate) struct NotFound(String);

impl fmt::Display for NotFound {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for NotFound {}

fn is_not_found(e: &InvokeError) -> bool {
    e.downcast_ref::<NotFound>().is_some()
}

pub(crate) struct DockerApi {
    host: String,
    client: OnceCell<Result<(reqwest::Client, String), String>>,
}

impl DockerApi {
    async fn http(&self) -> Result<(reqwest::Client, String), InvokeError> {
        let r = self
            .client
            .get_or_init(|| async {
                let host = if self.host.is_empty() {
                    "unix:///var/run/docker.sock"
                } else {
                    self.host.as_str()
                };
                let (scheme, rest) = host
                    .split_once("://")
                    .ok_or_else(|| format!("unsupported docker host {host:?}"))?;
                match scheme {
                    #[cfg(unix)]
                    "unix" => {
                        let client = reqwest::Client::builder()
                            .unix_socket(std::path::PathBuf::from(rest))
                            .build()
                            .map_err(|e| e.to_string())?;
                        Ok((client, format!("http://docker{DOCKER_VERSION}")))
                    }
                    "tcp" | "http" => {
                        let authority = rest.split('/').next().unwrap_or(rest);
                        let client = reqwest::Client::builder()
                            .no_proxy()
                            .build()
                            .map_err(|e| e.to_string())?;
                        Ok((client, format!("http://{authority}{DOCKER_VERSION}")))
                    }
                    _ => Err(format!("unsupported docker host {host:?}")),
                }
            })
            .await;
        r.clone().map_err(Into::into)
    }

    async fn request(
        &self,
        method: &str,
        p: &str,
        body: Option<Value>,
    ) -> Result<reqwest::Response, InvokeError> {
        let (hc, base) = self.http().await?;
        let method = reqwest::Method::from_bytes(method.as_bytes())?;
        let mut req = hc
            .request(method, format!("{base}{p}"))
            .header("Content-Type", "application/json");
        if let Some(b) = body {
            req = req.body(serde_json::to_vec(&b)?);
        }
        Ok(req.send().await?)
    }

    /// One call: its JSON answer (Null when it has none).
    pub(crate) async fn call(
        &self,
        ctx: &Ctx,
        method: &str,
        p: &str,
        body: Option<Value>,
    ) -> Result<Value, InvokeError> {
        let call = async {
            let resp = self.request(method, p, body).await?;
            let status = resp.status();
            let raw = resp.bytes().await.unwrap_or_default();
            let text = String::from_utf8_lossy(&raw).trim().to_string();
            if status == reqwest::StatusCode::NOT_FOUND {
                return Err(
                    Box::new(NotFound(format!("docker {method} {p}: not found: {text}")))
                        as InvokeError,
                );
            }
            if status.as_u16() >= 300 && status != reqwest::StatusCode::NOT_MODIFIED {
                return Err(format!("docker {method} {p}: {status}: {text}").into());
            }
            if raw.is_empty() {
                return Ok(Value::Null);
            }
            Ok(serde_json::from_slice(&raw)?)
        };
        tokio::select! {
            r = call => r,
            e = ctx.done() => Err(Box::new(e)),
        }
    }

    /// stream posts a request whose answer is a stream read until its end (the outputs of an exec). The request is
    /// not bounded by a context: the caller drops the stream.
    async fn stream(&self, p: &str, body: Value) -> Result<reqwest::Response, InvokeError> {
        let resp = self.request("POST", p, Some(body)).await?;
        let status = resp.status();
        if status.as_u16() >= 300 {
            let raw = resp.bytes().await.unwrap_or_default();
            return Err(format!(
                "docker POST {p}: {status}: {}",
                String::from_utf8_lossy(&raw).trim()
            )
            .into());
        }
        Ok(resp)
    }
}
