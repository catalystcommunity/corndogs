// Corndogs client transport: CSIL-RPC over a persistent TCP connection.
//
// This is Corndogs' own, ready-to-use carrier — you do not need to write one or
// read csilgen docs. Point it at a corndogs server's TCP address and pass it to
// the generated async client:
//
//   import 'package:corndogs/corndogs.dart';
//   import 'package:corndogs/transport.dart';
//
//   final tr = await TcpTransport.connect('localhost:5080');
//   tr.startHeartbeat(const Duration(seconds: 15)); // keep the connection alive
//   final client = CorndogsAsyncClient(tr);
//   final resp = await client.submitTask(SubmitTaskRequest(
//     queue: 'q', currentState: 'submitted', autoTargetState: 'sending',
//     timeout: -1, payload: Uint8List(0), priority: 0,
//   ));
//
// The wire is CSIL-RPC framed with the canonical 4-byte big-endian length prefix
// over TCP. This carrier owns only the envelope + framing + connection; the
// generated client owns (de)serialization. Dependency-free (dart:io + dart:async
// only; reuses the generated CBOR codec for every value it can).
//
// One connection MULTIPLEXES many concurrent calls: each request carries a
// correlation `id`, a background frame reader matches each response back to its
// waiting caller, so concurrent submitTask/getNextTask calls do not head-of-line
// block each other.
//
// Dart has no synchronous socket API, so only the generated *async* client
// (`CorndogsAsyncClient`, via `AsyncCsilTransport`) is served here. The generated
// synchronous `CorndogsClient` (`CsilTransport`) cannot be backed by real network
// I/O in Dart and is intentionally not implemented by this carrier.

import 'dart:async';
import 'dart:io';
import 'dart:typed_data';

import 'csil_cbor.gen.dart';
import 'client.async.gen.dart';
import 'types.gen.dart';

const int _tagEncodedCbor = 24; // RFC 8949 §3.4.5.1 — embedded encoded CBOR data item
const int _maxFrame = 1025 << 20; // payload maximum plus envelope allowance
const Duration _defaultConnectTimeout = Duration(seconds: 5);
const Duration _defaultHeartbeatInterval = Duration(seconds: 15);
const String _controlService = 'CorndogsService';
const String _opPing = r'$ping'; // control-plane heartbeat op (never collides with app ops)

/// A transport-level failure: connection lost, malformed frame, a non-zero
/// transport status, or an invalid address/timeout. Distinct from
/// [CorndogsServiceException], which carries a structured application error.
class CorndogsTransportException implements Exception {
  final String message;
  const CorndogsTransportException(this.message);

  @override
  String toString() => 'CorndogsTransportException: $message';
}

/// A structured application error the server returned (CSIL-RPC's
/// `ServiceError` variant), decoded via the generated [ServiceError] type.
class CorndogsServiceException implements Exception {
  final ServiceError error;
  const CorndogsServiceException(this.error);

  int get code => error.code;
  String get message => error.message;

  @override
  String toString() => 'CorndogsServiceException($code): $message';
}

/// CSIL-RPC transport over a persistent, multiplexed TCP connection. Implements
/// the generated [AsyncCsilTransport] protocol (`call`), so it plugs straight
/// into [CorndogsAsyncClient]. The connection is dialed lazily (on first call,
/// or eagerly via [TcpTransport.connect]) and re-dialed on failure.
class TcpTransport implements AsyncCsilTransport {
  /// A lazily-connecting transport for `host:port`. The first call to [call]
  /// (or [ping]) dials the connection. Use [TcpTransport.connect] instead if
  /// you want to await the connection up front.
  TcpTransport(String addr, {Duration connectTimeout = _defaultConnectTimeout})
      : _host = _splitAddr(addr).$1,
        _port = _splitAddr(addr).$2,
        _connectTimeout = connectTimeout;

  final String _host;
  final int _port;
  final Duration _connectTimeout;

  Socket? _socket;
  StreamSubscription<Uint8List>? _sub;
  Future<void>? _connecting;
  bool _closed = false;

  final List<int> _recvBuf = <int>[];
  final Map<int, Completer<Object?>> _pending = <int, Completer<Object?>>{};
  int _nextId = 0;

  Timer? _hbTimer;

  /// An already-connected [TcpTransport] for `addr` (`host:port`).
  static Future<TcpTransport> connect(
    String addr, {
    Duration connectTimeout = _defaultConnectTimeout,
  }) async {
    final t = TcpTransport(addr, connectTimeout: connectTimeout);
    await t._ensureConnected();
    return t;
  }

