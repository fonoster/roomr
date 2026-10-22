package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fonoster/roomr/internal/caller"
	"github.com/fonoster/roomr/internal/control"
	"github.com/fonoster/roomr/internal/node"
	"github.com/fonoster/roomr/internal/sipua"
	"github.com/nats-io/nats.go"
)

const (
	// callRefHeader carries an inbound call's call_ref, and roomHeader the room it joins: the fake
	// configures its nodes with them, so a test names its calls.
	callRefHeader = "X-Call-Ref"
	roomHeader    = "X-Room-Ref"
	// dialRefHeader tags the node's outbound INVITEs with their call_ref, so the phone can answer
	// each as scripted (refDialer).
	dialRefHeader = "X-Fake-Call-Ref"
	// forever keeps pre-filled rooms up for the life of the node.
	forever = 10 * 365 * 24 * time.Hour
)

// fake holds the nodes of one roomr-fake process.
type fake struct {
	host, advertise string
	nc              *nats.Conn
	logger          *slog.Logger

	mu    sync.Mutex
	nodes map[string]*fakeNode
}

func newFake(host, advertise string, nc *nats.Conn, logger *slog.Logger) *fake {
	return &fake{host: host, advertise: advertise, nc: nc, logger: logger, nodes: map[string]*fakeNode{}}
}

// fakeNode is one real node, its Roomr gRPC server and its phone.
type fakeNode struct {
	ref                  string
	n                    *node.Node
	ua                   *sipua.UA
	ctl                  *control.Server
	phone                *caller.Bot
	faults               *faults
	cancel               context.CancelFunc
	controlAddr, sipAddr string
	// reported is the room count heartbeats report (-1: the real one).
	reported atomic.Int64

	mu sync.Mutex
	// inbound maps the call_ref of each call the phone placed to the phone's call id.
	inbound map[string]string
}

// nodeOptions is POST /nodes. Durations are milliseconds; zero keeps the node's default.
type nodeOptions struct {
	NodeRef            string
	Region             string
	MaxRooms           int
	MaxParticipants    int
	Rooms              int
	ReportedRooms      *int
	HeartbeatMs        int
	SessionEndpoint    string
	AnswerTimeoutMs    int
	ReadyTimeoutMs     int
	MediaTimeoutMs     int
	EmptyRoomTimeoutMs int
	MaxDurationMs      int
	Codecs             []string
	Callee             *calleeOptions
}

// calleeOptions scripts how the phone answers what the node dials: for every call, or for one
// call_ref.
type calleeOptions struct {
	Mode    string
	AfterMs int
	CallRef string
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func newRef(prefix string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return prefix + "-" + hex.EncodeToString(b)
}

var errNodeExists = errors.New("a node with that ref exists")

func (f *fake) createNode(o nodeOptions) (*fakeNode, error) {
	if o.NodeRef == "" {
		o.NodeRef = newRef("fake")
	}
	f.mu.Lock()
	if f.nodes[o.NodeRef] != nil {
		f.mu.Unlock()
		return nil, errNodeExists
	}
	f.nodes[o.NodeRef] = nil // reserved while it starts
	f.mu.Unlock()
	fn, err := f.startNode(o)
	f.mu.Lock()
	if err != nil {
		delete(f.nodes, o.NodeRef)
	} else {
		f.nodes[o.NodeRef] = fn
	}
	f.mu.Unlock()
	return fn, err
}

func (f *fake) startNode(o nodeOptions) (*fakeNode, error) {
	phone, err := caller.NewWithOptions(caller.Options{Host: f.host})
	if err != nil {
		return nil, fmt.Errorf("phone: %w", err)
	}
	phone.KeyHeader(dialRefHeader)
	fn := &fakeNode{ref: o.NodeRef, phone: phone, faults: newFaults(), inbound: map[string]string{}}
	fn.reported.Store(-1)
	if o.ReportedRooms != nil {
		fn.reported.Store(int64(*o.ReportedRooms))
	}
	if c := o.Callee; c != nil {
		fn.script(*c)
	}
	backend := &faultyBackend{f: fn.faults}
	ctl, err := control.Listen(net.JoinHostPort(f.host, "0"), backend, nil, f.logger)
	if err != nil {
		phone.Close()
		return nil, fmt.Errorf("roomr grpc: %w", err)
	}
	_, grpcPort, _ := net.SplitHostPort(ctl.Addr())
	sipPort, err := freePort(f.host)
	if err != nil {
		ctl.Abort()
		phone.Close()
		return nil, err
	}
	fn.ctl = ctl
	fn.controlAddr = net.JoinHostPort(f.advertise, grpcPort)
	fn.sipAddr = net.JoinHostPort(f.host, strconv.Itoa(sipPort))
	cfg := node.Config{NodeID: o.NodeRef, RTPHost: f.host, AdvertiseHost: f.advertise,
		Heartbeat: ms(o.HeartbeatMs), MaxRooms: o.MaxRooms, MaxParticipants: o.MaxParticipants,
		MediaTimeout: ms(o.MediaTimeoutMs), MaxDuration: ms(o.MaxDurationMs),
		EmptyRoomTimeout: ms(o.EmptyRoomTimeoutMs), AnswerTimeout: ms(o.AnswerTimeoutMs),
		ReadyTimeout: ms(o.ReadyTimeoutMs), SessionEndpoint: o.SessionEndpoint, Codecs: o.Codecs,
		CallRefHeader: callRefHeader, RoomHeader: roomHeader, Version: version, Region: o.Region,
		OutboundProxy: "sip:" + phone.Address(), Logger: f.logger,
		ControlAddr: fn.controlAddr, SIPAddr: net.JoinHostPort(f.advertise, strconv.Itoa(sipPort)),
		ReportRooms: func(actual int) int {
			if v := fn.reported.Load(); v >= 0 {
				return int(v)
			}
			return actual
		}}
	fn.n = node.New(f.nc, cfg)
	backend.n = fn.n
	ua, err := sipua.New(fn.n, sipua.Config{Listen: fn.sipAddr, AdvertiseHost: f.host, AdvertisePort: sipPort,
		OutboundProxy: cfg.OutboundProxy, Logger: f.logger})
	if err != nil {
		ctl.Abort()
		phone.Close()
		return nil, fmt.Errorf("sip: %w", err)
	}
	fn.ua = ua
	fn.n.SetDialer(refDialer{ua})
	ctx, cancel := context.WithCancel(context.Background())
	fn.cancel = cancel
	go func() { _ = ua.Serve(ctx) }()
	go func() { _ = ctl.Serve() }()
	for range o.Rooms {
		if _, err := fn.n.CreateRoom("", node.RoomOptions{EmptyTimeout: forever, MaxDuration: forever}); err != nil {
			fn.stop()
			return nil, fmt.Errorf("pre-filled rooms: %w", err)
		}
	}
	_ = fn.n.Start()
	return fn, nil
}

// freePort is a port free for both UDP and TCP on host (SIP listens on both).
func freePort(host string) (int, error) {
	for range 20 {
		l, err := net.Listen("tcp4", net.JoinHostPort(host, "0"))
		if err != nil {
			return 0, err
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, err := net.ListenPacket("udp4", net.JoinHostPort(host, strconv.Itoa(port)))
		_ = l.Close()
		if err == nil {
			_ = u.Close()
			return port, nil
		}
	}
	return 0, errors.New("no port free for both UDP and TCP")
}

func (f *fake) node(ref string) *fakeNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nodes[ref]
}

func (f *fake) remove(ref string) *fakeNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn := f.nodes[ref]
	if fn != nil {
		delete(f.nodes, ref)
	}
	return fn
}

