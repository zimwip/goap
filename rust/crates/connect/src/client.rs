//! The client side: calls over `reqwest`.
use std::{fmt, sync::Arc, time::Instant};

use futures::StreamExt;
use goap_ctx::Ctx;
use http::{HeaderMap, HeaderName, HeaderValue, StatusCode, header::CONTENT_TYPE};

use crate::{
    error::ConnectError,
    interceptor::{BoxFuture, Interceptor, Interceptors},
    message::{Codec, Message},
    protocol::{
        EnvelopeReader, FLAG_COMPRESSED, FLAG_END_STREAM, HEADER_PROTOCOL_VERSION, HEADER_TIMEOUT, TRAILER_PREFIX,
        envelope, read_end_stream, read_unary_error, timeout_value,
    },
    request::{AnyRequest, AnyResponse, Request, Response, ServerStream, Spec, StreamType},
};

/// A Connect client of one base URL (connect-go's `connect.Client`, shared by the methods of a generated client).
///
/// Cheap to clone. Each call sends the client's headers, then the request's own (which win), through the client's
/// interceptors (the first outermost, [`Spec::is_client`] true). The deadline of the call's context is sent as
/// `Connect-Timeout-Ms` and bounds the HTTP exchange; a context cancelled while the call is in flight abandons it
/// with `canceled`. Transport failures are `unavailable`, an elapsed deadline `deadline_exceeded`.
#[derive(Clone)]
pub struct Client {
    inner: Arc<Inner>,
}

struct Inner {
    http: reqwest::Client,
    base: String,
    codec: Codec,
    headers: HeaderMap,
    interceptors: Interceptors,
}

impl fmt::Debug for Client {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Client")
            .field("base", &self.inner.base)
            .field("codec", &self.inner.codec)
            .finish()
    }
}

/// Builds a [`Client`].
pub struct ClientBuilder {
    http: Option<reqwest::Client>,
    base: String,
    codec: Codec,
    headers: HeaderMap,
    interceptors: Interceptors,
}

impl ClientBuilder {
    /// The HTTP client to call with (default: a plain `reqwest::Client`; `goap_platform::h2c_client()` between
    /// services).
    pub fn http(mut self, c: reqwest::Client) -> Self {
        self.http = Some(c);
        self
    }

    /// Speak the protobuf JSON form (the binary form is the default).
    pub fn json(mut self) -> Self {
        self.codec = Codec::Json;
        self
    }

    pub fn codec(mut self, c: Codec) -> Self {
        self.codec = c;
        self
    }

    /// A header sent with every call; an invalid name or value is ignored.
    pub fn header(mut self, name: &str, value: &str) -> Self {
        if let (Ok(n), Ok(v)) = (HeaderName::try_from(name), HeaderValue::try_from(value)) {
            self.headers.insert(n, v);
        }
        self
    }

    /// Adds an interceptor, innermost.
    pub fn interceptor(mut self, i: impl Interceptor) -> Self {
        self.interceptors = self.interceptors.with(i);
        self
    }

    /// Adds interceptors, innermost.
    pub fn interceptors(mut self, i: &Interceptors) -> Self {
        self.interceptors = self.interceptors.chain(i);
        self
    }

    pub fn build(self) -> Client {
        Client {
            inner: Arc::new(Inner {
                http: self.http.unwrap_or_default(),
                base: self.base,
                codec: self.codec,
                headers: self.headers,
                interceptors: self.interceptors,
            }),
        }
    }
}

impl Client {
    /// A client of `base_url` (`http://graph:8080`) with the default settings.
    pub fn new(base_url: &str) -> Self {
        Self::builder(base_url).build()
    }

    pub fn builder(base_url: &str) -> ClientBuilder {
        ClientBuilder {
            http: None,
            base: base_url.trim_end_matches('/').to_string(),
            codec: Codec::default(),
            headers: HeaderMap::new(),
            interceptors: Interceptors::none(),
        }
    }

    pub fn base_url(&self) -> &str {
        &self.inner.base
    }

    pub fn codec(&self) -> Codec {
        self.inner.codec
    }

