// Corndogs client transport: CSIL-RPC over a persistent TCP connection.
//
// This is Corndogs' own, ready-to-use carrier — you do not need to write one.
// Point it at a corndogs server's TCP address and hand it to the generated
// async client:
//
//   import { AsyncApiClient } from "./index.ts";
//   import { TcpTransport } from "./transport.ts";
//
//   const tr = new TcpTransport("localhost:5080");
//   tr.startHeartbeat();                 // keep the connection alive (background)
//   const client = new AsyncApiClient(tr);
//   const resp = await client.corndogs.submitTask({ queue: "q", currentState: "submitted", ... });
//
// The wire is CSIL-RPC framed with the canonical 4-byte big-endian length
// prefix over TCP. This carrier owns only the envelope + framing + connection;
// the generated codec owns (de)serialization. Dependency-free (Node's `net`
// plus this package's own CBOR codec — no CBOR library, no HTTP).
//
// One connection MULTIPLEXES many concurrent calls: each request carries a
// correlation `id`, a background reader matches each response back to its
// waiting caller, so concurrent `submitTask`/`getNextTask` calls do not
// head-of-line-block each other. TypeScript is Promise-based, so this is the
// natural shape: one socket, a pending map of id -> resolver.
//
// Only the async client (`AsyncServiceTransport`, `CorndogsAsyncClient`) is
// implemented: Node's `net.Socket` is non-blocking by nature, so a
// synchronous `ServiceTransport` would require blocking I/O tricks that
// aren't idiomatic here.

import * as net from "node:net";
import type { AsyncServiceTransport } from "./client.async.gen.ts";
import {
  type CborTag,
  type CborValue,
  decode,
  encodeValue,
  mapGet,
  asNumber,
  asString,
  asBytes,
  fromServiceErrorCborValue,
} from "./codec.gen.ts";

const TAG_ENCODED_CBOR = 24; // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
const MAX_FRAME = 1025 * (1 << 20); // payload maximum plus envelope allowance
const DEFAULT_CONNECT_TIMEOUT_MS = 5000;
const DEFAULT_HEARTBEAT_INTERVAL_MS = 15000;
const CONTROL_SERVICE = "CorndogsService";
const OP_PING = "$ping"; // control-plane heartbeat op (never collides with app ops)

/** A transport-level failure: connection dropped, timed out, or the transport
 * envelope carried a non-zero status. Distinct from {@link ServiceError}. */
export class TransportError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "TransportError";
  }
}

/** A typed application error returned by the server (status 0, `variant ===
 * "ServiceError"`). Carries the same `code`/`message` as the generated
 * `ServiceError` type. */
export class ServiceError extends Error {
  constructor(
    readonly code: number,
    message: string,
  ) {
    super(message);
    this.name = "ServiceError";
  }
}

export interface TcpTransportOptions {
  /** Dial timeout in milliseconds. Default 5000. */
  connectTimeoutMs?: number;
}

function splitAddr(addr: string): { host: string; port: number } {
  const idx = addr.lastIndexOf(":");
  if (idx <= 0) {
    throw new Error(`corndogs: address must be host:port, got ${JSON.stringify(addr)}`);
  }
  const host = addr.slice(0, idx);
  const port = Number(addr.slice(idx + 1));
  if (!Number.isInteger(port) || port <= 0) {
    throw new Error(`corndogs: invalid port in address ${JSON.stringify(addr)}`);
  }
  return { host, port };
}

function isCborTag(v: CborValue): v is CborTag {
  return (
    typeof v === "object" &&
    v !== null &&
    !Array.isArray(v) &&
    !(v instanceof Map) &&
    !(v instanceof Uint8Array) &&
    "tag" in v
  );
}

function sleepOrAbort(ms: number, signal: AbortSignal): Promise<"aborted" | "elapsed"> {
  return new Promise((resolve) => {
    if (signal.aborted) {
      resolve("aborted");
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener("abort", onAbort);
      resolve("elapsed");
    }, ms);
    const onAbort = () => {
      clearTimeout(timer);
      resolve("aborted");
    };
    signal.addEventListener("abort", onAbort, { once: true });
  });
}

interface Pending {
  resolve: (payload: Uint8Array) => void;
  reject: (err: Error) => void;
}

