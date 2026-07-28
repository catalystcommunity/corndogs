// Corndogs' own CSIL-RPC transport carrier: a persistent TCP connection framed with
// the canonical 4-byte big-endian length prefix (StreamCarrier). This is the
// ready-to-use carrier — you do not need to write one. Point it at a corndogs
// server's TCP address and pass it to the typed client:
//
//   val tr = TcpTransport("localhost:5080")
//   tr.startHeartbeat()               // keep the connection alive (background thread)
//   val client = CorndogsClient(tr)
//   val resp = client.submitTask(SubmitTaskRequest(queue = "q", currentState = "submitted", ...))
//
// This carrier owns only the CSIL-RPC envelope + framing + connection; the typed
// client owns (de)serialization. It reuses the package's own CBOR codec — no extra
// dependencies. Calls are serialized (one in flight) behind a lock; the connection is
// dialed lazily and re-dialed on failure. Synchronous, matching the generated
// `Transport` seam (no coroutines).
package community.catalyst.csilgen.generated

import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.EOFException
import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.concurrent.thread

private const val TAG_ENCODED_CBOR = 24uL
private const val CONTROL_SERVICE = "CorndogsService"
private const val OP_PING = "\$ping"
private const val MAX_FRAME = 1025 shl 20 // payload maximum plus envelope allowance
private const val DEFAULT_HEARTBEAT_INTERVAL_MILLIS = 15_000L

/** A transport-level failure: connection dropped, or a non-zero transport status. */
class TransportError(message: String, cause: Throwable? = null) : RuntimeException(message, cause)

private fun splitAddr(addr: String): Pair<String, Int> {
    val idx = addr.lastIndexOf(':')
    if (idx <= 0 || idx == addr.length - 1) {
        throw IllegalArgumentException("corndogs: address must be host:port, got '$addr'")
    }
    return addr.substring(0, idx) to addr.substring(idx + 1).toInt()
}

/**
 * Sync CSIL-RPC transport over one persistent TCP connection. Calls are serialized
 * (one in flight); the connection re-dials on failure. Thread-safe. Implements the
 * generated [Transport] seam (`call`).
 */
class TcpTransport(
    addr: String,
    private val connectTimeoutMillis: Int = 5_000,
) : Transport {
    private val host: String
    private val port: Int

    private val lock = Object()
    private var socket: Socket? = null
    private var input: DataInputStream? = null
    private var output: DataOutputStream? = null
    private var nextId: Long = 0
    private var closed = false

    private val hbLock = Object()
    private var hbThread: Thread? = null
    private var hbStopFlag: AtomicBoolean? = null

    init {
        val (h, p) = splitAddr(addr)
        host = h
        port = p
    }

    private fun ensure(): Pair<DataInputStream, DataOutputStream> {
        if (closed) throw TransportError("corndogs: transport closed")
        val s = socket
        if (s != null && !s.isClosed) return input!! to output!!
        val newSocket = Socket()
        newSocket.tcpNoDelay = true
        newSocket.connect(InetSocketAddress(host, port), connectTimeoutMillis)
        val newInput = DataInputStream(newSocket.getInputStream())
        val newOutput = DataOutputStream(newSocket.getOutputStream())
        socket = newSocket
        input = newInput
        output = newOutput
        return newInput to newOutput
    }

    private fun reset() {
        try {
            socket?.close()
        } catch (_: IOException) {
            // already gone
        }
        socket = null
        input = null
        output = null
    }

    /** Performs one CSIL-RPC call: implements the generated [Transport] seam. */
    override fun call(service: String, op: String, request: ByteArray): ByteArray {
        synchronized(lock) {
            val (inp, out) = ensure()
            nextId += 1
            val envelope = CsilCbor.encode(
                CborValue.CMap(
                    listOf(
                        CborValue.CText("v") to CborValue.CUint(1uL),
                        CborValue.CText("service") to CborValue.CText(service),
                        CborValue.CText("op") to CborValue.CText(op),
                        CborValue.CText("id") to CborValue.CUint(nextId.toULong()),
                        CborValue.CText("payload") to CborValue.CTag(TAG_ENCODED_CBOR, CborValue.CBytes(request)),
                    ),
                ),
            )
            try {
                writeFrame(out, envelope)
                val frame = readFrame(inp)
                if (frame == null) {
                    reset()
                    throw TransportError("corndogs: connection closed")
                }
                return parseResponse(frame)
            } catch (e: IOException) {
                reset()
                throw TransportError("corndogs: ${e.message}", e)
            }
        }
    }

    // --- heartbeat: one-shot, sync (blocking), and async (background thread) ---

    /** Sends one control-plane heartbeat (throws if the server is unreachable). */
    fun ping() {
        call(CONTROL_SERVICE, OP_PING, ByteArray(0))
    }

    /**
     * SYNCHRONOUS heartbeat start: blocks, pinging every [intervalMillis], until
     * [stop] is set (or forever, if null). Run it on a thread you own, or use
     * [startHeartbeat] for the background form.
     */
    fun runHeartbeat(intervalMillis: Long = DEFAULT_HEARTBEAT_INTERVAL_MILLIS, stop: AtomicBoolean? = null) {
        while (stop == null || !stop.get()) {
            try {
                Thread.sleep(intervalMillis)
            } catch (e: InterruptedException) {
                Thread.currentThread().interrupt()
                return
            }
            if (stop != null && stop.get()) return
            ping()
        }
    }

    /**
     * ASYNCHRONOUS heartbeat start: runs the heartbeat on a daemon [Thread] and
     * returns a stop handle. Calling it (or [close]) ends the heartbeat. Only one
     * background heartbeat runs at a time.
     */
    fun startHeartbeat(intervalMillis: Long = DEFAULT_HEARTBEAT_INTERVAL_MILLIS): () -> Unit {
        synchronized(hbLock) {
            hbThread?.let { existing ->
                val flag = hbStopFlag!!
                return { flag.set(true); existing.interrupt() }
            }
            val stopFlag = AtomicBoolean(false)
            val t = thread(start = true, isDaemon = true, name = "corndogs-heartbeat") {
                runHeartbeat(intervalMillis, stopFlag)
            }
            hbThread = t
            hbStopFlag = stopFlag
            return {
                stopFlag.set(true)
                t.interrupt()
                synchronized(hbLock) {
                    if (hbThread === t) {
                        hbThread = null
                        hbStopFlag = null
                    }
                }
            }
        }
    }

    /** Shuts the transport down: stops any heartbeat and closes the connection. */
    fun close() {
        synchronized(hbLock) {
            hbThread?.let { t ->
                hbStopFlag?.set(true)
                t.interrupt()
            }
            hbThread = null
            hbStopFlag = null
        }
        synchronized(lock) {
            closed = true
            reset()
        }
    }
}

