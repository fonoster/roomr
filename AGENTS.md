# AGENTS.md

## Overview
Roomr is the SIP and media edge for voice agents: a Go node that terminates SIP, mixes audio in
rooms (mix-minus, 16 kHz), and streams each call to an app over RTP plus a per-call gRPC stream.
It is Fonoster's media edge and is built to be used on its own (MIT). It ships as the
`fonoster/roomr` and `fonoster/roomr-fake` images, the npm package `@fonoster/roomr` (client,
`/fake`, `/contract`) and a Helm chart. Read `docs/ARCHITECTURE.md` (the map),
`docs/CONTRACT.md` (the wire contract) and `docs/CONFIGURATION.md` (every setting) before
changing behavior.

## Commands
```sh
go build ./cmd/... && go vet ./...
go test -race -p 1 ./cmd/... ./internal/...      # unit
go test -race -p 1 ./e2e/...                     # needs NATS and Routr: see CONTRIBUTING.md
api/generate.sh                                  # after editing a .proto
npx -y @fission-ai/openspec@1.14.3 validate --specs
```
- Opus is cgo (libopus). `CGO_ENABLED=0` builds without it.
- e2e tests skip without `CONFORMANCE_NATS_URL`. CI runs them against NATS and the Routr in
  `e2e/testdata/routr/compose.yaml`.
- Contract suite (`js/`, cases `CON-nnn`): `npm ci && npm run build && npx mocha`, with
  `ROOMR_BIN_DIR` set to the folder of the built `roomr`, `roomr-fake` and `sipcaller`.
- Default Verify: `test -z "$(gofmt -l .)" && go vet ./... && go test -race -p 1 ./cmd/... ./internal/...`.

## Conventions
Profile: repo-native
Overrides:
- Go: `gofmt`, `go vet`, errors wrapped with context, no global state in `internal/node`.
- Specs first: behavior lives in `openspec/specs`. A behavior change is an OpenSpec change under
  `openspec/changes/`. A bug fix that restores what a requirement already says needs none.
- Tests carry the requirement's case id in their name (`TestROM033_RegistersWithSessionCount`).
- The node knows no other component by name. Proxy, registrars, session endpoint and header
  names come from configuration or requests. Never hardcode a platform (Fonoster, Routr).
- No app-server features: no prompts, speech, recording or per-tenant logic in the node.
- NATS is best effort. A call never depends on it.
- Config is one YAML file. Unknown keys fail at start. There are no `ROOMR_*` variables.
- Comments keep to the non-obvious why and cite the spec requirement they implement.
- Commits and PR titles are conventional commits (`feat(node): ...`); PRs are squash merged.
Examples: `internal/sipua/register.go`, `internal/node/room.go`, `internal/fileconfig/fileconfig.go`

## Labels
These are the only labels; any other is deleted. An issue's kind is its native issue type (Bug,
Feature, Task), not a label. Close as "not planned" or "duplicate" instead of labeling.

| Label | Meaning | Applied by |
| :--- | :--- | :--- |
| needs-triage | Filed, not yet shaped | Issue forms |
| needs-info | Waiting on the reporter | /ps:report |
| ready | Plan approved; agents may implement | A maintainer |
| not-ready | The implementer refused; see its comment | /ps:implement |
| blocked | An agent stopped on a blocker | Any ps skill |
| needs-human | A loop limit was hit or a decision needs a maintainer | Any ps skill |
| needs-issue | A pull request with no linked issue | require-linked-issue workflow |
| breaking | Needs a major bump or a migration note | /ps:report |
| risk: high | A maintainer reviews the plan before work starts | /ps:report |
| external | Opened by someone outside the org | label-external workflow |
| good first issue | A ready issue a newcomer can take | A maintainer |
| dependencies | Dependency update | Dependabot |
| autorelease: pending | The open release PR | release-please |
| autorelease: tagged | A merged release PR, tagged | release-please |

No `area:` labels: Roomr is one binary, and there is no `## Areas` map to route.

## Compatibility
Breaking changes here:
- gRPC protos (`api/roomr/v1`, `api/session/v1`): append-only. Add fields, never renumber,
  reuse or remove them (mark removed ones `reserved`). Commit the generated Go.
- NATS subjects and payloads (`roomr.nodes.*`, `roomr.events.*`, `roomr.call.*`).
- Config keys and defaults (`docs/CONFIGURATION.md`) and Helm values (`charts/roomr/values.yaml`).
- `@fonoster/roomr` exports (`.`, `./fake`, `./contract`) and the fake's HTTP API (`docs/FAKE.md`).
- SIP behavior registrars rely on: REGISTERs leave from the `sip.listen` port, and
  registration headers are templated (`{sessions}`).

## Risk hotspots
- `internal/node`, `internal/caller`: most fix commits since August.
- `internal/sipua`: every SIP request's transport and source address; registration.
- `api/**/*.proto`: the wire contract Fonoster and other platforms vendor.
- `.github/workflows/release.yaml`: a merged release PR publishes images, npm, the chart and the protos.

## Release
release-please (`release-please-config.json`): every push to main updates one release PR
(`chore(main): release X.Y.Z`; `fix` patch, `feat` minor, breaking minor while 0.x). Merging it
tags `vX.Y.Z`, and the same `release.yaml` run checks the commit with CI and publishes the
images, `@fonoster/roomr`, the chart to `oci://ghcr.io/fonoster/charts/roomr`, the protos,
static `roomr-fake` binaries and a release card. CI's `CI passed` job gathers every CI job.

## Expert notes
### realtime-comms-engineer
- SIP stack is sipgo v1.6.0. It refuses UDP messages over 1,300 B (`sip/transport_udp.go:327`),
  so large INVITEs (WhatsApp's) need TCP toward the proxy.
- Rooms mix at 16 kHz. Contacts carry `;transport=tls` only on the TLS listener's dialogs.
- No re-INVITE support: an in-dialog INVITE is handled as a new call.
### qa-engineer
- The e2e rig listens on 127.0.0.1, so it can't catch wildcard-listen bugs (roomr#58).
- The contract suite runs against the real binary and the fake in CI.
### performance-engineer
- The capacity bar is 200 G.711 rooms on 2 vCPU for 30 minutes (`docs/PERFORMANCE.md`).
### release-engineer
- Fonoster pins Roomr by `@fonoster/roomr` and image tags in its bundle and Helm values; a
  proto or config change here reaches it only through a release.

## Decisions
- 2026-10-21: Roomr is the SIP and media edge, not an app server: no Record, Play or Stop;
  recording is a listener.
- 2026-10-21: The Roomr gRPC is CreateCall, EndCall, CreateRoom, EndRoom, AddParticipant,
  RemoveParticipant.
- 2026-10-22: Roomr has its own repo and release lifecycle; Fonoster doesn't build or test
  Roomr internals.
