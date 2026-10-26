package node

import (
	"strings"
	"sync"
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

// recordingDialer keeps the headers of the INVITE it is asked to send and answers busy.
type recordingDialer struct {
	mu      sync.Mutex
	headers map[string]string
}

func (d *recordingDialer) DialTarget(_ Context, t SipTarget, _ string, _ []byte, _ func(int)) (OutLeg, []byte, error) {
	d.mu.Lock()
	d.headers = t.Headers
	d.mu.Unlock()
	return nil, nil, &DialError{Status: 486}
}

// An app that dials with a traceparent gets a call span in its trace, and the INVITE carries
// the call span's traceparent, so Routr's SIP spans are children of the call.
func TestOutboundInviteCarriesTheCallsTraceparent(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	defer otel.SetTracerProvider(prev)

	const traceID, appSpan = "0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331"
	n := New(nil, Config{SessionEndpoint: serveApp(t, &app{}), EmptyRoomTimeout: -1})
	defer n.Shutdown()
	d := &recordingDialer{}
	n.SetDialer(d)
	if _, err := n.CreateCall(CallRequest{CallRef: "o1", From: "+15550001",
		To: SipTarget{URI: "sip:+15550002@carrier.example.com",
			Headers: map[string]string{"traceparent": "00-" + traceID + "-" + appSpan + "-01"}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { d.mu.Lock(); defer d.mu.Unlock(); return d.headers != nil })
	eventually(t, func() bool { return len(rec.Ended()) == 1 })

	span := rec.Ended()[0]
	sent := strings.Split(d.headers["traceparent"], "-")
	if span.Name() != "roomr.call.outbound" || span.Parent().SpanID().String() != appSpan ||
		len(sent) != 4 || sent[1] != traceID || sent[2] != span.SpanContext().SpanID().String() {
		t.Fatalf("span %s parent %s; INVITE traceparent %q", span.Name(), span.Parent().SpanID(), d.headers["traceparent"])
	}
}