func (f *fake) all() []*fakeNode {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*fakeNode, 0, len(f.nodes))
	for _, fn := range f.nodes {
		if fn != nil {
			out = append(out, fn)
		}
	}
	return out
}

// script sets how the phone answers: one call_ref, or every call.
func (fn *fakeNode) script(c calleeOptions) {
	if c.Mode == "" {
		c.Mode = "answer"
	}
	if c.CallRef != "" {
		fn.phone.CalleeFor(dialRefHeader, c.CallRef, c.Mode, ms(c.AfterMs))
		return
	}
	fn.phone.Callee(c.Mode, ms(c.AfterMs))
}

// down drains the node: node.down, health NOT_SERVING; its rooms keep running.
func (fn *fakeNode) down() {
	fn.ctl.NotServing()
	fn.n.Down()
}

// stop ends the node gracefully: node.down, every call ends with node_error, then it closes.
func (fn *fakeNode) stop() {
	fn.ctl.NotServing()
	fn.n.Shutdown()
	fn.ctl.Abort()
	fn.cancel()
	fn.ua.Close()
	fn.phone.Close()
}

// kill stops the node as a crash would: no node.down, no Ended, gRPC refuses at once.
func (fn *fakeNode) kill() {
	fn.n.Kill()
	fn.ctl.Abort()
	fn.cancel()
	fn.ua.Close()
	fn.phone.Close()
}

// inboundCall is POST /nodes/{ref}/calls: the phone calls the node.
type inboundCall struct {
	From, To, CallRef, RoomRef string
	Headers                    map[string]string
	Codecs                     []string
}

func (fn *fakeNode) placeCall(req inboundCall) (string, error) {
	if req.CallRef == "" {
		req.CallRef = newRef("call")
	}
	if req.To == "" {
		req.To = "+15550002222"
	}
	headers := map[string]string{callRefHeader: req.CallRef}
	for k, v := range req.Headers {
		headers[k] = v
	}
	if req.RoomRef != "" {
		headers[roomHeader] = req.RoomRef
	}
	c, err := fn.phone.DialWith(fn.sipAddr, req.To, req.From, headers, caller.DialOptions{Codecs: req.Codecs})
	if err != nil {
		return "", err
	}
	fn.mu.Lock()
	fn.inbound[req.CallRef] = c.ID
	fn.mu.Unlock()
	return req.CallRef, nil
}

// phoneCall is the phone's side of a call: the caller of an inbound call, or the callee that
// answered an outbound one (nil while it rings or when it never answered).
func (fn *fakeNode) phoneCall(callRef string) (*caller.Call, string) {
	fn.mu.Lock()
	id, ok := fn.inbound[callRef]
	fn.mu.Unlock()
	if ok {
		return fn.phone.Get(id), "inbound"
	}
	for _, st := range fn.phone.Calls() {
		for k, v := range st.Headers {
			if strings.EqualFold(k, dialRefHeader) && v == callRef {
				return fn.phone.Get(st.ID), "outbound"
			}
		}
	}
	return nil, "outbound"
}

// refDialer tags the node's outbound INVITEs with their call_ref (dialRefHeader).
type refDialer struct{ inner node.TargetDialer }

func (d refDialer) DialTarget(ctx context.Context, t node.SipTarget, from string, offer []byte,
	onProgress func(status int)) (node.OutLeg, []byte, error) {
	if ref := node.CallRefOf(ctx); ref != "" {
		headers := make(map[string]string, len(t.Headers)+1)
		for k, v := range t.Headers {
			headers[k] = v
		}
		headers[dialRefHeader] = ref
		t.Headers = headers
	}
	return d.inner.DialTarget(ctx, t, from, offer, onProgress)
}
