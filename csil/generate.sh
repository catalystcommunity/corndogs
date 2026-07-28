#!/usr/bin/env bash
#
# Generate corndogs CSIL-RPC clients from csil/corndogs.csil into clients/<lang>/,
# and the server-side service interface + types into corndogs/server/csilapi.
#
# The wire is CSIL-RPC (csilgen docs/csil-rpc-transport.md): deterministic CBOR
# envelopes (CsilRpcRequest/CsilRpcResponse) with tag-24-wrapped payloads, over the
# envelope-in-body HTTP profile at POST /csil/v1/rpc. Generated clients are
# transport-agnostic (they call a Transport seam); a per-language CSIL-RPC HTTP
# carrier wires them to the wire (see clients/<lang>/ support files / README).
#
# Pinned to a known-good csilgen revision so codegen is reproducible (csilgen is
# alpha). Bump CSILGEN_REV deliberately and re-run; the reactorcide
# `csil-gen-check` job fails if committed output drifts from a fresh run.
#
# Requires `csilgen` on PATH, built from CSILGEN_REV below. CI's csil-gen-check builds
# csilgen at exactly that rev on a stock rust image (not a baked binary), so the rev
# here is the single source of truth — bump it and regenerate. Locally, build+install
# csilgen at the same rev:
#   git -C <csilgen> checkout <CSILGEN_REV> && cargo build -p csilgen --release \
#     && cargo run -p xtask install-wasm   # then `csilgen` on PATH + ./csil/generate.sh
set -euo pipefail

CSILGEN_REV="abac53b"   # csilgen git rev this output was generated against (matches the image's ARG CSILGEN_REF)

# CSIL-RPC client languages, each emitted as a complete, self-contained, publishable
# PACKAGE (csilgen emit_packages, set in the spec): generated surfaces + codec + a
# genquickstart.md whose copy-paste carriers (all three transports, built on the
# transports/<lang> reference lib) are how a consumer wires the byte carrier. We no
# longer hand-write carriers per language — the genquickstart is their home. Go is
# handled separately below (it also carries the server surface + a real carrier the
# corndogs server's tests use).
LANGUAGES=(rust typescript python java csharp c swift kotlin zig ocaml elixir ruby dart)

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${HERE}/.." && pwd)"
SPEC="${HERE}/corndogs.csil"
OUT="${ROOT}/clients"

if ! command -v csilgen >/dev/null 2>&1; then
  echo "error: csilgen not found on PATH (rev ${CSILGEN_REV} expected)" >&2
  exit 1
fi

echo "=== validating ${SPEC} ==="
csilgen validate --input "${SPEC}"

# Each clients/<lang>/ IS the generated package (manifest + sources + genquickstart.md),
# wholly owned by csilgen and rewritten each run — there are no hand-written files to
# preserve (carriers retired; manifests are generated). The csil-gen-check job diffs
# clients/ after running this, so output must be deterministic.
# Corndogs SHIPS its own client transports (CSIL-RPC over TCP + a heartbeat) and its
# own README examples, per language, so users get a working client out of the box
# without reading csilgen docs. Those hand-written files live inside each generated
# package and are PRESERVED across regeneration (csilgen owns only the *.gen/codec/
# types/genquickstart surfaces). List each language's Corndogs-maintained files here
# as they are added; they are backed up before the package is rewritten and restored
# after. (csil-gen-check stays clean because these files don't change on regen.)
declare -A CORNDOGS_CARRIERS
CORNDOGS_CARRIERS[python]="corndogs/transport.py corndogs/transport_async.py README.md"
CORNDOGS_CARRIERS[typescript]="transport.ts README.md package.json"
CORNDOGS_CARRIERS[rust]="src/transport.rs README.md src/lib.rs"
CORNDOGS_CARRIERS[csharp]="Transport.cs TransportAsync.cs README.md"
CORNDOGS_CARRIERS[dart]="lib/transport.dart README.md"
CORNDOGS_CARRIERS[java]="src/main/java/csilgen/generated/TcpTransport.java README.md"
CORNDOGS_CARRIERS[kotlin]="src/main/kotlin/community/catalyst/csilgen/generated/TcpTransport.kt README.md"
CORNDOGS_CARRIERS[ruby]="lib/transport.rb README.md"
CORNDOGS_CARRIERS[elixir]="lib/transport.ex README.md"
CORNDOGS_CARRIERS[c]="transport.h transport.c README.md"
CORNDOGS_CARRIERS[zig]="transport.zig README.md"
CORNDOGS_CARRIERS[ocaml]="lib/transport.ml README.md lib/dune"
CORNDOGS_CARRIERS[swift]="Sources/Corndogs/Transport.swift README.md"
# NOTE: three languages need a one-line edit to an otherwise-generated file to
# register the carrier — rust `src/lib.rs` (adds `pub mod transport;`), ocaml
# `lib/dune` (adds unix + threads.posix libs), typescript `package.json` (adds the
# `@types/node` devDep + `./transport` export). Those files are preserved here too,
# so Corndogs owns them; regeneration for a fixed spec is deterministic, so the
# preserved copies stay in sync. If the spec's module/package shape ever changes,
# reconcile those three files by hand.

for lang in "${LANGUAGES[@]}"; do
  echo "=== package ${lang}-client -> clients/${lang} ==="
  bak="$(mktemp -d)"
  for f in ${CORNDOGS_CARRIERS[$lang]:-}; do
    if [ -f "${OUT}/${lang}/${f}" ]; then
      mkdir -p "$(dirname "${bak}/${f}")"
      cp "${OUT}/${lang}/${f}" "${bak}/${f}"
    fi
  done
  rm -rf "${OUT:?}/${lang}"
  mkdir -p "${OUT}/${lang}"
  csilgen generate --input "${SPEC}" --target "${lang}-client" --output "${OUT}/${lang}"
  for f in ${CORNDOGS_CARRIERS[$lang]:-}; do
    if [ -f "${bak}/${f}" ]; then
      mkdir -p "$(dirname "${OUT}/${lang}/${f}")"
      cp "${bak}/${f}" "${OUT}/${lang}/${f}"
    fi
  done
  rm -rf "${bak}"
done

# Go is the one package corndogs consumes itself: the `go` target emits ALL surfaces
# (CorndogsService interface the server implements + CorndogsClient + types + codec) as a
# real module (emit_packages → go.mod at the package_name path), so the corndogs server
# depends on it via go.mod require+replace, exactly like an external `go get` consumer.
# Unlike the other languages it keeps a hand-written carrier (transport.go) + E2E test
# alongside the generated files, so we remove only generated artifacts here.
GO_LIB="${OUT}/corndogs"
echo "=== package go (all surfaces) -> clients/corndogs ==="
mkdir -p "${GO_LIB}"
rm -f "${GO_LIB}"/*.gen.go "${GO_LIB}/go.mod" "${GO_LIB}/genquickstart.md"
csilgen generate --input "${SPEC}" --target go --output "${GO_LIB}"

echo "=== done; every client packaged under ${OUT}/ (Go module at ${GO_LIB}) ==="