    /// Calls a unary procedure (`/goap.graph.v1.GraphService/GetNode`).
    pub async fn unary<Req: Message, Res: Message>(
        &self,
        ctx: &Ctx,
        procedure: &str,
        req: Request<Req>,
    ) -> Result<Response<Res>, ConnectError> {
        let any = AnyRequest {
            spec: Spec {
                procedure: procedure.to_string(),
                is_client: true,
                stream_type: StreamType::Unary,
            },
            headers: req.headers,
            msg: Box::new(req.msg),
        };
        let inner = self.inner.clone();
        let end = move |ctx: Ctx, req: AnyRequest| -> BoxFuture<'static, Result<AnyResponse, ConnectError>> {
            let inner = inner.clone();
            Box::pin(async move { inner.send_unary::<Res>(ctx, req).await })
        };
        self.inner
            .interceptors
            .run_unary(ctx.clone(), any, &end)
            .await?
            .into_typed()
    }

    /// Calls a server-streaming procedure: the response carries the headers and the stream of messages, which ends
    /// after the last one or with the error the server ended it with.
    pub async fn server_stream<Req: Message, Res: Message>(
        &self,
        ctx: &Ctx,
        procedure: &str,
        req: Request<Req>,
    ) -> Result<Response<ServerStream<Res>>, ConnectError> {
        let mut any = AnyRequest {
            spec: Spec {
                procedure: procedure.to_string(),
                is_client: true,
                stream_type: StreamType::ServerStream,
            },
            headers: req.headers,
            msg: Box::new(req.msg),
        };
        let ctx = self.inner.interceptors.run_stream(ctx.clone(), &mut any).await?;
        self.inner.send_stream::<Res>(ctx, any).await
    }
}

fn transport_error(e: reqwest::Error) -> ConnectError {
    if e.is_timeout() {
        ConnectError::deadline_exceeded(e.to_string())
    } else {
        ConnectError::unavailable(e.to_string())
    }
}

impl Inner {
    fn request(
        &self,
        ctx: &Ctx,
        req: &AnyRequest,
        content_type: &'static str,
        body: Vec<u8>,
    ) -> Result<reqwest::RequestBuilder, ConnectError> {
        if let Some(e) = ctx.err() {
            return Err(e.into());
        }
        let mut h = self.headers.clone();
        for (k, v) in &req.headers {
            if !is_protocol_header(k.as_str()) {
                h.insert(k.clone(), v.clone());
            }
        }
        h.insert(CONTENT_TYPE, HeaderValue::from_static(content_type));
        h.insert(
            HeaderName::from_static(HEADER_PROTOCOL_VERSION),
            HeaderValue::from_static("1"),
        );
        let mut b = self.http.post(format!("{}{}", self.base, req.spec.procedure));
        if let Some(d) = ctx.deadline() {
            let left = d.saturating_duration_since(Instant::now());
            if left.is_zero() {
                return Err(ConnectError::from(goap_ctx::CtxError::DeadlineExceeded));
            }
            h.insert(HeaderName::from_static(HEADER_TIMEOUT), timeout_value(left));
            b = b.timeout(left);
        }
        Ok(b.headers(h).body(body))
    }

    async fn send_unary<Res: Message>(&self, ctx: Ctx, req: AnyRequest) -> Result<AnyResponse, ConnectError> {
        let body = req.msg.encode_with(self.codec)?;
        let b = self.request(&ctx, &req, self.codec.unary_content_type(), body)?;
        let exchange = async {
            let resp = b.send().await.map_err(transport_error)?;
            let status = resp.status();
            let headers = resp.headers().clone();
            let body = resp.bytes().await.map_err(transport_error)?;
            Ok::<_, ConnectError>((status, headers, body))
        };
        let (status, headers, body) = tokio::select! {
            r = exchange => r?,
            e = ctx.done() => return Err(e.into()),
        };
        let (headers, trailers) = split_trailers(headers);
        if status != StatusCode::OK {
            let mut meta = headers;
            for (k, v) in &trailers {
                meta.append(k.clone(), v.clone());
            }
            return Err(read_unary_error(status, meta, &body));
        }
        let codec = response_codec(&headers, false).unwrap_or(self.codec);
        let msg: Res = codec
            .decode(&body)
            .map_err(|e| ConnectError::internal(e.message().to_string()))?;
        Ok(AnyResponse {
            headers,
            trailers,
            msg: Box::new(msg),
        })
    }

