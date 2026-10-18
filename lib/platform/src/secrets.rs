//! Secrets from HashiCorp Vault (KV v2), the environment, or a local file in development.
use std::{collections::BTreeMap, path::PathBuf, time::Duration};

use goap_ctx::Ctx;
use serde::Deserialize;

use crate::{PlatformError, Result, env};

/// Secrets reads secrets from HashiCorp Vault (KV v2) and falls back to environment variables. A secret reference is
/// `<path>#<field>`, e.g. `goap/modelgw#anthropic_api_key`.
#[derive(Debug)]
pub struct Secrets {
    pub addr: String,
    pub token: String,
    pub mount: String,
    /// A local fallback store (one JSON file, `dir/secrets.json`) used by [`Secrets::put`] / [`Secrets::resolve`] when
    /// Vault is not configured (`addr` empty): a devlocal convenience (ADR 0010) so an admin can still have a raw key
    /// vaulted for them without running a real Vault. Empty: no local fallback, `put` then fails and `resolve` leaves
    /// the reference unresolved.
    pub dir: String,
    http: reqwest::Client,
    mu: tokio::sync::Mutex<()>,
}

impl Default for Secrets {
    fn default() -> Self {
        Secrets {
            addr: String::new(),
            token: String::new(),
            mount: String::new(),
            dir: String::new(),
            http: reqwest::Client::builder()
                .timeout(Duration::from_secs(5))
                .build()
                .unwrap_or_default(),
            mu: tokio::sync::Mutex::new(()),
        }
    }
}

#[derive(Deserialize)]
struct VaultBody {
    #[serde(default)]
    data: VaultData,
}

#[derive(Deserialize, Default)]
struct VaultData {
    #[serde(default)]
    data: serde_json::Map<String, serde_json::Value>,
}

type LocalStore = BTreeMap<String, BTreeMap<String, String>>;

impl Secrets {
    /// Configures Vault from `VAULT_ADDR` / `VAULT_TOKEN` / `VAULT_KV_MOUNT` (default `secret`). Vault is disabled when
    /// `VAULT_ADDR` is empty.
    pub fn new() -> Self {
        Secrets {
            addr: std::env::var("VAULT_ADDR").unwrap_or_default(),
            token: std::env::var("VAULT_TOKEN").unwrap_or_default(),
            mount: env("VAULT_KV_MOUNT", "secret"),
            ..Default::default()
        }
    }

    /// The secrets of the Vault at `addr`.
    pub fn vault(addr: &str, token: &str, mount: &str) -> Self {
        Secrets {
            addr: addr.to_string(),
            token: token.to_string(),
            mount: mount.to_string(),
            ..Default::default()
        }
    }

    /// The secrets of the local store of `dir`, no Vault.
    pub fn local(dir: &str) -> Self {
        Secrets {
            dir: dir.to_string(),
            ..Default::default()
        }
    }

    /// Returns the secret, looking at Vault first then at the environment variable `env_fallback`.
    pub async fn get(&self, ctx: &Ctx, reference: &str, env_fallback: &str) -> Result<String> {
        if !self.addr.is_empty() && !reference.is_empty() {
            let r = self.read_vault(ctx, reference).await;
            match r {
                Ok(v) if !v.is_empty() => return Ok(v),
                Ok(v) if env_fallback.is_empty() => return Ok(v),
                Err(e) if env_fallback.is_empty() => return Err(e),
                _ => {}
            }
        }
        Ok(std::env::var(env_fallback).unwrap_or_default())
    }

    fn url(&self, path: &str) -> String {
        format!(
            "{}/v1/{}/data/{}",
            self.addr.trim_end_matches('/'),
            self.mount,
            path
        )
    }

    async fn read_vault(&self, _ctx: &Ctx, reference: &str) -> Result<String> {
        let (path, field) = split_ref(reference)?;
        let resp = self
            .http
            .get(self.url(path))
            .header("X-Vault-Token", &self.token)
            .send()
            .await?;
        if resp.status() != reqwest::StatusCode::OK {
            return Err(PlatformError::Other(format!(
                "vault {path}: {}",
                resp.status()
            )));
        }
        let body: VaultBody = serde_json::from_slice(&resp.bytes().await?)?;
        Ok(body
            .data
            .data
            .get(field)
            .and_then(|v| v.as_str())
            .unwrap_or_default()
            .to_string())
    }

