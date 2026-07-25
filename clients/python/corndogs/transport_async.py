"""Corndogs async client transport: CSIL-RPC over a persistent asyncio TCP
connection, with real correlation-id multiplexing (many concurrent ``await`` calls
share one connection without head-of-line blocking).

    import asyncio
    from corndogs import CorndogsAsyncClient, SubmitTaskRequest
    from corndogs.transport_async import AsyncTcpTransport

    async def main():
        tr = await AsyncTcpTransport.connect("localhost:5080")
        tr.start_heartbeat()                 # background asyncio task
        client = CorndogsAsyncClient(tr)
        await client.submit_task(SubmitTaskRequest(queue="q", current_state="submitted"))

    asyncio.run(main())

Dependency-free (uses the package's own CBOR codec).
"""

from __future__ import annotations

import asyncio
import struct
from typing import Callable, Dict, Optional

from .codec import CborTag, cbor_decode, cbor_encode
from .client import ServiceError
from .transport import (
    TransportError,
    _CONTROL_SERVICE,
    _MAX_FRAME,
    _OP_PING,
    _TAG_ENCODED_CBOR,
    _parse_response,
    _split_addr,
)


class AsyncTcpTransport:
    """Async CSIL-RPC transport. Build with ``await AsyncTcpTransport.connect(addr)``.
    Implements the generated ``AsyncTransport`` protocol (``async call``)."""

    def __init__(self, host: str, port: int) -> None:
        self._host = host
        self._port = port
        self._reader: Optional[asyncio.StreamReader] = None
        self._writer: Optional[asyncio.StreamWriter] = None
        self._pending: Dict[int, asyncio.Future] = {}
        self._next_id = 0
        self._read_task: Optional[asyncio.Task] = None
        self._hb_task: Optional[asyncio.Task] = None
        self._lock = asyncio.Lock()

    @classmethod
    async def connect(cls, addr: str) -> "AsyncTcpTransport":
        host, port = _split_addr(addr)
        t = cls(host, port)
        await t._ensure()
        return t

    async def _ensure(self) -> None:
        async with self._lock:
            if self._writer is not None:
                return
            self._reader, self._writer = await asyncio.open_connection(self._host, self._port)
            self._read_task = asyncio.ensure_future(self._read_loop(self._reader))

    async def _read_loop(self, reader: asyncio.StreamReader) -> None:
        try:
            while True:
                head = await reader.readexactly(4)
                (length,) = struct.unpack(">I", head)
                if length > _MAX_FRAME:
                    raise TransportError(f"frame too large ({length})")
                frame = await reader.readexactly(length)
                val = cbor_decode(frame)
                mid = int(val.get("id", 0)) if isinstance(val, dict) else 0
                fut = self._pending.pop(mid, None)
                if fut is not None and not fut.done():
                    fut.set_result(frame)
        except (asyncio.IncompleteReadError, OSError, TransportError) as exc:
            await self._teardown(exc)

    async def _teardown(self, cause: Exception) -> None:
        pending, self._pending = self._pending, {}
        self._writer = None
        for fut in pending.values():
            if not fut.done():
                fut.set_exception(TransportError(f"connection lost: {cause}"))

    async def call(self, service: str, method: str, req: bytes) -> bytes:
        await self._ensure()
        self._next_id += 1
        mid = self._next_id
        fut: asyncio.Future = asyncio.get_event_loop().create_future()
        self._pending[mid] = fut
        env = cbor_encode(
            {
                "v": 1,
                "service": service,
                "op": method,
                "id": mid,
                "payload": CborTag(_TAG_ENCODED_CBOR, req),
            }
        )
        try:
            self._writer.write(struct.pack(">I", len(env)) + env)
            await self._writer.drain()
        except OSError as exc:
            self._pending.pop(mid, None)
            raise TransportError(str(exc)) from None
        frame = await fut
        return _parse_response(frame)

    # --- heartbeat: sync-style (awaitable loop) and async (background task) ---

    async def ping(self) -> None:
        await self.call(_CONTROL_SERVICE, _OP_PING, b"")

    async def run_heartbeat(self, interval: float = 15.0) -> None:
        """The awaitable heartbeat loop: pings every ``interval`` until cancelled."""
        while True:
            await asyncio.sleep(interval)
            await self.ping()

    def start_heartbeat(self, interval: float = 15.0) -> Callable[[], None]:
        """Start the heartbeat as a background asyncio task; returns a stop func."""
        if self._hb_task is not None:
            return self._stop_hb
        self._hb_task = asyncio.ensure_future(self.run_heartbeat(interval))
        return self._stop_hb

    def _stop_hb(self) -> None:
        if self._hb_task is not None:
            self._hb_task.cancel()
            self._hb_task = None

    async def close(self) -> None:
        self._stop_hb()
        if self._read_task is not None:
            self._read_task.cancel()
        if self._writer is not None:
            self._writer.close()
            self._writer = None


async def connect(addr: str) -> AsyncTcpTransport:
    """Convenience: an already-connected AsyncTcpTransport for ``addr``."""
    return await AsyncTcpTransport.connect(addr)
