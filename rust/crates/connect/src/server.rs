//! The server side: the routes of a service, as an `axum::Router` to mount on the server.
use std::{future::Future, sync::Arc};

use axum::{
    Router,
    body::Body,
    extract::DefaultBodyLimit,
    response::{IntoResponse, Response as HttpResponse},
    routing::post,
};
use bytes::Bytes;
use futures::StreamExt;
use goap_ctx::{CancelHandle, Ctx};
use http::{HeaderMap, HeaderName, HeaderValue, StatusCode, header::CONTENT_TYPE};

use crate::{
    error::ConnectError,
    interceptor::{BoxFuture, Interceptors},
    message::{Codec, Message},
    protocol::{
        COMPRESS_MIN_BYTES, EnvelopeReader, FLAG_COMPRESSED, GZIP, HEADER_ACCEPT_ENCODING, HEADER_CONTENT_ENCODING,
        HEADER_STREAM_ACCEPT_ENCODING, HEADER_STREAM_CONTENT_ENCODING, TRAILER_PREFIX, accepts_gzip, check_encoding,
        check_protocol_version, end_stream, envelope, gunzip, gzip, parse_timeout,
    },
    request::{AnyRequest, AnyResponse, Request, Response, ServerStream, Spec, StreamType},
};

/// Builds the routes of one service: `POST /<service>/<Method>` for each method. The generated
/// `<service>_router` functions use it; a hand-written service can too.
///
/// Unary methods accept `application/json` and `application/proto` and answer in the codec of the request; server
/// streams accept `application/connect+json` and `application/connect+proto`. Another content type is refused with
/// 415, another HTTP method with 405, an unknown procedure is axum's 404 (the web reads it as `unimplemented`). The
/// size of a request is not limited (connect-go's default). `Connect-Timeout-Ms` becomes the deadline of the
/// handler's context, which is also cancelled when the client goes away (the handler's future is dropped) or the call
/// ends.
pub struct ServiceRouter {
    service: String,
    interceptors: Interceptors,
    router: Router,
}

impl ServiceRouter {
    /// The routes of `service` (`goap.graph.v1.GraphService`), every call going through `interceptors`.
    pub fn new(service: &str, interceptors: Interceptors) -> Self {
        ServiceRouter {
            service: service.to_string(),
            interceptors,
            router: Router::new(),
        }
    }

    /// The procedure of a method: `/<service>/<method>`.
    pub fn procedure(&self, method: &str) -> String {
        format!("/{}/{}", self.service, method)
    }

    /// Adds a unary method.
    pub fn unary<Req, Res, F, Fut>(mut self, method: &str, f: F) -> Self
    where
        Req: Message,
        Res: Message,
        F: Fn(Ctx, Request<Req>) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = Result<Response<Res>, ConnectError>> + Send + 'static,
    {
        let procedure = self.procedure(method);
        let h = Arc::new(f);
        let ics = self.interceptors.clone();
        let p = procedure.clone();
        let route = post(move |headers: HeaderMap, body: Bytes| {
            let (h, ics, p) = (h.clone(), ics.clone(), p.clone());
            async move { serve_unary::<Req, Res, F, Fut>(&h, &ics, p, headers, body).await }
        });
        self.router = self.router.route(&procedure, route);
        self
    }

    /// Adds a server-streaming method.
    pub fn server_stream<Req, Res, F, Fut>(mut self, method: &str, f: F) -> Self
    where
        Req: Message,
        Res: Message,
        F: Fn(Ctx, Request<Req>) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = Result<Response<ServerStream<Res>>, ConnectError>> + Send + 'static,
    {
        let procedure = self.procedure(method);
        let h = Arc::new(f);
        let ics = self.interceptors.clone();
        let p = procedure.clone();
        let route = post(move |headers: HeaderMap, body: Bytes| {
            let (h, ics, p) = (h.clone(), ics.clone(), p.clone());
            async move { serve_stream::<Req, Res, F, Fut>(&h, &ics, p, headers, body).await }
        });
        self.router = self.router.route(&procedure, route);
        self
    }

    /// The router of the service.
    pub fn build(self) -> Router {
        self.router.layer(DefaultBodyLimit::disable())
    }
}

/// Cancels the context of a call when the call is over or dropped.
struct CancelOnDrop(CancelHandle);

impl Drop for CancelOnDrop {
    fn drop(&mut self) {
        self.0.cancel();
    }
}

/// The context of a call: background, with the deadline the client gave.
fn call_ctx(headers: &HeaderMap) -> Result<(Ctx, CancelOnDrop), ConnectError> {
    let timeout = parse_timeout(headers)?;
    let (ctx, cancel) = match timeout {
        Some(d) => Ctx::background().with_timeout(d),
        None => Ctx::background().with_cancel(),
    };
    Ok((ctx, CancelOnDrop(cancel)))
}

