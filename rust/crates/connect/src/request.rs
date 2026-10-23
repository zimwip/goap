//! Requests and responses: a message with its headers, typed for handlers and clients, erased for interceptors.
use std::{fmt, pin::Pin};

use futures::Stream;
use http::HeaderMap;

use crate::{
    error::ConnectError,
    message::{AnyMessage, Message},
};

/// The kind of a procedure.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Default)]
pub enum StreamType {
    #[default]
    Unary,
    /// One request, a stream of responses.
    ServerStream,
}

/// What is being called (connect-go's `connect.Spec`).
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Spec {
    /// `/goap.graph.v1.GraphService/GetNode`.
    pub procedure: String,
    /// Whether the call is seen from the client side (an interceptor of a client) or the server side.
    pub is_client: bool,
    pub stream_type: StreamType,
}

impl Spec {
    /// The service of the procedure: `goap.graph.v1.GraphService`.
    pub fn service(&self) -> &str {
        self.procedure.trim_start_matches('/').split('/').next().unwrap_or("")
    }

    /// The method of the procedure: `GetNode`.
    pub fn method(&self) -> &str {
        self.procedure.rsplit('/').next().unwrap_or("")
    }
}

/// A request: the message and the headers (`connect.Request[T]`). On the server side `headers` are every header of
/// the HTTP request; on the client side the headers to send with this call (added to the client's own).
#[derive(Debug, Clone, Default)]
pub struct Request<T> {
    pub headers: HeaderMap,
    pub msg: T,
    pub(crate) spec: Spec,
}

impl<T> Request<T> {
    /// A request of `msg` with no header.
    pub fn new(msg: T) -> Self {
        Request {
            headers: HeaderMap::new(),
            msg,
            spec: Spec::default(),
        }
    }

    /// A request of `msg` with these headers.
    pub fn with_headers(msg: T, headers: HeaderMap) -> Self {
        Request {
            headers,
            msg,
            spec: Spec::default(),
        }
    }

    /// The procedure being served (empty on a request a client has not sent yet).
    pub fn spec(&self) -> &Spec {
        &self.spec
    }

    /// The message.
    pub fn into_inner(self) -> T {
        self.msg
    }

    /// A header as text (`req.Header().Get(name)`): empty when absent or not text.
    pub fn header(&self, name: &str) -> &str {
        header_str(&self.headers, name)
    }
}

impl<T> From<T> for Request<T> {
    fn from(msg: T) -> Self {
        Request::new(msg)
    }
}

/// A response: the message, its headers and trailers (`connect.Response[T]`). The trailers of a unary call travel as
/// `Trailer-`-prefixed headers, those of a stream in its end-of-stream message.
#[derive(Debug, Clone, Default)]
pub struct Response<T> {
    pub headers: HeaderMap,
    pub trailers: HeaderMap,
    pub msg: T,
}

impl<T> Response<T> {
    /// `connect.NewResponse(msg)`.
    pub fn new(msg: T) -> Self {
        Response {
            headers: HeaderMap::new(),
            trailers: HeaderMap::new(),
            msg,
        }
    }

    pub fn into_inner(self) -> T {
        self.msg
    }

    /// A header as text: empty when absent or not text.
    pub fn header(&self, name: &str) -> &str {
        header_str(&self.headers, name)
    }
}

/// The messages of a server stream: what a server-streaming handler returns (in a [`Response`] carrying the response
/// headers and the trailers sent at the end) and what a client reads. An `Err` ends the stream with that error; a
/// client sees the stream end without error after the last message.
pub type ServerStream<T> = Pin<Box<dyn Stream<Item = Result<T, ConnectError>> + Send + 'static>>;

/// Boxes a stream as a [`ServerStream`].
pub fn server_stream<T, S>(s: S) -> ServerStream<T>
where
    S: Stream<Item = Result<T, ConnectError>> + Send + 'static,
{
    Box::pin(s)
}

/// A request as interceptors see it (connect-go's `connect.AnyRequest`).
pub struct AnyRequest {
    pub spec: Spec,
    pub headers: HeaderMap,
    pub msg: Box<dyn AnyMessage>,
}

impl AnyRequest {
    pub(crate) fn into_typed<T: Message>(self) -> Result<Request<T>, ConnectError> {
        let spec = self.spec;
        Ok(Request {
            headers: self.headers,
            msg: self.msg.downcast::<T>()?,
            spec,
        })
    }

    /// The message as a `T`.
    pub fn message<T: 'static>(&self) -> Option<&T> {
        self.msg.downcast_ref()
    }

    /// The protobuf JSON form of the message.
    pub fn message_json(&self) -> serde_json::Value {
        self.msg.to_json()
    }

    /// A header as text: empty when absent or not text.
    pub fn header(&self, name: &str) -> &str {
        header_str(&self.headers, name)
    }
}

impl fmt::Debug for AnyRequest {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("AnyRequest")
            .field("spec", &self.spec)
            .field("headers", &self.headers)
            .field("msg", &self.msg)
            .finish()
    }
}

/// A unary response as interceptors see it (connect-go's `connect.AnyResponse`).
pub struct AnyResponse {
    pub headers: HeaderMap,
    pub trailers: HeaderMap,
    pub msg: Box<dyn AnyMessage>,
}

impl AnyResponse {
    pub(crate) fn from_typed<T: Message>(r: Response<T>) -> Self {
        AnyResponse {
            headers: r.headers,
            trailers: r.trailers,
            msg: Box::new(r.msg),
        }
    }

    pub(crate) fn into_typed<T: Message>(self) -> Result<Response<T>, ConnectError> {
        Ok(Response {
            headers: self.headers,
            trailers: self.trailers,
            msg: self.msg.downcast::<T>()?,
        })
    }

    pub fn message<T: 'static>(&self) -> Option<&T> {
        self.msg.downcast_ref()
    }

    pub fn message_json(&self) -> serde_json::Value {
        self.msg.to_json()
    }
}

impl fmt::Debug for AnyResponse {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("AnyResponse")
            .field("headers", &self.headers)
            .field("trailers", &self.trailers)
            .field("msg", &self.msg)
            .finish()
    }
}

fn header_str<'a>(h: &'a HeaderMap, name: &str) -> &'a str {
    h.get(name).and_then(|v| v.to_str().ok()).unwrap_or("")
}
