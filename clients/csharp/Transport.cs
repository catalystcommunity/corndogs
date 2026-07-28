// CSIL-RPC transport carrier for the corndogs C# library, over a persistent TCP
// connection (the canonical corndogs client transport):
//
//     var client = Corndogs.Connect("localhost:5080");
//     var resp = client.SubmitTask(new SubmitTaskRequest { Queue = "q", ... });
//
// The wire is CSIL-RPC over a single TCP connection framed with the canonical 4-byte
// big-endian length prefix. This carrier (TcpTransport) is single-in-flight — one call
// at a time, guarded by a lock; see TransportAsync.cs for the correlation-id
// multiplexed async carrier used by CorndogsAsyncClient. The connection is dialed
// lazily and re-dialed on failure.
//
// This file owns only the CSIL-RPC envelope + framing + connection; the generated
// codec (Codec.gen.cs) owns (de)serialization. No third-party dependencies — this is
// Corndogs' own ready-to-use carrier, so you never need to write one yourself.
#nullable enable

using System;
using System.Buffers.Binary;
using System.IO;
using System.Net.Sockets;
using System.Threading;
// NOTE: no `using System.Threading.Tasks;` — this namespace (corndogs) also declares a
// generated record type named `Task` (Types.gen.cs), which would shadow
// System.Threading.Tasks.Task. Task-returning members below are fully qualified.

namespace corndogs;

/// <summary>Raised on a transport-level failure: a dropped/unreachable connection, a
/// connect timeout, or a non-zero transport status from the server. A typed
/// application-level failure instead raises the generated
/// <see cref="CsilClientException"/> (see <c>Client.gen.cs</c>).</summary>
public sealed class CorndogsTransportException : Exception
{
    public CorndogsTransportException(string message) : base(message) { }
    public CorndogsTransportException(string message, Exception inner) : base(message, inner) { }
}

/// <summary>Convenience entry points: a ready, typed client wired to Corndogs' own TCP
/// transport. Equivalent to constructing the transport and client yourself, for the
/// common case.</summary>
public static class Corndogs
{
    /// <summary>A <see cref="CorndogsClient"/> wired to a <see cref="TcpTransport"/> at
    /// <paramref name="addr"/> (host:port).</summary>
    public static CorndogsClient Connect(string addr) => new(new TcpTransport(addr));

    /// <summary>A <see cref="CorndogsAsyncClient"/> wired to an
    /// <see cref="AsyncTcpTransport"/> at <paramref name="addr"/> (host:port).</summary>
    public static async System.Threading.Tasks.Task<CorndogsAsyncClient> ConnectAsync(string addr) =>
        new(await AsyncTcpTransport.ConnectAsync(addr).ConfigureAwait(false));
}

/// <summary>Shared CSIL-RPC envelope + framing helpers used by both the sync
/// (<see cref="TcpTransport"/>) and async (<see cref="AsyncTcpTransport"/>) carriers.</summary>
internal static class CsilRpcWire
{
    internal const ulong TagEncodedCbor = 24; // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
    internal const int MaxFrame = 1025 << 20; // payload maximum plus envelope allowance
    internal const string ControlService = "CorndogsService";
    internal const string OpPing = "$ping"; // control-plane heartbeat op (never collides with app ops)

    internal static (string Host, int Port) SplitAddr(string addr)
    {
        int idx = addr.LastIndexOf(':');
        if (idx < 0 || idx == addr.Length - 1)
            throw new ArgumentException($"corndogs: address must be host:port, got '{addr}'");
        return (addr[..idx], int.Parse(addr[(idx + 1)..]));
    }

    internal static byte[] BuildEnvelope(string service, string op, ulong id, byte[] request)
    {
        var entries = new (CborValue Key, CborValue Value)[]
        {
            (new CborValue.Text("v"), new CborValue.Uint(1)),
            (new CborValue.Text("service"), new CborValue.Text(service)),
            (new CborValue.Text("op"), new CborValue.Text(op)),
            (new CborValue.Text("id"), new CborValue.Uint(id)),
            (new CborValue.Text("payload"), new CborValue.Tag(TagEncodedCbor, new CborValue.Bytes(request))),
        };
        return Cbor.Encode(new CborValue.Map(entries));
    }

    internal static ulong ResponseId(CborValue val)
    {
        var idv = Cbor.MapGet(val, "id");
        return idv is not null ? Cbor.AsU64(idv) : 0;
    }