fn content_type(headers: &HeaderMap) -> &str {
    headers.get(CONTENT_TYPE).and_then(|v| v.to_str().ok()).unwrap_or("")
}

const ACCEPT_UNARY: &str = "application/json, application/proto";
const ACCEPT_STREAM: &str = "application/connect+json, application/connect+proto";

fn unsupported_media(accept: &'static str) -> HttpResponse {
    let mut r = StatusCode::UNSUPPORTED_MEDIA_TYPE.into_response();
    r.headers_mut()
        .insert(HeaderName::from_static("accept-post"), HeaderValue::from_static(accept));
    r
}

/// The HTTP answer of a unary error: the status of its code, its metadata as headers, the JSON body.
pub fn error_response(err: &ConnectError) -> HttpResponse {
    let mut r = HttpResponse::new(Body::from(err.to_json()));
    *r.status_mut() = err.code().http_status();
    let h = r.headers_mut();
    for (k, v) in err.meta() {
        h.append(k.clone(), v.clone());
    }
    h.insert(CONTENT_TYPE, HeaderValue::from_static("application/json"));
    r
}

async fn serve_unary<Req, Res, F, Fut>(
    h: &Arc<F>,
    ics: &Interceptors,
    procedure: String,
    headers: HeaderMap,
    body: Bytes,
) -> HttpResponse
where
    Req: Message,
    Res: Message,
    F: Fn(Ctx, Request<Req>) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<Response<Res>, ConnectError>> + Send + 'static,
{
    let Some(codec) = Codec::from_unary_content_type(content_type(&headers)) else {
        return unsupported_media(ACCEPT_UNARY);
    };
    let compressed = match check_encoding(&headers, HEADER_CONTENT_ENCODING) {
        Ok(c) => c,
        Err(e) => return accepting(error_response(&e), HEADER_ACCEPT_ENCODING),
    };
    if let Err(e) = check_protocol_version(&headers) {
        return accepting(error_response(&e), HEADER_ACCEPT_ENCODING);
    }
    let (ctx, _guard) = match call_ctx(&headers) {
        Ok(c) => c,
        Err(e) => return accepting(error_response(&e), HEADER_ACCEPT_ENCODING),
    };
    // connect-go answers in the compression of the request, else the first the client accepts
    let gzipped = compressed || accepts_gzip(&headers, HEADER_ACCEPT_ENCODING);
    let body = if compressed {
        match gunzip(&body) {
            Ok(b) => b,
            Err(e) => return accepting(error_response(&e), HEADER_ACCEPT_ENCODING),
        }
    } else {
        body
    };
    let msg: Req = match codec.decode(&body) {
        Ok(m) => m,
        Err(e) => return accepting(error_response(&e), HEADER_ACCEPT_ENCODING),
    };
    let req = AnyRequest {
        spec: Spec {
            procedure,
            is_client: false,
            stream_type: StreamType::Unary,
        },
        headers,
        msg: Box::new(msg),
    };
    let h = h.clone();
    let end = move |ctx: Ctx, req: AnyRequest| -> BoxFuture<'static, Result<AnyResponse, ConnectError>> {
        let h = h.clone();
        Box::pin(async move {
            let r = req.into_typed::<Req>()?;
            let resp = h(ctx, r).await?;
            Ok(AnyResponse::from_typed(resp))
        })
    };
    let answer = match ics.run_unary(ctx, req, &end).await {
        Ok(resp) => match resp.msg.encode_with(codec) {
            Ok(b) => {
                let zip = gzipped && b.len() >= COMPRESS_MIN_BYTES;
                let mut r = HttpResponse::new(Body::from(if zip { gzip(&b) } else { Bytes::from(b) }));
                let hs = r.headers_mut();
                if zip {
                    hs.insert(HEADER_CONTENT_ENCODING, HeaderValue::from_static(GZIP));
                }
                for (k, v) in &resp.headers {
                    hs.append(k.clone(), v.clone());
                }
                for (k, v) in &resp.trailers {
                    if let Ok(n) = HeaderName::try_from(format!("{TRAILER_PREFIX}{}", k.as_str())) {
                        hs.append(n, v.clone());
                    }
                }
                hs.insert(CONTENT_TYPE, HeaderValue::from_static(codec.unary_content_type()));
                r
            }
            Err(e) => error_response(&e),
        },
        Err(e) => error_response(&e),
    };
    accepting(answer, HEADER_ACCEPT_ENCODING)
}

/// An answer telling the compressions the server reads, as connect-go's does on every answer.
fn accepting(mut r: HttpResponse, header: &'static str) -> HttpResponse {
    r.headers_mut()
        .insert(HeaderName::from_static(header), HeaderValue::from_static(GZIP));
    r
}

