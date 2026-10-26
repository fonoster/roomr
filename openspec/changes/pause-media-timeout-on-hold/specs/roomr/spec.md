## MODIFIED Requirements

### Requirement: A silent leg ends the call

The node SHALL end an answered call when a leg sends no RTP for the media timeout (`mediaTimeout`,
one key for the caller leg and the app leg, 15 s by default, like LiveKit), with `cause: "media_timeout"` when
the caller's leg went silent and `cause: "app_gone"` when the app's did, sending BYE on the SIP
leg. A SIP leg is not silent while its remote is on hold: the node SHALL answer a re-INVITE or
UPDATE with SDP on an answered leg with `200 OK` and the same stream, its direction the offer's
mirror (RFC 3264), and SHALL pause the leg's media timeout while the remote's latest offer says it
sends no RTP (`a=inactive` or `a=recvonly`). An offer that says the remote sends (`a=sendrecv`,
or `a=sendonly` such as music on hold) SHALL restart the count from that offer. With
`mediaHoldTimeout` set (off by default) the node SHALL end a call whose SIP leg has been on hold
that long with `cause: "media_timeout"`.

#### Scenario: App stops sending audio
- **WHEN** the app's RTP stops while its control stream stays open
- **THEN** after the media timeout the node sends BYE to the caller and `Ended` with `cause: "app_gone"` on the stream

#### Scenario: Caller stops sending audio
- **WHEN** the caller's RTP stops
- **THEN** after the same media timeout the node ends the call with `cause: "media_timeout"`

#### Scenario: Caller on hold
- **WHEN** the caller sends a re-INVITE with `a=inactive` and then no RTP for longer than the media timeout
- **THEN** the node answers `200 OK` with `a=inactive` and the call stays up

#### Scenario: Caller back from hold
- **WHEN** a held caller sends a re-INVITE with `a=sendrecv` and still no RTP
- **THEN** after the media timeout from that re-INVITE the node ends the call with `cause: "media_timeout"`

#### Scenario: Hold by UPDATE
- **WHEN** the caller sends an UPDATE with `a=recvonly` and then no RTP for longer than the media timeout
- **THEN** the node answers `200 OK` with `a=sendonly` and the call stays up

#### Scenario: Music on hold keeps the timer
- **WHEN** the caller sends a re-INVITE with `a=sendonly` and then no RTP
- **THEN** after the media timeout the node ends the call with `cause: "media_timeout"`

#### Scenario: Held too long
- **WHEN** `mediaHoldTimeout` is set and the caller stays on hold longer than it
- **THEN** the node ends the call with `cause: "media_timeout"`
