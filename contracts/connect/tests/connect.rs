//! The protocol end to end: a service served by `ServiceRouter` on a local port, called raw (as the web calls) and
//! through `Client`.
use std::{
    sync::{Arc, Mutex},
    time::Duration,
};

use goap_connect::{
    AnyRequest, AnyResponse, Before, Client, Code, ConnectError, Ctx, Interceptor, Interceptors,
    Next, Request, Response, ServerStream, ServiceRouter, async_trait, protocol,
};

#[derive(Clone, PartialEq, prost::Message, serde::Serialize, serde::Deserialize)]
#[serde(rename_all = "camelCase", default)]
pub struct Echo {
    #[prost(string, tag = "1")]
    pub text: String,
    #[prost(int32, tag = "2")]
    #[serde(skip_serializing_if = "is_zero")]
    pub count: i32,
}

fn is_zero(n: &i32) -> bool {
    *n == 0
}

#[derive(Clone)]
struct Who(String);

const SVC: &str = "goap.test.v1.EchoService";

fn router(ics: Interceptors) -> axum::Router {
    ServiceRouter::new(SVC, ics)
        .unary("Echo", |ctx: Ctx, req: Request<Echo>| async move {
            let who = ctx.value::<Who>().map(|w| w.0.clone()).unwrap_or_default();
            let mut r = Response::new(Echo {
                text: format!("{}{}", who, req.msg.text),
                count: req.msg.count + 1,
            });
            r.headers
                .insert("x-seen", req.header("x-hello").parse().unwrap());
            r.trailers.insert("x-done", "yes".parse().unwrap());
            Ok(r)
        })
        .unary("Fail", |_ctx: Ctx, _req: Request<Echo>| async move {
            Err::<Response<Echo>, _>(
                ConnectError::not_found("no such node")
                    .with_meta("Goap-Error-Reason", "nothing-to-commit"),
            )
        })
        .unary("Deadline", |ctx: Ctx, _req: Request<Echo>| async move {
            let has = ctx.deadline().is_some();
            tokio::select! {
                _ = tokio::time::sleep(Duration::from_millis(300)) => {},
                e = ctx.done() => return Err(e.into()),
            }
            Ok(Response::new(Echo {
                text: has.to_string(),
                count: 0,
            }))
        })
        .server_stream("Count", |_ctx: Ctx, req: Request<Echo>| async move {
            let n = req.msg.count;
            let fail = req.msg.text == "fail";
            let s = futures::stream::iter(
                (0..n)
                    .map(|i| {
                        Ok(Echo {
                            text: format!("m{i}"),
                            count: i,
                        })
                    })
                    .chain(fail.then(|| {
                        Err(ConnectError::resource_exhausted("too many")
                            .with_meta("x-why", "quota"))
                    })),
            );
            let mut r: Response<ServerStream<Echo>> = Response::new(Box::pin(s));
            r.headers.insert("x-stream", "1".parse().unwrap());
            Ok(r)
        })
        .server_stream("Refuse", |_ctx: Ctx, _req: Request<Echo>| async move {
            Err::<Response<ServerStream<Echo>>, _>(ConnectError::permission_denied("not yours"))
        })
        .build()
}

async fn serve(ics: Interceptors) -> String {
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = ln.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(ln, router(ics)).await.unwrap() });
    format!("http://{addr}")
}

fn url(base: &str, m: &str) -> String {
    format!("{base}/{SVC}/{m}")
}

#[tokio::test]
async fn unary_json_as_the_web_sends_it() {
    let base = serve(Interceptors::none()).await;
    let resp = reqwest::Client::new()
        .post(url(&base, "Echo"))
        .header("content-type", "application/json")
        .header("x-hello", "hi")
        .body(r#"{"text":"a","count":2,"unknownField":1}"#)
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["content-type"], "application/json");
    assert_eq!(resp.headers()["x-seen"], "hi");
    assert_eq!(resp.headers()["trailer-x-done"], "yes");
    let v: serde_json::Value = resp.json().await.unwrap();
    assert_eq!(v, serde_json::json!({"text": "a", "count": 3}));
}