  Future<void> _ensureConnected() {
    if (_closed) {
      throw const CorndogsTransportException('transport closed');
    }
    if (_socket != null) return Future.value();
    final inFlight = _connecting;
    if (inFlight != null) return inFlight;

    final completer = Completer<void>();
    _connecting = completer.future;
    () async {
      try {
        final socket = await Socket.connect(_host, _port, timeout: _connectTimeout);
        socket.setOption(SocketOption.tcpNoDelay, true);
        _recvBuf.clear();
        _socket = socket;
        _sub = socket.listen(
          _onData,
          onDone: () => _teardown(const CorndogsTransportException('connection closed')),
          onError: (Object e) => _teardown(CorndogsTransportException('socket error: $e')),
          cancelOnError: true,
        );
        completer.complete();
      } catch (e) {
        completer.completeError(CorndogsTransportException('connect failed: $e'));
      } finally {
        _connecting = null;
      }
    }();
    return completer.future;
  }

  void _onData(List<int> chunk) {
    _recvBuf.addAll(chunk);
    while (true) {
      if (_recvBuf.length < 4) return;
      final len = (_recvBuf[0] << 24) | (_recvBuf[1] << 16) | (_recvBuf[2] << 8) | _recvBuf[3];
      if (len > _maxFrame) {
        _teardown(CorndogsTransportException('frame too large ($len)'));
        return;
      }
      if (_recvBuf.length < 4 + len) return;
      final frame = Uint8List.fromList(_recvBuf.sublist(4, 4 + len));
      _recvBuf.removeRange(0, 4 + len);
      _handleFrame(frame);
    }
  }

  void _handleFrame(Uint8List frame) {
    Object? val;
    try {
      val = CsilCbor.decode(frame);
    } catch (_) {
      return; // malformed frame: drop it, the caller's request will eventually time out
    }
    if (val is! Map) return;
    final idVal = val['id'];
    final id = idVal is int ? idVal : 0;
    final completer = _pending.remove(id);
    if (completer != null && !completer.isCompleted) {
      completer.complete(val);
    }
  }

  /// Closes the current connection (if it is still the live one) and fails every
  /// pending call, so the next call re-dials.
  void _teardown(CorndogsTransportException cause) {
    final socket = _socket;
    if (socket == null) return;
    _socket = null;
    _sub?.cancel();
    _sub = null;
    _recvBuf.clear();
    socket.destroy();
    final pending = Map<int, Completer<Object?>>.from(_pending);
    _pending.clear();
    for (final c in pending.values) {
      if (!c.isCompleted) c.completeError(cause);
    }
  }

  @override
  Future<Uint8List> call(String service, String op, List<int> request) async {
    await _ensureConnected();
    final socket = _socket;
    if (socket == null) {
      throw const CorndogsTransportException('connection lost');
    }

    final id = ++_nextId;
    final completer = Completer<Object?>();
    _pending[id] = completer;

    final envelope = _buildRequestEnvelope(service, op, id, request);
    try {
      await _writeFrame(socket, _withLengthPrefix(envelope));
    } catch (e) {
      _pending.remove(id);
      final err = CorndogsTransportException('write failed: $e');
      _teardown(err);
      throw err;
    }

    final val = await completer.future;
    return _extractPayload(val);
  }

  // Serializes frame writes on the shared connection: concurrent calls each add
  // + flush the same Socket, and Dart's IOSink throws if a second flush starts
  // before the first completes, so writes are chained through this queue. Reads
  // (and therefore concurrent calls in flight) are unaffected — only the write
  // side is serialized.
  Future<void> _writeQueue = Future<void>.value();

  Future<void> _writeFrame(Socket socket, Uint8List frame) {
    final next = _writeQueue.then((_) async {
      socket.add(frame);
      await socket.flush();
    });
    // Swallow the error here so a failed write doesn't leave `_writeQueue`
    // permanently rejected for the next call; the caller above still sees it.
    _writeQueue = next.catchError((Object _) {});
    return next;
  }

  Uint8List _extractPayload(Object? val) {
    if (val is! Map) {
      throw const CorndogsTransportException('malformed response envelope');
    }
    final status = val['status'];
    if (status is int && status != 0) {
      final err = val['error'];
      throw CorndogsTransportException('transport status $status: ${err ?? ''}');
    }
    final payload = val['payload'];
    if (payload == null) {
      return Uint8List(0); // control replies (e.g. $pong) carry no payload
    }
    final bytes = payload is Uint8List ? payload : Uint8List.fromList((payload as List).cast<int>());
    if (val['variant'] == 'ServiceError') {
      throw CorndogsServiceException(ServiceError.fromCborValue(CsilCbor.decode(bytes)));
    }
    return bytes;
  }

  // --- heartbeat -------------------------------------------------------------

  /// Sends a single control-plane heartbeat; throws if the server is
  /// unreachable. Cheap; keeps an idle connection alive and detects a dead
  /// server.
  Future<void> ping() async {
    await call(_controlService, _opPing, const <int>[]);
  }

