package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	roomrv1 "github.com/fonoster/roomr/api/roomr/v1"
	sessionv1 "github.com/fonoster/roomr/api/session/v1"
	"github.com/fonoster/roomr/internal/audio"
	"github.com/fonoster/roomr/internal/rtpio"
	"github.com/nats-io/nats.go"
	"github.com/pion/rtp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// ---- a minimal session endpoint: answers inbound calls, is ready for outbound ones ----------

type app struct {
	sessionv1.UnimplementedSessionServer
	addr string
	// srtpKey, when set, goes in the app's Answer and Ready media (an SRTP app leg).
	srtpKey string
	// closeAfter, when set, ends each call's stream that long after the app answers.
	closeAfter time.Duration
	mu         sync.Mutex
	msgs       map[string][]*sessionv1.NodeMessage // by call_ref
	rtp        map[string]*atomic.Int64            // RTP packets received, by call_ref
	legs       map[string]appLeg                   // each call's RTP socket and Start media
}

// appLeg is where the app sends a call's RTP: from its socket to the media address in Start.
type appLeg struct {
	conn net.PacketConn
	to   *net.UDPAddr
}

func startApp(t *testing.T) *app {
	return startAppWith(t, &app{})
}

func startAppWith(t *testing.T, a *app) *app {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a.addr, a.msgs, a.rtp, a.legs = l.Addr().String(), map[string][]*sessionv1.NodeMessage{}, map[string]*atomic.Int64{}, map[string]appLeg{}
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
	media := &sessionv1.MediaEndpoint{Ip: "127.0.0.1", Port: uint32(rtp.LocalAddr().(*net.UDPAddr).Port), Codec: "L16/16000",
		SrtpKey: a.srtpKey}
	ref := ""
	for {
		m, err := stream.Recv()
		if err != nil {
			return nil
		}
		if s := m.GetStart(); s != nil {
			ref = s.CallRef
			count := &atomic.Int64{}
			to := &net.UDPAddr{IP: net.ParseIP(s.GetMedia().GetIp()), Port: int(s.GetMedia().GetPort())}
			a.mu.Lock()
			a.rtp[ref] = count
			a.legs[ref] = appLeg{conn: rtp, to: to}
			a.mu.Unlock()
			go func() {
				buf := make([]byte, 2048)
				for {
					if _, _, err := rtp.ReadFrom(buf); err != nil {
						return
					}
					count.Add(1)
				}
			}()
			reply := &sessionv1.AppMessage{Msg: &sessionv1.AppMessage_Ready{Ready: &sessionv1.Ready{Media: media}}}
			if s.Direction == sessionv1.Start_INBOUND {
				reply = &sessionv1.AppMessage{Msg: &sessionv1.AppMessage_Answer{Answer: &sessionv1.Answer{Media: media}}}
			}
			_ = stream.Send(reply)
			if a.closeAfter > 0 {
				time.Sleep(a.closeAfter)
				return nil
			}
		}
		a.mu.Lock()
		a.msgs[ref] = append(a.msgs[ref], m)
		a.mu.Unlock()
	}
}

// tone sends n audible L16 frames on a call's app leg, one every 20 ms, then stops.
func (a *app) tone(t *testing.T, ref string, n int) {
	t.Helper()
	a.mu.Lock()
	leg, ok := a.legs[ref]
	a.mu.Unlock()
	if !ok {
		t.Fatalf("call %s: no Start yet", ref)
	}
	f := audio.Silence()
	for j := range f {
		f[j] = int16(8000 * math.Sin(2*math.Pi*440*float64(j)/audio.Rate))
	}
	payload := audio.L16{}.Encode(f)
	ssrc := uint32(time.Now().UnixNano())
	// A ticker keeps the app at the node's 20 ms pace; sleeping drifts behind it.
	tick := time.NewTicker(audio.FrameMs * time.Millisecond)
	defer tick.Stop()
	for i := range n {
		pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: uint16(i),
			Timestamp: uint32(i * audio.FrameSamples), SSRC: ssrc}, Payload: payload}
		data, _ := pkt.Marshal()
		if _, err := leg.conn.WriteTo(data, leg.to); err != nil {
			t.Fatal(err)
		}
		<-tick.C
	}
}

