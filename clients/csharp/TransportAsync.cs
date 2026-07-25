// CSIL-RPC async transport carrier for the corndogs C# library: a persistent TCP
// connection with real correlation-id multiplexing — many concurrent `await` calls
// share one connection without head-of-line blocking:
//
//     var client = await Corndogs.ConnectAsync("localhost:5080");
//     var resp = await client.SubmitTaskAsync(new SubmitTaskRequest { Queue = "q", ... });
//
// A background Task reads response frames off the connection and completes the
// matching caller's TaskCompletionSource by correlation id. This file owns only the
// CSIL-RPC envelope + framing + connection; the generated codec (Codec.gen.cs) owns
// (de)serialization. No third-party dependencies.
#nullable enable

using System;
using System.Collections.Concurrent;
using System.Net.Sockets;
using System.Threading;
// NOTE: no `using System.Threading.Tasks;` — this namespace (corndogs) also declares a
// generated record type named `Task` (Types.gen.cs), which would shadow
// System.Threading.Tasks.Task. Task-returning members below are fully qualified.

namespace corndogs;

/// <summary>Async, multiplexed CSIL-RPC transport over one persistent TCP connection.
/// Build with <c>await AsyncTcpTransport.ConnectAsync(addr)</c>. Implements the
/// generated <see cref="ICsilAsyncTransport"/> seam.</summary>
public sealed class AsyncTcpTransport : ICsilAsyncTransport, IAsyncDisposable
{
    readonly string _host;
    readonly int _port;
    readonly TimeSpan _connectTimeout;
    readonly SemaphoreSlim _connectLock = new(1, 1);
    readonly SemaphoreSlim _writeLock = new(1, 1); // serializes frame writes on the shared connection
    readonly ConcurrentDictionary<ulong, System.Threading.Tasks.TaskCompletionSource<byte[]>> _pending = new();
    TcpClient? _client;
    NetworkStream? _stream;
    long _nextId;
    System.Threading.Tasks.Task? _readLoopTask;
    CancellationTokenSource? _hbCts;
    bool _closed;

    AsyncTcpTransport(string host, int port, TimeSpan connectTimeout)
    {
        _host = host;
        _port = port;
        _connectTimeout = connectTimeout;
    }

    /// <summary>Dials <paramref name="addr"/> (host:port) and starts the background
    /// read loop.</summary>
    public static async System.Threading.Tasks.Task<AsyncTcpTransport> ConnectAsync(string addr, TimeSpan? connectTimeout = null)
    {
        var (host, port) = CsilRpcWire.SplitAddr(addr);
        var t = new AsyncTcpTransport(host, port, connectTimeout ?? TimeSpan.FromSeconds(5));
        await t.EnsureAsync().ConfigureAwait(false);
        return t;
    }

    async System.Threading.Tasks.Task<NetworkStream> EnsureAsync()
    {
        await _connectLock.WaitAsync().ConfigureAwait(false);
        try
        {
            if (_closed) throw new CorndogsTransportException("corndogs: transport closed");
            if (_stream is not null) return _stream;
            var client = new TcpClient();
            using var cts = new CancellationTokenSource(_connectTimeout);
            try
            {
                await client.ConnectAsync(_host, _port, cts.Token).ConfigureAwait(false);
            }
            catch (OperationCanceledException)
            {
                client.Dispose();
                throw new CorndogsTransportException($"corndogs: connect timeout dialing {_host}:{_port}");
            }
            catch (Exception ex)
            {
                client.Dispose();
                throw new CorndogsTransportException($"corndogs: {ex.Message}", ex);
            }
            client.NoDelay = true;
            _client = client;
            var stream = client.GetStream();
            _stream = stream;
            _readLoopTask = System.Threading.Tasks.Task.Run(() => ReadLoopAsync(stream));
            return stream;
        }
        finally
        {
            _connectLock.Release();
        }
    }

    async System.Threading.Tasks.Task ReadLoopAsync(NetworkStream stream)
    {
        try
        {
            while (true)
            {
                var frame = await CsilRpcWire.ReadFrameAsync(stream).ConfigureAwait(false);
                if (frame is null)
                {
                    Teardown(stream, new CorndogsTransportException("corndogs: connection closed"));
                    return;
                }
                CborValue val;
                try
                {
                    val = Cbor.Decode(frame);
                }
                catch (CborException)
                {
                    continue; // malformed frame: drop it, keep reading
                }
                var id = CsilRpcWire.ResponseId(val);
                if (_pending.TryRemove(id, out var tcs))
                    tcs.TrySetResult(frame);
            }
        }
        catch (Exception ex)
        {
            Teardown(stream, ex);
        }
    }

