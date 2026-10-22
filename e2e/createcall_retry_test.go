package e2e

import (
	"context"
	"fmt"
	"testing"
	"time"

	roomrv1 "github.com/fonoster/roomr/api/roomr/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// TestROM024_CreateCallRetryAfterClientTimeout: the node accepted CreateCall and the callee is still
// ringing, but the controller's deadline expired before the reply reached it (the reply is dropped
// on the client side, which is what a lost or late answer looks like). Retrying the same call_ref
// with the same parameters returns the existing call and the callee sees one INVITE.
func TestROM024_CreateCallRetryAfterClientTimeout(t *testing.T) {
	r := start(t)
	r.callee.Callee("no_answer", 0) // rings until the node's ring timeout
	s, _ := r.withControl(nil)
	conn, err := grpc.NewClient(s.Addr(), grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn,
			invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			if err := invoker(ctx, method, req, reply, cc, opts...); err != nil {
				return err
			}
			return status.Error(codes.DeadlineExceeded, "context deadline exceeded")
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	ref := fmt.Sprintf("retry-%d", time.Now().UnixNano())
	req := &roomrv1.CreateCallRequest{CallRef: ref, RingTimeoutS: 30, Metadata: map[string]string{"k": "v"},
		To: &roomrv1.SipTarget{Uri: "sip:+1555@carrier.invalid", Proxy: r.callee.Address()}}
	_, err = roomrv1.NewRoomrClient(conn).CreateCall(rpcCtx(t), req)
	if status.Code(err) != codes.DeadlineExceeded {
		t.Fatalf("first CreateCall = %v, want DeadlineExceeded", err)
	}
	r.waitEvent(typed("call.ringing", "call_ref", ref), 3*time.Second)

	again, err := r.mc.CreateCall(rpcCtx(t), req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.CallRef != ref || again.State != roomrv1.Call_RINGING {
		t.Fatalf("retry returned %v, want the ringing call %s", again, ref)
	}
	time.Sleep(300 * time.Millisecond) // a stray second INVITE would have arrived by now
	if n := len(r.callee.Invites()); n != 1 {
		t.Fatalf("callee saw %d INVITEs, want 1", n)
	}
	if _, err := r.mc.EndCall(rpcCtx(t), &roomrv1.EndCallRequest{CallRef: ref}); err != nil {
		t.Fatal(err)
	}
}