    /// Writes `value` under `field` of `path`: in Vault (KV v2) when configured, else in the local fallback store
    /// (`dir`). A clear error when neither is configured, so the caller can fall back to `env:VAR`.
    pub async fn put(&self, _ctx: &Ctx, path: &str, field: &str, value: &str) -> Result<()> {
        if self.addr.is_empty() {
            if self.dir.is_empty() {
                return Err(PlatformError::Other(
                    "vault is not configured (VAULT_ADDR): store the key in an environment variable instead (env:VAR)".into(),
                ));
            }
            return self.local_put(path, field, value).await;
        }
        let body = serde_json::json!({ "data": { field: value } });
        let resp = self
            .http
            .post(self.url(path))
            .header("X-Vault-Token", &self.token)
            .json(&body)
            .send()
            .await?;
        let st = resp.status();
        if st != reqwest::StatusCode::OK && st != reqwest::StatusCode::NO_CONTENT {
            return Err(PlatformError::Other(format!("vault {path}: {st}")));
        }
        Ok(())
    }

    /// Resolves a secret reference: `env:<VAR>` reads the environment, anything else is a Vault reference
    /// `<path>#<field>` (the local store without Vault). Alternatives separated by `|` are tried in order and the
    /// first non-empty value wins (`goap/modelgw#anthropic_api_key | env:ANTHROPIC_API_KEY`); nothing found: the
    /// last error, or an empty value.
    pub async fn resolve(&self, ctx: &Ctx, reference: &str) -> Result<String> {
        let mut last_err = None;
        for alt in reference.split('|') {
            let alt = alt.trim();
            if alt.is_empty() {
                continue;
            }
            let mut v = String::new();
            if let Some(name) = alt.strip_prefix("env:") {
                v = std::env::var(name).unwrap_or_default();
            } else if !self.addr.is_empty() {
                match self.read_vault(ctx, alt).await {
                    Ok(x) => v = x,
                    Err(e) => last_err = Some(e),
                }
            } else if !self.dir.is_empty() {
                match self.local_get(alt).await {
                    Ok(x) => v = x,
                    Err(e) => last_err = Some(e),
                }
            }
            if !v.is_empty() {
                return Ok(v);
            }
        }
        match last_err {
            Some(e) => Err(e),
            None => Ok(String::new()),
        }
    }

    fn local_path(&self) -> PathBuf {
        PathBuf::from(&self.dir).join("secrets.json")
    }

    async fn local_get(&self, reference: &str) -> Result<String> {
        let (path, field) = split_ref(reference)?;
        let _g = self.mu.lock().await;
        let store = self.read_local_file()?;
        Ok(store
            .get(path)
            .and_then(|m| m.get(field))
            .cloned()
            .unwrap_or_default())
    }

    async fn local_put(&self, path: &str, field: &str, value: &str) -> Result<()> {
        let _g = self.mu.lock().await;
        let mut store = self.read_local_file()?;
        store
            .entry(path.to_string())
            .or_default()
            .insert(field.to_string(), value.to_string());
        let b = serde_json::to_vec_pretty(&store)?;
        std::fs::create_dir_all(&self.dir)?;
        write_private(&self.local_path(), &b)?;
        Ok(())
    }

    fn read_local_file(&self) -> Result<LocalStore> {
        match std::fs::read(self.local_path()) {
            Ok(b) => Ok(serde_json::from_slice(&b)?),
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(LocalStore::new()),
            Err(e) => Err(e.into()),
        }
    }
}

fn split_ref(reference: &str) -> Result<(&str, &str)> {
    reference.split_once('#').ok_or_else(|| {
        PlatformError::Other(format!("secret ref {reference:?}: expected <path>#<field>"))
    })
}

/// Writes a file readable by its owner only (0600 where the platform has modes).
fn write_private(path: &std::path::Path, b: &[u8]) -> std::io::Result<()> {
    #[cfg(unix)]
    {
        use std::{io::Write, os::unix::fs::OpenOptionsExt};
        let mut f = std::fs::OpenOptions::new()
            .write(true)
            .create(true)
            .truncate(true)
            .mode(0o600)
            .open(path)?;
        f.write_all(b)
    }
    #[cfg(not(unix))]
    {
        std::fs::write(path, b)
    }
}

#[cfg(test)]
mod tests {
    use std::{collections::HashMap, sync::Arc};

    use axum::{
        Router,
        body::Bytes,
        extract::{Path, State},
        http::{HeaderMap, StatusCode},
        routing::any,
    };
    use parking_lot::Mutex;

    use super::*;

    type Stored = Arc<Mutex<HashMap<String, String>>>;