/**
 * Async CSIL-RPC transport over one persistent, multiplexed TCP connection.
 * Implements the generated `AsyncServiceTransport` (`call`). The connection
 * is dialed lazily (on first `call`, or eagerly via `TcpTransport.connect`)
 * and re-dialed on failure.
 */
export class TcpTransport implements AsyncServiceTransport {
  private readonly host: string;
  private readonly port: number;
  private readonly connectTimeoutMs: number;

  private socket: net.Socket | null = null;
  private dialing: Promise<net.Socket> | null = null;
  private recvBuf: Buffer = Buffer.alloc(0);
  private nextId = 0;
  private pending = new Map<number, Pending>();
  private closed = false;

  private hbController: AbortController | null = null;

  constructor(addr: string, options: TcpTransportOptions = {}) {
    const { host, port } = splitAddr(addr);
    this.host = host;
    this.port = port;
    this.connectTimeoutMs = options.connectTimeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS;
  }

  /** Returns a `TcpTransport` for `addr` (host:port) that is already connected. */
  static async connect(addr: string, options: TcpTransportOptions = {}): Promise<TcpTransport> {
    const t = new TcpTransport(addr, options);
    await t.ensureConn();
    return t;
  }

  private async ensureConn(): Promise<net.Socket> {
    if (this.closed) throw new TransportError("corndogs: transport closed");
    if (this.socket) return this.socket;
    if (!this.dialing) {
      this.dialing = this.dial().finally(() => {
        this.dialing = null;
      });
    }
    return this.dialing;
  }

  private dial(): Promise<net.Socket> {
    return new Promise((resolve, reject) => {
      const socket = net.createConnection({ host: this.host, port: this.port });
      let settled = false;

      const timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        socket.destroy();
        reject(new TransportError(`corndogs: connect timeout after ${this.connectTimeoutMs}ms`));
      }, this.connectTimeoutMs);

      socket.once("error", (err: Error) => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        reject(new TransportError(err.message));
      });

