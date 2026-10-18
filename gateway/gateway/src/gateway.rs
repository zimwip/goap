use std::{
    collections::HashMap,
    error::Error as StdError,
    future::Future,
    pin::Pin,
    sync::Arc,
    time::{Duration, Instant},
};

use async_trait::async_trait;
use axum::{
    Json, Router,
    body::Body,
    extract::Request,
    http::{HeaderMap, HeaderName, HeaderValue, StatusCode},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{MethodRouter, any, get, post},
};
use bytes::Bytes;
use chrono::{DateTime, Utc};
use goap_authz::Principal;
use goap_connect::Code;
use goap_ctx::Ctx;
use jsonwebtoken::{Algorithm, DecodingKey, EncodingKey, Header, Validation, errors::ErrorKind};
use parking_lot::Mutex;
use serde::{Deserialize, Deserializer, Serialize, de::DeserializeOwned};

use crate::{mode::lookup_mode, status::status_handler};

// Headers propagated to services. Incoming values are always overwritten.
pub const HEADER_SUBJECT: &str = goap_identity::HEADER_SUBJECT;
pub const HEADER_ORG: &str = goap_identity::HEADER_ORG;
pub const HEADER_ROLES: &str = goap_identity::HEADER_ROLES;
pub const HEADER_PROJECT: &str = goap_identity::HEADER_PROJECT;

/// Any error of a callback or of the credentials service.
pub type BoxError = Box<dyn StdError + Send + Sync>;

type BoxFuture<T> = Pin<Box<dyn Future<Output = T> + Send + 'static>>;

/// OnSignIn runs after a successful local register/login, before the token is signed (ADR 0042).
pub type OnSignIn = Arc<dyn Fn(Ctx, String) -> BoxFuture<Result<(), BoxError>> + Send + Sync>;
/// ProjectAccess tells whether a caller may work on a project (ADR 0039).
pub type ProjectAccess =
    Arc<dyn Fn(Ctx, Principal, String) -> BoxFuture<Result<bool, BoxError>> + Send + Sync>;
/// Enrich completes the authenticated principal with what the graph knows of its subject.
pub type Enrich = Arc<dyn Fn(Ctx, Principal) -> BoxFuture<Principal> + Send + Sync>;

/// An [`OnSignIn`] of an async function.
pub fn on_sign_in_fn<F, Fut>(f: F) -> OnSignIn
where
    F: Fn(Ctx, String) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<(), BoxError>> + Send + 'static,
{
    Arc::new(move |ctx, s| Box::pin(f(ctx, s)))
}

/// A [`ProjectAccess`] of an async function.
pub fn project_access_fn<F, Fut>(f: F) -> ProjectAccess
where
    F: Fn(Ctx, Principal, String) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<bool, BoxError>> + Send + 'static,
{
    Arc::new(move |ctx, p, project| Box::pin(f(ctx, p, project)))
}

/// An [`Enrich`] of an async function.
pub fn enrich_fn<F, Fut>(f: F) -> Enrich
where
    F: Fn(Ctx, Principal) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Principal> + Send + 'static,
{
    Arc::new(move |ctx, p| Box::pin(f(ctx, p)))
}

/// The errors of the configuration of the gateway.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// A configuration the gateway refuses (an unknown mode, a missing secret...).
    #[error("{0}")]
    Config(String),
    /// A route whose upstream is not a URL.
    #[error("route {prefix}: {message}")]
    Route { prefix: String, message: String },
}

/// The error of an endpoint: its status and its message, answered as echo does (`{"message": "..."}`).
#[derive(Debug, Clone, PartialEq, Eq, thiserror::Error)]
#[error("code={}, message={message}", status.as_u16())]
pub struct HttpError {
    pub status: StatusCode,
    pub message: String,
}

impl HttpError {
    pub fn new(status: StatusCode, message: impl Into<String>) -> Self {
        HttpError {
            status,
            message: message.into(),
        }
    }

    /// What echo answers for an error that is not an HTTP error.
    fn internal() -> Self {
        HttpError::new(StatusCode::INTERNAL_SERVER_ERROR, "Internal Server Error")
    }
}

impl IntoResponse for HttpError {
    fn into_response(self) -> Response {
        (
            self.status,
            Json(serde_json::json!({ "message": self.message })),
        )
            .into_response()
    }
}

/// Route maps a Connect service prefix to an upstream base URL.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Route {
    /// e.g. /goap.graph.v1.GraphService/
    pub prefix: String,
    /// e.g. http://graph:8080
    pub upstream: String,
}

