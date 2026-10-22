// @fonoster/roomr: the Roomr contract for Node.js. The protos (roomr.v1.Roomr and
// session.v1.Session) ship in this package; RoomrClient drives a node over Roomr gRPC with
// promises. See docs/CONTRACT.md for the contract itself.
import * as path from "path";
import * as grpc from "@grpc/grpc-js";
import * as protoLoader from "@grpc/proto-loader";

/** The directory holding roomr/v1/roomr.proto and session/v1/session.proto. */
export const PROTO_DIR = path.join(__dirname, "..", "protos");

/**
 * How messages look in JavaScript: field names in camelCase, enums as their names, 64-bit
 * integers as strings, unset fields at their defaults, and the set oneof member named by its
 * oneof (e.g. `msg: "start"`).
 */
export const LOADER_OPTIONS: protoLoader.Options = {
  keepCase: false,
  longs: String,
  enums: String,
  defaults: true,
  oneofs: true,
  includeDirs: [PROTO_DIR]
};

type Loaded = {
  roomr: { v1: { Roomr: grpc.ServiceClientConstructor } };
  session: { v1: { Session: grpc.ServiceClientConstructor } };
};

let loaded: Loaded | undefined;

/** The loaded protos, as grpc-js service constructors and definitions. */
export function protos(): Loaded {
  if (!loaded) {
    const def = protoLoader.loadSync(["roomr/v1/roomr.proto", "session/v1/session.proto"], LOADER_OPTIONS);
    loaded = grpc.loadPackageDefinition(def) as unknown as Loaded;
  }
  return loaded;
}

export type SipTarget = {
  uri: string;
  proxy?: string;
  headers?: Record<string, string>;
  auth?: { username: string; password: string };
};

export type CreateCallRequest = {
  callRef: string;
  to: SipTarget;
  from?: string;
  roomRef?: string;
  ringTimeoutS?: number;
  metadata?: Record<string, string>;
  maxDurationS?: number;
};

export type Call = {
  callRef: string;
  roomRef: string;
  state: "RINGING" | "ANSWERED" | "ENDED";
  participantRef: string;
};

export type CreateRoomRequest = {
  roomRef?: string;
  maxDurationS?: number;
  emptyRoomTimeoutS?: number;
  maxParticipants?: number;
};

export type MediaEndpoint = { ip: string; port: number; codec: string; srtpKey?: string };

export type AddParticipantRequest = {
  roomRef: string;
  /** An RTP participant: the node sends the room's mix here and takes its audio. */
  rtp?: MediaEndpoint;
  /** A listen-only participant: one RTP destination per selected track (set without rtp). */
  listen?: Array<{ trackId: number; rtp: MediaEndpoint }>;
};

export type Participant = {
  participantRef: string;
  roomRef: string;
  /** RTP participants: where the participant sends its audio (the node's side of the leg). */
  rtp?: MediaEndpoint;
  trackId: number;
};

/** A refusal from a node: the gRPC status and the node's reason (ErrorInfo), when it gave one. */
export type RoomrError = grpc.ServiceError;

/** A Roomr gRPC client for one node. Every method rejects with the node's gRPC status. */
export class RoomrClient {
  private readonly client: grpc.Client & Record<string, Function>;

  constructor(
    readonly address: string,
    credentials: grpc.ChannelCredentials = grpc.credentials.createInsecure(),
    options: grpc.ClientOptions = {}
  ) {
    const Roomr = protos().roomr.v1.Roomr;
    this.client = new Roomr(address, credentials, options) as grpc.Client & Record<string, Function>;
  }

  createCall(req: CreateCallRequest, deadlineMs = 10_000): Promise<Call> {
    return this.unary("createCall", req, deadlineMs);
  }

  endCall(callRef: string, deadlineMs = 10_000): Promise<void> {
    return this.unary("endCall", { callRef }, deadlineMs);
  }

  createRoom(req: CreateRoomRequest = {}, deadlineMs = 10_000): Promise<{ roomRef: string }> {
    return this.unary("createRoom", req, deadlineMs);
  }

  endRoom(roomRef: string, deadlineMs = 10_000): Promise<void> {
    return this.unary("endRoom", { roomRef }, deadlineMs);
  }

  addParticipant(req: AddParticipantRequest, deadlineMs = 10_000): Promise<Participant> {
    return this.unary("addParticipant", req, deadlineMs);
  }

  removeParticipant(roomRef: string, participantRef: string, deadlineMs = 10_000): Promise<void> {
    return this.unary("removeParticipant", { roomRef, participantRef }, deadlineMs);
  }

  close() {
    this.client.close();
  }

  private unary<T>(method: string, req: unknown, deadlineMs: number): Promise<T> {
    return new Promise((resolve, reject) => {
      this.client[method](req, { deadline: Date.now() + deadlineMs }, (err: grpc.ServiceError | null, res: T) =>
        err ? reject(err) : resolve(res)
      );
    });
  }
}
