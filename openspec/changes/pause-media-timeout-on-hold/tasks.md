## 1. Node

- [x] 1.1 `sdp`: parse the stream's direction (media level over session level), `Sends`, `AnswerDirection`, `WithDirection`
- [x] 1.2 `sipua`: answer re-INVITE and UPDATE on answered legs (inbound and dialed), report the remote's direction
- [x] 1.3 `node`: pause a held leg's media timeout, count again from the offer that restores sending; `MediaHoldTimeout`
- [x] 1.4 `mediaHoldTimeout` in `roomr.yaml` and docs/CONFIGURATION.md

## 2. Tests

- [x] 2.1 Unit: SDP direction, the node's hold timer (ROM-036)
- [x] 2.2 E2E: re-INVITE `a=inactive` keeps the call past `mediaTimeout`, back to `sendrecv` ends it; UPDATE `a=recvonly` holds; `sendonly` keeps the timer; `mediaHoldTimeout` ends a long hold (ROM-036)
