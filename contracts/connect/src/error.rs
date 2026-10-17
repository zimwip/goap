//! The Connect error: a code, a message, metadata headers and details (connect-go's `*connect.Error`).
use std::{error::Error as StdError, fmt, str::FromStr, sync::Arc};

use base64::Engine as _;
use http::{HeaderMap, HeaderName, HeaderValue, StatusCode};
use serde::{Deserialize, Serialize};

use goap_ctx::CtxError;

/// The Connect error codes (the gRPC codes, spelled in snake case on the wire).
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash)]
pub enum Code {
    Canceled,
    Unknown,
    InvalidArgument,
    DeadlineExceeded,
    NotFound,
    AlreadyExists,
    PermissionDenied,
    ResourceExhausted,
    FailedPrecondition,
    Aborted,
    OutOfRange,
    Unimplemented,
    Internal,
    Unavailable,
    DataLoss,
    Unauthenticated,
}

impl Code {
    /// Every code, in the order of their gRPC numbers (1 to 16).
    pub const ALL: [Code; 16] = [
        Code::Canceled,
        Code::Unknown,
        Code::InvalidArgument,
        Code::DeadlineExceeded,
        Code::NotFound,
        Code::AlreadyExists,
        Code::PermissionDenied,
        Code::ResourceExhausted,
        Code::FailedPrecondition,
        Code::Aborted,
        Code::OutOfRange,
        Code::Unimplemented,
        Code::Internal,
        Code::Unavailable,
        Code::DataLoss,
        Code::Unauthenticated,
    ];

    /// The name of the code on the wire (`invalid_argument`).
    pub fn as_str(self) -> &'static str {
        match self {
            Code::Canceled => "canceled",
            Code::Unknown => "unknown",
            Code::InvalidArgument => "invalid_argument",
            Code::DeadlineExceeded => "deadline_exceeded",
            Code::NotFound => "not_found",
            Code::AlreadyExists => "already_exists",
            Code::PermissionDenied => "permission_denied",
            Code::ResourceExhausted => "resource_exhausted",
            Code::FailedPrecondition => "failed_precondition",
            Code::Aborted => "aborted",
            Code::OutOfRange => "out_of_range",
            Code::Unimplemented => "unimplemented",
            Code::Internal => "internal",
            Code::Unavailable => "unavailable",
            Code::DataLoss => "data_loss",
            Code::Unauthenticated => "unauthenticated",
        }
    }

    /// The gRPC number of the code (1 to 16).
    pub fn number(self) -> u32 {
        Code::ALL
            .iter()
            .position(|c| *c == self)
            .map_or(2, |i| i as u32 + 1)
    }

    /// The HTTP status a unary error of this code is answered with (the Connect specification).
    pub fn http_status(self) -> StatusCode {
        let n = match self {
            Code::Canceled => 499,
            Code::Unknown => 500,
            Code::InvalidArgument => 400,
            Code::DeadlineExceeded => 504,
            Code::NotFound => 404,
            Code::AlreadyExists => 409,
            Code::PermissionDenied => 403,
            Code::ResourceExhausted => 429,
            Code::FailedPrecondition => 400,
            Code::Aborted => 409,
            Code::OutOfRange => 400,
            Code::Unimplemented => 501,
            Code::Internal => 500,
            Code::Unavailable => 503,
            Code::DataLoss => 500,
            Code::Unauthenticated => 401,
        };
        StatusCode::from_u16(n).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR)
    }

    /// The code a client infers from the HTTP status of an answer that carries no Connect error (connect-go's
    /// `httpToCode`: the gRPC mapping, not the inverse of [`Code::http_status`]).
    pub fn from_http_status(status: StatusCode) -> Code {
        match status.as_u16() {
            400 => Code::Internal,
            401 => Code::Unauthenticated,
            403 => Code::PermissionDenied,
            404 => Code::Unimplemented,
            429 | 502 | 503 | 504 => Code::Unavailable,
            _ => Code::Unknown,
        }
    }
}

impl fmt::Display for Code {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// The name is not a Connect code.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct UnknownCode(pub String);

impl fmt::Display for UnknownCode {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "unknown connect code {:?}", self.0)
    }
}

impl StdError for UnknownCode {}

impl FromStr for Code {
    type Err = UnknownCode;

