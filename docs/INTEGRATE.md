# Integrating with Roomr

To put Roomr in a platform you write two small things, and you can write them in any language with
gRPC and RTP:

1. A **controller** that calls `roomr.v1.Roomr` to place calls and manage rooms.
2. An **app** that serves `session.v1.Session`, receives each call as a stream, and exchanges audio
   with the node over RTP.

Read [ARCHITECTURE.md](ARCHITECTURE.md) first for the picture and [CONTRACT.md](CONTRACT.md) for
the exact messages.

## 1. Run a node

```sh
docker run --rm --network host \
  -v "$PWD/roomr.yaml:/config/roomr.yaml:ro" \
  fonoster/roomr
```

A minimal `roomr.yaml` (every key is in [CONFIGURATION.md](CONFIGURATION.md); the Compose
examples are in [`examples/`](../examples), and the Helm chart in [`charts/roomr`](../charts/roomr)
renders one for Kubernetes):

```yaml
nodeId: node-a
sip:
  listen: 0.0.0.0:5060
  advertise: 192.0.2.10          # the address the proxy and callers can reach
  outboundProxy: sip:proxy:5060;transport=tcp
rtp:
  advertise: 192.0.2.10
  portMin: 30000
  portMax: 30199                 # publish only this range
control:
  grpc:
    listen: 0.0.0.0:50061
sessionEndpoint: app.internal:50070   # where each call's stream goes
nats:
  url: nats://nats:4222          # optional in practice: best effort
```

Check it is up: `grpc_health_probe -addr localhost:50061` reports `SERVING`.

## 2. Write the app

The app is a gRPC server for `session.v1.Session`. For each call the node connects, sends `Start`,
and waits for `Answer` (inbound) or `Ready` (outbound). The sequence for an inbound call, in
TypeScript with `@grpc/grpc-js`:

```ts
import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";

const def = protoLoader.loadSync("api/session/v1/session.proto", { keepCase: true, defaults: true });
const { session } = grpc.loadPackageDefinition(def) as any;

const server = new grpc.Server({
  // The node pings every 10 s: allow it, or the call ends with app_gone.
  "grpc.http2.min_ping_interval_without_data_ms": 5000,
  "grpc.keepalive_permit_without_calls": 1
});

server.addService(session.v1.Session.service, {
  Connect(call: grpc.ServerDuplexStream<any, any>) {
    call.on("data", (msg: any) => {
      if (msg.start) {
        const rtp = openRtpSocket(); // your RTP socket: L16/16000, payload type 96, 20 ms
        call.write({
          answer: { media: { ip: "192.0.2.20", port: rtp.port, codec: "L16/16000" } }
        });
        pumpAudio(rtp, msg.start.media); // send to msg.start.media.{ip,port}, receive the room's mix
      } else if (msg.dtmf) {
        console.log("digit", msg.dtmf.digit);
      } else if (msg.event?.ended) {
        console.log("ended", msg.event.ended.cause);
        call.end();
      }
    });
    call.on("error", () => {}); // a broken stream ends the call on the node
  }
});

server.bindAsync("0.0.0.0:50070", grpc.ServerCredentials.createInsecure(), () => {});
```

What the app must do, whatever the language:

- **Answer fast.** Inbound callers hear ringing until `Answer`; after `answerTimeout` (default
  10 s) they get `480`. For outbound calls send `Ready` within `readyTimeout`.
- **Send and receive RTP L16/16000**, mono, 20 ms packets, payload type 96, to and from the
  endpoints in `Start.media` and your `Answer.media`. Keep sending (silence is fine): a silent app
  leg ends the call with `app_gone` after `mediaTimeout`.
- **Allow keepalive pings** on its gRPC server (above).
- **Treat `Ended` as final.** Close the stream; the node never reconnects.
- **Use `metadata`** from `CreateCall` (returned in `Start.metadata`) to carry your own ids. Roomr
  never reads it.

Other things an app does on the same stream: `Hold`/`Resume`, `SendDtmf`, `Transfer`,
`AddListener` (for recording), `Hangup`. See [CONTRACT.md](CONTRACT.md).

## 3. Write the controller

The controller calls the node, usually one it picked from the heartbeats:

```ts
const def = protoLoader.loadSync("api/roomr/v1/roomr.proto", { keepCase: true, defaults: true });
const { roomr } = grpc.loadPackageDefinition(def) as any;
const client = new roomr.v1.Roomr("node-a:50061", grpc.credentials.createInsecure());

client.CreateCall(
  {
    call_ref: "call-123",              // idempotent for ten minutes
    from: "+15551230000",
    to: { uri: "sip:+15551234567@carrier.example" },
    session: "app.internal:50070",     // optional: else the node's sessionEndpoint
    metadata: { agent: "support" }
  },
  (err: any, call: any) => console.log(err?.message ?? call.state)  // RINGING
);
```

To pick nodes, subscribe to `roomr.nodes.*.heartbeat` on NATS. Each heartbeat has `control_addr`,
`rooms` and `max_rooms`; `node.down` means stop choosing it. To follow calls, subscribe to
`roomr.events.<call_ref>` and use `seq` to detect gaps. Events are best effort: reconcile with the
RPCs if you need certainty.

## 4. Connect a SIP proxy

Point the proxy's route for your numbers at the node's `sip.listen`. A node can register itself
with the proxy: add an entry under `sip.registrations` (with an optional `X-Session-Count` header
so the proxy can balance by load). Keep the node's SIP and control ports private: the proxy may
reach SIP, only the controller may reach gRPC.

## Testing your integration

- Run the real node with the example configs in [`examples/`](../examples).
- The [`cmd/sipcaller`](../cmd/sipcaller) tool places a SIP call into a node, and
  [`cmd/loadgen`](../cmd/loadgen) drives many calls and serves a session endpoint.
- For tests that must not run a real node, a fake is planned (see below).

## Testing without a real node: the fake (planned, not yet available)

A fake node, `fonoster/roomr-fake`, is planned in
[#13](https://github.com/fonoster/roomr/issues/13): it will implement the same two gRPC interfaces
and the NATS events, with no SIP and no media, scripted over a small control API (refuse a call,
go silent, end with a given cause), with the same contract tests run against the real node and the
fake in this repository's CI so the fake cannot drift from the node. Until it ships, test against
the real node with the tools above.
