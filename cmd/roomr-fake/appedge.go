package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	sessionv1 "github.com/fonoster/roomr/api/session/v1"
	"github.com/fonoster/roomr/internal/audio"
	"github.com/fonoster/roomr/internal/caller"
	"github.com/fonoster/roomr/internal/node"
	"github.com/pion/rtp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// The node's edge toward each call's app (FAK-004): on a node started with measureAppEdge, the
// fake stands a session proxy between the node and the app, and a UDP relay between their RTP
// endpoints, so it can stamp audible frames as they leave and reach the node. The node itself
// is unchanged: it dials the proxy as its session endpoint.

const (
	// sessionKeepalive is the node's default keepalive on control streams; the fake sets it on
	// opted-in nodes so the proxy's upstream connection uses the same.
	sessionKeepalive = 10 * time.Second
	// upstreamTimeout bounds the upstream dial, as the node bounds its own (sessionConnectTimeout).
	upstreamTimeout = 5 * time.Second
)

// edgeStatus is GET /calls/{call}'s app object (unix ms; 0: none).
type edgeStatus struct {
	FirstAudibleSentMs    int64 `json:"firstAudibleSentMs"`
	LastAudibleReceivedMs int64 `json:"lastAudibleReceivedMs"`
}

// sessionProxy relays every Session.Connect of one node to the call's real session endpoint.
type sessionProxy struct {
	sessionv1.UnimplementedSessionServer
	host, advertise string
	// fallback is the node's sessionEndpoint option, for calls without a CreateCall session.
	fallback string
	srv      *grpc.Server
	addr     string

	mu sync.Mutex
	// upstream is each outbound call's CreateCall session, by call_ref.
	upstream map[string]string
	relays   map[string]*relay
}

func startProxy(host, advertise, fallback string) (*sessionProxy, error) {
	l, err := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, fmt.Errorf("session proxy: %w", err)
	}
	_, port, _ := net.SplitHostPort(l.Addr().String())
	p := &sessionProxy{host: host, advertise: advertise, fallback: fallback,
		addr: net.JoinHostPort(advertise, port), upstream: map[string]string{}, relays: map[string]*relay{}}
	// The node pings every sessionKeepalive, even between streams.
	p.srv = grpc.NewServer(grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
		MinTime: sessionKeepalive / 2, PermitWithoutStream: true}))
	sessionv1.RegisterSessionServer(p.srv, p)
	go func() { _ = p.srv.Serve(l) }()
	return p, nil
}

func (p *sessionProxy) stop() { p.srv.Stop() }

// createCall points an outbound call's control stream at the proxy and keeps its own session
// as the upstream. The lock spans CreateCall so a Start never arrives before the route.
func (p *sessionProxy) createCall(n *node.Node, req node.CallRequest) (node.CallInfo, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	session := req.Session
	req.Session = p.addr
	info, err := n.CreateCall(req)
	if err == nil && session != "" {
		p.upstream[info.CallRef] = session
	}
	return info, err
}

func (p *sessionProxy) relay(callRef string) *relay {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.relays[callRef]
}

// Connect relays one call's control stream, in order and unbuffered, both ways.
func (p *sessionProxy) Connect(down sessionv1.Session_ConnectServer) error {
	first, err := down.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return errors.New("the first message is not Start")
	}
	p.mu.Lock()
	endpoint := p.fallback
	if e, ok := p.upstream[start.CallRef]; ok {
		endpoint = e
		delete(p.upstream, start.CallRef)
	}
	p.mu.Unlock()
	conn, err := dialUpstream(endpoint)
	if err != nil {
		// Closing before Answer: the node ends the call app_gone, as when it can't dial.
		return err
	}
	defer conn.Close()
	rl, err := newRelay(p.host, start.GetMedia())
	if err != nil {
		return err
	}
	defer rl.close()
	p.mu.Lock()
	p.relays[start.CallRef] = rl
	p.mu.Unlock()
	start.Media.Ip, start.Media.Port = p.advertise, uint32(rl.appPort())

	ctx, cancel := context.WithCancel(down.Context())
	defer cancel()
	up, err := sessionv1.NewSessionClient(conn).Connect(ctx)
	if err != nil {
		return err
	}
	if err := up.Send(first); err != nil {
		return err
	}
	go func() {
		for {
			m, err := down.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					_ = up.CloseSend()
				} else {
					cancel()
				}
				return
			}
			if up.Send(m) != nil {
				return
			}
		}
	}()
	for {
		m, err := up.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if media := appMedia(m); media != nil {
			rl.answered(media)
			media.Ip, media.Port = p.advertise, uint32(rl.nodePort())
		}
		if err := down.Send(m); err != nil {
			return err
		}
	}
}

// appMedia is the app's RTP endpoint in an Answer or Ready.
func appMedia(m *sessionv1.AppMessage) *sessionv1.MediaEndpoint {
	if a := m.GetAnswer(); a != nil {
		return a.GetMedia()
	}
	if r := m.GetReady(); r != nil {
		return r.GetMedia()
	}
	return nil
}

