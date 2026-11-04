# perf

Roomr's benchmark definitions for [perfkit](https://github.com/fonoster/perfkit) v0.1.5.
perfkit runs Roomr on one server and SIPp on another, then writes a `report.json` with each
benchmark's verdict. Roomr is pinned to two cores, the spec's capacity bar
(`openspec/specs/roomr/spec.md`, "Capacity bar"). Next to Roomr runs a small app that answers
its calls; the app is not under test.

These benchmarks measure call setup, failed calls and memory. They don't measure dropped audio
frames or keypresses, so they give no verdict on the Capacity bar itself: that is still measured
with `cmd/loadgen` ([docs/PERFORMANCE.md](../docs/PERFORMANCE.md)).

## The benchmarks

| Id | Kind | What it measures | Passes when |
| --- | --- | --- | --- |
| `invite-answer` | capacity | Inbound calls/s at a 2 s hold. Roomr answers each INVITE after the app answers its control stream, and every answered call mixes audio, so about twice the rate is mixing at once. SIPp adds 10 calls/s every minute, from 10 to 300. | Each step's p99 INVITE-to-200 stays under 200 ms and failed calls under 0.1%. The limit is the last step that passed. |
| `rooms-g711` | soak | About 200 concurrent rooms for six hours, each with one G.711 caller streaming PCMU and the app on L16 (3.34 calls/s, 60 s each). | After a 30-minute warm-up, Roomr's memory grows less than 5 MB/h, under 0.1% of calls fail, and Roomr never restarts. |

`perfkit.smoke.yaml` runs the same benchmarks, shortened: the ramp stops at 30 calls/s, and the
soak holds about 60 rooms for 5 minutes after a 12-minute warm-up. It checks the harness, not
Roomr's limits.

Every soak's warm-up is longer than 10 minutes because the node keeps each ended call, with its
room, for `CallDedupeWindow` (10 minutes, `internal/node/node.go`). Until then memory climbs
with every call, then levels off. A shorter warm-up would report that as a leak.

## Map

| Path | What it is |
| --- | --- |
| `perfkit.yaml` | The full run: the project, the servers, the image and both benchmarks (`apiVersion: perfkit/v1`). |
| `perfkit.smoke.yaml` | The same benchmarks, shortened, to check the harness in under 30 minutes. |
| `compose.yaml` | What runs on the server under test: `roomr` on cores 0 and 1, and `app` on cores 2 and 3, both with host networking. |
| `roomr.yaml` | The node's config. `${PERFKIT_UUT_IP}` is expanded by the node. No NATS runs there; the node works without it. `maxRooms` is high so the ramp, not the room cap, finds the limit. |
| `app/app.mjs` | The app: serves `session.v1.Session` on `127.0.0.1:50071` (the node's `sessionEndpoint`), answers every call with an L16/16000 RTP endpoint and echoes each packet back, so neither leg goes silent and trips `mediaTimeout` (15 s). |
| `app/package.json`, `app/package-lock.json` | The app's one dependency, `@fonoster/roomr`, pinned (not the version under test: the app isn't under test, and `session.v1` only grows). `npm ci` installs it inside the container. |
| `invite-answer/uac_invite.xml` | SIPp caller: INVITE with a PCMU offer, 200, ACK, hold, BYE, 200. Sends no RTP, so SIPp isn't the bottleneck at high rates. |
| `rooms-g711/uac_invite.xml` | The same dialog, streaming PCMU RTP (`rtp_stream`, looped silence) for the whole hold. |
| `audio/silence.ulaw` | One second of PCMU silence (8000 bytes of `0xFF`), raw, as SIPp's `rtp_stream` reads it. |

The compose file reads the variables perfkit sets on the server under test:
`PERFKIT_IMAGE_ROOMR` (pinned by digest) and `PERFKIT_UUT_IP`.

## Run it on your machine

You need Node 22, Docker with host networking (Docker Desktop: Settings → Resources → Network →
Enable host networking), and nothing else on ports 5060, 5080 or 50071, including another
perfkit run. From the repository root:

```sh
npx -y npm@11 exec --yes --package=github:fonoster/perfkit#v0.1.5 -- perfkit run --local \
  --config perf/perfkit.smoke.yaml --target-version 0.4.0 --commit "$(git rev-list -n1 v0.4.0)"
```

`npx -y npm@11` is there because Node 22 ships npm 10, which can't install perfkit from a git
tag. The run takes about 25 minutes (SIPp is amd64 only and runs emulated on Apple silicon).
It exits 0 when every benchmark passed and 1 when one didn't, and writes
`.perfkit/<run id>/report.json` (ignored by git). Running on your machine checks the harness.
It can't pin real cores, so its numbers aren't Roomr's limits.

## Read a report

In `report.json` (`perfkit/report/v1`, described in perfkit's
[docs/report-v1.md](https://github.com/fonoster/perfkit/blob/v0.1.5/docs/report-v1.md)):

- `benchmarks[].verdict`: `passed`, `failed`, `inconclusive` (the load generator was the
  bottleneck) or `aborted` (too little data).
- `invite-answer`: `headline.limitCps` and `headline.p99AtLimitMs`; `limitIsLowerBound` is true
  when the ramp ended without breaking a rule. `series.steps[]` has each step's p99, failures
  and `uutCpuPct`. That CPU figure is roomr and the app added together. Each one's own CPU is in
  `resources.processes[]`. If the app is near 100% (one core) at the limit step, the limit is
  the app's, not Roomr's.
- `rooms-g711`: `headline.memoryGrowthMBPerHour` (Roomr's memory slope after the warm-up) and
  `rules[]` with each rule's limit and measured value.

To see a report as a page, render it with perfkit:

```sh
npx -y npm@11 exec --yes --package=github:fonoster/perfkit#v0.1.5 -- perfkit render \
  --report .perfkit/<run id>/report.json --out /tmp/roomr-bench
```

## Check a benchmark by hand

Start the server under test with a released image, then drive it with SIPp 3.7:

```sh
cd perf
export PERFKIT_IMAGE_ROOMR=fonoster/roomr:0.4.0 PERFKIT_UUT_IP=127.0.0.1
docker compose up -d --wait
docker run --rm --network host --platform linux/amd64 -v "$PWD":/project \
  pumpagubben/sipp:3.7.3 /sipp/sipp 127.0.0.1:5060 -sf /project/rooms-g711/uac_invite.xml \
  -i 127.0.0.1 -p 5080 -t u1 -m 3 -r 1 -d 20000 -nostdin
docker compose down
```

Three calls held 20 s that end with "Successful call 3" show that RTP keeps both legs alive
past `mediaTimeout`.

## perfkit version

perfkit is pinned to the git tag `v0.1.5` in every command here. To move to a new tag, change
all of them in one pull request and check both files with the new tag:

```sh
npx -y npm@11 exec --yes --package=github:fonoster/perfkit#vX.Y.Z -- perfkit validate perf/perfkit.yaml
npx -y npm@11 exec --yes --package=github:fonoster/perfkit#vX.Y.Z -- perfkit validate perf/perfkit.smoke.yaml
```

perfkit's sampler matches each `processes[].match` against the process's command line with a
leading space, so a pattern anchored with `^` matches nothing. Use `(^| )/roomr( |$)`.
