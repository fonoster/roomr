// The app behind the benchmarks: serves session.v1.Session on 127.0.0.1:50071 (roomr.yaml's
// sessionEndpoint), answers every inbound call with an L16/16000 RTP endpoint and echoes each
// packet back to the node, so neither leg goes silent (mediaTimeout). It is not under test.
import * as dgram from "node:dgram";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const grpc = require("@grpc/grpc-js");
const { protos } = require("@fonoster/roomr");

const HOST = "127.0.0.1";
const PORT = 50071;

const server = new grpc.Server({
  // The node pings every 10 s on each control stream; grpc-js refuses pings more often than 5 min by default.
  "grpc.http2.min_ping_interval_without_data_ms": 5000,
  "grpc.keepalive_permit_without_calls": 1
});

server.addService(protos().session.v1.Session.service, {
  connect(stream) {
    let rtp;
    stream.on("data", (m) => {
      if (!m.start || rtp) return;
      rtp = dgram.createSocket("udp4");
      rtp.on("message", (pkt, from) => rtp.send(pkt, from.port, from.address));
      rtp.bind(0, HOST, () => {
        const media = { ip: HOST, port: rtp.address().port, codec: "L16/16000" };
        stream.write(m.start.direction === "OUTBOUND" ? { ready: { media } } : { answer: { media } });
      });
    });
    const close = () => {
      rtp?.close();
      rtp = undefined;
    };
    stream.on("end", () => {
      close();
      stream.end();
    });
    stream.on("error", close);
  }
});

server.bindAsync(`${HOST}:${PORT}`, grpc.ServerCredentials.createInsecure(), (err) => {
  if (err) throw err;
  console.log(`app: session endpoint on ${HOST}:${PORT}`);
});
