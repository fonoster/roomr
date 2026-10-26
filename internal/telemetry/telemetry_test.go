package telemetry

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestDisabledIsNoop(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		t.Setenv(k, "")
	}
	if Enabled() {
		t.Fatal("enabled without endpoint")
	}
	shutdown, err := Setup(context.Background(), "x", "n")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, end := StartCall(context.Background(), "x", "call-1")
	end()
	CallIncoming(context.Background(), "call-1", "claimed")
}

func TestEnabledWithEndpointAndSetup(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	if !Enabled() {
		t.Fatal("expected enabled")
	}
	shutdown, err := Setup(context.Background(), "x", "n")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	m, err := RegisterNode("n", func() int { return 0 })
	if err != nil {
		t.Fatal(err)
	}
	SetMeters(m)
	_, end := StartCall(context.Background(), "x", "call-1")
	end()
	_ = shutdown(context.Background())
	SetMeters(nil)
}

func TestTraceparentPropagation(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(prev)

	if got := Traceparent(context.Background()); got != "" {
		t.Fatalf("traceparent without a span: %q", got)
	}
	const parent = "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	ctx := Extract(context.Background(), parent)
	ctx, end := StartSpan(ctx, "roomr.command")
	out := Traceparent(ctx)
	end()
	if !strings.HasPrefix(out, "00-0af7651916cd43dd8448eb211c80319c-") || out == parent {
		t.Fatalf("child traceparent %q does not continue %q", out, parent)
	}
	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Parent().SpanID().String() != "b7ad6b7169203331" {
		t.Fatalf("span parent not taken from traceparent: %+v", spans)
	}
	if Extract(context.Background(), "garbage") == nil {
		t.Fatal("nil context")
	}
}

func TestSetSIPCallIDTagsTheCallSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	ctx, end := StartCall(context.Background(), "roomr.call.inbound", "call-1")
	SetSIPCallID(ctx, "abc@10.0.0.1")
	end()

	attrs := map[string]string{}
	for _, a := range rec.Ended()[0].Attributes() {
		attrs[string(a.Key)] = a.Value.AsString()
	}
	if attrs[CallRefKey] != "call-1" || attrs[SIPCallIDKey] != "abc@10.0.0.1" {
		t.Fatalf("attributes = %v", attrs)
	}
}

// recordingHandler keeps the attributes of every record it handles.
type recordingHandler struct {
	attrs *[]map[string]string
	with  []slog.Attr
}

func (h recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h recordingHandler) Handle(_ context.Context, r slog.Record) error {
	m := map[string]string{}
	for _, a := range h.with {
		m[a.Key] = a.Value.String()
	}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.String(); return true })
	*h.attrs = append(*h.attrs, m)
	return nil
}

func (h recordingHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return recordingHandler{h.attrs, append(append([]slog.Attr{}, h.with...), attrs...)}
}

func (h recordingHandler) WithGroup(string) slog.Handler { return h }

func TestLogRecordsCarryTheCallRef(t *testing.T) {
	var got []map[string]string
	logger := slog.New(callRefHandler{recordingHandler{attrs: &got}})

	logger.Warn("control stream ended", "callRef", "call-1")
	logger.With("callRef", "call-2").Warn("hangup")
	logger.Warn("no call")

	if got[0][CallRefKey] != "call-1" || got[1][CallRefKey] != "call-2" {
		t.Fatalf("records = %v", got)
	}
	if _, ok := got[2][CallRefKey]; ok {
		t.Fatalf("a record without a call got a call ref: %v", got[2])
	}
}

func TestLogHandlerIsUnchangedWithoutAnEndpoint(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(k, "")
	}
	h := slog.NewJSONHandler(io.Discard, nil)
	if LogHandler(h) != slog.Handler(h) {
		t.Fatal("LogHandler wrapped the handler with telemetry off")
	}
}

func TestNoLogExportWithOnlyATracesEndpoint(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "http://collector:4318/v1/traces")
	h := slog.NewJSONHandler(io.Discard, nil)
	if LogHandler(h) != slog.Handler(h) {
		t.Fatal("logs would go to localhost: only a traces endpoint is set")
	}
}
