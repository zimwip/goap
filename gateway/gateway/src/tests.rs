use std::{
    collections::HashMap,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::Duration,
};

use async_trait::async_trait;
use axum::{Router, http::HeaderMap};
use goap_authz::Principal;
use goap_connect::ConnectError;
use goap_ctx::Ctx;
use parking_lot::Mutex;

use crate::{
    gateway::{Issued, parse_token, sign},
    mode::{BearerMode, NoneMode},
    *,
};

/// fakeCredentials is an in-memory Credentials for local auth tests (ADR 0040): passwords in clear, sessions kept by
/// a credsvc service on a memory store (ADR 0045).
#[derive(Default)]
struct FakeCredentials {
    passwords: Mutex<HashMap<String, String>>,
    sessions: std::sync::OnceLock<goap_credsvc::Service>,
    /// down makes the session checks fail, as an unreachable credentials service
    down: AtomicBool,
}

impl FakeCredentials {
    fn with(passwords: &[(&str, &str)]) -> Arc<Self> {
        let f = FakeCredentials::default();
        for (s, p) in passwords {
            f.passwords.lock().insert(s.to_string(), p.to_string());
        }
        Arc::new(f)
    }

    fn svc(&self) -> &goap_credsvc::Service {
        self.sessions
            .get_or_init(|| goap_credsvc::Service::new(Arc::new(goap_credsvc::MemoryStore::new())))
    }
}

#[async_trait]
impl Credentials for FakeCredentials {
    async fn register(&self, _ctx: &Ctx, subject: &str, password: &str) -> Result<(), BoxError> {
        let mut p = self.passwords.lock();
        if p.contains_key(subject) {
            return Err(Box::new(ConnectError::already_exists("already exists")));
        }
        p.insert(subject.into(), password.into());
        Ok(())
    }

    async fn verify(&self, _ctx: &Ctx, subject: &str, password: &str) -> Result<bool, BoxError> {
        let p = self.passwords.lock();
        Ok(p.get(subject)
            .is_some_and(|x| !x.is_empty() && x == password))
    }

    async fn start_session(
        &self,
        ctx: &Ctx,
        subject: &str,
        max_age: Duration,
    ) -> Result<String, BoxError> {
        Ok(self.svc().start_session(ctx, subject, max_age).await?)
    }

    async fn session_active(&self, ctx: &Ctx, id: &str, subject: &str) -> Result<bool, BoxError> {
        if self.down.load(Ordering::SeqCst) {
            return Err("credentials unreachable".into());
        }
        Ok(self.svc().session_active(ctx, id, subject).await?)
    }

    async fn end_session(&self, ctx: &Ctx, id: &str) -> Result<(), BoxError> {
        Ok(self.svc().end_session(ctx, id).await?)
    }

    async fn end_sessions(&self, ctx: &Ctx, subject: &str) -> Result<(), BoxError> {
        Ok(self.svc().end_sessions(ctx, subject).await?)
    }
}

/// anyProject lets every caller work on every project.
fn any_project() -> ProjectAccess {
    project_access_fn(|_, _, _| async { Ok(true) })
}

async fn serve(router: Router) -> String {
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let url = format!("http://{}", ln.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(ln, router).await });
    url
}

/// An upstream answering `{}` to everything, recording the headers of the last request.
async fn upstream() -> (String, Arc<Mutex<HeaderMap>>) {
    let got = Arc::new(Mutex::new(HeaderMap::new()));
    let g = got.clone();
    let r = Router::new().fallback(move |h: HeaderMap| {
        let g = g.clone();
        async move {
            *g.lock() = h;
            "{}"
        }
    });
    (serve(r).await, got)
}

fn header(h: &Mutex<HeaderMap>, name: &str) -> String {
    h.lock()
        .get(name.to_lowercase())
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string()
}

fn secret() -> Vec<u8> {
    "s".repeat(32).into_bytes()
}

fn graph_route(upstream: &str) -> Vec<Route> {
    vec![Route {
        prefix: "/goap.graph.v1.GraphService/".into(),
        upstream: upstream.into(),
    }]
}

