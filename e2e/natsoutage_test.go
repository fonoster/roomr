package e2e

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	roomrv1 "github.com/fonoster/roomr/api/roomr/v1"
	"github.com/fonoster/roomr/internal/node"
)

// NATS is best effort (media-control, "Events are best effort while NATS is down"): the node
// runs with the production NATS options behind a proxy that can cut the broker away.

const outage = 60 * time.Second

// seqsOn is the seq of every event the rig's direct subscriber saw on subject, in arrival order.
func seqsOn(r *rig, subject string) []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []int
	for _, ev := range r.events {
		if ev["subject"] == subject {
			out = append(out, int(ev["seq"].(float64)))
		}
	}
	return out
}

// cutNATS starts an outage and waits until the node's client notices it.
func cutNATS(t *testing.T, r *rig, p *natsProxy) {
	t.Helper()
	p.cut()
	for deadline := time.Now().Add(5 * time.Second); r.nodeNC.IsConnected(); {
		if time.Now().After(deadline) {
			t.Fatal("the node's NATS connection did not drop")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// restoreNATS ends the outage and waits for the node's client to reconnect by itself.
func restoreNATS(t *testing.T, r *rig, p *natsProxy) {
	t.Helper()
	p.restore()
	for deadline := time.Now().Add(15 * time.Second); !r.nodeNC.IsConnected(); {
		if time.Now().After(deadline) {
			t.Fatal("the node did not reconnect to NATS")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestROM030_CallSurvivesSixtySecondNATSOutage: a call keeps both audio directions and the node
// keeps serving RPCs through a 60 s outage; events published after NATS returns arrive, and seq
// keeps counting where it stopped, skipping exactly the events dropped meanwhile (never reset,
// repeated or replayed), per call and per room.
func TestROM030_CallSurvivesSixtySecondNATSOutage(t *testing.T) {
	proxy := newNATSProxy(t, envNATS())
	r := startWith(t, rigOptions{natsProxy: proxy})

	call, ac := r.answeredCall()
	roomA := ac.start.GetRoomRef()
	// A non-call participant of the call's room puts events on the room's subject.
	if _, _, err := r.join(roomA, ""); err != nil {
		t.Fatal(err)
	}
	// Call B rings, then is answered during the outage: its answered and two joined events are lost.
	refB := fmt.Sprintf("outage-b-%d", time.Now().UnixNano())
	r.callee.Callee("ring", 0)
	r.callee.KeyHeader("X-Call-Ref")
	b, err := r.mc.CreateCall(rpcCtx(t), &roomrv1.CreateCallRequest{CallRef: refB, RingTimeoutS: 120,
		To: &roomrv1.SipTarget{Uri: "sip:+1555@carrier.invalid", Proxy: r.callee.Address(),
			Headers: map[string]string{"X-Call-Ref": refB}}})
	if err != nil {
		t.Fatal(err)
	}
	r.waitEvent(typed("call.ringing", "call_ref", refB), 3*time.Second)
	refA := r.waitEvent(typed("call.started", "room_ref", roomA), 3*time.Second)["call_ref"].(string)
	subA, subB, subRoom := node.CallEventsSubject(refA), node.CallEventsSubject(refB), node.RoomEventsSubject(roomA)
	time.Sleep(500 * time.Millisecond)
	preA := seqsOn(r, subA)
	for i, seq := range preA {
		if seq != i+1 {
			t.Fatalf("call A events before the outage: %v", preA)
		}
	}
	if len(preA) < 3 {
		t.Fatalf("call A events before the outage: %v", preA)
	}
	if got := seqsOn(r, subRoom); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("room events before the outage: %v", got)
	}

	cutNATS(t, r, proxy)
	if !r.callee.AnswerRinging(refB) {
		t.Fatal("call B was not ringing")
	}
	deadline := time.Now().Add(5 * time.Second)
	for r.node.Participants(b.RoomRef) < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := r.node.Participants(b.RoomRef); n < 2 {
		t.Fatalf("call B has %d participants after being answered", n)
	}
	// RPCs still work: this join's participant.joined cannot be published.
	_, p2, err := r.join(roomA, "")
	if err != nil {
		t.Fatalf("AddParticipant during the outage: %v", err)
	}

	// 60 s of audio both ways, checked in 10 s windows.
	call.Speak("tone:1000", int(outage/time.Millisecond)+15000)
	go ac.play(600, int(outage/time.Millisecond)+15000)
	start := time.Now()
	for w := 0; w < 6; w++ {
		heard, audible := ac.m.get(1000), call.Status().AudibleMs
		time.Sleep(10 * time.Second)
		if got := ac.m.get(1000) - heard; got < 400 {
			t.Fatalf("window %d: the app heard %d frames of the caller's tone, want about 500", w, got)
		}
		if got := call.Status().AudibleMs - audible; got < 8000 {
			t.Fatalf("window %d: the caller heard %d ms of the app, want about 10000", w, got)
		}
		if s := call.Status(); s.State != "answered" || ac.closed() {
			t.Fatalf("window %d: call state %q, stream closed %v", w, s.State, ac.closed())
		}
	}
	if d := time.Since(start); d < outage {
		t.Fatalf("outage lasted %v", d)
	}
	if got := seqsOn(r, subA); len(got) != len(preA) {
		t.Fatalf("call A events arrived during the outage: %v", got)
	}

	restoreNATS(t, r, proxy)
	// Nothing dropped during the outage is replayed.
	time.Sleep(3 * time.Second)
	if got := seqsOn(r, subB); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("call B events after reconnecting: %v, want only 1 and 2 (answered and joined were dropped)", got)
	}
	if got := seqsOn(r, subRoom); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("room events after reconnecting: %v, want only 1", got)
	}
	// Audio still flows after reconnecting.
	heard := ac.m.get(1000)
	time.Sleep(time.Second)
	if ac.m.get(1000)-heard < 30 {
		t.Fatal("no audio after NATS returned")
	}

	// Events after the outage continue each counter: B after 2 plus the 3 dropped, the room after
	// 1 plus the 1 dropped, A (nothing dropped) right after its last.
	if _, err := r.mc.RemoveParticipant(rpcCtx(t), &roomrv1.RemoveParticipantRequest{RoomRef: roomA, ParticipantRef: p2.ParticipantRef}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.mc.EndCall(rpcCtx(t), &roomrv1.EndCallRequest{CallRef: refB}); err != nil {
		t.Fatal(err)
	}
	_ = call.Hangup()
	r.waitEvent(typed("call.ended", "call_ref", refA), 3*time.Second)
	r.waitEvent(typed("call.ended", "call_ref", refB), 3*time.Second)
	time.Sleep(300 * time.Millisecond)
	if got, want := seqsOn(r, subB), []int{1, 2, 6, 7, 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("call B seq = %v, want %v", got, want)
	}
	// After 3 come the events of the room ending with call A (the other participant leaves, room.ended).
	if got := seqsOn(r, subRoom); len(got) < 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("room seq = %v, want 1 then 3 (2 was dropped)", got)
	} else {
		for i := 2; i < len(got); i++ {
			if got[i] != got[i-1]+1 {
				t.Fatalf("room seq = %v, not contiguous after the outage", got)
			}
		}
	}
	gotA := seqsOn(r, subA)
	for i := range gotA {
		if gotA[i] != i+1 {
			t.Fatalf("call A seq = %v, want contiguous from 1", gotA)
		}
	}
	if len(gotA) <= len(preA) {
		t.Fatalf("no call A event after NATS returned: %v", gotA)
	}
}

// TestROM030_CallEndedDuringOutageIsNotReplayed: a call that ends while NATS is unreachable frees
// its room, and neither its call.ended nor its other events appear once NATS returns.
func TestROM030_CallEndedDuringOutageIsNotReplayed(t *testing.T) {
	proxy := newNATSProxy(t, envNATS())
	r := startWith(t, rigOptions{natsProxy: proxy})

	call, ac := r.answeredCall()
	room := ac.start.GetRoomRef()
	ref := r.waitEvent(typed("call.started", "room_ref", room), 3*time.Second)["call_ref"].(string)
	time.Sleep(500 * time.Millisecond)
	sub := node.CallEventsSubject(ref)
	pre := seqsOn(r, sub)

	cutNATS(t, r, proxy)
	_ = call.Hangup()
	ac.ended(t, 3*time.Second)
	roomGone(t, r, room)
	time.Sleep(2 * time.Second)

	restoreNATS(t, r, proxy)
	time.Sleep(5 * time.Second)
	if got := seqsOn(r, sub); !reflect.DeepEqual(got, pre) {
		t.Fatalf("events on the call subject after reconnecting: %v, were %v before the outage", got, pre)
	}
	if ev := r.find(typed("call.ended", "call_ref", ref)); ev != nil {
		t.Fatalf("call.ended was published after NATS returned: %v", ev)
	}
	if ev := r.find(typed("room.ended", "room_ref", room)); ev != nil {
		t.Fatalf("room.ended was published after NATS returned: %v", ev)
	}
	// Publishing works again: a new call's events arrive.
	_, ac2 := r.answeredCall()
	r.waitEvent(typed("call.started", "room_ref", ac2.start.GetRoomRef()), 3*time.Second)
}
