//! Setup of the exporters, propagation, and the hooks of the RPC and HTTP layers.

use std::time::{Duration, Instant};

use goap_ctx::Ctx;
use opentelemetry::{
    Context, KeyValue, global,
    propagation::{Extractor, Injector, TextMapCompositePropagator},
    trace::{SpanKind, Status, TraceContextExt, Tracer},
};
use opentelemetry_sdk::{
    Resource,
    metrics::{PeriodicReader, SdkMeterProvider},
    propagation::{BaggagePropagator, TraceContextPropagator},
    trace::{SdkTracer, SdkTracerProvider},
};
use tower_http::{
    classify::{ServerErrorsAsFailures, SharedClassifier},
    trace::TraceLayer,
};
use tracing_opentelemetry::OpenTelemetrySpanExt;

/// Scope is the instrumentation scope of the platform's spans and metrics.
pub const SCOPE: &str = "github.com/zimwip/goap";

/// The OpenTelemetry context a [`Ctx`] carries (see [`with_otel`]).
#[derive(Clone)]
struct OtelKey(Context);

/// WithOtel returns a child of ctx carrying the OpenTelemetry context cx (its span, its baggage).
pub fn with_otel(ctx: &Ctx, cx: Context) -> Ctx {
    ctx.with(OtelKey(cx))
}

/// OtelFrom is the OpenTelemetry context of ctx: the one [`with_otel`] put there, else the current one of the
/// thread (`Context::current()`).
pub fn otel_from(ctx: &Ctx) -> Context {
    ctx.value::<OtelKey>()
        .map(|k| k.0.clone())
        .unwrap_or_else(Context::current)
}

/// The resources and providers [`setup`] installed: flush and shut them down with [`Telemetry::shutdown`].
#[derive(Default)]
pub struct Telemetry {
    tracer: Option<SdkTracerProvider>,
    meter: Option<SdkMeterProvider>,
}

impl Telemetry {
    /// Shutdown flushes and shuts the providers down (blocking: call it off the runtime's worker threads, or at the
    /// end of main). The errors are joined, one per line, as Go's `errors.Join`.
    pub fn shutdown(&self) -> Result<(), String> {
        let mut errs = Vec::new();
        if let Some(tp) = &self.tracer
            && let Err(e) = tp.shutdown()
        {
            errs.push(e.to_string());
        }
        if let Some(mp) = &self.meter
            && let Err(e) = mp.shutdown()
        {
            errs.push(e.to_string());
        }
        if errs.is_empty() {
            Ok(())
        } else {
            Err(errs.join("\n"))
        }
    }

    /// Reports whether the export is enabled.
    pub fn enabled(&self) -> bool {
        self.tracer.is_some()
    }

    /// The layer of a `tracing` subscriber that exports its spans (the HTTP spans of [`http_trace_layer`], the
    /// `#[instrument]`ed functions) through the tracer provider; None when the export is disabled. The service
    /// plumbing adds it to its subscriber.
    pub fn tracing_layer<S>(
        &self,
    ) -> Option<tracing_opentelemetry::OpenTelemetryLayer<S, SdkTracer>>
    where
        S: tracing::Subscriber + for<'span> tracing_subscriber_registry::LookupSpan<'span>,
    {
        use opentelemetry::trace::TracerProvider as _;
        self.tracer
            .as_ref()
            .map(|tp| tracing_opentelemetry::layer().with_tracer(tp.tracer(SCOPE)))
    }
}

/// Re-export of the trait bound of [`Telemetry::tracing_layer`].
pub mod tracing_subscriber_registry {
    pub use tracing_subscriber::registry::LookupSpan;
}

fn env(k: &str) -> String {
    std::env::var(k).unwrap_or_default()
}

/// Setup installs the global tracer and meter providers for a service. The exporters are enabled when
/// OTEL_EXPORTER_OTLP_ENDPOINT (or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT) is set; otherwise only context propagation is
/// installed. The returned value flushes and shuts the providers down. It logs through `tracing`.
pub fn setup(_ctx: &Ctx, service: &str) -> Telemetry {
    global::set_text_map_propagator(TextMapCompositePropagator::new(vec![
        Box::new(TraceContextPropagator::new()),
        Box::new(BaggagePropagator::new()),
    ]));
    if env("OTEL_EXPORTER_OTLP_ENDPOINT").is_empty()
        && env("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT").is_empty()
    {
        tracing::info!("telemetry export disabled (OTEL_EXPORTER_OTLP_ENDPOINT not set)");
        return Telemetry::default();
    }
    let res = Resource::builder()
        .with_service_name(service.to_string())
        .with_attribute(KeyValue::new("service.namespace", "goap"))
        .build();
    let trace_exp = match opentelemetry_otlp::SpanExporter::builder()
        .with_http()
        .build()
    {
        Ok(e) => e,
        Err(err) => {
            tracing::error!(err = %err, "telemetry trace exporter");
            return Telemetry::default();
        }
    };
    let tp = SdkTracerProvider::builder()
        .with_batch_exporter(trace_exp)
        .with_resource(res.clone())
        .build();
    global::set_tracer_provider(tp.clone());
    let mut t = Telemetry {
        tracer: Some(tp),
        meter: None,
    };
    match opentelemetry_otlp::MetricExporter::builder()
        .with_http()
        .build()
    {
        Ok(metric_exp) => {
            let mp = SdkMeterProvider::builder()
                .with_resource(res)
                .with_reader(
                    PeriodicReader::builder(metric_exp)
                        .with_interval(Duration::from_secs(15))
                        .build(),
                )
                .build();
            global::set_meter_provider(mp.clone());
            t.meter = Some(mp);
        }
        Err(err) => tracing::warn!(err = %err, "telemetry metric exporter"),
    }
    tracing::info!(
        endpoint = %env("OTEL_EXPORTER_OTLP_ENDPOINT"),
        "telemetry export enabled"
    );
    t
}