/// Config configures the gateway.
#[derive(Clone, Default)]
pub struct Config {
    pub routes: Vec<Route>,
    /// AuthMode names the authentication strategy (Mode, ADR 0068): "none" (dev: every caller is "dev"), "hs256"
    /// (bearer tokens minted out of band, dev-token only) or "local" (ADR 0040: signup/login against Credentials,
    /// backed by credsvc, for a deployment with no external identity provider). "local" verifies bearer tokens the
    /// same way "hs256" does: only how a token is first obtained differs. It is the default (DEFAULT_AUTH_MODE, ADR
    /// 0042): a deployment signs its users in itself unless an external identity provider issues their tokens. An
    /// unset mode is refused.
    pub auth_mode: String,
    /// JWTSecret signs and validates HS256 tokens.
    pub jwt_secret: Vec<u8>,
    /// DevTokens enables POST /auth/dev-token (never in production).
    pub dev_tokens: bool,
    /// TokenTTL is how long a signed token is valid (DEFAULT_TOKEN_TTL when zero). The web refreshes it before it
    /// expires (POST /auth/refresh), so it is also how long an idle session survives.
    pub token_ttl: Duration,
    /// MaxSession bounds how long tokens are refreshed after a sign-in (DEFAULT_MAX_SESSION when zero): past it, the
    /// user signs in again.
    pub max_session: Duration,
    /// SessionCheckTTL is how long the authenticator trusts that a session is still open before asking Credentials
    /// again (DEFAULT_SESSION_CHECK_TTL when zero): a session ended elsewhere is refused within it (ADR 0045), at
    /// once by the gateway that ended it.
    pub session_check_ttl: Duration,
    /// sessions caches the state of the sessions: set by [`prepare`] (leave it None), shared by the authenticator
    /// and the endpoints.
    pub sessions: Option<Sessions>,
    /// Credentials backs the register/login endpoints and the sessions of a mode that keeps them (Mode::sessions,
    /// "local"; ADR 0040).
    pub credentials: Option<Arc<dyn Credentials>>,
    /// OnSignIn runs after a successful local register/login, before the token is signed (ADR 0042): it creates the
    /// subject's User node when the graph has none yet (graphsvc.EnsureUser, member of the unit new users join), so
    /// a user exists from the moment they sign in rather than from their first call. A failure refuses the sign-in.
    /// None skips it (the node is then created on the first call, EnsureCaller).
    pub on_sign_in: Option<OnSignIn>,
    /// ProjectAccess tells whether a caller may work on a project, asked before a token is reissued for it (project
    /// switch, ADR 0039). None refuses every project (only clearing it is allowed), unless dev tokens are on (which
    /// let anyone mint any token anyway).
    pub project_access: Option<ProjectAccess>,
    /// AllowOrigins for CORS.
    pub allow_origins: Vec<String>,
    /// Enrich completes the authenticated principal with what the graph knows of its subject (roles and unit of its
    /// User node). None leaves the principal as the token gives it.
    pub enrich: Option<Enrich>,
    /// Session answers GET /api/whoami: the caller and what the client derives from it (access.Authorizer.Session).
    /// None answers the principal alone.
    pub session: Option<goap_identity::Session>,
}

/// DefaultTokenTTL and DefaultMaxSession are the lifetimes of a token and of a session when Config names none.
pub const DEFAULT_TOKEN_TTL: Duration = Duration::from_secs(12 * 3600);
pub const DEFAULT_MAX_SESSION: Duration = Duration::from_secs(7 * 24 * 3600);
pub const DEFAULT_SESSION_CHECK_TTL: Duration = Duration::from_secs(15);

/// DefaultAuthMode is the AuthMode of a deployment that names none (GOAP_AUTH_MODE unset, ADR 0042): local sign-in,
/// since no external identity provider (SSO) is configured. "none" (no sign-in at all) is for local development
/// only and must be asked for explicitly.
pub const DEFAULT_AUTH_MODE: &str = "local";

/// Prepare returns cfg ready to be shared by [`mount_auth_endpoints`] and [`authenticator`]: they then see the same
/// state of the sessions, so a sign-out is refused at once by the authenticator of the same process (ADR 0045).
/// [`mount`] does it itself.
pub fn prepare(mut cfg: Config) -> Config {
    if cfg.sessions.is_none()
        && let Ok(m) = lookup_mode(&cfg.auth_mode)
        && m.sessions()
        && let Some(creds) = &cfg.credentials
    {
        let ttl = if cfg.session_check_ttl.is_zero() {
            DEFAULT_SESSION_CHECK_TTL
        } else {
            cfg.session_check_ttl
        };
        cfg.sessions = Some(Sessions(Arc::new(SessionCache {
            creds: creds.clone(),
            ttl,
            entries: Mutex::new(HashMap::new()),
        })));
    }
    cfg
}

/// The state of the sessions [`prepare`] shares between the endpoints and the authenticator (opaque).
#[derive(Clone)]
pub struct Sessions(Arc<SessionCache>);

impl std::ops::Deref for Sessions {
    type Target = SessionCache;

    fn deref(&self) -> &SessionCache {
        &self.0
    }
}

/// SessionCache remembers for a while whether a session is open, so the authenticator does not ask Credentials on
/// every request.
pub struct SessionCache {
    creds: Arc<dyn Credentials>,
    ttl: Duration,
    entries: Mutex<HashMap<String, SessionEntry>>,
}

