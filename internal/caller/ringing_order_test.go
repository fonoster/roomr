package caller

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// burstUAS answers every INVITE on l with 100, 180 and 503 written back to back from one
// goroutine, in that order (what a node whose session endpoint refuses connections sends, minus
// the few hundred microseconds between them), and ACKs nothing back.
func burstUAS(l net.PacketConn) {
	buf := make([]byte, 65535)
	for {
		n, from, err := l.ReadFrom(buf)
		if err != nil {
			return
		}
		msg := string(buf[:n])
		if !strings.HasPrefix(msg, "INVITE ") {
			continue
		}
		var keep []string
		for _, h := range strings.Split(msg, "\r\n")[1:] {
			if h == "" {
				break
			}
			k, _, _ := strings.Cut(h, ":")
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "via", "from", "call-id", "cseq":
				keep = append(keep, h)
			case "to":
				keep = append(keep, h+";tag=uas")
			}
		}
		for _, st := range []string{"100 Trying", "180 Ringing", "503 Service Unavailable"} {
			res := "SIP/2.0 " + st + "\r\n" + strings.Join(keep, "\r\n") + "\r\nContent-Length: 0\r\n\r\n"
			_, _ = l.WriteTo([]byte(res), from)
		}
	}
}

// The bot reports every provisional response the UAS sent before its final, in order, even when
// the final arrives right behind it (media-control "Ringing before answer": the conformance
// suites assert caller.sip [180 503]).
func TestROM002_CallerKeepsProvisionalBeforeBackToBackFinal(t *testing.T) {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go burstUAS(l)
	bot, err := NewWithOptions(Options{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	runs := 200
	if n, err := strconv.Atoi(os.Getenv("RINGING_RUNS")); err == nil && n > 0 {
		runs = n
	}
	bad := map[string]int{}
	for i := range runs {
		c, err := bot.Dial(l.LocalAddr().String(), "+15550002222", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(5 * time.Second)
		for c.Status().State != "ended" && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		s := c.Status()
		if got := fmt.Sprint(s.SIP); got != "[100 180 503]" {
			bad[got]++
			if bad[got] == 1 {
				t.Errorf("call %d: statuses %s, want [100 180 503]", i, got)
			}
		}
	}
	total := 0
	for _, n := range bad {
		total += n
	}
	t.Logf("%d/%d calls lost a provisional: %v", total, runs, bad)
}
