package caller

import (
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// crossingUAS answers every INVITE on l with 100 and 180, and when the CANCEL arrives it answers
// the CANCEL with 200 and the INVITE with 200 OK instead of 487: the callee's answer crossed the
// caller's CANCEL. It records the methods it receives after that and answers BYE with 200.
type crossingUAS struct {
	l    net.PacketConn
	mu   sync.Mutex
	seen []string
}

func (u *crossingUAS) methods() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

// headersOf keeps the headers a response copies from a request; the To gets the UAS tag.
func headersOf(msg string) []string {
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
			if strings.Contains(h, ";tag=") {
				keep = append(keep, h)
			} else {
				keep = append(keep, h+";tag=uas")
			}
		}
	}
	return keep
}

func (u *crossingUAS) serve() {
	buf := make([]byte, 65535)
	var invite string
	for {
		n, from, err := u.l.ReadFrom(buf)
		if err != nil {
			return
		}
		msg := string(buf[:n])
		method, _, _ := strings.Cut(msg, " ")
		reply := func(status string, req string, extra ...string) {
			res := "SIP/2.0 " + status + "\r\n" + strings.Join(append(headersOf(req), extra...), "\r\n")
			_, _ = u.l.WriteTo([]byte(res), from)
		}
		switch method {
		case "INVITE":
			invite = msg
			reply("100 Trying", msg, "Content-Length: 0\r\n\r\n")
			reply("180 Ringing", msg, "Content-Length: 0\r\n\r\n")
		case "CANCEL":
			u.record(method)
			reply("200 OK", msg, "Content-Length: 0\r\n\r\n")
			sdp := "v=0\r\no=- 1 1 IN IP4 127.0.0.1\r\ns=-\r\nc=IN IP4 127.0.0.1\r\nt=0 0\r\nm=audio 40000 RTP/AVP 0\r\na=rtpmap:0 PCMU/8000\r\n"
			reply("200 OK", invite, "Contact: <sip:uas@"+u.l.LocalAddr().String()+">",
				"Content-Type: application/sdp", "Content-Length: "+itoa(len(sdp))+"\r\n\r\n"+sdp)
		case "ACK":
			u.record(method)
		case "BYE":
			u.record(method)
			reply("200 OK", msg, "Content-Length: 0\r\n\r\n")
		}
	}
}

func (u *crossingUAS) record(method string) {
	u.mu.Lock()
	u.seen = append(u.seen, method)
	u.mu.Unlock()
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for ; n > 0; n /= 10 {
		b = append([]byte{byte('0' + n%10)}, b...)
	}
	return string(b)
}

// A caller that hangs up while ringing, whose CANCEL crosses the callee's 200 OK, still ACKs the
// 200 and ends the dialog with BYE (RFC 3261 9.1); otherwise the callee waits 64*T1 for the ACK
// and keeps the call up.
func TestCallerAcksAndByesA200ThatCrossedItsCancel(t *testing.T) {
	l, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	uas := &crossingUAS{l: l}
	go uas.serve()
	bot, err := NewWithOptions(Options{Host: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	defer bot.Close()
	c, err := bot.Dial(l.LocalAddr().String(), "+15550002222", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !ringing(c.Status().SIP) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if err := c.Hangup(); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		m := strings.Join(uas.methods(), " ")
		if strings.Contains(m, "ACK") && strings.Contains(m, "BYE") && c.Status().State == "ended" {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("callee saw %v, call state %q: want CANCEL, ACK and BYE and the call ended", uas.methods(), c.Status().State)
}

func ringing(statuses []int) bool {
	for _, st := range statuses {
		if st == 180 {
			return true
		}
	}
	return false
}
