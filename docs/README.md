# Corndogs documentation

- [Storage backends](./storage-backends.md) — choosing between the `postgres`
  and `file` backends, their full environment-variable reference, durability
  modes, and trade-offs.
- [Deploying with Helm](./deployment.md) — installing the chart from this repo
  (it is not published to a registry), with examples for each backend.
- [Tier-1 clustering](./clustering-tier1.md) — configuring replicated local
  files, leader failover, and acknowledgement requirements.
- [Resilience contract](./resilience.md) — submission keys, task guards,
  admission policies, client retry rules, and the upgrade from 0.7.x.
- [API reference](../corndogs/APIDOCS.md) — the RPC surface.