#[derive(Clone, Copy)]
struct SessionEntry {
    active: bool,
    at: Instant,
}

impl SessionCache {
    /// active reports whether a session is open: from the cache while fresh, else from Credentials (the last known
    /// state when they cannot answer; an error when nothing is known).
    async fn active(&self, ctx: &Ctx, id: &str, subject: &str) -> Result<bool, BoxError> {
        let e = self.entries.lock().get(id).copied();
        if let Some(e) = e
            && (!e.active || e.at.elapsed() < self.ttl)
        {
            return Ok(e.active); // an ended session never reopens
        }
        match self.creds.session_active(ctx, id, subject).await {
            Ok(active) => {
                self.set(id, active);
                Ok(active)
            }
            Err(err) => match e {
                Some(e) => Ok(e.active),
                None => Err(err),
            },
        }
    }

    fn set(&self, id: &str, active: bool) {
        let mut entries = self.entries.lock();
        if entries.len() > 10000 {
            // forget the old entries rather than grow without bound
            let ttl = self.ttl;
            entries.retain(|_, e| e.at.elapsed() <= ttl);
        }
        entries.insert(
            id.to_string(),
            SessionEntry {
                active,
                at: Instant::now(),
            },
        );
    }
}

/// Credentials is what the local AuthMode needs of the credentials service (credsvc, ADR 0040, 0045).
#[async_trait]
pub trait Credentials: Send + Sync {
    async fn register(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<(), BoxError>;
    async fn verify(&self, ctx: &Ctx, subject: &str, password: &str) -> Result<bool, BoxError>;
    /// StartSession opens a sign-in session lasting max_age at most and returns its id (the token's sid).
    async fn start_session(
        &self,
        ctx: &Ctx,
        subject: &str,
        max_age: Duration,
    ) -> Result<String, BoxError>;
    /// SessionActive reports whether a session of subject still accepts its tokens.
    async fn session_active(&self, ctx: &Ctx, id: &str, subject: &str) -> Result<bool, BoxError>;
    /// EndSession ends a session; end_sessions every session of a subject.
    async fn end_session(&self, ctx: &Ctx, id: &str) -> Result<(), BoxError>;
    async fn end_sessions(&self, ctx: &Ctx, subject: &str) -> Result<(), BoxError>;
}

/// Claims are the GOAP JWT claims (Go's `Claims` with the registered claims of golang-jwt, in its JSON order).
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub struct Claims {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub org: String,
    /// Sid is the sign-in session the token belongs to (ADR 0045, local AuthMode): refused once it ended.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sid: String,
    /// Project is the caller's active project (ADR 0039): re-issued by /auth/dev-token/project each time the user
    /// switches, so every call carries it without the caller having to pass it explicitly.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub project: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub roles: Vec<String>,
    /// AuthTime is when the subject signed in (OIDC auth_time, unix seconds): kept across refreshes, it bounds the
    /// session (Config.max_session).
    #[serde(
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "numeric_date"
    )]
    pub auth_time: Option<i64>,
    #[serde(rename = "iss", default, skip_serializing_if = "String::is_empty")]
    pub issuer: String,
    #[serde(rename = "sub", default, skip_serializing_if = "String::is_empty")]
    pub subject: String,
    #[serde(rename = "aud", default, skip_serializing_if = "Option::is_none")]
    pub audience: Option<serde_json::Value>,
    #[serde(
        rename = "exp",
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "numeric_date"
    )]
    pub expires_at: Option<i64>,
    #[serde(
        rename = "nbf",
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "numeric_date"
    )]
    pub not_before: Option<i64>,
    #[serde(
        rename = "iat",
        default,
        skip_serializing_if = "Option::is_none",
        deserialize_with = "numeric_date"
    )]
    pub issued_at: Option<i64>,
    #[serde(rename = "jti", default, skip_serializing_if = "String::is_empty")]
    pub id: String,
}

/// A JWT NumericDate: a JSON number of seconds, possibly with a fraction (truncated), or null.
fn numeric_date<'de, D: Deserializer<'de>>(d: D) -> Result<Option<i64>, D::Error> {
    Ok(Option::<f64>::deserialize(d)?.map(|f| f as i64))
}

/// MountAuthEndpoints registers the sign-in HTTP endpoints alone (no CORS, no proxying, no /api/whoami or
/// /api/status): the endpoints of the Mode (dev-token for hs256 with dev_tokens; register, login, logout, refresh
/// for local) and /api/auth/config (always). Split out of mount so a single-process deployment (cmd/goap-dev) can
/// offer the same sign-in UI as the distributed gateway without also wanting a reverse proxy.
pub fn mount_auth_endpoints(router: Router, cfg: Config) -> Result<Router, Error> {
    let cfg = prepare(cfg);
    let mode = lookup_mode(&cfg.auth_mode)?;
    mode.validate(&cfg)?;
    let mut router = router;
    for r in mode.routes(&cfg) {
        router = router.route(r.path, r.handler);
    }
    Ok(router.route("/api/auth/config", auth_config(Arc::new(cfg))))
}

