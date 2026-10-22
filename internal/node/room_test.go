package node

import (
	"sync"
	"testing"
	"time"

	"github.com/fonoster/roomr/internal/audio"
	"github.com/fonoster/roomr/internal/rtpio"
)

// slowByeLeg is an answered SIP leg whose BYE waits until release closes.
type slowByeLeg struct {
	*fakeLeg
	bye     chan struct{}
	release chan struct{}
}

func (l *slowByeLeg) Bye() error {
	close(l.bye)
	<-l.release
	return l.fakeLeg.Bye()
}

// TestROM029_ParticipantLeftBeforeRoomEnded: a room that ends while a removed participant is
// still being retired (its BYE in flight) sends participant.left before room.ended.
func TestROM029_ParticipantLeftBeforeRoomEnded(t *testing.T) {
	n := New(nil, Config{EmptyRoomTimeout: -1})
	defer n.Shutdown()
	var mu sync.Mutex
	var types []string
	n.published = func(subject string, ev Event) {
		if subject == RoomEventsSubject("lobby") {
			mu.Lock()
			types = append(types, ev["type"].(string))
			mu.Unlock()
		}
	}
	if _, err := n.CreateRoom("lobby", RoomOptions{}); err != nil {
		t.Fatal(err)
	}
	r := n.room("lobby")
	ep, err := rtpio.Listen("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	codec, _ := audio.CodecFor("PCMU/8000")
	p := newParticipant("p-1", KindPhone, "PCMU/8000", codec, ep, 0)
	leg := &slowByeLeg{fakeLeg: newFakeLeg(), bye: make(chan struct{}), release: make(chan struct{})}
	p.leg = leg
	p.answered.Store(true)
	if err := r.add(p); err != nil {
		t.Fatal(err)
	}
	r.participantEvent(p, "participant.joined", "")

	go r.remove(p.Ref, ReasonRemoved)
	<-leg.bye
	if err := n.EndRoom("lobby"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return n.room("lobby") == nil })
	close(leg.release)
	want := []string{"participant.joined", "participant.left", "room.ended"}
	eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(types) == len(want)
	})
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for i := range want {
		if len(types) != len(want) || types[i] != want[i] {
			t.Fatalf("room events %v, want %v", types, want)
		}
	}
}
