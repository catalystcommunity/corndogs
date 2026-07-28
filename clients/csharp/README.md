# corndogs (C# client)

The official C# client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — you connect and go, in **sync** or **async** style. No extra
setup, no dependencies beyond the .NET base class library.

```sh
dotnet add reference path/to/clients/csharp/corndogs.csproj
```

## Sync

```csharp
using corndogs;

var client = Corndogs.Connect("localhost:5080"); // your corndogs server's TCP address

// Submit a task, then claim the next one from the queue.
client.SubmitTask(new SubmitTaskRequest
{
    Queue = "emails", CurrentState = "submitted", AutoTargetState = "sending",
    Timeout = -1, Payload = System.Array.Empty<byte>(), Priority = 0,
});
var delivery = client.GetNextTask(new GetNextTaskRequest
{
    Queue = "emails", CurrentState = "submitted",
    OverrideTimeout = 0, OverrideCurrentState = "", OverrideAutoTargetState = "",
}).Delivery;
```

Heartbeat, two ways — build the transport explicitly so you can start it:

```csharp
var transport = new TcpTransport("localhost:5080");
var client = new CorndogsClient(transport);

using var stop = transport.StartHeartbeat(TimeSpan.FromSeconds(15)); // async: background thread, Dispose to stop
// ... or run it yourself, blocking, on a thread you control:
//   transport.RunHeartbeat(TimeSpan.FromSeconds(15));  // sync: blocks, pinging until cancelled
//   transport.Ping();                                   // or a single one-shot heartbeat
```

## Async

Concurrent calls are multiplexed over one connection (correlation ids), so they don't
block each other.

```csharp
using System.Linq;
using corndogs;

var client = await Corndogs.ConnectAsync("localhost:5080");

// 100 submits, concurrent, one connection:
var submits = Enumerable.Range(0, 100).Select(i => client.SubmitTaskAsync(new SubmitTaskRequest
{
    Queue = "emails", CurrentState = "submitted", AutoTargetState = "sending",
    Timeout = -1, Payload = System.Text.Encoding.UTF8.GetBytes($"job-{i}"), Priority = 0,
}));
await System.Threading.Tasks.Task.WhenAll(submits);
```

Async heartbeat, two ways:

```csharp
var transport = await AsyncTcpTransport.ConnectAsync("localhost:5080");
var client = new CorndogsAsyncClient(transport);

using var stop = transport.StartHeartbeat(TimeSpan.FromSeconds(15)); // background Task, Dispose to stop
await transport.RunHeartbeatAsync(TimeSpan.FromSeconds(15));         // or await the loop yourself
await transport.PingAsync();                                          // single one-shot heartbeat
```

## Notes

- **Transport:** CSIL-RPC over TCP, framed with a 4-byte length prefix. HTTP is not
  used for RPC (the corndogs server serves RPC on its TCP port; HTTP is only for
  health and Prometheus).
- **Clustered deployments:** point the transport at any node; a write that lands on a
  follower is transparently redirected to the leader.
- Errors: a service error raises the generated `corndogs.CsilClientException` (`Code` /
  `Message`); a transport failure raises `corndogs.CorndogsTransportException`.
- `TcpTransport` implements `IDisposable`; `AsyncTcpTransport` implements
  `IAsyncDisposable`. Dispose (or `await using`) when you're done with a connection.
- The generated package declares a `corndogs.Task` record (a queue task). If a file
  has both `using corndogs;` and `using System.Threading.Tasks;`, plain `Task` is
  ambiguous — qualify one side (`System.Threading.Tasks.Task` for the async type, or
  `corndogs.Task` for the queue task) as shown above.
