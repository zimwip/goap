use std::{
    collections::BTreeMap,
    sync::{Arc, LazyLock},
};

use async_trait::async_trait;
use axum::routing::MethodRouter;
use goap_authz::Principal;
use goap_ctx::Ctx;
use http::HeaderMap;
use parking_lot::RwLock;

use crate::gateway::{
    Config, Error, HttpError, check_session, dev_token, login, logout, parse_token, refresh,
    register, switch_project, unauthorized,
};

/// A route a mode adds to the gateway: its path (POST) and its handler.
pub struct AuthRoute {
    pub path: &'static str,
    pub handler: MethodRouter,
}

impl AuthRoute {
    fn post(path: &'static str, handler: MethodRouter) -> Self {
        AuthRoute { path, handler }
    }
}

/// Mode is an authentication strategy (ADR 0068): how a caller proves who it is, which endpoints it adds to the
/// gateway, and what it needs. The gateway holds one, found by the name of Config.auth_mode, and has no other
/// knowledge of the names: a new way of signing in is a new Mode registered by name ([`register_mode`]).
#[async_trait]
pub trait Mode: Send + Sync {
    /// Name is what Config.auth_mode, GOAP_AUTH_MODE and /api/auth/config call the mode.
    fn name(&self) -> &'static str;
    /// Open reports a mode that checks no credentials (every caller is the fixed development principal): a
    /// deployment with its own fixed identity (cmd/goap-dev) needs no gateway in front of its services.
    fn open(&self) -> bool;
    /// VerifiesTokens reports a mode whose callers carry a bearer token signed with Config.jwt_secret.
    fn verifies_tokens(&self) -> bool;
    /// Sessions reports a mode that signs its users in itself (register / login) and keeps their sign-in
    /// server-side, in Config.credentials: its tokens are refused once their session ended (ADR 0045).
    fn sessions(&self) -> bool;
    /// Validate checks that cfg holds what the mode needs (a secret, Credentials), at start.
    fn validate(&self, cfg: &Config) -> Result<(), Error>;
    /// Authenticate turns the credentials of a request (its headers, which it may change) into the caller.
    async fn authenticate(
        &self,
        ctx: &Ctx,
        headers: &mut HeaderMap,
        cfg: &Config,
    ) -> Result<Principal, HttpError>;
    /// Routes are the endpoints of the mode (sign-in, token reissue).
    fn routes(&self, cfg: &Config) -> Vec<AuthRoute>;
}

static MODES: LazyLock<RwLock<BTreeMap<String, Arc<dyn Mode>>>> = LazyLock::new(|| {
    let mut m: BTreeMap<String, Arc<dyn Mode>> = BTreeMap::new();
    for mode in [
        Arc::new(NoneMode) as Arc<dyn Mode>,
        Arc::new(BearerMode),
        Arc::new(LocalMode),
    ] {
        m.insert(mode.name().to_string(), mode);
    }
    RwLock::new(m)
});

/// RegisterMode makes a mode available under its name; a second mode of the same name is a programming error.
pub fn register_mode(m: Arc<dyn Mode>) {
    let mut modes = MODES.write();
    if modes.contains_key(m.name()) {
        panic!("gateway: auth mode {} registered twice", m.name());
    }
    modes.insert(m.name().to_string(), m);
}

/// LookupMode returns the mode named name. An unset (empty) name is an error, never an open gateway.
pub fn lookup_mode(name: &str) -> Result<Arc<dyn Mode>, Error> {
    let modes = MODES.read();
    if let Some(m) = modes.get(name) {
        return Ok(m.clone());
    }
    let known: Vec<&str> = modes.keys().map(String::as_str).collect();
    Err(Error::Config(format!(
        "unknown auth mode {} (one of [{}])",
        serde_json::to_string(name).unwrap_or_default(),
        known.join(" ")
    )))
}

/// NoneMode (explicit only): every caller is "dev", an administrator.
pub(crate) struct NoneMode;

