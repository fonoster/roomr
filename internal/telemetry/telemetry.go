// Package telemetry wires OpenTelemetry traces and metrics for the Roomr node (add-platform
// observability). It is driven by the standard OTEL_* environment variables and is a complete
// no-op when no OTLP endpoint is configured. Logs are JSON on standard error only.
package telemetry

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// CallRefKey is the attribute every call-scoped span, log record and metric carries.
const CallRefKey = "fonoster.call_ref"

// SIPCallIDKey is the call's SIP Call-ID, the attribute Routr's SIP spans carry too.
const SIPCallIDKey = "sip.call_id"

const scope = "github.com/fonoster/roomr"

// logsEnabled reports whether log records have an OTLP endpoint: the base one or the logs one.
func logsEnabled() bool {
	return Enabled() && (os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" ||
		os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") != "")
}

// Enabled reports whether an OTLP endpoint is configured (and OTEL_SDK_DISABLED is not true).
func Enabled() bool {
	if os.Getenv("OTEL_SDK_DISABLED") == "true" {
		return false
	}
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

// CallAttr is the call correlation attribute.
func CallAttr(callRef string) attribute.KeyValue { return attribute.String(CallRefKey, callRef) }

// Setup installs the global trace, metric and log providers. It returns the shutdown func (flushes
// exporters); with no OTLP endpoint configured it does nothing.
func Setup(ctx context.Context, service, nodeID string) (func(context.Context) error, error) {
	if !Enabled() {
		return func(context.Context) error { return nil }, nil
	}
	res, err := resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", envOr("OTEL_SERVICE_NAME", service)),
		attribute.String("service.instance.id", nodeID)))
	if err != nil {
		return nil, err
	}
	te, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res))
	me, err := otlpmetrichttp.New(ctx)
	if err != nil {
		return nil, err
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(me, sdkmetric.WithInterval(15*time.Second))))
	shutdowns := []func(context.Context) error{tp.Shutdown, mp.Shutdown}
	// Logs only with an endpoint for them: with only a traces (or metrics) endpoint the exporter
	// would fall back to localhost.
	if logsEnabled() {
		le, err := otlploghttp.New(ctx)
		if err != nil {
			_ = errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
			return nil, err
		}
		lp := sdklog.NewLoggerProvider(sdklog.WithResource(res),
			sdklog.WithProcessor(sdklog.NewBatchProcessor(le)))
		global.SetLoggerProvider(lp)
		shutdowns = append(shutdowns, lp.Shutdown)
	}
	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var errs []error
		for _, shutdown := range shutdowns {
			errs = append(errs, shutdown(ctx))
		}
		return errors.Join(errs...)
	}, nil
}

// StartCall starts a span tagged with the call ref and returns the func that ends it. Without
// Setup the global provider is a no-op, so this costs nothing.
func StartCall(ctx context.Context, name, callRef string) (context.Context, func()) {
	ctx, span := otel.Tracer(scope).Start(ctx, name, trace.WithAttributes(CallAttr(callRef)))
	return ctx, func() { span.End() }
}

// SetSIPCallID tags the call's span (in ctx, from StartCall) with its SIP Call-ID, so a search
// by Call-ID finds Routr's SIP spans and this call, which gives the call ref.
func SetSIPCallID(ctx context.Context, callID string) {
	if callID != "" {
		trace.SpanFromContext(ctx).SetAttributes(attribute.String(SIPCallIDKey, callID))
	}
}

// LogHandler returns h, plus the OTLP log bridge when telemetry is on: records keep going to h
// and also go out over OTLP, with the node's `callRef` attribute also sent as
// `fonoster.call_ref`.
func LogHandler(h slog.Handler) slog.Handler {
	if !logsEnabled() {
		return h
	}
	return slog.NewMultiHandler(h, callRefHandler{otelslog.NewHandler(scope)})
}

// callRefHandler copies `callRef` attributes to `fonoster.call_ref`.
type callRefHandler struct{ slog.Handler }

func (h callRefHandler) Handle(ctx context.Context, r slog.Record) error {
	var ref string
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == "callRef" {
			ref = a.Value.String()
			return false
		}
		return true
	})
	if ref != "" {
		r = r.Clone()
		r.AddAttrs(slog.String(CallRefKey, ref))
	}
	return h.Handler.Handle(ctx, r)
}

func (h callRefHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	for _, a := range attrs {
		if a.Key == "callRef" {
			attrs = append(attrs, slog.String(CallRefKey, a.Value.String()))
			break
		}
	}
	return callRefHandler{h.Handler.WithAttrs(attrs)}
}

func (h callRefHandler) WithGroup(name string) slog.Handler {
	return callRefHandler{h.Handler.WithGroup(name)}
}

// Meters holds the node's instruments.
type Meters struct {
	incoming metric.Int64Counter
}

// RegisterNode creates the node's metrics: active rooms as a gauge read at export time, and a
// counter of incoming calls by outcome.
func RegisterNode(nodeID string, activeRooms func() int) (*Meters, error) {
	m := otel.Meter(scope)
	base := metric.WithAttributes(attribute.String("fonoster.node_id", nodeID))
	_, err := m.Int64ObservableGauge("fonoster.roomr.rooms.active",
		metric.WithDescription("Rooms that have not ended"),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			o.Observe(int64(activeRooms()), base)
			return nil
		}))
	if err != nil {
		return nil, err
	}
	c, err := m.Int64Counter("fonoster.roomr.calls.incoming", metric.WithDescription("Incoming calls by outcome"))
	return &Meters{incoming: c}, err
}

var meters *Meters

// SetMeters makes the counters reachable from the node package.
func SetMeters(m *Meters) { meters = m }

// CallIncoming counts an incoming call; outcome is answered, unanswered or refused.
func CallIncoming(ctx context.Context, callRef, outcome string) {
	if meters == nil || meters.incoming == nil {
		return
	}
	meters.incoming.Add(ctx, 1, metric.WithAttributes(CallAttr(callRef), attribute.String("outcome", outcome)))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TraceparentHeader is the NATS header carrying the W3C trace context between services (the
// unused by the node since the NATS command path was retired; kept for callers).
const TraceparentHeader = "traceparent"

var traceContext = propagation.TraceContext{}

// Extract returns ctx with the remote span context of a W3C traceparent (unchanged when empty or
// invalid), so spans started from it join the sender's trace.
func Extract(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return traceContext.Extract(ctx, propagation.MapCarrier{TraceparentHeader: traceparent})
}

// Traceparent returns the W3C traceparent of ctx's span, or "" when there is no recording span.
func Traceparent(ctx context.Context) string {
	c := propagation.MapCarrier{}
	traceContext.Inject(ctx, c)
	return c[TraceparentHeader]
}

// StartSpan starts a span (not call-scoped) under ctx and returns the func that ends it.
func StartSpan(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, func()) {
	ctx, span := otel.Tracer(scope).Start(ctx, name, trace.WithAttributes(attrs...))
	return ctx, func() { span.End() }
}