    fn from_str(s: &str) -> Result<Self, Self::Err> {
        Code::ALL
            .iter()
            .copied()
            .find(|c| c.as_str() == s)
            .ok_or_else(|| UnknownCode(s.to_string()))
    }
}

impl Serialize for Code {
    fn serialize<S: serde::Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(self.as_str())
    }
}

impl<'de> Deserialize<'de> for Code {
    /// An unknown name reads as `unknown`, as connect-go does.
    fn deserialize<D: serde::Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        let s = String::deserialize(d)?;
        Ok(s.parse().unwrap_or(Code::Unknown))
    }
}

/// A detail of an error: a protobuf `Any` (its fully-qualified type name and its encoded value), plus the JSON form
/// of the message when the sender had it (`debug`).
#[derive(Debug, Clone, PartialEq)]
pub struct ErrorDetail {
    /// The fully-qualified name of the message type (`google.rpc.RetryInfo`), without the `type.googleapis.com/`.
    pub type_name: String,
    /// The binary encoding of the message.
    pub value: Vec<u8>,
    /// The JSON form of the message, informative only.
    pub debug: Option<serde_json::Value>,
}

impl ErrorDetail {
    /// The detail holding `msg`, named `type_name`.
    pub fn new<M: prost::Message + serde::Serialize>(type_name: &str, msg: &M) -> Self {
        ErrorDetail {
            type_name: type_name
                .trim_start_matches("type.googleapis.com/")
                .to_string(),
            value: msg.encode_to_vec(),
            debug: serde_json::to_value(msg).ok(),
        }
    }

    /// Decodes the value as `M`.
    pub fn decode<M: prost::Message + Default>(&self) -> Result<M, prost::DecodeError> {
        M::decode(self.value.as_slice())
    }
}

/// The wire form of an error: the body of a unary error and the `error` of an end-of-stream message.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub(crate) struct WireError {
    #[serde(default)]
    pub code: Option<Code>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub message: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub details: Vec<WireDetail>,
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub(crate) struct WireDetail {
    #[serde(rename = "type", default)]
    pub type_name: String,
    #[serde(default)]
    pub value: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub debug: Option<serde_json::Value>,
}

/// An RPC error (connect-go's `*connect.Error`): what a handler returns and what a client gets.
///
/// Its `Display` is `<code>: <message>` (only the code when the message is empty), as in Go. The metadata
/// ([`ConnectError::meta`]) travels as response headers of a unary call and as the trailers of a stream: the
/// `Goap-Error-Reason` header of `rpcerr` rides there.
///
/// It is one pointer wide (its content is boxed), so that every `Result<_, ConnectError>` stays small.
#[derive(Clone)]
pub struct ConnectError(Box<Inner>);

#[derive(Clone)]
struct Inner {
    code: Code,
    message: String,
    meta: HeaderMap,
    details: Vec<ErrorDetail>,
    wire: bool,
    source: Option<Arc<dyn StdError + Send + Sync>>,
}

impl ConnectError {
    /// An error of `code` saying `message`.
    pub fn new(code: Code, message: impl Into<String>) -> Self {
        ConnectError(Box::new(Inner {
            code,
            message: message.into(),
            meta: HeaderMap::new(),
            details: Vec::new(),
            wire: false,
            source: None,
        }))
    }

