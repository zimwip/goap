use std::{error::Error as StdError, future::Future, sync::Arc};

use goap_authz::{Principal, principal_from, with_principal};
use goap_connect::axum::{
    Json,
    http::{HeaderMap, HeaderName, HeaderValue, StatusCode},
    response::{IntoResponse, Response},
    routing::{MethodRouter, get},
};
use goap_connect::{
    AnyRequest, AnyResponse, BoxFuture, ConnectError, Ctx, Interceptor, Next, async_trait,
};

// Headers set by the gateway. Services must only be reachable through the gateway, which always overwrites them.
pub const HEADER_SUBJECT: &str = "X-Goap-Subject";
pub const HEADER_ORG: &str = "X-Goap-Org";
pub const HEADER_ROLES: &str = "X-Goap-Roles";
pub const HEADER_PROJECT: &str = "X-Goap-Project";
/// HeaderOnBehalfOf is the end user a platform service acts for (authz.Principal.OnBehalfOf, ADR 0114); read only
/// for a system subject.
pub const HEADER_ON_BEHALF_OF: &str = "X-Goap-On-Behalf-Of";

fn get_header(h: &HeaderMap, name: &str) -> String {
    h.get(name)
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string()
}

/// `h.Set(name, value)`: the constants keep Go's canonical spelling, `http` lower-cases them.
fn set_header(h: &mut HeaderMap, name: &str, value: &str) {
    let name = HeaderName::try_from(name).expect("a valid header name");
    match HeaderValue::try_from(value) {
        Ok(v) => {
            h.insert(name, v);
        }
        Err(_) => {
            h.remove(name);
        }
    }
}

/// FromHeaders reads the principal of a request.
pub fn from_headers(h: &HeaderMap) -> Principal {
    let mut p = Principal {
        subject: get_header(h, HEADER_SUBJECT),
        org: get_header(h, HEADER_ORG),
        project: get_header(h, HEADER_PROJECT),
        ..Default::default()
    };
    let roles = get_header(h, HEADER_ROLES);
    if !roles.is_empty() {
        p.roles = roles.split(',').map(str::to_string).collect();
    }
    if p.is_system() {
        p.on_behalf_of = get_header(h, HEADER_ON_BEHALF_OF);
    }
    p
}

/// SetHeaders writes the identity headers of a principal, for a platform service reached as a Connect service.
pub fn set_headers(p: &Principal, h: &mut HeaderMap) {
    set_header(h, HEADER_SUBJECT, &p.subject);
    set_header(h, HEADER_ORG, &p.org);
    set_header(h, HEADER_PROJECT, &p.project);
    set_header(h, HEADER_ROLES, &p.roles.join(","));
    if p.is_system() && !p.on_behalf_of.is_empty() {
        set_header(h, HEADER_ON_BEHALF_OF, &p.on_behalf_of);
    } else {
        h.remove(HEADER_ON_BEHALF_OF);
    }
}

/// Forward is the client interceptor of a platform client that acts for its caller: the requests carry the principal
/// of their context (unless they already name one). Go's `Forward()` client option: add it to the client
/// (`ClientBuilder::interceptor(Forward)`).
#[derive(Debug, Clone, Copy, Default)]
pub struct Forward;

/// `Forward()`.
pub fn forward() -> Forward {
    Forward
}

#[async_trait]
impl Interceptor for Forward {
    async fn unary(
        &self,
        ctx: Ctx,
        mut req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        let p = principal_from(&ctx);
        if !p.anonymous() && req.spec.is_client && req.header(HEADER_SUBJECT).is_empty() {
            set_headers(&p, &mut req.headers);
        }
        next.run(ctx, req).await
    }
}

/// Extractor puts the caller identity in the context. Default is used when the request carries none (single-process
/// dev without gateway).
#[derive(Debug, Clone, Default)]
pub struct Extractor {
    pub default: Option<Principal>,
}

impl Extractor {
    /// Context returns ctx carrying the caller principal.
    pub fn context(&self, ctx: &Ctx, h: &HeaderMap) -> Ctx {
        let mut p = from_headers(h);
        if p.anonymous()
            && let Some(d) = &self.default
        {
            p = d.clone();
        }
        with_principal(ctx, p)
    }
}

