# Converting corndogs: protobuf → CSIL, and GitHub Actions → reactorcide

Two independent migrations, planned together because they touch the same files
and release flow. They can ship in either order; this doc sequences CSIL first
because it reshapes the artifacts the CI later publishes.

- **Migration A — protobuf/gRPC → CSIL + CBOR-over-HTTP** (csilgen).
- **Migration B — GitHub Actions → reactorcide** (`.reactorcide/jobs/`).

Goal driver (per maintainer): get off Google-controlled wire formats (protobuf /
gRPC) onto a CBOR + CSIL ecosystem, and off GitHub Actions onto reactorcide,
which corndogs itself backs as the task queue.

---

## Part 0 — What was empirically validated

Before planning, `corndogs.proto` was hand-translated to `corndogs.csil` and run
through a locally-built `csilgen` (all 11 RPCs, maps, `bytes`, empty requests).
Findings that shape the plan:

**Works today (correct CSIL usage):**
- The whole service translates and validates (28 rules). `bytes → []byte /
  Vec<u8> (serde_bytes) / bytes / Uint8Array`; arrays, optionals, and empty
  messages (`GetQueuesRequest = {}`) all generate.
- Maps must be **named type aliases**, not inline field types. `queue_counts:
  {* text => int}` fails to parse (so does csilgen's own
  `complex-arrays-maps.csil` fixture); `StringInt64Map = {* text => int}` then
  `queue_counts: StringInt64Map` works. This is idiomatic CSIL, not a defect to
  file.
- **TypeScript** produces a complete, typed, transport-agnostic client
  (`CorndogsClient`) plus a server dispatch helper. The maintainer's theory —
  "the TS client can do CBOR directly" — is confirmed *with one nuance*: the
  generated client is CBOR-ready but delegates the actual encode to a ~15-line
  `ServiceTransport` the consumer supplies (longhouse ships `CborHttpTransport`).
  That split is intentional and good; CBOR bytes go straight in the POST body, no
  JSON, no gRPC.

**Gaps that were filed as csilgen-requests — all RESOLVED 2026-06-08:**
1. `client-generators-go-rust-python.md` — `go-client`/`rust-client`/`python-client`
   now emit real typed clients (and unknown sub-targets error instead of falling
   back). ✅
2. `typed-service-responses-go-rust.md` — Go emits `(Res, error)`, Rust
   `Result<Res, ServiceError>`; Rust acronym casing fixed. ✅
3. `python-typed-models.md` — Python emits real `@dataclass` models. ✅
4. `cbor-wire-key-consistency.md` — Go emits `cbor:"<field>"` tags; canonical wire
   contract documented in csilgen `docs/cbor-wire-contract.md`. ✅

These were real generator gaps (not misuse). With them resolved, **all four
clients now generate** (verified — see `clients/`); the Go/Rust/Python CBOR
transports remain to be hand-written (TS is done), and the Go server edge (A3) is
unblocked.

> Provider note: the maintainer asked that generated clients not reference
> provider-specific endpoints (bandwidth, telnyx, etc.). **N/A for corndogs** —
> the service is pure task-state management with no third-party provider surface
> in the contract; there is nothing to strip.

---

## Part A — protobuf/gRPC → CSIL + CBOR-over-HTTP

### A.1 What changes architecturally

Today corndogs is a **gRPC** service: `grpc.NewServer`, `RegisterCorndogsServiceServer`,
gRPC health-checking (`grpc_health_probe` in the Dockerfile, `grpc_health_v1`),
`grpc_prometheus` interceptors, port 5080, and a gRPC Python client on PyPI.

CSIL is **not** gRPC and emits no wire. The new shape:

| Concern | Today (gRPC) | After (CSIL + CBOR/HTTP) |
|---|---|---|
| IDL | `protos/corndogsapis/.../corndogs.proto` | `csil/corndogs.csil` |
| Codegen | `buf` + protoc plugins (go, go-grpc, python, grpclib) | `csilgen generate` per target |
| Wire | protobuf over HTTP/2 (gRPC) | CBOR in an HTTP POST body |
| Routing | gRPC method dispatch | `POST /v1alpha1/{Service}/{Method}`, body = CBOR(request) |
| Server | gRPC server + generated stubs | `net/http` server + generated Go service interface + small CBOR dispatch |
| Health | `grpc_health_v1` + `grpc_health_probe` | `GET /healthz` (k8s `httpGet` probe) |
| Metrics | `grpc_prometheus` interceptors | `promhttp` + HTTP middleware on the mux |
| Clients | Python gRPC (PyPI) | Go, Rust, Python, TypeScript — each: generated client + tiny CBOR transport |