    /// `connect.NewError(code, err)`: the message is `err`'s text and `err` is kept as the source (so
    /// [`ConnectError::source_as`] finds the domain error behind it).
    pub fn from_error<E: StdError + Send + Sync + 'static>(code: Code, err: E) -> Self {
        let mut e = ConnectError::new(code, err.to_string());
        e.0.source = Some(Arc::new(err));
        e
    }

    /// `connect.NewError(code, err)` for an error already boxed.
    pub fn from_boxed(code: Code, err: Box<dyn StdError + Send + Sync + 'static>) -> Self {
        let mut e = ConnectError::new(code, err.to_string());
        e.0.source = Some(Arc::from(err));
        e
    }

    /// The error of a method the service does not implement, worded as connect-go's generated handlers word it:
    /// `goap.graph.v1.GraphService.GetNode is not implemented`.
    pub fn unimplemented_procedure(procedure: &str) -> Self {
        let p = procedure.trim_start_matches('/').replace('/', ".");
        ConnectError::new(Code::Unimplemented, format!("{p} is not implemented"))
    }

    pub fn canceled(m: impl Into<String>) -> Self {
        Self::new(Code::Canceled, m)
    }
    pub fn unknown(m: impl Into<String>) -> Self {
        Self::new(Code::Unknown, m)
    }
    pub fn invalid_argument(m: impl Into<String>) -> Self {
        Self::new(Code::InvalidArgument, m)
    }
    pub fn deadline_exceeded(m: impl Into<String>) -> Self {
        Self::new(Code::DeadlineExceeded, m)
    }
    pub fn not_found(m: impl Into<String>) -> Self {
        Self::new(Code::NotFound, m)
    }
    pub fn already_exists(m: impl Into<String>) -> Self {
        Self::new(Code::AlreadyExists, m)
    }
    pub fn permission_denied(m: impl Into<String>) -> Self {
        Self::new(Code::PermissionDenied, m)
    }
    pub fn resource_exhausted(m: impl Into<String>) -> Self {
        Self::new(Code::ResourceExhausted, m)
    }
    pub fn failed_precondition(m: impl Into<String>) -> Self {
        Self::new(Code::FailedPrecondition, m)
    }
    pub fn aborted(m: impl Into<String>) -> Self {
        Self::new(Code::Aborted, m)
    }
    pub fn out_of_range(m: impl Into<String>) -> Self {
        Self::new(Code::OutOfRange, m)
    }
    pub fn unimplemented(m: impl Into<String>) -> Self {
        Self::new(Code::Unimplemented, m)
    }
    pub fn internal(m: impl Into<String>) -> Self {
        Self::new(Code::Internal, m)
    }
    pub fn unavailable(m: impl Into<String>) -> Self {
        Self::new(Code::Unavailable, m)
    }
    pub fn data_loss(m: impl Into<String>) -> Self {
        Self::new(Code::DataLoss, m)
    }
    pub fn unauthenticated(m: impl Into<String>) -> Self {
        Self::new(Code::Unauthenticated, m)
    }

    pub fn code(&self) -> Code {
        self.0.code
    }

    /// The message, without the code.
    pub fn message(&self) -> &str {
        &self.0.message
    }

    /// The metadata (`err.Meta()`): response headers of a unary call, trailers of a stream.
    pub fn meta(&self) -> &HeaderMap {
        &self.0.meta
    }

    pub fn meta_mut(&mut self) -> &mut HeaderMap {
        &mut self.0.meta
    }

    /// `err.Meta().Set(name, value)`, for chaining; a name or value that is not a valid header is ignored.
    pub fn with_meta(mut self, name: &str, value: &str) -> Self {
        if let (Ok(n), Ok(v)) = (HeaderName::try_from(name), HeaderValue::try_from(value)) {
            self.0.meta.insert(n, v);
        }
        self
    }

    pub fn details(&self) -> &[ErrorDetail] {
        &self.0.details
    }

    pub fn add_detail(&mut self, d: ErrorDetail) {
        self.0.details.push(d);
    }

    /// Whether the error was read from the wire (a client's error) rather than made in this process.
    pub fn is_wire(&self) -> bool {
        self.0.wire
    }

    /// The error this one was made from ([`ConnectError::from_error`]), when it is an `E`.
    pub fn source_as<E: StdError + 'static>(&self) -> Option<&E> {
        let mut cur: Option<&(dyn StdError + 'static)> = self
            .0
            .source
            .as_deref()
            .map(|e| e as &(dyn StdError + 'static));
        while let Some(e) = cur {
            if let Some(x) = e.downcast_ref::<E>() {
                return Some(x);
            }
            cur = e.source();
        }
        None
    }

    pub(crate) fn to_wire(&self) -> WireError {
        WireError {
            code: Some(self.0.code),
            message: self.0.message.clone(),
            details: self
                .0
                .details
                .iter()
                .map(|d| WireDetail {
                    type_name: d.type_name.clone(),
                    value: base64::engine::general_purpose::STANDARD_NO_PAD.encode(&d.value),
                    debug: d.debug.clone(),
                })
                .collect(),
        }
    }

    pub(crate) fn from_wire(w: WireError, meta: HeaderMap) -> Self {
        let details = w
            .details
            .into_iter()
            .filter_map(|d| {
                let v = d.value.trim_end_matches('=');
                let value = base64::engine::general_purpose::STANDARD_NO_PAD
                    .decode(v)
                    .or_else(|_| base64::engine::general_purpose::URL_SAFE_NO_PAD.decode(v))
                    .ok()?;
                Some(ErrorDetail {
                    type_name: d.type_name,
                    value,
                    debug: d.debug,
                })
            })
            .collect();
        ConnectError(Box::new(Inner {
            code: w.code.unwrap_or(Code::Unknown),
            message: w.message,
            meta,
            details,
            wire: true,
            source: None,
        }))
    }

    /// The JSON body of a unary error: `{"code","message","details"}`.
    pub fn to_json(&self) -> Vec<u8> {
        serde_json::to_vec(&self.to_wire()).unwrap_or_else(|_| b"{\"code\":\"internal\"}".to_vec())
    }
}

impl fmt::Display for ConnectError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.0.message.is_empty() {
            write!(f, "{}", self.0.code)
        } else {
            write!(f, "{}: {}", self.0.code, self.0.message)
        }
    }
}

