// A session endpoint for contract tests: serves session.v1.Session like an app behind its
// Service, answers inbound calls and is Ready for outbound ones with a real RTP endpoint (or
// rejects, as scripted), and records every message of every call.
import * as dgram from "dgram";
import { EventEmitter } from "events";
import * as grpc from "@grpc/grpc-js";
import { protos } from "..";

/** One message from the node, as loaded: `msg` names the set member (start, dtmf or event). */
export type NodeMessage = {
  msg: "start" | "dtmf" | "event";
  start?: {
    callRef: string;
    direction: "INBOUND" | "OUTBOUND";
    from: string;
    to: string;
    headers: Record<string, string>;
    metadata: Record<string, string>;
    roomRef: string;
    nodeRef: string;
    media?: { ip: string; port: number; codec: string };
  };
  dtmf?: { digit: string; source: string };
  event?: { event: string; ended?: { cause: string; sipStatus: number }; [k: string]: unknown };
};

export type AppMode = { kind: "answer" } | { kind: "reject"; sipStatus: number };

const CODEC = "L16/16000";

/** One control stream the app accepted. */
export class AppCall extends EventEmitter {
  readonly messages: NodeMessage[] = [];
  /** RTP packets the app received from the node. */
  rtpPackets = 0;

  constructor(
    readonly start: NonNullable<NodeMessage["start"]>,
    private readonly stream: grpc.ServerDuplexStream<unknown, unknown>,
    readonly rtp: dgram.Socket
  ) {
    super();
    rtp.on("message", () => this.rtpPackets++);
  }

  get callRef() {
    return this.start.callRef;
  }

  /** @internal */
  push(m: NodeMessage) {
    this.messages.push(m);
    this.emit("message", m);
  }

  /** The first message (already received or next) that ok accepts. */
  waitFor(ok: (m: NodeMessage) => boolean, ms = 5000): Promise<NodeMessage> {
    const seen = this.messages.find(ok);
    if (seen) return Promise.resolve(seen);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.off("message", on);
        reject(new Error(`call ${this.callRef}: no such message within ${ms} ms`));
      }, ms);
      const on = (m: NodeMessage) => {
        if (!ok(m)) return;
        clearTimeout(timer);
        this.off("message", on);
        resolve(m);
      };
      this.on("message", on);
    });
  }

  /** The call's Ended event. */
  async ended(ms = 5000): Promise<{ cause: string; sipStatus: number }> {
    const m = await this.waitFor((m) => !!m.event?.ended, ms);
    return m.event!.ended!;
  }

  /** The app hangs up. */
  hangup() {
    this.stream.write({ hangup: {} });
  }
}

export class TestApp extends EventEmitter {
  mode: AppMode = { kind: "answer" };
  readonly calls = new Map<string, AppCall>();
  private server = new grpc.Server();

  private constructor(readonly address: string) {
    super();
  }

  /** Serves on 127.0.0.1 at a free port. */
  static async start(host = "127.0.0.1"): Promise<TestApp> {
    const server = new grpc.Server();
    const port = await new Promise<number>((resolve, reject) =>
      server.bindAsync(`${host}:0`, grpc.ServerCredentials.createInsecure(), (err, p) => (err ? reject(err) : resolve(p)))
    );
    const app = new TestApp(`${host}:${port}`);
    app.server = server;
    server.addService(protos().session.v1.Session.service, {
      connect: (stream: grpc.ServerDuplexStream<unknown, unknown>) => app.accept(stream, host)
    });
    return app;
  }

  private accept(stream: grpc.ServerDuplexStream<unknown, unknown>, host: string) {
    let call: AppCall | undefined;
    stream.on("data", async (raw: unknown) => {
      const m = raw as NodeMessage;
      if (m.start && !call) {
        const rtp = dgram.createSocket("udp4");
        await new Promise<void>((r) => rtp.bind(0, host, r));
        call = new AppCall(m.start, stream, rtp);
        this.calls.set(call.callRef, call);
        const media = { ip: host, port: rtp.address().port, codec: CODEC };
        if (m.start.direction === "OUTBOUND") stream.write({ ready: { media } });
        else if (this.mode.kind === "reject") stream.write({ reject: { sipStatus: this.mode.sipStatus } });
        else stream.write({ answer: { media } });
        call.push(m);
        this.emit("call", call);
        return;
      }
      call?.push(m);
    });
    const close = () => {
      call?.rtp.close();
      stream.end();
    };
    stream.on("end", close);
    stream.on("error", () => call?.rtp.close());
  }

  /** The call with this call_ref once its Start arrived. */
  call(callRef: string, ms = 5000): Promise<AppCall> {
    const seen = this.calls.get(callRef);
    if (seen) return Promise.resolve(seen);
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => {
        this.off("call", on);
        reject(new Error(`no Start for ${callRef} within ${ms} ms`));
      }, ms);
      const on = (c: AppCall) => {
        if (c.callRef !== callRef) return;
        clearTimeout(timer);
        this.off("call", on);
        resolve(c);
      };
      this.on("call", on);
    });
  }

  stop() {
    this.server.forceShutdown();
    for (const c of this.calls.values()) {
      try {
        c.rtp.close();
      } catch {
        // already closed
      }
    }
  }
}
