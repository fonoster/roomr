## Why

Platforms that drive Roomr (controllers, session endpoints such as Fonoster's apiserver and
AppBridge) need a node in their own tests. Today each platform writes its own imitation of the
node (Fonoster carries three: FakeRoomr, FakeSessionNode, FakeRoomrNode, about 2,700 lines), and
those imitations drift from the node they imitate. Roomr should ship the fake, built from the real
node, and prove it against the same contract suite as the real image (fonoster/roomr#13, #15).

## What Changes

- New command `roomr-fake` and image `fonoster/roomr-fake`: one process that hosts any number of
  real Roomr nodes, each with its own Roomr gRPC port, plus an in-process phone (the caller and
  callee bot) on loopback SIP and RTP. A small HTTP+JSON API creates nodes, places inbound calls,
  scripts callees, plays caller audio, sends DTMF, hangs up, silences a leg, ends a call with a
  given cause, injects gRPC faults, pre-fills rooms, skews heartbeats, kills a node and crashes the
  process.
- Internal hooks on the node used only by the fake: end a call with a given cause and SIP status,
  kill the node as a crash would, report a different room count in heartbeats, observe the app's
  messages, and tell the dialer which call_ref it dials. The real binary never uses them.
- The caller bot's callee can be scripted per call (by call_ref) and can ring until told to answer.
- New npm package `@fonoster/roomr`: the Roomr gRPC client with the protos (`@fonoster/roomr`),
  the fake's client (`@fonoster/roomr/fake`) and the contract suite (`@fonoster/roomr/contract`).
- CI runs the contract suite against the real node and the fake. The release publishes the fake
  image and the npm package with the node image.

## Capabilities

### New Capabilities
- `roomr-fake`: the fake node process, its control API, and the contract suite that keeps it
  identical to the real node on the wire.

### Modified Capabilities
None: the node's behavior is unchanged.

## Impact

- `cmd/roomr-fake` (new), `internal/node` (two hooks), `internal/caller` (per-call callee),
  `cmd/sipcaller` (callee endpoints, so the contract suite can drive the real node).
- `Dockerfile` gains a `fake` target; `release.yaml` publishes `fonoster/roomr-fake` and npm.
- `js/` (new): the `@fonoster/roomr` package and its tests.
