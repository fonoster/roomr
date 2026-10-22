# Performance: the 200-room load bar

Spec: `openspec/specs/roomr/spec.md`, "Capacity bar": a node on 2 vCPU holds 200 concurrent rooms,
each one G.711 caller plus one session participant, for 30 minutes with keypresses injected, with
zero dropped audio frames and every keypress reported exactly once. Opus-heavy load is measured and
reported separately (it is not a pass/fail bar).

**Verdict: the G.711 run meets the bar** (0 sequence gaps, 71,800 of 71,800 keypresses reported, in
every room exactly once). This is an approximation, see Method.

## Method

- Date: 2026-10-22. Git sha: `66285ff` (main at the time; no node code changed).
- Host: Apple M4 Pro, 14 cores, 24 GB, macOS 26.3.1 (arm64).
- **"2 vCPU" is approximated with `GOMAXPROCS=2` on the node. `cmd/loadgen` ran unrestricted on the
  same host**, over loopback, so the node did not have a dedicated 2-vCPU machine and the numbers
  are approximate. Opus is cgo (libopus) and the node was built normally (`go build ./cmd/roomr`).
- NATS without auth on `127.0.0.1:14222`. Nothing else heavy ran during the runs.
- Node CPU is `ps -o %cpu` sampled each second by loadgen. On macOS that value is a decayed average,
  not an instantaneous reading, so treat it as indicative; 100% is one core, so 200% would saturate
  `GOMAXPROCS=2`.
- Frames: "expected" is window length / 20 ms per call; "received" is what the session side got from
  the node; "gaps" is missing RTP sequence numbers on that side. "Received" can exceed "expected" by
  a few dozen frames because the window edges are not aligned to the 20 ms tick; gaps are the
  measure of loss. Caller side is audio the callers heard, in ms.

Node config (`roomr.yaml`; no secrets in it):

```yaml
nodeId: loadnode
nats:
  url: nats://127.0.0.1:14222
sip:
  listen: 127.0.0.1:15060   # 5060 was taken by another process on the host
  advertise: 127.0.0.1
rtp:
  host: 127.0.0.1
  advertise: 127.0.0.1
control:
  grpc:
    listen: 127.0.0.1:50061
sessionEndpoint: 127.0.0.1:50071
maxRooms: 300
```

Commands (`$NODE_PID` is the node started by the first line; loadgen serves `sessionEndpoint`):

```sh
ulimit -n 65536
GOMAXPROCS=2 ./roomr -config roomr.yaml &
./loadgen -nats nats://127.0.0.1:14222 -node-id loadnode -session 127.0.0.1:50071 \
  -sip 127.0.0.1:15060 -pid $NODE_PID <run flags>
```

| Run | `<run flags>` |
| --- | --- |
| smoke | `-calls 20 -duration 1m -codec PCMU/8000 -dtmf-every 5s` |
| G.711 (the bar) | `-calls 200 -duration 30m -codec PCMU/8000 -dtmf-every 5s` |
| Opus | `-calls 200 -duration 10m -codec opus/48000 -dtmf-every 5s` |

No impairment (`-loss`, `-jitter`, `-reorder` unset); calls placed at the default 20 per second.

## Results

| | Smoke (PCMU) | **G.711 (PCMU), the bar** | Opus |
| --- | --- | --- | --- |
| Calls / answered | 20 / 20 | 200 / 200 | 200 / 200 |
| Setup errors | 0 | 0 | 0 |
| Setup time | 1.0 s | 10.0 s | 10.0 s |
| Measured window | 60.7 s | 1801.0 s | 600.5 s |
| Session side frames expected / received | 60,700 / 60,711 | 18,009,800 / 18,009,831 | 6,004,800 / 6,004,824 |
| Session side sequence gaps | 0 | 0 | 0 |
| Caller side heard / expected (ms) | 1,214,220 / 1,214,180 | 360,210,560 / 360,196,600 | 120,101,380 / 120,096,600 |
| Keypresses injected / reported | 220 / 220 | 71,800 / 71,800 | 23,800 / 23,800 |
| Rooms with exactly the injected count | 20 / 20 | 200 / 200 | 200 / 200 |
| Node CPU avg / peak (`ps`) | 13% / 19% | 42% / 57% | 122% / 138% |
| Heartbeat rooms / participants (last) | 20 / 40 | 200 / 400 | 200 / 400 |

Keypresses: 359 per call (G.711) and 119 per call (Opus), one every 5 s, each counted as a `Dtmf`
message on the call's control stream; "reported" being equal to "injected" in every room rules out
both misses and duplicates.

Opus costs about 2.9x the CPU of G.711 here, and stayed inside two cores on this host with no gaps.
The Opus figure is not a spec bar and, like the rest, is from a shared dev machine, not 2 real vCPUs.
Raw loadgen JSON reports of these runs are not kept in the repo.
