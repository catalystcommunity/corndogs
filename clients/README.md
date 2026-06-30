# corndogs clients

Generated from [`../csil/corndogs.csil`](../csil/corndogs.csil) by
[`../csil/generate.sh`](../csil/generate.sh) (csilgen, pinned rev in that script).

Every language here is a **complete, self-contained, publishable package** named
`corndogs` — generated types, codec, the typed client (and server surface), and a
generated **`genquickstart.md`** with copy-paste examples. There is nothing
hand-written to wire up (one exception: Go, below).

## Layout

```
clients/<lang>/                 <- the package itself: manifest + generated sources + genquickstart.md
clients/<lang>/genquickstart.md <- copy-paste Quickstart (carrier + a call) for all three CSIL transports
```

Each package is **wholly owned by csilgen** — wiped and rewritten on every run; do
not edit it. Edit the CSIL and regenerate. The `csil-gen-check` reactorcide job
re-runs `generate.sh` and fails if committed output drifts.

The package name is `corndogs` in every ecosystem (npm, PyPI, crates.io, RubyGems,
NuGet, pub.dev, Hex, opam — the name is free on all of them). The Go module keeps
its full, `go get`-able path (`github.com/CatalystCommunity/corndogs/clients/corndogs`).

## The wire

CSIL-RPC (csilgen `docs/csil-rpc-transport.md`), envelope-in-body HTTP profile:
each call POSTs a `CsilRpcRequest` to `POST {baseUrl}/csil/v1/rpc` and reads a
`CsilRpcResponse`, both `application/cbor`. The operation's request/response rides
as a **tag-24-wrapped** (`#6.24(bstr)`) CBOR payload; `service` is the lowercased
service name (`corndogs`), `op` is the operation as written in CSIL (e.g.
`SubmitTask`). Application errors ride as a typed `ServiceError` arm (`status: 0`,
`variant: "ServiceError"`), distinct from transport failures (non-zero `status`).
The generated client is **transport-agnostic** — it calls a `Transport`/carrier
seam and contains no wire code.

## Using a client

1. Install the package (`corndogs`) from its `clients/<lang>/` directory.
2. Open that package's **`genquickstart.md`** and copy the carrier + example for
   your transport (CSIL-RPC / Events / Datagrams).
3. Change the host/port and run.

The carrier is the one small host-specific piece — it moves bytes (envelope + your
HTTP/TLS/UDP client of choice) while the generated codec owns (de)serialization.
The Quickstart carriers build on csilgen's dependency-free `transports/<lang>`
reference library (vendor it or add a `replace`/path until it's published).

## Go: consumed by the corndogs server too

`clients/corndogs` (Go) is special: it carries **both** surfaces — the
`CorndogsService` interface the corndogs server implements **and** the
`CorndogsClient` a Go caller uses — and the corndogs server depends on it via a
`go.mod` `require` + `replace`, exactly like an external `go get` consumer
(dogfooding the package). It also ships a real, dependency-free carrier
(`transport.go`, reusing the generated codec's CBOR) plus an end-to-end test, so a
Go client works by `import` + `corndogs.New(baseURL)` with no copy-paste:

```go
import corndogs "github.com/CatalystCommunity/corndogs/clients/corndogs"

c := corndogs.New("http://corndogs.example.com")
resp, err := c.SubmitTask(ctx, corndogs.SubmitTaskRequest{Queue: "q", Priority: 0})
```

## Testing

`run-tests.sh` is the multi-language analogue of the Go E2E test: for each package it
extracts the CSIL-RPC carrier + call from that package's generated `genquickstart.md`
(the exact code we ship), wires it to the committed package and the csilgen
`transports/<lang>` reference lib, and runs it against a live corndogs server. A passing
`SubmitTask` exercises the whole stack: generated client → codec → carrier → transport →
HTTP → server.

```sh
clients/install-transport-toolchains.sh   # one-time: set up the shared catalyst-tools bundle (~/.local/catalyst-tools)
source ~/.local/catalyst-tools/env.sh      # put the toolchains on PATH (see ../../CLAUDE.md)
clients/run-tests.sh                        # builds+starts a local server if CORNDOGS_E2E is unset
```

- Toolchains come from the shared **catalyst-tools** bundle (zig, jdk17 + gradle [Kotlin
  builds via Gradle], dotnet, dart, go, node, opam/ocaml; Ruby/Elixir are system installs,
  Rust is the host rustup), so every Catalyst multi-language repo bootstraps the same way.
- `transports/<lang>` is fetched via **git** from GitHub at run time (never vendored).
  Every language can consume the cloned source: Go/Rust/Python/Ruby/Dart/Elixir/OCaml/Zig
  use a path/module dep, and the JVM/.NET ones (Java/Kotlin/C#) — whose package managers
  can't take a git *URL* as a dependency — compile/reference the cloned **sources**
  directly (`javac`/`kotlinc`/ProjectReference), same as C.
- Languages without a toolchain are SKIPPED, not failed (Swift has no Linux toolchain;
  Elixir needs an Erlang built with `:inets`). Set `CORNDOGS_E2E=<url>` to target a
  running server; `LANGS="go python"` to run a subset.

## Regenerate

```sh
# csilgen must be on PATH (the corndogs-test-env image bakes it in at the pinned rev)
../csil/generate.sh
```
