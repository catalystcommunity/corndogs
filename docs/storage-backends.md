# Storage backends

Corndogs stores task state behind a single `Store` interface and selects the
implementation at startup with the `STORAGE_BACKEND` environment variable:

| `STORAGE_BACKEND` | Backend | Use when |
| --- | --- | --- |
| `postgres` (default) | External PostgreSQL | You want multiple replicas, HA/failover, and shared state, and are willing to operate a database. |
| `file` | Embedded, single-process [bbolt](https://github.com/etcd-io/bbolt) | You want one process and no separate database to run, and a single replica is acceptable. |

Both backends implement identical semantics (priority ordering, the timeout
state-swap, completion/cancellation, metrics). They differ only in operational
properties.

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

Migrations run automatically on startup. For Helm deployment of postgres itself
(bundled bitnami chart or the Zalando operator), see
[deployment.md](./deployment.md) and the [chart README](../helm_chart/README.md).

### Trade-offs

- **Pros:** horizontal scale-out, replica failover, backups/replication handled
  by your database, mature operational tooling.
- **Cons:** you run and maintain a separate database.

---

## file (embedded)

An embedded backend that keeps all state in a single bbolt file on a mounted
volume. There is no separate database process. Because the data file is owned by
one OS process (it takes an exclusive file lock), the file backend is
**single-replica only** — it cannot be shared across pods.

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

Every mutation (submit, claim, update, complete, cancel, timeout) is appended as
newline-delimited JSON — a durable, replayable history of state transitions
(more than the postgres backend keeps, which only records terminal
completed/canceled rows). Appends are O(1) regardless of log size, so it never
slows the write path.

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

It is a log, not a query store: corndogs' own queries and metrics hit the
indexed bbolt state, never the log. For ad-hoc analytics over the history, query
the JSONL with a tool rather than scanning by hand — e.g. **DuckDB**
(`SELECT ... FROM read_json_auto('audit-*.log')`, no server) for local analysis,
or **ship it to ClickHouse / Loki / BigQuery** for ongoing dashboards.

### Trade-offs

- **Pros:** no separate system to operate; very fast on the dequeue hot path; a
  single file is trivial to back up (copy `corndogs.bolt`); the audit log gives
  a replayable history.
- **Cons:** single replica only (no horizontal scale-out or HA failover); you
  own backups/redundancy.

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
