use std::sync::Arc;
use std::time::Duration;

use goap_connect::Interceptors;
use goap_engine::{InprocSandboxes, Sandboxes};
use goap_platform::{env, env_int, parse_duration};

use crate::Error;
use crate::docker::DockerProvisioner;
use crate::kubernetes::KubernetesProvisioner;
use crate::pool::{Pool, Provisioner, Spec};
use crate::process::ProcessProvisioner;
use crate::runtime::Runtime;

/// What [`from_env`] builds: the sandbox provider of the engine and, for remote sandboxes, the Runtime to mount on the
/// engine server and the pool (to reap and close).
pub struct FromEnv {
    pub sandboxes: Arc<dyn Sandboxes>,
    pub runtime: Option<Arc<Runtime>>,
    pub pool: Option<Arc<Pool>>,
}

/// FromEnv builds the sandbox provider of the engine from the environment:
///
/// ```text
/// GOAP_SANDBOX              inproc (default) | process | docker | kubernetes
/// GOAP_RUNTIME_URL          engine URL reachable from sandboxes (RuntimeService)
/// GOAP_SANDBOX_IMAGE        goap-runner image (docker, kubernetes)
/// GOAP_SANDBOX_MEMORY_MB, GOAP_SANDBOX_CPUS, GOAP_SANDBOX_PIDS  limits
/// GOAP_SANDBOX_IDLE_TTL     stop idle sandboxes after this duration (10m)
/// GOAP_RUNNER_BIN, GOAP_SANDBOX_WRAPPER   process: binary and isolation wrapper
/// GOAP_DOCKER_HOST, GOAP_SANDBOX_NETWORK, GOAP_SANDBOX_RUNTIME   docker
/// GOAP_K8S_NAMESPACE, GOAP_SANDBOX_RUNTIME_CLASS                 kubernetes
/// ```
///
/// `http` and `options` are the HTTP client and the interceptors (tracing) of the calls to the sandboxes.
pub fn from_env(http: reqwest::Client, options: Interceptors) -> Result<FromEnv, Error> {
    let kind = env("GOAP_SANDBOX", "inproc");
    if kind == "inproc" {
        tracing::warn!(
            "script actions run in the engine process (GOAP_SANDBOX=inproc): not isolated, development only"
        );
        return Ok(FromEnv {
            sandboxes: Arc::new(InprocSandboxes),
            runtime: None,
            pool: None,
        });
    }
    let prov: Arc<dyn Provisioner> = match kind.as_str() {
        "process" => {
            let mut p = ProcessProvisioner::new(&env("GOAP_RUNNER_BIN", "goap-runner"));
            let w = std::env::var("GOAP_SANDBOX_WRAPPER").unwrap_or_default();
            if !w.is_empty() {
                p.wrapper = w.split_whitespace().map(String::from).collect();
            }
            Arc::new(p)
        }
        "docker" => {
            let mut d =
                DockerProvisioner::new(&env("GOAP_DOCKER_HOST", "unix:///var/run/docker.sock"));
            d.network = env("GOAP_SANDBOX_NETWORK", "goap-sandbox");
            d.runtime = std::env::var("GOAP_SANDBOX_RUNTIME").unwrap_or_default();
            Arc::new(d)
        }
        "kubernetes" => {
            let mut k = KubernetesProvisioner::default();
            k.namespace = std::env::var("GOAP_K8S_NAMESPACE").unwrap_or_default();
            k.runtime_class = std::env::var("GOAP_SANDBOX_RUNTIME_CLASS").unwrap_or_default();
            Arc::new(k)
        }
        _ => {
            return Err(Error::msg(format!(
                "unknown GOAP_SANDBOX {kind:?} (inproc, process, docker, kubernetes)"
            )));
        }
    };
    let runtime_url = std::env::var("GOAP_RUNTIME_URL").unwrap_or_default();
    if runtime_url.is_empty() {
        return Err(Error::msg(format!(
            "GOAP_RUNTIME_URL is required with GOAP_SANDBOX={kind}"
        )));
    }
    let rt = Arc::new(Runtime::new());
    let idle = parse_duration(&env("GOAP_SANDBOX_IDLE_TTL", "10m")).unwrap_or(Duration::ZERO);
    let mut pool = Pool::new(prov, rt.clone(), &runtime_url);
    pool.http = http;
    pool.options = options;
    pool.idle_ttl = idle;
    pool.template = Spec {
        image: std::env::var("GOAP_SANDBOX_IMAGE").unwrap_or_default(),
        memory_mb: env_int("GOAP_SANDBOX_MEMORY_MB", 512),
        pids: env_int("GOAP_SANDBOX_PIDS", 256),
        cpus: env_float("GOAP_SANDBOX_CPUS", 1.0),
        ..Default::default()
    };
    tracing::info!(provisioner = %kind, runtime = %runtime_url, "script sandboxes");
    let pool = Arc::new(pool);
    Ok(FromEnv {
        sandboxes: pool.clone(),
        runtime: Some(rt),
        pool: Some(pool),
    })
}

fn env_float(k: &str, def: f64) -> f64 {
    std::env::var(k)
        .ok()
        .and_then(|v| v.parse().ok())
        .unwrap_or(def)
}