/// POST path with an optional bearer token and a JSON body: the status and the body.
async fn post(srv: &str, path: &str, token: &str, body: &str) -> (u16, String) {
    let mut req = reqwest::Client::new()
        .post(format!("{srv}{path}"))
        .header("Content-Type", "application/json")
        .body(body.to_string());
    if !token.is_empty() {
        req = req.header("Authorization", format!("Bearer {token}"));
    }
    let resp = req.send().await.unwrap();
    let code = resp.status().as_u16();
    (code, resp.text().await.unwrap_or_default())
}

fn token_of(body: &str) -> String {
    serde_json::from_str::<serde_json::Value>(body)
        .ok()
        .and_then(|v| v["token"].as_str().map(String::from))
        .unwrap_or_default()
}

fn issued_of(body: &str) -> Issued {
    serde_json::from_str(body).unwrap_or_default()
}

#[tokio::test]
async fn auth_and_routing() {
    let (up, got) = upstream().await;
    let router = mount(
        Router::new(),
        Config {
            auth_mode: "hs256".into(),
            jwt_secret: secret(),
            dev_tokens: true,
            routes: graph_route(&up),
            ..Default::default()
        },
    )
    .unwrap();
    let srv = serve(router).await;

    let call = |token: String| {
        let srv = srv.clone();
        async move {
            let mut req = reqwest::Client::new()
                .post(format!("{srv}/goap.graph.v1.GraphService/ListBaselines"))
                .header("Content-Type", "application/json")
                .header(HEADER_SUBJECT, "spoofed")
                .body("{}");
            if !token.is_empty() {
                req = req.header("Authorization", format!("Bearer {token}"));
            }
            req.send().await.unwrap().status().as_u16()
        }
    };
    assert_eq!(call(String::new()).await, 401);
    assert_eq!(call("garbage".into()).await, 401);
    let (_, body) = post(
        &srv,
        "/auth/dev-token",
        "",
        r#"{"subject":"alice","org":"acme","project":"PROJ-X","roles":["admin"]}"#,
    )
    .await;
    let tok = token_of(&body);
    assert_eq!(call(tok.clone()).await, 200);
    assert!(
        header(&got, HEADER_SUBJECT) == "alice"
            && header(&got, HEADER_ROLES) == "admin"
            && header(&got, HEADER_PROJECT) == "PROJ-X",
        "identity not propagated: {:?}",
        got.lock()
    );
    assert!(
        header(&got, "Authorization").is_empty(),
        "the token is not forwarded"
    );

    // switching project reissues the token with the same subject/org/roles but a new project (ADR 0039)
    let (code, body) = post(
        &srv,
        "/auth/dev-token/project",
        &tok,
        r#"{"project":"PROJ-Y"}"#,
    )
    .await;
    let sw = token_of(&body);
    assert!(
        code == 200 && !sw.is_empty(),
        "switch project: status {code}, token {sw:?}"
    );
    assert_eq!(call(sw).await, 200);
    assert!(
        header(&got, HEADER_SUBJECT) == "alice"
            && header(&got, HEADER_ROLES) == "admin"
            && header(&got, HEADER_PROJECT) == "PROJ-Y",
        "switched identity not propagated"
    );
    // a bogus/expired token cannot switch project
    let resp = reqwest::Client::new()
        .post(format!("{srv}/auth/dev-token/project"))
        .header("Authorization", "Bearer garbage")
        .body(r#"{"project":"PROJ-Z"}"#)
        .send()
        .await
        .unwrap();
    assert_eq!(
        resp.status().as_u16(),
        401,
        "switch project with a bad token"
    );
    let status: PlatformStatus = reqwest::get(format!("{srv}/api/status"))
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    // the fake upstream answers 200 on /readyz
    assert!(
        status.status == "ok" && status.services.len() == 1 && status.services[0].name == "graph",
        "status {status:?}"
    );
}

/// Local auth (ADR 0040): a subject registers, logs in and calls through with the token; a duplicate registration
/// and a wrong password are refused; /api/auth/config tells the web which mode is active.
#[tokio::test]
async fn local_auth() {
    let (up, got) = upstream().await;
    let creds = FakeCredentials::with(&[]);
    // subjects OnSignIn was called for (ADR 0042: the User node is created at sign-in)
    let declared: Arc<Mutex<Vec<String>>> = Arc::default();
    let d = declared.clone();
    let on_sign_in = on_sign_in_fn(move |_, subject: String| {
        let d = d.clone();
        async move {
            if subject == "broken" {
                return Err::<(), BoxError>("graph unavailable".into());
            }
            d.lock().push(subject);
            Ok(())
        }
    });
    let router = mount(
        Router::new(),
        Config {
            auth_mode: "local".into(),
            jwt_secret: secret(),
            credentials: Some(creds.clone()),
            on_sign_in: Some(on_sign_in),
            project_access: Some(any_project()),
            routes: graph_route(&up),
            ..Default::default()
        },
    )
    .unwrap();
    let srv = serve(router).await;

    let cfg: serde_json::Value = reqwest::get(format!("{srv}/api/auth/config"))
        .await
        .unwrap()
        .json()
        .await
        .unwrap();
    assert!(
        cfg["authMode"] == "local" && cfg["signsIn"] == true,
        "auth config = {cfg}"
    );

    let creds_body = |s: &str, p: &str| format!(r#"{{"subject":"{s}","password":"{p}"}}"#);
    let (code, body) = post(
        &srv,
        "/auth/register",
        "",
        &creds_body("alice", "correct horse"),
    )
    .await;
    assert!(
        code == 200 && !token_of(&body).is_empty(),
        "register: {code} {body}"
    );
    let (code, _) = post(
        &srv,
        "/auth/register",
        "",
        &creds_body("alice", "another password"),
    )
    .await;
    assert_eq!(code, 409, "duplicate register");
    let (code, _) = post(
        &srv,
        "/auth/login",
        "",
        &creds_body("alice", "wrong password"),
    )
    .await;
    assert_eq!(code, 401, "login with wrong password");
    let (code, body) = post(
        &srv,
        "/auth/login",
        "",
        &creds_body("alice", "correct horse"),
    )
    .await;
    let tok = token_of(&body);
    assert!(code == 200 && !tok.is_empty(), "login: {code} {body}");
    let (code, _) = post(
        &srv,
        "/goap.graph.v1.GraphService/ListBaselines",
        &tok,
        "{}",
    )
    .await;
    assert_eq!(code, 200, "call with login token");
    assert_eq!(header(&got, HEADER_SUBJECT), "alice", "subject propagated");

    assert_eq!(
        *declared.lock(),
        vec!["alice".to_string(), "alice".to_string()],
        "OnSignIn must run on register and on login only"
    );
    // a user that cannot be declared in the graph is not signed in
    creds
        .passwords
        .lock()
        .insert("broken".into(), "some password".into());
    let (code, body) = post(
        &srv,
        "/auth/login",
        "",
        &creds_body("broken", "some password"),
    )
    .await;
    assert!(
        code == 503 && token_of(&body).is_empty(),
        "login when OnSignIn fails: {code} {body}"
    );

    // switching project reissues the token in local mode too (ADR 0039)
    let (code, body) = post(
        &srv,
        "/auth/dev-token/project",
        &tok,
        r#"{"project":"PROJ-A"}"#,
    )
    .await;
    assert!(
        code == 200 && !token_of(&body).is_empty(),
        "switch project: {code} {body}"
    );

    // logout answers (no body, no content type)
    let resp = reqwest::Client::new()
        .post(format!("{srv}/auth/logout"))
        .header("Authorization", format!("Bearer {tok}"))
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status().as_u16(), 204, "logout");
}

/// A valid token is refreshed with a fresh expiry, keeping its identity and its sign-in time; an expired one is
/// refused as such (401 "token expired"), and so is a refresh past the maximum session.
#[tokio::test]
async fn refresh_token() {
    let creds = FakeCredentials::with(&[]);
    let new_server = |ttl: Duration, max_session: Duration| {
        let creds = creds.clone();
        async move {
            let r = mount(
                Router::new(),
                Config {
                    auth_mode: "local".into(),
                    jwt_secret: secret(),
                    credentials: Some(creds),
                    token_ttl: ttl,
                    max_session,
                    project_access: Some(any_project()),
                    ..Default::default()
                },
            )
            .unwrap();
            serve(r).await
        }
    };
    let claims_of = |tok: &str| {
        parse_token(
            &Config {
                jwt_secret: secret(),
                ..Default::default()
            },
            &format!("Bearer {tok}"),
        )
        .unwrap()
    };
    let hour = Duration::from_secs(3600);
    let day = Duration::from_secs(24 * 3600);

    let srv = new_server(hour, day).await;
    let (code, body) = post(
        &srv,
        "/auth/register",
        "",
        r#"{"subject":"alice","password":"correct horse"}"#,
    )
    .await;
    let reg = issued_of(&body);
    let until = (reg.expires_at - chrono::Utc::now()).num_seconds();
    assert!(
        code == 200 && !reg.token.is_empty() && (59 * 60..=3600).contains(&until),
        "register: {code} {body}"
    );
    let signed_in_at = claims_of(&reg.token).auth_time;

    // switch project, then refresh: subject, project and sign-in time are kept
    let (code, body) = post(
        &srv,
        "/auth/dev-token/project",
        &reg.token,
        r#"{"project":"PROJ-A"}"#,
    )
    .await;
    assert_eq!(code, 200, "switch: {body}");
    let sw = issued_of(&body);
    let (code, body) = post(&srv, "/auth/refresh", &sw.token, "").await;
    let refreshed = issued_of(&body);
    assert!(
        code == 200 && !refreshed.token.is_empty(),
        "refresh: {code} {body}"
    );
    let c = claims_of(&refreshed.token);
    assert!(
        c.subject == "alice" && c.project == "PROJ-A" && c.auth_time == signed_in_at,
        "refreshed claims: {c:?}"
    );
    let (code, _) = post(&srv, "/auth/refresh", "", "").await;
    assert_eq!(code, 401, "refresh without a token");

    // an expired token is refused, and says so
    let expired = new_server(Duration::from_millis(1), day).await;
    let (_, body) = post(
        &expired,
        "/auth/login",
        "",
        r#"{"subject":"alice","password":"correct horse"}"#,
    )
    .await;
    let short = issued_of(&body);
    tokio::time::sleep(Duration::from_millis(1100)).await; // JWT times have a one-second resolution
    let (code, body) = post(&expired, "/auth/refresh", &short.token, "").await;
    assert!(
        code == 401 && body.contains("token expired"),
        "refresh of an expired token: {code} {body}"
    );

    // past the maximum session, a valid token is not refreshed any more
    let old = new_server(hour, Duration::from_nanos(1)).await;
    let (_, body) = post(
        &old,
        "/auth/login",
        "",
        r#"{"subject":"alice","password":"correct horse"}"#,
    )
    .await;
    let fresh = issued_of(&body);
    let (code, body) = post(&old, "/auth/refresh", &fresh.token, "").await;
    assert!(
        code == 401 && body.contains("session expired"),
        "refresh past the maximum session: {code} {body}"
    );
}

/// Signing out ends the session server side (ADR 0045): its tokens, and the ones refreshed from it, are refused at
/// once; signing out everywhere ends the subject's other sessions; a token from before sessions is refused; a
/// session check the credentials service cannot answer is a 503, not a sign-out.
#[tokio::test]
async fn logout_revokes_the_session() {
    let (up, _) = upstream().await;
    let creds = FakeCredentials::with(&[("alice", "correct horse")]);
    let cfg = Config {
        auth_mode: "local".into(),
        jwt_secret: secret(),
        credentials: Some(creds.clone()),
        session_check_ttl: Duration::from_secs(3600),
        routes: graph_route(&up),
        ..Default::default()
    };
    let srv = serve(mount(Router::new(), cfg.clone()).unwrap()).await;
    let login = || {
        let srv = srv.clone();
        async move {
            let (code, body) = post(
                &srv,
                "/auth/login",
                "",
                r#"{"subject":"alice","password":"correct horse"}"#,
            )
            .await;
            let out = issued_of(&body);
            assert!(code == 200 && !out.token.is_empty(), "login: {code} {body}");
            out.token
        }
    };
    let call = |token: String| {
        let srv = srv.clone();
        async move {
            post(
                &srv,
                "/goap.graph.v1.GraphService/ListBaselines",
                &token,
                "{}",
            )
            .await
        }
    };

    let tok = login().await;
    assert_eq!(call(tok.clone()).await.0, 200, "call with a fresh token");
    let (code, body) = post(&srv, "/auth/refresh", &tok, "").await;
    assert_eq!(code, 200, "refresh: {body}");
    let refreshed = issued_of(&body);
    // sign out with the first token: the refreshed one, of the same session, is refused at once
    assert_eq!(post(&srv, "/auth/logout", &tok, "").await.0, 204, "logout");
    let (code, body) = call(refreshed.token.clone()).await;
    assert!(
        code == 401 && body.contains("session ended"),
        "call after sign-out: {code} {body}"
    );
    assert_eq!(
        post(&srv, "/auth/refresh", &refreshed.token, "").await.0,
        401,
        "refresh after sign-out"
    );

    // signing out everywhere ends the other sessions too (checked afresh by a refresh)
    let (a, b) = (login().await, login().await);
    assert_eq!(
        post(&srv, "/auth/logout", &a, r#"{"everywhere":true}"#)
            .await
            .0,
        204,
        "logout everywhere"
    );
    assert_eq!(
        post(&srv, "/auth/refresh", &b, "").await.0,
        401,
        "another session after signing out everywhere"
    );

    // a token from before sessions (no sid) is refused
    let old = sign(&cfg, "alice", "", "", &[]).unwrap();
    assert_eq!(call(old).await.0, 401, "token without a session");

    // the credentials service down: a session never checked is a 503, a known one keeps its last state
    let known = login().await;
    assert_eq!(call(known.clone()).await.0, 200, "call");
    let unknown = login().await;
    creds.down.store(true, Ordering::SeqCst);
    assert_eq!(
        call(unknown).await.0,
        503,
        "unchecked session while credentials are down"
    );
    assert_eq!(
        call(known).await.0,
        200,
        "known session while credentials are down"
    );
}

/// Switching project reissues a token, so it is checked like a request: an ended session cannot do it (ADR 0045),
/// and the caller must have access to the target project (ADR 0039); with no access check wired no project is
/// granted, and only clearing the project is allowed.
#[tokio::test]
async fn switch_project_checks_session_and_access() {
    let creds = FakeCredentials::with(&[("alice", "correct horse")]);
    let member = project_access_fn(|_, p: Principal, project: String| async move {
        Ok(p.subject == "alice" && project == "PROJ-A")
    });
    let setup = |access: Option<ProjectAccess>| {
        let creds = creds.clone();
        async move {
            serve(
                mount(
                    Router::new(),
                    Config {
                        auth_mode: "local".into(),
                        jwt_secret: secret(),
                        credentials: Some(creds),
                        project_access: access,
                        ..Default::default()
                    },
                )
                .unwrap(),
            )
            .await
        }
    };
    let login = |srv: String| async move {
        let (code, body) = post(
            &srv,
            "/auth/login",
            "",
            r#"{"subject":"alice","password":"correct horse"}"#,
        )
        .await;
        let out = issued_of(&body);
        assert!(code == 200 && !out.token.is_empty(), "login: {code} {body}");
        out.token
    };

    let srv = setup(Some(member)).await;
    let tok = login(srv.clone()).await;
    let (code, body) = post(
        &srv,
        "/auth/dev-token/project",
        &tok,
        r#"{"project":"PROJ-A"}"#,
    )
    .await;
    assert_eq!(code, 200, "a member's project: {body}");
    let (code, _) = post(
        &srv,
        "/auth/dev-token/project",
        &tok,
        r#"{"project":"PROJ-B"}"#,
    )
    .await;
    assert_eq!(code, 403, "a project the caller has no access to");
    let (code, _) = post(&srv, "/auth/dev-token/project", &tok, r#"{"project":""}"#).await;
    assert_eq!(code, 200, "clearing the project");
    // the session ends: the token no longer switches project
    assert_eq!(post(&srv, "/auth/logout", &tok, "").await.0, 204, "logout");
    let (code, body) = post(
        &srv,
        "/auth/dev-token/project",
        &tok,
        r#"{"project":"PROJ-A"}"#,
    )
    .await;
    assert!(
        code == 401 && body.contains("session ended"),
        "switch with an ended session: {code} {body}"
    );

    // nothing wired: no project is granted
    let bare = setup(None).await;
    let tok = login(bare.clone()).await;
    let (code, _) = post(
        &bare,
        "/auth/dev-token/project",
        &tok,
        r#"{"project":"PROJ-A"}"#,
    )
    .await;
    assert_eq!(code, 403, "switch with no access check");
}

/// The three auth strategies state what they are (ADR 0068): the gateway, the commands and the web read these,
/// never the name; an unknown or unset name has no strategy.
#[tokio::test]
async fn auth_modes_are_strategies() {
    let secret = "k".repeat(32).into_bytes();
    struct Case {
        name: &'static str,
        open: bool,
        verifies_tokens: bool,
        sessions: bool,
        routes: &'static [&'static str],
        missing_secret: bool,
        missing_creds: bool,
    }
    let cases = [
        Case {
            name: "none",
            open: true,
            verifies_tokens: false,
            sessions: false,
            routes: &[],
            missing_secret: false,
            missing_creds: false,
        },
        Case {
            name: "hs256",
            open: false,
            verifies_tokens: true,
            sessions: false,
            routes: &["/auth/dev-token", "/auth/dev-token/project"],
            missing_secret: true,
            missing_creds: false,
        },
        Case {
            name: "local",
            open: false,
            verifies_tokens: true,
            sessions: true,
            routes: &[
                "/auth/register",
                "/auth/login",
                "/auth/logout",
                "/auth/refresh",
                "/auth/dev-token/project",
            ],
            missing_secret: true,
            missing_creds: true,
        },
    ];
    for c in cases {
        let m = lookup_mode(c.name).unwrap();
        assert!(
            m.name() == c.name
                && m.open() == c.open
                && m.verifies_tokens() == c.verifies_tokens
                && m.sessions() == c.sessions,
            "{}",
            c.name
        );
        let creds: Arc<dyn Credentials> = FakeCredentials::with(&[]);
        let cfg = Config {
            auth_mode: c.name.into(),
            jwt_secret: secret.clone(),
            dev_tokens: true,
            credentials: Some(creds.clone()),
            ..Default::default()
        };
        if let Err(e) = m.validate(&cfg) {
            panic!("{}: {e}", c.name);
        }
        let no_secret = Config {
            auth_mode: c.name.into(),
            credentials: Some(creds),
            ..Default::default()
        };
        assert_eq!(
            m.validate(&no_secret).is_err(),
            c.missing_secret,
            "{} without a secret",
            c.name
        );
        let no_creds = Config {
            auth_mode: c.name.into(),
            jwt_secret: secret.clone(),
            ..Default::default()
        };
        assert_eq!(
            m.validate(&no_creds).is_err(),
            c.missing_creds,
            "{} without Credentials",
            c.name
        );
        let mut got: Vec<&str> = m.routes(&cfg).iter().map(|r| r.path).collect();
        got.sort();
        let mut want = c.routes.to_vec();
        want.sort();
        assert_eq!(got, want, "{} routes", c.name);
    }
    for name in ["", "oidc"] {
        assert!(lookup_mode(name).is_err(), "mode {name:?} must be unknown");
        assert!(
            mount_auth_endpoints(
                Router::new(),
                Config {
                    auth_mode: name.into(),
                    ..Default::default()
                }
            )
            .is_err(),
            "mounting mode {name:?} must fail"
        );
    }
    let e = lookup_mode("oidc").err().unwrap().to_string();
    assert_eq!(e, r#"unknown auth mode "oidc" (one of [hs256 local none])"#);
    // hs256 mints dev tokens only when asked
    let routes = BearerMode.routes(&Config {
        auth_mode: "hs256".into(),
        jwt_secret: secret.clone(),
        ..Default::default()
    });
    assert!(routes.is_empty(), "hs256 without DevTokens adds routes");
    // none authenticates every caller as the dev administrator
    let p = NoneMode
        .authenticate(
            &Ctx::background(),
            &mut HeaderMap::new(),
            &Config::default(),
        )
        .await
        .unwrap();
    assert!(
        p.subject == "dev" && p.roles.contains(&"admin".to_string()),
        "none: {p:?}"
    );
}

/// An unset auth mode is an error, not an open gateway: only an explicit "none" lets everybody in as the dev
/// principal.
#[test]
fn empty_auth_mode_is_refused() {
    assert!(
        authenticator(Config::default()).is_err(),
        "an empty auth mode must be refused"
    );
    if let Err(e) = authenticator(Config {
        auth_mode: "none".into(),
        ..Default::default()
    }) {
        panic!("explicit none: {e}");
    }
}
