use std::sync::OnceLock;
use std::time::Duration;

use async_trait::async_trait;
use goap_ctx::Ctx;
use reqwest::Method;
use serde_json::{Map, Value, json};

use crate::pool::{Instance, Provisioner, Spec};
use crate::{Error, with_ctx};

const SA_DIR: &str = "/var/run/secrets/kubernetes.io/serviceaccount";

/// The API server, token, namespace and client in force.
#[derive(Clone)]
struct Resolved {
    api: String,
    token: String,
    namespace: String,
    http: reqwest::Client,
}

/// KubernetesProvisioner starts one pod per sandbox through the Kubernetes API (in-cluster service account). Pods
/// run non-root with a read-only root file system, no service account token, dropped capabilities, the RuntimeDefault
/// seccomp profile and optional RuntimeClass (gVisor, Kata). A NetworkPolicy (deploy/k8s/sandbox-networkpolicy.yaml)
/// must restrict their egress to the engine.
#[derive(Default)]
pub struct KubernetesProvisioner {
    /// APIServer, Token and CA default to the in-cluster configuration.
    pub api_server: String,
    pub token: String,
    pub ca_file: String,
    pub namespace: String,
    pub runtime_class: String,
    /// HTTP overrides the API client (tests).
    pub http: Option<reqwest::Client>,
    resolved: OnceLock<Result<Resolved, String>>,
}

impl KubernetesProvisioner {
    fn init(&self) -> Result<Resolved, Error> {
        self.resolved
            .get_or_init(|| self.resolve().map_err(|e| e.to_string()))
            .clone()
            .map_err(Error::Msg)
    }

    fn resolve(&self) -> Result<Resolved, Error> {
        let mut api = self.api_server.clone();
        if api.is_empty() {
            let host = std::env::var("KUBERNETES_SERVICE_HOST").unwrap_or_default();
            let port = std::env::var("KUBERNETES_SERVICE_PORT").unwrap_or_default();
            if host.is_empty() {
                return Err(Error::msg(
                    "kubernetes provisioner: not running in a cluster and no API server configured",
                ));
            }
            api = format!("https://{host}:{port}");
        }
        let mut token = self.token.clone();
        if token.is_empty() {
            token = std::fs::read_to_string(format!("{SA_DIR}/token"))?
                .trim()
                .to_string();
        }
        let mut namespace = self.namespace.clone();
        if namespace.is_empty() {
            namespace = match std::fs::read_to_string(format!("{SA_DIR}/namespace")) {
                Ok(b) => b.trim().to_string(),
                Err(_) => "default".to_string(),
            };
        }
        let http = match &self.http {
            Some(h) => h.clone(),
            None => {
                let ca = if self.ca_file.is_empty() {
                    format!("{SA_DIR}/ca.crt")
                } else {
                    self.ca_file.clone()
                };
                let pem = std::fs::read(ca)?;
                let certs = reqwest::Certificate::from_pem_bundle(&pem)?;
                reqwest::Client::builder()
                    .timeout(Duration::from_secs(30))
                    .tls_certs_only(certs)
                    .tls_version_min(reqwest::tls::Version::TLS_1_2)
                    .build()?
            }
        };
        Ok(Resolved {
            api,
            token,
            namespace,
            http,
        })
    }

    async fn call(
        &self,
        ctx: &Ctx,
        method: Method,
        path: &str,
        body: Option<&Value>,
    ) -> Result<Vec<u8>, Error> {
        let k = self.init()?;
        let mut req = k
            .http
            .request(
                method.clone(),
                format!("{}{path}", k.api.trim_end_matches('/')),
            )
            .header("Content-Type", "application/json");
        if !k.token.is_empty() {
            req = req.header("Authorization", format!("Bearer {}", k.token));
        }
        if let Some(b) = body {
            req = req.body(serde_json::to_vec(b)?);
        }
        let resp = with_ctx(ctx, req.send()).await?;
        let status = resp.status();
        let raw = with_ctx(ctx, resp.bytes()).await?;
        if status.as_u16() >= 300 {
            return Err(Error::Status {
                code: status.as_u16(),
                message: format!(
                    "kubernetes {method} {path}: {status}: {}",
                    String::from_utf8_lossy(&raw).trim()
                ),
            });
        }
        Ok(raw.to_vec())
    }

