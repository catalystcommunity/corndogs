# Corndogs Helm chart

This chart is not in a chart registry. Install it from a repository checkout:

```sh
helm dependency update ./helm_chart/chart
helm install corndogs ./helm_chart/chart
```

See [Deploying with Helm](../docs/deployment.md) for examples.

## Storage backend

Set `storage.backend` to `postgres` or `file`.

The PostgreSQL backend is the default. The chart can install the Bitnami
PostgreSQL chart, connect to an external database, or create a database resource
for the Zalando PostgreSQL operator.

The file backend uses a local bbolt file. The chart permits only one Corndogs
replica in this mode. It rejects file-backend settings that enable autoscaling
or more than one replica. Disable both PostgreSQL options when you use the file
backend:

```sh
helm install corndogs ./helm_chart/chart \
  --set storage.backend=file \
  --set postgresql.enabled=false \
  --set zalando_postgres.enabled=false
```

See [values.yaml](./chart/values.yaml) for all chart values. See
[Storage backends](../docs/storage-backends.md) for the runtime settings and
trade-offs.

## PostgreSQL options

### Bitnami chart

The chart installs the Bitnami PostgreSQL chart by default. This configuration
has TLS and persistence disabled. Use it for tests and local development. Put
Bitnami chart values under `postgresql`.

### External database

Set `postgresql.enabled=false`. Then set the values under `database`.

### Zalando operator

Set `zalando_postgres.enabled=true` to create a PostgreSQL custom resource. Put
its specification under `zalando_postgres.spec`.

This chart does not install the Zalando operator. Install the operator before
you install this chart.

For the Zalando configuration, set `database.tls.enabled` to a PostgreSQL SSL
mode such as `require` or `disable`. For the Bitnami configuration, this value
is a Boolean value.
