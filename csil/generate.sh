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
# Requires `csilgen` on PATH. Regenerate with the SAME csilgen binary CI uses —
# the corndogs-test-env image baked at the pinned rev — e.g.:
#   docker run --rm -v "$PWD:/src" -w /src --entrypoint bash \
#     containers.catalystsquad.com/public/catalystcommunity/corndogs-test-env:latest \
#     -lc 'HOME=/home/runner ./csil/generate.sh'
set -euo pipefail

CSILGEN_REV="19bd3c2"   # csilgen git rev this output was generated against (matches the image's ARG CSILGEN_REF)

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
for lang in "${LANGUAGES[@]}"; do
  echo "=== package ${lang}-client -> clients/${lang} ==="
  rm -rf "${OUT:?}/${lang}"
  mkdir -p "${OUT}/${lang}"
  csilgen generate --input "${SPEC}" --target "${lang}-client" --output "${OUT}/${lang}"
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
