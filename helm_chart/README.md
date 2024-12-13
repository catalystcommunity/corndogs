# chart-corndogs
Helm chart for corndogs

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