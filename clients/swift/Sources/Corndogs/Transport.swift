// Corndogs' own CSIL-RPC transport carrier: a persistent TCP connection framed with
// the canonical 4-byte big-endian length prefix (the CSIL StreamCarrier). This is the
// ready-to-use carrier — you do not need to write one. Point it at a corndogs server's
// TCP address and hand it to the generated client:
//
//   let tr = TcpTransport.connect("localhost:5080")
//   tr.startHeartbeat()                    // keep the connection alive (background thread)
//   let client = CorndogsClient(transport: tr)
//   let resp = try client.submitTask(SubmitTaskRequest(
//       queue: "q", currentState: "submitted", autoTargetState: "sending",
//       timeout: -1, payload: [], priority: 0))
//
// The wire is CSIL-RPC over a single TCP connection framed with the canonical 4-byte
// big-endian length prefix. This carrier owns only the envelope + framing + connection;
// the generated Client.swift/Codec.swift own (de)serialization. Dependency-free: raw
// POSIX sockets plus the package's own `CsilCbor` codec — no third-party networking or
// CBOR library.
//
// Calls are serialized (one call in flight at a time) behind a lock, matching the
// synchronous `CsilTransport` seam the generated `CorndogsClient` expects. The
// connection is dialed lazily on first use and re-dialed on failure.
//
// Hand-written, not code-generated — safe to edit, unlike the files around it.

#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#endif
import Dispatch
import Foundation

private let tagEncodedCbor: UInt64 = 24  // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
private let maxFrame = 1025 << 20  // payload maximum plus envelope allowance
private let controlService = "CorndogsService"
private let opPing = "$ping"  // control-plane heartbeat op (never collides with app ops)

/// A transport-level failure: a dropped/unreachable connection, a connect timeout, or a
/// non-zero transport status from the server. A typed application-level failure instead
/// throws the generated `CsilClientError` (see `Client.swift`).
public struct TransportError: Error, CustomStringConvertible, LocalizedError {
    public let message: String
    public init(_ message: String) { self.message = message }
    public var description: String { message }
    public var errorDescription: String? { message }
}

private func splitAddr(_ addr: String) throws -> (host: String, port: UInt16) {
    guard let idx = addr.lastIndex(of: ":"), idx > addr.startIndex, addr.index(after: idx) < addr.endIndex
    else {
        throw TransportError("corndogs: address must be host:port, got \(addr)")
    }
    let host = String(addr[addr.startIndex..<idx])
    guard let port = UInt16(addr[addr.index(after: idx)...]) else {
        throw TransportError("corndogs: invalid port in address \(addr)")
    }
    return (host, port)
}

/// Sync CSIL-RPC transport over one persistent TCP connection. Calls are serialized
/// (one in flight); the connection re-dials on failure. Safe for concurrent use.
/// Implements the generated `CsilTransport` seam (`call`).
public final class TcpTransport: CsilTransport {
    private let addr: String
    private let connectTimeout: TimeInterval

    private let lock = NSLock()  // guards fd, nextId, closed
    private var fd: Int32?
    private var nextId: UInt64 = 0
    private var closed = false

    private let hbLock = NSLock()  // guards hbStop
    private var hbStop: DispatchSemaphore?

    /// - Parameters:
    ///   - addr: corndogs server address as "host:port".
    ///   - connectTimeout: dial timeout; defaults to 5 seconds.
    public init(_ addr: String, connectTimeout: TimeInterval = 5.0) {
        self.addr = addr
        self.connectTimeout = connectTimeout
    }

    /// Convenience: a ready `TcpTransport` for `addr` (host:port). Dialing itself is
    /// still lazy — this just constructs the transport.
    public static func connect(_ addr: String) -> TcpTransport {
        TcpTransport(addr)
    }

    // --- connection lifecycle (all called with `lock` held) --------------------

