package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	roomrv1 "github.com/fonoster/roomr/api/roomr/v1"
	sessionv1 "github.com/fonoster/roomr/api/session/v1"
	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---- a minimal session endpoint: answers inbound calls, is ready for outbound ones ----------

type app struct {
	sessionv1.UnimplementedSessionServer
	addr string
	mu   sync.Mutex
	msgs map[string][]*sessionv1.NodeMessage // by call_ref
}

func startApp(t *testing.T) *app {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{addr: l.Addr().String(), msgs: map[string][]*sessionv1.NodeMessage{}}
	s := grpc.NewServer()
	sessionv1.RegisterSessionServer(s, a)
	go func() { _ = s.Serve(l) }()
	t.Cleanup(s.Stop)
	return a
}

func (a *app) Connect(stream sessionv1.Session_ConnectServer) error {
	rtp, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer rtp.Close()
	media := &sessionv1.MediaEndpoint{Ip: "127.0.0.1", Port: uint32(rtp.LocalAddr().(*net.UDPAddr).Port), Codec: "L16/16000"}
	ref := ""
	for {
		m, err := stream.Recv()
		if err != nil {
			return nil
		}
		if s := m.GetStart(); s != nil {
			ref = s.CallRef
			reply := &sessionv1.AppMessage{Msg: &sessionv1.AppMessage_Ready{Ready: &sessionv1.Ready{Media: media}}}
			if s.Direction == sessionv1.Start_INBOUND {
				reply = &sessionv1.AppMessage{Msg: &sessionv1.AppMessage_Answer{Answer: &sessionv1.Answer{Media: media}}}
			}
			_ = stream.Send(reply)
		}
		a.mu.Lock()
		a.msgs[ref] = append(a.msgs[ref], m)
		a.mu.Unlock()
	}
}

// await returns the first message of a call that ok accepts.
func (a *app) await(t *testing.T, ref string, ok func(*sessionv1.NodeMessage) bool) *sessionv1.NodeMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		for _, m := range a.msgs[ref] {
			if ok(m) {
				a.mu.Unlock()
				return m
			}
		}
		a.mu.Unlock()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("call %s: no such message", ref)
	return nil
}

// ---- the fake under test --------------------------------------------------------------------

type rig struct {
	t   *testing.T
	api string
	nc  *nats.Conn
}

func startFake(t *testing.T) *rig {
	url := os.Getenv("CONFORMANCE_NATS_URL")
	if url == "" {
		t.Skip("set CONFORMANCE_NATS_URL to run")
	}
	nc, err := nats.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	f := newFake("127.0.0.1", "127.0.0.1", nc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(f.routes())
	t.Cleanup(func() {
		for _, fn := range f.all() {
			fn.stop()
		}
		srv.Close()
		nc.Close()
	})
	return &rig{t: t, api: srv.URL, nc: nc}
}

func (r *rig) call(method, path string, body any, out any) int {
	r.t.Helper()
	var buf io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		buf = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, r.api+path, buf)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil {
		_ = json.NewDecoder(res.Body).Decode(out)
	}
	return res.StatusCode
}

func (r *rig) node(o map[string]any) nodeView {
	r.t.Helper()
	var v nodeView
	if code := r.call("POST", "/nodes", o, &v); code != 201 {
		r.t.Fatalf("POST /nodes: %d", code)
	}
	return v
}

func client(t *testing.T, addr string) roomrv1.RoomrClient {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return roomrv1.NewRoomrClient(conn)
}

