package sipua

import (
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/fonoster/roomr/internal/rtpio"
	"github.com/fonoster/roomr/internal/sdp"
)

// remoteSDP is a caller's offer at 10.0.0.1:4000 with PCMU, PCMA and telephone-event 101,
// direction dir, the port and the payload types changed by the arguments.
func remoteSDP(port int, pts, dir string) []byte {
	return []byte("v=0\r\no=caller 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\n" +
		"m=audio " + strconv.Itoa(port) + " RTP/AVP " + pts + "\r\na=rtpmap:101 telephone-event/8000\r\n" +
		"a=fmtp:101 0-16\r\na=" + dir + "\r\n")
}

func mustParse(t *testing.T, body []byte) sdp.Media {
	t.Helper()
	m, err := sdp.Parse(body)
	if err != nil {
		t.Fatalf("%v in\n%s", err, body)
	}
	return m
}

// inboundLeg is the in-dialog state of a leg the node answered with PCMU to remoteSDP.
func inboundLeg(t *testing.T) (*inDialog, sdp.Media) {
	t.Helper()
	d := &inDialog{}
	answer := sdp.Write("10.0.0.9", 5000, 0, "PCMU/8000", 101)
	if !d.setupInbound(answer, mustParse(t, remoteSDP(4000, "0 8 101", "sendrecv"))) {
		t.Fatal("setup refused")
	}
	return d, mustParse(t, answer)
}

// TestROM036_OfferThatMovesMediaIsRefused: an in-dialog offer that moves the remote's address or
// port, drops the negotiated codec or moves telephone-event is answered 488 and changes nothing;
// a later hold offer is still answered against the setup stream.
func TestROM036_OfferThatMovesMediaIsRefused(t *testing.T) {
	d, setup := inboundLeg(t)
	moved := strings.Replace(string(remoteSDP(4000, "0 8 101", "inactive")), "c=IN IP4 10.0.0.1", "c=IN IP4 10.0.0.2", 1)
	for i, body := range [][]byte{
		remoteSDP(4002, "0 8 101", "inactive"), // new port
		[]byte(moved),                          // new address
		remoteSDP(4000, "8 101", "inactive"),   // PCMU dropped
		[]byte(strings.Replace(string(remoteSDP(4000, "0 8 96", "inactive")), "rtpmap:101", "rtpmap:96", 1)), // telephone-event moved
	} {
		o := d.handle(sip.INVITE, uint32(2+i), body)
		if o.status != 488 || o.report || len(o.body) != 0 || o.why == "" {
			t.Fatalf("offer %d: %+v", i, o)
		}
	}
	o := d.handle(sip.INVITE, 10, remoteSDP(4000, "0 8 101", "inactive"))
	if o.status != 200 || !o.report || o.sends || o.receives {
		t.Fatalf("hold after refusals: %+v", o)
	}
	a := mustParse(t, o.body)
	if a.Direction != sdp.Inactive || a.Port != 5000 || a.SessionID != setup.SessionID || a.SessionVersion != setup.SessionVersion+1 {
		t.Fatalf("answer %+v (setup %+v)", a, setup)
	}
}

// TestROM036_AnswerVersionRisesOnlyOnChange: o= keeps the version while the node's SDP is the
// same and raises it by one when it changes (RFC 3264 section 8).
func TestROM036_AnswerVersionRisesOnlyOnChange(t *testing.T) {
	d, setup := inboundLeg(t)
	steps := []struct {
		dir     string
		version uint64
	}{{"sendrecv", 0}, {"inactive", 1}, {"inactive", 1}, {"sendonly", 2}, {"sendrecv", 3}}
	for i, s := range steps {
		o := d.handle(sip.UPDATE, uint32(2+i), remoteSDP(4000, "0 8 101", s.dir))
		a := mustParse(t, o.body)
		if o.status != 200 || a.SessionVersion != setup.SessionVersion+s.version || a.Direction != sdp.AnswerDirection(s.dir) {
			t.Fatalf("step %d (%s): status %d answer %+v", i, s.dir, o.status, a)
		}
	}
}