/// The middleware that turns a request's credentials (a bearer token, or nothing in a mode that checks none) into
/// the propagated identity headers: a single-process deployment (cmd/goap-dev) applies it globally
/// ([`Authenticator::wrap`]) instead of per route ([`Authenticator::route_layer`]).
#[derive(Clone)]
pub struct Authenticator {
    mode: Arc<dyn crate::Mode>,
    cfg: Arc<Config>,
}

impl Authenticator {
    /// Authenticates a request: its credentials checked, the identity headers set (and the token removed), or the
    /// refusal to answer.
    pub async fn authenticate(&self, ctx: &Ctx, headers: &mut HeaderMap) -> Result<(), HttpError> {
        let p = self.mode.authenticate(ctx, headers, &self.cfg).await?;
        set_principal(ctx, &self.cfg, p, headers).await;
        Ok(())
    }

    async fn run(self, mut req: Request, next: Next) -> Response {
        let ctx = Ctx::background();
        if let Err(e) = self.authenticate(&ctx, req.headers_mut()).await {
            return e.into_response();
        }
        next.run(req).await
    }

    /// Every route of `router` authenticated.
    pub fn wrap<S: Clone + Send + Sync + 'static>(&self, router: Router<S>) -> Router<S> {
        let a = self.clone();
        router.layer(middleware::from_fn(move |req: Request, next: Next| {
            a.clone().run(req, next)
        }))
    }

    /// One route authenticated.
    pub fn route_layer<S: Clone + Send + Sync + 'static>(
        &self,
        route: MethodRouter<S>,
    ) -> MethodRouter<S> {
        let a = self.clone();
        route.layer(middleware::from_fn(move |req: Request, next: Next| {
            a.clone().run(req, next)
        }))
    }
}

/// Authenticator returns the middleware that turns a request's credentials into the propagated identity headers
/// (set_principal): exported so a single-process deployment (cmd/goap-dev) can apply it globally instead of per
/// route.
pub fn authenticator(cfg: Config) -> Result<Authenticator, Error> {
    authenticator_of(prepare(cfg))
}

fn authenticator_of(cfg: Config) -> Result<Authenticator, Error> {
    // an unset mode is a configuration error, never an open gateway
    let mode = lookup_mode(&cfg.auth_mode)?;
    mode.validate(&cfg)?;
    Ok(Authenticator {
        mode,
        cfg: Arc::new(cfg),
    })
}

/// Mount installs the gateway on router: the sign-in endpoints, /api/status, /api/whoami and the proxied routes,
/// with CORS over every route of the router when origins are allowed.
pub fn mount(router: Router, cfg: Config) -> Result<Router, Error> {
    let cfg = prepare(cfg);
    let mut router = mount_auth_endpoints(router, cfg.clone())?;
    let auth = authenticator_of(cfg.clone())?;
    router = router.route("/api/status", status_handler(cfg.routes.clone()));
    router = router.route(
        "/api/whoami",
        auth.route_layer(goap_identity::who_am_i(
            goap_identity::Extractor::default(),
            cfg.session.clone(),
        )),
    );
    let client = reqwest::Client::new();
    for r in &cfg.routes {
        let upstream = reqwest::Url::parse(&r.upstream).map_err(|e| Error::Route {
            prefix: r.prefix.clone(),
            message: e.to_string(),
        })?;
        let client = client.clone();
        let proxy = any(move |req: Request| {
            let client = client.clone();
            let upstream = upstream.clone();
            async move { reverse_proxy(&client, &upstream, req).await }
        });
        router = router.route(&format!("{}{{*rest}}", r.prefix), auth.route_layer(proxy));
    }
    if !cfg.allow_origins.is_empty() {
        router = router.layer(cors(&cfg.allow_origins));
    }
    Ok(router)
}

fn cors(origins: &[String]) -> tower_http::cors::CorsLayer {
    use tower_http::cors::{AllowOrigin, CorsLayer};
    let origin = if origins.iter().any(|o| o == "*") {
        AllowOrigin::any()
    } else {
        AllowOrigin::list(
            origins
                .iter()
                .filter_map(|o| HeaderValue::from_str(o).ok())
                .collect::<Vec<_>>(),
        )
    };
    let names = |l: &[&'static str]| -> Vec<HeaderName> {
        l.iter().map(|n| HeaderName::from_static(n)).collect()
    };
    CorsLayer::new()
        .allow_origin(origin)
        .allow_headers(names(&[
            "authorization",
            "content-type",
            "connect-protocol-version",
            "connect-timeout-ms",
            "x-user-agent",
            "x-goap-command",
        ]))
        .expose_headers(names(&["grpc-status", "grpc-message"]))
        .allow_methods([http::Method::GET, http::Method::POST, http::Method::OPTIONS])
}

/// Hop-by-hop headers, never forwarded (Go's httputil).
const HOP_HEADERS: &[&str] = &[
    "connection",
    "proxy-connection",
    "keep-alive",
    "proxy-authenticate",
    "proxy-authorization",
    "te",
    "trailer",
    "transfer-encoding",
    "upgrade",
];

