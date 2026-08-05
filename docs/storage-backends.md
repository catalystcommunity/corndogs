# Storage backends

Corndogs stores task state behind a single `Store` interface and selects the
implementation at startup with the `STORAGE_BACKEND` environment variable:

| `STORAGE_BACKEND` | Backend | Use when |
| --- | --- | --- |
| `postgres` (default) | External PostgreSQL | You want multiple replicas, HA/failover, and shared state, and are willing to operate a database. |
| `file` | Embedded [bbolt](https://github.com/etcd-io/bbolt) | You want one process and no separate database. |

Both backends implement identical semantics (priority ordering, the timeout
state-swap, completion/cancellation, metrics). They differ only in operational
properties.

## Payload storage

Both backends store payloads as opaque bytes. Corndogs does not use JSON
marshalling for payloads.

The default payload limit is 16 MiB. Set `CORNDOGS_MAX_PAYLOAD_BYTES` to change
the limit. The minimum is 1 byte. The maximum is 1073741823 bytes. Client frame
limits must permit the configured payload size and the RPC envelope. The
clients in this repository permit the full Corndogs range.

Corndogs sends payload bytes only in `GetNextTask` and `GetNextTaskGroup`
responses. State lookup, submit, update, complete, and cancel responses contain
only task metadata.

---

## postgres (default)

The shared backend. Corndogs connects to a PostgreSQL instance you provide (or
that the Helm chart deploys for you) and uses `SELECT ... FOR UPDATE SKIP
LOCKED` to hand out tasks safely across any number of server replicas.

### Configuration

| Env var | Default | Meaning |
| --- | --- | --- |
| `STORAGE_BACKEND` | `postgres` | Select this backend. |
| `DATABASE_HOST` | `corndogs-postgresql` | Postgres host. |
| `DATABASE_PORT` | `5432` | Postgres port. |
| `DATABASE_NAME` | `localcorndogsdev` | Database name. |
| `DATABASE_USER` | `postgres` | Username. |
| `DATABASE_PASSWORD` | `localcorndogsdevpass` | Password. |
| `DATABASE_SSL_MODE` | `disable` | libpq sslmode (`disable`, `require`, `verify-full`, ...). |
| `DATABASE_MAX_OPEN_CONNS` | `10` | Max open pooled connections. |
| `DATABASE_MAX_IDLE_CONNS` | `1` | Max idle pooled connections. |
| `DATABASE_CONN_MAX_LIFETIME_SECONDS` | `3600` | Connection max lifetime. |
| `DATABASE_STARTUP_TIMEOUT_SECONDS` | `120` | Maximum time to wait for PostgreSQL at startup. |

Migrations run automatically on startup. For Helm deployment of postgres itself
(bundled bitnami chart or the Zalando operator), see
[deployment.md](./deployment.md) and the [chart README](../helm_chart/README.md).

The payload column is PostgreSQL `bytea` with `STORAGE EXTERNAL`. PostgreSQL
can store a large value outside the task row. PostgreSQL does not compress the
payload. Metadata-only queries do not select the payload column.

### Trade-offs

- **Pros:** horizontal scale-out, replica failover, backups/replication handled
  by your database, mature operational tooling.
- **Cons:** you run and maintain a separate database.

---

## file (embedded)

An embedded backend keeps all state in a bbolt file on a mounted volume. There
is no separate database process. One process owns each data file and takes an
exclusive file lock. Do not share one file between processes. For replicated
local files, use [Tier-1 clustering](./clustering-tier1.md). The Helm chart
supports only the single-replica file configuration.

The file backend stores task metadata as JSON. It stores payloads as raw values
in a separate bbolt bucket. A deadline index supports timeout cleanup. On
startup, Corndogs migrates old task records that contain JSON payload fields.
The migration uses atomic batches and can continue after a restart.

### Configuration

| Env var | Default | Meaning |
| --- | --- | --- |
| `STORAGE_BACKEND` | `postgres` | Set to `file` to use this backend. |
| `CORNDOGS_FILESTORE_DIR` | `./corndogs-data` | Directory holding the data file (`corndogs.bolt`). |
| `CORNDOGS_FILESTORE_AUDIT_DIR` | = `CORNDOGS_FILESTORE_DIR` | Directory for the append-only audit segments. Point at a separate volume to isolate it. |
| `CORNDOGS_FILESTORE_AUDIT_ENABLED` | `true` | Write the audit log. |
| `CORNDOGS_FILESTORE_AUDIT_CHUNK_MB` | `250` | Roll to a new segment file once the active one reaches this many MB (`0` = never roll). |
| `CORNDOGS_FILESTORE_SYNC` | `group` | Durability mode (see below). |
| `CORNDOGS_FILESTORE_GROUP_MAX_BATCH` | `0` | Cap on writes coalesced per fsync in `group` mode (`0` = unbounded; self-limits to in-flight writers). |
| `CORNDOGS_FILESTORE_GROUP_MAX_DELAY` | `0s` | Optional linger to widen group-commit batches, e.g. `200us`. |

### Durability modes (`CORNDOGS_FILESTORE_SYNC`)

A write is "acked" once `SubmitTask`/etc. returns to the caller.

| Mode | Acked write is on disk before returning? | Notes |
| --- | --- | --- |
| `group` (default) | Yes | **Group commit:** coalesces all in-flight writes into one fsync. Durable *and* fast — the recommended default. |
| `always` | Yes | One fsync per write. Durable but slow under load. |
| `interval` | No (≤ ~1s window) | Flushes on a timer; a crash may lose the last second of acked writes. |
| `never` | No | No explicit fsync; durability left to the OS. Fastest, least safe. |

With `group` (or `always`), an acked task is fsync'd before the ack, so it
survives an abrupt process/container kill **and** a host power loss. (bbolt's
copy-on-write + double-buffered metadata means the file is never left in a torn
state, so it always reopens cleanly.) `interval`/`never` only protect against
process death, not power loss.

### Audit log

Corndogs appends each mutation to a newline-delimited JSON log. The operations
are submit, claim, update, complete, cancel, and timeout. This log contains more
history than the PostgreSQL backend, which records only completed and canceled
tasks.

The audit log uses the selected sync mode. In `always` mode, Corndogs flushes
each audit record before it continues. In `interval` mode, it flushes the log on
a timer. In `group` and `never` modes, the operating system can keep the newest
audit records in memory until rotation or shutdown. Thus, a power failure can
remove the newest audit records even when the bbolt data is durable in `group`
mode.

The log is written as size-bounded segment files named `audit-000001.log`,
`audit-000002.log`, …. The active segment rolls to the next once it reaches
`CORNDOGS_FILESTORE_AUDIT_CHUNK_MB` (default 250 MB; `0` disables rolling). The
size is a threshold, not a hard cap — a segment may slightly exceed it.
Rotation bounds individual file size; total history still grows, so prune or
ship old segments per your retention needs.

By default it lives alongside the data file. Point it at its own volume with
`CORNDOGS_FILESTORE_AUDIT_DIR` (env) or, in Helm,
`storage.file.persistence.audit.enabled=true` plus
`storage.file.auditDir=/audit` — useful to isolate audit I/O or retain history
on cheaper/separate storage. Disable entirely with
`CORNDOGS_FILESTORE_AUDIT_ENABLED=false`.

Corndogs does not query the audit log. Its queries and metrics use the indexed
bbolt data. Use a JSONL query tool or send the segments to your log system if
you need to analyze the history.

### Trade-offs

- **Pros:** no separate system to operate; very fast on the dequeue hot path; a
  single file is trivial to back up (copy `corndogs.bolt`); the audit log gives
  a replayable history.
- **Cons:** one process can write each data file. Without Tier-1 clustering, you
  own backups and redundancy.

### Single-replica enforcement

The Helm chart **refuses to install** the file backend with more than one
replica (it forces `replicas: 1`, sets a `Recreate` rollout strategy, and calls
`fail` if `replicaCount > 1` or autoscaling is enabled). At runtime, bbolt's
exclusive file lock also prevents a second process from opening the same data
file. If you need multiple replicas, use `postgres`.

---

## Rough performance (illustrative)

`GetNextTask` drain, single queue, concurrent workers, on one machine. Numbers
are hardware-dependent — measure on yours — but the shape is representative:
the file backend matches postgres at low concurrency and pulls ahead as workers
increase, because postgres contends on `SKIP LOCKED` at the queue head while the
file backend's group commit batches more writes per fsync.

| Concurrent workers | postgres (durable) | file `group` (durable) |
| --- | --- | --- |
| 8 | ~1,100/s | ~1,500/s |
| 32 | ~2,000/s | ~5,800/s |
| 128 | ~1,800/s | ~12,000/s |

These are throughput figures, not a recommendation to pick on speed alone —
choose `postgres` for HA/scale-out and `file` for operational simplicity.