struct HeaderExtractor<'a>(&'a http::HeaderMap);

impl Extractor for HeaderExtractor<'_> {
    fn get(&self, key: &str) -> Option<&str> {
        self.0.get(key).and_then(|v| v.to_str().ok())
    }

    fn keys(&self) -> Vec<&str> {
        self.0.keys().map(|k| k.as_str()).collect()
    }
}

struct HeaderInjector<'a>(&'a mut http::HeaderMap);

impl Injector for HeaderInjector<'_> {
    fn set(&mut self, key: &str, value: String) {
        if let (Ok(k), Ok(v)) = (
            http::HeaderName::from_bytes(key.as_bytes()),
            http::HeaderValue::from_str(&value),
        ) {
            self.0.insert(k, v);
        }
    }
}

/// Extract reads the trace context and the baggage of a request's headers (W3C `traceparent`, `baggage`) with the
/// global propagator, over the parent context.
pub fn extract(parent: &Context, headers: &http::HeaderMap) -> Context {
    global::get_text_map_propagator(|p| p.extract_with_context(parent, &HeaderExtractor(headers)))
}

/// Inject writes the trace context and the baggage of cx in a request's headers with the global propagator.
pub fn inject(cx: &Context, headers: &mut http::HeaderMap) {
    global::get_text_map_propagator(|p| p.inject_context(cx, &mut HeaderInjector(headers)));
}

/// The span of one RPC, as otelconnect makes it: named after the procedure (`package.Service/Method`), attributes
/// `rpc.system` (`connect_rpc`), `rpc.service`, `rpc.method`, and the Connect code of an error
/// (`rpc.connect_rpc.error_code`). Its context is the parent of what the call does ([`RpcSpan::ctx`]).
#[derive(Clone)]
pub struct RpcSpan {
    cx: Context,
}

fn rpc_span(parent: &Context, procedure: &str, kind: SpanKind) -> RpcSpan {
    let name = procedure.trim_start_matches('/').to_string();
    let (service, method) = name.split_once('/').unwrap_or((name.as_str(), ""));
    let attrs = vec![
        KeyValue::new("rpc.system", "connect_rpc"),
        KeyValue::new("rpc.service", service.to_string()),
        KeyValue::new("rpc.method", method.to_string()),
    ];
    let tracer = global::tracer(SCOPE);
    let span = tracer
        .span_builder(name.clone())
        .with_kind(kind)
        .with_attributes(attrs)
        .start_with_context(&tracer, parent);
    RpcSpan {
        cx: parent.with_span(span),
    }
}

/// ServerSpan starts the span of a call a Connect handler serves. Internal calls: the server span is a child of the
/// caller's span carried by the headers (otelconnect's `WithTrustRemote`), else of the context of ctx; no peer
/// attribute is recorded (`WithoutServerPeerAttributes`).
pub fn server_span(ctx: &Ctx, procedure: &str, headers: &http::HeaderMap) -> RpcSpan {
    let parent = extract(&otel_from(ctx), headers);
    rpc_span(&parent, procedure, SpanKind::Server)
}

/// ClientSpan starts the span of a call a Connect client makes, a child of the context of ctx, and writes its trace
/// context in the request's headers.
pub fn client_span(ctx: &Ctx, procedure: &str, headers: &mut http::HeaderMap) -> RpcSpan {
    let s = rpc_span(&otel_from(ctx), procedure, SpanKind::Client);
    inject(&s.cx, headers);
    s
}

impl RpcSpan {
    /// The context of the call: ctx carrying the span.
    pub fn ctx(&self, ctx: &Ctx) -> Ctx {
        with_otel(ctx, self.cx.clone())
    }

    /// The OpenTelemetry context holding the span.
    pub fn otel(&self) -> &Context {
        &self.cx
    }

    /// End ends the span with the outcome of the call: None when it succeeded, else the Connect code of its error
    /// (`not_found`, ...) and its message.
    pub fn end(&self, error: Option<(&str, &str)>) {
        let span = self.cx.span();
        if let Some((code, msg)) = error {
            span.set_attribute(KeyValue::new(
                "rpc.connect_rpc.error_code",
                code.to_string(),
            ));
            span.set_status(Status::error(msg.to_string()));
        }
        span.end();
    }
}

