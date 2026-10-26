## Why

Some phones and PBXs stop sending RTP when the caller presses hold, and say so with a re-INVITE
or UPDATE whose SDP is `a=inactive` (or `a=recvonly`). The node ignores SDP direction today, so a
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
- Direction is read from the remote's side: `a=inactive` or `a=recvonly` means the remote sends no
  RTP, so the leg's media timeout is paused; `a=sendrecv` or `a=sendonly` (music on hold still
  flows) means it sends, and the timeout counts again from that offer.
- New optional `mediaHoldTimeout` in `roomr.yaml` (off by default): a leg held this long ends the
  call with `media_timeout`.
- The caller bot can send a re-INVITE or UPDATE with a chosen direction (tests).

## Capabilities

### Modified Capabilities
- `roomr`: "A silent leg ends the call" gains the hold scenarios and `mediaHoldTimeout`.

## Impact

- `internal/sdp` (direction), `internal/sipua` (in-dialog offers), `internal/node` (hold state,
  limits), `internal/fileconfig` and `cmd/roomr` (`mediaHoldTimeout`), `internal/caller` (`Reoffer`).
- No wire contract change: no new cause, no proto change.
