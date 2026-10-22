# Contributing to Roomr

Thank you for helping. Roomr is the SIP and media edge of Fonoster, and it is also meant to be used
on its own, so changes are judged by the contract first: keep it small, keep it boring, keep it
compatible.

## Ways to help

Bug reports with a way to reproduce, fixes, tests, documentation, and diagrams are all welcome. For
anything bigger than a fix, open an issue or a [discussion](https://github.com/fonoster/roomr/discussions)
first so we can agree on the shape before you write code.

## Specs first

Behavior is specified in [`openspec/`](openspec) as requirements with scenarios. A change that adds
or alters behavior starts as an OpenSpec change (`openspec/changes/<name>/` with a proposal, design,
tasks and spec deltas) and is archived when it is done. A bug fix that restores what a requirement
already says needs no spec change. The wire contract is stricter still: **messages only grow**
(see [docs/CONTRACT.md](docs/CONTRACT.md)).

## Development setup

You need Go (the version in `go.mod`), `libopus` (`brew install opus` or `apt install
libopus-dev`) and, for the end-to-end tests, Docker.

```sh
go build ./cmd/...
go vet ./...
go test -race -p 1 ./cmd/... ./internal/...     # unit tests
```

End-to-end tests run a real node against a real SIP proxy and NATS:

```sh
docker run -d --name nats -p 4222:4222 nats:2.11.8
docker compose -f e2e/testdata/routr/compose.yaml up -d --wait
CONFORMANCE_NATS_URL=nats://127.0.0.1:4222 \
ROUTR_EDGEPORT=127.0.0.1:15360 ROUTR_NODE_HOST=host.docker.internal \
  go test -race -p 1 ./e2e/...
```

If you change a `.proto`, run `api/generate.sh` (needs `protoc`, `protoc-gen-go` v1.36.8 and
`protoc-gen-go-grpc` v1.5.1) and commit the generated Go.

## Tests

Every requirement is proven by a test whose name carries the requirement's case id. A new behavior
comes with its test, and a fixed bug with the test that failed before. The race detector is on in CI.

## Pull requests

- Branch from `main`; keep a pull request to one change.
- **The PR title is a [conventional commit](https://www.conventionalcommits.org)** (`feat(node): ...`,
  `fix(sdp): ...`). Pull requests are squash merged, and the title becomes the commit, which drives
  the release notes and the version.
- Sign off your commits (`git commit -s`): we use the
  [Developer Certificate of Origin](https://developercertificate.org).
- Fill in the pull request template, and update the docs and specs the change touches.

## Releases

Maintainers tag `vX.Y.Z`; the release workflow tests, builds the multi-arch images
(`fonoster/roomr`, `fonoster/roomr-fake`) and publishes the release notes with the protos attached.

## Code of conduct

Be kind. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
