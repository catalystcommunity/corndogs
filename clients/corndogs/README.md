# corndogs (Go client)

The official Go client for [Corndogs](https://github.com/catalystcommunity/corndogs),
a task-state service. It ships a ready-to-use transport (CSIL-RPC over TCP, with a
built-in heartbeat) — connect and go.

```sh
go get github.com/CatalystCommunity/corndogs/clients/corndogs
```

## Usage

```go
package main

import (
	"context"

	corndogs "github.com/CatalystCommunity/corndogs/clients/corndogs"
)

func main() {
	c := corndogs.New("localhost:5080") // your corndogs server's TCP address
	ctx := context.Background()

	_, _ = c.SubmitTask(ctx, corndogs.SubmitTaskRequest{
		Queue: "emails", CurrentState: "submitted", AutoTargetState: "sending",
		Timeout: -1, Payload: []byte("..."),
	})
	got, _ := c.GetNextTask(ctx, corndogs.GetNextTaskRequest{
		Queue: "emails", CurrentState: "submitted",
	})
	_ = got.Delivery
}
```

Go calls are multiplexed over one connection (correlation ids), so concurrent
goroutines don't block each other.

## Heartbeat (keep the connection alive)

Build the transport explicitly to start a heartbeat. Both a sync (blocking) and an
async (background) start are provided:

```go
tr := &corndogs.StreamTransport{Addr: "localhost:5080"}
c := corndogs.NewCorndogsClient(tr)

stop := tr.StartHeartbeat(15 * time.Second) // async: background goroutine, returns stop()
defer stop()
// ... or run it yourself, blocking, in a goroutine you control:
//   go tr.RunHeartbeat(ctx, 15*time.Second)   // sync-style: blocks until ctx/err
//   tr.Ping(ctx)                               // single one-shot heartbeat
```

## Clustered deployments

Point the client at any node; a write that lands on a follower is transparently
redirected to the leader. Seed it with several nodes for failover:

```go
c := corndogs.NewCluster("node1:5080", "node2:5080", "node3:5080")
```

## Notes

- **Transport:** CSIL-RPC over TCP (4-byte length-prefix framing). HTTP is not used
  for RPC — the server serves RPC on its TCP port; HTTP is only for health/metrics.
- Errors: a service error is a `*corndogs.ClientError` with `Code`/`Message`; a
  transport failure is a `*corndogs.ClientError` wrapping the underlying error.
