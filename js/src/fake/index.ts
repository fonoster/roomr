// @fonoster/roomr/fake: the client of roomr-fake (docs/FAKE.md). FakeRoomr.start() runs the fake
// (a binary, else the fonoster/roomr-fake image with host networking); createNode() starts a
// real Roomr node in it whose phone calls the node and answers what it dials, scripted from here.
import { ChildProcess, spawn, spawnSync } from "child_process";
import * as fs from "fs";
import * as net from "net";
import * as path from "path";

export type CalleeMode = "answer" | "busy" | "no_answer" | "unavailable" | "decline" | "ring";

/** Roomr gRPC methods a fault can target. */
export type RoomrMethod =
  | "CreateCall"
  | "EndCall"
  | "CreateRoom"
  | "EndRoom"
  | "AddParticipant"
  | "RemoveParticipant";

/** gRPC status codes by name ("UNAVAILABLE") or number (14). */
export type GrpcCode = string | number;

export type NodeOptions = {
  nodeRef?: string;
  region?: string;
  maxRooms?: number;
  maxParticipants?: number;
  /** Rooms the node already holds: real rooms that count toward maxRooms and never end. */
  rooms?: number;
  /** What heartbeats claim instead of the real room count (a stale heartbeat). */
  reportedRooms?: number;
  heartbeatMs?: number;
  /** Where the node opens each call's control stream (host:port). */
  sessionEndpoint?: string;
  answerTimeoutMs?: number;
  readyTimeoutMs?: number;
  mediaTimeoutMs?: number;
  emptyRoomTimeoutMs?: number;
  maxDurationMs?: number;
  codecs?: string[];
  /** How the phone answers what the node dials (default answer). */
  callee?: { mode: CalleeMode; afterMs?: number };
};

export type NodeInfo = { nodeRef: string; controlAddr: string; sipAddr: string; rooms: number };

/** The phone's side of a call (roomr's caller bot status). */
export type PhoneStatus = {
  id: string;
  /** SIP statuses the phone got (inbound) or sent (outbound), in order. */
  sip: number[];
  state: "calling" | "answered" | "ended";
  heardMs: number;
  audibleMs: number;
  audibleMsSince: number;
  lastAudibleAfterMs: number;
  firstAudibleAfterMs: number;
  spokeUntilMs: number;
  codec?: string;
  headers?: Record<string, string>;
  digits?: string;
};

export type CallStatus = {
  callRef: string;
  nodeRef: string;
  direction: "inbound" | "outbound";
  /** The node's state ("" once it forgot the call). */
  state: "ringing" | "answered" | "ended" | "";
  roomRef?: string;
  held: boolean;
  phone?: PhoneStatus;
};

export type InboundCall = {
  from?: string;
  to?: string;
  /** Becomes the call's call_ref (sent as X-Call-Ref); default a new one. */
  callRef?: string;
  /** Join or create this room (sent as X-Room-Ref). */
  roomRef?: string;
  /** More INVITE headers, e.g. X-App-Ref. */
  headers?: Record<string, string>;
  /** The phone's offer, e.g. ["PCMU/8000"]. */
  codecs?: string[];
};

export type StartOptions = {
  /** A roomr-fake binary; default $ROOMR_FAKE_BIN, else $ROOMR_BIN_DIR/roomr-fake, else Docker. */
  binary?: string;
  /** The image when Docker runs the fake (default fonoster/roomr-fake:<this package's version>). */
  image?: string;
  /** NATS for heartbeats and events (default nats://127.0.0.1:4222; "none": no NATS). */
  nats?: string;
  /** Address the nodes bind to (default 127.0.0.1). */
  host?: string;
  /** Address heartbeats and SDP announce (default host). */
  advertise?: string;
};

