# Changelog

## [0.3.1](https://github.com/fonoster/roomr/compare/v0.3.0...v0.3.1) (2026-10-30)


### Bug Fixes

* **sipua:** reuse one TCP or TLS connection per peer with a wildcard sip.listen ([#59](https://github.com/fonoster/roomr/issues/59)) ([606f5ef](https://github.com/fonoster/roomr/commit/606f5ef600d96136691cb42c30b1cae416f3d0e5)), closes [#58](https://github.com/fonoster/roomr/issues/58)

## [0.3.0](https://github.com/fonoster/roomr/compare/v0.2.7...v0.3.0) (2026-10-26)


### Features

* **telemetry:** logs over OTLP and sip.call_id on the call span ([#43](https://github.com/fonoster/roomr/issues/43)) ([0a28e3c](https://github.com/fonoster/roomr/commit/0a28e3ca0641e5c7dad24ddd4ae165c2198e60af))

## [0.2.7](https://github.com/fonoster/roomr/compare/v0.2.6...v0.2.7) (2026-10-25)


### Bug Fixes

* **node:** a caller hanging up as the call is answered ends it with caller_hangup ([#41](https://github.com/fonoster/roomr/issues/41)) ([eb38782](https://github.com/fonoster/roomr/commit/eb38782eb3b316226959d21482d72b743b42a27b))

## [0.2.6](https://github.com/fonoster/roomr/compare/v0.2.5...v0.2.6) (2026-10-24)


### Bug Fixes

* force release ([#38](https://github.com/fonoster/roomr/issues/38)) ([16ec3a9](https://github.com/fonoster/roomr/commit/16ec3a9190c785641e31fc2077b0b9e6f4a07a51))

## [0.2.5](https://github.com/fonoster/roomr/compare/v0.2.4...v0.2.5) (2026-10-24)


### CI

* **release:** attach a release card to each GitHub release ([#36](https://github.com/fonoster/roomr/issues/36)) ([e38b65b](https://github.com/fonoster/roomr/commit/e38b65b85e2e768e25d76c169dde9a72c8d64c43))

## [0.2.4](https://github.com/fonoster/roomr/compare/v0.2.3...v0.2.4) (2026-10-23)


### Bug Fixes

* **caller:** keep 1xx responses that arrive just before the final ([#31](https://github.com/fonoster/roomr/issues/31)) ([71575a2](https://github.com/fonoster/roomr/commit/71575a2b9396d2ea9dde197d7529cacfd5565abc))
* **node:** participant.left precedes room.ended when EndRoom follows RemoveParticipant ([#30](https://github.com/fonoster/roomr/issues/30)) ([041306c](https://github.com/fonoster/roomr/commit/041306c4b9b3a82fecbb4b58a9e51256a5b2c5d7))
