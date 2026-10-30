## Why

Some phones and PBXs stop sending RTP when the caller presses hold, and say so with a re-INVITE
or UPDATE whose SDP is `a=inactive`, `a=recvonly`, `a=sendonly` (most desk phones) or
`c=0.0.0.0` (older peers). The node ignores SDP direction today, so a
caller on hold for longer than `mediaTimeout` (15 s) is hung up with `media_timeout`. The rule is
"when media is expected, media must be present": Asterisk (`rtp_timeout_hold`) and FreeSWITCH
(`media_hold_timeout`) keep a separate timer for hold (fonoster/fonoster#1017).

## What Changes

- The node answers in-dialog offers (re-INVITE and UPDATE) on an answered SIP leg: `200 OK` with
  the same stream, its direction the offer's mirror (RFC 3264). Today a re-INVITE is mistaken for
  a new call. It takes only what it can honor without moving its RTP endpoint: an offer that
  moves the remote's address or port, drops the negotiated codec, changes the SRTP key or moves
  telephone-event gets `488` and changes nothing, so the call is never worse off than before.
  Before the answer an offer gets `500` with `Retry-After`. While the remote says it receives no
  RTP (`a=inactive`, `a=sendonly`) the node sends it none, as its answer says.
- A leg, inbound or dialed, is held while the remote's latest offer says `a=inactive`,
  `a=recvonly` or `a=sendonly`, or has `c=0.0.0.0` (answered `a=inactive` with the node's own
  address); `a=sendrecv` or no direction is active. A held leg's media timeout is paused, and an
  offer that switches the leg between held and active restarts the count of its silence.
- New optional `mediaHoldTimeout` in `roomr.yaml` (30m by default, negative never): a held leg
  that sends no RTP this long ends the call with `media_timeout`. It bounds silence, not hold
  time: a held peer that keeps sending (music on hold) is never ended. FreeSWITCH's sample
  `media_hold_timeout` is 1800 s.
- The caller bot can send a re-INVITE or UPDATE with a chosen direction, on calls it placed or
  answered (tests).

## Capabilities

### Modified Capabilities
- `roomr`: "A silent leg ends the call" gains the hold scenarios and `mediaHoldTimeout`.

## Impact

- `internal/sdp` (direction), `internal/sipua` (in-dialog offers), `internal/node` (hold state,
  limits), `internal/fileconfig` and `cmd/roomr` (`mediaHoldTimeout`), `internal/caller` (`Reoffer`).
- No wire contract change: no new cause, no proto change.