fn copy_headers(from: &HeaderMap, to: &mut HeaderMap) {
    for (k, v) in from {
        if HOP_HEADERS.contains(&k.as_str()) || k == http::header::HOST {
            continue;
        }
        to.append(k.clone(), v.clone());
    }
}

/// The reverse proxy of a route: the request streamed to the upstream, its answer streamed back (streaming
/// friendly, as Go's `FlushInterval = -1`); an upstream that cannot be reached is a 502.
async fn reverse_proxy(
    client: &reqwest::Client,
    upstream: &reqwest::Url,
    req: Request,
) -> Response {
    let (parts, body) = req.into_parts();
    let mut url = upstream.clone();
    let base = upstream.path().trim_end_matches('/');
    url.set_path(&format!("{base}{}", parts.uri.path()));
    url.set_query(parts.uri.query());
    let mut headers = HeaderMap::new();
    copy_headers(&parts.headers, &mut headers);
    let out = client
        .request(parts.method, url)
        .headers(headers)
        .body(reqwest::Body::wrap_stream(body.into_data_stream()))
        .send()
        .await;
    let resp = match out {
        Ok(r) => r,
        Err(e) => {
            // the error names the upstream's address: logged, not answered to the caller
            tracing::warn!(error = %e, "http: proxy error");
            return StatusCode::BAD_GATEWAY.into_response();
        }
    };
    let mut b = Response::builder().status(resp.status());
    if let Some(h) = b.headers_mut() {
        copy_headers(resp.headers(), h);
    }
    b.body(Body::from_stream(resp.bytes_stream()))
        .unwrap_or_else(|_| StatusCode::BAD_GATEWAY.into_response())
}

/// set_principal propagates the caller to the services, completed by its User node when there is one.
async fn set_principal(ctx: &Ctx, cfg: &Config, p: Principal, h: &mut HeaderMap) {
    let p = match &cfg.enrich {
        Some(enrich) => enrich(ctx.clone(), p).await,
        None => p,
    };
    set_header(h, HEADER_SUBJECT, &p.subject);
    set_header(h, HEADER_ORG, &p.org);
    set_header(h, HEADER_PROJECT, &p.project);
    set_header(h, HEADER_ROLES, &p.roles.join(","));
    // a caller never says whom it acts for: only platform services do (ADR 0114)
    if let Ok(n) = HeaderName::try_from(goap_identity::HEADER_ON_BEHALF_OF) {
        h.remove(n);
    }
}

/// `h.Set(name, value)`.
fn set_header(h: &mut HeaderMap, name: &str, value: &str) {
    let Ok(name) = HeaderName::try_from(name) else {
        return;
    };
    match HeaderValue::try_from(value) {
        Ok(v) => {
            h.insert(name, v);
        }
        Err(_) => {
            h.remove(name);
        }
    }
}

/// The refusal of a token by [`parse_token`].
#[derive(Debug)]
pub(crate) enum TokenError {
    Missing,
    Expired,
    Invalid,
}

fn validation(claims: bool) -> Validation {
    let mut v = Validation::new(Algorithm::HS256);
    v.leeway = 0;
    v.validate_aud = false;
    if claims {
        v.validate_exp = true;
        v.validate_nbf = true;
        v.set_required_spec_claims(&["exp"]);
    } else {
        v.validate_exp = false;
        v.validate_nbf = false;
        v.required_spec_claims.clear();
    }
    v
}

fn decode(cfg: &Config, raw: &str, claims: bool) -> Result<Claims, TokenError> {
    jsonwebtoken::decode::<Claims>(
        raw,
        &DecodingKey::from_secret(&cfg.jwt_secret),
        &validation(claims),
    )
    .map(|t| t.claims)
    .map_err(|e| match e.kind() {
        ErrorKind::ExpiredSignature => TokenError::Expired,
        _ => TokenError::Invalid,
    })
}

/// parse_token validates a "Bearer <token>" Authorization header value and returns its claims.
pub(crate) fn parse_token(cfg: &Config, authorization: &str) -> Result<Claims, TokenError> {
    let raw = authorization
        .strip_prefix("Bearer ")
        .ok_or(TokenError::Missing)?;
    decode(cfg, raw, true)
}

/// unauthorized is the 401 of a request whose token was refused, telling an expired token (sign in again, or
/// refresh earlier) from an invalid one.
pub(crate) fn unauthorized(err: &TokenError) -> HttpError {
    match err {
        TokenError::Expired => HttpError::new(StatusCode::UNAUTHORIZED, "token expired"),
        _ => HttpError::new(StatusCode::UNAUTHORIZED, "invalid token"),
    }
}

