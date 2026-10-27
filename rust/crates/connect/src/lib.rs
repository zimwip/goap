//! The Connect protocol of the platform, replacing connect-go: what every service serves and calls.
//!
//! - [`ConnectError`] / [`Code`]: the error of a call (code, message, metadata headers, details), its HTTP status and
//!   its JSON wire form `{"code","message","details"}`.
//! - [`Request`] / [`Response`]: a message with its headers (and trailers); [`ServerStream`] the messages of a
//!   server stream.
//! - [`Interceptor`]: connect-go's interceptors, on servers and clients ([`Before`] for the common synchronous
//!   ones), chained in [`Interceptors`].
//! - [`ServiceRouter`]: the routes of a service as an `axum::Router` (`POST /<pkg.Service>/<Method>`): unary calls in
//!   `application/json` (the web) and `application/proto`, server streams in `application/connect+json` /
//!   `application/connect+proto` (5-byte envelopes, an end-of-stream message carrying the error and the trailers).
//! - [`Client`]: the client over `reqwest`, with the same codecs and interceptors.
//!
//! The generated contracts (`goap_proto`) give, per service, a trait `<Service>` with one async method per RPC (default
//! bodies answer `unimplemented`), `<service>_router(Arc<dyn Service>, Interceptors) -> axum::Router` and a
//! `<Service>Client` implementing the trait over a [`Client`].
//!
//! ```ignore
//! use std::sync::Arc;
//! use goap_connect::{async_trait, Client, ConnectError, Ctx, Interceptors, Request, Response};
//! use goap_proto::preferences::v1::{self as pb, PreferencesService};
//!
//! struct Prefs;
//!
//! #[async_trait]
//! impl PreferencesService for Prefs {
//!     async fn get_preferences(&self, _ctx: &Ctx, _req: Request<pb::GetPreferencesRequest>)
//!         -> Result<Response<pb::GetPreferencesResponse>, ConnectError> {
//!         Ok(Response::new(pb::GetPreferencesResponse::default()))
//!     }
//! }
//!
//! // server: mount the router on the axum app
//! let app = pb::preferences_service_router(Arc::new(Prefs), Interceptors::none());
//! // client: every RPC is a method of the trait
//! let client = pb::PreferencesServiceClient::new(Client::new("http://preferences:8080"));
//! let got = client.get_preferences(&Ctx::background(), Request::new(pb::GetPreferencesRequest::default())).await?;
//! ```
//!
//! Compression, on the server only, as the Go services configure connect-go: a gzip request is read, an answer of
//! [`protocol::COMPRESS_MIN_BYTES`] or more is gzipped when the client accepts it (`Accept-Encoding` of a unary call,
//! `Connect-Accept-Encoding` of a stream), another compression refused with `unimplemented`; the client sends and
//! accepts none.
//!
//! Not reproduced from connect-go: the gRPC and gRPC-Web protocols, GET requests for side-effect-free methods, client
//! and bidirectional streaming.

mod client;
mod error;
mod interceptor;
mod message;
pub mod protocol;
mod request;
mod server;

pub use async_trait::async_trait;
pub use axum;
pub use goap_ctx::Ctx;

pub use client::{Client, ClientBuilder};
pub use error::{Code, ConnectError, ErrorDetail, UnknownCode, canonical, code_of};
pub use interceptor::{Before, BoxFuture, Interceptor, Interceptors, Next, UnaryEnd};
pub use message::{AnyMessage, Codec, Message};
pub use request::{AnyRequest, AnyResponse, Request, Response, ServerStream, Spec, StreamType, server_stream};
pub use server::{ServiceRouter, error_response};

/// The result of an RPC method.
pub type RpcResult<T> = Result<Response<T>, ConnectError>;
