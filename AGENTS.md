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