async function request<T>(base: string, method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(base + path, {
    method,
    headers: body === undefined ? {} : { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  if (!res.ok) {
    throw new Error(`roomr-fake ${method} ${path}: ${res.status} ${(await res.text()).trim()}`);
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  const type = res.headers.get("content-type") ?? "";
  if (type.includes("json")) return (await res.json()) as T;
  return Buffer.from(await res.arrayBuffer()) as unknown as T;
}

function freePort(): Promise<number> {
  return new Promise((resolve, reject) => {
    const s = net.createServer();
    s.once("error", reject);
    s.listen(0, "127.0.0.1", () => {
      const port = (s.address() as net.AddressInfo).port;
      s.close(() => resolve(port));
    });
  });
}

function packageVersion(): string {
  try {
    return JSON.parse(fs.readFileSync(path.join(__dirname, "..", "..", "package.json"), "utf8")).version;
  } catch {
    return "latest";
  }
}

/** A running roomr-fake: its control API at `url`. */
export class FakeRoomr {
  private process?: ChildProcess;
  private container?: string;

  constructor(readonly url: string) {}

  /** Runs a roomr-fake and resolves once its API answers. */
  static async start(o: StartOptions = {}): Promise<FakeRoomr> {
    const nats = o.nats ?? process.env.CONFORMANCE_NATS_URL ?? "nats://127.0.0.1:4222";
    const port = await freePort();
    const listen = `127.0.0.1:${port}`;
    const args = ["-listen", listen, "-nats", nats, "-host", o.host ?? "127.0.0.1"];
    if (o.advertise) args.push("-advertise", o.advertise);
    const dir = process.env.ROOMR_BIN_DIR;
    const binary =
      o.binary ?? process.env.ROOMR_FAKE_BIN ?? (dir && fs.existsSync(path.join(dir, "roomr-fake")) ? path.join(dir, "roomr-fake") : undefined);
    const fake = new FakeRoomr(`http://${listen}`);
    if (binary) {
      fake.process = spawn(binary, args, { stdio: ["ignore", "pipe", "inherit"] });
    } else {
      // Host networking: the nodes take ports at random and must reach the session endpoint.
      const image = o.image ?? `fonoster/roomr-fake:${packageVersion()}`;
      const run = spawnSync("docker", ["run", "-d", "--rm", "--network", "host", "--entrypoint", "/roomr-fake", image, ...args], {
        encoding: "utf8"
      });
      if (run.status !== 0) throw new Error(`docker run ${image}: ${run.stderr.trim()}`);
      fake.container = run.stdout.trim();
    }
    const deadline = Date.now() + 20_000;
    for (;;) {
      try {
        await request(fake.url, "GET", "/healthz");
        return fake;
      } catch (err) {
        if (Date.now() > deadline || fake.process?.exitCode != null) {
          await fake.stop();
          throw new Error(`roomr-fake did not start: ${(err as Error).message}`);
        }
        await new Promise((r) => setTimeout(r, 100));
      }
    }
  }

  /** Starts a real node in the fake. */
  async createNode(o: NodeOptions = {}): Promise<FakeNode> {
    const info = await request<NodeInfo>(this.url, "POST", "/nodes", o);
    return new FakeNode(this, info);
  }

  async nodes(): Promise<NodeInfo[]> {
    return (await request<{ nodes: NodeInfo[] }>(this.url, "GET", "/nodes")).nodes;
  }

  /** A call by call_ref, on whichever node holds it. */
  call(callRef: string): FakeCall {
    return new FakeCall(this, callRef);
  }

  /** Stops every node (between tests). */
  reset(): Promise<void> {
    return request(this.url, "POST", "/reset");
  }

  /** Exits the fake at once: every node's control streams drop without Ended. */
  async crash(): Promise<void> {
    await request(this.url, "POST", "/crash");
  }

  /** Stops the fake process or container. */
  async stop(): Promise<void> {
    if (this.process && this.process.exitCode == null) {
      const exited = new Promise((r) => this.process!.once("exit", r));
      this.process.kill("SIGKILL");
      await exited;
    }
    if (this.container) spawnSync("docker", ["rm", "-f", this.container]);
  }

  /** @internal */
  request<T>(method: string, path: string, body?: unknown): Promise<T> {
    return request<T>(this.url, method, path, body);
  }
}

/** One real node in the fake. */
export class FakeNode {
  readonly nodeRef: string;
  readonly controlAddr: string;
  readonly sipAddr: string;

  constructor(
    private readonly fake: FakeRoomr,
    info: NodeInfo
  ) {
    this.nodeRef = info.nodeRef;
    this.controlAddr = info.controlAddr;
    this.sipAddr = info.sipAddr;
  }

  private path(suffix = "") {
    return `/nodes/${encodeURIComponent(this.nodeRef)}${suffix}`;
  }

  info(): Promise<NodeInfo> {
    return this.fake.request("GET", this.path());
  }

  /** The phone calls the node; resolves once the INVITE is sent. */
  async placeCall(call: InboundCall = {}): Promise<FakeCall> {
    const status = await this.fake.request<CallStatus>("POST", this.path("/calls"), call);
    return new FakeCall(this.fake, status.callRef);
  }

  /** How the phone answers what the node dials: every call, or one call_ref. */
  callee(mode: CalleeMode, o: { afterMs?: number; callRef?: string } = {}): Promise<void> {
    return this.fake.request("POST", this.path("/callee"), { mode, ...o });
  }

  /** The next `times` calls to `method` fail with `code`. */
  failNext(method: RoomrMethod, code: GrpcCode, times = 1): Promise<void> {
    return this.fake.request("POST", this.path("/faults"), { method, code, times });
  }

  /** The next `times` calls to `method` wait `ms` before they are served. */
  stallNext(method: RoomrMethod, ms: number, times = 1): Promise<void> {
    return this.fake.request("POST", this.path("/faults"), { method, stallMs: ms, times });
  }

  clearFaults(): Promise<void> {
    return this.fake.request("DELETE", this.path("/faults"));
  }

  /** Heartbeats report `rooms` instead of the real count; null restores the real one. */
  async reportRooms(rooms: number | null): Promise<void> {
    await this.fake.request("PATCH", this.path(), { reportedRooms: rooms });
  }

  /** Drains: node.down, health NOT_SERVING; live rooms keep running. */
  down(): Promise<void> {
    return this.fake.request("POST", this.path("/down"));
  }

  /** A crash: no node.down, heartbeats and gRPC stop at once, streams drop without Ended. */
  kill(): Promise<void> {
    return this.fake.request("POST", this.path("/kill"));
  }

  /** Stops gracefully: node.down, every call ends with node_error. */
  async stop(): Promise<void> {
    await this.fake.request("DELETE", this.path()).catch(() => undefined);
  }
}

/** A call by call_ref, and the phone's leg on it. */
export class FakeCall {
  constructor(
    private readonly fake: FakeRoomr,
    readonly callRef: string
  ) {}

  private path(suffix = "") {
    return `/calls/${encodeURIComponent(this.callRef)}${suffix}`;
  }

  /** The node's state and the phone's; `since` (unix ms) adds audio measures from then on. */
  status(since?: number): Promise<CallStatus> {
    return this.fake.request("GET", this.path(since ? `?since=${since}` : ""));
  }

  /** Answers a callee ringing in mode "ring". */
  answer(): Promise<void> {
    return this.fake.request("POST", this.path("/answer"));
  }

  /** The node ends the call with this cause and SIP status, as if it had decided it. */
  end(cause: string, sipStatus?: number): Promise<void> {
    return this.fake.request("POST", this.path("/end"), { cause, sipStatus });
  }

  /** The phone hangs up (caller_hangup inbound, callee_hangup outbound). */
  hangup(): Promise<void> {
    return this.fake.request("POST", this.path("/hangup"));
  }

  /** The phone speaks: a tone, silence, or 16 kHz s16le PCM. */
  speak(audio: { kind: "tone" | `tone:${number}` | "silence"; ms: number } | { pcm: Buffer }): Promise<void> {
    const body = "pcm" in audio ? { kind: "pcm", pcm: audio.pcm.toString("base64") } : audio;
    return this.fake.request("POST", this.path("/audio"), body);
  }

  /** The phone presses keys (RFC 4733 by default). */
  dtmf(digits: string, mode: "rfc4733" | "info" | "inband" = "rfc4733"): Promise<void> {
    return this.fake.request("POST", this.path("/dtmf"), { digits, mode });
  }

  /** The phone stops sending RTP (a silent leg: media_timeout), or starts again. */
  mute(muted = true): Promise<void> {
    return this.fake.request("POST", this.path("/mute"), { muted });
  }

  /** What the phone heard, raw 16 kHz s16le. */
  heard(): Promise<Buffer> {
    return this.fake.request("GET", this.path("/heard"));
  }
}
