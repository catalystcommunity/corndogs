defmodule Corndogs.Transport do
  @moduledoc """
  Corndogs' own, ready-to-use client transport: CSIL-RPC over a persistent TCP
  connection. You do not need to write a carrier yourself — point this at a
  corndogs server's TCP address and hand it to the generated client:

      transport = Corndogs.Transport.connect("localhost:5080")
      Corndogs.Transport.start_heartbeat(transport)   # keep the connection alive (background)
      client = Csilgen.Generated.CorndogsClient.new(transport)

      resp =
        Csilgen.Generated.CorndogsClient.submit_task(client, %Csilgen.Generated.SubmitTaskRequest{
          queue: "emails",
          current_state: "submitted",
          auto_target_state: "sending",
          timeout: -1,
          payload: "...",
          priority: 0
        })

  The wire is CSIL-RPC framed with a 4-byte big-endian length prefix over
  `:gen_tcp` (no `:inets`/`:ssl` dependency, so it runs as-is under `mix` or a
  release with no `extra_applications`). This module owns only the envelope,
  framing, and connection lifecycle; the generated codec owns
  (de)serialization of typed request/response structs.

  The connection is a `GenServer` that owns the socket, so calls are
  serialized and automatically re-dial on failure. It implements
  `Csilgen.Generated.Transport`, the behaviour the generated client drives.
  """

  use GenServer

  @behaviour Csilgen.Generated.Transport

  alias Csilgen.Generated.Cbor
  alias Csilgen.Generated.ServiceError, as: GeneratedServiceError

  # RFC 8949 §3.4.5.1 — embedded encoded CBOR data item.
  @tag_encoded_cbor 24
  @max_frame 256 * 1024 * 1024
  @default_connect_timeout 5_000
  @default_heartbeat_interval 15_000

  # Control-plane heartbeat: never collides with an application op.
  @control_service "CorndogsService"
  @op_ping "$ping"

  defstruct [:pid]
  @type t :: %__MODULE__{pid: pid()}

  defmodule TransportError do
    @moduledoc "A transport-level failure: connection dropped, dial failed, malformed frame."
    defexception [:message]
  end

  defmodule ServiceError do
    @moduledoc "A structured application error the service returned."
    defexception [:code, :message]

    @impl true
    def message(%__MODULE__{code: code, message: msg}),
      do: "corndogs: service error #{code}: #{msg}"
  end

  # --- construction ------------------------------------------------------

  @doc """
  Connects a new transport to `addr` (`"host:port"`). The socket is dialed
  lazily on the first call, and re-dialed automatically after a failure.
  """
  @spec connect(String.t(), keyword()) :: t()
  def connect(addr, opts \\ []) do
    {:ok, pid} = GenServer.start_link(__MODULE__, {addr, opts})
    %__MODULE__{pid: pid}
  end

  @doc "Closes the connection (and stops the socket)."
  @spec close(t()) :: :ok
  def close(%__MODULE__{pid: pid}) do
    if Process.alive?(pid), do: GenServer.stop(pid)
    :ok
  end

  # --- Csilgen.Generated.Transport behaviour ------------------------------

  @doc """
  Implements `Csilgen.Generated.Transport.call/4`: sends one request and
  waits for its correlated response. Raises `Corndogs.Transport.ServiceError`
  for a typed application error, or `Corndogs.Transport.TransportError` for a
  connection/transport failure.
  """
  @impl Csilgen.Generated.Transport
  @spec call(t(), String.t(), String.t(), binary()) :: binary()
  def call(%__MODULE__{pid: pid}, service, op, req) do
    case GenServer.call(pid, {:rpc, service, op, req}, :infinity) do
      {:ok, payload} ->
        payload

      {:error, %GeneratedServiceError{code: code, message: message}} ->
        raise ServiceError, code: code, message: message

      {:error, reason} ->
        raise TransportError, message: format_reason(reason)
    end
  end

  # --- heartbeat -----------------------------------------------------------

  @doc "Sends a single control-plane heartbeat; raises if the server is unreachable."
  @spec ping(t()) :: :ok
  def ping(%__MODULE__{} = t) do
    call(t, @control_service, @op_ping, <<>>)
    :ok
  end

  @doc """
  SYNCHRONOUS heartbeat: blocks the calling process, pinging every `interval`
  milliseconds, until either a ping fails (returns `{:error, exception}`) or
  the process receives `:corndogs_stop_heartbeat` (returns `:ok`). Run it in a
  process you own (e.g. `spawn/1` or `Task.start/1`) and `send/2` it the stop
  message to end it — or use `start_heartbeat/2` for the background form.
  """
  @spec run_heartbeat(t(), non_neg_integer()) :: :ok | {:error, Exception.t()}
  def run_heartbeat(%__MODULE__{} = t, interval \\ @default_heartbeat_interval) do
    receive do
      :corndogs_stop_heartbeat -> :ok
    after
      interval ->
        try do
          ping(t)
          run_heartbeat(t, interval)
        rescue
          e -> {:error, e}
        end
    end
  end

  @doc """
  ASYNCHRONOUS heartbeat: runs `run_heartbeat/2` on a background process and
  returns a zero-arity stop function. Calling it stops the heartbeat; a failed
  ping also ends it on its own (the connection re-dials on the next real call
  regardless).
  """
  @spec start_heartbeat(t(), non_neg_integer()) :: (-> :ok)
  def start_heartbeat(%__MODULE__{} = t, interval \\ @default_heartbeat_interval) do
    pid = spawn(fn -> run_heartbeat(t, interval) end)
    fn -> send(pid, :corndogs_stop_heartbeat) end
  end

  # --- GenServer: owns the socket, serializes calls, re-dials on failure ---

  @impl GenServer
  def init({addr, opts}) do
    {host, port} = split_addr(addr)
    timeout = Keyword.get(opts, :connect_timeout, @default_connect_timeout)
    {:ok, %{host: host, port: port, timeout: timeout, socket: nil, next_id: 0}}
  end

  @impl GenServer
  def handle_call({:rpc, service, op, req}, _from, state) do
    id = state.next_id + 1
    state = %{state | next_id: id}

    case ensure_socket(state) do
      {:ok, socket, state} ->
        case send_and_recv(socket, encode_envelope(service, op, id, req)) do
          {:ok, frame} ->
            {:reply, parse_response(frame), state}

          {:error, reason} ->
            close_socket(socket)
            {:reply, {:error, reason}, %{state | socket: nil}}
        end

      {:error, reason} ->
        {:reply, {:error, reason}, state}
    end
  end

  @impl GenServer
  def terminate(_reason, state) do
    close_socket(state.socket)
    :ok
  end

  defp ensure_socket(%{socket: nil, host: host, port: port, timeout: timeout} = state) do
    case :gen_tcp.connect(host, port, [:binary, active: false, packet: :raw], timeout) do
      {:ok, socket} ->
        :inet.setopts(socket, nodelay: true, keepalive: true)
        {:ok, socket, %{state | socket: socket}}

      {:error, reason} ->
        {:error, reason}
    end
  end

  defp ensure_socket(%{socket: socket} = state), do: {:ok, socket, state}

  defp close_socket(nil), do: :ok
  defp close_socket(socket), do: :gen_tcp.close(socket)

  defp send_and_recv(socket, env) do
    with :ok <- write_frame(socket, env) do
      read_frame(socket)
    end
  end

  defp write_frame(_socket, env) when byte_size(env) > @max_frame,
    do: {:error, {:frame_too_large, byte_size(env)}}

  defp write_frame(socket, env) do
    :gen_tcp.send(socket, <<byte_size(env)::big-unsigned-integer-size(32), env::binary>>)
  end

  defp read_frame(socket) do
    with {:ok, len_bin} <- :gen_tcp.recv(socket, 4) do
      <<len::big-unsigned-integer-size(32)>> = len_bin

      cond do
        len > @max_frame -> {:error, {:frame_too_large, len}}
        len == 0 -> {:ok, <<>>}
        true -> :gen_tcp.recv(socket, len)
      end
    end
  end

  # --- envelope -------------------------------------------------------------

  defp encode_envelope(service, op, id, req) do
    Cbor.encode(
      {:map,
       [
         {{:text, "v"}, {:int, 1}},
         {{:text, "service"}, {:text, service}},
         {{:text, "op"}, {:text, op}},
         {{:text, "id"}, {:int, id}},
         {{:text, "payload"}, {:tag, @tag_encoded_cbor, {:bytes, req}}}
       ]}
    )
  end

  # Decodes a CsilRpcResponse frame into {:ok, inner_payload} | {:error, reason}. A
  # non-zero transport status or a "ServiceError" variant becomes an {:error, _}.
  defp parse_response(frame) do
    case Cbor.decode(frame) do
      {:map, _} = env ->
        status = map_get_int(env, "status", 0)

        if status != 0 do
          {:error, {:transport_status, status, map_get_text(env, "error", "")}}
        else
          decode_payload(env)
        end

      _ ->
        {:error, :malformed_response}
    end
  rescue
    e -> {:error, {:malformed_response, e}}
  end

  defp decode_payload(env) do
    case map_get(env, "payload") do
      nil ->
        {:ok, <<>>}

      {:tag, _num, {:bytes, inner}} ->
        wrap_variant(env, inner)

      {:bytes, inner} ->
        wrap_variant(env, inner)

      _ ->
        {:error, :malformed_payload}
    end
  end

  defp wrap_variant(env, inner) do
    if map_get_text(env, "variant", nil) == "ServiceError" do
      {:error, GeneratedServiceError.from_cbor(inner)}
    else
      {:ok, inner}
    end
  end

  defp map_get({:map, kvs}, key) do
    Enum.find_value(kvs, fn
      {{:text, ^key}, v} -> {:ok, v}
      _ -> nil
    end)
    |> case do
      {:ok, v} -> v
      nil -> nil
    end
  end

  defp map_get_int(env, key, default) do
    case map_get(env, key) do
      {:int, n} -> n
      _ -> default
    end
  end

  defp map_get_text(env, key, default) do
    case map_get(env, key) do
      {:text, s} -> s
      _ -> default
    end
  end

  # --- address parsing --------------------------------------------------

  defp split_addr(addr) do
    parts = String.split(addr, ":")

    case Enum.split(parts, -1) do
      {host_parts, [port_str]} when host_parts != [] ->
        {String.to_charlist(Enum.join(host_parts, ":")), String.to_integer(port_str)}

      _ ->
        raise ArgumentError, "corndogs: address must be host:port, got #{inspect(addr)}"
    end
  end

  defp format_reason({:transport_status, status, msg}), do: "transport status #{status}: #{msg}"
  defp format_reason({:frame_too_large, n}), do: "frame too large (#{n})"
  defp format_reason(:malformed_response), do: "malformed response envelope"
  defp format_reason(:malformed_payload), do: "response payload is not a tag-24 byte string"

  defp format_reason({:malformed_response, e}),
    do: "malformed response envelope: #{Exception.message(e)}"

  defp format_reason(:closed), do: "connection closed"
  defp format_reason(reason) when is_atom(reason), do: :inet.format_error(reason) |> to_string()
  defp format_reason(reason), do: inspect(reason)

  @doc "Convenience: a ready transport for `addr` (`\"host:port\"`)."
  @spec new(String.t(), keyword()) :: t()
  def new(addr, opts \\ []), do: connect(addr, opts)
end
