package node

import (
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

// TestROM036_HoldPausesTheMediaTimeout: while the caller's SDP says it is not sending (a=inactive
// or a=recvonly) its media timeout is paused; an offer that restores sending starts the count
// again from then (roomr, "A silent leg ends the call", scenario "Caller on hold").
func TestROM036_HoldPausesTheMediaTimeout(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since

	p.setSending(false, t0.Add(time.Second))
	c := answeredCall(n, p, t0)
	c.checkLimits(t0.Add(time.Hour - time.Minute))
	if c.ending {
		t.Fatalf("a held caller ended with %q", c.cause)
	}
	if got := p.silentFor(t0.Add(30 * time.Second)); got != 0 {
		t.Fatalf("silentFor while held = %v", got)
	}
	if got := p.heldFor(t0.Add(31 * time.Second)); got != 30*time.Second {
		t.Fatalf("heldFor = %v", got)
	}

	// A second offer that still does not send keeps the hold's start.
	p.setSending(false, t0.Add(40*time.Second))
	if got := p.heldFor(t0.Add(41 * time.Second)); got != 40*time.Second {
		t.Fatalf("heldFor after a repeated hold = %v", got)
	}

	// Back to sendrecv at 60 s: the media timeout counts from then.
	resumed := t0.Add(60 * time.Second)
	p.setSending(true, resumed)
	if got := p.heldFor(resumed.Add(time.Second)); got != 0 {
		t.Fatalf("heldFor after resume = %v", got)
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

// TestROM036_MediaHoldTimeoutEndsALongHold: with mediaHoldTimeout set, a call held that long ends
// with media_timeout; without it (the default) a hold never ends the call.
func TestROM036_MediaHoldTimeoutEndsALongHold(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, MediaHoldTimeout: 10 * time.Minute, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since
	p.setSending(false, t0)
	c := answeredCall(n, p, t0)
	c.checkLimits(t0.Add(9 * time.Minute))
	if c.ending {
		t.Fatalf("ended %q before the hold timeout", c.cause)
	}
	c.checkLimits(t0.Add(11 * time.Minute))
	if !c.ending || c.cause != ReasonMediaTimeout {
		t.Fatalf("long hold: ending %v cause %q", c.ending, c.cause)
	}
}

// TestROM036_SendingCallerKeepsTheTimer: a caller whose offer still sends (sendonly: music on
// hold) is not held, so its silence ends the call after the media timeout as usual.
func TestROM036_SendingCallerKeepsTheTimer(t *testing.T) {
	n := New(nil, Config{MediaTimeout: 15 * time.Second, EmptyRoomTimeout: -1})
	defer n.Shutdown()
	p := heldParticipant(t)
	t0 := p.since
	p.setSending(true, t0.Add(time.Second))
	c := answeredCall(n, p, t0)
	c.checkLimits(t0.Add(16 * time.Second))
	if !c.ending || c.cause != ReasonMediaTimeout {
		t.Fatalf("silent sendonly caller: ending %v cause %q", c.ending, c.cause)
	}
}
