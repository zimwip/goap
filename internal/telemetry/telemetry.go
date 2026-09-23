// Package telemetry wires OpenTelemetry: OTLP exporters (traces and
// metrics, configured by the standard OTEL_* environment variables),
// W3C trace context propagation, Connect interceptors, and the GOAP
// instrumentation of processes, actions, LLM calls (GenAI semantic
// conventions) and tool calls.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// Setup installs the global tracer and meter providers for a service. The
// exporters are enabled when OTEL_EXPORTER_OTLP_ENDPOINT is set; otherwise
// only context propagation is installed. The returned function flushes and
// shuts the providers down.
func Setup(ctx context.Context, log *slog.Logger, service string) func(context.Context) error {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" && os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") == "" {
		log.Info("telemetry export disabled (OTEL_EXPORTER_OTLP_ENDPOINT not set)")
		return func(context.Context) error { return nil }
	}
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(service), semconv.ServiceNamespace("goap")))
	if err != nil {
		log.Warn("telemetry resource", "err", err)
		res = resource.Default()
	}
	traceExp, err := otlptracehttp.New(ctx)
	if err != nil {
		log.Error("telemetry trace exporter", "err", err)
		return func(context.Context) error { return nil }
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(traceExp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	shutdown := []func(context.Context) error{tp.Shutdown}
	if metricExp, err := otlpmetrichttp.New(ctx); err == nil {
		mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
			sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp, sdkmetric.WithInterval(15*time.Second))))
		otel.SetMeterProvider(mp)
		shutdown = append(shutdown, mp.Shutdown)
	} else {
		log.Warn("telemetry metric exporter", "err", err)
	}
	log.Info("telemetry export enabled", "endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	return func(ctx context.Context) error {
		var errs []error
		for _, f := range shutdown {
			errs = append(errs, f(ctx))
		}
		return errors.Join(errs...)
	}
}

func interceptor() connect.Interceptor {
	// internal calls: server spans are children of the caller span
	i, err := otelconnect.NewInterceptor(otelconnect.WithTrustRemote(), otelconnect.WithoutServerPeerAttributes())
	if err != nil {
		panic(err)
	}
	return i
}

// HandlerOptions instruments Connect handlers.
func HandlerOptions() []connect.HandlerOption {
	return []connect.HandlerOption{connect.WithInterceptors(interceptor())}
}

// ClientOptions instruments Connect clients.
func ClientOptions() []connect.ClientOption {
	return []connect.ClientOption{connect.WithInterceptors(interceptor())}
}