/// check_session refuses a token of a mode that keeps sessions (Mode::sessions) whose sign-in session ended or that
/// belongs to none (ADR 0045): from the cache of the sessions, or asking Credentials when fresh is set (a refresh).
pub(crate) async fn check_session(
    ctx: &Ctx,
    cfg: &Config,
    claims: &Claims,
    fresh: bool,
) -> Result<(), HttpError> {
    let Some(creds) = &cfg.credentials else {
        return Ok(());
    };
    match lookup_mode(&cfg.auth_mode) {
        Ok(m) if m.sessions() => {}
        _ => return Ok(()),
    }
    if claims.sid.is_empty() {
        // issued before sessions: sign in again
        return Err(HttpError::new(StatusCode::UNAUTHORIZED, "invalid token"));
    }
    let active = match (&cfg.sessions, fresh) {
        (Some(cache), false) => cache.active(ctx, &claims.sid, &claims.subject).await,
        (cache, _) => {
            let r = creds
                .session_active(ctx, &claims.sid, &claims.subject)
                .await;
            if let (Ok(a), Some(cache)) = (&r, cache) {
                cache.set(&claims.sid, *a);
            }
            r
        }
    };
    match active {
        Err(e) => Err(HttpError::new(
            StatusCode::SERVICE_UNAVAILABLE,
            format!("cannot check the session: {e}"),
        )),
        Ok(false) => Err(HttpError::new(StatusCode::UNAUTHORIZED, "session ended")),
        Ok(true) => Ok(()),
    }
}

/// Session is a token's identity, kept when it is reissued (refresh, project switch).
#[derive(Debug, Clone)]
pub(crate) struct Session {
    sid: String,
    subject: String,
    org: String,
    project: String,
    roles: Vec<String>,
    auth_time: DateTime<Utc>,
}

fn unix(secs: i64) -> DateTime<Utc> {
    DateTime::from_timestamp(secs, 0).unwrap_or_default()
}

fn session_of(c: &Claims) -> Session {
    let auth_time = c
        .auth_time
        .or(c.issued_at)
        .map(unix)
        .unwrap_or_else(Utc::now);
    Session {
        sid: c.sid.clone(),
        subject: c.subject.clone(),
        org: c.org.clone(),
        project: c.project.clone(),
        roles: c.roles.clone(),
        auth_time,
    }
}

/// Issued is the response of an endpoint signing a token: the token and when it expires, for the web to refresh it
/// in time.
#[derive(Debug, Clone, Default, PartialEq, Serialize, Deserialize)]
pub(crate) struct Issued {
    pub token: String,
    #[serde(rename = "expiresAt")]
    pub expires_at: DateTime<Utc>,
}

pub(crate) fn sign(
    cfg: &Config,
    subject: &str,
    org: &str,
    project: &str,
    roles: &[String],
) -> Result<String, HttpError> {
    sign_session(
        cfg,
        &Session {
            sid: String::new(),
            subject: subject.into(),
            org: org.into(),
            project: project.into(),
            roles: roles.to_vec(),
            auth_time: Utc::now(),
        },
    )
    .map(|t| t.token)
}

fn sign_session(cfg: &Config, s: &Session) -> Result<Issued, HttpError> {
    let ttl = if cfg.token_ttl.is_zero() {
        DEFAULT_TOKEN_TTL
    } else {
        cfg.token_ttl
    };
    let now = Utc::now();
    let exp = now + chrono::TimeDelta::from_std(ttl).unwrap_or(chrono::TimeDelta::MAX);
    let claims = Claims {
        sid: s.sid.clone(),
        org: s.org.clone(),
        project: s.project.clone(),
        roles: s.roles.clone(),
        auth_time: Some(s.auth_time.timestamp()),
        subject: s.subject.clone(),
        issuer: "goap-gateway".into(),
        issued_at: Some(now.timestamp()),
        expires_at: Some(exp.timestamp()),
        ..Default::default()
    };
    let token = jsonwebtoken::encode(
        &Header::new(Algorithm::HS256),
        &claims,
        &EncodingKey::from_secret(&cfg.jwt_secret),
    )
    .map_err(|_| HttpError::internal())?;
    Ok(Issued {
        token,
        expires_at: unix(exp.timestamp()),
    })
}

fn authorization(h: &HeaderMap) -> String {
    h.get(http::header::AUTHORIZATION)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string()
}

/// `c.Bind(&in)`: no body binds nothing; a JSON body is decoded; another kind of body is refused.
fn bind<T: DeserializeOwned + Default>(headers: &HeaderMap, body: &[u8]) -> Result<T, ()> {
    if body.is_empty() {
        return Ok(T::default());
    }
    let ct = headers
        .get(http::header::CONTENT_TYPE)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("");
    if !ct.starts_with("application/json") {
        return Err(());
    }
    serde_json::from_slice(body).map_err(|_| ())
}

type Handled = Result<Response, HttpError>;

/// An endpoint of the gateway: a POST handler over the configuration, the headers and the body of a request.
fn endpoint<F, Fut>(cfg: Arc<Config>, f: F) -> MethodRouter
where
    F: Fn(Arc<Config>, HeaderMap, Bytes) -> Fut + Clone + Send + Sync + 'static,
    Fut: Future<Output = Handled> + Send + 'static,
{
    post(move |headers: HeaderMap, body: Bytes| {
        let (cfg, f) = (cfg.clone(), f.clone());
        async move {
            f(cfg, headers, body)
                .await
                .unwrap_or_else(IntoResponse::into_response)
        }
    })
}