/// Any error of a session function.
pub type BoxError = Box<dyn StdError + Send + Sync>;

/// What a session function returns: the JSON the caller is served.
pub type SessionFuture = BoxFuture<'static, Result<serde_json::Value, BoxError>>;

/// The session of [`who_am_i`]: what the platform makes of the principal of the request (Go's
/// `func(context.Context, authz.Principal) (any, error)`).
pub type Session = Arc<dyn Fn(Ctx, Principal) -> SessionFuture + Send + Sync>;

/// A [`Session`] of an async function.
pub fn session_fn<F, Fut>(f: F) -> Session
where
    F: Fn(Ctx, Principal) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<serde_json::Value, BoxError>> + Send + 'static,
{
    Arc::new(move |ctx, p| Box::pin(f(ctx, p)))
}

/// WhoAmI serves the caller as the platform sees it: what session makes of the principal of the request (the principal
/// completed by its User node, and what the client derives its UI from, access.Session). No session serves the
/// principal as it is; an error is a 503 (the organisation cannot be read yet), answered as echo does:
/// `{"message": "<error>"}`.
pub fn who_am_i<S>(x: Extractor, session: Option<Session>) -> MethodRouter<S>
where
    S: Clone + Send + Sync + 'static,
{
    get(move |headers: HeaderMap| {
        let x = x.clone();
        let session = session.clone();
        async move { serve_who_am_i(&x, session, &headers).await }
    })
}