/// SlowRPC is the duration from which a unary call is logged at warn level instead of debug.
pub const SLOW_RPC: Duration = Duration::from_secs(1);

/// LogRPC logs one unary call (the timing hook): `rpc` with its procedure, duration and code ("ok" when it
/// succeeded), at debug level (GOAP_LOG_LEVEL=debug), warn from [`SLOW_RPC`]. Streaming calls are not timed (their
/// duration is the connection's).
pub fn log_rpc(procedure: &str, d: Duration, code: &str) {
    let ms = d.as_millis() as i64;
    if d >= SLOW_RPC {
        tracing::warn!(procedure, duration_ms = ms, code, "rpc");
    } else {
        tracing::debug!(procedure, duration_ms = ms, code, "rpc");
    }
}

/// Timed runs one unary call and logs it with [`log_rpc`]; code_of gives the Connect code of its error.
pub async fn timed<T, E, F>(
    procedure: &str,
    code_of: impl FnOnce(&E) -> String,
    call: F,
) -> Result<T, E>
where
    F: Future<Output = Result<T, E>>,
{
    let start = Instant::now();
    let out = call.await;
    let code = match &out {
        Ok(_) => "ok".to_string(),
        Err(e) => code_of(e),
    };
    log_rpc(procedure, start.elapsed(), &code);
    out
}

/// The span of an HTTP request a server serves (the Echo middleware of the Go gateway): `http.request` with
/// `http.request.method` and `url.path`, a child of the trace context of the request's headers.
pub fn http_span<B>(req: &http::Request<B>) -> tracing::Span {
    let span = tracing::info_span!(
        "http.request",
        http.request.method = %req.method(),
        url.path = %req.uri().path(),
    );
    let _ = span.set_parent(extract(&Context::new(), req.headers()));
    span
}

/// The tower layer tracing the HTTP requests of an axum router (tower-http's trace layer with [`http_span`]); its
/// spans are exported by [`Telemetry::tracing_layer`].
#[allow(clippy::type_complexity)]
pub fn http_trace_layer<B>()
-> TraceLayer<SharedClassifier<ServerErrorsAsFailures>, fn(&http::Request<B>) -> tracing::Span> {
    TraceLayer::new_for_http()
        .make_span_with(http_span::<B> as fn(&http::Request<B>) -> tracing::Span)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::{Arc, Mutex};

    #[derive(Clone, Default)]
    struct Buf(Arc<Mutex<Vec<u8>>>);

    impl std::io::Write for Buf {
        fn write(&mut self, b: &[u8]) -> std::io::Result<usize> {
            self.0.lock().unwrap().extend_from_slice(b);
            Ok(b.len())
        }
        fn flush(&mut self) -> std::io::Result<()> {
            Ok(())
        }
    }

    fn logged(level: tracing::Level, f: impl FnOnce()) -> String {
        let buf = Buf::default();
        let w = buf.clone();
        let sub = tracing_subscriber::fmt()
            .with_max_level(level)
            .with_ansi(false)
            .with_writer(move || w.clone())
            .finish();
        tracing::subscriber::with_default(sub, f);
        String::from_utf8(buf.0.lock().unwrap().clone()).unwrap()
    }

    // A unary call is logged with its procedure, duration and code at debug level, nothing at info.
    #[test]
    fn timing_logs_every_unary_call_at_debug() {
        let run = || {
            let rt = tokio::runtime::Builder::new_current_thread()
                .build()
                .unwrap();
            rt.block_on(timed(
                "/x.v1.S/M",
                |_: &String| "not_found".to_string(),
                async { Err::<(), String>("no".into()) },
            ))
            .unwrap_err();
        };
        let out = std::thread::spawn(move || logged(tracing::Level::DEBUG, run))
            .join()
            .unwrap();
        for want in ["rpc", "duration_ms=", "not_found", "DEBUG"] {
            assert!(out.contains(want), "{want:?} missing from {out:?}");
        }
        let out = std::thread::spawn(move || logged(tracing::Level::INFO, run))
            .join()
            .unwrap();
        assert!(out.is_empty(), "logged at info: {out:?}");
    }

    #[test]
    fn propagation_round_trips_the_trace_context() {
        global::set_text_map_propagator(TraceContextPropagator::new());
        let mut h = http::HeaderMap::new();
        h.insert(
            "traceparent",
            "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
                .parse()
                .unwrap(),
        );
        let cx = extract(&Context::new(), &h);
        assert_eq!(
            cx.span().span_context().trace_id().to_string(),
            "4bf92f3577b34da6a3ce929d0e0e4736"
        );
        let mut out = http::HeaderMap::new();
        inject(&cx, &mut out);
        assert_eq!(out["traceparent"], h["traceparent"]);
    }
}
