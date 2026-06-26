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

This provisions a `ReadWriteOnce` PVC mounted at `/data`, forces a single
replica, and uses a `Recreate` rollout. Common overrides:

```sh
  --set storage.file.persistence.size=20Gi \
  --set storage.file.sync=group \
  --set storage.file.persistence.audit.enabled=true \   # audit log on its own PVC
  --set storage.file.auditDir=/audit
```

> The file backend is **single replica only**. The chart will refuse to install
> (it calls Helm `fail`) if `replicaCount > 1` or autoscaling is enabled while
> `storage.backend=file`. Use the postgres backend for multiple replicas.

Full details, durability modes, and the crash-safety guarantee:
[storage-backends.md → file](./storage-backends.md#file-embedded).

---

## Running without Kubernetes

The same backends work for a plain local process. Set the env vars and run:

```sh
# file backend (no database needed)
STORAGE_BACKEND=file CORNDOGS_FILESTORE_DIR=./corndogs-data go run main.go run

# postgres backend
STORAGE_BACKEND=postgres DATABASE_HOST=localhost DATABASE_USER=postgres \
  DATABASE_PASSWORD=postgres DATABASE_NAME=corndogs go run main.go run
```

See [storage-backends.md](./storage-backends.md) for the full env-var reference.
