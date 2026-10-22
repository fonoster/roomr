// The two targets the contract suite runs against: the real node binary with the SIP caller bot
// as its phone, and roomr-fake. Both offer the same small interface, so the cases cannot tell them
// apart.
import { ChildProcess, spawn } from "child_process";
import { randomBytes } from "crypto";
import * as fs from "fs";
import * as net from "net";
import * as os from "os";
import * as path from "path";
import * as readline from "readline";
import { CalleeMode, FakeRoomr } from "../fake";

export type NodeSetup = { sessionEndpoint: string; maxRooms?: number; heartbeatMs?: number };

/** The phone's side of one call. */
export interface Phone {
  status(): Promise<{ sip: number[]; state: string; digits?: string }>;
  dtmf(digits: string): Promise<void>;
  /** Speaks a tone for ms. */
  speak(ms: number): Promise<void>;
  hangup(): Promise<void>;
}

export interface ContractNode {
  nodeRef: string;
  controlAddr: string;
  /** How the phone answers the node's INVITE for callRef (CreateCall puts X-Call-Ref in to.headers). */
  callee(mode: CalleeMode, callRef: string): Promise<void>;
  /** Answers the INVITE for callRef ringing in mode "ring". */
  answerRinging(callRef: string): Promise<void>;
  /** The phone calls the node with this call_ref (X-Call-Ref) and headers. */
  placeCall(callRef: string, headers?: Record<string, string>): Promise<Phone>;
  /** The phone's leg on an outbound call it answered. */
  calleeLeg(callRef: string): Promise<Phone>;
  /** Drains: node.down. */
  down(): Promise<void>;
  stop(): Promise<void>;
}

export interface ContractTarget {
  name: string;
  startNode(setup: NodeSetup): Promise<ContractNode>;
  close(): Promise<void>;
}

const newRef = (prefix: string) => `${prefix}-${randomBytes(4).toString("hex")}`;

// ---- the fake --------------------------------------------------------------------------------

/** roomr-fake (FakeRoomr.start options apply: a binary, else the image). */
export async function fakeTarget(fake?: FakeRoomr): Promise<ContractTarget> {
  const f = fake ?? (await FakeRoomr.start());
  return {
    name: "roomr-fake",
    async startNode(s) {
      const n = await f.createNode({ nodeRef: newRef("fake"), sessionEndpoint: s.sessionEndpoint, maxRooms: s.maxRooms,
        heartbeatMs: s.heartbeatMs ?? 200 });
      const phone = (ref: string): Phone => {
        const c = f.call(ref);
        return {
          status: async () => (await c.status()).phone ?? { sip: [], state: "calling" },
          dtmf: (d) => c.dtmf(d),
          speak: (ms) => c.speak({ kind: "tone", ms }),
          hangup: () => c.hangup()
        };
      };
      return {
        nodeRef: n.nodeRef,
        controlAddr: n.controlAddr,
        callee: (mode, callRef) => n.callee(mode, { callRef }),
        answerRinging: (callRef) => f.call(callRef).answer(),
        placeCall: async (callRef, headers) => {
          await n.placeCall({ callRef, headers });
          return phone(callRef);
        },
        calleeLeg: async (callRef) => phone(callRef),
        down: () => n.down(),
        stop: () => n.stop()
      };
    },
    async close() {
      if (!fake) await f.stop();
    }
  };
}

// ---- the real node -------------------------------------------------------------------------

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

/** Spawns a command and resolves with the first stdout line that matches. */
function spawnUntil(cmd: string, args: string[], match: RegExp): Promise<{ proc: ChildProcess; line: string }> {
  const proc = spawn(cmd, args, { stdio: ["ignore", "pipe", "pipe"] });
  let stderr = "";
  proc.stderr!.on("data", (d) => (stderr = (stderr + d).slice(-4000)));
  return new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error(`${cmd}: not ready in 15 s\n${stderr}`)), 15_000);
    readline.createInterface({ input: proc.stdout! }).on("line", (line) => {
      if (match.test(line)) {
        clearTimeout(timer);
        resolve({ proc, line });
      }
    });
    proc.once("exit", (code) => {
      clearTimeout(timer);
      reject(new Error(`${cmd} exited with ${code}\n${stderr}`));
    });
  });
}

