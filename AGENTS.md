# AGENTS.md

Guidance for AI coding agents working in this repository. `CLAUDE.md` points here.

## What this is

Roomr is the SIP and media edge for voice agents: a Go node that terminates SIP, mixes audio in
rooms (mix-minus, 16 kHz), and streams each call to an app over RTP plus a per-call gRPC stream.
Read `docs/ARCHITECTURE.md` (the map), `docs/CONTRACT.md` (the wire contract) and
`docs/CONFIGURATION.md` (every setting) before changing behavior.

## Rules that matter

- **The contract is append-only.** `api/**/*.proto`: add fields, never renumber, reuse or remove
  them (mark removed ones `reserved`). Regenerate with `api/generate.sh` and commit the Go output.
- **Specs first.** Behavior lives in `openspec/specs`. A behavior change is an OpenSpec change under
  `openspec/changes/`. Tests carry the requirement's case id in their name.
- **The node knows no other component by name.** Proxy, registrars, session endpoint and header
  names come from configuration or requests. Do not hardcode a platform (Fonoster, Routr).
- **No app-server features.** No prompts, speech, recording or per-tenant logic in the node.
- **NATS is best effort.** A call must never depend on it.
- **Config is one YAML file.** Unknown keys fail at start. There are no `ROOMR_*` variables.

## Commands

```sh
go build ./cmd/... && go vet ./...
go test -race -p 1 ./cmd/... ./internal/...      # unit
go test -race -p 1 ./e2e/...                     # needs NATS and Routr: see CONTRIBUTING.md
api/generate.sh                                  # after editing a .proto
```

Opus is cgo (libopus). `CGO_ENABLED=0` builds without it.

## Conventions

- Go: `gofmt`, `go vet`, errors wrapped with context, no global state in `internal/node`.
- Commits and PR titles are conventional commits (`feat(node): ...`); PRs are squash merged.
- Keep comments to the non-obvious why, and cite the spec requirement they implement.

## Areas

| Area label | Paths | Experts |
| :--- | :--- | :--- |
| (none) | internal/sipua, internal/sdp, internal/sipheader, internal/edgetls, internal/rtpio, internal/jitter, internal/audio, internal/dtmf, internal/caller, internal/node | realtime-comms-engineer, performance-engineer |
| (none) | api/, internal/control, js/ | release-engineer |
| (none) | charts/, Dockerfile, .github/workflows, release-please-config.json | release-engineer, security-engineer |

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
| autorelease: pending | Release PR not yet tagged | release-please |
| autorelease: tagged | Release PR tagged and released | release-please |

No `area:` labels: every row of `## Areas` is `(none)`.

## Release

release-please opens a `chore(main): release X.Y.Z` PR; merging it tags the release and publishes the
images, npm package, Helm chart and protos. Never add or remove the `autorelease:*` labels by hand:
release-please finds its release PR by them. Merge other PRs one at a time while a release PR is open.

## Decisions

- 2026-10-28: One label standard across fonoster, routr and roomr (`## Labels`); kinds are native issue types.
