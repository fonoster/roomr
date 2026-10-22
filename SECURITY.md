# Security policy

## Reporting a vulnerability

Please do **not** open a public issue for a security problem. Use GitHub's private reporting:
[Report a vulnerability](https://github.com/fonoster/roomr/security/advisories/new), or email
team@fonoster.com. Include the version, a description, and steps to reproduce if you can. We
acknowledge reports within three working days and keep you informed until a fix ships.

## Supported versions

Security fixes go into the latest minor release.

## Scope and the trust model

Roomr authenticates neither gRPC callers nor SIP sources: **the network is the boundary**. Its SIP
port should be reachable only from your SIP proxy, and its control port only from the controller.
Reports that depend on exposing those ports to untrusted networks are hardening feedback, not
vulnerabilities, but we want to hear them. TLS for SIP and gRPC and SRTP (SDES) are available and
off by default; see [docs/CONFIGURATION.md](docs/CONFIGURATION.md).