// TestROM036_RetransmittedReInviteGetsTheSameAnswer: a re-INVITE with the CSeq already answered
// gets the same response (same o= version) and is not applied again; a lower CSeq gets 500.
func TestROM036_RetransmittedReInviteGetsTheSameAnswer(t *testing.T) {
	d, _ := inboundLeg(t)
	first := d.handle(sip.INVITE, 5, remoteSDP(4000, "0 8 101", "inactive"))
	again := d.handle(sip.INVITE, 5, remoteSDP(4000, "0 8 101", "inactive"))
	if first.status != 200 || again.status != 200 || string(first.body) != string(again.body) {
		t.Fatalf("first %d %q, again %d %q", first.status, first.body, again.status, again.body)
	}
	if again.report {
		t.Fatal("a retransmission was applied again")
	}
	if first.acked == nil || again.acked != first.acked {
		t.Fatal("the retransmission does not wait for the same ACK")
	}
	if o := d.handle(sip.INVITE, 4, remoteSDP(4000, "0 8 101", "sendrecv")); o.status != 500 {
		t.Fatalf("lower CSeq answered %d", o.status)
	}
	if o := d.handle(sip.UPDATE, 5, remoteSDP(4000, "0 8 101", "sendrecv")); o.status != 500 {
		t.Fatalf("same CSeq, other method answered %d", o.status)
	}
	d.ack(5, nil)
	select {
	case <-first.acked:
	default:
		t.Fatal("the ACK did not stop the 2xx retransmissions")
	}
}

// TestROM036_DialedLegAnswersWithTheNegotiatedCodec: on a leg the node dialed, the in-dialog
// answer carries the one codec the callee chose, not the node's whole setup offer.
func TestROM036_DialedLegAnswersWithTheNegotiatedCodec(t *testing.T) {
	offer := sdp.WriteDescription(sdp.Description{Host: "10.0.0.9", Port: 5000, DTMF: 101, Codecs: []sdp.Codec{
		{PT: 111, Name: "opus/48000"}, {PT: 9, Name: "G722/8000"}, {PT: 0, Name: "PCMU/8000"}, {PT: 8, Name: "PCMA/8000"}}})
	d := &inDialog{}
	if !d.setupDialed(offer, remoteSDP(4000, "0 101", "sendrecv")) {
		t.Fatal("setup refused")
	}
	o := d.handle(sip.INVITE, 2, remoteSDP(4000, "0 8 101", "inactive"))
	if o.status != 200 || !strings.Contains(string(o.body), "m=audio 5000 RTP/AVP 0 101\r\n") ||
		strings.Contains(string(o.body), "opus") || strings.Contains(string(o.body), "G722") {
		t.Fatalf("answer %d\n%s", o.status, o.body)
	}
	if a := mustParse(t, o.body); a.SessionVersion != mustParse(t, offer).SessionVersion+1 {
		t.Fatalf("version %d, offer's %d", a.SessionVersion, mustParse(t, offer).SessionVersion)
	}
}

// TestROM036_LateOfferHoldInTheACK: a re-INVITE without SDP gets the node's stream as a sendrecv
// offer (version unchanged: it is the same SDP), and the remote's answer in the ACK applies its
// direction; an answer that moves media is not applied.
func TestROM036_LateOfferHoldInTheACK(t *testing.T) {
	d, setup := inboundLeg(t)
	o := d.handle(sip.INVITE, 2, nil)
	a := mustParse(t, o.body)
	if o.status != 200 || o.report || a.Direction != sdp.SendRecv || a.SessionVersion != setup.SessionVersion {
		t.Fatalf("late offer %+v: %+v", o, a)
	}
	sends, receives, report, why := d.ack(2, remoteSDP(4000, "0 101", "inactive"))
	if !report || sends || receives || why != "" {
		t.Fatalf("ACK answer: report %v sends %v receives %v why %q", report, sends, receives, why)
	}
	_ = d.handle(sip.INVITE, 3, nil)
	if _, _, report, why := d.ack(3, remoteSDP(4010, "0 101", "sendrecv")); report || why == "" {
		t.Fatalf("a moving answer was applied (why %q)", why)
	}
	if _, _, report, _ := d.ack(3, remoteSDP(4000, "0 101", "sendrecv")); report {
		t.Fatal("a second ACK was applied")
	}
}