/** Convenience: a ready [TcpTransport] for `addr` (host:port). */
fun connect(addr: String): TcpTransport = TcpTransport(addr)

// --- envelope + framing helpers ---------------------------------------------

private fun writeFrame(out: DataOutputStream, payload: ByteArray) {
    if (payload.size > MAX_FRAME) {
        throw TransportError("corndogs: frame too large (${payload.size} > $MAX_FRAME)")
    }
    out.writeInt(payload.size) // big-endian per DataOutput
    out.write(payload)
    out.flush()
}

/** Reads one frame, or null on a clean connection close. */
private fun readFrame(input: DataInputStream): ByteArray? {
    val length = try {
        input.readInt() // big-endian per DataInput
    } catch (e: EOFException) {
        return null
    }
    if (length < 0 || length > MAX_FRAME) {
        throw TransportError("corndogs: frame too large ($length)")
    }
    val buf = ByteArray(length)
    input.readFully(buf)
    return buf
}

/**
 * Decodes a CsilRpcResponse frame into the inner payload bytes. A non-zero
 * transport status becomes a [TransportError]; a `"ServiceError"` variant becomes a
 * [ClientError] decoded from the generated [ServiceError] type.
 */
private fun parseResponse(frame: ByteArray): ByteArray {
    val value = CsilCbor.decode(frame)
    val status = CsilCbor.mapGet(value, "status")?.let { CsilCbor.asLong(it) } ?: 0L
    if (status != 0L) {
        val msg = CsilCbor.mapGet(value, "error")?.let { CsilCbor.asText(it) } ?: ""
        throw TransportError("corndogs: transport status $status: $msg")
    }
    var payload = CsilCbor.mapGet(value, "payload") ?: return ByteArray(0) // e.g. $pong carries none
    if (payload is CborValue.CTag) payload = payload.value
    val inner = CsilCbor.asBytes(payload)
    val variant = CsilCbor.mapGet(value, "variant")?.let { CsilCbor.asText(it) }
    if (variant == "ServiceError") {
        val se = serviceErrorFromCborValue(CsilCbor.decode(inner))
        throw ClientError(code = se.code.toLong(), message = se.message)
    }
    return inner
}
