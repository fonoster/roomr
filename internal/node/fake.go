package node

import "context"

// Hooks for roomr-fake (openspec add-roomr-fake, "The fake injects faults"): the fake runs this
// node and uses them to script what a real node only does on its own. The roomr binary never
// calls them.

// EndCallWith ends a call with cause and SIP status, as if the node had decided it: the app gets
// Ended with them and an unanswered inbound caller gets the status (480 when it is not 3xx-6xx).
func (n *Node) EndCallWith(ref, cause string, status int) error {
	c := n.call(ref)
	if c == nil {
		return errorf(CodeCallNotFound, "%s", ref)
	}
	c.end(cause, status)
	return nil
}

// StopAppMedia stops (or resumes) the node's RTP to a call's app leg while the call and its
// control stream go on, as a node whose media path broke would.
func (n *Node) StopAppMedia(ref string, stopped bool) error {
	c := n.call(ref)
	if c == nil {
		return errorf(CodeCallNotFound, "%s", ref)
	}
	c.mu.Lock()
	app := c.app
	c.mu.Unlock()
	if app == nil {
		return errorf(CodeParticipantNotFound, "call %s has no app leg", ref)
	}
	app.stopped.Store(stopped)
	return nil
}

// Kill stops the node as a crash would: the heartbeats stop without node.down, every call's
// control stream drops without Ended, and nothing more is published on NATS.
func (n *Node) Kill() {
	n.killed.Store(true)
	n.downOnce.Do(func() {
		n.beatMu.Lock()
		close(n.stop)
		n.beatMu.Unlock()
	})
	n.mu.Lock()
	calls := make([]*call, 0, len(n.calls))
	for _, c := range n.calls {
		calls = append(calls, c)
	}
	n.mu.Unlock()
	for _, c := range calls {
		c.mu.Lock()
		sess := c.sess
		c.mu.Unlock()
		if sess != nil {
			sess.abort()
		}
	}
	n.endAll(ReasonNodeError)
}

// abort drops the stream at once, sending nothing more (a crashed node's stream).
func (s *appStream) abort() {
	s.sendMu.Lock()
	s.closed = true
	s.sendMu.Unlock()
	s.cancel()
	_ = s.conn.Close()
}

type callRefKey struct{}

func withCallRef(ctx context.Context, ref string) context.Context {
	return context.WithValue(ctx, callRefKey{}, ref)
}

// CallRefOf is the call_ref a TargetDialer is dialing for ("": a transfer target, or unknown).
func CallRefOf(ctx context.Context) string {
	ref, _ := ctx.Value(callRefKey{}).(string)
	return ref
}
