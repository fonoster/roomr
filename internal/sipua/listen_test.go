package sipua_test

// Requests from the sip.listen port (roomr ROM-033, "Registration is optional and configured"):
// with a wildcard or a specific sip.listen, TCP and TLS requests share one connection per peer
// from the listen port, and UDP requests and ACKs leave from the listening socket.

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/fonoster/roomr/internal/node"
	"github.com/fonoster/roomr/internal/sipua"
)

// peerMsg is one message the peer received, with the index of the connection it came on (UDP:
// always 0) and its source address.
type peerMsg struct {
	conn int
	src  string
	msg  sip.Message
}

// peer is a fake registrar and proxy on 127.0.0.1: REGISTER gets a digest challenge, then 200
// once it carries Authorization; INVITE and BYE get 200.
type peer struct {
	t         *testing.T
	transport string
	addr      string
	tlsConfig *tls.Config
	// closeAfterOK closes the first connection after its first 200 to a REGISTER.
	closeAfterOK bool

	mu    sync.Mutex
	conns []net.Conn
	srcs  []string
	msgs  []peerMsg
	oks   []int // 200s to REGISTERs, by connection
	udp   net.PacketConn
}

func startPeer(t *testing.T, transport string) *peer {
	t.Helper()
	p := &peer{t: t, transport: transport}
	if transport == "udp" {
		c, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		p.udp, p.addr, p.oks = c, c.LocalAddr().String(), []int{0}
		t.Cleanup(func() { _ = c.Close() })
		go p.serveUDP()
		return p
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p.addr = l.Addr().String()
	if transport == "tls" {
		p.tlsConfig = &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}
	}
	t.Cleanup(func() {
		_ = l.Close()
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, c := range p.conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			i := len(p.conns)
			p.srcs = append(p.srcs, c.RemoteAddr().String())
			if p.tlsConfig != nil {
				c = tls.Server(c, p.tlsConfig)
			}
			p.conns = append(p.conns, c)
			p.oks = append(p.oks, 0)
			p.mu.Unlock()
			go p.serveStream(i, c, p.srcs[i])
		}
	}()
	return p
}

func (p *peer) serveUDP() {
	buf := make([]byte, 65535)
	for {
		n, src, err := p.udp.ReadFrom(buf)
		if err != nil {
			return
		}
		msg, err := sip.ParseMessage(append([]byte(nil), buf[:n]...))
		if err != nil {
			continue
		}
		if res := p.handle(0, src.String(), msg); res != nil {
			_, _ = p.udp.WriteTo([]byte(res.String()), src)
		}
	}
}

func (p *peer) serveStream(i int, c net.Conn, src string) {
	r := bufio.NewReader(c)
	for {
		data, err := readStreamMessage(r)
		if err != nil {
			return
		}
		msg, err := sip.ParseMessage(data)
		if err != nil {
			continue
		}
		res := p.handle(i, src, msg)
		if res == nil {
			continue
		}
		if _, err := c.Write([]byte(res.String())); err != nil {
			return
		}
		p.mu.Lock()
		closeNow := p.closeAfterOK && i == 0 && p.oks[0] == 1 && res.StatusCode == 200 &&
			res.CSeq().MethodName == sip.REGISTER
		p.mu.Unlock()
		if closeNow {
			_ = c.Close()
			return
		}
	}
}

// readStreamMessage reads one SIP message from a stream, framed by Content-Length.
func readStreamMessage(r *bufio.Reader) ([]byte, error) {
	var head strings.Builder
	length := 0
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(line) == "" {
			if head.Len() == 0 {
				continue // keepalive
			}
			head.WriteString(line)
			break
		}
		head.WriteString(line)
		if name, value, ok := strings.Cut(line, ":"); ok {
			name = strings.TrimSpace(name)
			if strings.EqualFold(name, "Content-Length") || name == "l" {
				length, _ = strconv.Atoi(strings.TrimSpace(value))
			}
		}
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return append([]byte(head.String()), body...), nil
}

