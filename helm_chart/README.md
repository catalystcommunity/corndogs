# chart-corndogs
Helm chart for corndogs

This chart is not published to a chart registry; install it from a checkout of
this repo (`helm install corndogs ./helm_chart/chart`). See
[docs/deployment.md](../docs/deployment.md) for getting-started examples.

# Storage backend

The chart supports two storage backends, selected with `storage.backend`:

- `postgres` (default) — the shared, horizontally-scalable backend. The chart
  can deploy postgres for you (see [Database](#database) below) or point at an
  external one.
- `file` — an embedded, single-process bbolt backend with no separate database.
  It is **single-replica only**: the chart calls Helm `fail` and refuses to
  install if `replicaCount > 1` or autoscaling is enabled while
  `storage.backend=file`. When using it, set `postgresql.enabled=false` (and
  `zalando_postgres.enabled=false`) so no database is deployed alongside it.

All `storage.*` values are documented inline in
[values.yaml](./chart/values.yaml). Full backend reference and trade-offs:
[docs/storage-backends.md](../docs/storage-backends.md).

The sections below cover deploying postgres for the `postgres` backend.

# Database
This helm chart includes support for two methods of deploying postgres

**Bitnami Postgres Helm Chart**

By default this helm chart deploys postgres using the [bitnami postgresql helm chart](https://github.com/bitnami/charts/tree/main/bitnami/postgresql/), with tls and persistence disabled. The bitnami
chart is great for quickly experimenting with the chart, or running locally or in CI. Configuration is under the key `postgresql`
and you can simply use the values from the bitnami helm chart under this key.

**Zalando Operator**

For production workloads we recommend using the [zalando postgres operator](https://github.com/zalando/postgres-operator).
This helm chart can be configured to deploy a zalando postgresql crd using the `zalando_postgres` key. The CRD will only be
created if `zalando_postgres.enabled` is true. In this case the database user and password are populated by a secret key reference
to the credentials that the operator creates. The entire crd spec is passed into the crd using the `zalando_postgres.spec`
key. This way you can configure your zalando CRD however you want. We've included a default spec that works and shows an example of how to configure your crd.
> **_NOTE:_**  When using the zalando configuration you need to set the `database.tls.enabled` key differently. When using bitnami postgres this is a boolean.
> When using zalando this is the ssl type. So you should set it to [one of the ssl modes supported by postgres](https://www.postgresql.org/docs/8.4/libpq-connect.html#LIBPQ-CONNECT-SSLMODE)
>. Typically when using zalando this will be set to `require` buf if you disable tls in your crd spec you can set it to `disable`

> **_NOTE:_** This helm chart does not install the zalando operator. Installing the operator itself along with the chart would be a mistake. You'll need to run the zalando operator 
> in your cluster via other means if you want to use zalando postgres. This chart only supports creating the CRD for a postgres database.