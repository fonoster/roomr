package main

import (
	"context"
	"path"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// fault is what the next Times calls to a Roomr method do: wait Stall, then fail with Code
// (codes.OK: served normally after the wait).
type fault struct {
	code  codes.Code
	stall time.Duration
	times int
}

// rpc is one Roomr request a node received, as proto JSON (camelCase, defaults included).
type rpc struct {
	At      int64          `json:"at"`
	Method  string         `json:"method"`
	Request map[string]any `json:"request"`
}

// faults are one node's scripted gRPC failures, by method name (CreateCall, ...), and the
// requests it received.
type faults struct {
	mu       sync.Mutex
	rules    map[string][]*fault
	requests []rpc
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

// received lists the requests of one method ("": every method), in order.
func (f *faults) received(method string) []rpc {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []rpc{}
	for _, r := range f.requests {
		if method == "" || r.Method == method {
			out = append(out, r)
		}
	}
	return out
}

var marshal = protojson.MarshalOptions{EmitUnpopulated: true}

// interceptor records every Roomr request, then runs the method's next fault, if any: the stall,
// then the refusal; health checks pass untouched.
func (f *faults) interceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		method := path.Base(info.FullMethod)
		if path.Dir(info.FullMethod) != "/roomr.v1.Roomr" {
			return next(ctx, req)
		}
		f.mu.Lock()
		r := rpc{At: time.Now().UnixMilli(), Method: method, Request: map[string]any{}}
		if m, ok := req.(proto.Message); ok {
			if data, err := marshal.Marshal(m); err == nil {
				_ = unmarshalJSON(data, &r.Request)
			}
		}
		f.requests = append(f.requests, r)
		var ft fault
		if queue := f.rules[method]; len(queue) > 0 {
			ft = *queue[0]
			if queue[0].times--; queue[0].times <= 0 {
				f.rules[method] = queue[1:]
			}
		}
		f.mu.Unlock()
		if ft.stall > 0 {
			select {
			case <-time.After(ft.stall):
			case <-ctx.Done():
				return nil, status.FromContextError(ctx.Err()).Err()
			}
		}
		if ft.code != codes.OK {
			return nil, status.Error(ft.code, "roomr-fake: scripted "+ft.code.String())
		}
		return next(ctx, req)
	}
}