fn stream_response(codec: Codec, headers: &HeaderMap, body: Body, gzipped: bool) -> HttpResponse {
    let mut r = HttpResponse::new(body);
    let hs = r.headers_mut();
    for (k, v) in headers {
        hs.append(k.clone(), v.clone());
    }
    hs.insert(CONTENT_TYPE, HeaderValue::from_static(codec.stream_content_type()));
    if gzipped {
        hs.insert(HEADER_STREAM_CONTENT_ENCODING, HeaderValue::from_static(GZIP));
    }
    accepting(r, HEADER_STREAM_ACCEPT_ENCODING)
}

/// A stream that fails before its first message: a 200 whose body is the end-of-stream message.
fn stream_error(codec: Codec, err: &ConnectError) -> HttpResponse {
    stream_response(
        codec,
        &HeaderMap::new(),
        Body::from(end_stream(Some(err), &HeaderMap::new())),
        false,
    )
}

fn read_request_envelope(body: &Bytes, compressed: bool) -> Result<Bytes, ConnectError> {
    let mut rd = EnvelopeReader::new();
    rd.push(body);
    let Some((flags, payload)) = rd.next_envelope() else {
        return Err(ConnectError::invalid_argument("protocol error: incomplete envelope"));
    };
    if rd.remaining() > 0 || rd.next_envelope().is_some() {
        return Err(ConnectError::unimplemented("unary request has multiple messages"));
    }
    if flags & FLAG_COMPRESSED != 0 {
        if !compressed {
            return Err(ConnectError::internal(
                "protocol error: received compressed message without a compression algorithm",
            ));
        }
        return gunzip(&payload);
    }
    Ok(payload)
}

struct StreamState<Res> {
    inner: Option<ServerStream<Res>>,
    ctx: Ctx,
    codec: Codec,
    gzipped: bool,
    trailers: HeaderMap,
    _guard: CancelOnDrop,
}

async fn serve_stream<Req, Res, F, Fut>(
    h: &Arc<F>,
    ics: &Interceptors,
    procedure: String,
    headers: HeaderMap,
    body: Bytes,
) -> HttpResponse
where
    Req: Message,
    Res: Message,
    F: Fn(Ctx, Request<Req>) -> Fut + Send + Sync + 'static,
    Fut: Future<Output = Result<Response<ServerStream<Res>>, ConnectError>> + Send + 'static,
{
    let Some(codec) = Codec::from_stream_content_type(content_type(&headers)) else {
        return unsupported_media(ACCEPT_STREAM);
    };
    let compressed = match check_encoding(&headers, HEADER_STREAM_CONTENT_ENCODING) {
        Ok(c) => c,
        Err(e) => return stream_error(codec, &e),
    };
    if let Err(e) = check_protocol_version(&headers) {
        return stream_error(codec, &e);
    }
    let gzipped = compressed || accepts_gzip(&headers, HEADER_STREAM_ACCEPT_ENCODING);
    let (ctx, guard) = match call_ctx(&headers) {
        Ok(c) => c,
        Err(e) => return stream_error(codec, &e),
    };
    let msg: Req = match read_request_envelope(&body, compressed).and_then(|p| codec.decode(&p)) {
        Ok(m) => m,
        Err(e) => return stream_error(codec, &e),
    };
    let mut req = AnyRequest {
        spec: Spec {
            procedure,
            is_client: false,
            stream_type: StreamType::ServerStream,
        },
        headers,
        msg: Box::new(msg),
    };
    let ctx = match ics.run_stream(ctx, &mut req).await {
        Ok(c) => c,
        Err(e) => return stream_error(codec, &e),
    };
    let typed = match req.into_typed::<Req>() {
        Ok(t) => t,
        Err(e) => return stream_error(codec, &e),
    };
    let resp = match h(ctx.clone(), typed).await {
        Ok(r) => r,
        Err(e) => return stream_error(codec, &e),
    };
    let state = StreamState {
        inner: Some(resp.msg),
        ctx,
        codec,
        gzipped,
        trailers: resp.trailers,
        _guard: guard,
    };
    let frames = futures::stream::unfold(state, |mut st| async move {
        let mut inner = st.inner.take()?;
        let item = tokio::select! {
            item = inner.next() => item,
            e = st.ctx.done() => Some(Err(ConnectError::from(e))),
        };
        let frame = match item {
            Some(Ok(m)) => match st.codec.encode(&m) {
                Ok(b) => {
                    st.inner = Some(inner);
                    if st.gzipped && b.len() >= COMPRESS_MIN_BYTES {
                        envelope(FLAG_COMPRESSED, &gzip(&b))
                    } else {
                        envelope(0, &b)
                    }
                }
                Err(e) => end_stream(Some(&e), &st.trailers),
            },
            Some(Err(e)) => end_stream(Some(&e), &st.trailers),
            None => end_stream(None, &st.trailers),
        };
        Some((Ok::<Bytes, std::convert::Infallible>(frame), st))
    });
    stream_response(codec, &resp.headers, Body::from_stream(frames), gzipped)
}
