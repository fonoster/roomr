package main

import (
	"sync"
	"time"

	"github.com/fonoster/roomr/internal/node"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fault is what the next Times calls to a Roomr method do: wait Stall, then fail with Code
// (codes.OK: served normally after the wait).
type fault struct {
	code  codes.Code
	stall time.Duration
	times int
}

// faults are the scripted failures of one node's Roomr gRPC, by method name (CreateCall, ...).
type faults struct {
	mu    sync.Mutex
	rules map[string][]*fault
}

func newFaults() *faults { return &faults{rules: map[string][]*fault{}} }

func (f *faults) add(method string, ft *fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[method] = append(f.rules[method], ft)
}

func (f *faults) clear() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules = map[string][]*fault{}
}

// apply runs the method's next fault, if any: the stall, then the refusal.
func (f *faults) apply(method string) error {
	f.mu.Lock()
	var ft fault
	queue := f.rules[method]
	if len(queue) > 0 {
		ft = *queue[0]
		if queue[0].times--; queue[0].times <= 0 {
			f.rules[method] = queue[1:]
		}
	}
	f.mu.Unlock()
	if ft.stall > 0 {
		time.Sleep(ft.stall)
	}
	if ft.code != codes.OK {
		return status.Error(ft.code, "roomr-fake: scripted "+ft.code.String())
	}
	return nil
}

// faultyBackend is the node behind the Roomr gRPC service, with the scripted faults in front.
type faultyBackend struct {
	n *node.Node
	f *faults
}

func (b *faultyBackend) CreateCall(req node.CallRequest) (node.CallInfo, error) {
	if err := b.f.apply("CreateCall"); err != nil {
		return node.CallInfo{}, err
	}
	return b.n.CreateCall(req)
}

func (b *faultyBackend) EndCall(ref string) error {
	if err := b.f.apply("EndCall"); err != nil {
		return err
	}
	return b.n.EndCall(ref)
}

func (b *faultyBackend) CreateRoom(ref string, o node.RoomOptions) (string, error) {
	if err := b.f.apply("CreateRoom"); err != nil {
		return "", err
	}
	return b.n.CreateRoom(ref, o)
}

func (b *faultyBackend) EndRoom(ref string) error {
	if err := b.f.apply("EndRoom"); err != nil {
		return err
	}
	return b.n.EndRoom(ref)
}

func (b *faultyBackend) AddRTPParticipant(roomRef string, ep node.MediaEndpoint) (node.ParticipantInfo, error) {
	if err := b.f.apply("AddParticipant"); err != nil {
		return node.ParticipantInfo{}, err
	}
	return b.n.AddRTPParticipant(roomRef, ep)
}

func (b *faultyBackend) AddListenerParticipant(roomRef string, tracks []node.ListenTrack) (node.ParticipantInfo, error) {
	if err := b.f.apply("AddParticipant"); err != nil {
		return node.ParticipantInfo{}, err
	}
	return b.n.AddListenerParticipant(roomRef, tracks)
}

func (b *faultyBackend) RemoveParticipant(roomRef, ref string) error {
	if err := b.f.apply("RemoveParticipant"); err != nil {
		return err
	}
	return b.n.RemoveParticipant(roomRef, ref)
}