    /// <summary>Decodes a response envelope into the inner payload bytes. A non-zero
    /// transport status throws <see cref="CorndogsTransportException"/>; a
    /// "ServiceError" variant throws the generated <see cref="CsilClientException"/>.
    /// Empty (not null) for control replies (e.g. "$pong") that carry no payload.</summary>
    internal static byte[] ParseResponse(byte[] frame)
    {
        CborValue val;
        try
        {
            val = Cbor.Decode(frame);
        }
        catch (CborException ex)
        {
            throw new CorndogsTransportException($"corndogs: decode response envelope: {ex.Message}", ex);
        }

        var statusV = Cbor.MapGet(val, "status");
        if (statusV is not null && Cbor.AsI64(statusV) != 0)
        {
            var errV = Cbor.MapGet(val, "error");
            var msg = errV is not null ? Cbor.AsText(errV) : "";
            throw new CorndogsTransportException($"corndogs: transport status {Cbor.AsI64(statusV)}: {msg}");
        }

        var payloadV = Cbor.MapGet(val, "payload");
        if (payloadV is null)
            return Array.Empty<byte>(); // control replies (e.g. $pong) carry no payload

        var inner = payloadV is CborValue.Tag tag ? tag.Inner : payloadV;
        byte[] innerBytes;
        try
        {
            innerBytes = Cbor.AsBytes(inner);
        }
        catch (CborException ex)
        {
            throw new CorndogsTransportException($"corndogs: response payload is not tag-24 bytes: {ex.Message}", ex);
        }

        var variantV = Cbor.MapGet(val, "variant");
        if (variantV is not null && Cbor.AsText(variantV) == "ServiceError")
        {
            var se = Codec.Decode<ServiceError>(innerBytes);
            throw new CsilClientException((long)se.Code, se.Message);
        }
        return innerBytes;
    }

    internal static void WriteFrame(Stream s, byte[] payload)
    {
        if (payload.Length > MaxFrame)
            throw new CorndogsTransportException($"corndogs: frame too large ({payload.Length})");
        Span<byte> prefix = stackalloc byte[4];
        BinaryPrimitives.WriteUInt32BigEndian(prefix, (uint)payload.Length);
        s.Write(prefix);
        s.Write(payload, 0, payload.Length);
        s.Flush();
    }

    /// <summary>Reads one length-prefixed frame; null on a clean close at a frame
    /// boundary.</summary>
    internal static byte[]? ReadFrame(Stream s)
    {
        var lenBuf = new byte[4];
        int n = ReadFull(s, lenBuf, 4);
        if (n == 0) return null;
        if (n < 4) throw new CorndogsTransportException("corndogs: connection closed mid-frame");
        uint len = BinaryPrimitives.ReadUInt32BigEndian(lenBuf);
        if (len > MaxFrame) throw new CorndogsTransportException($"corndogs: frame too large ({len})");
        var buf = new byte[len];
        int got = ReadFull(s, buf, (int)len);
        if (got < len) throw new CorndogsTransportException("corndogs: connection closed mid-frame");
        return buf;
    }

    static int ReadFull(Stream s, byte[] buf, int count)
    {
        int total = 0;
        while (total < count)
        {
            int r = s.Read(buf, total, count - total);
            if (r == 0) break;
            total += r;
        }
        return total;
    }

    internal static async System.Threading.Tasks.Task WriteFrameAsync(Stream s, byte[] payload, CancellationToken ct = default)
    {
        if (payload.Length > MaxFrame)
            throw new CorndogsTransportException($"corndogs: frame too large ({payload.Length})");
        var prefix = new byte[4];
        BinaryPrimitives.WriteUInt32BigEndian(prefix, (uint)payload.Length);
        await s.WriteAsync(prefix, ct).ConfigureAwait(false);
        await s.WriteAsync(payload, ct).ConfigureAwait(false);
        await s.FlushAsync(ct).ConfigureAwait(false);
    }

    internal static async System.Threading.Tasks.Task<byte[]?> ReadFrameAsync(Stream s, CancellationToken ct = default)
    {
        var lenBuf = new byte[4];
        int n = await ReadFullAsync(s, lenBuf, 4, ct).ConfigureAwait(false);
        if (n == 0) return null;
        if (n < 4) throw new CorndogsTransportException("corndogs: connection closed mid-frame");
        uint len = BinaryPrimitives.ReadUInt32BigEndian(lenBuf);
        if (len > MaxFrame) throw new CorndogsTransportException($"corndogs: frame too large ({len})");
        var buf = new byte[len];
        int got = await ReadFullAsync(s, buf, (int)len, ct).ConfigureAwait(false);
        if (got < len) throw new CorndogsTransportException("corndogs: connection closed mid-frame");
        return buf;
    }

    static async System.Threading.Tasks.Task<int> ReadFullAsync(Stream s, byte[] buf, int count, CancellationToken ct)
    {
        int total = 0;
        while (total < count)
        {
            int r = await s.ReadAsync(buf.AsMemory(total, count - total), ct).ConfigureAwait(false);
            if (r == 0) break;
            total += r;
        }
        return total;
    }
}

/// <summary>A one-shot stop handle returned by <c>StartHeartbeat</c>. Dispose (once) to
/// stop the background heartbeat.</summary>
internal sealed class CancelHandle(Action onDispose) : IDisposable
{
    int _disposed;
    public void Dispose()
    {
        if (Interlocked.Exchange(ref _disposed, 1) == 0) onDispose();
    }
}