// TestROM036_SRTPKeyChangeIsRefused: on an SRTP leg an offer with another key is refused; the
// same key is answered with the offer's crypto tag and the node's own key.
func TestROM036_SRTPKeyChangeIsRefused(t *testing.T) {
	key1, key2, mine := rtpio.NewSRTPKey(), rtpio.NewSRTPKey(), rtpio.NewSRTPKey()
	offer := func(tag, key, dir string) []byte {
		return []byte("v=0\r\no=c 1 1 IN IP4 10.0.0.1\r\ns=-\r\nc=IN IP4 10.0.0.1\r\nt=0 0\r\n" +
			"m=audio 4000 RTP/SAVP 0\r\na=crypto:" + tag + " AES_CM_128_HMAC_SHA1_80 inline:" + key + "\r\na=" + dir + "\r\n")
	}
	answer := sdp.WriteDescription(sdp.Description{Host: "10.0.0.9", Port: 5000, DTMF: -1,
		Codecs: []sdp.Codec{{PT: 0, Name: "PCMU/8000"}}, Crypto: &sdp.Crypto{Tag: 1, Suite: "AES_CM_128_HMAC_SHA1_80", Key: mine}})
	d := &inDialog{}
	if !d.setupInbound(answer, mustParse(t, offer("1", key1, "sendrecv"))) {
		t.Fatal("setup refused")
	}
	if o := d.handle(sip.INVITE, 2, offer("1", key2, "inactive")); o.status != 488 {
		t.Fatalf("new key answered %d", o.status)
	}
	o := d.handle(sip.INVITE, 3, offer("2", key1, "inactive"))
	a := mustParse(t, o.body)
	if o.status != 200 || len(a.Crypto) != 1 || a.Crypto[0].Tag != 2 || a.Crypto[0].Key != mine {
		t.Fatalf("same key: %d %+v", o.status, a)
	}
}

// TestROM036_DirectionReplayedToALateHandler: a direction reported before the node registered
// its handler is delivered when it registers.
func TestROM036_DirectionReplayedToALateHandler(t *testing.T) {
	d := &inDialog{}
	d.report(false, true)
	var got []bool
	d.OnDirection(func(sends, receives bool) { got = append(got, sends, receives) })
	if len(got) != 2 || got[0] || !got[1] {
		t.Fatalf("replayed %v", got)
	}
	d.report(true, true)
	if len(got) != 4 || !got[2] {
		t.Fatalf("after a later report %v", got)
	}
}

// fakeTx records responses (only Respond is used).
type fakeTx struct {
	sip.ServerTransaction
	mu  sync.Mutex
	res []*sip.Response
}

func (f *fakeTx) Respond(r *sip.Response) error {
	f.mu.Lock()
	f.res = append(f.res, r)
	f.mu.Unlock()
	return nil
}

type fakeOfferLeg struct {
	inDialog
	conf bool
	gone chan struct{}
}

func (l *fakeOfferLeg) offers() *inDialog     { return &l.inDialog }
func (l *fakeOfferLeg) confirmed() bool       { return l.conf }
func (l *fakeOfferLeg) Gone() <-chan struct{} { return l.gone }

func inDialogRequest(method sip.RequestMethod, cseq uint32, body []byte) *sip.Request {
	req := sip.NewRequest(method, sip.Uri{User: "roomr", Host: "10.0.0.9"})
	from := &sip.FromHeader{Address: sip.Uri{User: "caller", Host: "10.0.0.1"}, Params: sip.NewParams()}
	from.Params.Add("tag", "ft")
	to := &sip.ToHeader{Address: sip.Uri{User: "roomr", Host: "10.0.0.9"}, Params: sip.NewParams()}
	to.Params.Add("tag", "tt")
	callID := sip.CallIDHeader("call-1")
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: cseq, MethodName: method})
	req.SetTransport("TCP")
	req.SetBody(body)
	return req
}

