"""Corndogs sync client transport: CSIL-RPC over a persistent TCP connection.

This is Corndogs' own, ready-to-use carrier — you do not need to write one or read
csilgen docs. Point it at a corndogs server's TCP address and pass it to the
generated client:

    from corndogs import CorndogsClient, SubmitTaskRequest
    from corndogs.transport import TcpTransport

    tr = TcpTransport("localhost:5080")
    tr.start_heartbeat()                    # keep the connection alive (background)
    client = CorndogsClient(tr)
    resp = client.submit_task(SubmitTaskRequest(queue="q", current_state="submitted"))

The wire is CSIL-RPC framed with the canonical 4-byte big-endian length prefix over
TCP. This carrier owns only the envelope + framing + connection; the generated
client owns (de)serialization. Dependency-free (uses the package's own CBOR codec).
"""

from __future__ import annotations

import socket
import struct
import threading
import time
from typing import Callable, Optional

from .codec import CborTag, cbor_decode, cbor_encode
from .client import ServiceError

_TAG_ENCODED_CBOR = 24
_CONTROL_SERVICE = "CorndogsService"
_OP_PING = "$ping"
_MAX_FRAME = 1025 << 20  # payload hard maximum plus RPC envelope allowance


class TransportError(Exception):
    """A transport-level failure (connection dropped, non-zero transport status)."""


def _split_addr(addr: str) -> tuple[str, int]:
    host, _, port = addr.rpartition(":")
    if not host:
        raise ValueError(f"corndogs: address must be host:port, got {addr!r}")
    return host, int(port)


def _write_frame(sock: socket.socket, payload: bytes) -> None:
    if len(payload) > _MAX_FRAME:
        raise TransportError(f"frame too large ({len(payload)})")
    sock.sendall(struct.pack(">I", len(payload)) + payload)


def _read_exact(sock: socket.socket, n: int) -> Optional[bytes]:
    buf = bytearray()
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            return None
        buf.extend(chunk)
    return bytes(buf)


def _read_frame(sock: socket.socket) -> Optional[bytes]:
    head = _read_exact(sock, 4)
    if head is None:
        return None
    (length,) = struct.unpack(">I", head)
    if length > _MAX_FRAME:
        raise TransportError(f"frame too large ({length})")
    return _read_exact(sock, length)


def _parse_response(frame: bytes) -> bytes:
    val = cbor_decode(frame)
    if not isinstance(val, dict):
        raise TransportError("malformed response envelope")
    status = val.get("status", 0) or 0
    if status != 0:
        raise TransportError(f"transport status {status}: {val.get('error', '')}")
    payload = val.get("payload")
    if payload is None:
        return b""  # control replies (e.g. $pong) carry no payload
    if isinstance(payload, CborTag):
        payload = payload.value
    if val.get("variant") == "ServiceError":
        inner = cbor_decode(payload)
        if isinstance(inner, dict):
            raise ServiceError(int(inner.get("code", 0)), str(inner.get("message", "")))
        raise ServiceError(0, "service error")
    return payload


class TcpTransport:
    """Sync CSIL-RPC transport over one persistent TCP connection. Calls are
    serialized (one in flight); the connection re-dials on failure. Thread-safe.
    Implements the generated ``Transport`` protocol (``call``)."""

    def __init__(self, addr: str, connect_timeout: float = 5.0) -> None:
        self._host, self._port = _split_addr(addr)
        self._timeout = connect_timeout
        self._lock = threading.Lock()
        self._sock: Optional[socket.socket] = None
        self._next_id = 0
        self._hb_stop: Optional[threading.Event] = None

    def _ensure(self) -> socket.socket:
        if self._sock is None:
            s = socket.create_connection((self._host, self._port), self._timeout)
            s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
            self._sock = s
        return self._sock

    def _reset(self) -> None:
        if self._sock is not None:
            try:
                self._sock.close()
            except OSError:
                pass
            self._sock = None

    def call(self, service: str, method: str, req: bytes) -> bytes:
        with self._lock:
            sock = self._ensure()
            self._next_id += 1
            env = cbor_encode(
                {
                    "v": 1,
                    "service": service,
                    "op": method,
                    "id": self._next_id,
                    "payload": CborTag(_TAG_ENCODED_CBOR, req),
                }
            )
            try:
                _write_frame(sock, env)
                resp = _read_frame(sock)
            except OSError as exc:
                self._reset()
                raise TransportError(str(exc)) from None
            if resp is None:
                self._reset()
                raise TransportError("connection closed")
            return _parse_response(resp)

    # --- heartbeat: sync and async (background thread) start functions ---

    def ping(self) -> None:
        """Send one control-plane heartbeat (raises on a dead server)."""
        self.call(_CONTROL_SERVICE, _OP_PING, b"")

    def run_heartbeat(self, interval: float = 15.0, stop: Optional[threading.Event] = None) -> None:
        """SYNCHRONOUS heartbeat start: blocks, pinging every ``interval`` seconds,
        until ``stop`` is set (or forever). Run it in a thread you own, or use
        ``start_heartbeat`` for the background form."""
        while True:
            if stop is not None:
                if stop.wait(interval):
                    return
            else:
                time.sleep(interval)
            self.ping()

    def start_heartbeat(self, interval: float = 15.0) -> Callable[[], None]:
        """ASYNCHRONOUS heartbeat start: runs the heartbeat on a daemon thread and
        returns a stop function."""
        if self._hb_stop is not None:
            existing = self._hb_stop
            return existing.set
        stop = threading.Event()
        self._hb_stop = stop
        threading.Thread(target=self.run_heartbeat, args=(interval, stop), daemon=True).start()

        def stopper() -> None:
            stop.set()
            self._hb_stop = None

        return stopper

    def close(self) -> None:
        if self._hb_stop is not None:
            self._hb_stop.set()
            self._hb_stop = None
        with self._lock:
            self._reset()


def connect(addr: str) -> TcpTransport:
    """Convenience: a ready TcpTransport for ``addr`` (host:port)."""
    return TcpTransport(addr)
