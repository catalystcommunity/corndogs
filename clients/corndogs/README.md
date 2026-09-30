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

## TLS

Use `NewTLS` when the server sets `CORNDOGS_TLS_CERT_FILE` and
`CORNDOGS_TLS_KEY_FILE`. A nil config verifies the server against the system
roots. To verify the server against your CA, set `RootCAs`:

```go
pem, err := os.ReadFile("/etc/corndogs/ca.crt")
if err != nil {
	log.Fatal(err)
}
roots := x509.NewCertPool()
roots.AppendCertsFromPEM(pem)
c := corndogs.NewTLS("corndogs.example.com:5080", &tls.Config{RootCAs: roots})
```

The client verifies the host part of the address. To verify a different name,
set `ServerName`. For a cluster, use `NewClusterTLS(cfg, seeds...)`. The
certificate of each node must be valid for the address that the node
advertises.

## Update a task

In `UpdateTaskRequest`, `Payload` and `Priority` are pointers. A nil value
keeps the stored value. To park a task without a new write of its payload,
and without a change to its priority, leave both nil:

```go
_, err := c.UpdateTask(ctx, corndogs.UpdateTaskRequest{
	Uuid:            task.Uuid,
	NewState:        "retry",
	AutoTargetState: "retry-working",
	Timeout:         60,
})
```

## Safe retries (server 0.8.0 and later)

Use a submission key for each logical submission, and task guards for each
worker. Store the key with your work item if you must survive a restart. Send
the same key and the same fields on every retry.

```go
if _, err := c.RequireFeatures(ctx, corndogs.FeatureSubmissionKeys, corndogs.FeatureTaskGuards); err != nil {
	log.Fatal(err) // an older server: do not fall back to SubmitTask
}
key := corndogs.NewSubmissionKey()
sub, err := c.SubmitKeyedTask(ctx, corndogs.SubmitKeyedTaskRequest{
	SubmissionKey: key, Guarded: true, Queue: "emails",
	CurrentState: "submitted", AutoTargetState: "submitted-working",
	Timeout: 60, Payload: payload,
})
// sub.Replayed is true when an earlier attempt already created the task.

claim, err := c.ClaimGuardedTask(ctx, corndogs.ClaimGuardedTaskRequest{
	OperationId: corndogs.NewOperationID(), Queue: "emails", CurrentState: "submitted",
})
if err == nil && claim.Delivery != nil {
	d := claim.Delivery
	// ... do the work with d.Payload ...
	_, err = c.CompleteGuardedTask(ctx, corndogs.CompleteGuardedTaskRequest{
		OperationId: corndogs.NewOperationID(), Uuid: d.Task.Task.Uuid,
		Queue: "emails", ExpectedRevision: d.Task.Revision,
	})
	if code, ok := corndogs.ServiceErrorCode(err); ok && code == corndogs.CodeRevisionConflict {
		// The claim expired and another worker has the task. Do not repeat the effect.
	}
}
```

The transports classify every failure:

| Check | Meaning |
| --- | --- |
| `errors.Is(err, corndogs.ErrOutcomeUncertain)` | The request was sent; it may have run |
| `errors.Is(err, corndogs.ErrNotApplied)` | The server did not apply it |
| `errors.Is(err, corndogs.ErrUnsupported)` | The server does not know the operation |
| `corndogs.ServiceErrorCode(err)` | A ServiceError from the server; final |

Both transports send a request again only when that is safe: after a failure
before the send, after a leader redirect, or for a keyed or guarded request
with the same bytes. They never send a legacy mutation (`SubmitTask`,
`GetNextTask`, `UpdateTask`, and so on) again after an uncertain outcome.
`StreamTransport.MaxAttempts` sets the attempt budget (default 3). See
[Resilience contract](../../docs/resilience.md).

## Notes

- **Transport:** CSIL-RPC over TCP (4-byte length-prefix framing). HTTP is not used
  for RPC — the server serves RPC on its TCP port; HTTP is only for health/metrics.
- Errors: a service error is a `*corndogs.ClientError` with `Code`/`Message`; a
  transport failure is a `*corndogs.ClientError` wrapping the underlying error.
  Use the checks in "Safe retries" to classify them.