// TestROM036_EarlyOfferChangesNothing: an UPDATE or re-INVITE before the leg is answered is
// answered 500 with Retry-After and reports no direction; a re-INVITE after the 200 but before
// its ACK too, while an UPDATE then is taken (RFC 3311).
func TestROM036_EarlyOfferChangesNothing(t *testing.T) {
	u := &UA{cfg: Config{Logger: slog.New(slog.DiscardHandler)}}
	leg := &fakeOfferLeg{gone: make(chan struct{})}
	u.legs.Store(sip.DialogIDMake("call-1", "tt", "ft"), leg)
	reports := 0
	leg.OnDirection(func(bool, bool) { reports++ })
	offer := remoteSDP(4000, "0 8 101", "inactive")

	for _, m := range []sip.RequestMethod{sip.UPDATE, sip.INVITE} {
		tx := &fakeTx{}
		u.onOffer(inDialogRequest(m, 2, offer), tx)
		if len(tx.res) != 1 || tx.res[0].StatusCode != 500 || tx.res[0].GetHeader("Retry-After") == nil {
			t.Fatalf("early %s answered %v", m, tx.res)
		}
	}
	if reports != 0 {
		t.Fatal("an early offer changed the hold state")
	}

	leg.setupInbound(sdp.Write("10.0.0.9", 5000, 0, "PCMU/8000", 101), mustParse(t, remoteSDP(4000, "0 8 101", "sendrecv")))
	tx := &fakeTx{}
	u.onOffer(inDialogRequest(sip.INVITE, 3, offer), tx)
	if tx.res[0].StatusCode != 500 || reports != 0 {
		t.Fatalf("re-INVITE before the ACK answered %d", tx.res[0].StatusCode)
	}
	tx = &fakeTx{}
	u.onOffer(inDialogRequest(sip.UPDATE, 4, offer), tx)
	if tx.res[0].StatusCode != 200 || reports != 1 {
		t.Fatalf("UPDATE after the 200 answered %d, %d reports", tx.res[0].StatusCode, reports)
	}
	leg.conf = true
	tx = &fakeTx{}
	u.onOffer(inDialogRequest(sip.INVITE, 5, remoteSDP(4000, "0 8 101", "sendrecv")), tx)
	if tx.res[0].StatusCode != 200 || reports != 2 {
		t.Fatalf("re-INVITE after the ACK answered %d, %d reports", tx.res[0].StatusCode, reports)
	}
	// A dialog the node does not know is 481.
	tx = &fakeTx{}
	other := inDialogRequest(sip.UPDATE, 2, offer)
	callID := sip.CallIDHeader("call-2")
	other.ReplaceHeader(&callID)
	u.onOffer(other, tx)
	if tx.res[0].StatusCode != 481 {
		t.Fatalf("unknown dialog answered %d", tx.res[0].StatusCode)
	}
}

// TestRetransmit2xx: the 2xx goes again at T1, doubling up to T2, until the ACK.
func TestRetransmit2xx(t *testing.T) {
	var mu sync.Mutex
	var at []time.Time
	acked := make(chan struct{})
	done := make(chan struct{})
	start := time.Now()
	go func() {
		retransmit2xx(func() error {
			mu.Lock()
			at = append(at, time.Now())
			mu.Unlock()
			return nil
		}, acked, nil, 20*time.Millisecond, 80*time.Millisecond)
		close(done)
	}()
	time.Sleep(300 * time.Millisecond)
	close(acked)
	<-done
	// retransmit2xx returned on the ACK, so nothing is sent after it.
	mu.Lock()
	defer mu.Unlock()
	// 20, 60, 140, 220 ms: about four sends in 300 ms.
	if len(at) < 3 || len(at) > 5 {
		t.Fatalf("%d sends in 300 ms", len(at))
	}
	if first := at[0].Sub(start); first < 15*time.Millisecond {
		t.Fatalf("first resend after %v, before T1", first)
	}
}
