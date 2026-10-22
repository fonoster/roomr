package e2e

// Ringing before answer (media-control "Ringing before answer" / "Caller hears ringing while the
// app is unreachable"): the node sends 180 before the 503 it gives a caller whose session endpoint
// refuses connections, on the wire, and the caller bot reports both.

import (
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/fonoster/roomr/internal/caller"
	"github.com/fonoster/roomr/internal/node"
)

// ringingRuns is how many calls each ringing test places (RINGING_RUNS overrides it).
func ringingRuns() int {
	if n, err := strconv.Atoi(os.Getenv("RINGING_RUNS")); err == nil && n > 0 {
		return n
	}
	return 20
}

// rawInvite sends one INVITE over a fresh UDP socket and returns every response status the node
// sent for it, in arrival order, up to and including the final (which it ACKs).
func rawInvite(t *testing.T, target string, i int) ([]int, time.Duration) {
	t.Helper()
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dst, err := net.ResolveUDPAddr("udp4", target)
	if err != nil {
		t.Fatal(err)
	}
	local := conn.LocalAddr().String()
	branch := fmt.Sprintf("z9hG4bK-raw-%d-%d", time.Now().UnixNano(), i)
	callID := fmt.Sprintf("raw-%d-%d@127.0.0.1", time.Now().UnixNano(), i)
	body := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\n" +
		"m=audio 40000 RTP/AVP 0 101\r\na=rtpmap:0 PCMU/8000\r\na=rtpmap:101 telephone-event/8000\r\na=sendrecv\r\n"
	head := func(method, cseqMethod string, extra string) string {
		return fmt.Sprintf("%s sip:+15550002222@%s SIP/2.0\r\n"+
			"Via: SIP/2.0/UDP %s;branch=%s;rport\r\n"+
			"Max-Forwards: 70\r\n"+
			"From: <sip:+15550001111@127.0.0.1>;tag=raw%d\r\n"+
			"To: <sip:+15550002222@%s>%s\r\n"+
			"Call-ID: %s\r\n"+
			"CSeq: 1 %s\r\n"+
			"Contact: <sip:caller@%s>\r\n", method, target, local, branch, i, target, extra, callID, cseqMethod, local)
	}
	invite := head("INVITE", "INVITE", "") + "Content-Type: application/sdp\r\n" +
		"Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
	if _, err := conn.WriteTo([]byte(invite), dst); err != nil {
		t.Fatal(err)
	}
	var got []int
	var ringAt, finalAt time.Time
	buf := make([]byte, 65535)
	deadline := time.Now().Add(7 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(deadline)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break
		}
		msg := string(buf[:n])
		line, _, _ := strings.Cut(msg, "\r\n")
		f := strings.Fields(line)
		if len(f) < 2 || f[0] != "SIP/2.0" {
			continue
		}
		code, _ := strconv.Atoi(f[1])
		got = append(got, code)
		if code == 180 {
			ringAt = time.Now()
		}
		if code >= 200 {
			finalAt = time.Now()
			toTag := ""
			for _, h := range strings.Split(msg, "\r\n") {
				if k, v, ok := strings.Cut(h, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "To") {
					if _, tag, ok := strings.Cut(v, ";tag="); ok {
						toTag = ";tag=" + strings.TrimSpace(tag)
					}
				}
			}
			ack := head("ACK", "ACK", toTag) + "Content-Length: 0\r\n\r\n"
			_, _ = conn.WriteTo([]byte(ack), dst)
			break
		}
	}
	gap := time.Duration(-1)
	if !ringAt.IsZero() && !finalAt.IsZero() {
		gap = finalAt.Sub(ringAt)
	}
	return got, gap
}

// Responses other than 100 Trying, as the conformance suites compare them.
func withoutTrying(s []int) []int {
	var out []int
	for _, c := range s {
		if c != 100 {
			out = append(out, c)
		}
	}
	return out
}

// TestROM002_UnreachableAppRingsBefore503OnTheWire: on the raw UDP socket of the caller, every
// call to a node whose session endpoint refuses connections gets 180 and then 503.
func TestROM002_UnreachableAppRingsBefore503OnTheWire(t *testing.T) {
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	r := startWith(t, rigOptions{nodeCfg: func(c *node.Config) { c.SessionEndpoint = dead }})
	runs, bad := ringingRuns(), 0
	var gaps []time.Duration
	for i := range runs {
		statuses, gap := rawInvite(t, r.sip, i)
		gaps = append(gaps, gap)
		got := withoutTrying(statuses)
		if len(got) != 2 || got[0] != 180 || got[1] != 503 {
			bad++
			t.Errorf("call %d: wire statuses %v, want [180 503]", i, got)
		}
	}
	slices.Sort(gaps)
	t.Logf("wire: %d/%d calls without [180 503]; 180->503 gap min %v median %v max %v", bad, runs,
		gaps[0], gaps[len(gaps)/2], gaps[len(gaps)-1])
}

// TestROM002_UnreachableAppCallerBotReports180Then503: the caller bot (sipgo dialog client)
// reports [180 503] for the same calls.
func TestROM002_UnreachableAppCallerBotReports180Then503(t *testing.T) {
	dead := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	r := startWith(t, rigOptions{nodeCfg: func(c *node.Config) { c.SessionEndpoint = dead }})
	runs, bad := ringingRuns(), 0
	for i := range runs {
		call, err := r.bot.Dial(r.sip, "+15550002222", "", nil)
		if err != nil {
			t.Fatal(err)
		}
		s := waitStatus(t, call, func(s caller.Status) bool { return s.State == "ended" }, 7*time.Second)
		got := withoutTrying(s.SIP)
		if len(got) != 2 || got[0] != 180 || got[1] != 503 {
			bad++
			t.Errorf("call %d: caller bot statuses %v, want [180 503]", i, s.SIP)
		}
	}
	t.Logf("caller bot: %d/%d calls without [180 503]", bad, runs)
}