#[tokio::test]
async fn unary_through_the_client_both_codecs() {
    let base = serve(Interceptors::none()).await;
    for client in [Client::new(&base), Client::builder(&base).json().build()] {
        let mut req = Request::new(Echo {
            text: "x".into(),
            count: 1,
        });
        req.headers.insert("x-hello", "there".parse().unwrap());
        let r: Response<Echo> = client
            .unary(&Ctx::background(), &format!("/{SVC}/Echo"), req)
            .await
            .unwrap();
        assert_eq!(
            r.msg,
            Echo {
                text: "x".into(),
                count: 2
            }
        );
        assert_eq!(r.header("x-seen"), "there");
        assert_eq!(r.trailers["x-done"], "yes");
        assert!(r.headers.get("trailer-x-done").is_none());
    }
}

#[tokio::test]
async fn errors_on_the_wire_and_in_the_client() {
    let base = serve(Interceptors::none()).await;
    let resp = reqwest::Client::new()
        .post(url(&base, "Fail"))
        .header("content-type", "application/json")
        .body("{}")
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 404);
    assert_eq!(resp.headers()["goap-error-reason"], "nothing-to-commit");
    let v: serde_json::Value = resp.json().await.unwrap();
    assert_eq!(
        v,
        serde_json::json!({"code": "not_found", "message": "no such node"})
    );

    let err = Client::new(&base)
        .unary::<Echo, Echo>(
            &Ctx::background(),
            &format!("/{SVC}/Fail"),
            Request::new(Echo::default()),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::NotFound);
    assert_eq!(err.message(), "no such node");
    assert_eq!(err.to_string(), "not_found: no such node");
    assert_eq!(err.meta()["goap-error-reason"], "nothing-to-commit");
    assert!(err.is_wire());

    // an unknown procedure is a 404 the client reads as unimplemented
    let err = Client::new(&base)
        .unary::<Echo, Echo>(
            &Ctx::background(),
            &format!("/{SVC}/Nope"),
            Request::new(Echo::default()),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Unimplemented);
}

#[tokio::test]
async fn unsupported_content_type_and_method() {
    let base = serve(Interceptors::none()).await;
    let http = reqwest::Client::new();
    let resp = http
        .post(url(&base, "Echo"))
        .header("content-type", "text/plain")
        .body("x")
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 415);
    assert!(resp.headers().contains_key("accept-post"));
    let resp = http.get(url(&base, "Echo")).send().await.unwrap();
    assert_eq!(resp.status(), 405);
    let resp = http
        .post(url(&base, "Echo"))
        .header("content-type", "application/json")
        .header("connect-protocol-version", "2")
        .body("{}")
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 400);
}

#[tokio::test]
async fn timeout_becomes_the_deadline() {
    let base = serve(Interceptors::none()).await;
    let client = Client::new(&base);
    let (ctx, _c) = Ctx::background().with_timeout(Duration::from_millis(50));
    let err = client
        .unary::<Echo, Echo>(
            &ctx,
            &format!("/{SVC}/Deadline"),
            Request::new(Echo::default()),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::DeadlineExceeded, "{err}");
    let (ctx, _c) = Ctx::background().with_timeout(Duration::from_secs(5));
    let r: Response<Echo> = client
        .unary(
            &ctx,
            &format!("/{SVC}/Deadline"),
            Request::new(Echo::default()),
        )
        .await
        .unwrap();
    assert_eq!(r.msg.text, "true");
    // the server alone cuts the call at the timeout the client sent
    let resp = reqwest::Client::new()
        .post(url(&base, "Deadline"))
        .header("content-type", "application/json")
        .header("connect-timeout-ms", "20")
        .body("{}")
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 504);
    let (ctx, cancel) = Ctx::background().with_cancel();
    cancel.cancel();
    let err = client
        .unary::<Echo, Echo>(&ctx, &format!("/{SVC}/Echo"), Request::new(Echo::default()))
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Canceled);
}