    async fn send_stream<Res: Message>(
        &self,
        ctx: Ctx,
        req: AnyRequest,
    ) -> Result<Response<ServerStream<Res>>, ConnectError> {
        let payload = req.msg.encode_with(self.codec)?;
        let b = self.request(
            &ctx,
            &req,
            self.codec.stream_content_type(),
            envelope(0, &payload).to_vec(),
        )?;
        let resp = tokio::select! {
            r = b.send() => r.map_err(transport_error)?,
            e = ctx.done() => return Err(e.into()),
        };
        let status = resp.status();
        let headers = resp.headers().clone();
        if status != StatusCode::OK {
            let body = resp.bytes().await.unwrap_or_default();
            return Err(read_unary_error(status, headers, &body));
        }
        let codec = response_codec(&headers, true).unwrap_or(self.codec);
        let state = ReadState {
            body: Some(resp.bytes_stream().boxed()),
            reader: EnvelopeReader::new(),
            codec,
            ctx,
        };
        let stream = futures::stream::unfold(state, |mut st| async move {
            loop {
                let body = st.body.as_mut()?;
                if let Some((flags, payload)) = st.reader.next_envelope() {
                    if flags & FLAG_COMPRESSED != 0 {
                        st.body = None;
                        return Some((
                            Err(ConnectError::internal(
                                "protocol error: compressed message not supported",
                            )),
                            st,
                        ));
                    }
                    if flags & FLAG_END_STREAM != 0 {
                        st.body = None;
                        return match read_end_stream(&payload) {
                            Ok((_, None)) => None,
                            Ok((_, Some(e))) | Err(e) => Some((Err(e), st)),
                        };
                    }
                    return match st.codec.decode::<Res>(&payload) {
                        Ok(m) => Some((Ok(m), st)),
                        Err(e) => {
                            st.body = None;
                            Some((Err(ConnectError::internal(e.message().to_string())), st))
                        }
                    };
                }
                let chunk = tokio::select! {
                    c = body.next() => c,
                    e = st.ctx.done() => {
                        st.body = None;
                        return Some((Err(e.into()), st));
                    }
                };
                match chunk {
                    Some(Ok(c)) => st.reader.push(&c),
                    Some(Err(e)) => {
                        st.body = None;
                        return Some((Err(transport_error(e)), st));
                    }
                    None => {
                        st.body = None;
                        return Some((Err(ConnectError::internal("protocol error: unexpected EOF")), st));
                    }
                }
            }
        });
        let (headers, _) = split_trailers(headers);
        Ok(Response {
            headers,
            trailers: HeaderMap::new(),
            msg: Box::pin(stream),
        })
    }
}

struct ReadState {
    body: Option<futures::stream::BoxStream<'static, Result<bytes::Bytes, reqwest::Error>>>,
    reader: EnvelopeReader,
    codec: Codec,
    ctx: Ctx,
}

/// The headers the protocol and the transport own: never copied from a request's headers (a handler forwarding the
/// request it serves forwards its own headers, not the length or the encoding of the body it received).
fn is_protocol_header(name: &str) -> bool {
    matches!(
        name,
        "host"
            | "content-length"
            | "content-type"
            | "content-encoding"
            | "accept-encoding"
            | "transfer-encoding"
            | "connection"
            | "keep-alive"
            | "te"
            | "trailer"
            | "upgrade"
            | "proxy-connection"
    ) || name.starts_with("connect-")
}

fn response_codec(h: &HeaderMap, stream: bool) -> Option<Codec> {
    let ct = h.get(CONTENT_TYPE).and_then(|v| v.to_str().ok()).unwrap_or("");
    if stream {
        Codec::from_stream_content_type(ct)
    } else {
        Codec::from_unary_content_type(ct)
    }
}

/// Splits the `Trailer-` headers of a unary response from the others.
fn split_trailers(h: HeaderMap) -> (HeaderMap, HeaderMap) {
    let mut headers = HeaderMap::new();
    let mut trailers = HeaderMap::new();
    let mut last: Option<HeaderName> = None;
    for (k, v) in h {
        if let Some(k) = k {
            last = Some(k);
        }
        let Some(k) = last.clone() else { continue };
        match k
            .as_str()
            .strip_prefix(TRAILER_PREFIX)
            .and_then(|n| HeaderName::try_from(n).ok())
        {
            Some(t) => {
                trailers.append(t, v);
            }
            None => {
                headers.append(k, v);
            }
        }
    }
    (headers, trailers)
}
