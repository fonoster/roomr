# The fake: testing without running Roomr yourself

`roomr-fake` is for the tests of platforms that drive Roomr: controllers that call Roomr gRPC and
apps that serve the session endpoint. It hosts any number of **real** Roomr nodes in one process.
Each node has its own Roomr gRPC address, heartbeats and events on NATS, and a phone: a SIP caller
bot on loopback that calls the node and answers what the node dials. Your tests script the phone,
inject faults and kill nodes over a small HTTP+JSON API.

Because the nodes are the real node, the fake behaves exactly like Roomr on the wire. A contract
suite (`@fonoster/roomr/contract`) runs against the real binary and the fake in this repository's CI
on every change, so the two cannot drift.

## Run it

```sh
# Docker: host networking, because each node takes ports at random and dials your session endpoint
docker run --rm --network host fonoster/roomr-fake -nats nats://127.0.0.1:4222

# Or the static binary from the GitHub release (macOS, no Docker)
./roomr-fake-darwin-arm64 -listen 127.0.0.1:8090 -nats nats://127.0.0.1:4222
```

| Flag | Default | |
| --- | --- | --- |
| `-listen` | `127.0.0.1:8090` (image: `0.0.0.0:8090`) | The control API |
| `-nats` | `nats://127.0.0.1:4222` | Heartbeats and events (`none`: no NATS) |
| `-host` | `127.0.0.1` | Where the nodes' gRPC, SIP and RTP bind |
| `-advertise` | `-host` | The address heartbeats and SDP announce |
| `-rtp-ports` | any | An RTP range, e.g. `20000-20999` |

The binaries are built without cgo, so the fake's nodes offer every codec except Opus.

## From Node.js

```ts
import { FakeRoomr } from "@fonoster/roomr/fake";
import { RoomrClient } from "@fonoster/roomr";

const fake = await FakeRoomr.start(); // $ROOMR_FAKE_BIN, else the image of this package's version
const node = await fake.createNode({ sessionEndpoint: "127.0.0.1:50070", maxRooms: 10 });

// A controller test: script the callee, then drive the node over gRPC
await node.callee("busy", { callRef: "c1" });
await new RoomrClient(node.controlAddr).createCall({ callRef: "c1", to: { uri: "sip:+15551230000@carrier" } });

// An app test: the phone calls the node, which opens a control stream to your app
const call = await node.placeCall({ headers: { "X-App-Ref": "app-1" } });
await call.speak({ kind: "tone", ms: 800 });
await call.dtmf("1");
await call.end("node_error"); // the node ends it with this cause
await fake.stop();
```

## The control API

Nodes:

| | |
| --- | --- |
| `POST /nodes` | Start a node. Body: `nodeRef`, `region`, `maxRooms`, `maxParticipants`, `rooms` (pre-filled rooms that count toward the limit and never end), `reportedRooms`, `heartbeatMs`, `sessionEndpoint`, `answerTimeoutMs`, `readyTimeoutMs`, `mediaTimeoutMs`, `emptyRoomTimeoutMs`, `maxDurationMs`, `codecs`, `callee: {mode, afterMs}`. Returns `nodeRef`, `controlAddr`, `sipAddr`, `rooms` |
| `GET /nodes`, `GET /nodes/{node}` | Nodes and their room counts |
| `PATCH /nodes/{node}` | `{"reportedRooms": 0}` makes heartbeats report 0 rooms; `null` restores the real count |
| `POST /nodes/{node}/callee` | `{"mode": "answer\|busy\|no_answer\|unavailable\|decline\|ring", "afterMs": 0, "callRef": "c1"}`: how the phone answers what the node dials, for one call_ref or every call. `ring` rings until `POST /calls/{call}/answer` |
| `POST /nodes/{node}/faults` | `{"method": "CreateCall", "code": "UNAVAILABLE", "times": 1}` fails the next calls; `{"method": "CreateRoom", "stallMs": 2500}` serves them late |
| `DELETE /nodes/{node}/faults` | Clears the faults |
| `GET /nodes/{node}/requests` | The Roomr requests the node received, as proto JSON (`?method=CreateCall` for one method) |
| `POST /nodes/{node}/calls` | The phone calls the node: `from`, `to`, `callRef` (sent as `X-Call-Ref`, the call's call_ref), `roomRef` (sent as `X-Room-Ref`), `headers`, `codecs` |
| `POST /nodes/{node}/down` | Drain: `node.down`, health NOT_SERVING, live rooms keep running |
| `POST /nodes/{node}/kill` | A crash: no `node.down`, heartbeats and gRPC stop at once, streams drop without Ended |
| `DELETE /nodes/{node}` | Stop gracefully: `node.down`, every call ends with `node_error` |
| `POST /reset` | Stop every node |
| `POST /crash` | Exit the process at once |

Calls, by call_ref:

| | |
| --- | --- |
| `GET /calls/{call}` | The node's `state` (`ringing`, `answered`, `ended`), `cause` and `sipStatus` once ended, `roomRef`, `held`, and the phone's side: SIP statuses, heard and audible audio, keys received. `?since=<unix ms>` adds audio measures from then on |
| `GET /calls/{call}/commands` | What the app sent on the call's control stream, in order: `{"commands": [{"at", "callRef", "command": {"type": "answer", ...}}]}` |
| `POST /calls/{call}/answer` | Answer a callee ringing in mode `ring` |
| `POST /calls/{call}/end` | The node ends the call: `{"cause": "node_error", "sipStatus": 503}` |
| `POST /calls/{call}/hangup` | The phone hangs up |
| `POST /calls/{call}/audio` | The phone speaks: `{"kind": "tone\|tone:<hz>\|silence", "ms": 800}` or `{"kind": "pcm", "pcm": "<base64 16 kHz s16le>"}` |
| `POST /calls/{call}/dtmf` | `{"digits": "12#", "mode": "rfc4733\|info\|inband"}` |
| `POST /calls/{call}/mute` | `{"muted": true}`: the phone stops sending RTP, so the call ends with `media_timeout` |
| `GET /calls/{call}/heard` | What the phone heard, raw 16 kHz s16le |

## The contract suite

```js
// mocha
const { describeContract, fakeTarget, realTarget } = require("@fonoster/roomr/contract");
describeContract(() => fakeTarget());
```

Cases `CON-001` and up cover heartbeats, room refs and limits, outbound and inbound calls, keys,
audio, hangups, EndCall, Reject and draining. `realTarget()` runs the `roomr` and `sipcaller`
binaries from `$ROOMR_BIN_DIR`.
