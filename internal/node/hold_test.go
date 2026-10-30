package node

import (
	"net"
	"testing"
	"time"

	"github.com/fonoster/roomr/internal/audio"
	"github.com/fonoster/roomr/internal/rtpio"
)

func heldParticipant(t *testing.T) *Participant {
	t.Helper()
	ep, err := rtpio.Listen("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ep.Close)
	codec, _ := audio.CodecFor("PCMU/8000")
	p := newParticipant("p-1", KindPhone, "PCMU/8000", codec, ep, 0)
	p.since, p.leg = time.Now(), newFakeLeg()
	p.answered.Store(true)
	return p
}

// answeredCall is an answered call whose SIP leg is p, with no app leg.
func answeredCall(n *Node, p *Participant, at time.Time) *call {
	c := n.newCall("c1", true, "")
	c.state, c.answeredAt, c.maxDuration, c.leg = CallAnswered, at, time.Hour, p
	return c
}

// TestROM036_HoldPausesTheMediaTimeout: while the caller is held (its SDP says inactive,
// recvonly, sendonly or c=0.0.0.0) its media timeout is paused, and the hold timeout (30m by
// default) counts its silence instead; an offer that makes it active again starts the media
// timeout's count from then (roomr, "A silent leg ends the call", scenario "Caller on hold").
func TestROM036_HoldPausesTheMediaTimeout(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since

	p.setDirection(false, false, t0.Add(time.Second)) // inactive
	c := answeredCall(n, p, t0)
	c.checkLimits(t0.Add(29 * time.Minute))
	if c.ending {
		t.Fatalf("a held caller ended with %q", c.cause)
	}
	if got := p.silentFor(t0.Add(30 * time.Second)); got != 0 {
		t.Fatalf("silentFor while held = %v", got)
	}
	if got := p.heldSilentFor(t0.Add(31 * time.Second)); got != 30*time.Second {
		t.Fatalf("heldSilentFor = %v", got)
	}

	// From one held direction to another (inactive to sendonly) the count goes on.
	p.setDirection(true, false, t0.Add(40*time.Second))
	if got := p.heldSilentFor(t0.Add(41 * time.Second)); got != 40*time.Second {
		t.Fatalf("heldSilentFor after a second hold offer = %v", got)
	}

	// Back to sendrecv at 60 s: the media timeout counts from then.
	resumed := t0.Add(60 * time.Second)
	p.setDirection(true, true, resumed)
	if got := p.heldSilentFor(resumed.Add(time.Second)); got != 0 {
		t.Fatalf("heldSilentFor after resume = %v", got)
	}
	c.checkLimits(resumed.Add(14 * time.Second))
	if c.ending {
		t.Fatalf("ended %v after the resume, before the media timeout", c.cause)
	}
	c.checkLimits(resumed.Add(16 * time.Second))
	if !c.ending || c.cause != ReasonMediaTimeout {
		t.Fatalf("silent after the resume: ending %v cause %q", c.ending, c.cause)
	}
}

// TestROM036_HoldTimeoutCountsSilence: a held caller that sends no RTP for mediaHoldTimeout ends
// the call with media_timeout, counted from the offer that held it; a later offer that keeps it
// held (sendonly to inactive) does not restart the count.
func TestROM036_HoldTimeoutCountsSilence(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, MediaHoldTimeout: 10 * time.Minute, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since
	p.setDirection(true, false, t0) // sendonly
	c := answeredCall(n, p, t0)
	p.setDirection(false, false, t0.Add(5*time.Minute)) // inactive
	c.checkLimits(t0.Add(9 * time.Minute))
	if c.ending {
		t.Fatalf("ended %q before the hold timeout", c.cause)
	}
	c.checkLimits(t0.Add(11 * time.Minute))
	if !c.ending || c.cause != ReasonMediaTimeout {
		t.Fatalf("silent hold: ending %v cause %q", c.ending, c.cause)
	}
}

// TestROM036_SendOnlyCallerIsHeld: a caller whose offer says sendonly is held like one that says
// inactive (desk phones hold so and send no RTP): its silence does not end the call after the
// media timeout.
func TestROM036_SendOnlyCallerIsHeld(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since
	p.setDirection(true, false, t0.Add(time.Second))
	c := answeredCall(n, p, t0)
	c.checkLimits(t0.Add(time.Minute))
	if c.ending {
		t.Fatalf("a silent sendonly caller ended with %q", c.cause)
	}
}

// TestROM036_MediaHoldTimeoutDefault: mediaHoldTimeout is 30m when absent or 0; a negative one
// (never) and a set one are kept.
func TestROM036_MediaHoldTimeoutDefault(t *testing.T) {
	for in, want := range map[time.Duration]time.Duration{0: 30 * time.Minute, -time.Second: -time.Second,
		time.Minute: time.Minute} {
		n := New(nil, Config{MediaHoldTimeout: in, EmptyRoomTimeout: -1})
		got := n.cfg.MediaHoldTimeout
		n.Shutdown()
		if got != want {
			t.Errorf("MediaHoldTimeout %v: got %v, want %v", in, got, want)
		}
	}
}

// TestROM036_NegativeHoldTimeoutNeverEnds: with a negative mediaHoldTimeout a held, silent leg
// is never ended for its silence.
func TestROM036_NegativeHoldTimeoutNeverEnds(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, MediaHoldTimeout: -time.Second, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since
	p.setDirection(false, false, t0)
	c := answeredCall(n, p, t0)
	c.checkLimits(t0.Add(59 * time.Minute))
	if c.ending {
		t.Fatalf("a held caller ended with %q", c.cause)
	}
}

// sinkParticipant is an answered phone participant whose RTP goes to a local socket it returns.
func sinkParticipant(t *testing.T, ref string) (*Participant, *net.UDPConn) {
	t.Helper()
	sink, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sink.Close() })
	ep, err := rtpio.Listen("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ep.Close)
	codec, _ := audio.CodecFor("PCMU/8000")
	if err := ep.Configure(codec, 0, -1, "127.0.0.1", sink.LocalAddr().(*net.UDPAddr).Port); err != nil {
		t.Fatal(err)
	}
	p := newParticipant(ref, KindPhone, "PCMU/8000", codec, ep, 0)
	p.answered.Store(true)
	return p, sink
}

func received(sink *net.UDPConn) bool {
	_ = sink.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 1500)
	_, err := sink.Read(buf)
	return err == nil
}

// TestROM036_LegAnsweredAsNotReceivingGetsNoRTP: a leg whose offer says it wants no RTP
// (a=inactive or a=sendonly, answered inactive or recvonly) is sent none until an offer asks for
// it again, so the answer is honest (RFC 3264 section 6.1).
func TestROM036_LegAnsweredAsNotReceivingGetsNoRTP(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	r := newRoom(n, "r1", false)
	held, heldSink := sinkParticipant(t, "p-held")
	other, otherSink := sinkParticipant(t, "p-other")
	for _, p := range []*Participant{held, other} {
		if err := r.add(p); err != nil {
			t.Fatal(err)
		}
	}
	held.setDirection(false, false, time.Now())
	r.tick()
	if received(heldSink) {
		t.Fatal("a leg on hold (inactive) was sent RTP")
	}
	if !received(otherSink) {
		t.Fatal("the other leg was sent no RTP")
	}
	held.setDirection(false, true, time.Now()) // recvonly: wants RTP again
	r.tick()
	if !received(heldSink) {
		t.Fatal("a recvonly leg was sent no RTP")
	}
}
