## 1. Node and bot hooks

- [x] 1.1 `node.(*Node).EndCallWith(ref, cause, status)` and `Config.ReportRooms` (fake only)
- [x] 1.2 `caller.Bot`: callee per call_ref (`CalleeFor`), `ring` mode answered by `AnswerRinging`
- [x] 1.3 `cmd/sipcaller`: `PUT /callee`, `GET /callee/calls`, `GET /info` (the real target needs them)

## 2. The fake

- [x] 2.1 `cmd/roomr-fake`: nodes, phone, control API, faults, kill, crash
- [x] 2.2 Go tests FAK-001..003
- [x] 2.3 Dockerfile `fake` target; release publishes `fonoster/roomr-fake`

## 3. npm package and contract suite

- [x] 3.1 `js/`: `@fonoster/roomr` client with the protos, `/fake` client, `/contract` suite
- [x] 3.2 Contract cases CON-001..; targets: real binary + sipcaller, and the fake
- [x] 3.3 CI job runs the suite against both; release publishes npm
- [x] 3.4 Docs: README, docs/FAKE.md, llms.txt