struct Trace(&'static str, Arc<Mutex<Vec<String>>>);

#[async_trait]
impl Interceptor for Trace {
    async fn unary(
        &self,
        ctx: Ctx,
        req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        self.1.lock().unwrap().push(format!(
            "{} in {} {}",
            self.0,
            req.spec.method(),
            req.message_json()["text"]
        ));
        let r = next.run(ctx, req).await;
        self.1.lock().unwrap().push(format!("{} out", self.0));
        r
    }
}

#[tokio::test]
async fn interceptors_on_server_and_client() {
    let log = Arc::new(Mutex::new(vec![]));
    let identify = Before(|ctx: Ctx, req: &mut AnyRequest| {
        let who = req.header("x-subject").to_string();
        if who.is_empty() {
            return Err(ConnectError::unauthenticated(
                "the caller is not identified",
            ));
        }
        assert!(!req.spec.is_client);
        Ok(ctx.with(Who(who)))
    });
    let ics = Interceptors::none()
        .with(Trace("a", log.clone()))
        .with(Trace("b", log.clone()))
        .with(identify);
    let base = serve(ics).await;

    let err = Client::new(&base)
        .unary::<Echo, Echo>(
            &Ctx::background(),
            &format!("/{SVC}/Echo"),
            Request::new(Echo::default()),
        )
        .await
        .unwrap_err();
    assert_eq!(err.code(), Code::Unauthenticated);

    let client = Client::builder(&base)
        .interceptor(Before(|ctx: Ctx, req: &mut AnyRequest| {
            assert!(req.spec.is_client);
            req.headers.insert("x-subject", "alice:".parse().unwrap());
            Ok(ctx)
        }))
        .build();
    let r: Response<Echo> = client
        .unary(
            &Ctx::background(),
            &format!("/{SVC}/Echo"),
            Request::new(Echo {
                text: "z".into(),
                count: 0,
            }),
        )
        .await
        .unwrap();
    assert_eq!(r.msg.text, "alice:z");
    let got = log.lock().unwrap().clone();
    assert_eq!(
        &got[4..],
        ["a in Echo \"z\"", "b in Echo \"z\"", "b out", "a out"]
    );

    // streams go through the stream hook only
    let mut s = client
        .server_stream::<Echo, Echo>(
            &Ctx::background(),
            &format!("/{SVC}/Count"),
            Request::new(Echo {
                text: String::new(),
                count: 2,
            }),
        )
        .await
        .unwrap();
    use futures::StreamExt;
    let mut n = 0;
    while let Some(m) = s.msg.next().await {
        m.unwrap();
        n += 1;
    }
    assert_eq!(n, 2);
}

#[tokio::test]
async fn server_stream_as_the_web_reads_it() {
    let base = serve(Interceptors::none()).await;
    let body = protocol::envelope(0, br#"{"count":2,"text":"fail"}"#);
    let resp = reqwest::Client::new()
        .post(url(&base, "Count"))
        .header("content-type", "application/connect+json")
        .header("connect-protocol-version", "1")
        .body(body.to_vec())
        .send()
        .await
        .unwrap();
    assert_eq!(resp.status(), 200);
    assert_eq!(resp.headers()["content-type"], "application/connect+json");
    assert_eq!(resp.headers()["x-stream"], "1");
    let bytes = resp.bytes().await.unwrap();
    let mut rd = protocol::EnvelopeReader::new();
    rd.push(&bytes);
    let mut frames = vec![];
    while let Some((flags, p)) = rd.next_envelope() {
        frames.push((
            flags,
            serde_json::from_slice::<serde_json::Value>(&p).unwrap(),
        ));
    }
    assert_eq!(rd.remaining(), 0);
    assert_eq!(
        frames,
        vec![
            (0, serde_json::json!({"text": "m0"})),
            (0, serde_json::json!({"text": "m1", "count": 1})),
            (
                2,
                serde_json::json!({"error": {"code": "resource_exhausted", "message": "too many"}, "metadata": {"X-Why": ["quota"]}})
            ),
        ]
    );
}

#[tokio::test]
async fn server_stream_through_the_client() {
    use futures::StreamExt;
    let base = serve(Interceptors::none()).await;
    for client in [Client::new(&base), Client::builder(&base).json().build()] {
        let r = client
            .server_stream::<Echo, Echo>(
                &Ctx::background(),
                &format!("/{SVC}/Count"),
                Request::new(Echo {
                    text: String::new(),
                    count: 3,
                }),
            )
            .await
            .unwrap();
        assert_eq!(r.header("x-stream"), "1");
        let got: Vec<_> = r.msg.collect().await;
        assert_eq!(got.len(), 3);
        assert_eq!(got[2].as_ref().unwrap().text, "m2");

        let r = client
            .server_stream::<Echo, Echo>(
                &Ctx::background(),
                &format!("/{SVC}/Count"),
                Request::new(Echo {
                    text: "fail".into(),
                    count: 1,
                }),
            )
            .await
            .unwrap();
        let got: Vec<_> = r.msg.collect().await;
        assert_eq!(got.len(), 2);
        let err = got[1].as_ref().unwrap_err();
        assert_eq!(err.code(), Code::ResourceExhausted);
        assert_eq!(err.meta()["x-why"], "quota");

        // a refusal before the first message ends the stream at once
        let r = client
            .server_stream::<Echo, Echo>(
                &Ctx::background(),
                &format!("/{SVC}/Refuse"),
                Request::new(Echo::default()),
            )
            .await
            .unwrap();
        let got: Vec<_> = r.msg.collect().await;
        assert_eq!(got.len(), 1);
        assert_eq!(got[0].as_ref().unwrap_err().code(), Code::PermissionDenied);
    }
}

#[test]
fn codes_statuses_and_wire_form() {
    assert_eq!(Code::Canceled.http_status(), 499);
    assert_eq!(Code::FailedPrecondition.http_status(), 400);
    assert_eq!(Code::Aborted.http_status(), 409);
    assert_eq!(Code::Unauthenticated.http_status(), 401);
    assert_eq!(Code::ResourceExhausted.http_status(), 429);
    assert_eq!(Code::Unavailable.number(), 14);
    for c in Code::ALL {
        assert_eq!(c.as_str().parse::<Code>().unwrap(), c);
    }
    assert!("nope".parse::<Code>().is_err());
    let e = ConnectError::unimplemented_procedure("/goap.graph.v1.GraphService/GetNode");
    assert_eq!(
        e.to_string(),
        "unimplemented: goap.graph.v1.GraphService.GetNode is not implemented"
    );
    assert_eq!(ConnectError::internal("").to_string(), "internal");
    let wire: serde_json::Value =
        serde_json::from_slice(&ConnectError::aborted("raced").to_json()).unwrap();
    assert_eq!(
        wire,
        serde_json::json!({"code": "aborted", "message": "raced"})
    );
    assert_eq!(
        goap_connect::canonical("goap-error-reason"),
        "Goap-Error-Reason"
    );
}

#[derive(Debug)]
struct Domain;

impl std::fmt::Display for Domain {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str("conflict")
    }
}

impl std::error::Error for Domain {}

#[test]
fn sources_and_code_of() {
    let e = ConnectError::from_error(Code::FailedPrecondition, Domain);
    assert_eq!(e.message(), "conflict");
    assert!(e.source_as::<Domain>().is_some());
    assert_eq!(goap_connect::code_of(&e), Code::FailedPrecondition);
    assert_eq!(goap_connect::code_of(&Domain), Code::Unknown);
    assert_eq!(
        goap_connect::code_of(&goap_ctx_err()),
        Code::DeadlineExceeded
    );
    assert_eq!(
        ConnectError::from(goap_ctx_err()).code(),
        Code::DeadlineExceeded
    );
}

fn goap_ctx_err() -> goap_ctx::CtxError {
    goap_ctx::CtxError::DeadlineExceeded
}

/// The context of a handler ends when its client goes away.
#[tokio::test]
async fn the_context_ends_with_the_call() {
    let (tx, rx) = tokio::sync::oneshot::channel::<goap_ctx::CtxError>();
    let tx = Arc::new(Mutex::new(Some(tx)));
    let app = ServiceRouter::new(SVC, Interceptors::none())
        .unary("Wait", move |ctx: Ctx, _req: Request<Echo>| {
            let tx = tx.clone();
            async move {
                // the guard of the call cancels the context when the future is dropped
                struct Report(
                    Ctx,
                    Arc<Mutex<Option<tokio::sync::oneshot::Sender<goap_ctx::CtxError>>>>,
                );
                impl Drop for Report {
                    fn drop(&mut self) {
                        let tx = self.1.lock().unwrap().take();
                        let ctx = self.0.clone();
                        tokio::spawn(async move {
                            let e = ctx.done().await;
                            if let Some(tx) = tx {
                                let _ = tx.send(e);
                            }
                        });
                    }
                }
                let _r = Report(ctx.clone(), tx);
                ctx.done().await;
                Ok(Response::new(Echo::default()))
            }
        })
        .build();
    let ln = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let addr = ln.local_addr().unwrap();
    tokio::spawn(async move { axum::serve(ln, app).await.unwrap() });
    let r = reqwest::Client::new()
        .post(format!("http://{addr}/{SVC}/Wait"))
        .header("content-type", "application/json")
        .body("{}")
        .timeout(Duration::from_millis(100))
        .send()
        .await;
    assert!(r.is_err());
    let e = tokio::time::timeout(Duration::from_secs(5), rx)
        .await
        .expect("the handler's context ended")
        .unwrap();
    assert_eq!(e, goap_ctx::CtxError::Canceled);
}
