// Command roomr-fake runs real Roomr nodes behind an HTTP+JSON control API, for the tests of
// platforms that drive Roomr (openspec add-roomr-fake). Each node is the real node with its SIP
// user agent and Roomr gRPC server; its phone (the caller bot) calls it and answers what it dials
// over loopback SIP and RTP, so controllers and session endpoints see a node that behaves like the
// real one, while tests script the phone, faults and failures. See docs/FAKE.md.
//
//	roomr-fake -listen 127.0.0.1:8090 -nats nats://127.0.0.1:4222 -host 127.0.0.1
package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fonoster/roomr/internal/rtpio"
	"github.com/nats-io/nats.go"
)

// version is the build's version (-ldflags "-X main.version=..."), reported in heartbeats.
var version = "dev"

func main() {
	listen := flag.String("listen", "127.0.0.1:8090", "HTTP control API address")
	host := flag.String("host", "127.0.0.1", "address the nodes' gRPC, SIP and RTP bind to")
	advertise := flag.String("advertise", "", "address heartbeats and SDP announce (default: -host)")
	natsURL := flag.String("nats", nats.DefaultURL, `NATS for heartbeats and events ("none": no NATS)`)
	rtpPorts := flag.String("rtp-ports", "", "RTP port range, e.g. 20000-20999 (default: any)")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	if *rtpPorts != "" {
		lo, hi, ok := strings.Cut(*rtpPorts, "-")
		min, err1 := strconv.Atoi(lo)
		max, err2 := strconv.Atoi(hi)
		if !ok || err1 != nil || err2 != nil || min <= 0 || min > max {
			fmt.Fprintln(os.Stderr, "invalid -rtp-ports:", *rtpPorts)
			os.Exit(2)
		}
		rtpio.SetPortRange(min, max)
	}
	var nc *nats.Conn
	if *natsURL != "none" && *natsURL != "" {
		var err error
		nc, err = nats.Connect(*natsURL, nats.Name("roomr-fake"), nats.MaxReconnects(-1),
			nats.RetryOnFailedConnect(true), nats.ReconnectWait(time.Second), nats.ReconnectBufSize(-1))
		if err != nil {
			fmt.Fprintln(os.Stderr, "nats:", err)
			os.Exit(1)
		}
		defer nc.Close()
	}
	if *advertise == "" {
		*advertise = *host
	}
	f := newFake(*host, *advertise, nc, logger)
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// The first stdout line tells a harness where to reach the API.
	fmt.Println("roomr-fake listening " + l.Addr().String())
	if err := http.Serve(l, f.routes()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