// dialUpstream connects as the node does (internal/node/session.go): insecure, the node's
// keepalive, failing fast when the endpoint refuses connections.
func dialUpstream(endpoint string) (*grpc.ClientConn, error) {
	if endpoint == "" {
		return nil, errors.New("no session endpoint configured")
	}
	conn, err := grpc.NewClient(endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: sessionKeepalive, Timeout: 2 * sessionKeepalive,
			PermitWithoutStream: true}))
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), upstreamTimeout)
	defer cancel()
	conn.Connect()
	for st := conn.GetState(); st != connectivity.Ready; st = conn.GetState() {
		if st == connectivity.TransientFailure || st == connectivity.Shutdown || !conn.WaitForStateChange(ctx, st) {
			_ = conn.Close()
			return nil, fmt.Errorf("session endpoint %s unreachable (%s)", endpoint, st)
		}
	}
	return conn, nil
}

// relay forwards one call's app-leg RTP byte for byte between the node and the app, each side on
// its own socket, and stamps audible frames on a plain RTP leg.
type relay struct {
	toNode, toApp net.PacketConn // facing the node, facing the app
	node          *net.UDPAddr   // the node's media, from Start

	mu sync.Mutex
	// app is the app's media from its Answer or Ready (nil until then: no app leg yet).
	app *net.UDPAddr
	// measure: a plain RTP leg; sentCodec and receivedCodec decode each direction.
	measure                  bool
	pt                       uint8
	sentCodec, receivedCodec audio.Codec
	// sent and received: when each audible frame went toward the app, and came from it.
	sent, received []time.Time
}

func newRelay(host string, nodeMedia *sessionv1.MediaEndpoint) (*relay, error) {
	toNode, err := net.ListenPacket("udp4", net.JoinHostPort(host, "0"))
	if err != nil {
		return nil, err
	}
	toApp, err := net.ListenPacket("udp4", net.JoinHostPort(host, "0"))
	if err != nil {
		_ = toNode.Close()
		return nil, err
	}
	rl := &relay{toNode: toNode, toApp: toApp,
		node: &net.UDPAddr{IP: net.ParseIP(nodeMedia.GetIp()), Port: int(nodeMedia.GetPort())}}
	go rl.forward(toNode, true)
	go rl.forward(toApp, false)
	return rl, nil
}

func (rl *relay) nodePort() int { return rl.toNode.LocalAddr().(*net.UDPAddr).Port }
func (rl *relay) appPort() int  { return rl.toApp.LocalAddr().(*net.UDPAddr).Port }

func (rl *relay) close() {
	_ = rl.toNode.Close()
	_ = rl.toApp.Close()
}

// answered sets the app's leg. The leg is SRTP when the app's media carries a key (attachApp,
// internal/node/call.go); the relay forwards those packets without measuring.
func (rl *relay) answered(m *sessionv1.MediaEndpoint) {
	codec, ok := audio.CodecFor(m.GetCodec())
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.app = &net.UDPAddr{IP: net.ParseIP(m.GetIp()), Port: int(m.GetPort())}
	rl.measure = ok && m.GetSrtpKey() == ""
	if rl.measure {
		// The node's payload type for the codec (defaultPayloadType, internal/node/room.go).
		pt := audio.StaticPayloadType(m.GetCodec())
		if pt < 0 {
			pt = 96
		}
		rl.pt = uint8(pt)
		rl.sentCodec = codec
		rl.receivedCodec, _ = audio.CodecFor(m.GetCodec())
	}
}

// forward reads one socket and sends what it reads on: from the node to the app (fromNode), or
// from the app to the node. Node-bound packets leave from the socket advertised to the node,
// because the node's endpoint latches to that source (internal/rtpio/endpoint.go).
func (rl *relay) forward(in net.PacketConn, fromNode bool) {
	buf := make([]byte, 2048)
	for {
		n, _, err := in.ReadFrom(buf)
		if err != nil {
			return
		}
		if fromNode {
			rl.mu.Lock()
			app := rl.app
			rl.mu.Unlock()
			if app == nil {
				continue
			}
			_, _ = rl.toApp.WriteTo(buf[:n], app)
		} else {
			_, _ = rl.toNode.WriteTo(buf[:n], rl.node)
		}
		rl.stamp(fromNode, buf[:n])
	}
}

// stamp records an audible frame on a plain RTP leg: sent toward the app, or received from it.
// Audible is the phone's rule (caller.Audible).
func (rl *relay) stamp(toApp bool, data []byte) {
	at := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if !rl.measure {
		return
	}
	var p rtp.Packet
	if p.Unmarshal(data) != nil || p.PayloadType != rl.pt {
		return
	}
	if toApp {
		if caller.Audible(rl.sentCodec.Decode(p.Payload)) {
			rl.sent = append(rl.sent, at)
		}
		return
	}
	if caller.Audible(rl.receivedCodec.Decode(p.Payload)) {
		rl.received = append(rl.received, at)
	}
}

// status is the first audible frame sent and the last received at or after since (the rule
// caller.StatusSince uses; zero since: the whole call). Nil while the call has no app leg.
func (rl *relay) status(since time.Time) *edgeStatus {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if rl.app == nil {
		return nil
	}
	st := &edgeStatus{}
	for _, at := range rl.sent {
		if !at.Before(since) {
			st.FirstAudibleSentMs = at.UnixMilli()
			break
		}
	}
	for _, at := range rl.received {
		if !at.Before(since) {
			st.LastAudibleReceivedMs = at.UnixMilli()
		}
	}
	return st
}
