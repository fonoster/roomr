package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/fonoster/roomr/internal/caller"
	"google.golang.org/grpc/codes"
)

// nodeView is a node as the API reports it.
type nodeView struct {
	NodeRef     string `json:"nodeRef"`
	ControlAddr string `json:"controlAddr"`
	SIPAddr     string `json:"sipAddr"`
	Rooms       int    `json:"rooms"`
}

func (fn *fakeNode) view() nodeView {
	return nodeView{NodeRef: fn.ref, ControlAddr: fn.controlAddr, SIPAddr: fn.sipAddr, Rooms: fn.n.ActiveRooms()}
}

// callView is a call as the API reports it: the node's state and the phone's side.
type callView struct {
	CallRef   string `json:"callRef"`
	NodeRef   string `json:"nodeRef"`
	Direction string `json:"direction"`
	// State is the node's: ringing, answered or ended ("" once the node forgot the call).
	State   string `json:"state"`
	RoomRef string `json:"roomRef,omitempty"`
	Held    bool   `json:"held"`
	// Phone is the caller (inbound) or the callee that answered (outbound).
	Phone *caller.Status `json:"phone,omitempty"`
}

// routes is the control API (docs/FAKE.md).
func (f *fake) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(204) })

	mux.HandleFunc("POST /nodes", func(w http.ResponseWriter, r *http.Request) {
		var o nodeOptions
		if !decode(w, r, &o) {
			return
		}
		fn, err := f.createNode(o)
		switch {
		case errors.Is(err, errNodeExists):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, http.StatusCreated, fn.view())
		}
	})
	mux.HandleFunc("GET /nodes", func(w http.ResponseWriter, _ *http.Request) {
		views := []nodeView{}
		for _, fn := range f.all() {
			views = append(views, fn.view())
		}
		writeJSON(w, 200, map[string]any{"nodes": views})
	})
	// Stops every node (between tests).
	mux.HandleFunc("POST /reset", func(w http.ResponseWriter, _ *http.Request) {
		for _, fn := range f.all() {
			if f.remove(fn.ref) != nil {
				fn.stop()
			}
		}
		w.WriteHeader(204)
	})
	// Exits the process at once, as a crash would: every node's streams drop without Ended.
	mux.HandleFunc("POST /crash", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(202)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		go func() {
			time.Sleep(50 * time.Millisecond)
			os.Exit(137)
		}()
	})

	withNode := func(h func(fn *fakeNode, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fn := f.node(r.PathValue("node"))
			if fn == nil {
				http.Error(w, "no such node", http.StatusNotFound)
				return
			}
			h(fn, w, r)
		}
	}
	mux.HandleFunc("GET /nodes/{node}", withNode(func(fn *fakeNode, w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, fn.view())
	}))
	mux.HandleFunc("PATCH /nodes/{node}", withNode(func(fn *fakeNode, w http.ResponseWriter, r *http.Request) {
		var req struct{ ReportedRooms *int }
		if !decode(w, r, &req) {
			return
		}
		if req.ReportedRooms == nil {
			fn.reported.Store(-1)
		} else {
			fn.reported.Store(int64(*req.ReportedRooms))
		}
		writeJSON(w, 200, fn.view())
	}))
	mux.HandleFunc("DELETE /nodes/{node}", withNode(func(fn *fakeNode, w http.ResponseWriter, _ *http.Request) {
		if f.remove(fn.ref) != nil {
			fn.stop()
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /nodes/{node}/down", withNode(func(fn *fakeNode, w http.ResponseWriter, _ *http.Request) {
		fn.down()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /nodes/{node}/kill", withNode(func(fn *fakeNode, w http.ResponseWriter, _ *http.Request) {
		if f.remove(fn.ref) != nil {
			fn.kill()
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /nodes/{node}/faults", withNode(func(fn *fakeNode, w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method  string
			Code    codes.Code
			StallMs int
			Times   int
		}
		if !decode(w, r, &req) {
			return
		}
		switch req.Method {
		case "CreateCall", "EndCall", "CreateRoom", "EndRoom", "AddParticipant", "RemoveParticipant":
		default:
			http.Error(w, "method must be a Roomr RPC (CreateCall, EndCall, ...)", http.StatusBadRequest)
			return
		}
		if req.Times <= 0 {
			req.Times = 1
		}
		fn.faults.add(req.Method, &fault{code: req.Code, stall: ms(req.StallMs), times: req.Times})
		w.WriteHeader(204)
	}))
	mux.HandleFunc("DELETE /nodes/{node}/faults", withNode(func(fn *fakeNode, w http.ResponseWriter, _ *http.Request) {
		fn.faults.clear()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /nodes/{node}/callee", withNode(func(fn *fakeNode, w http.ResponseWriter, r *http.Request) {
		var c calleeOptions
		if !decode(w, r, &c) {
			return
		}
		switch c.Mode {
		case "", "answer", "busy", "no_answer", "unavailable", "decline", "ring":
		default:
			http.Error(w, "mode must be answer, busy, no_answer, unavailable, decline or ring", http.StatusBadRequest)
			return
		}
		fn.script(c)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /nodes/{node}/calls", withNode(func(fn *fakeNode, w http.ResponseWriter, r *http.Request) {
		var req inboundCall
		if !decode(w, r, &req) {
			return
		}
		ref, err := fn.placeCall(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusCreated, f.callView(fn, ref, time.Time{}))
	}))

	// Calls, by call_ref, on whichever node holds them.
	withCall := func(h func(fn *fakeNode, ref string, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			ref := r.PathValue("call")
			fn := f.nodeOf(ref)
			if fn == nil {
				http.Error(w, "no such call", http.StatusNotFound)
				return
			}
			h(fn, ref, w, r)
		}
	}
	withPhone := func(h func(c *caller.Call, w http.ResponseWriter, r *http.Request)) http.HandlerFunc {
		return withCall(func(fn *fakeNode, ref string, w http.ResponseWriter, r *http.Request) {
			c, _ := fn.phoneCall(ref)
			if c == nil {
				http.Error(w, "the phone has no leg on that call (still ringing?)", http.StatusConflict)
				return
			}
			h(c, w, r)
		})
	}
	mux.HandleFunc("GET /calls/{call}", withCall(func(fn *fakeNode, ref string, w http.ResponseWriter, r *http.Request) {
		// ?since=<unix ms> adds the phone's audio measurements from that time on.
		since := time.Time{}
		if v, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64); err == nil {
			since = time.UnixMilli(v)
		}
		writeJSON(w, 200, f.callView(fn, ref, since))
	}))
	mux.HandleFunc("POST /calls/{call}/answer", withCall(func(fn *fakeNode, ref string, w http.ResponseWriter, _ *http.Request) {
		if !fn.phone.AnswerRinging(ref) {
			http.Error(w, "no callee rings for that call (callee mode ring)", http.StatusConflict)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /calls/{call}/end", withCall(func(fn *fakeNode, ref string, w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cause     string
			SipStatus int
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Cause == "" {
			req.Cause = "node_error"
		}
		if err := fn.n.EndCallWith(ref, req.Cause, req.SipStatus); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /calls/{call}/hangup", withPhone(func(c *caller.Call, w http.ResponseWriter, _ *http.Request) {
		_ = c.Hangup()
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /calls/{call}/audio", withPhone(func(c *caller.Call, w http.ResponseWriter, r *http.Request) {
		// {"kind":"tone|tone:<hz>|silence","ms":800} or {"kind":"pcm","pcm":"<base64 16 kHz s16le>"}
		var req struct {
			Kind string
			Ms   int
			PCM  []byte `json:"pcm"`
		}
		if !decode(w, r, &req) {
			return
		}
		if req.Kind == "pcm" {
			c.SpeakPCM(req.PCM)
		} else {
			c.Speak(req.Kind, req.Ms)
		}
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /calls/{call}/dtmf", withPhone(func(c *caller.Call, w http.ResponseWriter, r *http.Request) {
		var req struct{ Digits, Mode string }
		if !decode(w, r, &req) {
			return
		}
		if req.Mode == "" {
			req.Mode = "rfc4733"
		}
		go c.DTMFVia(req.Digits, req.Mode)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("POST /calls/{call}/mute", withPhone(func(c *caller.Call, w http.ResponseWriter, r *http.Request) {
		req := struct{ Muted *bool }{}
		if !decode(w, r, &req) {
			return
		}
		c.Mute(req.Muted == nil || *req.Muted)
		w.WriteHeader(204)
	}))
	mux.HandleFunc("GET /calls/{call}/heard", withPhone(func(c *caller.Call, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(c.Heard())
	}))
	return mux
}

// nodeOf is the node that holds a call: the one whose phone placed it, else the one that knows it.
func (f *fake) nodeOf(ref string) *fakeNode {
	nodes := f.all()
	for _, fn := range nodes {
		fn.mu.Lock()
		_, ok := fn.inbound[ref]
		fn.mu.Unlock()
		if ok {
			return fn
		}
	}
	for _, fn := range nodes {
		if _, err := fn.n.GetCall(ref); err == nil {
			return fn
		}
	}
	return nil
}

func (f *fake) callView(fn *fakeNode, ref string, since time.Time) callView {
	v := callView{CallRef: ref, NodeRef: fn.ref}
	if info, err := fn.n.GetCall(ref); err == nil {
		v.State, v.RoomRef = info.State, info.RoomRef
		v.Held = info.RoomRef != "" && fn.n.Held(info.RoomRef)
	}
	c, dir := fn.phoneCall(ref)
	v.Direction = dir
	if c != nil {
		st := c.StatusSince(since)
		v.Phone = &st
	}
	return v
}

// decode reads a JSON body; an empty body is an empty request.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.ContentLength == 0 {
		return true
	}
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