    private func ensureLocked() throws -> Int32 {
        if closed { throw TransportError("corndogs: transport closed") }
        if let fd { return fd }
        let newFd = try dial()
        fd = newFd
        return newFd
    }

    private func resetLocked() {
        if let fd { _ = close(fd) }
        fd = nil
    }

    /// Resolves `addr` and connects to the first reachable result, bounded by
    /// `connectTimeout`.
    private func dial() throws -> Int32 {
        let (host, port) = try splitAddr(addr)

        var hints = addrinfo()
        hints.ai_family = AF_UNSPEC
        hints.ai_socktype = SOCK_STREAM
        hints.ai_protocol = Int32(IPPROTO_TCP)

        var resultPtr: UnsafeMutablePointer<addrinfo>?
        let gaiRc = getaddrinfo(host, String(port), &hints, &resultPtr)
        guard gaiRc == 0, let firstAddr = resultPtr else {
            let reason = gaiRc != 0 ? String(cString: gai_strerror(gaiRc)) : "no address"
            throw TransportError("corndogs: resolve \(host):\(port) failed: \(reason)")
        }
        defer { freeaddrinfo(resultPtr) }

        var lastError = "no address"
        var cursor: UnsafeMutablePointer<addrinfo>? = firstAddr
        while let info = cursor {
            let candidate = socket(info.pointee.ai_family, info.pointee.ai_socktype, info.pointee.ai_protocol)
            if candidate < 0 {
                lastError = String(cString: strerror(errno))
                cursor = info.pointee.ai_next
                continue
            }
            do {
                try connectWithTimeout(candidate, info.pointee.ai_addr, info.pointee.ai_addrlen)
                configure(candidate)
                return candidate
            } catch {
                _ = close(candidate)
                lastError = "\(error)"
                cursor = info.pointee.ai_next
            }
        }
        throw TransportError("corndogs: connect \(host):\(port) failed: \(lastError)")
    }

    /// Connects `fd`, bounded by `connectTimeout`: a non-blocking `connect()` followed
    /// by `poll()` for writability, the standard POSIX pattern for a bounded connect
    /// (plain blocking `connect()` has no portable per-call timeout).
    private func connectWithTimeout(_ fd: Int32, _ rawAddr: UnsafeMutablePointer<sockaddr>?, _ addrLen: socklen_t)
        throws
    {
        guard let rawAddr else { throw TransportError("corndogs: resolved address is empty") }

        let flags = fcntl(fd, F_GETFL, 0)
        _ = fcntl(fd, F_SETFL, flags | O_NONBLOCK)

        let rc = connect(fd, rawAddr, addrLen)
        if rc == 0 {
            _ = fcntl(fd, F_SETFL, flags)  // restore blocking mode
            return
        }
        guard errno == EINPROGRESS else {
            throw TransportError("corndogs: connect failed: \(String(cString: strerror(errno)))")
        }

        var pfd = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
        let timeoutMillis = Int32(max(connectTimeout, 0) * 1000)
        let pollRc = poll(&pfd, 1, timeoutMillis)
        if pollRc == 0 {
            throw TransportError("corndogs: connect timed out after \(connectTimeout)s")
        }
        if pollRc < 0 {
            throw TransportError("corndogs: poll failed: \(String(cString: strerror(errno)))")
        }

        var soError: Int32 = 0
        var soErrorLen = socklen_t(MemoryLayout<Int32>.size)
        getsockopt(fd, SOL_SOCKET, SO_ERROR, &soError, &soErrorLen)
        if soError != 0 {
            throw TransportError("corndogs: connect failed: \(String(cString: strerror(soError)))")
        }
        _ = fcntl(fd, F_SETFL, flags)  // restore blocking mode
    }

    private func configure(_ fd: Int32) {
        var one: Int32 = 1
        _ = setsockopt(fd, Int32(IPPROTO_TCP), TCP_NODELAY, &one, socklen_t(MemoryLayout<Int32>.size))
        _ = setsockopt(fd, SOL_SOCKET, SO_KEEPALIVE, &one, socklen_t(MemoryLayout<Int32>.size))
    }

