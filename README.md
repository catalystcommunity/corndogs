# Corndogs

Corndogs is a task-state service for Kubernetes and other environments. It
manages task state. It does not run task code.

Corndogs is alpha software.

## Run Corndogs locally

Install Go 1.25 or later. Then start the server with the embedded file backend:

```sh
cd corndogs
STORAGE_BACKEND=file CORNDOGS_FILESTORE_DIR=./corndogs-data go run . run
```

The server listens on two ports:

- Port `5080` uses CSIL-RPC over TCP for client requests.
- Port `8080` uses HTTP for `/healthz` and optional Prometheus metrics.

Submit a task with the command-line client:

```sh
go run . submit-task --queue myqueue --current-state submitted
```

Run `go run . --help` to see all commands and flags.

## Use a generated client

The [`clients`](./clients) directory contains clients for Go, Python, Rust,
TypeScript, Java, Kotlin, C#, Dart, Ruby, Elixir, OCaml, Zig, C, and Swift. Each
client README contains an example.

All clients use CSIL-RPC over a persistent TCP connection. A client can use one
server address. A cluster client can use multiple seed addresses and follow
leader redirects.

The following pseudocode shows the main flow:

```text
client = CorndogsClient("host:5080")

client.submit_task(
    queue = "emails",
    current_state = "submitted",
    auto_target_state = "sending",
    timeout = 30,
    payload = bytes(...),
    priority = 0,
)

loop:
    result = client.get_next_task(
        queue = "emails",
        current_state = "submitted",
    )
    if result.delivery is absent:
        wait
        continue

    task = result.delivery.task
    process(result.delivery.payload)
    client.complete_task(
        uuid = task.uuid,
        queue = task.queue,
        current_state = task.current_state,
    )
```

`GetNextTask` claims one task and changes its state in one operation. The
service selects the highest priority first. For equal priorities, it selects
the oldest task first.

## Task states and timeouts

If a submit request has no current state, Corndogs uses `submitted`. If it has
no auto-target state, Corndogs adds `-working` to the current state.

When a worker claims a task, Corndogs exchanges the current state and the
auto-target state. A timeout exchanges the states again. This operation makes
the task available for another claim.

Corndogs does not process timeouts on a schedule. Send a `CleanUpTimedOut`
request when you want to process them. The CLI provides this command:

```sh
go run . timeout
go run . timeout --queue myqueue
```

## Storage

Corndogs supports these storage modes:

| Mode | Use |
| --- | --- |
| PostgreSQL | Use a shared database for horizontal scale. This mode is the default. |
| File | Use one bbolt file and no external database. |
| Clustered file | Replicate a local bbolt file between Corndogs nodes. |

See [Storage backends](./docs/storage-backends.md) and [Tier-1 clustering](./docs/clustering-tier1.md).

## Deploy and operate

- [Deployment with Helm](./docs/deployment.md)
- [API reference](./corndogs/APIDOCS.md)
- [Client packages](./clients/README.md)

## Develop and contribute

For local Kubernetes development, install Helm, Skaffold, `kubectl`, Kind, and
Go 1.25 or later. Create a Kind cluster. Then run this command from the
repository root:

```sh
skaffold dev
```

Run the Go tests from the server module:

```sh
cd corndogs
go test ./...
```

Use a Conventional Commits title for a pull request to `main`.
