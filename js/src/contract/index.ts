// @fonoster/roomr/contract: the contract suite (openspec add-roomr-fake, "One contract suite
// proves the fake and the node"). describeContract registers mocha cases, CON-001 and up, that
// any target passes: the real node (realTarget) and roomr-fake (fakeTarget). A platform can run
// it against the image it pins.
import { strict as assert } from "assert";
import * as grpc from "@grpc/grpc-js";
import { connect, NatsConnection, StringCodec, Subscription } from "nats";
import { RoomrClient } from "..";
import { TestApp } from "./app";
import { ContractNode, ContractTarget } from "./targets";

export * from "./app";
export * from "./targets";

const sc = StringCodec();
let seq = 0;
const ref = (prefix: string) => `${prefix}-${process.pid}-${Date.now()}-${++seq}`;

/** The next message on a subscription that ok accepts, as JSON. */
async function next(sub: Subscription, ok: (ev: Record<string, unknown>) => boolean, ms = 5000) {
  const timer = setTimeout(() => sub.unsubscribe(), ms);
  for await (const m of sub) {
    const ev = JSON.parse(sc.decode(m.data)) as Record<string, unknown>;
    if (ok(ev)) {
      clearTimeout(timer);
      sub.unsubscribe();
      return ev;
    }
  }
  throw new Error(`no matching message on ${sub.getSubject()} within ${ms} ms`);
}

const waitUntil = async (ok: () => Promise<boolean>, ms = 5000) => {
  const deadline = Date.now() + ms;
  while (!(await ok())) {
    if (Date.now() > deadline) throw new Error("timed out");
    await new Promise((r) => setTimeout(r, 50));
  }
};

const grpcCode = async (p: Promise<unknown>) => {
  try {
    await p;
    return grpc.status.OK;
  } catch (err) {
    return (err as grpc.ServiceError).code;
  }
};

/**
 * Registers the contract cases against a target. natsUrl is where the target's nodes publish
 * (default $CONFORMANCE_NATS_URL, else nats://127.0.0.1:4222).
 */