func (p *peer) handle(i int, src string, msg sip.Message) *sip.Response {
	p.mu.Lock()
	p.msgs = append(p.msgs, peerMsg{conn: i, src: src, msg: msg})
	p.mu.Unlock()
	req, ok := msg.(*sip.Request)
	if !ok {
		return nil
	}
	switch req.Method {
	case sip.REGISTER:
		if req.GetHeader("Authorization") == nil {
			res := sip.NewResponseFromRequest(req, 401, "Unauthorized", nil)
			res.AppendHeader(sip.NewHeader("WWW-Authenticate", `Digest realm="r", nonce="n", algorithm=MD5, qop="auth"`))
			return res
		}
		p.mu.Lock()
		p.oks[i]++
		p.mu.Unlock()
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	case sip.INVITE:
		res := sip.NewResponseFromRequest(req, 200, "OK", []byte("v=0\r\n"))
		if _, ok := res.To().Params.Get("tag"); !ok {
			res.To().Params.Add("tag", "peer")
		}
		params := sip.NewParams()
		if p.transport != "udp" {
			params.Add("transport", p.transport)
		}
		host, port, _ := sip.ParseAddr(p.addr)
		res.AppendHeader(&sip.ContactHeader{Address: sip.Uri{User: "callee", Host: host, Port: port, UriParams: params}})
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		return res
	case sip.BYE:
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	}
	return nil
}

// accepted is the number of connections the peer accepted and their source addresses.
func (p *peer) accepted() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.srcs...)
}

func (p *peer) okCount(i int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.oks) {
		return 0
	}
	return p.oks[i]
}

// requests are the requests the peer received with this method.
func (p *peer) requests(method sip.RequestMethod) []peerMsg {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []peerMsg
	for _, m := range p.msgs {
		if req, ok := m.msg.(*sip.Request); ok && req.Method == method {
			out = append(out, m)
		}
	}
	return out
}

// write sends raw bytes to the peer's i-th connection.
func (p *peer) write(i int, data string) {
	p.t.Helper()
	p.mu.Lock()
	c := p.conns[i]
	p.mu.Unlock()
	if _, err := c.Write([]byte(data)); err != nil {
		p.t.Fatal(err)
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "peer"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// clientTLS trusts the peer's certificate.
func clientTLS(t *testing.T, p *peer) *tls.Config {
	t.Helper()
	cert, err := x509.ParseCertificate(p.tlsConfig.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &tls.Config{RootCAs: pool}
}

// freePort is a port free on 0.0.0.0 for both UDP and TCP.
func freePort(t *testing.T) int {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp4", "0.0.0.0:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		u, err := net.ListenPacket("udp4", net.JoinHostPort("0.0.0.0", strconv.Itoa(port)))
		if err != nil {
			continue
		}
		_ = u.Close()
		return port
	}
	t.Fatal("no free port")
	return 0
}

type testNode struct {
	ua   *sipua.UA
	port int
	mu   sync.Mutex
	regs []sipua.RegistrationState
}

func (n *testNode) registered() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, s := range n.regs {
		if s.State == sipua.RegRegistered {
			return true
		}
	}
	return false
}

// startNode serves a node listening on host:<free port>, registering to p over its transport
// when p is not nil.
func startNode(t *testing.T, host string, p *peer, tlsConfig *tls.Config) *testNode {
	t.Helper()
	n := newNode(t, host, p, tlsConfig)
	n.serve(t)
	return n
}

// serve starts the node's Serve and stops it when the test ends.
func (n *testNode) serve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = n.ua.Serve(ctx) }()
	t.Cleanup(cancel)
}