    void Teardown(NetworkStream stream, Exception cause)
    {
        if (!ReferenceEquals(_stream, stream)) return; // already superseded/torn down
        _stream = null;
        _client?.Dispose();
        _client = null;
        foreach (var id in _pending.Keys)
        {
            if (_pending.TryRemove(id, out var tcs))
                tcs.TrySetException(new CorndogsTransportException($"corndogs: connection lost: {cause.Message}", cause));
        }
    }

    /// <summary>Sends one request and awaits its correlated response. Implements
    /// <see cref="ICsilAsyncTransport.Call"/> for <see cref="CorndogsAsyncClient"/>.
    /// Concurrent callers are multiplexed over the one connection by correlation
    /// id.</summary>
    public async System.Threading.Tasks.Task<byte[]> Call(string service, string op, byte[] request)
    {
        var stream = await EnsureAsync().ConfigureAwait(false);
        var id = (ulong)Interlocked.Increment(ref _nextId);
        var tcs = new System.Threading.Tasks.TaskCompletionSource<byte[]>(System.Threading.Tasks.TaskCreationOptions.RunContinuationsAsynchronously);
        _pending[id] = tcs;

        var env = CsilRpcWire.BuildEnvelope(service, op, id, request);
        await _writeLock.WaitAsync().ConfigureAwait(false);
        try
        {
            await CsilRpcWire.WriteFrameAsync(stream, env).ConfigureAwait(false);
        }
        catch (Exception ex)
        {
            _pending.TryRemove(id, out _);
            Teardown(stream, ex);
            throw new CorndogsTransportException($"corndogs: {ex.Message}", ex);
        }
        finally
        {
            _writeLock.Release();
        }

        var frame = await tcs.Task.ConfigureAwait(false);
        return CsilRpcWire.ParseResponse(frame);
    }

    // --- heartbeat: awaitable loop and background-task start -----------------

    /// <summary>Sends a single control-plane heartbeat; the returned task faults if the
    /// server is unreachable.</summary>
    public System.Threading.Tasks.Task PingAsync() => Call(CsilRpcWire.ControlService, CsilRpcWire.OpPing, Array.Empty<byte>());

    /// <summary>The awaitable heartbeat loop: pings every <paramref name="interval"/>
    /// until <paramref name="cancellationToken"/> is cancelled or a ping fails. Await it
    /// yourself for a "sync-style" heartbeat, or use <see cref="StartHeartbeat"/> for the
    /// background-task form.</summary>
    public async System.Threading.Tasks.Task RunHeartbeatAsync(TimeSpan interval, CancellationToken cancellationToken = default)
    {
        while (true)
        {
            await System.Threading.Tasks.Task.Delay(interval, cancellationToken).ConfigureAwait(false);
            await PingAsync().ConfigureAwait(false);
        }
    }

    /// <summary>Starts the heartbeat as a background Task; returns a stop handle
    /// (Dispose to stop). Only one background heartbeat runs at a time.</summary>
    public IDisposable StartHeartbeat(TimeSpan interval)
    {
        if (_hbCts is { } existingCts)
            return new CancelHandle(() => existingCts.Cancel());
        var cts = new CancellationTokenSource();
        _hbCts = cts;
        _ = System.Threading.Tasks.Task.Run(async () =>
        {
            try { await RunHeartbeatAsync(interval, cts.Token).ConfigureAwait(false); }
            catch { /* connection lost or cancelled: end the background loop quietly */ }
        });
        return new CancelHandle(() =>
        {
            if (_hbCts == cts) _hbCts = null;
            cts.Cancel();
        });
    }

    /// <summary>Shuts the transport down: stops any heartbeat, closes the connection,
    /// and awaits the background read loop.</summary>
    public async System.Threading.Tasks.ValueTask DisposeAsync()
    {
        _closed = true;
        if (_hbCts is { } cts)
        {
            cts.Cancel();
            _hbCts = null;
        }
        var stream = _stream;
        if (stream is not null)
            Teardown(stream, new CorndogsTransportException("corndogs: transport closed"));
        if (_readLoopTask is not null)
        {
            try { await _readLoopTask.ConfigureAwait(false); } catch { /* already surfaced to pending callers */ }
        }
    }
}