The server's business logic (`server/store`, `server/implementations`,
`server/conversions`, the postgres/gorm layer, goose migrations) is **unchanged**.
Only the edge — transport, codec, routing, health, metrics — is rebuilt.

### A.2 The canonical wire (decide once, document in the CSIL repo too)

- One endpoint per operation: `POST /v1alpha1/{Service}/{Method}`
  (e.g. `POST /v1alpha1/CorndogsService/SubmitTask`). `Service`/`Method` strings
  come straight from the generated clients (csilgen passes lowercased service +
  PascalCase method to the transport; we map them into the path).
- Request body: `application/cbor` = CBOR(request struct).
- Response body: `application/cbor` = CBOR(response struct); errors as a
  `ServiceError { code, message }` with a non-2xx status.
- Map/field keys on the wire are the **CSIL field names verbatim** (snake_case).
  This is the contract csilgen-request #4 asks csilgen to guarantee in Go.

### A.3 The CSIL source

Replace `protos/` with a `csil/` module. `corndogs.csil` (validated draft built
during planning) mirrors the proto with these deltas:
- `package`/`version` via the `options { package: "corndogs", version: "v1alpha1" }` block.
- Named map aliases: `StringInt64Map = {* text => int}`,
  `QueueAndStateCountsMap = {* text => QueueAndStateCounts}`.
- Add `ServiceError = { code: uint, message: text }` and append
  `/ ServiceError` to every operation so the error channel is modeled.
- `Task` keeps `payload: bytes` (opaque CBOR/bytes blob — unchanged semantics).

### A.4 Phases

**A-Phase 1 — Land the CSIL contract (no behavior change yet).**
- Add `csil/corndogs.csil`. Keep `protos/` in place for now (parallel run).
- Add a generation script `csil/generate.sh` invoking `csilgen generate` for each
  installed target, writing into per-language `clients/{go,rust,python,typescript}/`
  directories. Generated output is committed (own directories per language); these
  may be published as packages later but are not release artifacts yet.

**A-Phase 2 — Unblock the four clients (depends on csilgen-requests).**
- Track requests #1–#4. The TypeScript client is usable **today**; Go/Rust/Python
  clients need #1 (and #2/#3 for type fidelity, #4 for Go wire keys).
- As each generator lands, wire a thin per-language CBOR-over-HTTP transport
  (~15–30 lines each), mirroring longhouse's `CborHttpTransport`:
  - Go: `net/http` + `github.com/fxamacker/cbor/v2`.
  - Rust: `reqwest` + `ciborium`.
  - Python: `httpx`/`requests` + `cbor2`.
  - TypeScript: `fetch` + a CBOR lib (e.g. `cbor-x`).
- Each client package ships the generated types + client + transport as one
  installable unit.

