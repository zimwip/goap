//! The interceptors of the Connect layer (`HandlerOptions` / `ClientOptions` of `telemetry.go`): otelconnect's
//! spans ([`server_span`] / [`client_span`]) and the timing log of the unary calls ([`log_rpc`]).

use std::time::Instant;

use goap_connect::{
    AnyRequest, AnyResponse, ConnectError, Ctx, Interceptor, Interceptors, Next, async_trait,
};

use crate::telemetry::{client_span, log_rpc, server_span};

/// Otel is otelconnect's interceptor (`WithTrustRemote`, `WithoutServerPeerAttributes`): one span per call, a
/// server span on a handler (a child of the caller's span carried by the headers), a client span on a client (its
/// trace context written in the request's headers). The context of the call carries the span.
///
/// A server stream gets its span when it opens and the span ends there (the hook of a stream sees no end): what the
/// stream does is a child of it all the same.
#[derive(Debug, Clone, Copy, Default)]
pub struct Otel;

#[async_trait]
impl Interceptor for Otel {
    async fn unary(
        &self,
        ctx: Ctx,
        mut req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        let procedure = req.spec.procedure.clone();
        let span = if req.spec.is_client {
            client_span(&ctx, &procedure, &mut req.headers)
        } else {
            server_span(&ctx, &procedure, &req.headers)
        };
        let out = next.run(span.ctx(&ctx), req).await;
        match &out {
            Ok(_) => span.end(None),
            Err(e) => span.end(Some((e.code().as_str(), e.message()))),
        }
        out
    }

    async fn stream(&self, ctx: Ctx, req: &mut AnyRequest) -> Result<Ctx, ConnectError> {
        let procedure = req.spec.procedure.clone();
        let span = if req.spec.is_client {
            client_span(&ctx, &procedure, &mut req.headers)
        } else {
            server_span(&ctx, &procedure, &req.headers)
        };
        let ctx = span.ctx(&ctx);
        span.end(None);
        Ok(ctx)
    }
}

/// Timing logs the duration of every unary call with [`log_rpc`]: debug (GOAP_LOG_LEVEL=debug) for all of them,
/// warn from [`crate::SLOW_RPC`]. Streaming calls are not timed (their duration is the connection's).
#[derive(Debug, Clone, Copy, Default)]
pub struct Timing;

#[async_trait]
impl Interceptor for Timing {
    async fn unary(
        &self,
        ctx: Ctx,
        req: AnyRequest,
        next: Next<'_>,
    ) -> Result<AnyResponse, ConnectError> {
        let procedure = req.spec.procedure.clone();
        let start = Instant::now();
        let out = next.run(ctx, req).await;
        let code = match &out {
            Ok(_) => "ok",
            Err(e) => e.code().as_str(),
        };
        log_rpc(&procedure, start.elapsed(), code);
        out
    }
}

/// HandlerOptions instruments Connect handlers: give it to a service's router (`<service>_router(svc, ...)`),
/// before the service's own interceptors.
pub fn handler_options() -> Interceptors {
    Interceptors::none().with(Otel).with(Timing)
}

/// ClientOptions instruments Connect clients (`ClientBuilder::interceptors`).
pub fn client_options() -> Interceptors {
    Interceptors::none().with(Otel)
}

#[cfg(test)]
mod tests {
    use super::*;
    use goap_connect::{Code, Spec, UnaryEnd};
    use opentelemetry::{global, trace::TraceContextExt};
    use opentelemetry_sdk::propagation::TraceContextPropagator;
    use std::sync::{Arc, Mutex};

    fn request(is_client: bool, headers: http::HeaderMap) -> AnyRequest {
        AnyRequest {
            spec: Spec {
                procedure: "/x.v1.S/M".into(),
                is_client,
                ..Default::default()
            },
            headers,
            msg: Box::new(pbjson_types::Empty {}),
        }
    }

    const PARENT: &str = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01";

    // A handler's call runs with the caller's trace (the headers'), and a client's call carries the trace of its
    // context to the server.
    #[tokio::test]
    async fn the_trace_crosses_the_calls() {
        global::set_text_map_propagator(TraceContextPropagator::new());
        let seen: Arc<Mutex<Option<String>>> = Arc::default();
        let s = seen.clone();
        let end: Box<UnaryEnd> = Box::new(move |ctx: Ctx, req: AnyRequest| {
            let s = s.clone();
            Box::pin(async move {
                let cx = crate::otel_from(&ctx);
                *s.lock().unwrap() = Some(cx.span().span_context().trace_id().to_string());
                // a client call made by the handler: the trace goes on in its headers
                let mut out = request(true, http::HeaderMap::new());
                let client = client_options();
                let sent: Arc<Mutex<String>> = Arc::default();
                let sent2 = sent.clone();
                let inner: Box<UnaryEnd> = Box::new(move |_ctx: Ctx, req: AnyRequest| {
                    *sent2.lock().unwrap() = req.header("traceparent").to_string();
                    Box::pin(async { Err(ConnectError::new(Code::NotFound, "no")) })
                });
                out.headers = req.headers.clone();
                out.headers.remove("traceparent");
                let _ = client.run_unary(ctx, out, &*inner).await;
                let tp = sent.lock().unwrap().clone();
                Err(ConnectError::new(Code::Internal, tp))
            })
        });
        let mut h = http::HeaderMap::new();
        h.insert("traceparent", PARENT.parse().unwrap());
        let err = handler_options()
            .run_unary(Ctx::background(), request(false, h), &*end)
            .await
            .unwrap_err();
        assert_eq!(
            seen.lock().unwrap().as_deref(),
            Some("4bf92f3577b34da6a3ce929d0e0e4736")
        );
        assert!(
            err.message().contains("4bf92f3577b34da6a3ce929d0e0e4736"),
            "client headers {:?}",
            err.message()
        );
    }

    #[tokio::test]
    async fn a_stream_opens_with_the_span() {
        global::set_text_map_propagator(TraceContextPropagator::new());
        let mut h = http::HeaderMap::new();
        h.insert("traceparent", PARENT.parse().unwrap());
        let mut req = request(false, h);
        let ctx = handler_options()
            .run_stream(Ctx::background(), &mut req)
            .await
            .unwrap();
        assert_eq!(
            crate::otel_from(&ctx)
                .span()
                .span_context()
                .trace_id()
                .to_string(),
            "4bf92f3577b34da6a3ce929d0e0e4736"
        );
    }
}
