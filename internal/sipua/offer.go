package sipua

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/fonoster/roomr/internal/rtpio"
	"github.com/fonoster/roomr/internal/sdp"
)

// In-dialog offers (re-INVITE and UPDATE) on an answered leg: roomr, "A silent leg ends the
// call". The node takes only what it can honor without touching the leg's RTP endpoint: the
// same remote address and port, the negotiated codec and payload type, the same SRTP key. Such
// an offer changes the media direction only (hold and resume) and is answered 200 with the same
// stream, its direction the offer's mirror (RFC 3264 section 6.1). Any other offer is answered
// 488 and changes nothing (RFC 3261 section 14.2: the session stays as it was).

// inDialog is a leg's offer/answer state after setup (node.DirectionSource).
type inDialog struct {
	// serial runs one in-dialog offer at a time on the leg.
	serial sync.Mutex

	mu sync.Mutex
	// ready: the setup offer/answer is complete (the node sent its 200, or the callee answered).
	ready bool
	// remote is the remote's description at setup: the address, port, codec and keys the leg's
	// RTP endpoint was configured with. pt and codec are the negotiated payload type (the
	// remote's numbering) and codec; key is the remote's SRTP key in use ("" for plain RTP).
	remote    sdp.Media
	pt        int
	codec     string
	key       string
	local     sdp.Media
	localKey  string
	version   uint64
	lastShape string
	// cseq is the highest in-dialog offer CSeq seen; last is how it was answered, so a
	// retransmission (same method and CSeq) gets the same response without being handled again.
	cseq uint32
	last *handled
	// lateOffer is the CSeq of a body-less re-INVITE: the node offered in the 200 and the
	// remote's answer comes in the ACK (0: none pending).
	lateOffer uint32
	// acked closes when the ACK for the 2xx to re-INVITE ackCSeq arrives.
	acked   chan struct{}
	ackCSeq uint32
	// fn gets the remote's direction; the last one reported is kept and replayed to a handler
	// registered later.
	fn              func(sends, receives bool)
	reported        bool
	sends, receives bool
}

type handled struct {
	method sip.RequestMethod
	status int
	body   []byte
}