**A-Phase 3 — Rebuild the server edge on CBOR/HTTP.**
- Generate the Go **service interface** (after request #2 so handlers are typed).
- Implement an HTTP mux: route → CBOR-decode request → call the existing
  implementation method → CBOR-encode response (or `ServiceError` + status).
- Replace gRPC health with `GET /healthz`; replace `grpc_prometheus` with
  `promhttp` + a per-route latency/count middleware (keep the same metric
  intent).
- Adapt `server/implementations/*` to satisfy the generated interface (they
  already contain the logic; mostly signature/glue changes).

**A-Phase 4 — Cut over deployment + clients.**
- Dockerfile: drop `grpc_health_probe`; the image just needs the HTTP server.
- Helm chart: change the container port, switch liveness/readiness to
  `httpGet: /healthz`, update any service annotations. Bump chart appVersion via
  the normal release flow.
- `corndogs/test/`: port integration tests from a gRPC client to the generated
  Go client + CBOR transport (the kind-cluster test waits on the HTTP port
  instead of gRPC 5080).
- The four clients live in `clients/<lang>/`. Publishing them as packages is
  deferred (optional, later) — not a cutover blocker.
- Once consumers are migrated, delete `protos/` and the `buf`/protoc tooling.

### A.5 Risks / watch-items (Migration A)
- **Blocked on csilgen** for 3 of 4 clients and the typed Go server. The TS client
  is the proof it all hangs together; the rest is generator work tracked in the
  request files. If a request stalls, the interim fallback is a hand-written
  client for that language against the documented wire — but that's the thing we
  want to avoid, so prefer landing the generator.
- **Breaking change for every existing client.** gRPC consumers must move to a
  CBOR client. corndogs is pre-1.0 (currently `0.3.0-alpha`), so this ships as a
  normal pre-1.0 version bump — **no major version** — but it is a hard wire
  break: ship it behind a clear changelog entry and a migration note for
  `pycorndogs` users rather than treating it as a routine change.
- **Health/metrics parity** — make sure the new `/healthz` + `promhttp` surface
  matches what dashboards/alerts expect before deleting the gRPC paths.
- **csilgen is alpha** — pin the `csilgen` version used for generation and record
  it in `csil/generate.sh` so codegen is reproducible.

---

## Part B — GitHub Actions → reactorcide

corndogs is the task queue reactorcide runs on, so adopting reactorcide here is
natural (and dogfoods it). reactorcide jobs live in `.reactorcide/jobs/*.yaml`;
each job runs one command in a fresh container, triggers follow-up jobs via
`runnerlib trigger`, and reads secrets as `${secret:org/path:key}`. Patterns are
proven in `semver-tags`, `linkkeys`, and `longhouse`.

### B.1 Mapping current workflows → reactorcide jobs

| Today (`.github/workflows`) | reactorcide job(s) |
|---|---|
| `pull_request.yaml` (kind test, `go test ./...`) | `test-server.yaml` |
| `pr_protos.yaml` (buf lint/breaking + regenerate) | `csil-gen-check.yaml` (validate + regenerate-and-diff) |
| `pr_helm_validation.yaml` (helm lint/template) | `helm-validate.yaml` |
| `validate-pr-title` + semantic checks | `conventional-commits.yaml` (copy from longhouse/linkkeys) |
| `release.yaml` orchestration (protos→corndogs→helm) | a `release-*` set triggered on `pull_request_merged` / version bump |
| `protos_release.yaml` (semver-tags, bump pyproject, PyPI publish) | *(deferred)* — clients just live in `clients/<lang>/`; a `release-clients.yaml` is added only if/when we decide to publish them |
| `corndogs_release.yaml` (semver-tags, build+push Quay image, bump appVersion) | `release-server.yaml` |
| `helm_release.yaml` + `upload_chart.yaml` (package + upload chart) | `release-helm.yaml` |
| *(none — new)* runner image with Go+Postgres+csilgen baked in | `build-test-env.yaml` (+ `images/corndogs-test-env/Dockerfile`) |

### B.2 Jobs (implemented — `.reactorcide/jobs/` is the source of truth)

These are written and live in `.reactorcide/jobs/`; descriptions here are the
intent, not a duplicate spec.

- **`build-test-env.yaml`** — builds and pushes the `corndogs-test-env` runner
  image (`docker` capability → `docker build` + `crane push`, buildkit fallback)
  to `containers.catalystsquad.com/public/catalystcommunity/corndogs-test-env`.
  The image bakes Go + PostgreSQL + csilgen so the PR jobs need no per-run
  apt/sudo and are uid-agnostic (write only to `/tmp`), which lets them run the
  same under `run-local` and on a worker.
- **`test-server.yaml`** — on the test-env image: start a user-owned Postgres in
  `/tmp` (trust auth, `127.0.0.1:5432`), build the server, run it (auto-migrates),
  wait on `:5080`, `go test ./...`. (Replaces the kind path. The integration
  tests still speak gRPC until the A3/A4 CBOR cutover; only the client transport
  inside them changes then.)
- **`csil-gen-check.yaml`** — on the test-env image (csilgen baked): `csilgen
  validate` + `./csil/generate.sh` + `git diff --exit-code -- clients/` (stale-
  clients guard).
- **`helm-validate.yaml`** — `helm lint`/`template` on chart changes.
- **`conventional-commits.yaml`** — fork-safe commit lint, then `runnerlib
  trigger` of test-server + csil-gen-check + helm-validate.
- **`release-server.yaml`** + **`release-helm.yaml`** (+ `scripts/`) — two
  independent jobs, each runs `semver-tags run --directories <component>` on merge.
  semver-tags emits **prefixed, per-component tags** (`corndogs/vX.Y.Z`,
  `helm_chart/vX.Y.Z`) — separate sequences, so the two jobs never collide even
  running in parallel (no orchestration needed). server builds+pushes the image to
  `containers.catalystsquad.com/public/catalystcommunity/corndogs` and bumps Helm
  appVersion; helm bumps the chart `version`, packages, publishes the chart
  release. Both push their Chart.yaml line back to main with a rebase-retry. GitHub
  release via `gh` with `${secret:catalystcommunity/ci:githubpat}`. (The `VERSION`
  file is not CI-managed.)
- **Client publishing deferred** — clients live committed in `clients/<lang>/`;
  no `release-clients.yaml` yet (add per-registry jobs if/when we publish).

### B.3 Cutover (Migration B)
1. Add `.reactorcide/jobs/` + `.reactorcide/jobs/scripts/` alongside the existing
   `.github/workflows/` (both live for one or two PRs).
2. Register the corndogs project with the reactorcide coordinator (webhook +
   project config: `target_branches: [main]`, the event allow-list, default
   image/timeout) per `reactorcide/docs/github-webhook-setup.md`.
3. Validate locally first with `reactorcide run-local --job-dir ./ ./.reactorcide/jobs/<job>.yaml`.
4. Once green in parallel, delete `.github/workflows/` and the `.github/scripts/`
   that the jobs replaced.

### B.4 Secrets to set in the reactorcide secret store
- `catalystcommunity/ci:githubpat` — replaces `AUTOMATION_PAT` (releases, push-back).
- `catalystcommunity/registry:user` / `:password` — credentials for
  `containers.catalystsquad.com` (replaces the old `QUAY_DOCKER_REGISTRY_*`; new
  registry, so new creds, not the Quay ones).
- `catalystcommunity/...:pypitoken` (and any npm/crates tokens) — only needed
  later, if/when we publish the `clients/<lang>/` packages.

These must exist in the **local** store (for `run-local`) and on the
reactorcide.catalystsquad.com deployment once this repo is onboarded there.

### B.5 Local verification status (`reactorcide run-local`)
- `helm-validate` — **verified green** via `run-local` (exit 0).
- `generate.sh` — verified locally: idempotent, clean re-diff, all four clients
  generate (csilgen `ccc3f02`).
- `test-server` / `csil-gen-check` — now run on the `corndogs-test-env` image
  (Go+Postgres+csilgen baked, uid-agnostic, writes only to `/tmp`), so they are
  designed to run identically under `run-local` and on a worker. **Not yet
  re-run** because the image must be built+published first (`build-test-env`) or
  built locally. Earlier `run-local` attempts on stock images surfaced and fixed
  three real issues: the `docker` capability is BuildKit-for-builds (no docker
  CLI / sibling containers); `/dev/tcp` needs an explicit `bash -c` (runner uses
  `sh`); and run-local-as-host-uid vs worker-as-1001 breaks `sudo`/HOME (the
  reason the baked image went the no-sudo, `/tmp`-only route — and the subject of
  `reactorcide/run-local-user-consistencies.md`).
- `build-test-env`, `conventional-commits`, `release-*` — need `docker`
  capability / secrets / `disable_run_local`; validate on a worker.

---

## Sequencing & dependencies

```
A1 contract+gen ✓ ─┬─> A2 clients ✓ (gen) → transports (TS ✓; go/rust/python TODO) ─┐
                   └─> A3 server edge on HTTP+CBOR (csilgen resolved → unblocked) ───┼─> A4 cutover ─> delete protos/
                                                                                     │
B (reactorcide) ✓ jobs+image authored; runs in parallel; publishing deferred ───────┘
```

**Status: the conversion is implemented and verified end-to-end.**
- **A1 ✓** CSIL contract + `generate.sh` (clients + server types, pinned csilgen).
- **A2 ✓** All four clients generated **and** given CBOR transports — TS
  (`cbor-x`), Go (`fxamacker/cbor`), Rust (`reqwest`+`ciborium`), Python
  (`urllib`+`cbor2`). Verified: `go build`, `cargo check`, Python logic.
- **A3 ✓** Go server rebuilt on `net/http`+CBOR (`POST /v1alpha1/corndogs/{Method}`),
  `/healthz`, promhttp metrics; implementations/store/conversions/cmd swapped off
  protobuf onto the generated `api` types. `go build` + `go vet` clean.
- **A4 ✓** Integration tests ported to a CBOR client and **pass end-to-end**
  against a real Postgres (`go test ./test/...` → ok; `/healthz` → 200). Dockerfile
  drops `grpc_health_probe`; helm probes → `httpGet /healthz`; `protos/` and the
  GitHub Actions workflows/scripts removed.
- **Remaining (operational, not code):** onboard corndogs to the
  reactorcide.catalystsquad.com deployment (webhook + project) so the
  `.reactorcide/jobs` run; build+publish the `corndogs-test-env` image; decide
  client-package publishing. Nothing is committed yet.

## Decisions made
- **Clients live in `clients/<lang>/`**, committed, generated by `csil/generate.sh`.
  `csil-gen-check.yaml` regenerates and fails on a diff (stale-clients guard).
- **No major version bump** — corndogs is pre-1.0; the gRPC→CBOR break ships as a
  normal pre-1.0 bump with a loud changelog entry + `pycorndogs` migration note.
- **Client publishing is deferred** — packages may be published later; not a
  cutover blocker and no release job for it yet.
- **`test-server` Postgres** is a user-owned cluster in `/tmp` (initdb/pg_ctl,
  trust auth) inside the baked `corndogs-test-env` image — no kind, no sidecar, no
  `docker run` (reactorcide's `docker` capability is BuildKit-for-builds, not a
  docker CLI). Jobs write only to `/tmp` so they're uid-agnostic.
- **`corndogs-test-env` image** bakes Go + Postgres + csilgen (pinned rev), built
  by `build-test-env.yaml`, published to the public catalyst registry. It exists
  to keep PR jobs fast, sudo-free, and runnable under both `run-local` and a
  worker.
- **No corndogs job needs run-local uid parity.** reactorcide keeps host-uid as
  the run-local default (parity is opt-in; see
  `reactorcide/run-local-user-consistencies.md`). Because the image removes the
  `sudo` need — the only pull toward the worker uid — every job runs as the
  default uid: `test-server` writes only to `/tmp`; `csil-gen-check` and
  `helm-validate` write into the mounted tree and so *must* stay on the host-uid
  default (they carry a comment not to opt into parity).
- **Image registry:** publish to `containers.catalystsquad.com/public/catalystcommunity/corndogs`
  (moves off Quay, consolidating with the reactorcide-native repos). Creds from
  `${secret:catalystcommunity/registry:user|password}`.

## Open decisions
1. **Distribution targets (only when we decide to publish):** Rust → crates.io or
   internal registry? TS → public npm or internal? Determines the future
   `release-clients.yaml` secrets.
---

## Appendix — files touched

- **New (done):** `csil/corndogs.csil`, `csil/generate.sh`,
  `clients/<lang>/gen/**` (generated, committed) + `clients/typescript/`
  hand-written transport, `clients/README.md`, `.reactorcide/jobs/*.yaml`,
  `.reactorcide/jobs/scripts/*.sh`, `.reactorcide/images/corndogs-test-env/Dockerfile`.
- **New (remaining):** `clients/{go,rust,python}/` CBOR transports, new HTTP/CBOR
  server edge under `corndogs/server/`.
- **Changed:** `corndogs/Dockerfile` (drop grpc_health_probe), `helm_chart/chart`
  (HTTP port + `/healthz` probes), `corndogs/test/**` (CBOR client), `corndogs/server/server.go`
  + `implementations/*` (HTTP mux, typed handlers, promhttp).
- **Removed (end state):** `protos/**`, `buf.*`, `.github/workflows/**`,
  `.github/scripts/install_and_buf_generate.sh` and release scripts.
- **In csilgen repo:** four request files in `docs/csilgen-requests/` (filed).