  /// The SYNCHRONOUS-style heartbeat start: an awaitable loop that pings every
  /// [interval] until [close] is called (returns normally) or a ping fails
  /// (the error propagates out of the awaited future). Await it in a context
  /// you own, or use [startHeartbeat] for the background form.
  Future<void> runHeartbeat([Duration interval = _defaultHeartbeatInterval]) async {
    while (!_closed) {
      await Future<void>.delayed(interval);
      if (_closed) return;
      await ping();
    }
  }

  /// The ASYNCHRONOUS heartbeat start: runs the heartbeat on a background
  /// [Timer] and returns a stop function. Calling the returned function (or
  /// [close]) ends it. Only one background heartbeat runs at a time; a repeat
  /// call returns a stop function for the existing one.
  void Function() startHeartbeat([Duration interval = _defaultHeartbeatInterval]) {
    final existing = _hbTimer;
    if (existing != null) {
      return () => _stopHeartbeatTimer(existing);
    }
    final timer = Timer.periodic(interval, (_) {
      unawaited(ping().catchError((Object _) {})); // a dead server just means the next tick retries
    });
    _hbTimer = timer;
    return () => _stopHeartbeatTimer(timer);
  }

  void _stopHeartbeatTimer(Timer timer) {
    timer.cancel();
    if (identical(_hbTimer, timer)) _hbTimer = null;
  }

  /// Shuts the transport down: stops any heartbeat, fails pending calls, and
  /// closes the connection.
  Future<void> close() async {
    _closed = true;
    _hbTimer?.cancel();
    _hbTimer = null;
    _teardown(const CorndogsTransportException('transport closed'));
  }
}

/// Convenience: an already-connected [TcpTransport] for `addr` (`host:port`).
Future<TcpTransport> connect(String addr) => TcpTransport.connect(addr);

(String, int) _splitAddr(String addr) {
  final idx = addr.lastIndexOf(':');
  if (idx <= 0 || idx == addr.length - 1) {
    throw ArgumentError('corndogs: address must be host:port, got $addr');
  }
  final host = addr.substring(0, idx);
  final port = int.tryParse(addr.substring(idx + 1));
  if (port == null) {
    throw ArgumentError('corndogs: address must be host:port, got $addr');
  }
  return (host, port);
}

// --- envelope + framing helpers ---------------------------------------------
//
// The generated CsilCbor codec (csil_cbor.gen.dart) encodes any single scalar,
// list, or map value, so it is reused for every value below. It does not expose
// a builder for a CBOR tag (major type 6) — needed here to wrap the request
// payload in tag 24 ("embedded CBOR data item") — so the handful of bytes that
// make up that wrapper, and the fixed 5-key envelope map header, are written
// directly, mirroring CsilCbor's own canonical head-byte encoding.

Uint8List _buildRequestEnvelope(String service, String op, int id, List<int> payload) {
  final out = BytesBuilder();
  _writeHead(out, 5, 5); // map, 5 key/value pairs
  out.add(CsilCbor.encodeValue('v'));
  out.add(CsilCbor.encodeValue(1));
  out.add(CsilCbor.encodeValue('service'));
  out.add(CsilCbor.encodeValue(service));
  out.add(CsilCbor.encodeValue('op'));
  out.add(CsilCbor.encodeValue(op));
  out.add(CsilCbor.encodeValue('id'));
  out.add(CsilCbor.encodeValue(id));
  out.add(CsilCbor.encodeValue('payload'));
  _writeHead(out, 6, _tagEncodedCbor); // tag 24
  out.add(CsilCbor.encodeValue(Uint8List.fromList(payload))); // the tagged byte string
  return out.toBytes();
}

void _writeHead(BytesBuilder out, int major, int n) {
  final mt = major << 5;
  if (n < 24) {
    out.addByte(mt | n);
  } else if (n < 0x100) {
    out.addByte(mt | 24);
    out.addByte(n);
  } else if (n < 0x10000) {
    out.addByte(mt | 25);
    out.addByte((n >> 8) & 0xff);
    out.addByte(n & 0xff);
  } else {
    out.addByte(mt | 26);
    for (var i = 3; i >= 0; i--) {
      out.addByte((n >> (i * 8)) & 0xff);
    }
  }
}

Uint8List _withLengthPrefix(Uint8List body) {
  if (body.length > _maxFrame) {
    throw CorndogsTransportException('frame too large (${body.length} > $_maxFrame)');
  }
  final out = Uint8List(4 + body.length);
  ByteData.sublistView(out).setUint32(0, body.length, Endian.big);
  out.setRange(4, 4 + body.length, body);
  return out;
}
