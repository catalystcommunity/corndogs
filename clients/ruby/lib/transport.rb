# frozen_string_literal: true

# Corndogs sync client transport: CSIL-RPC over a persistent TCP connection.
#
# This is Corndogs' own, ready-to-use carrier -- you do not need to write one or
# read any generator docs. Point it at a corndogs server's TCP address and pass
# it to the generated client:
#
#   require "corndogs"
#   require "corndogs/transport"
#
#   tr = TcpTransport.new("localhost:5080")
#   tr.start_heartbeat                      # keep the connection alive (background thread)
#   client = CorndogsClient.new(tr)
#   resp = client.submit_task(SubmitTaskRequest.new(
#     queue: "q", current_state: "submitted", auto_target_state: "sending",
#     timeout: -1, payload: "...".b, priority: 0,
#   ))
#
# The wire is CSIL-RPC framed with the canonical 4-byte big-endian length prefix
# over TCP. This carrier owns only the envelope + framing + connection; the
# generated client owns (de)serialization. Dependency-free (reuses the
# package's own CBOR codec).

require "socket"
require_relative "codec"

# Raised on a transport-level failure: a dropped connection, a non-zero
# transport status, or a malformed frame/envelope.
class TransportError < StandardError; end

# Raised when the server replies with a typed ServiceError (a status-0
# "ServiceError" variant). Carries the decoded #code and #message.
class CorndogsServiceError < StandardError
  attr_reader :code, :message

  def initialize(code, message)
    @code = code
    @message = message
    super("service error #{code}: #{message}")
  end
end

TAG_ENCODED_CBOR = 24 # RFC 8949 3.4.5.1 -- embedded encoded CBOR data item
CONTROL_SERVICE = "CorndogsService" # control-plane service name for the heartbeat
OP_PING = "$ping" # control-plane heartbeat op (never collides with app ops)
MAX_FRAME = 1025 << 20 # payload hard maximum plus RPC envelope allowance

# Sync CSIL-RPC transport over one persistent TCP connection. Calls are
# serialized (one in flight); the connection re-dials on failure. Thread-safe.
# Implements the generated client's transport seam: a duck-typed object
# responding to `call(service, op, req_bytes) -> resp_bytes`.
class TcpTransport
  # A ready TcpTransport for `addr` (host:port).
  def self.connect(addr)
    new(addr)
  end

  def initialize(addr, connect_timeout: 5)
    @host, @port = self.class.split_addr(addr)
    @connect_timeout = connect_timeout
    @mutex = Mutex.new
    @socket = nil
    @next_id = 0
    @hb_thread = nil
    @hb_stop = nil
  end

  def self.split_addr(addr)
    host, _, port = addr.to_s.rpartition(":")
    raise ArgumentError, "corndogs: address must be host:port, got #{addr.inspect}" if host.empty?

    [host, Integer(port)]
  end

  # The seam the generated client calls with the already-encoded request bytes.
  def call(service, op, req_bytes)
    @mutex.synchronize do
      sock = ensure_socket
      @next_id += 1
      env = CsilCbor.encode(
        {
          "v" => 1,
          "service" => service,
          "op" => op,
          "id" => @next_id,
          "payload" => CsilCbor::Tag.new(TAG_ENCODED_CBOR, req_bytes.b)
        }
      )
      begin
        write_frame(sock, env)
        resp = read_frame(sock)
      rescue TransportError
        reset
        raise
      rescue StandardError => e
        reset
        raise TransportError, e.message
      end
      if resp.nil?
        reset
        raise TransportError, "connection closed"
      end
      parse_response(resp)
    end
  end

  # --- heartbeat: one-shot, sync/blocking, and background thread -------------

  # Send one control-plane heartbeat (raises TransportError on a dead server).
  def ping
    call(CONTROL_SERVICE, OP_PING, "".b)
  end

  # SYNCHRONOUS heartbeat start: blocks, pinging every `interval` seconds,
  # until `stop` (a HeartbeatStop) is signalled -- or forever if no `stop` is
  # given. Run it in a Thread you own, or use `start_heartbeat` for the
  # background form.
  def run_heartbeat(interval = 15, stop: nil)
    loop do
      if stop
        return if stop.wait(interval)
      else
        sleep interval
      end
      ping
    end
  end

  # ASYNCHRONOUS heartbeat start: runs the heartbeat on a background Thread and
  # returns a stop proc. Idempotent -- only one background heartbeat runs at a
  # time; calling it again returns the existing stopper.
  def start_heartbeat(interval = 15)
    new_stop = nil
    @mutex.synchronize do
      return @hb_stop.stopper if @hb_thread

      new_stop = HeartbeatStop.new
      @hb_stop = new_stop
      @hb_thread = Thread.new { run_heartbeat(interval, stop: new_stop) }
      @hb_thread.abort_on_exception = false
    end
    new_stop.stopper
  end

  # Stops any heartbeat and closes the connection.
  def close
    hb_thread, hb_stop = nil, nil
    @mutex.synchronize do
      hb_thread, hb_stop = @hb_thread, @hb_stop
      @hb_thread = nil
      @hb_stop = nil
      reset
    end
    hb_stop&.set
    hb_thread&.join
  end

  private

  def ensure_socket
    return @socket if @socket

    sock = Socket.tcp(@host, @port, connect_timeout: @connect_timeout)
    sock.setsockopt(Socket::IPPROTO_TCP, Socket::TCP_NODELAY, 1)
    @socket = sock
  end

  def reset
    @socket&.close
  rescue IOError
    nil
  ensure
    @socket = nil
  end

  def write_frame(sock, bytes)
    raise TransportError, "frame too large (#{bytes.bytesize})" if bytes.bytesize > MAX_FRAME

    sock.write([bytes.bytesize].pack("N") + bytes)
  end

  def read_frame(sock)
    head = read_exact(sock, 4)
    return nil unless head

    length = head.unpack1("N")
    raise TransportError, "frame too large (#{length})" if length > MAX_FRAME

    read_exact(sock, length)
  end

  def read_exact(sock, n)
    return "".b if n.zero?

    buf = "".b
    while buf.bytesize < n
      chunk = sock.read(n - buf.bytesize)
      return nil if chunk.nil?

      buf << chunk
    end
    buf
  end

  def parse_response(frame)
    val = CsilCbor.decode(frame)
    raise TransportError, "malformed response envelope" unless val.is_a?(Hash)

    status = val["status"] || 0
    raise TransportError, "transport status #{status}: #{val["error"]}" unless status.zero?

    payload = val["payload"]
    return "".b if payload.nil? # control replies (e.g. $pong) carry no payload

    payload = payload.value if payload.is_a?(CsilCbor::Tag)

    if val["variant"] == "ServiceError"
      inner = ServiceError.from_cbor(payload)
      raise CorndogsServiceError.new(inner.code, inner.message)
    end

    payload
  end
end

# A tiny stop-flag usable from `run_heartbeat`'s `stop:` kwarg or
# `start_heartbeat`'s background thread. #wait blocks up to `timeout` seconds
# and returns true if signalled meanwhile (or already signalled).
class HeartbeatStop
  def initialize
    @mutex = Mutex.new
    @cond = ConditionVariable.new
    @set = false
  end

  def set
    @mutex.synchronize do
      @set = true
      @cond.broadcast
    end
  end

  def wait(timeout)
    @mutex.synchronize do
      return true if @set

      @cond.wait(@mutex, timeout)
      @set
    end
  end

  def stopper
    method(:set)
  end
end