    fn namespace(&self) -> String {
        self.init().map(|k| k.namespace).unwrap_or_default()
    }
}

#[async_trait]
impl Provisioner for KubernetesProvisioner {
    fn name(&self) -> &str {
        "kubernetes"
    }

    /// Start implements Provisioner: create the pod and wait for its IP.
    async fn start(&self, ctx: &Ctx, spec: Spec) -> Result<Instance, Error> {
        if spec.image.is_empty() {
            return Err(Error::msg(
                "kubernetes provisioner: sandbox image not configured",
            ));
        }
        self.init()?;
        let ns = self.namespace();
        let mut limits = Map::new();
        if spec.memory_mb > 0 {
            limits.insert("memory".into(), json!(format!("{}Mi", spec.memory_mb)));
        }
        if spec.cpus > 0.0 {
            limits.insert(
                "cpu".into(),
                json!(format!("{}m", (spec.cpus * 1000.0) as i64)),
            );
        }
        let mut pod_spec = json!({
            "restartPolicy": "Never",
            "automountServiceAccountToken": false,
            "enableServiceLinks": false,
            "securityContext": {
                "runAsNonRoot": true, "runAsUser": 65532, "runAsGroup": 65532,
                "seccompProfile": {"type": "RuntimeDefault"},
            },
            "containers": [{
                "name": "runner",
                "image": spec.image,
                "env": [{"name": "GOAP_HTTP_ADDR", "value": ":8080"}, {"name": "GOAP_SANDBOX_ID", "value": spec.id}],
                "ports": [{"containerPort": 8080}],
                "resources": {"limits": limits},
                "securityContext": {
                    "allowPrivilegeEscalation": false, "readOnlyRootFilesystem": true,
                    "capabilities": {"drop": ["ALL"]},
                },
                "volumeMounts": [{"name": "tmp", "mountPath": "/tmp"}],
            }],
            "volumes": [{"name": "tmp", "emptyDir": {"medium": "Memory", "sizeLimit": "64Mi"}}],
        });
        if !self.runtime_class.is_empty() {
            pod_spec["runtimeClassName"] = json!(self.runtime_class);
        }
        let pod = json!({
            "apiVersion": "v1", "kind": "Pod",
            "metadata": {"name": spec.id, "labels": {"app.kubernetes.io/name": "goap-sandbox", "goap.io/process": spec.process_id}},
            "spec": pod_spec,
        });
        self.call(
            ctx,
            Method::POST,
            &format!("/api/v1/namespaces/{ns}/pods"),
            Some(&pod),
        )
        .await?;
        loop {
            let raw = self
                .call(
                    ctx,
                    Method::GET,
                    &format!("/api/v1/namespaces/{ns}/pods/{}", spec.id),
                    None,
                )
                .await?;
            let got: Value = serde_json::from_slice(&raw)?;
            let phase = got["status"]["phase"].as_str().unwrap_or_default();
            let ip = got["status"]["podIP"].as_str().unwrap_or_default();
            if phase == "Running" && !ip.is_empty() {
                return Ok(Instance {
                    id: spec.id.clone(),
                    endpoint: format!("http://{ip}:8080"),
                });
            }
            if phase == "Failed" || phase == "Succeeded" {
                let _ = self.stop(&Ctx::background(), &spec.id).await;
                return Err(Error::msg(format!(
                    "sandbox pod {} ended ({phase})",
                    spec.id
                )));
            }
            tokio::select! {
                e = ctx.done() => {
                    let _ = self.stop(&Ctx::background(), &spec.id).await;
                    return Err(Error::Ctx(e));
                }
                _ = tokio::time::sleep(Duration::from_millis(500)) => {}
            }
        }
    }

    async fn stop(&self, ctx: &Ctx, id: &str) -> Result<(), Error> {
        let ns = self.namespace();
        match self
            .call(
                ctx,
                Method::DELETE,
                &format!("/api/v1/namespaces/{ns}/pods/{id}?gracePeriodSeconds=0"),
                None,
            )
            .await
        {
            Err(Error::Status { code: 404, .. }) => Ok(()),
            r => r.map(|_| ()),
        }
    }
}
