## 1. Node

- [x] 1.1 `sdp`: parse the stream's direction (media level over session level), `Sends`, `AnswerDirection`, `WithDirection`
- [x] 1.2 `sipua`: answer re-INVITE and UPDATE on answered legs (inbound and dialed), report the remote's direction
- [x] 1.3 `node`: a leg is held on `inactive`, `recvonly`, `sendonly` or `c=0.0.0.0`; pause a held leg's media timeout, count its silence again from the offer that switches it between held and active; `MediaHoldTimeout` (30m by default, negative never) bounds a held leg's silence
- [x] 1.4 `mediaHoldTimeout` in `roomr.yaml` and docs/CONFIGURATION.md
- [x] 1.7 `sdp` and `sipua`: `c=0.0.0.0` is a hold (`inactive`), not a moved address
- [x] 1.5 `sipua`: 488 for an offer that moves media, codec, SRTP key or telephone-event; 500 with Retry-After before the answer; late offer answered in the ACK; same answer for a retransmitted re-INVITE and 2xx retransmission until the ACK; dialed legs answer with the negotiated codec only
- [x] 1.6 `node`: no RTP to a leg whose offer says it receives none; a direction reported before the leg is served is replayed

## 2. Tests

- [x] 2.1 Unit: SDP direction, the node's hold timer (ROM-036)
- [x] 2.2 E2E: re-INVITE `a=inactive` keeps the call past `mediaTimeout`, back to `sendrecv` ends it; UPDATE `a=recvonly` holds; `sendonly` and `c=0.0.0.0` hold; a dialed leg holds; `mediaHoldTimeout` ends a silent hold, and music on hold outlives it (ROM-036)
- [x] 2.4 Test bot: `Reoffer` and `ReofferSDP` on a call it answered
- [x] 2.3 Unit and E2E: a media-moving offer gets 488 and the call keeps its audio; an early UPDATE changes nothing; hold by late offer; a retransmitted re-INVITE gets the same answer; a held leg gets no RTP (ROM-036)
