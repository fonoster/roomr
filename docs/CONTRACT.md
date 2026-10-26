# The Roomr contract

Roomr has three interfaces, and nothing else is public:

| Interface | Direction | Defined in |
| --- | --- | --- |
| `roomr.v1.Roomr` gRPC service | controller **to** node | [`api/roomr/v1/roomr.proto`](../api/roomr/v1/roomr.proto) |
| `session.v1.Session` gRPC stream | node **to** app, one per call | [`api/session/v1/session.proto`](../api/session/v1/session.proto) |
| RTP, and NATS subjects | node **to** app (audio), node **to** anyone (events) | this document |

The `.proto` files are the source of truth for field numbers and types; this page explains
behavior and the rules around them. Normative requirements, with scenarios, are in
[`openspec/specs/media-control`](../openspec/specs/media-control/spec.md) and
[`openspec/specs/roomr`](../openspec/specs/roomr/spec.md).

## Compatibility rules

- **Messages only grow.** Fields are added, never renumbered, reused or removed. Receivers ignore
  fields they do not know. Removed fields are `reserved` in the proto.
- **The package is the major version** (`roomr.v1`, `session.v1`). A breaking change would be a
  new package served beside the old one.
- A node release says in its changelog which fields it added. A controller or app written against
  an older proto keeps working against a newer node.

## `roomr.v1.Roomr`: how a controller drives a node

Served on `control.grpc.listen` (default `:50061`, plus `grpc.health.v1.Health`).

| RPC | Request | Reply | Behavior |
| --- | --- | --- | --- |
| `CreateCall` | `call_ref`, `to` (a `SipTarget`), `from`, `room_ref`, `session`, `ring_timeout_s`, `metadata`, `max_duration_s` | `Call` | Dials `to`. Returns at once with `Call{RINGING}`. `call_ref` is idempotent for ten minutes. |
| `EndCall` | `call_ref` | `Empty` | CANCEL while ringing, BYE once answered, `480` to an unanswered inbound caller. Cause `removed`. |
| `CreateRoom` | `room_ref`, `max_duration_s`, `empty_room_timeout_s`, `max_participants` | `Room` | Creates an empty room. A taken ref is `ALREADY_EXISTS`. |
| `EndRoom` | `room_ref` | `Empty` | Ends every call and participant in the room (cause `removed`). |
| `AddParticipant` | `room_ref`, one of `rtp` or `webrtc`, `listen` tracks | `Participant` | Adds an RTP participant, or a listen-only one. WebRTC is `UNIMPLEMENTED`. |
| `RemoveParticipant` | `room_ref`, `participant_ref` | `Empty` | Removes a participant. |

`SipTarget` is `uri`, `proxy` (the transport lives in its URI: `sip:host:5060;transport=tcp`),
`headers` (sent byte for byte) and `auth` (answers a 401/407 digest challenge). A CR or LF in a
header value, or a header the node sets itself (Via, From, To, Call-ID, CSeq, Contact,
Max-Forwards, Route, Record-Route, Authorization, Proxy-Authorization, Expires, `Content-*`), is
`INVALID_ARGUMENT`.

### Errors

gRPC codes with a `google.rpc.ErrorInfo` whose domain is `roomr.fonoster.com`:

| Code | Reasons |
| --- | --- |
| `RESOURCE_EXHAUSTED` | `at_capacity`, `room_full` |
| `NOT_FOUND` | `room_not_found`, `call_not_found`, `participant_not_found` |
| `ALREADY_EXISTS` | `room_exists`, `call_exists` (same `call_ref`, different parameters) |
| `INVALID_ARGUMENT` | `bad_message`, `unsupported_codec` |
| `UNIMPLEMENTED` | `not_supported` |
| `INTERNAL` | `internal` (anything the node did not expect) |

## `session.v1.Session`: the per-call stream

`rpc Connect(stream NodeMessage) returns (stream AppMessage)`. The **node is the client** and opens
a **new connection for every call** to the session endpoint (`sessionEndpoint`, or a call's own
`session`). Messages are handled in order. Audio never travels here.

### Node to app (`NodeMessage`)

