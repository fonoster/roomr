## ADDED Requirements

### Requirement: The fake hosts real nodes behind a control API

`roomr-fake` SHALL run any number of Roomr nodes in one process, each built from the real node
(`internal/node`, its SIP user agent and its Roomr gRPC server) and each with its own Roomr gRPC
address, SIP address, NATS heartbeats and events, and session endpoint. An HTTP+JSON control API
SHALL create a node (`POST /nodes`) with its node ref, region, room limit, session endpoint and
timeouts, and SHALL return its control and SIP addresses. Rooms requested as pre-filled SHALL be
real rooms that count toward the limit and never end on their own. Case id FAK-001.

#### Scenario: Two nodes in one process
- **WHEN** a test creates nodes `a` and `b` with room limits 5 and 10
- **THEN** each answers Roomr gRPC on its own address and publishes its own heartbeat with its `max_rooms` and `control_addr`

#### Scenario: Pre-filled rooms count toward the limit
- **WHEN** a node is created with `maxRooms: 2` and `rooms: 2`
- **THEN** its heartbeat reports 2 rooms and CreateRoom fails with RESOURCE_EXHAUSTED

### Requirement: The fake places and answers calls like a phone

Each fake node SHALL have a phone on loopback SIP and RTP. `POST /nodes/{ref}/calls` SHALL send an
INVITE to the node, so the node opens the call's control stream exactly as for any caller; the
phone's callee SHALL answer what the node dials as scripted (`answer`, `busy`, `no_answer`,
`decline`, `unavailable` or `ring`, after a delay, per call_ref or for every call). A call SHALL be
addressable by its call_ref to read its SIP statuses and state, play audio, send DTMF, read what
the phone heard, hang up, go silent, answer a ringing callee, or be ended by the node with a given
cause and SIP status. Case id FAK-002.

#### Scenario: Inbound call reaches the session endpoint
- **WHEN** a test places an inbound call with headers `X-App-Ref: app-1`
- **THEN** the session endpoint receives Start with direction INBOUND and that header
- **AND** after the app's Answer the call's SIP statuses are 180 then 200

#### Scenario: A ringing callee answers on demand
- **WHEN** the callee for call_ref `c1` is `ring` and a controller sends CreateCall `c1`
- **THEN** the call stays ringing until `POST /calls/c1/answer`, then the session endpoint receives Start with direction OUTBOUND

#### Scenario: The node ends a call with a scripted cause
- **WHEN** a test ends an answered call with cause `node_error`
- **THEN** the session endpoint receives Ended with cause `node_error` and the phone's leg is hung up

### Requirement: The fake injects faults

The control API SHALL make the next N calls to a Roomr gRPC method fail with a given gRPC status
code, or stall for a given time before being served; SHALL make a node report a different room
count in its heartbeats; SHALL stop a node gracefully (`node.down`, then its rooms end) or kill it
(no `node.down`, heartbeats and gRPC stop at once, its calls' control streams drop without Ended);
and SHALL crash the whole process. Case id FAK-003.

#### Scenario: The next CreateCall fails
- **WHEN** a test asks for CreateCall to fail once with UNAVAILABLE
- **THEN** the next CreateCall fails with UNAVAILABLE and the one after it succeeds

#### Scenario: A killed node goes silent
- **WHEN** a test kills a node
- **THEN** no `node.down` is published, its heartbeats stop and its Roomr gRPC address refuses connections

### Requirement: One contract suite proves the fake and the node

`@fonoster/roomr/contract` SHALL be one test suite, written against a small target interface
(start a node, script the callee, place an inbound call, act as the phone, stop the node), that
runs unchanged against the real node binary with the SIP caller bot and against the fake. CI SHALL
run it against both on every change. Case ids CON-001 and up.

#### Scenario: The suite passes against both targets
- **WHEN** CI runs the contract suite against the real node and against the fake
- **THEN** every case passes against both, with the same case ids