async fn serve_who_am_i(x: &Extractor, session: Option<Session>, headers: &HeaderMap) -> Response {
    let ctx = x.context(&Ctx::background(), headers);
    let p = principal_from(&ctx);
    let Some(session) = session else {
        return (StatusCode::OK, Json(p)).into_response();
    };
    match session(ctx, p).await {
        Ok(s) => (StatusCode::OK, Json(s)).into_response(),
        Err(e) => (
            StatusCode::SERVICE_UNAVAILABLE,
            Json(serde_json::json!({ "message": e.to_string() })),
        )
            .into_response(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_authz::system;
    use goap_connect::axum::{Router, body::Body, http::Request};
    use goap_connect::{Interceptors, Spec, UnaryEnd};
    use http_body_util::BodyExt;
    use std::sync::Mutex;
    use tower::ServiceExt;

    /// The end user a platform service acts for travels with its identity (ADR 0114); a person never acts for another.
    #[test]
    fn on_behalf_of_travels_for_platform_services_only() {
        let mut h = HeaderMap::new();
        set_headers(&system("trigger:nightly", &[]).for_user("alice"), &mut h);
        let p = from_headers(&h);
        assert!(
            p.subject == "system:trigger:nightly"
                && p.on_behalf_of == "alice"
                && p.end_user() == "alice",
            "a service on behalf of alice: {p:?}"
        );
        set_headers(
            &Principal {
                subject: "bob".into(),
                ..Default::default()
            },
            &mut h,
        );
        assert!(
            h.get(HEADER_ON_BEHALF_OF).is_none(),
            "a person's headers carry no end user of a previous principal"
        );
        h.insert(
            HeaderName::try_from(HEADER_ON_BEHALF_OF).unwrap(),
            HeaderValue::from_static("alice"),
        );
        let p = from_headers(&h);
        assert!(
            p.on_behalf_of.is_empty() && p.end_user() == "bob",
            "a person acts for themselves: {p:?}"
        );
    }

    #[test]
    fn headers_round_trip_and_default() {
        let p = Principal {
            subject: "alice".into(),
            org: "ORG-A".into(),
            project: "PROJ-A".into(),
            roles: vec!["dev".into(), "admin".into()],
            ..Default::default()
        };
        let mut h = HeaderMap::new();
        set_headers(&p, &mut h);
        assert_eq!(h.get(HEADER_ROLES).unwrap(), "dev,admin");
        assert_eq!(from_headers(&h), p);

        let dev = Principal {
            subject: "dev".into(),
            ..Default::default()
        };
        let x = Extractor {
            default: Some(dev.clone()),
        };
        assert_eq!(
            principal_from(&x.context(&Ctx::background(), &HeaderMap::new())),
            dev
        );
        assert_eq!(principal_from(&x.context(&Ctx::background(), &h)), p);
        assert!(
            principal_from(&Extractor::default().context(&Ctx::background(), &HeaderMap::new()))
                .anonymous()
        );
    }

    async fn call(
        chain: &Interceptors,
        ctx: Ctx,
        is_client: bool,
        headers: HeaderMap,
    ) -> HeaderMap {
        let seen: Arc<Mutex<HeaderMap>> = Arc::default();
        let s = seen.clone();
        let end: Box<UnaryEnd> = Box::new(move |_ctx: Ctx, req: AnyRequest| {
            *s.lock().unwrap() = req.headers.clone();
            Box::pin(async move {
                Ok(AnyResponse {
                    headers: HeaderMap::new(),
                    trailers: HeaderMap::new(),
                    msg: Box::new(()),
                })
            })
        });
        let req = AnyRequest {
            spec: Spec {
                is_client,
                ..Default::default()
            },
            headers,
            msg: Box::new(()),
        };
        chain.run_unary(ctx, req, &*end).await.unwrap();
        seen.lock().unwrap().clone()
    }

    #[tokio::test]
    async fn forward_carries_the_principal_of_the_context() {
        let chain = Interceptors::none().with(forward());
        let ctx = with_principal(
            &Ctx::background(),
            Principal {
                subject: "alice".into(),
                ..Default::default()
            },
        );
        let h = call(&chain, ctx.clone(), true, HeaderMap::new()).await;
        assert_eq!(h.get(HEADER_SUBJECT).unwrap(), "alice");
        // a request naming its principal keeps it
        let mut named = HeaderMap::new();
        named.insert(
            HeaderName::try_from(HEADER_SUBJECT).unwrap(),
            HeaderValue::from_static("bob"),
        );
        let h = call(&chain, ctx.clone(), true, named).await;
        assert_eq!(h.get(HEADER_SUBJECT).unwrap(), "bob");
        // server side and anonymous: nothing added
        assert!(call(&chain, ctx, false, HeaderMap::new()).await.is_empty());
        assert!(
            call(&chain, Ctx::background(), true, HeaderMap::new())
                .await
                .is_empty()
        );
    }

    async fn get_json(
        router: Router,
        headers: &[(&'static str, &'static str)],
    ) -> (StatusCode, serde_json::Value) {
        let mut req = Request::get("/api/whoami");
        for (k, v) in headers {
            req = req.header(*k, *v);
        }
        let res = router
            .oneshot(req.body(Body::empty()).unwrap())
            .await
            .unwrap();
        let status = res.status();
        let body = res.into_body().collect().await.unwrap().to_bytes();
        (status, serde_json::from_slice(&body).unwrap())
    }

    #[tokio::test]
    async fn who_am_i_serves_the_session() {
        let r = Router::new().route("/api/whoami", who_am_i(Extractor::default(), None));
        let (status, body) = get_json(r, &[(HEADER_SUBJECT, "alice")]).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body, serde_json::json!({"subject": "alice"}));

        let session = session_fn(|_ctx, p: Principal| async move {
            Ok(serde_json::json!({"principal": p.subject, "can": {}}))
        });
        let r = Router::new().route("/api/whoami", who_am_i(Extractor::default(), Some(session)));
        let (status, body) = get_json(r, &[(HEADER_SUBJECT, "alice")]).await;
        assert_eq!(status, StatusCode::OK);
        assert_eq!(body, serde_json::json!({"principal": "alice", "can": {}}));

        let failing = session_fn(|_ctx, _p| async move {
            Err::<serde_json::Value, BoxError>("organisation unreadable".into())
        });
        let r = Router::new().route("/api/whoami", who_am_i(Extractor::default(), Some(failing)));
        let (status, body) = get_json(r, &[]).await;
        assert_eq!(status, StatusCode::SERVICE_UNAVAILABLE);
        assert_eq!(
            body,
            serde_json::json!({"message": "organisation unreadable"})
        );
    }
}