| Message | When | Carries |
| --- | --- | --- |
| `Start` | First, always | `call_ref`, `direction` (`INBOUND` or `OUTBOUND`), `from`, `to`, every INVITE `headers`, `metadata` (CreateCall's, untouched), `room_ref`, the node's RTP `media` endpoint, `node_ref` |
| `Dtmf` | A digit was received | `digit`, `source` (`rfc4733`, `sip_info`, `inband`) |
| `Event.participant_joined` / `participant_left` | A leg joined or left the room | `participant_ref`, `track_id` / `cause` |
| `Event.transfer_result` | A transfer finished | `status` (`completed`, `referred`, `failed`), `cause`, `sip_status` |
| `Event.ended` | The call is over; the node then half-closes the stream | `cause`, `sip_status` |

### App to node (`AppMessage`)

| Message | Use |
| --- | --- |
| `Answer{media, max_duration_s}` | Inbound: take the call; the node answers `200 OK`. `max_duration_s` lowers the call's limit. |
| `Ready{media}` | Outbound: the app is ready for the answered call. |
| `Reject{sip_status, reason}` | Inbound: refuse with a SIP status (4xx to 6xx). |
| `Transfer{to, mode, music, ring_timeout_s}` | `mode` is `cold`, `warm` or `refer`. `music` plays to the caller during a warm transfer. |
| `TransferComplete{}` | Warm: the app's leg leaves and the caller joins the new party. |
| `Hold{music}` / `Resume{}` | Silence the app and play looped hold music to the caller, and undo it. |
| `SendDtmf{digits}` | RFC 4733 events on the caller's leg, in order. Digits `0-9 * # A-D`. |
| `AddListener{listener_ref, listen[]}` / `RemoveListener{listener_ref}` | Fork audio by track to RTP endpoints. This is how recording works. |
| `Hangup{sip_status, reason}` | End the call. |

### Stream lifecycle

- The node sends keepalive pings every 10 seconds; **the app's gRPC server must allow them**. A
  stream that breaks, or misses two pings, ends the call with `app_gone`. It is never reconnected.
- No `Answer` within `answerTimeout` (inbound) gives the caller `480`; no `Ready` within
  `readyTimeout` (outbound) hangs up with `app_gone`.
- After `Ended` the node half-closes. The app should close its side.

### `Ended.cause`

`caller_hangup`, `callee_hangup`, `busy`, `no_answer`, `rejected`, `media_timeout` (the caller leg
went silent, or stayed on hold past `mediaHoldTimeout`), `node_error`, `max_duration`, `removed` (EndCall or EndRoom), `app_gone` (the app leg
went silent or the stream broke). `sip_status` is set when SIP gave one.

## Media

- **App leg:** RTP, payload type 96, **L16 at 16000 Hz**, mono, 20 ms packets, between the node's
  `MediaEndpoint` (in `Start`) and the app's (in `Answer` or `Ready`). `MediaEndpoint` is
  `ip`, `port`, `codec` (`"L16/16000"`) and `srtp_key`.
- **SRTP (SDES):** `Start.media.srtp_key` is always set. If the app answers with its own
  `srtp_key`, the leg is SRTP; if it leaves it empty, plain RTP.
- **Phone leg:** negotiated by SDP with Opus, G.722, PCMU and PCMA (configurable order). The room
  mixes at 16 kHz.
- **In-dialog offers (hold):** a re-INVITE or UPDATE on an answered phone leg may change the media
  direction only (hold and resume): it is answered `200 OK` with the same stream and the mirrored
  direction, and the node sends no RTP to a leg that says it receives none. An offer that moves
  the remote's address or port, drops the negotiated codec, changes the SRTP key or moves
  telephone-event is answered `488 Not Acceptable Here` and changes nothing; one before the answer
  gets `500` with `Retry-After`.
- **Mix-minus:** every participant hears everyone except themselves. A listener hears the tracks it
  asked for and sends nothing.

## NATS (best effort)

Core NATS only. The node runs with NATS down, buffers nothing and does not replay.

| Subject | Messages |
| --- | --- |
| `roomr.nodes.<node_ref>.heartbeat` | `node.heartbeat` every `heartbeatInterval` (`rooms`, `participants`, `max_rooms`, `codecs`, `version`, `region`, `control_addr`, `sip_addr`, and `rtp_ports` when a range is configured); `node.down` when draining starts |
| `roomr.nodes.<node_ref>.events` | `registration.state` (`registered`, `failed`, `expired`, `unregistered`) |
| `roomr.events.<call_ref>` | `call.started`, `call.ringing`, `call.answered`, `call.ended` (`cause`, `sip_status`, `answered`), `participant.joined`, `participant.left` |
| `roomr.events.room.<room_ref>` | `room.ended` (`cause`) and RTP participants' events |

Every call event carries `node_ref`, a per-call `seq` starting at 1, the call's `metadata` and
`ts`. Use `seq` to detect gaps. A controller finds nodes, and where to send RPCs, from
`control_addr` in the heartbeats.

## Versioning of the node

The node follows semantic versioning. Its `version` is in every heartbeat. A contract change that
only adds fields is a minor release.