    async fn serve(app: Router) -> String {
        let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = ln.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(ln, app).await.unwrap() });
        format!("http://{addr}")
    }

    #[tokio::test]
    async fn secrets_put() {
        let app = Router::new().route(
            "/{*path}",
            any(
                |method: axum::http::Method,
                 Path(path): Path<String>,
                 headers: HeaderMap,
                 body: Bytes| async move {
                    assert_eq!(
                        method,
                        axum::http::Method::POST,
                        "method = {method}, want POST"
                    );
                    assert_eq!(path, "v1/secret/data/goap/modelgw/acme");
                    assert_eq!(
                        headers.get("X-Vault-Token").and_then(|v| v.to_str().ok()),
                        Some("tok"),
                        "missing/wrong X-Vault-Token"
                    );
                    let decoded: serde_json::Value = serde_json::from_slice(&body).unwrap();
                    assert_eq!(
                        decoded["data"]["api_key"], "sk-raw",
                        "posted field = {decoded}"
                    );
                    StatusCode::OK
                },
            ),
        );
        let s = Secrets::vault(&serve(app).await, "tok", "secret");
        s.put(&Ctx::background(), "goap/modelgw/acme", "api_key", "sk-raw")
            .await
            .unwrap();
    }

    #[tokio::test]
    async fn secrets_put_then_resolve() {
        let stored: Stored = Arc::default();
        let app = Router::new()
            .route(
                "/{*path}",
                any(
                    |State(st): State<Stored>,
                     method: axum::http::Method,
                     Path(path): Path<String>,
                     body: Bytes| async move {
                        let path = path.trim_start_matches("v1/secret/data/").to_string();
                        match method {
                            axum::http::Method::POST => {
                                let decoded: serde_json::Value =
                                    serde_json::from_slice(&body).unwrap();
                                for (k, v) in decoded["data"].as_object().unwrap() {
                                    st.lock().insert(
                                        format!("{path}#{k}"),
                                        v.as_str().unwrap().to_string(),
                                    );
                                }
                                (StatusCode::OK, String::new())
                            }
                            axum::http::Method::GET => {
                                let st = st.lock();
                                let (k, v) = st
                                    .iter()
                                    .find(|(k, _)| k.starts_with(&format!("{path}#")))
                                    .unwrap();
                                let field = k.trim_start_matches(&format!("{path}#"));
                                (
                                    StatusCode::OK,
                                    serde_json::json!({"data": {"data": {field: v}}}).to_string(),
                                )
                            }
                            _ => (StatusCode::NOT_FOUND, String::new()),
                        }
                    },
                ),
            )
            .with_state(stored);
        let s = Secrets::vault(&serve(app).await, "tok", "secret");
        let ctx = Ctx::background();
        s.put(&ctx, "goap/modelgw/acme", "api_key", "sk-raw")
            .await
            .unwrap();
        assert_eq!(
            s.resolve(&ctx, "goap/modelgw/acme#api_key").await.unwrap(),
            "sk-raw"
        );
    }

    #[tokio::test]
    async fn secrets_put_no_vault() {
        let s = Secrets::default();
        let err = s
            .put(&Ctx::background(), "goap/modelgw/acme", "api_key", "sk-raw")
            .await
            .unwrap_err();
        assert!(
            err.to_string().contains("env:"),
            "error should point at env:VAR fallback, got {err}"
        );
    }

    /// The devlocal case (ADR 0010): no Vault running (addr empty), a local directory configured instead (as goap-dev
    /// does from the SQLite database's directory).
    #[tokio::test]
    async fn secrets_local_fallback() {
        let dir =
            std::env::temp_dir().join(format!("goap-secrets-{}-{}", std::process::id(), line!()));
        let s = Secrets::local(dir.to_str().unwrap());
        let ctx = Ctx::background();
        s.put(&ctx, "goap/modelgw/acme", "api_key", "sk-raw")
            .await
            .unwrap();
        assert_eq!(
            s.resolve(&ctx, "goap/modelgw/acme#api_key").await.unwrap(),
            "sk-raw"
        );
        // a second provider's key does not clobber the first
        s.put(&ctx, "goap/modelgw/other", "api_key", "sk-other")
            .await
            .unwrap();
        assert_eq!(
            s.resolve(&ctx, "goap/modelgw/acme#api_key").await.unwrap(),
            "sk-raw"
        );
        // alternatives: the first non-empty wins, env: reads the environment
        assert_eq!(
            s.resolve(
                &ctx,
                "goap/modelgw/none#api_key | goap/modelgw/other#api_key"
            )
            .await
            .unwrap(),
            "sk-other"
        );
        assert_eq!(
            s.resolve(&ctx, "env:GOAP_SECRETS_TEST_UNSET")
                .await
                .unwrap(),
            ""
        );
        assert!(s.resolve(&ctx, "no-field").await.is_err());
        let _ = std::fs::remove_dir_all(dir);
    }
}