async function http<T>(base: string, method: string, p: string, body?: unknown): Promise<T> {
  const res = await fetch(base + p, {
    method,
    headers: body === undefined ? {} : { "content-type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body)
  });
  if (!res.ok) throw new Error(`sipcaller ${method} ${p}: ${res.status} ${await res.text()}`);
  return (res.status === 204 ? undefined : await res.json()) as T;
}

/**
 * The real roomr binary, with sipcaller as its phone: binDir holds roomr and sipcaller (default
 * $ROOMR_BIN_DIR).
 */
export async function realTarget(o: { binDir?: string; nats?: string } = {}): Promise<ContractTarget> {
  const binDir = o.binDir ?? process.env.ROOMR_BIN_DIR;
  if (!binDir) throw new Error("realTarget: set binDir or ROOMR_BIN_DIR (roomr and sipcaller)");
  const nats = o.nats ?? process.env.CONFORMANCE_NATS_URL ?? "nats://127.0.0.1:4222";
  const procs: ChildProcess[] = [];
  return {
    name: "roomr",
    async startNode(s) {
      const bot = await spawnUntil(path.join(binDir, "sipcaller"), ["-listen", "127.0.0.1:0"], /^listening /);
      procs.push(bot.proc);
      const api = "http://" + bot.line.replace(/^listening /, "").trim();
      const { address } = await http<{ address: string }>(api, "GET", "/sip");
      const nodeRef = newRef("roomr");
      const [sipPort, grpcPort] = [await freePort(), await freePort()];
      const ms = s.heartbeatMs ?? 200;
      const config = [
        `nodeId: ${nodeRef}`,
        `sessionEndpoint: ${s.sessionEndpoint}`,
        `nats: { url: "${nats}" }`,
        `heartbeatInterval: ${ms}ms`,
        `drainWait: 1s`,
        ...(s.maxRooms ? [`maxRooms: ${s.maxRooms}`] : []),
        `sip: { listen: "127.0.0.1:${sipPort}", advertise: "127.0.0.1", outboundProxy: "sip:${address}", callRefHeader: X-Call-Ref }`,
        `rtp: { host: 127.0.0.1 }`,
        `control: { grpc: { listen: "127.0.0.1:${grpcPort}" } }`
      ].join("\n");
      const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), "roomr-")), "roomr.yaml");
      fs.writeFileSync(file, config + "\n");
      const node = await spawnUntil(path.join(binDir, "roomr"), ["-config", file], / ready /);
      procs.push(node.proc);
      const sipAddr = `127.0.0.1:${sipPort}`;
      const inbound = new Map<string, string>();
      const phone = (id: () => Promise<string>): Phone => ({
        status: async () => http(api, "GET", `/calls/${await id()}`),
        dtmf: async (digits) => void (await http(api, "POST", `/calls/${await id()}/dtmf`, { digits, mode: "rfc4733" })),
        speak: async (ms) => void (await http(api, "POST", `/calls/${await id()}/audio`, { kind: "tone", ms })),
        hangup: async () => void (await http(api, "POST", `/calls/${await id()}/hangup`))
      });
      return {
        nodeRef,
        controlAddr: `127.0.0.1:${grpcPort}`,
        callee: (mode, callRef) => http(api, "POST", "/callee", { mode, header: "X-Call-Ref", key: callRef }),
        answerRinging: (callRef) => http(api, "POST", "/callee/answer", { header: "X-Call-Ref", key: callRef }),
        placeCall: async (callRef, headers = {}) => {
          const st = await http<{ id: string }>(api, "POST", "/calls", { target: sipAddr, to: "+15550002222",
            headers: { ...headers, "X-Call-Ref": callRef } });
          inbound.set(callRef, st.id);
          return phone(async () => st.id);
        },
        calleeLeg: async (callRef) =>
          phone(async () => {
            const { calls } = await http<{ calls: Array<{ id: string; headers?: Record<string, string> }> }>(api, "GET", "/calls");
            const c = calls.find((c) => Object.entries(c.headers ?? {}).some(([k, v]) => k.toLowerCase() === "x-call-ref" && v === callRef));
            if (!c) throw new Error(`the phone has no leg for ${callRef}`);
            return c.id;
          }),
        down: async () => void node.proc.kill("SIGTERM"),
        stop: async () => {
          node.proc.kill("SIGKILL");
          bot.proc.kill("SIGKILL");
        }
      };
    },
    async close() {
      for (const p of procs) if (p.exitCode == null) p.kill("SIGKILL");
    }
  };
}
