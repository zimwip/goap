use std::sync::OnceLock;

use async_trait::async_trait;
use goap_ctx::Ctx;
use reqwest::Method;
use serde_json::{Map, Value, json};

use crate::pool::{Instance, Provisioner, Spec};
use crate::{Error, escape, with_ctx};

const DOCKER_API: &str = "/v1.43";

/// DockerProvisioner starts one container per sandbox through the Docker Engine API. Containers are hardened:
/// read-only root file system, no capabilities, no privilege escalation, non-root user, resource limits and an
/// internal network where only the engine is reachable.
///
/// The engine needs access to the Docker API: prefer a socket proxy limited to the containers endpoints over
/// mounting /var/run/docker.sock.
#[derive(Default)]
pub struct DockerProvisioner {
    /// Host is the Docker API address: "unix:///var/run/docker.sock" or "tcp://proxy:2375".
    pub host: String,
    /// Network the sandbox joins (must reach the engine RuntimeService).
    pub network: String,
    /// Runtime is an optional OCI runtime (e.g. "runsc" for gVisor).
    pub runtime: String,
    /// User inside the container.
    pub user: String,
    client: OnceLock<Result<(reqwest::Client, String), String>>,
}

impl DockerProvisioner {
    pub fn new(host: &str) -> Self {
        DockerProvisioner {
            host: host.to_string(),
            ..Default::default()
        }
    }

    fn http(&self) -> Result<(reqwest::Client, String), Error> {
        self.client
            .get_or_init(|| {
                let host = if self.host.is_empty() {
                    "unix:///var/run/docker.sock"
                } else {
                    &self.host
                };
                let Some((scheme, rest)) = host.split_once("://") else {
                    return Err(format!("unsupported docker host {host:?}"));
                };
                match scheme {
                    #[cfg(unix)]
                    "unix" => reqwest::Client::builder()
                        .unix_socket(std::path::PathBuf::from(rest))
                        .build()
                        .map(|c| (c, format!("http://docker{DOCKER_API}")))
                        .map_err(|e| e.to_string()),
                    "tcp" | "http" => {
                        let authority = rest.split('/').next().unwrap_or_default();
                        Ok((
                            reqwest::Client::new(),
                            format!("http://{authority}{DOCKER_API}"),
                        ))
                    }
                    _ => Err(format!("unsupported docker host {host:?}")),
                }
            })
            .clone()
            .map_err(Error::Msg)
    }

    async fn call(
        &self,
        ctx: &Ctx,
        method: Method,
        path: &str,
        body: Option<&Value>,
    ) -> Result<Vec<u8>, Error> {
        let (hc, base) = self.http()?;
        let mut req = hc
            .request(method.clone(), format!("{base}{path}"))
            .header("Content-Type", "application/json");
        if let Some(b) = body {
            req = req.body(serde_json::to_vec(b)?);
        }
        let resp = with_ctx(ctx, req.send()).await?;
        let status = resp.status();
        let raw = with_ctx(ctx, resp.bytes()).await?;
        if status.as_u16() >= 300 && status != reqwest::StatusCode::NOT_MODIFIED {
            return Err(Error::Status {
                code: status.as_u16(),
                message: format!(
                    "docker {method} {path}: {status}: {}",
                    String::from_utf8_lossy(&raw).trim()
                ),
            });
        }
        Ok(raw.to_vec())
    }
}

#[async_trait]
impl Provisioner for DockerProvisioner {
    fn name(&self) -> &str {
        "docker"
    }

    async fn start(&self, ctx: &Ctx, spec: Spec) -> Result<Instance, Error> {
        if spec.image.is_empty() {
            return Err(Error::msg(
                "docker provisioner: sandbox image not configured",
            ));
        }
        let user = if self.user.is_empty() {
            "65532:65532"
        } else {
            &self.user
        };
        let mut host = Map::new();
        host.insert("NetworkMode".into(), json!(self.network));
        host.insert("ReadonlyRootfs".into(), json!(true));
        host.insert("CapDrop".into(), json!(["ALL"]));
        host.insert("SecurityOpt".into(), json!(["no-new-privileges"]));
        host.insert("Tmpfs".into(), json!({"/tmp": "rw,noexec,nosuid,size=64m"}));
        host.insert("AutoRemove".into(), json!(true));
        if spec.memory_mb > 0 {
            host.insert("Memory".into(), json!(spec.memory_mb * 1024 * 1024));
        }
        if spec.cpus > 0.0 {
            host.insert("NanoCpus".into(), json!((spec.cpus * 1e9) as i64));
        }
        if spec.pids > 0 {
            host.insert("PidsLimit".into(), json!(spec.pids));
        }
        if !self.runtime.is_empty() {
            host.insert("Runtime".into(), json!(self.runtime));
        }
        let body = json!({
            "Image": spec.image,
            "User": user,
            "Env": ["GOAP_HTTP_ADDR=:8080", format!("GOAP_SANDBOX_ID={}", spec.id)],
            "Labels": {"goap.sandbox": spec.id, "goap.process": spec.process_id},
            "HostConfig": host,
        });
        let raw = self
            .call(
                ctx,
                Method::POST,
                &format!("/containers/create?name={}", escape(&spec.id)),
                Some(&body),
            )
            .await?;
        let created: Value = if raw.is_empty() {
            Value::Null
        } else {
            serde_json::from_slice(&raw)?
        };
        let id = created
            .get("Id")
            .and_then(Value::as_str)
            .unwrap_or_default()
            .to_string();
        if let Err(e) = self
            .call(ctx, Method::POST, &format!("/containers/{id}/start"), None)
            .await
        {
            let _ = self
                .call(
                    &Ctx::background(),
                    Method::DELETE,
                    &format!("/containers/{id}?force=true"),
                    None,
                )
                .await;
            return Err(e);
        }
        // the container name resolves on the sandbox network
        Ok(Instance {
            endpoint: format!("http://{}:8080", spec.id),
            id: spec.id,
        })
    }

    async fn stop(&self, ctx: &Ctx, id: &str) -> Result<(), Error> {
        self.call(
            ctx,
            Method::DELETE,
            &format!("/containers/{}?force=true", escape(id)),
            None,
        )
        .await
        .map(|_| ())
    }
}
