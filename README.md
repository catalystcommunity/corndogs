# Corndogs
Cloud Native Background Task state manager for kubernetes and scaling out

Inspired by Celery and Sidekick, but meant to be language and UI agnostic.

## Status

We are alpha and not used in production, but ready to support people who want to!

### But why `Corndogs`?

My coworker Charlie and I were talking about Celery and how it was an option for Python, and he said something along the lines of wanting corndogs instead.

# Develop/Contribute

Install Helm, Skaffold, Kubectl, Kind, and Go 1.25+, then created a Kind cluster and then run the following in the root dir:

`skaffold dev && kubectl delete namespace skaffoldcorndogs`

This will deploy everything to your local Kubernetes in that namespace.

This will change to be a single bash script in the root, but will use the same tools as a workflow. Other workflows are valid, but you're on your own.

If you want to contribute, just PR into the `main` branch with a PR name following [Conventional Commits](https://www.conventionalcommits.org/) and describe the changes. You should only use `fix:` unless you create an Issue and chat first about what you want to do that's a minor bump so we know this is going to fit with our intentions.

PRs that don't match that branch schema will be rejected, maybe with a reminder. This is because CI/CD won't even run the tests when we've got CI/CD setup.

# Developing Without Kubernetes

One can also develop without the full Kubernetes flow by doing the following to get a normal local Go workflow:
1. Run the server from the project root. With the file backend it needs no external database:
```
STORAGE_BACKEND=file CORNDOGS_FILESTORE_DIR=./corndogs-data go run main.go run
```
   For the postgres backend, set the `DATABASE_*` variables first (see [docs/storage-backends.md](./docs/storage-backends.md)).
2. Make a request with the bundled CLI client. Each invocation makes one request; the flags default to `--address 127.0.0.1 --port 5080`:
```
go run main.go submit-task --queue myqueue --current-state submitted
```
   Generated clients for other languages live under [`clients/`](./clients).

# Intended Design
## Data Structures

A "task" is just a row in a db with the following fields:
 * Task UUID
 * Queue string
 * Current State string
 * Auto Target State (the state to move it to when picked up or timed out)
 * Submit time
 * Update time
 * Timeout (null if waiting for pulling, otherwise number of seconds until it "times out")
 * Payload (a bytestring, package it however you want. JSON, msgpack, whatever)

## Flow

Corndogs doesn't work on tasks. It is a task state manager.

Submitters submit to a queue. If a state string isn't given, it will be "submitted" and the auto target state will be "submitted-working" which for simple workflows should be fine. Any time something is submitted with a state but not an auto target state "-working" will be added to the state for the auto target state.

Workers can pull a new task from a queue and state. State defaults to "submitted" and it will get the next task based on Submit Time, not Update Time. This means a task that is failing will keep getting picked up. Error handling and submitting to "dead letter" queues is a responsibility of clients.

When workers "complete" a task, they submit it with an optional next task. This means they can submit the next state of the workflow in an atomic way. This optional "next_task" is the same as the submit requirements.

This allows simple and complicated workflows, alongside workers dedicated to each phase of a workflow. This should also allow highly horizontally scalable workloads using an appropriate datastore.

### Flow Continued

The above is a basic use case. However there are other features available that may not be required by most use cases.

### Timeouts 

When getting a task the auto target state is swapped with the current state, e.g. the current state becomes "submitted-working" by default. When getting a task, you can set also timeout. When a task times out the current and auto target states are swapped back so they can get picked up again.

You may want a task to timeout after being submitted. In this case, as an example, you can submit a task with a timeout and a "dead" state as it's auto target state. When getting a task you can then override the current and auto target states to move the task forward to state "B" with auto target "dead B".

A timeout does *not* happen automatically. You control when your timeout checks happen, using a `CleanUpTimedOutRequest`. It uses `at_time` to compare tasks against to see if they're timed out, and optionaly a `queue` to limit which tasks this affects. This has the added benefit of letting you time out tasks early or late in testing and such.

Corndogs does provide some helper utilities for timeouts. There is a `timeout` command provided in the cli that will send a request at the current time using the address, port, and optional queue flags. There is also a simple cronjob provided by [the corndogs chart](./helm_chart/README.md).

### Priority

A `priority` can be set when submitting a task and when updating a task. You must specify the priority in both or it will default to `0`. You can use a positive value to make sure it is prioritized by a GetNextTask request, or negative to make sure it is deprioritized.

### State and Timeout overrides
When using `GetNextTask` you can use the override fields to change what you get back. The current override fields are `override_current_state`, `override_auto_target_state`, and `override_timeout`. The timeout is pretty simple, it follows the same rules. The state overrides will override what you get back *after* the states are switched.

A task with `current_state: A` and `auto_target_state: B`\
gotten with `override_auto_target_state: C`\
will return with `current_state: B` and `auto_target_state: C`

## Storage backends

Corndogs stores task state behind a single `Store` interface and picks the
implementation at startup via `STORAGE_BACKEND`. Two backends are supported;
both have identical task semantics and differ only operationally.

**postgres** (default) — the shared backend for HA and horizontal scale-out.
Corndogs connects to a PostgreSQL you provide (or that the Helm chart deploys)
and hands out tasks with `SELECT ... FOR UPDATE SKIP LOCKED` across any number of
replicas.

```sh
STORAGE_BACKEND=postgres DATABASE_HOST=localhost DATABASE_USER=postgres \
  DATABASE_PASSWORD=postgres DATABASE_NAME=corndogs go run main.go run
```

**file** (embedded) — a single bbolt file, no separate database to operate. Fast
and simple, but **single-replica only** (the data file is owned by one process).
Group commit makes acked writes durable (they survive an abrupt kill or power
loss) while amortizing fsync across concurrent writers.

```sh
STORAGE_BACKEND=file CORNDOGS_FILESTORE_DIR=./corndogs-data go run main.go run
```

Full environment-variable reference, durability modes, and trade-offs:
**[docs/storage-backends.md](./docs/storage-backends.md)**. Deploying either
backend with Helm: **[docs/deployment.md](./docs/deployment.md)**.

## Metrics and such

Aside from logs and Prometheus metrics, a few initial endpoints are provided that allow intelligence around operation of or working against tasks.
Theres some overlap between these endpoints, but they allow for some flexibility for different use cases.
- `GetQueues` will list all queues and the total tasks in the system.
- `GetQueueTaskCounts` will return the amount of tasks in each queue, along with the total tasks in the system.
- `GetTaskStateCounts` returns tasks per state for the requested queue, and the total tasks in the queue.
- `GetQueueAndStateCounts` returns all queues, their tasks per state, and the total tasks in each queue.

## API docs
[corndogs/APIDOCS.md](./corndogs/APIDOCS.md) has more granular descriptions and helpful links.