impl fmt::Debug for ConnectError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("ConnectError")
            .field("code", &self.0.code)
            .field("message", &self.0.message)
            .field("meta", &self.0.meta)
            .field("details", &self.0.details)
            .finish()
    }
}

impl StdError for ConnectError {
    fn source(&self) -> Option<&(dyn StdError + 'static)> {
        self.0
            .source
            .as_deref()
            .map(|e| e as &(dyn StdError + 'static))
    }
}

impl From<CtxError> for ConnectError {
    fn from(e: CtxError) -> Self {
        match e {
            CtxError::Canceled => ConnectError::from_error(Code::Canceled, e),
            CtxError::DeadlineExceeded => ConnectError::from_error(Code::DeadlineExceeded, e),
        }
    }
}

/// `connect.CodeOf(err)`: the code of the first [`ConnectError`] of the chain of `err`, `unknown` when there is none
/// (a [`CtxError`] reads as `canceled` / `deadline_exceeded`).
pub fn code_of(err: &(dyn StdError + 'static)) -> Code {
    let mut cur = Some(err);
    while let Some(e) = cur {
        if let Some(c) = e.downcast_ref::<ConnectError>() {
            return c.code();
        }
        if let Some(c) = e.downcast_ref::<CtxError>() {
            return match c {
                CtxError::Canceled => Code::Canceled,
                CtxError::DeadlineExceeded => Code::DeadlineExceeded,
            };
        }
        cur = e.source();
    }
    Code::Unknown
}

/// Writes the metadata as the JSON object of an end-of-stream message: header name (canonical, `Goap-Error-Reason`)
/// to its values.
pub(crate) fn headers_to_json(h: &HeaderMap) -> serde_json::Map<String, serde_json::Value> {
    let mut out = serde_json::Map::new();
    for name in h.keys() {
        let vals: Vec<serde_json::Value> = h
            .get_all(name)
            .iter()
            .filter_map(|v| v.to_str().ok())
            .map(|v| serde_json::Value::String(v.to_string()))
            .collect();
        out.insert(canonical(name.as_str()), serde_json::Value::Array(vals));
    }
    out
}

pub(crate) fn headers_from_json(m: &serde_json::Map<String, serde_json::Value>) -> HeaderMap {
    let mut h = HeaderMap::new();
    for (k, v) in m {
        let Ok(name) = HeaderName::try_from(k.as_str()) else {
            continue;
        };
        let vals: Vec<&str> = match v {
            serde_json::Value::Array(a) => a.iter().filter_map(|x| x.as_str()).collect(),
            serde_json::Value::String(s) => vec![s.as_str()],
            _ => vec![],
        };
        for s in vals {
            if let Ok(v) = HeaderValue::try_from(s) {
                h.append(name.clone(), v);
            }
        }
    }
    h
}

/// The canonical form of a header name (`goap-error-reason` → `Goap-Error-Reason`), as Go's `http.Header` keys.
pub fn canonical(name: &str) -> String {
    name.split('-')
        .map(|p| {
            let mut c = p.chars();
            match c.next() {
                Some(f) => f.to_ascii_uppercase().to_string() + &c.as_str().to_ascii_lowercase(),
                None => String::new(),
            }
        })
        .collect::<Vec<_>>()
        .join("-")
}