#[async_trait]
impl Mode for NoneMode {
    fn name(&self) -> &'static str {
        "none"
    }
    fn open(&self) -> bool {
        true
    }
    fn verifies_tokens(&self) -> bool {
        false
    }
    fn sessions(&self) -> bool {
        false
    }
    fn validate(&self, _cfg: &Config) -> Result<(), Error> {
        Ok(())
    }
    async fn authenticate(
        &self,
        _ctx: &Ctx,
        _headers: &mut HeaderMap,
        _cfg: &Config,
    ) -> Result<Principal, HttpError> {
        Ok(Principal {
            subject: "dev".into(),
            org: "dev".into(),
            roles: vec!["admin".into()],
            ..Default::default()
        })
    }
    fn routes(&self, _cfg: &Config) -> Vec<AuthRoute> {
        Vec::new()
    }
}

/// BearerMode ("hs256"): bearer tokens minted out of band; POST /auth/dev-token mints one when dev_tokens is set.
pub(crate) struct BearerMode;

#[async_trait]
impl Mode for BearerMode {
    fn name(&self) -> &'static str {
        "hs256"
    }
    fn open(&self) -> bool {
        false
    }
    fn verifies_tokens(&self) -> bool {
        true
    }
    fn sessions(&self) -> bool {
        false
    }
    fn validate(&self, cfg: &Config) -> Result<(), Error> {
        require_secret(self, cfg)
    }
    async fn authenticate(
        &self,
        ctx: &Ctx,
        headers: &mut HeaderMap,
        cfg: &Config,
    ) -> Result<Principal, HttpError> {
        authenticate_token(ctx, headers, cfg).await
    }
    fn routes(&self, cfg: &Config) -> Vec<AuthRoute> {
        if !cfg.dev_tokens {
            return Vec::new(); // never in production
        }
        let c = Arc::new(cfg.clone());
        vec![
            AuthRoute::post("/auth/dev-token", dev_token(c.clone())),
            AuthRoute::post("/auth/dev-token/project", switch_project(c)),
        ]
    }
}

/// LocalMode ("local", ADR 0040): signup / login against Credentials, for a deployment with no external identity
/// provider. It verifies bearer tokens the way hs256 does: only how a token is first obtained differs, and the
/// session it belongs to is checked (ADR 0045).
pub(crate) struct LocalMode;

#[async_trait]
impl Mode for LocalMode {
    fn name(&self) -> &'static str {
        "local"
    }
    fn open(&self) -> bool {
        false
    }
    fn verifies_tokens(&self) -> bool {
        true
    }
    fn sessions(&self) -> bool {
        true
    }
    fn validate(&self, cfg: &Config) -> Result<(), Error> {
        require_secret(self, cfg)?;
        if cfg.credentials.is_none() {
            return Err(Error::Config("local auth requires Credentials".into()));
        }
        Ok(())
    }
    async fn authenticate(
        &self,
        ctx: &Ctx,
        headers: &mut HeaderMap,
        cfg: &Config,
    ) -> Result<Principal, HttpError> {
        authenticate_token(ctx, headers, cfg).await
    }
    fn routes(&self, cfg: &Config) -> Vec<AuthRoute> {
        let c = Arc::new(cfg.clone());
        vec![
            AuthRoute::post("/auth/register", register(c.clone())),
            AuthRoute::post("/auth/login", login(c.clone())),
            AuthRoute::post("/auth/logout", logout(c.clone())),
            AuthRoute::post("/auth/refresh", refresh(c.clone())),
            // switching project reissues the token (ADR 0039): a signed-in user needs it as much as a dev token
            AuthRoute::post("/auth/dev-token/project", switch_project(c)),
        ]
    }
}

fn require_secret(m: &dyn Mode, cfg: &Config) -> Result<(), Error> {
    if cfg.jwt_secret.len() < 32 {
        return Err(Error::Config(format!(
            "{} auth requires a JWT secret of at least 32 bytes",
            m.name()
        )));
    }
    Ok(())
}

/// authenticate_token verifies the bearer token of a request (and its session, when the mode keeps them).
async fn authenticate_token(
    ctx: &Ctx,
    headers: &mut HeaderMap,
    cfg: &Config,
) -> Result<Principal, HttpError> {
    let auth = headers
        .get(http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    let claims = parse_token(cfg, &auth).map_err(|e| unauthorized(&e))?;
    check_session(ctx, cfg, &claims, false).await?;
    headers.remove(http::header::AUTHORIZATION);
    Ok(Principal {
        subject: claims.subject.clone(),
        org: claims.org.clone(),
        project: claims.project.clone(),
        roles: claims.roles.clone(),
        ..Default::default()
    })
}