    // --- CsilTransport ------------------------------------------------------------

    /// Sends one request and waits for its response. Implements `CsilTransport.call`
    /// for `CorndogsClient`.
    public func call(service: String, op: String, request: [UInt8]) throws -> [UInt8] {
        lock.lock()
        defer { lock.unlock() }

        let sockFd = try ensureLocked()
        nextId += 1
        let id = nextId

        let envelope = CsilCbor.encode(
            .map([
                (.text("v"), .uint(1)),
                (.text("service"), .text(service)),
                (.text("op"), .text(op)),
                (.text("id"), .uint(id)),
                (.text("payload"), .tag(tagEncodedCbor, .bytes(request))),
            ]))

        do {
            try writeFrame(sockFd, envelope)
        } catch {
            resetLocked()
            throw error
        }

        let frame: [UInt8]
        do {
            guard let received = try readFrame(sockFd) else {
                throw TransportError("corndogs: connection closed")
            }
            frame = received
        } catch {
            resetLocked()
            throw error
        }

        return try parseResponse(frame)
    }

    // --- heartbeat: one-shot, sync (blocking), and async (background thread) ------

    /// Sends a single control-plane heartbeat; throws if the server is unreachable.
    /// Cheap; keeps an idle connection alive and detects a dead server.
    public func ping() throws {
        _ = try call(service: controlService, op: opPing, request: [])
    }

    /// SYNCHRONOUS heartbeat start: blocks, pinging every `interval`, until `stop` is
    /// signaled (returns) or a ping fails (throws). Run it on a thread you own, or use
    /// `startHeartbeat` for the background form.
    public func runHeartbeat(interval: TimeInterval = 15.0, stop: DispatchSemaphore? = nil) throws {
        while true {
            if let stop {
                if stop.wait(timeout: .now() + interval) == .success { return }
            } else {
                Thread.sleep(forTimeInterval: interval)
            }
            try ping()
        }
    }

    /// ASYNCHRONOUS heartbeat start: runs the heartbeat on a background thread and
    /// returns a stop function. Calling it (or `close`) ends the heartbeat. Only one
    /// background heartbeat runs at a time.
    @discardableResult
    public func startHeartbeat(interval: TimeInterval = 15.0) -> () -> Void {
        hbLock.lock()
        if let existing = hbStop {
            hbLock.unlock()
            return { existing.signal() }
        }
        let stop = DispatchSemaphore(value: 0)
        hbStop = stop
        hbLock.unlock()

        let thread = Thread { [weak self] in
            try? self?.runHeartbeat(interval: interval, stop: stop)
        }
        thread.name = "corndogs-heartbeat"
        thread.start()

        return { [weak self] in
            stop.signal()
            guard let self else { return }
            self.hbLock.lock()
            if self.hbStop === stop { self.hbStop = nil }
            self.hbLock.unlock()
        }
    }

    /// Shuts the transport down: stops any heartbeat and closes the connection.
    public func close() {
        hbLock.lock()
        let stop = hbStop
        hbStop = nil
        hbLock.unlock()
        stop?.signal()

        lock.lock()
        closed = true
        resetLocked()
        lock.unlock()
    }

    deinit {
        close()
    }
}

// --- envelope + framing helpers -------------------------------------------------

private func writeFrame(_ fd: Int32, _ payload: [UInt8]) throws {
    if payload.count > maxFrame {
        throw TransportError("corndogs: frame too large (\(payload.count) > \(maxFrame))")
    }
    let len = UInt32(payload.count)
    var framed = [UInt8](repeating: 0, count: 4 + payload.count)
    framed[0] = UInt8((len >> 24) & 0xff)
    framed[1] = UInt8((len >> 16) & 0xff)
    framed[2] = UInt8((len >> 8) & 0xff)
    framed[3] = UInt8(len & 0xff)
    framed.replaceSubrange(4..<framed.count, with: payload)
    try writeAll(fd, framed)
}

