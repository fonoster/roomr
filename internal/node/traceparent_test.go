package node

import (
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// An INVITE that carries a traceparent (Routr's SIP span) makes the call span its child, and the
// app's session start carries the call span's traceparent, so the app continues the same trace.
func TestInboundCallContinuesTheInvitesTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	const traceID, routrSpan = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	a := &app{}
	n := New(nil, Config{SessionEndpoint: serveApp(t, a), CallRefHeader: "X-Call-Ref", EmptyRoomTimeout: -1})
	defer n.Shutdown()
	leg := newFakeLeg()
	go n.Incoming(invite(leg, map[string]string{"X-Call-Ref": "c1",
		"Traceparent": "00-" + traceID + "-" + routrSpan + "-01"}))
	eventually(t, func() bool { _, answered, _ := leg.state(); return answered })

	a.mu.Lock()
	headers := a.starts[0].GetHeaders()
	a.mu.Unlock()
	if _, ok := headers["Traceparent"]; ok {
		t.Fatalf("the INVITE's traceparent reached the app: %v", headers)
	}
	sent := strings.Split(headers["traceparent"], "-")
	if len(sent) != 4 || sent[1] != traceID || sent[2] == routrSpan {
		t.Fatalf("session start traceparent = %q", headers["traceparent"])
	}

	leg.hangup()
	eventually(t, func() bool { return len(rec.Ended()) == 1 })
	span := rec.Ended()[0]
	if span.Name() != "roomr.call.inbound" || span.Parent().SpanID().String() != routrSpan ||
		span.SpanContext().SpanID().String() != sent[2] {
		t.Fatalf("span %s parent %s id %s", span.Name(), span.Parent().SpanID(), span.SpanContext().SpanID())
	}
}