fn ok_json<T: Serialize>(v: &T) -> Handled {
    Ok((StatusCode::OK, Json(v)).into_response())
}

fn max_session(cfg: &Config) -> Duration {
    if cfg.max_session.is_zero() {
        DEFAULT_MAX_SESSION
    } else {
        cfg.max_session
    }
}

/// refresh reissues a valid token with a fresh expiry, keeping its identity and its sign-in time: the web calls it
/// before its token expires, so an active session goes on without signing in again, up to Config.max_session after
/// the sign-in. An expired or invalid token is refused (401): the user signs in again.
pub(crate) fn refresh(cfg: Arc<Config>) -> MethodRouter {
    endpoint(cfg, |cfg, headers, _body| async move {
        let ctx = Ctx::background();
        let claims = parse_token(&cfg, &authorization(&headers)).map_err(|e| unauthorized(&e))?;
        let s = session_of(&claims);
        let since = (Utc::now() - s.auth_time).to_std().unwrap_or_default();
        if since > max_session(&cfg) {
            return Err(HttpError::new(
                StatusCode::UNAUTHORIZED,
                "session expired: sign in again",
            ));
        }
        check_session(&ctx, &cfg, &claims, true).await?;
        signed_in(&ctx, &cfg, &s.subject).await?;
        ok_json(&sign_session(&cfg, &s)?)
    })
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct DevTokenIn {
    subject: String,
    org: String,
    project: String,
    roles: Vec<String>,
}

pub(crate) fn dev_token(cfg: Arc<Config>) -> MethodRouter {
    endpoint(cfg, |cfg, headers, body| async move {
        let input = match bind::<DevTokenIn>(&headers, &body) {
            Ok(i) if !i.subject.is_empty() => i,
            _ => return Err(HttpError::new(StatusCode::BAD_REQUEST, "subject required")),
        };
        let token = sign(
            &cfg,
            &input.subject,
            &input.org,
            &input.project,
            &input.roles,
        )?;
        ok_json(&serde_json::json!({ "token": token }))
    })
}

/// auth_config tells the web which sign-in UI to show: unauthenticated, so it can be called before any token exists
/// (ADR 0040 — the signin/signup screens, and the Logout action's own SSO seam, both read it).
fn auth_config(cfg: Arc<Config>) -> MethodRouter {
    get(move || {
        let cfg = cfg.clone();
        async move {
            match lookup_mode(&cfg.auth_mode) {
                Ok(m) => (
                    StatusCode::OK,
                    Json(serde_json::json!({"authMode": m.name(), "signsIn": m.sessions()})),
                )
                    .into_response(),
                Err(_) => HttpError::internal().into_response(),
            }
        }
    })
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct CredentialsIn {
    #[serde(alias = "Subject")]
    subject: String,
    #[serde(alias = "Password")]
    password: String,
}

fn credentials_of(cfg: &Config) -> Result<&Arc<dyn Credentials>, HttpError> {
    cfg.credentials.as_ref().ok_or_else(HttpError::internal)
}

/// register creates the credential of a new subject and signs them straight in (ADR 0040): the returned token
/// carries no org/roles of its own, filled in by Enrich from the subject's User node — created, with the
/// org-membership/first-admin bootstrap, by OnSignIn (ADR 0042), or else the moment this token is first used
/// (EnsureCaller).
pub(crate) fn register(cfg: Arc<Config>) -> MethodRouter {
    endpoint(cfg, |cfg, headers, body| async move {
        let ctx = Ctx::background();
        let input = match bind::<CredentialsIn>(&headers, &body) {
            Ok(i) if !i.subject.is_empty() => i,
            _ => return Err(HttpError::new(StatusCode::BAD_REQUEST, "subject required")),
        };
        if let Err(err) = credentials_of(&cfg)?
            .register(&ctx, &input.subject, &input.password)
            .await
        {
            let status = if goap_connect::code_of(&*err as &(dyn StdError + 'static))
                == Code::AlreadyExists
            {
                StatusCode::CONFLICT
            } else {
                StatusCode::BAD_REQUEST
            };
            return Err(HttpError::new(status, err.to_string()));
        }
        signed_in(&ctx, &cfg, &input.subject).await?;
        ok_json(&start_session(&ctx, &cfg, &input.subject).await?)
    })
}

/// login verifies a subject's password and signs a token the same shape register's is.
pub(crate) fn login(cfg: Arc<Config>) -> MethodRouter {
    endpoint(cfg, |cfg, headers, body| async move {
        let ctx = Ctx::background();
        let input = match bind::<CredentialsIn>(&headers, &body) {
            Ok(i) if !i.subject.is_empty() => i,
            _ => return Err(HttpError::new(StatusCode::BAD_REQUEST, "subject required")),
        };
        let ok = credentials_of(&cfg)?
            .verify(&ctx, &input.subject, &input.password)
            .await
            .map_err(|_| HttpError::internal())?;
        if !ok {
            return Err(HttpError::new(
                StatusCode::UNAUTHORIZED,
                "wrong subject or password",
            ));
        }
        signed_in(&ctx, &cfg, &input.subject).await?;
        ok_json(&start_session(&ctx, &cfg, &input.subject).await?)
    })
}

/// start_session opens the sign-in session of a subject whose credentials were just accepted, and signs its first
/// token (ADR 0045).
async fn start_session(ctx: &Ctx, cfg: &Config, subject: &str) -> Result<Issued, HttpError> {
    let sid = credentials_of(cfg)?
        .start_session(ctx, subject, max_session(cfg))
        .await
        .map_err(|e| {
            HttpError::new(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("cannot open the session: {e}"),
            )
        })?;
    sign_session(
        cfg,
        &Session {
            sid,
            subject: subject.into(),
            org: String::new(),
            project: String::new(),
            roles: Vec::new(),
            auth_time: Utc::now(),
        },
    )
}

/// signed_in runs OnSignIn for a subject whose credentials were just accepted: the user is declared in the graph
/// (created, member of the unit new users join) before they get a token.
async fn signed_in(ctx: &Ctx, cfg: &Config, subject: &str) -> Result<(), HttpError> {
    let Some(f) = &cfg.on_sign_in else {
        return Ok(());
    };
    f(ctx.clone(), subject.to_string()).await.map_err(|e| {
        HttpError::new(
            StatusCode::SERVICE_UNAVAILABLE,
            format!("cannot declare the user: {e}"),
        )
    })
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct LogoutIn {
    everywhere: bool,
}

fn no_content() -> Handled {
    Ok(StatusCode::NO_CONTENT.into_response())
}

/// logout ends the sign-in session of the token (ADR 0045): its tokens, and every token refreshed from it, are
/// refused from now on — at once by this gateway, within SessionCheckTTL by the others. {"everywhere": true} ends
/// every session of the subject (all their devices). An expired token still names its session; an invalid one has
/// nothing to end.
pub(crate) fn logout(cfg: Arc<Config>) -> MethodRouter {
    endpoint(cfg, |cfg, headers, body| async move {
        let ctx = Ctx::background();
        let auth = authorization(&headers);
        let Some(raw) = auth.strip_prefix("Bearer ") else {
            return no_content();
        };
        let Ok(claims) = decode(&cfg, raw, false) else {
            return no_content();
        };
        let input: LogoutIn = bind(&headers, &body).unwrap_or_default();
        let creds = credentials_of(&cfg)?;
        let r = if input.everywhere {
            creds.end_sessions(&ctx, &claims.subject).await
        } else {
            creds.end_session(&ctx, &claims.sid).await
        };
        if let Err(e) = r {
            return Err(HttpError::new(
                StatusCode::SERVICE_UNAVAILABLE,
                format!("cannot end the session: {e}"),
            ));
        }
        if let Some(cache) = &cfg.sessions
            && !claims.sid.is_empty()
        {
            cache.set(&claims.sid, false);
        }
        no_content()
    })
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct ProjectIn {
    project: String,
}

/// switch_project reissues the caller's token with a new active project (ADR 0039: "a token regenerated each time
/// the user changes project"), keeping its subject, org and roles: the identity a caller already proved, unchanged,
/// just pointed at a different project from here on.
pub(crate) fn switch_project(cfg: Arc<Config>) -> MethodRouter {
    endpoint(cfg, |cfg, headers, body| async move {
        let ctx = Ctx::background();
        let claims = parse_token(&cfg, &authorization(&headers)).map_err(|e| unauthorized(&e))?;
        // an ended session cannot reissue a token (afresh, like a refresh)
        check_session(&ctx, &cfg, &claims, true).await?;
        let input: ProjectIn = bind(&headers, &body)
            .map_err(|_| HttpError::new(StatusCode::BAD_REQUEST, "project required"))?;
        if !input.project.is_empty() {
            let mut ok = cfg.dev_tokens;
            if let Some(access) = &cfg.project_access {
                let p = Principal {
                    subject: claims.subject.clone(),
                    org: claims.org.clone(),
                    project: claims.project.clone(),
                    roles: claims.roles.clone(),
                    ..Default::default()
                };
                ok = access(ctx.clone(), p, input.project.clone())
                    .await
                    .map_err(|e| {
                        HttpError::new(
                            StatusCode::SERVICE_UNAVAILABLE,
                            format!("cannot check the project: {e}"),
                        )
                    })?;
            }
            if !ok {
                return Err(HttpError::new(
                    StatusCode::FORBIDDEN,
                    format!("no access to project {}", input.project),
                ));
            }
        }
        let mut s = session_of(&claims);
        s.project = input.project;
        ok_json(&sign_session(&cfg, &s)?)
    })
}