// heartbeat waits for the next heartbeat-subject message of a node.
func (r *rig) heartbeat(ref string, within time.Duration) map[string]any {
	r.t.Helper()
	sub, err := r.nc.SubscribeSync("roomr.nodes." + ref + ".heartbeat")
	if err != nil {
		r.t.Fatal(err)
	}
	defer sub.Unsubscribe()
	m, err := sub.NextMsg(within)
	if err != nil {
		return nil
	}
	var ev map[string]any
	_ = json.Unmarshal(m.Data, &ev)
	return ev
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestFAK001_RealNodesBehindTheControlAPI(t *testing.T) {
	r := startFake(t)
	a := r.node(map[string]any{"nodeRef": fmt.Sprintf("a-%d", time.Now().UnixNano()), "maxRooms": 5, "heartbeatMs": 100})
	b := r.node(map[string]any{"nodeRef": fmt.Sprintf("b-%d", time.Now().UnixNano()), "maxRooms": 10, "heartbeatMs": 100})
	if a.ControlAddr == b.ControlAddr {
		t.Fatal("two nodes share a control address")
	}
	for _, n := range []struct {
		v   nodeView
		max float64
	}{{a, 5}, {b, 10}} {
		hb := r.heartbeat(n.v.NodeRef, 2*time.Second)
		if hb == nil || hb["max_rooms"] != n.max || hb["control_addr"] != n.v.ControlAddr {
			t.Fatalf("heartbeat of %s: %v", n.v.NodeRef, hb)
		}
		if _, err := client(t, n.v.ControlAddr).CreateRoom(ctx(t), &roomrv1.CreateRoomRequest{}); err != nil {
			t.Fatalf("CreateRoom on %s: %v", n.v.NodeRef, err)
		}
	}

	t.Run("pre-filled rooms count toward the limit", func(t *testing.T) {
		full := r.node(map[string]any{"maxRooms": 2, "rooms": 2, "heartbeatMs": 100})
		if hb := r.heartbeat(full.NodeRef, 2*time.Second); hb == nil || hb["rooms"] != float64(2) {
			t.Fatalf("heartbeat: %v", hb)
		}
		_, err := client(t, full.ControlAddr).CreateRoom(ctx(t), &roomrv1.CreateRoomRequest{})
		if status.Code(err) != codes.ResourceExhausted {
			t.Fatalf("CreateRoom on a full node: %v", err)
		}
	})
}

func TestFAK002_ThePhoneCallsAndAnswersLikeAPhone(t *testing.T) {
	r := startFake(t)
	a := startApp(t)
	n := r.node(map[string]any{"sessionEndpoint": a.addr})

	t.Run("an inbound call reaches the session endpoint", func(t *testing.T) {
		var v callView
		if code := r.call("POST", "/nodes/"+n.NodeRef+"/calls", map[string]any{"headers": map[string]string{"X-App-Ref": "app-1"}}, &v); code != 201 {
			t.Fatalf("POST calls: %d", code)
		}
		start := a.await(t, v.CallRef, func(m *sessionv1.NodeMessage) bool { return m.GetStart() != nil }).GetStart()
		if start.Direction != sessionv1.Start_INBOUND || start.Headers["X-App-Ref"] != "app-1" {
			t.Fatalf("Start: %v", start)
		}
		waitFor(t, func() bool {
			r.call("GET", "/calls/"+v.CallRef, nil, &v)
			return v.Phone != nil && slices.Contains(v.Phone.SIP, 180) && slices.Contains(v.Phone.SIP, 200)
		})
	})

	t.Run("a ringing callee answers on demand", func(t *testing.T) {
		r.call("POST", "/nodes/"+n.NodeRef+"/callee", map[string]any{"mode": "ring", "callRef": "c1"}, nil)
		_, err := client(t, n.ControlAddr).CreateCall(ctx(t), &roomrv1.CreateCallRequest{CallRef: "c1",
			To: &roomrv1.SipTarget{Uri: "sip:+15551230000@carrier"}})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		var v callView
		r.call("GET", "/calls/c1", nil, &v)
		if v.State != "ringing" {
			t.Fatalf("state before answer: %q", v.State)
		}
		waitFor(t, func() bool { return r.call("POST", "/calls/c1/answer", nil, nil) == 204 })
		start := a.await(t, "c1", func(m *sessionv1.NodeMessage) bool { return m.GetStart() != nil }).GetStart()
		if start.Direction != sessionv1.Start_OUTBOUND {
			t.Fatalf("Start: %v", start)
		}
	})

	t.Run("the node ends a call with a scripted cause", func(t *testing.T) {
		waitFor(t, func() bool {
			var v callView
			r.call("GET", "/calls/c1", nil, &v)
			return v.State == "answered"
		})
		if code := r.call("POST", "/calls/c1/end", map[string]any{"cause": "node_error"}, nil); code != 204 {
			t.Fatalf("end: %d", code)
		}
		ended := a.await(t, "c1", func(m *sessionv1.NodeMessage) bool { return m.GetEvent().GetEnded() != nil })
		if c := ended.GetEvent().GetEnded().Cause; c != "node_error" {
			t.Fatalf("Ended cause %q", c)
		}
	})
}

func TestFAK003_ScriptedFaults(t *testing.T) {
	r := startFake(t)
	a := startApp(t)
	n := r.node(map[string]any{"sessionEndpoint": a.addr, "heartbeatMs": 100})
	c := client(t, n.ControlAddr)

	t.Run("the next CreateCall fails", func(t *testing.T) {
		r.call("POST", "/nodes/"+n.NodeRef+"/faults", map[string]any{"method": "CreateCall", "code": "UNAVAILABLE"}, nil)
		req := &roomrv1.CreateCallRequest{CallRef: "f1", To: &roomrv1.SipTarget{Uri: "sip:+1555@carrier"}}
		if _, err := c.CreateCall(ctx(t), req); status.Code(err) != codes.Unavailable {
			t.Fatalf("first CreateCall: %v", err)
		}
		if _, err := c.CreateCall(ctx(t), req); err != nil {
			t.Fatalf("second CreateCall: %v", err)
		}
	})

	t.Run("a stalled CreateRoom is served late", func(t *testing.T) {
		r.call("POST", "/nodes/"+n.NodeRef+"/faults", map[string]any{"method": "CreateRoom", "stallMs": 400}, nil)
		began := time.Now()
		if _, err := c.CreateRoom(ctx(t), &roomrv1.CreateRoomRequest{}); err != nil || time.Since(began) < 400*time.Millisecond {
			t.Fatalf("stalled CreateRoom: %v after %v", err, time.Since(began))
		}
	})

	t.Run("heartbeats report the scripted room count", func(t *testing.T) {
		r.call("PATCH", "/nodes/"+n.NodeRef, map[string]any{"reportedRooms": 0}, nil)
		if hb := r.heartbeat(n.NodeRef, 2*time.Second); hb == nil || hb["rooms"] != float64(0) {
			t.Fatalf("heartbeat: %v", hb)
		}
	})

	t.Run("a killed node goes silent", func(t *testing.T) {
		sub, _ := r.nc.SubscribeSync("roomr.nodes." + n.NodeRef + ".heartbeat")
		defer sub.Unsubscribe()
		r.call("POST", "/nodes/"+n.NodeRef+"/kill", nil, nil)
		_ = r.nc.Flush()
		// Drain what was in flight, then nothing more may arrive.
		time.Sleep(150 * time.Millisecond)
		for {
			m, err := sub.NextMsg(10 * time.Millisecond)
			if err != nil {
				break
			}
			if bytes.Contains(m.Data, []byte("node.down")) {
				t.Fatal("a killed node published node.down")
			}
		}
		if m, err := sub.NextMsg(500 * time.Millisecond); err == nil {
			t.Fatalf("heartbeat after kill: %s", m.Data)
		}
		if _, err := net.DialTimeout("tcp", n.ControlAddr, 300*time.Millisecond); err == nil {
			t.Fatal("the killed node's control address still accepts connections")
		}
	})
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
