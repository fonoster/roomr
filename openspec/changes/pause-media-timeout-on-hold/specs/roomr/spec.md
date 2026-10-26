## MODIFIED Requirements

### Requirement: A silent leg ends the call

The node SHALL end an answered call when a leg sends no RTP for the media timeout (`mediaTimeout`,
one key for the caller leg and the app leg, 15 s by default, like LiveKit), with `cause: "media_timeout"` when
the caller's leg went silent and `cause: "app_gone"` when the app's did, sending BYE on the SIP
leg. A SIP leg is not silent while its remote is on hold: the node SHALL answer a re-INVITE or
UPDATE with SDP on an answered leg with `200 OK` and the same stream, its direction the offer's
mirror (RFC 3264), and SHALL pause the leg's media timeout while the remote's latest offer says it
sends no RTP (`a=inactive` or `a=recvonly`). An offer that says the remote sends (`a=sendrecv`,
or `a=sendonly` such as music on hold) SHALL restart the count from that offer. While the
remote's latest offer says it receives no RTP (`a=inactive` or `a=sendonly`) the node SHALL send
the leg none, as its answer says. The node SHALL take only offers it can honor without touching the
leg's RTP endpoint: one that moves the remote's address or port, drops the negotiated codec or
payload type, changes the SRTP key or moves telephone-event SHALL be answered
`488 Not Acceptable Here` and change nothing. An offer before the leg is answered (and a re-INVITE
before the setup INVITE is ACKed) SHALL be answered `500` with `Retry-After` and change nothing. A
re-INVITE without SDP SHALL get the node's stream as a `sendrecv` offer, the remote's answer in
the ACK applying its direction. A retransmitted re-INVITE (same CSeq) SHALL get the same answer,
and the node SHALL retransmit its `200` over UDP until the ACK. With
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

#### Scenario: Offer that moves media
- **WHEN** an answered caller sends a re-INVITE or UPDATE whose SDP moves its RTP address or port, or drops the negotiated codec
- **THEN** the node answers `488 Not Acceptable Here` and the call keeps its media both ways

#### Scenario: Offer before the answer
- **WHEN** a ringing caller sends an UPDATE with `a=inactive`
- **THEN** the node answers `500` with `Retry-After` and, once answered, a silent caller still ends the call with `cause: "media_timeout"`

#### Scenario: Hold by late offer
- **WHEN** the caller sends a re-INVITE without SDP and answers the node's `sendrecv` offer with `a=inactive` in the ACK
- **THEN** the call is held as by an offer with `a=inactive`

#### Scenario: Held leg gets no RTP
- **WHEN** the node answered an offer `a=inactive` (or `a=recvonly` to `a=sendonly`)
- **THEN** it sends that leg no RTP until an offer asks for it again

#### Scenario: Retransmitted re-INVITE
- **WHEN** a re-INVITE arrives again with the same CSeq
- **THEN** the node answers it with the same SDP, `o=` version included, and does not apply it again
