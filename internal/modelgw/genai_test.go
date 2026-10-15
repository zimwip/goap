package modelgw

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/zimwip/goap/internal/telemetry"
	"github.com/zimwip/goap/pkg/llm"
)

// The router instrumented with the GenAI spans of the telemetry every service sets up (docs/architecture.md §3.7).

func recorder(t *testing.T) *tracetest.SpanRecorder {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

func attr(s sdktrace.ReadOnlySpan, key string) attribute.Value {
	for _, kv := range s.Attributes() {
		if string(kv.Key) == key {
			return kv.Value
		}
	}
	return attribute.Value{}
}

func one(t *testing.T, spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	t.Fatalf("no span %q in %d spans", name, len(spans))
	return nil
}

func TestGenAIRecordsRetriesAndTheErrorType(t *testing.T) {
	rec := recorder(t)
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) <= 2 {
			http.Error(w, "busy", http.StatusTooManyRequests)
			return
		}
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	r := NewRouter()
	r.RetryBase = time.Millisecond
	r.AddProvider("oa", &OpenAICompatible{BaseURL: srv.URL})
	r.Instrument = telemetry.NewGenAI().Instrument
	if _, err := r.Complete(context.Background(), llm.Request{Model: "oa/m"}); err == nil {
		t.Fatal("the 400 must fail the call")
	}
	s := one(t, rec.Ended(), "chat m")
	if attr(s, "error.type").AsString() != "400" {
		t.Fatalf("error.type %q", attr(s, "error.type").AsString())
	}
	var retries []string
	for _, e := range s.Events() {
		if e.Name == "gen_ai.retry" {
			for _, kv := range e.Attributes {
				if kv.Key == "error.type" {
					retries = append(retries, kv.Value.AsString())
				}
			}
		}
	}
	if strings.Join(retries, ",") != "429,429" {
		t.Fatalf("retry events %v", retries)
	}
}

func TestGenAIEmbeddingsSpan(t *testing.T) {
	rec := recorder(t)
	r := NewRouter()
	r.AddProvider("f", Fake{})
	r.InstrumentEmbed = telemetry.NewGenAI().InstrumentEmbed
	ctx := llm.WithMeta(context.Background(), llm.CallMeta{Source: llm.SourceIndexer})
	if _, err := r.Embed(ctx, llm.EmbedRequest{Model: "f/e", Texts: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	s := one(t, rec.Ended(), "embeddings e")
	if attr(s, "gen_ai.operation.name").AsString() != "embeddings" || attr(s, "goap.llm.texts").AsInt64() != 2 ||
		attr(s, "goap.llm.source").AsString() != "indexer" {
		t.Fatalf("attributes %v", s.Attributes())
	}
}