/// <summary>Sync CSIL-RPC transport over one persistent TCP connection. Calls are
/// serialized (one in flight); the connection re-dials on failure. Thread-safe.
/// Implements the generated <see cref="ICsilTransport"/> seam.</summary>
public sealed class TcpTransport : ICsilTransport, IDisposable
{
    readonly string _host;
    readonly int _port;
    readonly TimeSpan _connectTimeout;
    readonly object _lock = new();
    TcpClient? _client;
    NetworkStream? _stream;
    ulong _nextId;
    CancellationTokenSource? _hbCts;
    bool _closed;

    /// <param name="addr">Corndogs server address as "host:port".</param>
    /// <param name="connectTimeout">Dial timeout; defaults to 5 seconds.</param>
    public TcpTransport(string addr, TimeSpan? connectTimeout = null)
    {
        (_host, _port) = CsilRpcWire.SplitAddr(addr);
        _connectTimeout = connectTimeout ?? TimeSpan.FromSeconds(5);
    }

    NetworkStream Ensure()
    {
        if (_closed) throw new CorndogsTransportException("corndogs: transport closed");
        if (_stream is not null) return _stream;
        var client = new TcpClient();
        try
        {
            var connectTask = client.ConnectAsync(_host, _port);
            if (!connectTask.Wait(_connectTimeout))
            {
                client.Dispose();
                throw new CorndogsTransportException($"corndogs: connect timeout dialing {_host}:{_port}");
            }
        }
        catch (AggregateException aex)
        {
            client.Dispose();
            var inner = aex.InnerException ?? aex;
            throw new CorndogsTransportException($"corndogs: {inner.Message}", inner);
        }
        client.NoDelay = true;
        _client = client;
        _stream = client.GetStream();
        return _stream;
    }

    void Reset()
    {
        _stream?.Dispose();
        _client?.Dispose();
        _stream = null;
        _client = null;
    }

    /// <summary>Sends one request and waits for its response. Implements
    /// <see cref="ICsilTransport.Call"/> for <see cref="CorndogsClient"/>.</summary>
    public byte[] Call(string service, string op, byte[] request)
    {
        lock (_lock)
        {
            var stream = Ensure();
            var id = ++_nextId;
            var env = CsilRpcWire.BuildEnvelope(service, op, id, request);
            try
            {
                CsilRpcWire.WriteFrame(stream, env);
                var resp = CsilRpcWire.ReadFrame(stream);
                if (resp is null)
                {
                    Reset();
                    throw new CorndogsTransportException("corndogs: connection closed");
                }
                return CsilRpcWire.ParseResponse(resp);
            }
            catch (IOException ex)
            {
                Reset();
                throw new CorndogsTransportException($"corndogs: {ex.Message}", ex);
            }
            catch (SocketException ex)
            {
                Reset();
                throw new CorndogsTransportException($"corndogs: {ex.Message}", ex);
            }
        }
    }

    // --- heartbeat: sync (blocking) and async (background thread) start ------

    /// <summary>Sends a single control-plane heartbeat; throws if the server is
    /// unreachable. Cheap; keeps an idle connection alive and detects a dead
    /// server.</summary>
    public void Ping() => Call(CsilRpcWire.ControlService, CsilRpcWire.OpPing, Array.Empty<byte>());

    /// <summary>SYNCHRONOUS heartbeat start: blocks, pinging every
    /// <paramref name="interval"/>, until <paramref name="cancellationToken"/> is
    /// cancelled (returns) or a ping fails (throws). Run it on a thread you own, or use
    /// <see cref="StartHeartbeat"/> for the background form.</summary>
    public void RunHeartbeat(TimeSpan interval, CancellationToken cancellationToken = default)
    {
        while (true)
        {
            if (cancellationToken.WaitHandle.WaitOne(interval)) return;
            Ping();
        }
    }

    /// <summary>ASYNCHRONOUS heartbeat start: runs the heartbeat on a background thread
    /// and returns a stop handle (Dispose to stop). Only one background heartbeat runs
    /// at a time.</summary>
    public IDisposable StartHeartbeat(TimeSpan interval)
    {
        lock (_lock)
        {
            if (_hbCts is { } existingCts)
                return new CancelHandle(() => existingCts.Cancel());
            var cts = new CancellationTokenSource();
            _hbCts = cts;
            var thread = new Thread(() =>
            {
                try { RunHeartbeat(interval, cts.Token); }
                catch { /* connection lost or cancelled: end the background loop quietly */ }
            })
            { IsBackground = true, Name = "corndogs-heartbeat" };
            thread.Start();
            return new CancelHandle(() =>
            {
                lock (_lock) { if (_hbCts == cts) _hbCts = null; }
                cts.Cancel();
            });
        }
    }

    /// <summary>Shuts the transport down: stops any heartbeat and closes the
    /// connection.</summary>
    public void Dispose()
    {
        lock (_lock)
        {
            _closed = true;
            if (_hbCts is { } cts)
            {
                cts.Cancel();
                _hbCts = null;
            }
            Reset();
        }
    }
}