// setup records the stream negotiated at setup: local is the SDP the node gave, remote the
// remote's, pt the payload type the node's endpoint sends (remote numbering). It returns false
// when the two do not describe a stream (the leg then takes no in-dialog offer).
func (d *inDialog) setup(local []byte, remote sdp.Media, pt int) bool {
	lm, err := sdp.Parse(local)
	if err != nil {
		return false
	}
	codec, ok := remote.Codecs[pt]
	if !ok {
		return false
	}
	key, localKey := "", ""
	if lm.Secure {
		if len(lm.Crypto) == 0 {
			return false
		}
		localKey = lm.Crypto[0].Key
		for _, c := range remote.Crypto {
			if c.Suite == rtpio.SRTPSuite && rtpio.ValidSRTPKey(c.Key) {
				key = c.Key
				break
			}
		}
		if key == "" {
			return false
		}
	}
	tag := 0
	if len(lm.Crypto) > 0 {
		tag = lm.Crypto[0].Tag
	}
	pts := []int{}
	for _, p := range lm.PayloadTypes {
		if p != lm.DTMF {
			pts = append(pts, p)
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.remote, d.pt, d.codec, d.key = remote, pt, codec, key
	d.local, d.localKey, d.version = lm, localKey, lm.SessionVersion
	d.lastShape = shape(lm.Direction, pts, lm.DTMF, tag)
	d.ready = true
	return true
}

// setupInbound is setup for a leg the node answered: its answer names the one payload type it
// chose from the offer.
func (d *inDialog) setupInbound(answer []byte, offer sdp.Media) bool {
	lm, err := sdp.Parse(answer)
	if err != nil {
		return false
	}
	for _, p := range lm.PayloadTypes {
		if p != lm.DTMF {
			return d.setup(answer, offer, p)
		}
	}
	return false
}

// setupDialed is setup for a leg the node dialed: the codec is the first of the answer's that
// the node offered (node.dialOut chooses the same), in the answer's numbering.
func (d *inDialog) setupDialed(offer, answer []byte) bool {
	om, err := sdp.Parse(offer)
	if err != nil {
		return false
	}
	am, err := sdp.Parse(answer)
	if err != nil {
		return false
	}
	for _, p := range am.PayloadTypes {
		for _, q := range om.PayloadTypes {
			if q != om.DTMF && strings.EqualFold(am.Codecs[p], om.Codecs[q]) {
				return d.setup(offer, am, p)
			}
		}
	}
	return false
}

func (d *inDialog) isReady() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready
}

func shape(dir string, pts []int, dtmf, tag int) string {
	return fmt.Sprint(dir, pts, dtmf, tag)
}

// accepts checks an offer (or a late answer) against the stream the leg's endpoint uses. It
// returns the node's description for it, or why the node cannot take it. d.mu must be held.
func (d *inDialog) accepts(m sdp.Media) (sdp.Description, string) {
	if m.Host != d.remote.Host || m.Port != d.remote.Port {
		return sdp.Description{}, fmt.Sprintf("moves the remote's media to %s:%d", m.Host, m.Port)
	}
	if !strings.EqualFold(m.Codecs[d.pt], d.codec) {
		return sdp.Description{}, fmt.Sprintf("drops payload type %d (%s)", d.pt, d.codec)
	}
	if m.Secure != (d.key != "") {
		return sdp.Description{}, "changes the RTP profile"
	}
	desc := sdp.Description{Host: d.local.Host, Port: d.local.Port,
		Codecs: []sdp.Codec{{PT: d.pt, Name: d.codec}}, DTMF: -1}
	if d.key != "" {
		tag := -1
		for _, c := range m.Crypto {
			if c.Suite == rtpio.SRTPSuite && c.Key == d.key {
				tag = c.Tag
				break
			}
		}
		if tag < 0 {
			return sdp.Description{}, "changes the SRTP key"
		}
		desc.Crypto = &sdp.Crypto{Tag: tag, Suite: rtpio.SRTPSuite, Key: d.localKey}
	}
	// telephone-event: kept when it is the payload type the endpoint decodes, left out when the
	// offer has none (or the leg never had it); a different one cannot be applied.
	if m.DTMF >= 0 && d.remote.DTMF >= 0 {
		if m.DTMF != d.remote.DTMF {
			return sdp.Description{}, fmt.Sprintf("moves telephone-event to payload type %d", m.DTMF)
		}
		desc.DTMF = m.DTMF
	}
	return desc, ""
}

// write is desc with direction dir, the session's o= ID and a version one higher than the last
// SDP the node gave when it differs from it (RFC 3264 section 8). d.mu must be held.
func (d *inDialog) write(desc sdp.Description, dir string) []byte {
	tag := 0
	if desc.Crypto != nil {
		tag = desc.Crypto.Tag
	}
	s := shape(dir, []int{desc.Codecs[0].PT}, desc.DTMF, tag)
	if s != d.lastShape {
		d.version++
		d.lastShape = s
	}
	desc.Direction, desc.SessionID, desc.SessionVersion = dir, d.local.SessionID, d.version
	return sdp.WriteDescription(desc)
}

// outcome is how the node answers one in-dialog offer.
type outcome struct {
	status int
	reason string
	body   []byte
	// report: the offer's direction is applied (sends, receives).
	report          bool
	sends, receives bool
	// acked closes when the 2xx to a re-INVITE is ACKed (nil otherwise).
	acked chan struct{}
	// why, for a 488: what the offer would change.
	why string
}

// handle answers an in-dialog offer: method and CSeq, body its SDP (empty: none). The caller
// holds d.serial.
func (d *inDialog) handle(method sip.RequestMethod, cseq uint32, body []byte) outcome {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.last != nil && cseq == d.cseq && method == d.last.method {
		o := outcome{status: d.last.status, body: d.last.body, reason: reason(d.last.status)}
		if method == sip.INVITE && o.status == 200 {
			o.acked = d.awaitAck(cseq)
		}
		return o
	}
	if cseq <= d.cseq && d.last != nil {
		// A lower CSeq is out of order (RFC 3261 section 12.2.2); the same CSeq with another
		// method is not a retransmission.
		return outcome{status: 500, reason: "Server Internal Error"}
	}
	o := d.answer(method, cseq, body)
	d.cseq, d.last = cseq, &handled{method: method, status: o.status, body: o.body}
	if method == sip.INVITE && o.status == 200 {
		o.acked = d.awaitAck(cseq)
	}
	return o
}

func (d *inDialog) answer(method sip.RequestMethod, cseq uint32, body []byte) outcome {
	if len(body) == 0 {
		if method != sip.INVITE {
			return outcome{status: 200, reason: "OK"} // an UPDATE without offer refreshes nothing
		}
		// A late offer: the node offers its stream, sendrecv because it never holds a leg itself
		// (RFC 6337 section 5.3: the offer says what the offerer wants, not the last answer's
		// mirror); the remote's answer in the ACK applies its direction.
		desc, _ := d.accepts(d.remote)
		d.lateOffer = cseq
		return outcome{status: 200, reason: "OK", body: d.write(desc, sdp.SendRecv)}
	}
	m, err := sdp.Parse(body)
	if err != nil {
		return outcome{status: 488, reason: "Not Acceptable Here", why: err.Error()}
	}
	desc, why := d.accepts(m)
	if why != "" {
		return outcome{status: 488, reason: "Not Acceptable Here", why: why}
	}
	return outcome{status: 200, reason: "OK", body: d.write(desc, sdp.AnswerDirection(m.Direction)),
		report: true, sends: m.Sends(), receives: m.Receives()}
}

func reason(status int) string {
	switch status {
	case 200:
		return "OK"
	case 488:
		return "Not Acceptable Here"
	}
	return "Server Internal Error"
}

// awaitAck returns a channel the ACK for the 2xx to re-INVITE cseq closes. d.mu must be held.
func (d *inDialog) awaitAck(cseq uint32) chan struct{} {
	if d.acked != nil && d.ackCSeq == cseq {
		return d.acked
	}
	d.acked, d.ackCSeq = make(chan struct{}), cseq
	return d.acked
}

// ack reads an ACK on the leg: it stops the 2xx retransmissions of its re-INVITE, and for a late
// offer its SDP is the remote's answer. An answer the node cannot apply (it moves media) cannot
// be refused in an ACK: it is logged and the leg keeps its media and direction.
func (d *inDialog) ack(cseq uint32, body []byte) (sends, receives bool, report bool, why string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.acked != nil && d.ackCSeq == cseq {
		close(d.acked)
		d.acked = nil
	}
	if d.lateOffer == 0 || d.lateOffer != cseq {
		return false, false, false, ""
	}
	d.lateOffer = 0
	m, err := sdp.Parse(body)
	if err != nil {
		return false, false, false, "no answer in the ACK"
	}
	if _, why := d.accepts(m); why != "" {
		return false, false, false, why
	}
	return m.Sends(), m.Receives(), true, ""
}

// OnDirection registers the handler for the remote's direction; a direction reported before it
// was registered is delivered at once.
func (d *inDialog) OnDirection(fn func(sends, receives bool)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.fn = fn
	if d.reported && fn != nil {
		fn(d.sends, d.receives)
	}
}

// report delivers the remote's direction (under d.mu, so reports reach the handler in order).
func (d *inDialog) report(sends, receives bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reported, d.sends, d.receives = true, sends, receives
	if d.fn != nil {
		d.fn(sends, receives)
	}
}

// offerLeg is a leg that takes in-dialog offers.
type offerLeg interface {
	offers() *inDialog
	// confirmed: the setup INVITE was ACKed (a re-INVITE before it is refused, RFC 3261
	// section 14.2).
	confirmed() bool
	Gone() <-chan struct{}
}

func (l *inviteLeg) offers() *inDialog { return &l.inDialog }
func (l *outLeg) offers() *inDialog    { return &l.inDialog }

func (l *inviteLeg) confirmed() bool { return l.dlg.LoadState() == sip.DialogStateConfirmed }
func (l *outLeg) confirmed() bool    { return true } // stored once the node ACKed the callee's 200

// retryLater answers an offer the node cannot take yet: the setup offer/answer is not complete
// (RFC 3261 section 14.2 and RFC 3311 section 5.2: 500 with Retry-After, 0 to 10 s).
func retryLater(req *sip.Request, tx sip.ServerTransaction) {
	res := sip.NewResponseFromRequest(req, 500, "Server Internal Error", nil)
	res.AppendHeader(sip.NewHeader("Retry-After", strconv.Itoa(rand.IntN(11))))
	_ = tx.Respond(res)
}

// onOffer answers a re-INVITE or UPDATE: see inDialog. Before the leg's setup offer/answer is
// complete it is answered 500 with Retry-After and changes nothing.
func (u *UA) onOffer(req *sip.Request, tx sip.ServerTransaction) {
	v, found := u.legFor(req)
	leg, ok := v.(offerLeg)
	if !found || !ok {
		if _, dialing := u.dialing.Load(req.CallID().Value()); dialing {
			retryLater(req, tx) // an early dialog of a call the node is placing
			return
		}
		_ = tx.Respond(sip.NewResponseFromRequest(req, 481, "Call/Transaction Does Not Exist", nil))
		return
	}
	d := leg.offers()
	if !d.isReady() || (req.IsInvite() && !leg.confirmed()) {
		retryLater(req, tx)
		return
	}
	d.serial.Lock()
	o := d.handle(req.Method, req.CSeq().SeqNo, req.Body())
	d.serial.Unlock()
	res := sip.NewResponseFromRequest(req, o.status, o.reason, o.body)
	if len(o.body) > 0 {
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	if o.status == 200 {
		contact := u.contact
		if u.dialogsTLS != nil && strings.EqualFold(req.Transport(), "TLS") {
			contact = u.tlsContact
		}
		res.AppendHeader(sip.HeaderClone(&contact))
	}
	if o.why != "" {
		u.cfg.Logger.Info("in-dialog offer refused", "method", string(req.Method), "why", o.why)
	}
	if err := tx.Respond(res); err != nil {
		return
	}
	if o.acked != nil && strings.EqualFold(req.Transport(), "UDP") {
		// The INVITE server transaction does not retransmit a 2xx; the UAS core does, until
		// the ACK (RFC 3261 section 13.3.1.4).
		go retransmit2xx(func() error { return tx.Respond(res) }, o.acked, leg.Gone(), sip.T1, sip.T2)
	}
	if o.report {
		u.cfg.Logger.Debug("in-dialog offer", "method", string(req.Method), "sends", o.sends, "receives", o.receives)
		d.report(o.sends, o.receives)
	}
}

// onAckInDialog reads the ACK of a re-INVITE's 2xx on a leg (see inDialog.ack).
func (u *UA) onAckInDialog(req *sip.Request) {
	v, found := u.legFor(req)
	leg, ok := v.(offerLeg)
	if !found || !ok {
		return
	}
	d := leg.offers()
	sends, receives, report, why := d.ack(req.CSeq().SeqNo, req.Body())
	if why != "" {
		u.cfg.Logger.Warn("late-offer answer not applied", "why", why)
	}
	if report {
		d.report(sends, receives)
	}
}

// retransmit2xx resends a 2xx at T1, doubling up to T2, until acked or gone closes or 64*T1
// passes (RFC 3261 section 13.3.1.4).
func retransmit2xx(send func() error, acked, gone <-chan struct{}, t1, t2 time.Duration) {
	interval := t1
	deadline := time.NewTimer(64 * t1)
	defer deadline.Stop()
	timer := time.NewTimer(interval)
	defer timer.Stop()
	for {
		select {
		case <-acked:
			return
		case <-gone:
			return
		case <-deadline.C:
			return
		case <-timer.C:
			if send() != nil {
				return
			}
			interval = min(2*interval, t2)
			timer.Reset(interval)
		}
	}
}