private func writeAll(_ fd: Int32, _ bytes: [UInt8]) throws {
    var offset = 0
    try bytes.withUnsafeBufferPointer { buf in
        guard let base = buf.baseAddress else { return }
        while offset < bytes.count {
            let n = send(fd, base + offset, bytes.count - offset, 0)
            if n < 0 {
                if errno == EINTR { continue }
                throw TransportError("corndogs: write failed: \(String(cString: strerror(errno)))")
            }
            if n == 0 {
                throw TransportError("corndogs: connection closed during write")
            }
            offset += n
        }
    }
}

/// Reads exactly `count` bytes, or nil if the peer closed the connection before any
/// bytes arrived (a clean close at a frame boundary).
private func readExact(_ fd: Int32, _ count: Int) throws -> [UInt8]? {
    if count == 0 { return [] }
    var buf = [UInt8](repeating: 0, count: count)
    var offset = 0
    try buf.withUnsafeMutableBufferPointer { mbuf in
        guard let base = mbuf.baseAddress else { return }
        while offset < count {
            let n = recv(fd, base + offset, count - offset, 0)
            if n < 0 {
                if errno == EINTR { continue }
                throw TransportError("corndogs: read failed: \(String(cString: strerror(errno)))")
            }
            if n == 0 { return }  // peer closed
            offset += n
        }
    }
    if offset == 0 { return nil }
    if offset < count { throw TransportError("corndogs: connection closed mid-frame") }
    return buf
}

/// Reads one length-prefixed frame; nil on a clean close at a frame boundary.
private func readFrame(_ fd: Int32) throws -> [UInt8]? {
    guard let head = try readExact(fd, 4) else { return nil }
    let length = (UInt32(head[0]) << 24) | (UInt32(head[1]) << 16) | (UInt32(head[2]) << 8) | UInt32(head[3])
    if length > UInt32(maxFrame) {
        throw TransportError("corndogs: frame too large (\(length))")
    }
    guard let body = try readExact(fd, Int(length)) else {
        throw TransportError("corndogs: connection closed mid-frame")
    }
    return body
}

/// Decodes a response envelope into the inner payload bytes. A non-zero transport
/// status throws `TransportError`; a `"ServiceError"` variant throws the generated
/// `CsilClientError`. Empty (not a nil/absent case) for control replies (e.g. "$pong")
/// that carry no payload.
private func parseResponse(_ frame: [UInt8]) throws -> [UInt8] {
    let val: CsilCborValue
    do {
        val = try CsilCbor.decode(frame)
    } catch {
        throw TransportError("corndogs: decode response envelope: \(error)")
    }

    if let statusV = CsilCbor.mapGet(val, "status"), let status = try? CsilCbor.asI64(statusV), status != 0 {
        var message = ""
        if let errV = CsilCbor.mapGet(val, "error") {
            message = (try? CsilCbor.asText(errV)) ?? ""
        }
        throw TransportError("corndogs: transport status \(status): \(message)")
    }

    guard let payloadV = CsilCbor.mapGet(val, "payload") else {
        return []  // control replies (e.g. $pong) carry no payload
    }
    let inner: CsilCborValue
    if case .tag(_, let tagged) = payloadV {
        inner = tagged
    } else {
        inner = payloadV
    }
    let bytes: [UInt8]
    do {
        bytes = try CsilCbor.asBytes(inner)
    } catch {
        throw TransportError("corndogs: response payload is not tag-24 bytes: \(error)")
    }

    if let variantV = CsilCbor.mapGet(val, "variant"),
        let variant = try? CsilCbor.asText(variantV),
        variant == "ServiceError"
    {
        let se = try ServiceError.fromCbor(bytes)
        throw CsilClientError(code: Int64(se.code), message: se.message)
    }
    return bytes
}