// newNode is a node listening on host:<free port> whose Serve has not started.
func newNode(t *testing.T, host string, p *peer, tlsConfig *tls.Config) *testNode {
	t.Helper()
	n := &testNode{port: freePort(t)}
	cfg := sipua.Config{Listen: net.JoinHostPort(host, strconv.Itoa(n.port)), AdvertiseHost: "127.0.0.1",
		AdvertisePort: n.port, ClientTLS: tlsConfig, OnRegistrationState: func(s sipua.RegistrationState) {
			n.mu.Lock()
			n.regs = append(n.regs, s)
			n.mu.Unlock()
		}}
	if p != nil {
		registrar := "sip:" + p.addr
		if p.transport != "udp" {
			registrar += ";transport=" + p.transport
		}
		cfg.Registrations = []sipua.Registration{{Registrar: registrar, AOR: "sip:roomr@default",
			Username: "roomr", Password: "secret", Expires: 2 * time.Second}}
	}
	ua, err := sipua.New(node.New(nil, node.Config{NodeID: "t"}), cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.ua = ua
	t.Cleanup(ua.Close)
	return n
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func srcPort(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	return p
}

// assertOneConnection checks that the peer accepted exactly one connection, from the node's
// listen port, and that every message it received came on it.
func assertOneConnection(t *testing.T, p *peer, n *testNode) {
	t.Helper()
	srcs := p.accepted()
	if len(srcs) != 1 {
		t.Fatalf("peer accepted %d connections (%v), want 1", len(srcs), srcs)
	}
	if got := srcPort(t, srcs[0]); got != n.port {
		t.Fatalf("connection source port %d, want the listen port %d", got, n.port)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, m := range p.msgs {
		if m.conn != 0 {
			t.Fatalf("message on connection %d, want 0", m.conn)
		}
	}
}

// registersOnOneConnection: the REGISTER, the digest REGISTER, a 200 and a refresh all on one
// connection from the listen port.
func registersOnOneConnection(t *testing.T, transport, host string) {
	p := startPeer(t, transport)
	var tlsConfig *tls.Config
	if transport == "tls" {
		tlsConfig = clientTLS(t, p)
	}
	n := startNode(t, host, p, tlsConfig)
	waitFor(t, "a refresh accepted", func() bool { return p.okCount(0) >= 2 })
	if !n.registered() {
		t.Fatal("registration never reported registered")
	}
	if got := len(p.requests(sip.REGISTER)); got < 4 {
		t.Fatalf("%d REGISTERs, want the REGISTER, the digest REGISTER and a refresh", got)
	}
	assertOneConnection(t, p, n)
}

func TestROM033_RegistersOverTCPWithWildcardListen(t *testing.T) {
	registersOnOneConnection(t, "tcp", "0.0.0.0")
}

func TestROM033_RegistersOverTCPWithSpecificListen(t *testing.T) {
	registersOnOneConnection(t, "tcp", "127.0.0.1")
}

func TestROM033_RegistersOverTLSWithWildcardListen(t *testing.T) {
	registersOnOneConnection(t, "tls", "0.0.0.0")
}

func TestROM033_DialsThroughTCPProxyOnRegisteredConnection(t *testing.T) {
	p := startPeer(t, "tcp")
	n := startNode(t, "0.0.0.0", p, nil)
	waitFor(t, "registered", func() bool { return p.okCount(0) >= 1 })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leg, _, err := n.ua.DialTarget(ctx, node.SipTarget{URI: "sip:callee@127.0.0.1", Proxy: "sip:" + p.addr + ";transport=tcp"},
		"", []byte("v=0\r\n"), func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	if err := leg.Bye(); err != nil {
		t.Fatal(err)
	}
	for _, m := range []sip.RequestMethod{sip.INVITE, sip.ACK, sip.BYE} {
		waitFor(t, string(m), func() bool { return len(p.requests(m)) > 0 })
	}
	assertOneConnection(t, p, n)
}

func TestROM033_AnswersInboundRequestOnRegisteredConnection(t *testing.T) {
	p := startPeer(t, "tcp")
	n := startNode(t, "0.0.0.0", p, nil)
	waitFor(t, "registered", func() bool { return p.okCount(0) >= 1 })
	host, port, _ := sip.ParseAddr(p.addr)
	p.write(0, fmt.Sprintf("INVITE sip:roomr@127.0.0.1:%d;transport=tcp SIP/2.0\r\n"+
		"Via: SIP/2.0/TCP %s:%d;branch=z9hG4bK-peer-invite\r\n"+
		"From: <sip:caller@%s>;tag=peer\r\n"+
		"To: <sip:roomr@127.0.0.1>\r\n"+
		"Call-ID: peer-invite\r\n"+
		"CSeq: 1 INVITE\r\n"+
		"Contact: <sip:caller@%s:%d;transport=tcp>\r\n"+
		"Max-Forwards: 70\r\n"+
		"Content-Length: 0\r\n\r\n", n.port, host, port, host, host, port))
	waitFor(t, "a response to the INVITE", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		for _, m := range p.msgs {
			if res, ok := m.msg.(*sip.Response); ok && res.CallID().Value() == "peer-invite" {
				return true
			}
		}
		return false
	})
	assertOneConnection(t, p, n)
}

func TestROM033_ReregistersOverTCPAfterPeerClose(t *testing.T) {
	p := startPeer(t, "tcp")
	p.closeAfterOK = true
	n := startNode(t, "0.0.0.0", p, nil)
	waitFor(t, "a refresh accepted on a second connection", func() bool { return p.okCount(1) >= 1 })
	srcs := p.accepted()
	if len(srcs) != 2 {
		t.Fatalf("peer accepted %d connections (%v), want 2", len(srcs), srcs)
	}
	if got := srcPort(t, srcs[1]); got != n.port {
		t.Fatalf("second connection source port %d, want the listen port %d", got, n.port)
	}
}

func TestROM033_RegistersOverUDPFromListenPortWithWildcardListen(t *testing.T) {
	p := startPeer(t, "udp")
	n := startNode(t, "0.0.0.0", p, nil)
	waitFor(t, "registered", func() bool { return p.okCount(0) >= 1 })
	for _, m := range p.requests(sip.REGISTER) {
		if got := srcPort(t, m.src); got != n.port {
			t.Fatalf("REGISTER from port %d, want the listen port %d", got, n.port)
		}
	}
}

func TestROM033_AcksOverUDPFromListenPortWithWildcardListen(t *testing.T) {
	p := startPeer(t, "udp")
	n := startNode(t, "0.0.0.0", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leg, _, err := n.ua.DialTarget(ctx, node.SipTarget{URI: "sip:callee@" + p.addr}, "", []byte("v=0\r\n"), func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leg.Bye() }()
	waitFor(t, "the ACK", func() bool { return len(p.requests(sip.ACK)) > 0 })
	for _, m := range p.requests(sip.ACK) {
		if got := srcPort(t, m.src); got != n.port {
			t.Fatalf("ACK from port %d, want the listen port %d", got, n.port)
		}
	}
}

// A UDP request made before Serve has handed the listener to the transport waits for it, then
// leaves from the listen port; before, it bound a second socket on the port and failed on Linux.
func TestROM033_DialsBeforeServeOverUDP(t *testing.T) {
	p := startPeer(t, "udp")
	n := newNode(t, "0.0.0.0", nil, nil)
	time.AfterFunc(100*time.Millisecond, func() { n.serve(t) })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	leg, _, err := n.ua.DialTarget(ctx, node.SipTarget{URI: "sip:callee@" + p.addr}, "", []byte("v=0\r\n"), func(int) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = leg.Bye() }()
	waitFor(t, "the ACK", func() bool { return len(p.requests(sip.ACK)) > 0 })
	for _, method := range []sip.RequestMethod{sip.INVITE, sip.ACK} {
		for _, m := range p.requests(method) {
			if got := srcPort(t, m.src); got != n.port {
				t.Fatalf("%s from port %d, want the listen port %d", method, got, n.port)
			}
		}
	}
}

func TestROM033_DialFailsWhenContextEndsBeforeServe(t *testing.T) {
	p := startPeer(t, "udp")
	n := newNode(t, "0.0.0.0", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := n.ua.DialTarget(ctx, node.SipTarget{URI: "sip:callee@" + p.addr}, "", []byte("v=0\r\n"), func(int) {})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialTarget error = %v, want %v", err, context.DeadlineExceeded)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("DialTarget returned after %v, want about 200ms", d)
	}
	if got := len(p.requests(sip.INVITE)); got != 0 {
		t.Fatalf("peer received %d INVITEs, want none", got)
	}
}
