# Tier-1 clustering

Tier-1 clustering replicates the embedded file backend. It provides redundancy
and leader failover without an external database. It does not increase write
throughput because one leader performs all writes.

This feature is implemented. The Helm chart does not configure it. Configure
each node directly with environment variables.

## Architecture

Each node has a local bbolt database. One node is the leader. The leader sends
ordered physical database mutations to the followers. This method preserves
payloads, timestamps, state changes, and task order.

Followers serve local reads. A follower read can be stale. Only the leader
accepts writes. The cluster-aware Go client follows a leader redirect when it
sends a write to a follower.

Nodes use a persistent TCP connection for election and replication traffic.
Clients use CSIL-RPC over a separate persistent TCP connection. HTTP is only
for health checks and Prometheus metrics.

## Consistency and acknowledgements

`CORNDOGS_CLUSTER_ACK_COUNT` controls the number of followers that must apply a
write before the leader acknowledges it.

- The default is `floor(node_count / 2)`. This value permits only a partition
  with a write majority to commit data.
- A value of `0` enables asynchronous replication. Both sides of a network
  partition can then accept writes. Their data can diverge.
- `CORNDOGS_CLUSTER_ASYNC=true` also sets the effective acknowledgement count
  to `0`.

Use at least three nodes for write availability during one node failure. With
the default acknowledgement count, a two-node cluster cannot commit a write
when one node is unavailable.

## Configuration

Set these variables on each node:

| Variable | Default | Description |
| --- | --- | --- |
| `CORNDOGS_CLUSTER_ENABLED` | `false` | Enable the clustered file backend. This setting takes precedence over `STORAGE_BACKEND`. |
| `CORNDOGS_CLUSTER_NODE_ID` | none | Set the stable ID of this node. The ID must be in the peer list. |
| `CORNDOGS_CLUSTER_PEERS` | none | Set the full membership as `id=host:port` entries separated by commas. |
| `CORNDOGS_CLUSTER_LISTEN` | `0.0.0.0:5090` | Listen for cluster TCP traffic. |
| `CORNDOGS_CLUSTER_RPC_ADVERTISE` | none | Advertise the client RPC address as `host:port`. |
| `CORNDOGS_CLUSTER_ACK_COUNT` | `-1` | Set the required follower acknowledgements. `-1` selects the write-majority default. |
| `CORNDOGS_CLUSTER_ASYNC` | `false` | Use asynchronous replication and ignore the acknowledgement count. |
| `CORNDOGS_CLUSTER_REPLLOG_CHUNK_MB` | `64` | Start a new replication log segment at this size. Use `0` for one segment. |
| `CORNDOGS_CLUSTER_HEARTBEAT_TICKS` | `10` | Set the leader heartbeat interval in 100 ms ticks. |
| `CORNDOGS_CLUSTER_FAILURE_TICKS` | `20` | Set the leader failure interval in 100 ms ticks. The value must be at least two times the heartbeat value. |
| `CORNDOGS_LISTEN` | `0.0.0.0:5080` | Listen for client CSIL-RPC TCP connections. |
| `CORNDOGS_HTTP_LISTEN` | `:8080` | Listen for HTTP health and metrics requests. |
| `CORNDOGS_FILESTORE_DIR` | `./corndogs-data` | Store the local database and replication log in this directory. |

Example for the first node in a three-node cluster:

```sh
CORNDOGS_CLUSTER_ENABLED=true \
CORNDOGS_CLUSTER_NODE_ID=n1 \
CORNDOGS_CLUSTER_PEERS=n1=host1:5090,n2=host2:5090,n3=host3:5090 \
CORNDOGS_CLUSTER_LISTEN=0.0.0.0:5090 \
CORNDOGS_CLUSTER_RPC_ADVERTISE=host1:5080 \
CORNDOGS_LISTEN=0.0.0.0:5080 \
CORNDOGS_FILESTORE_DIR=/data \
./corndogs run
```

Use different node IDs, advertised RPC addresses, and data directories for the
other nodes.

## Client configuration

The Go client can use multiple seed addresses:

```go
client := corndogs.NewCluster("host1:5080", "host2:5080", "host3:5080")
```

The client retries a write only after a leader redirect or a connection
failure. It does not retry a service error.

## Operations

The cluster uses these network surfaces:

| Address | Protocol | Purpose |
| --- | --- | --- |
| `CORNDOGS_LISTEN` | CSIL-RPC over TCP | Client requests |
| `CORNDOGS_CLUSTER_LISTEN` | Cluster protocol over TCP | Election, replication, and topology updates |
| `CORNDOGS_HTTP_LISTEN` | HTTP | `/healthz` and optional `/metrics` |

When Prometheus is enabled, inspect these metrics:

- `corndogs_cluster_is_leader`
- `corndogs_cluster_epoch`
- `corndogs_cluster_applied_lsn`
- `corndogs_cluster_committed_lsn`
- `corndogs_cluster_ack_count`
- `corndogs_cluster_leader_changes_total`

A healthy cluster has one leader. Follower `applied_lsn` values must follow the
leader `committed_lsn` value.

If a node rejoins with stale or divergent data, it catches up from the
replication log or restores a leader snapshot. This operation does not require
operator action.

## Limitations

- The cluster has one writer and does not scale write throughput.
- Follower reads are eventually consistent.
- Asynchronous replication can cause divergent data during a network partition.
- The Helm chart supports only the single-replica file backend and does not
  create a Tier-1 cluster.