// packets is how many RTP packets the app received on a call so far.
func (a *app) packets(ref string) int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c := a.rtp[ref]; c != nil {
		return c.Load()
	}
	return 0
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
	f   *fake
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
	return &rig{t: t, api: srv.URL, nc: nc, f: f}
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
		var cmds struct{ Commands []command }
		r.call("GET", "/calls/"+v.CallRef+"/commands", nil, &cmds)
		if len(cmds.Commands) == 0 || cmds.Commands[0].Command["type"] != "answer" {
			t.Fatalf("commands: %+v", cmds.Commands)
		}
	})

	t.Run("a call_ref is looked up on the node named", func(t *testing.T) {
		other := r.node(map[string]any{"sessionEndpoint": a.addr})
		for _, nodeRef := range []string{n.NodeRef, other.NodeRef} {
			if code := r.call("POST", "/nodes/"+nodeRef+"/calls", map[string]any{"callRef": "same"}, nil); code != 201 {
				t.Fatalf("POST calls on %s: %d", nodeRef, code)
			}
		}
		waitFor(t, func() bool {
			var v callView
			r.call("GET", "/calls/same?node="+other.NodeRef, nil, &v)
			return v.NodeRef == other.NodeRef && v.State == "answered"
		})
	})

	t.Run("a ringing callee answers on demand", func(t *testing.T) {
		r.call("POST", "/nodes/"+n.NodeRef+"/callee", map[string]any{"mode": "ring", "callRef": "c1"}, nil)
		// A proxy the fake cannot reach: the phone answers anyway.
		_, err := client(t, n.ControlAddr).CreateCall(ctx(t), &roomrv1.CreateCallRequest{CallRef: "c1",
			To: &roomrv1.SipTarget{Uri: "sip:+15551230000@carrier", Proxy: "sip:192.0.2.1:5060"}})
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
		var v callView
		r.call("GET", "/calls/c1", nil, &v)
		if v.State != "ended" || v.Cause != "node_error" {
			t.Fatalf("call after end: %+v", v)
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
		var got struct{ Requests []rpc }
		r.call("GET", "/nodes/"+n.NodeRef+"/requests?method=CreateCall", nil, &got)
		if len(got.Requests) != 2 || got.Requests[0].Request["callRef"] != "f1" {
			t.Fatalf("recorded requests: %+v", got.Requests)
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

	t.Run("the node's RTP to the app stops on demand", func(t *testing.T) {
		var v callView
		r.call("POST", "/nodes/"+n.NodeRef+"/calls", map[string]any{"callRef": "quiet"}, &v)
		waitFor(t, func() bool { return a.packets("quiet") > 5 })
		if code := r.call("POST", "/calls/quiet/app-media", map[string]any{"stopped": true}, nil); code != 204 {
			t.Fatalf("app-media: %d", code)
		}
		time.Sleep(100 * time.Millisecond)
		before := a.packets("quiet")
		time.Sleep(300 * time.Millisecond)
		if got := a.packets("quiet"); got != before {
			t.Fatalf("the app still got %d packets", got-before)
		}
		r.call("GET", "/calls/quiet", nil, &v)
		if v.State != "answered" {
			t.Fatalf("the call did not go on: %q", v.State)
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

// ---- FAK-004: the node's edge toward the app -------------------------------------------------

// answered places an inbound call and waits until the app answered it.
func (r *rig) answered(t *testing.T, nodeRef, callRef string) {
	t.Helper()
	if code := r.call("POST", "/nodes/"+nodeRef+"/calls", map[string]any{"callRef": callRef}, nil); code != 201 {
		t.Fatalf("POST calls: %d", code)
	}
	waitFor(t, func() bool {
		var v callView
		r.call("GET", "/calls/"+callRef, nil, &v)
		return v.State == "answered"
	})
}

func (r *rig) status(callRef string, since int64) callView {
	var v callView
	r.call("GET", fmt.Sprintf("/calls/%s?since=%d", callRef, since), nil, &v)
	return v
}

func TestFAK004_TheFakeReportsTheNodesEdgeTowardTheApp(t *testing.T) {
	r := startFake(t)
	a := startApp(t)
	n := r.node(map[string]any{"sessionEndpoint": a.addr, "measureAppEdge": true})

	t.Run("the node sends the phone's speech to the app before the phone stops", func(t *testing.T) {
		r.answered(t, n.NodeRef, "edge")
		start := a.await(t, "edge", func(m *sessionv1.NodeMessage) bool { return m.GetStart() != nil }).GetStart()
		if relay := r.f.node(n.NodeRef).proxy.relay("edge"); relay == nil || int(start.Media.Port) != relay.appPort() {
			t.Fatalf("Start.media %v is not the relay's address", start.Media)
		}
		waitFor(t, func() bool { return a.packets("edge") > 5 })
		since := time.Now().UnixMilli()
		r.call("POST", "/calls/edge/audio", map[string]any{"kind": "tone", "ms": 400}, nil)
		var v callView
		waitFor(t, func() bool {
			v = r.status("edge", since)
			return v.Phone != nil && v.Phone.SpokeUntil > 0
		})
		if v.App == nil || !(since < v.App.FirstAudibleSentMs && v.App.FirstAudibleSentMs < v.Phone.SpokeUntil) {
			t.Fatalf("since %d, app %+v, spokeUntil %d", since, v.App, v.Phone.SpokeUntil)
		}
		t.Logf("firstAudibleSentMs is %d ms before phone.spokeUntilMs", v.Phone.SpokeUntil-v.App.FirstAudibleSentMs)
	})

	t.Run("the node receives the app's speech before the phone hears it", func(t *testing.T) {
		since := time.Now().UnixMilli()
		a.tone(t, "edge", 20)
		time.Sleep(600 * time.Millisecond)
		v := r.status("edge", since)
		if v.App == nil || v.Phone == nil || v.App.LastAudibleReceivedMs == 0 || v.Phone.LastAudibleAfter == 0 {
			t.Fatalf("app %+v, phone %+v", v.App, v.Phone)
		}
		gap := since + int64(v.Phone.LastAudibleAfter) - v.App.LastAudibleReceivedMs
		if gap < 20 || gap > 500 {
			t.Fatalf("the phone heard the app's last audible frame %d ms after the relay", gap)
		}
		t.Logf("phone's last audible frame is %d ms after lastAudibleReceivedMs", gap)
	})

	t.Run("since bounds both times", func(t *testing.T) {
		if v := r.status("edge", time.Now().UnixMilli()); v.App == nil || v.App.FirstAudibleSentMs != 0 || v.App.LastAudibleReceivedMs != 0 {
			t.Fatalf("after every audible frame: %+v", v.App)
		}
		var v callView
		r.call("GET", "/calls/edge", nil, &v)
		if v.App == nil || v.App.FirstAudibleSentMs == 0 || v.App.LastAudibleReceivedMs == 0 {
			t.Fatalf("the whole call: %+v", v.App)
		}
	})

	t.Run("nothing audible reaches the app while the node's RTP is stopped", func(t *testing.T) {
		if code := r.call("POST", "/nodes/"+n.NodeRef+"/calls", map[string]any{"callRef": "stopped"}, nil); code != 201 {
			t.Fatalf("POST calls: %d", code)
		}
		waitFor(t, func() bool {
			return r.call("POST", "/calls/stopped/app-media", map[string]any{"stopped": true}, nil) == 204
		})
		since := time.Now().UnixMilli()
		r.call("POST", "/calls/stopped/audio", map[string]any{"kind": "tone", "ms": 300}, nil)
		var v callView
		waitFor(t, func() bool {
			v = r.status("stopped", since)
			return v.Phone != nil && v.Phone.SpokeUntil > 0
		})
		if v.App == nil || v.App.FirstAudibleSentMs != 0 {
			t.Fatalf("app %+v", v.App)
		}
	})

	t.Run("an outbound call still ringing has no app leg", func(t *testing.T) {
		r.call("POST", "/nodes/"+n.NodeRef+"/callee", map[string]any{"mode": "ring", "callRef": "ringing"}, nil)
		if _, err := client(t, n.ControlAddr).CreateCall(ctx(t), &roomrv1.CreateCallRequest{CallRef: "ringing",
			To: &roomrv1.SipTarget{Uri: "sip:+15551230000@carrier"}}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		var v callView
		r.call("GET", "/calls/ringing", nil, &v)
		if v.State != "ringing" || v.App != nil {
			t.Fatalf("ringing call: %+v", v)
		}
		// Answer it: a node stopped while it rings out can crash in sipgo's CANCEL.
		waitFor(t, func() bool { return r.call("POST", "/calls/ringing/answer", nil, nil) == 204 })
		waitFor(t, func() bool { return r.status("ringing", 0).App != nil })
	})

	t.Run("a CreateCall session is reached through the proxy", func(t *testing.T) {
		own := startApp(t)
		if _, err := client(t, n.ControlAddr).CreateCall(ctx(t), &roomrv1.CreateCallRequest{CallRef: "own",
			To: &roomrv1.SipTarget{Uri: "sip:+15551230000@carrier"}, Session: own.addr}); err != nil {
			t.Fatal(err)
		}
		own.await(t, "own", func(m *sessionv1.NodeMessage) bool { return m.GetStart() != nil })
		waitFor(t, func() bool {
			return r.call("POST", "/calls/own/audio", map[string]any{"kind": "tone", "ms": 200}, nil) == 204
		})
		waitFor(t, func() bool {
			v := r.status("own", 0)
			return v.App != nil && v.App.FirstAudibleSentMs > 0
		})
	})

	t.Run("an app that closes its stream ends the call as without the proxy", func(t *testing.T) {
		closing := startAppWith(t, &app{closeAfter: 200 * time.Millisecond})
		causes := map[bool]string{}
		for _, measured := range []bool{false, true} {
			m := r.node(map[string]any{"sessionEndpoint": closing.addr, "measureAppEdge": measured})
			ref := fmt.Sprintf("closing-%t", measured)
			r.call("POST", "/nodes/"+m.NodeRef+"/calls", map[string]any{"callRef": ref}, nil)
			waitFor(t, func() bool {
				var v callView
				r.call("GET", "/calls/"+ref, nil, &v)
				causes[measured] = v.Cause
				return v.State == "ended"
			})
		}
		if causes[true] != causes[false] || causes[true] != "app_gone" {
			t.Fatalf("causes: %v", causes)
		}
	})

	t.Run("an SRTP app leg works and reports 0", func(t *testing.T) {
		secure := startAppWith(t, &app{srtpKey: rtpio.NewSRTPKey()})
		m := r.node(map[string]any{"sessionEndpoint": secure.addr, "measureAppEdge": true})
		r.answered(t, m.NodeRef, "secure")
		waitFor(t, func() bool { return secure.packets("secure") > 5 })
		since := time.Now().UnixMilli()
		r.call("POST", "/calls/secure/audio", map[string]any{"kind": "tone", "ms": 300}, nil)
		var v callView
		waitFor(t, func() bool {
			v = r.status("secure", since)
			return v.Phone != nil && v.Phone.SpokeUntil > 0
		})
		if v.State != "answered" || v.App == nil || v.App.FirstAudibleSentMs != 0 || v.App.LastAudibleReceivedMs != 0 {
			t.Fatalf("SRTP call: %+v, app %+v", v, v.App)
		}
	})

	t.Run("an unreachable upstream ends the call app_gone with 503", func(t *testing.T) {
		l, _ := net.Listen("tcp", "127.0.0.1:0")
		gone := l.Addr().String()
		_ = l.Close()
		m := r.node(map[string]any{"sessionEndpoint": gone, "measureAppEdge": true})
		r.call("POST", "/nodes/"+m.NodeRef+"/calls", map[string]any{"callRef": "nowhere"}, nil)
		var v callView
		waitFor(t, func() bool {
			r.call("GET", "/calls/nowhere", nil, &v)
			return v.State == "ended" && v.Phone != nil && slices.Contains(v.Phone.SIP, 503)
		})
		if v.Cause != "app_gone" || v.SipStatus != 503 || !slices.Contains(v.Phone.SIP, 180) {
			t.Fatalf("call: %+v, phone %+v", v, v.Phone)
		}
	})

	t.Run("a node without measureAppEdge reports no app and gets no proxy", func(t *testing.T) {
		plain := r.node(map[string]any{"sessionEndpoint": a.addr})
		r.answered(t, plain.NodeRef, "plain")
		var v callView
		r.call("GET", "/calls/plain", nil, &v)
		if v.App != nil || r.f.node(plain.NodeRef).proxy != nil {
			t.Fatalf("app %+v on a node without measureAppEdge", v.App)
		}
	})
}

// The relay's stamping, without a node (FAK-004).
func TestFAK004_RelayStampsTheFirstAudibleFrame(t *testing.T) {
	rl := &relay{}
	rl.answered(&sessionv1.MediaEndpoint{Codec: audio.NameL16})
	packet := func(f audio.Frame) []byte {
		data, _ := (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96}, Payload: audio.L16{}.Encode(f)}).Marshal()
		return data
	}
	loud := audio.Silence()
	for j := range loud {
		loud[j] = int16(8000 * math.Sin(2*math.Pi*440*float64(j)/audio.Rate))
	}
	for range 5 {
		rl.stamp(true, packet(audio.Silence()))
	}
	if st := rl.status(time.Time{}); st.FirstAudibleSentMs != 0 {
		t.Fatalf("silence was stamped: %+v", st)
	}
	rl.stamp(true, packet(loud))
	first := rl.status(time.Time{}).FirstAudibleSentMs
	if first == 0 {
		t.Fatal("the audible frame was not stamped")
	}
	time.Sleep(5 * time.Millisecond)
	rl.stamp(true, packet(loud))
	if got := rl.status(time.Time{}).FirstAudibleSentMs; got != first {
		t.Fatalf("the first-sent time moved from %d to %d", first, got)
	}
}