      socket.once("connect", () => {
        if (settled) return;
        settled = true;
        clearTimeout(timer);
        socket.setNoDelay(true);
        socket.setKeepAlive(true, 15000);
        this.recvBuf = Buffer.alloc(0);
        this.socket = socket;
        socket.on("data", (chunk: Buffer) => this.onData(chunk));
        socket.on("error", (err: Error) => this.teardown(new TransportError(err.message)));
        socket.on("close", () => this.teardown(new TransportError("corndogs: connection closed")));
        resolve(socket);
      });
    });
  }

  /** Closes the current connection (if it is still current) and fails every
   * pending call, so the next call re-dials. */
  private teardown(cause: Error): void {
    if (!this.socket) return;
    const socket = this.socket;
    this.socket = null;
    this.recvBuf = Buffer.alloc(0);
    socket.removeAllListeners();
    socket.destroy();
    const pending = this.pending;
    this.pending = new Map();
    for (const waiter of pending.values()) waiter.reject(cause);
  }

  private onData(chunk: Buffer): void {
    this.recvBuf = this.recvBuf.length ? Buffer.concat([this.recvBuf, chunk]) : chunk;
    for (;;) {
      if (this.recvBuf.length < 4) return;
      const len = this.recvBuf.readUInt32BE(0);
      if (len > MAX_FRAME) {
        this.teardown(new TransportError(`corndogs: frame too large (${len})`));
        return;
      }
      if (this.recvBuf.length < 4 + len) return;
      const frame = this.recvBuf.subarray(4, 4 + len);
      this.recvBuf = this.recvBuf.subarray(4 + len);
      this.onFrame(frame);
    }
  }

  /** Decodes one response envelope and delivers it to its waiting caller by
   * correlation id. A non-zero transport status or a "ServiceError" variant
   * becomes a rejection instead of a resolution. */
  private onFrame(frame: Buffer): void {
    let id = 0;
    let ok: Uint8Array | null = null;
    let err: Error | null = null;
    try {
      const val = decode(frame);
      const idv = mapGet(val, "id");
      if (idv !== undefined) id = asNumber(idv);

      const statusV = mapGet(val, "status");
      const status = statusV !== undefined ? asNumber(statusV) : 0;
      if (status !== 0) {
        const errV = mapGet(val, "error");
        const msg = errV !== undefined ? asString(errV) : "";
        err = new TransportError(`transport status ${status}: ${msg}`);
      } else {
        const payloadV = mapGet(val, "payload");
        if (payloadV === undefined) {
          ok = new Uint8Array(0); // control replies (e.g. $pong) carry no payload
        } else {
          const inner = isCborTag(payloadV) ? payloadV.value : payloadV;
          const bytes = asBytes(inner);
          const variantV = mapGet(val, "variant");
          if (variantV !== undefined && asString(variantV) === "ServiceError") {
            const se = fromServiceErrorCborValue(decode(bytes));
            err = new ServiceError(se.code, se.message);
          } else {
            ok = bytes;
          }
        }
      }
    } catch (e) {
      err = new TransportError(`corndogs: decode response envelope: ${(e as Error).message}`);
    }

    const waiter = this.pending.get(id);
    if (!waiter) return; // unmatched id (e.g. arrived after the caller gave up)
    this.pending.delete(id);
    if (err) waiter.reject(err);
    else waiter.resolve(ok as Uint8Array);
  }

  /** Sends one request and resolves with its correlated response payload. */
  async call(service: string, op: string, req: Uint8Array): Promise<Uint8Array> {
    const socket = await this.ensureConn();

    this.nextId += 1;
    const id = this.nextId;

    const envelope = encodeValue(
      new Map<CborValue, CborValue>([
        ["v", 1],
        ["service", service],
        ["op", op],
        ["id", id],
        ["payload", { tag: TAG_ENCODED_CBOR, value: req } as CborTag],
      ]),
    );
    if (envelope.length > MAX_FRAME) {
      throw new TransportError(`corndogs: frame too large (${envelope.length} > ${MAX_FRAME})`);
    }

    return new Promise<Uint8Array>((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      const frame = Buffer.alloc(4 + envelope.length);
      frame.writeUInt32BE(envelope.length, 0);
      frame.set(envelope, 4);
      socket.write(frame, (writeErr) => {
        if (writeErr) {
          this.pending.delete(id);
          reject(new TransportError(writeErr.message));
        }
      });
    });
  }

  // --- heartbeat -------------------------------------------------------------

  /** Sends a single control-plane heartbeat; rejects if the server is
   * unreachable. Cheap; keeps an idle connection alive and detects a dead
   * server. */
  async ping(): Promise<void> {
    await this.call(CONTROL_SERVICE, OP_PING, new Uint8Array(0));
  }

  /** The SYNCHRONOUS-style heartbeat start: an awaitable loop that pings every
   * `intervalMs` until `signal` is aborted (resolves) or a ping fails
   * (rejects with that error). Await it yourself, or use `startHeartbeat` for
   * the background form. */
  async runHeartbeat(intervalMs: number = DEFAULT_HEARTBEAT_INTERVAL_MS, signal?: AbortSignal): Promise<void> {
    const sig = signal ?? new AbortController().signal;
    while (!sig.aborted) {
      const outcome = await sleepOrAbort(intervalMs, sig);
      if (outcome === "aborted") return;
      await this.ping();
    }
  }

  /** The ASYNCHRONOUS heartbeat start: runs the heartbeat loop in the
   * background and returns a stop function. Calling stop (or `close`) ends
   * it. Only one background heartbeat runs at a time. */
  startHeartbeat(intervalMs: number = DEFAULT_HEARTBEAT_INTERVAL_MS): () => void {
    if (this.hbController) {
      const existing = this.hbController;
      return () => existing.abort();
    }
    const controller = new AbortController();
    this.hbController = controller;
    void this.runHeartbeat(intervalMs, controller.signal)
      .catch(() => {
        /* background heartbeat failures surface on the next `call`, which will
         * re-dial; nothing else to do with an unattended rejection here */
      })
      .finally(() => {
        if (this.hbController === controller) this.hbController = null;
      });
    return () => controller.abort();
  }

  /** Shuts the transport down: stops any heartbeat, closes the connection,
   * and fails every pending call. */
  async close(): Promise<void> {
    this.closed = true;
    if (this.hbController) {
      this.hbController.abort();
      this.hbController = null;
    }
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      await new Promise<void>((resolve) => socket.end(() => resolve()));
      socket.destroy();
    }
    const pending = this.pending;
    this.pending = new Map();
    for (const waiter of pending.values()) waiter.reject(new TransportError("corndogs: transport closed"));
  }
}

/** Convenience: an already-connected `TcpTransport` for `addr` (host:port). */
export function connect(addr: string, options: TcpTransportOptions = {}): Promise<TcpTransport> {
  return TcpTransport.connect(addr, options);
}
