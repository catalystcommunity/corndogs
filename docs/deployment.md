# Deploying with Helm

The chart lives in this repository at [`helm_chart/chart`](../helm_chart/chart)
and is **not published to a chart registry**, so install it from a local
checkout by pointing Helm at that path.

```sh
git clone https://github.com/CatalystCommunity/corndogs
cd corndogs

# Fetch the bundled postgres subchart dependency (needed for the default backend)
helm dependency update ./helm_chart/chart
```

The container image is published to
`containers.catalystsquad.com/public/catalystcommunity/corndogs` and is the
chart default; override with `--set image.tag=...` or `--set image.repository=...`.

For the full list of storage settings and trade-offs, see
[storage-backends.md](./storage-backends.md). The chart's postgres-deployment
options (bundled bitnami vs. the Zalando operator) are described in the
[chart README](../helm_chart/README.md).

---

## postgres backend (default)

By default the chart deploys a bundled bitnami PostgreSQL alongside corndogs —
good for trying it out, local, or CI:

```sh
helm install corndogs ./helm_chart/chart
```

Point at an existing/external database instead:

```sh
helm install corndogs ./helm_chart/chart \
  --set postgresql.enabled=false \
  --set database.host=my-postgres.example.com \
  --set database.dbname=corndogs \
  --set database.user=corndogs \
  --set database.password=secret
```

For production postgres (HA, backups) use the Zalando operator via the
`zalando_postgres` values — see the [chart README](../helm_chart/README.md).
Full details: [storage-backends.md → postgres](./storage-backends.md#postgres-default).

---

## file backend (embedded, single replica)

No database. Corndogs stores state in a single bbolt file on a PVC. Disable the
bundled postgres and select the file backend:

```sh
helm install corndogs ./helm_chart/chart \
  --set storage.backend=file \
  --set postgresql.enabled=false
```

This command creates a `ReadWriteOnce` PVC at `/data`. It also selects one
replica and the `Recreate` update strategy. You can add these settings:

```sh
helm install corndogs ./helm_chart/chart \
  --set storage.backend=file \
  --set postgresql.enabled=false \
  --set storage.file.persistence.size=20Gi \
  --set storage.file.sync=group \
  --set storage.file.persistence.audit.enabled=true \
  --set storage.file.auditDir=/audit
```

> The file backend is **single replica only**. The chart will refuse to install
> (it calls Helm `fail`) if `replicaCount > 1` or autoscaling is enabled while
> `storage.backend=file`. Use the postgres backend for multiple replicas.

Full details, durability modes, and the crash-safety guarantee:
[storage-backends.md → file](./storage-backends.md#file-embedded).

---

## Running without Kubernetes

The same backends work for a local process. Run these commands from the
`corndogs` module directory:

```sh
# file backend (no database needed)
STORAGE_BACKEND=file CORNDOGS_FILESTORE_DIR=./corndogs-data go run . run

# postgres backend
STORAGE_BACKEND=postgres DATABASE_HOST=localhost DATABASE_USER=postgres \
  DATABASE_PASSWORD=postgres DATABASE_NAME=corndogs go run . run
```

See [storage-backends.md](./storage-backends.md) for the full env-var reference.

## TLS

Set `tls.enabled=true` and `tls.secretName` to serve the RPC port with TLS. The
Secret must have the type `kubernetes.io/tls`, with `tls.crt` and `tls.key`.
cert-manager makes a Secret of this type. The certificate must be valid for
each name that clients use, for example `corndogs.<namespace>.svc`.

```sh
helm install corndogs ./helm_chart/chart \
  --set tls.enabled=true \
  --set tls.secretName=corndogs-tls \
  --set tls.caKey=ca.crt
```

The chart mounts the whole Secret. When the Secret changes, the kubelet updates
the files, and Corndogs loads the new certificate without a restart. Do not
mount the files with `subPath`, because the kubelet does not update a `subPath`
mount.

The timeout CronJob connects with TLS when `tls.enabled=true`. If `tls.caKey`
is set, the CronJob verifies the server with that CA from the Secret. If it is
empty, the CronJob uses the system roots. Set `tls.serverName` when the
certificate does not contain the CronJob address.

For a process outside Kubernetes, see
[the TLS settings](../corndogs/APIDOCS.md#tls).

## Payload limit

The Helm value `appconfig.maxPayloadBytes` sets the maximum payload size. The
default is `16777216` bytes (16 MiB). The valid range is 1 through 1073741823
bytes.

For a process outside Kubernetes, set `CORNDOGS_MAX_PAYLOAD_BYTES`. Configure
each custom client frame limit to permit the payload and its RPC envelope. The
clients in this repository permit the full Corndogs range.
