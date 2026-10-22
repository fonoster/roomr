package e2e

import (
	"net"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// natsProxy is a TCP proxy between the node and NATS that can take the broker away: cut closes
// every connection and refuses new ones (a full outage), restore lets them through again.
//
// It does not drop individual publishes on a live connection: NATS is a framed text protocol
// (PUB subject size\r\npayload\r\n), so discarding client bytes mid-stream would desync the
// server's parser and make it close the connection, which is a cut by another name.
type natsProxy struct {
	ln       net.Listener
	upstream string

	mu    sync.Mutex
	cutOn bool
	conns map[net.Conn]struct{}
}

func newNATSProxy(t *testing.T, natsURL string) *natsProxy {
	u, err := url.Parse(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &natsProxy{ln: ln, upstream: u.Host, conns: map[net.Conn]struct{}{}}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close(); p.closeAll() })
	return p
}

// URL is what the node connects to instead of NATS.
func (p *natsProxy) URL() string { return "nats://" + p.ln.Addr().String() }

func (p *natsProxy) serve() {
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		go p.handle(c)
	}
}

func (p *natsProxy) handle(c net.Conn) {
	p.mu.Lock()
	if p.cutOn {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	up, err := net.DialTimeout("tcp", p.upstream, 2*time.Second)
	if err != nil {
		p.mu.Unlock()
		_ = c.Close()
		return
	}
	p.conns[c], p.conns[up] = struct{}{}, struct{}{}
	p.mu.Unlock()
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					err = werr
				}
			}
			if err != nil {
				_ = dst.Close()
				_ = src.Close()
				p.mu.Lock()
				delete(p.conns, c)
				delete(p.conns, up)
				p.mu.Unlock()
				return
			}
		}
	}
	go pipe(up, c)
	go pipe(c, up)
}

func (p *natsProxy) closeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for c := range p.conns {
		_ = c.Close()
	}
}

// cut starts an outage: open connections drop and new ones are closed on accept.
func (p *natsProxy) cut() {
	p.mu.Lock()
	p.cutOn = true
	p.mu.Unlock()
	p.closeAll()
}

// restore ends the outage; the node's client reconnects on its own.
func (p *natsProxy) restore() {
	p.mu.Lock()
	p.cutOn = false
	p.mu.Unlock()
}

// productionNATSOptions are the node's NATS options from cmd/roomr/main.go: no reconnect buffer
// (an event that cannot go out is dropped, never replayed), reconnect forever, start without NATS.
func productionNATSOptions() []nats.Option {
	return []nats.Option{nats.ReconnectBufSize(-1), nats.MaxReconnects(-1),
		nats.RetryOnFailedConnect(true), nats.ReconnectWait(time.Second)}
}