export function describeContract(makeTarget: () => Promise<ContractTarget>, natsUrl?: string) {
  describe("Roomr contract", function () {
    let target: ContractTarget;
    let nc: NatsConnection;
    let app: TestApp;
    let node: ContractNode;
    let roomr: RoomrClient;

    before(async function () {
      target = await makeTarget();
      nc = await connect({ servers: natsUrl ?? process.env.CONFORMANCE_NATS_URL ?? "nats://127.0.0.1:4222" });
      app = await TestApp.start();
      node = await target.startNode({ sessionEndpoint: app.address, maxRooms: 50 });
      roomr = new RoomrClient(node.controlAddr);
    });

    after(async function () {
      roomr?.close();
      await node?.stop();
      app?.stop();
      await nc?.close();
      await target?.close();
    });

    beforeEach(function () {
      app.mode = { kind: "answer" };
    });

    // Flushed, so the server has the subscription before the call that publishes on it starts.
    const subscribe = async (subject: string) => {
      const sub = nc.subscribe(subject);
      await nc.flush();
      return sub;
    };
    const events = (callRef: string) => subscribe(`roomr.events.${callRef}`);
    const outbound = (callRef: string, metadata: Record<string, string> = {}) =>
      roomr.createCall({ callRef, to: { uri: "sip:+15551230000@carrier", headers: { "X-Call-Ref": callRef } }, metadata });

    it("[CON-001] heartbeats name the node, its control address and its room limit", async function () {
      const hb = await next(nc.subscribe(`roomr.nodes.${node.nodeRef}.heartbeat`), (ev) => ev.type === "node.heartbeat");
      assert.equal(hb.node_ref, node.nodeRef);
      assert.equal(hb.control_addr, node.controlAddr);
      assert.equal(hb.max_rooms, 50);
    });

    it("[CON-002] CreateRoom refuses a ref in use and EndRoom an unknown one", async function () {
      const roomRef = ref("room");
      assert.equal((await roomr.createRoom({ roomRef })).roomRef, roomRef);
      assert.equal(await grpcCode(roomr.createRoom({ roomRef })), grpc.status.ALREADY_EXISTS);
      await roomr.endRoom(roomRef);
      assert.equal(await grpcCode(roomr.endRoom(ref("nope"))), grpc.status.NOT_FOUND);
    });

    it("[CON-003] a node at its room limit refuses with RESOURCE_EXHAUSTED", async function () {
      const small = await target.startNode({ sessionEndpoint: app.address, maxRooms: 1 });
      const c = new RoomrClient(small.controlAddr);
      try {
        await c.createRoom();
        assert.equal(await grpcCode(c.createRoom()), grpc.status.RESOURCE_EXHAUSTED);
      } finally {
        c.close();
        await small.stop();
      }
    });

    it("[CON-004] an answered CreateCall opens a stream with Start OUTBOUND and the metadata", async function () {
      const callRef = ref("out");
      const sub = await events(callRef);
      await node.callee("answer", callRef);
      const call = await outbound(callRef, { app_ref: "app-1" });
      assert.equal(call.callRef, callRef);
      const c = await app.call(callRef);
      assert.equal(c.start.direction, "OUTBOUND");
      assert.equal(c.start.metadata.app_ref, "app-1");
      const ev = await next(sub, (ev) => ev.type === "call.answered");
      assert.equal(ev.call_ref, callRef);
      await roomr.endCall(callRef);
    });

    it("[CON-005] a repeated CreateCall returns the same call and dials once", async function () {
      const callRef = ref("dup");
      await node.callee("ring", callRef);
      const first = await outbound(callRef);
      const again = await outbound(callRef);
      assert.equal(again.callRef, first.callRef);
      assert.equal(again.roomRef, first.roomRef);
      await waitUntil(async () => {
        try {
          await node.answerRinging(callRef);
          return true;
        } catch {
          return false;
        }
      });
      await app.call(callRef);
      await new Promise((r) => setTimeout(r, 300));
      assert.equal([...app.calls.keys()].filter((k) => k === callRef).length, 1);
      await roomr.endCall(callRef);
    });

    it("[CON-006] a busy callee ends the call with busy and 486, and no stream opens", async function () {
      const callRef = ref("busy");
      const sub = await events(callRef);
      await node.callee("busy", callRef);
      await outbound(callRef);
      const ended = await next(sub, (ev) => ev.type === "call.ended");
      assert.equal(ended.cause, "busy");
      assert.equal(ended.sip_status, 486);
      assert.equal(app.calls.has(callRef), false);
    });

    it("[CON-007] an inbound call opens a stream with Start INBOUND and its headers; Answer gives 200", async function () {
      const callRef = ref("in");
      const phone = await node.placeCall(callRef, { "X-App-Ref": "app-7" });
      const c = await app.call(callRef);
      assert.equal(c.start.direction, "INBOUND");
      assert.equal(c.start.headers["X-App-Ref"], "app-7");
      await waitUntil(async () => (await phone.status()).sip.includes(200));
      const sip = (await phone.status()).sip;
      assert.ok(sip.indexOf(180) >= 0 && sip.indexOf(180) < sip.indexOf(200), `statuses ${sip}`);
      await phone.hangup();
    });

    it("[CON-008] a key the caller presses reaches the app as Dtmf", async function () {
      const callRef = ref("dtmf");
      const phone = await node.placeCall(callRef);
      const c = await app.call(callRef);
      await waitUntil(async () => (await phone.status()).state === "answered");
      await phone.dtmf("5");
      const m = await c.waitFor((m) => !!m.dtmf);
      assert.equal(m.dtmf!.digit, "5");
      await phone.hangup();
    });

    it("[CON-009] the caller's audio reaches the app's RTP endpoint", async function () {
      const callRef = ref("rtp");
      const phone = await node.placeCall(callRef);
      const c = await app.call(callRef);
      await waitUntil(async () => (await phone.status()).state === "answered");
      await phone.speak(400);
      await waitUntil(async () => c.rtpPackets >= 10);
      await phone.hangup();
    });

    it("[CON-010] the caller hanging up ends the stream with caller_hangup", async function () {
      const callRef = ref("bye");
      const phone = await node.placeCall(callRef);
      const c = await app.call(callRef);
      await waitUntil(async () => (await phone.status()).state === "answered");
      await phone.hangup();
      assert.equal((await c.ended()).cause, "caller_hangup");
    });

    it("[CON-011] EndCall ends an answered call's stream with removed", async function () {
      const callRef = ref("rm");
      await node.callee("answer", callRef);
      await outbound(callRef);
      const c = await app.call(callRef);
      await waitUntil(async () => (await (await node.calleeLeg(callRef)).status().catch(() => ({ state: "" }))).state === "answered");
      await roomr.endCall(callRef);
      assert.equal((await c.ended()).cause, "removed");
      assert.equal(await grpcCode(roomr.endCall(ref("nope"))), grpc.status.NOT_FOUND);
    });

    it("[CON-012] the app's Reject reaches the caller as its SIP status", async function () {
      app.mode = { kind: "reject", sipStatus: 486 };
      const callRef = ref("rej");
      const phone = await node.placeCall(callRef);
      await app.call(callRef);
      await waitUntil(async () => (await phone.status()).sip.includes(486));
    });

    it("[CON-013] a draining node publishes node.down", async function () {
      const n = await target.startNode({ sessionEndpoint: app.address });
      try {
        const sub = await subscribe(`roomr.nodes.${n.nodeRef}.heartbeat`);
        await next(nc.subscribe(`roomr.nodes.${n.nodeRef}.heartbeat`), (ev) => ev.type === "node.heartbeat");
        await n.down();
        await next(sub, (ev) => ev.type === "node.down");
      } finally {
        await n.stop();
      }
    });
  });
}
